package engine

import (
	"context"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
)

// Multi-gateway aggregation.
//
// A device transmission is heard by every gateway in range, and the forwarder on
// each of them sends its copy to the server. Those copies are not noise to be
// discarded: together they say the device is reachable from several directions,
// and the copy with the best signal-to-noise is the one worth answering through.
// Dropping the second copy, which is what a server that deduplicates by
// discarding does, throws away both the coverage information and the better
// downlink path.
//
// So the first copy of a frame opens a collection window, and the frame is
// processed once with everything that arrived inside it. The window is short —
// ChirpStack's 200 ms, which this mirrors so a deployment tuned against it sees
// the same behaviour — because it is paid on every uplink, and because the RX1
// window opens one second after the device transmitted.
//
// Backhaul skew between gateways does not threaten the downlink timing, and it is
// worth being precise about why: the Semtech downlink timestamp is an absolute
// concentrator counter value derived from the chosen copy's own reception, so a
// gateway whose packet arrived late still transmits one second after *it* heard
// the frame. What the window does cost is dispatch slack — the PULL_RESP has to
// leave the server before that timestamp arrives — and remainingSlack below
// surfaces the case where it would not.

// DefaultAggregationDelay is how long the server waits for further copies.
// Matching ChirpStack's network.deduplication_delay keeps the two servers
// comparable when a deployment is being migrated between them.
const DefaultAggregationDelay = 200 * time.Millisecond

// collectionOutcome is what HandleUplink should do with a copy, as decided by
// startCollection in a single critical section.
type collectionOutcome int

const (
	// collectionOpened means this copy is the first of the frame and the caller
	// owns the processing that closes the window.
	collectionOpened collectionOutcome = iota
	// collectionJoined means a window was already open and this copy has been
	// folded into it; the copy that opened it will answer.
	collectionJoined
	// collectionRejected means the frame was already answered inside the dedup
	// window, so this is a late copy and there is nothing to gather.
	collectionRejected
)

// startCollection either opens a window for a frame, folds a copy into a window
// already open, or rejects the copy as a late duplicate.
//
// The three decisions are made under one lock because splitting them is how this
// goes wrong: a copy that checks "is a window open?" and then "has this been
// answered?" can find neither true — the first copy has won the cross-process
// claim but not yet opened its window — and conclude it is a duplicate, which
// silently loses the second gateway's reception, or worse, open a second window
// and answer the device twice.
func (e *Engine) startCollection(key string, frame gateway.UplinkFrame) collectionOutcome {
	now := e.now()

	e.mu.Lock()
	defer e.mu.Unlock()

	if c, ok := e.collecting[key]; ok {
		c.frames = append(c.frames, frame)
		return collectionJoined
	}
	if at, ok := e.seen[key]; ok && now.Sub(at) < e.cfg.DedupWindow {
		return collectionRejected
	}
	e.collecting[key] = &collection{frames: []gateway.UplinkFrame{frame}}
	return collectionOpened
}

// collection is the set of copies gathered for one frame.
type collection struct {
	frames []gateway.UplinkFrame
}

// take removes and returns the collected copies of a frame.
func (e *Engine) take(key string) []gateway.UplinkFrame {
	e.mu.Lock()
	defer e.mu.Unlock()

	c, ok := e.collecting[key]
	if !ok {
		return nil
	}
	delete(e.collecting, key)
	return c.frames
}

// markProcessed records that a frame has been answered, so copies arriving after
// the window closes are rejected cheaply instead of each opening a window and
// spending a database round trip to discover the same thing.
//
// It writes the same book isDuplicate reads, deliberately: one notion of "we have
// dealt with this frame" is one less way for the two paths to disagree.
func (e *Engine) markProcessed(key string) {
	now := e.now()

	e.mu.Lock()
	defer e.mu.Unlock()

	e.seen[key] = now
	if len(e.seen) > 4096 {
		for k, at := range e.seen {
			if now.Sub(at) >= e.cfg.DedupWindow {
				delete(e.seen, k)
			}
		}
	}
}

