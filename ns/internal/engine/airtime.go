package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// SubBandUsage is one row of the status report.
type SubBandUsage struct {
	Gateway   string        `json:"gateway"`
	Region    string        `json:"region"`
	SubBand   string        `json:"sub_band"`
	Used      time.Duration `json:"used"`
	Budget    time.Duration `json:"budget"`
	Window    time.Duration `json:"window"`
	Transmits uint64        `json:"transmits"`
}

// usage reports current occupancy per gateway and sub-band. The limits are
// recomputed from the region tables rather than stored alongside the ledger, so
// the report cannot drift from what enforcement actually applies.
func (b *airtimeBudget) usage() []SubBandUsage {
	now := b.now()

	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]SubBandUsage, 0, len(b.records))
	for key, list := range b.records {
		var used time.Duration
		var live uint64
		for _, e := range list {
			if e.start.Before(now.Add(-b.window)) {
				continue
			}
			used += e.airtime
			live++
		}
		if used == 0 {
			continue
		}
		reg, err := region.New(key.region)
		if err != nil {
			continue
		}
		sub, ok := reg.SubBandFor(key.minHz)
		if !ok {
			continue
		}
		out = append(out, SubBandUsage{
			Gateway:   key.gateway.String(),
			Region:    key.region,
			SubBand:   sub.String(),
			Used:      used.Round(time.Millisecond),
			Budget:    sub.Budget(b.window),
			Window:    b.window,
			Transmits: live,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Gateway != out[j].Gateway {
			return out[i].Gateway < out[j].Gateway
		}
		return out[i].SubBand < out[j].SubBand
	})
	return out
}

// denialCounts copies the refusal counters, for the status endpoint.
func (b *airtimeBudget) denialCounts() map[string]uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]uint64, len(b.denied))
	for k, v := range b.denied {
		out[k] = v
	}
	return out
}

// enforceDutyCycle drops the planned windows the band's limits cannot afford
// and books the ones it can.
//
// Both windows are accounted separately, and that is not double-counting: the
// network genuinely transmits in each window it uses, and each transmission
// occupies its own channel. It is also what makes the filter useful — when the
// 868 MHz channels have run out of budget, RX1 falls away and RX2 on
// 869.525 MHz, which carries ten times the duty cycle, keeps the device
// reachable.
//
// A class-A window cannot be moved to a better moment: the device opens it at a
// fixed delay after its own uplink. So a refusal is final for this frame, and
// the answer to the operator is the event, not a retry.
func (e *Engine) enforceDutyCycle(ctx context.Context, reg region.Region,
	gatewayID lorawan.EUI64, plans []txPlan, frameLen int, rec DownlinkRecord) []txPlan {

	if e.cfg.DisableDutyCycleEnforcement || e.budget == nil {
		return plans
	}
	start := e.now()

	allowed := make([]txPlan, 0, len(plans))
	for _, plan := range plans {
		perm := e.budget.reserve(gatewayID, reg, plan.FrequencyHz,
			plan.DataRateIndex, frameLen, start.Add(plan.Delay))
		if perm.Allowed {
			e.log.Debug("duty-cycle budget allows the downlink",
				"gateway", gatewayID, "window", plan.Window.String(),
				"airtime", perm.Airtime.Round(time.Millisecond), "reason", perm.Reason)
			allowed = append(allowed, plan)
			continue
		}

		detail := fmt.Sprintf("%s at %s: %s", plan.Window.String(),
			dataRateLabel(reg, plan.DataRateIndex), perm.Reason)
		if perm.RetryAfter > 0 {
			detail += fmt.Sprintf("; budget frees again in %v", perm.RetryAfter.Round(time.Second))
		}
		e.log.Warn("downlink refused by the duty-cycle limit",
			"gateway", gatewayID, "dev_eui", rec.DevEUI,
			"window", plan.Window.String(), "freq_hz", plan.FrequencyHz,
			"airtime", perm.Airtime, "reason", perm.Reason)
		e.recordEvent(ctx, Event{
			Kind:      EventUplinkDrop,
			Reason:    ReasonDutyCycleDenied,
			DevEUI:    rec.DevEUI,
			DevAddr:   rec.DevAddr,
			GatewayID: gatewayID,
			FCntUp:    rec.FCntDown,
			Detail:    detail,
		})
	}
	return allowed
}

// DutyCycleUsage reports channel occupancy per gateway and sub-band, for the
// status endpoint.
func (e *Engine) DutyCycleUsage() []SubBandUsage {
	if e.budget == nil {
		return nil
	}
	return e.budget.usage()
}

// DutyCycleDenials reports how many transmissions each regulatory limit has
// refused, for the status endpoint.
func (e *Engine) DutyCycleDenials() map[string]uint64 {
	if e.budget == nil {
		return nil
	}
	return e.budget.denialCounts()
}

