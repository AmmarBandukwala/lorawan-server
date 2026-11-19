package engine

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// Duty cycle and dwell-time enforcement on our own transmissions.
//
// The regulation limits how long a transmitter may occupy a channel, and it
// binds whoever schedules the transmission. A network server that plans
// downlinks without tracking airtime therefore puts its gateways out of
// compliance, and gets no feedback when it does: the concentrator either
// transmits anyway or drops the frame with a TX_ACK error that reads like a
// radio fault rather than a policy violation.
//
// The model is a rolling window per gateway per sub-band, which is how the
// limit is written: "1% of the time" measured over an hour. Summing the airtime
// of recent transmissions against a budget is the whole algorithm; the
// complexity here is in reporting *why* a transmission was refused, because an
// operator staring at a silent gap in downlinks needs that answer.
//
// Two deliberate choices:
//
//   - Enforcement is on by default. A gateway may enforce it too, and two
//     conservative layers cost nothing; one permissive layer costs an
//     inspection.
//   - Airtime is booked when the transmission is planned, not when the socket
//     write succeeds. A failed send therefore over-counts slightly, which errs
//     toward compliance. The alternative — book on success — lets concurrent
//     plans all pass the check and then all transmit.

// DefaultDutyCycleWindow is the period a duty cycle is measured over. One hour
// matches the regulation's own wording and spreads traffic; a shorter window
// would let a burst through and then block everything after it.
const DefaultDutyCycleWindow = time.Hour

// airtimeBudget is the rolling-window ledger.
type airtimeBudget struct {
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	records map[budgetKey][]emission
	// denied counts refusals by cause so the status endpoint can show a
	// growing problem instead of leaving it in the logs.
	denied map[string]uint64
}

type budgetKey struct {
	gateway lorawan.EUI64
	region  string
	minHz   uint32
}

// emission is one transmission's channel occupancy.
type emission struct {
	start   time.Time
	airtime time.Duration
}

func newAirtimeBudget(window time.Duration, now func() time.Time) *airtimeBudget {
	if window <= 0 {
		window = DefaultDutyCycleWindow
	}
	if now == nil {
		now = time.Now
	}
	return &airtimeBudget{
		window:  window,
		now:     now,
		records: map[budgetKey][]emission{},
		denied:  map[string]uint64{},
	}
}

// txPermission is the answer to "may this frame go out?".
type txPermission struct {
	Allowed bool
	// Reason explains a refusal, or describes the remaining budget on an
	// approval, for the event log and the status endpoint.
	Reason string
	// Airtime is what the frame costs. It is computed even on a refusal so the
	// caller can report the number that caused it.
	Airtime time.Duration
	// Used is the sub-band occupancy including this frame, when allowed.
	Used   time.Duration
	Budget time.Duration
	// RetryAfter is how long to wait before the frame would fit. A class-A RX
	// window cannot be moved, so the engine logs this rather than rescheduling;
	// it is there to make the log actionable ("available again in 14m").
	RetryAfter time.Duration
}