// startAggregation opens the window and schedules the single processing that
// closes it.
func (e *Engine) startAggregation(ctx context.Context, key string, frame gateway.UplinkFrame) {
	delay := e.cfg.AggregationDelay

	// A zero delay means the operator asked for the old behaviour: answer the
	// first copy and discard the rest. It is worth keeping reachable because it
	// is the only way to run with a receiver that cannot tolerate the extra
	// latency, and because it makes aggregation testable by contrast.
	if delay <= 0 {
		if err := e.processAggregated(ctx, key, []gateway.UplinkFrame{frame}); err != nil {
			e.log.Error("aggregated uplink processing failed", "gateway", frame.GatewayID, "err", err)
		}
		return
	}

	e.scheduleFn(delay, func() {
		// The collection outlives the request that opened it, so it must not
		// inherit that request's cancellation.
		fresh := context.WithoutCancel(ctx)
		frames := e.take(key)
		if len(frames) == 0 {
			return
		}
		if err := e.processAggregated(fresh, key, frames); err != nil {
			e.log.Error("aggregated uplink processing failed",
				"gateway", frames[0].GatewayID, "copies", len(frames), "err", err)
		}
	})
}

// processAggregated merges the copies and hands the frame to the protocol
// handlers exactly once.
func (e *Engine) processAggregated(ctx context.Context, key string, frames []gateway.UplinkFrame) error {
	merged := mergeReceptions(frames)
	if len(frames) > 1 {
		e.log.Debug("aggregated a frame received by more than one gateway",
			"copies", len(frames),
			"chosen", merged.GatewayID,
			"gateways", receptionGateways(frames))
	}
	return e.handleFrame(ctx, merged)
}

// mergeReceptions folds the copies into the single frame the engine processes.
//
// The chosen copy is the one with the best signal-to-noise ratio, with RSSI as
// the tiebreak. SNR rather than RSSI because SNR is what determines whether a
// frame decodes and what the data-rate decision is made against; two gateways can
// disagree about RSSI for reasons that have nothing to do with link quality, such
// as antenna gain or a differently calibrated concentrator.
func mergeReceptions(frames []gateway.UplinkFrame) gateway.UplinkFrame {
	best := frames[0]
	receptions := best.AllReceptions()
	for _, f := range frames[1:] {
		receptions = append(receptions, f.AllReceptions()...)
		if better(f, best) {
			best = f
		}
	}

	// The chosen copy first, so a reader can take index zero and get the gateway
	// the downlink went through, then the rest behind it.
	chosen := receptionOf(best)
	ordered := make([]gateway.Reception, 0, len(receptions))
	ordered = append(ordered, chosen)
	for _, r := range receptions {
		if r.GatewayID != chosen.GatewayID || r.UplinkID != chosen.UplinkID {
			ordered = append(ordered, r)
		}
	}

	merged := best
	merged.Receptions = ordered
	return merged
}

// better reports whether a is the preferable copy to answer through.
func better(a, b gateway.UplinkFrame) bool {
	if a.SNR != b.SNR {
		return a.SNR > b.SNR
	}
	return a.RSSI > b.RSSI
}

func receptionOf(f gateway.UplinkFrame) gateway.Reception {
	return f.AllReceptions()[0]
}

func receptionGateways(frames []gateway.UplinkFrame) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		out = append(out, f.GatewayID.String())
	}
	return out
}

// maxReceptionSNR is the best signal-to-noise ratio across every copy, which is
// what the adaptive data-rate decision and the LinkCheckAns margin are computed
// from. A device on the edge of two gateways should be credited with the link it
// actually has rather than with whichever gateway's packet arrived first; using
// the first copy's SNR would push such a device to a slower data rate than it
// needs.
func maxReceptionSNR(frame gateway.UplinkFrame) float64 {
	receptions := frame.AllReceptions()
	best := receptions[0].SNR
	for _, r := range receptions[1:] {
		if r.SNR > best {
			best = r.SNR
		}
	}
	return best
}

// remainingSlack is how much of a receive-window delay is left to dispatch in,
// measured from the arrival of the copy the downlink is anchored to. Aggregation
// spends part of it, and a slow backhaul can spend all of it; the caller uses
// this to say so rather than silently transmitting late.
func (e *Engine) remainingSlack(frame gateway.UplinkFrame, planDelay time.Duration) time.Duration {
	arrival := frame.ReceivedAt
	if len(frame.Receptions) > 0 {
		arrival = frame.Receptions[0].ReceivedAt
	}
	if arrival.IsZero() {
		return planDelay
	}
	return planDelay - e.now().Sub(arrival)
}