// --- durability ---------------------------------------------------------------
//
// The ledger is the server's evidence that it stayed inside the band's limits,
// and a rolling window is only meaningful if it survives the process. A restart
// that forgets the hour's occupancy lets the first downlinks after boot exceed
// an allowance that was already spent — the exact failure the enforcement
// exists to prevent, arriving at the moment an operator is least likely to be
// watching.
//
// What is persisted is the booking, not the verdict: one row per planned
// transmission with its start time and airtime. That keeps the table a
// straightforward record of fact, and it composes across processes — two servers
// sharing a gateway each write their own emissions, so the combined view is more
// conservative than either one alone, which is the correct direction for a
// compliance ledger.

// AirtimeRecord is one booked transmission, in the shape a store persists.
type AirtimeRecord struct {
	Gateway lorawan.EUI64
	Region  string
	// MinHz identifies the regulatory sub-band, taken from the band table
	// rather than the frame's frequency: a sub-band is what the limit is
	// measured against, and storing the frequency would split one budget across
	// every channel inside it.
	MinHz   uint32
	Start   time.Time
	Airtime time.Duration
}

// AirtimeSnapshot copies the bookings still inside the window, for persistence.
func (e *Engine) AirtimeSnapshot() []AirtimeRecord {
	if e.budget == nil {
		return nil
	}
	return e.budget.snapshot()
}

// RestoreAirtime loads the ledger written by this process or a previous one.
//
// It is called explicitly rather than from New because it does I/O: a server
// that cannot read its ledger should say so at startup and carry on enforcing
// from an empty book, not fail to boot.
func (e *Engine) RestoreAirtime(ctx context.Context) (int, error) {
	if e.budget == nil || e.store.Airtime == nil {
		return 0, nil
	}
	records, err := e.store.Airtime.LoadAirtime(ctx, e.budget.window)
	if err != nil {
		return 0, err
	}
	return e.budget.restore(records), nil
}

// PruneAirtime drops bookings that have aged out of the window, so the table a
// flush writes holds only what still constrains a transmission.
func (e *Engine) PruneAirtime() {
	if e.budget == nil {
		return
	}
	e.budget.pruneNow()
}

// AirtimeWindow is the rolling period the limits are measured over.
func (e *Engine) AirtimeWindow() time.Duration {
	if e.budget == nil {
		return DefaultDutyCycleWindow
	}
	return e.budget.window
}

// DedupWindow is how long a handled frame stays claimed. Exposed so the process
// that prunes the shared book can agree with the engine on what "recently" means;
// a mismatch would either drop frames that should have been answered or let the
// table grow without bound.
func (e *Engine) DedupWindow() time.Duration { return e.cfg.DedupWindow }

// snapshot copies every booking still inside the window.
func (b *airtimeBudget) snapshot() []AirtimeRecord {
	now := b.now()
	cutoff := now.Add(-b.window)

	b.mu.Lock()
	defer b.mu.Unlock()

	var out []AirtimeRecord
	for key, emissions := range b.records {
		for _, em := range emissions {
			if em.start.Before(cutoff) {
				continue
			}
			out = append(out, AirtimeRecord{
				Gateway: key.gateway, Region: key.region, MinHz: key.minHz,
				Start: em.start, Airtime: em.airtime,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Gateway != out[j].Gateway {
			return out[i].Gateway.String() < out[j].Gateway.String()
		}
		if out[i].MinHz != out[j].MinHz {
			return out[i].MinHz < out[j].MinHz
		}
		return out[i].Start.Before(out[j].Start)
	})
	return out
}

// restore loads bookings and reports how many were kept. Anything already
// outside the window is dropped on the way in: a server that was down for two
// hours should not inherit a debt the regulation does not ask it to repay.
func (b *airtimeBudget) restore(records []AirtimeRecord) int {
	if len(records) == 0 {
		return 0
	}
	now := b.now()
	cutoff := now.Add(-b.window)

	b.mu.Lock()
	defer b.mu.Unlock()

	kept := 0
	for _, r := range records {
		if r.Start.Before(cutoff) || r.Airtime <= 0 {
			continue
		}
		key := budgetKey{gateway: r.Gateway, region: r.Region, minHz: r.MinHz}
		b.records[key] = append(b.records[key], emission{start: r.Start, airtime: r.Airtime})
		kept++
	}
	// Bookings are stored in whatever order the database returned them, and
	// freeAfterLocked walks the list assuming it is ordered in time.
	for key := range b.records {
		sort.Slice(b.records[key], func(i, j int) bool {
			return b.records[key][i].start.Before(b.records[key][j].start)
		})
	}
	return kept
}

// pruneNow drops expired bookings from every sub-band.
func (b *airtimeBudget) pruneNow() int {
	now := b.now()

	b.mu.Lock()
	defer b.mu.Unlock()

	dropped := 0
	for key, emissions := range b.records {
		before := len(emissions)
		live := emissions[:0]
		for _, em := range emissions {
			if now.Sub(em.start) < b.window {
				live = append(live, em)
			}
		}
		b.records[key] = live
		dropped += before - len(live)
		if len(live) == 0 {
			delete(b.records, key)
		}
	}
	return dropped
}