// reserve asks permission for one transmission and, when granted, books it.
//
// at is when the transmission starts, which for a class-A downlink is the
// uplink time plus the window delay — not "now". Measuring the window against
// the wrong instant would systematically under-count the near future, which is
// exactly the interval a busy gateway runs out of budget in.
func (b *airtimeBudget) reserve(gatewayID lorawan.EUI64, reg region.Region,
	frequencyHz uint32, dataRateIndex, frameLen int, at time.Time) txPermission {

	dr, err := reg.DataRate(dataRateIndex)
	if err != nil {
		return txPermission{Reason: fmt.Sprintf("unknown data rate %d in %s", dataRateIndex, reg.CommonName())}
	}
	airtime, err := region.TimeOnAir(dr, frameLen)
	if err != nil {
		return txPermission{Reason: fmt.Sprintf("cannot compute airtime: %v", err)}
	}
	perm := txPermission{Airtime: airtime}

	sub, ok := reg.SubBandFor(frequencyHz)
	if !ok {
		// A frequency outside every defined range means our channel plan is
		// wrong. Refusing is the only safe reading: treating an unknown range
		// as unlimited is how a server ends up transmitting on a restricted
		// frequency.
		perm.Reason = fmt.Sprintf("%d Hz is outside every %s regulatory range", frequencyHz, reg.CommonName())
		b.countDenial("outside_sub_band")
		return perm
	}

	// Dwell time caps a single transmission regardless of history, so it is
	// checked before the ledger.
	if dwell := reg.DownlinkDwellTime(); dwell > 0 && airtime > dwell {
		perm.Reason = fmt.Sprintf("%.1f ms frame exceeds the %s dwell time of %v",
			float64(airtime)/float64(time.Millisecond), reg.CommonName(), dwell)
		b.countDenial("dwell_time")
		return perm
	}

	budget := sub.Budget(b.window)
	perm.Budget = budget
	if sub.DutyCycle >= 1.0 {
		perm.Allowed = true
		perm.Reason = "no duty-cycle limit in this range"
		return perm
	}
	if airtime > budget {
		// No amount of waiting helps: the frame alone breaks the limit, so it
		// can only go out at a higher data rate or in a friendlier sub-band.
		perm.Reason = fmt.Sprintf("%.1f ms frame exceeds the %.1f ms hourly budget of %s",
			float64(airtime)/float64(time.Millisecond),
			float64(budget)/float64(time.Millisecond), sub)
		b.countDenial("frame_exceeds_budget")
		return perm
	}

	key := budgetKey{gateway: gatewayID, region: reg.CommonName(), minHz: sub.MinHz}
	b.mu.Lock()
	defer b.mu.Unlock()

	used := b.pruneLocked(key, at)
	if used+airtime > budget {
		perm.Reason = fmt.Sprintf("%s holds %.1f s of its %.1f s hourly budget; this frame needs %.1f ms",
			sub, used.Seconds(), budget.Seconds(), float64(airtime)/float64(time.Millisecond))
		perm.RetryAfter = b.freeAfterLocked(key, used, airtime, budget, at)
		// The mutex is already held here, and sync.Mutex is not reentrant: a
		// locking helper called from this path deadlocks on the first denial.
		b.countDenialLocked("duty_cycle")
		return perm
	}

	b.records[key] = append(b.records[key], emission{start: at, airtime: airtime})
	perm.Allowed = true
	perm.Used = used + airtime
	perm.Reason = fmt.Sprintf("%.1f s of %.1f s used after this frame", perm.Used.Seconds(), budget.Seconds())
	return perm
}

// pruneLocked drops emissions that have left the window and returns the
// occupancy still inside it.
func (b *airtimeBudget) pruneLocked(key budgetKey, at time.Time) time.Duration {
	list := b.records[key]
	cutoff := at.Add(-b.window)
	kept := list[:0]
	var used time.Duration
	for _, e := range list {
		if e.start.Before(cutoff) {
			continue
		}
		kept = append(kept, e)
		used += e.airtime
	}
	// Re-slicing in place reuses the backing array; entries past the new length
	// are unreachable and harmless.
	b.records[key] = kept
	return used
}

// freeAfterLocked works out how long to wait until the frame fits, by walking
// the ledger from the oldest entry: the wait ends when the first emission leaves
// the window and frees its occupancy.
func (b *airtimeBudget) freeAfterLocked(key budgetKey, used, airtime, budget time.Duration, at time.Time) time.Duration {
	list := b.records[key]
	sort.Slice(list, func(i, j int) bool { return list[i].start.Before(list[j].start) })

	remaining := used
	for _, e := range list {
		if remaining+airtime <= budget {
			break
		}
		remaining -= e.airtime
		if wait := e.start.Add(b.window).Sub(at); wait > 0 {
			return wait
		}
	}
	if remaining+airtime > budget {
		// Nothing left to evict, so the frame does not fit even with an empty
		// window. The caller already handles that case, making this a
		// belt-and-braces answer rather than an expected path.
		return b.window
	}
	return 0
}

// countDenial records a refusal. It takes the lock, so it must not be called
// from a path that already holds it — see countDenialLocked.
func (b *airtimeBudget) countDenial(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.countDenialLocked(reason)
}

// countDenialLocked increments a refusal counter with the mutex held.
func (b *airtimeBudget) countDenialLocked(reason string) {
	b.denied[reason]++
}

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
