package pgstore_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// These tests exercise the shared-state layer that makes a multi-instance
// deployment honest: the de-duplication book and the airtime ledger. Both are
// pure SQL, so a Go-only test cannot tell whether the ON CONFLICT clauses do
// what the comments claim.

// TestClaimFrameAllowsOneWinner is the core guarantee. Two instances seeing the
// same radio frame must produce exactly one answer, and the database is the only
// arbiter available to them.
func TestClaimFrameAllowsOneWinner(t *testing.T) {
	ctx, store := openStore(t)

	key := fmt.Sprintf("ns-test-claim-%d", time.Now().UnixNano())
	const window = time.Minute

	first, err := store.ClaimFrame(ctx, key, window)
	if err != nil {
		t.Fatalf("ClaimFrame (first): %v", err)
	}
	if !first {
		t.Fatal("the first claim of a fresh key was refused")
	}

	second, err := store.ClaimFrame(ctx, key, window)
	if err != nil {
		t.Fatalf("ClaimFrame (second): %v", err)
	}
	if second {
		t.Fatal("the same key was claimed twice inside the window: two instances would both answer")
	}
}

// TestClaimFrameConcurrent is the race the whole design exists for. A frame that
// reaches two gateways arrives at both instances at essentially the same moment,
// so a SELECT-then-INSERT would let both win. Exactly one may.
func TestClaimFrameConcurrent(t *testing.T) {
	ctx, store := openStore(t)

	key := fmt.Sprintf("ns-test-race-%d", time.Now().UnixNano())
	const window = time.Minute
	const racers = 12

	var (
		wg      sync.WaitGroup
		winners atomic.Int32
		start   = make(chan struct{})
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together; serialised claims prove nothing
			claimed, err := store.ClaimFrame(ctx, key, window)
			if err != nil {
				t.Errorf("ClaimFrame: %v", err)
				return
			}
			if claimed {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := winners.Load(); got != 1 {
		t.Errorf("%d of %d concurrent claims succeeded, want exactly 1", got, racers)
	}
}

// TestClaimFrameExpires: a claim must not last forever, or a device's
// legitimately repeated frame (the same payload and counter re-sent after a
// missed downlink) would be discarded forever.
func TestClaimFrameExpires(t *testing.T) {
	ctx, store := openStore(t)

	key := fmt.Sprintf("ns-test-expiry-%d", time.Now().UnixNano())
	// A window in the past: the row is written, then immediately considered aged.
	claimed, err := store.ClaimFrame(ctx, key, -time.Second)
	if err != nil {
		t.Fatalf("ClaimFrame: %v", err)
	}
	if !claimed {
		t.Fatal("the first claim failed")
	}
	again, err := store.ClaimFrame(ctx, key, -time.Second)
	if err != nil {
		t.Fatalf("ClaimFrame (re-claim): %v", err)
	}
	if !again {
		t.Error("an aged-out claim was not re-claimable; the window would be permanent")
	}
}

func TestPruneSeen(t *testing.T) {
	ctx, store := openStore(t)

	key := fmt.Sprintf("ns-test-prune-%d", time.Now().UnixNano())
	if _, err := store.ClaimFrame(ctx, key, time.Minute); err != nil {
		t.Fatalf("ClaimFrame: %v", err)
	}
	// Nothing is old yet, so a full-minute prune must not remove it.
	if _, err := store.PruneSeen(ctx, time.Minute); err != nil {
		t.Fatalf("PruneSeen: %v", err)
	}
	if claimed, err := store.ClaimFrame(ctx, key, time.Minute); err != nil {
		t.Fatalf("ClaimFrame after prune: %v", err)
	} else if claimed {
		t.Error("a live claim was pruned by a one-minute window")
	}
	// A negative window treats everything as aged, which is how a caller clears
	// the book.
	if _, err := store.PruneSeen(ctx, -time.Second); err != nil {
		t.Fatalf("PruneSeen (aged): %v", err)
	}
	claimed, err := store.ClaimFrame(ctx, key, time.Minute)
	if err != nil {
		t.Fatalf("ClaimFrame after clearing: %v", err)
	}
	if !claimed {
		t.Error("the cleared row still blocked a fresh claim")
	}
}

// TestAirtimeLedgerSurvivesAFlushCycle covers what a restart depends on: write
// the ledger, read it back, and get the same bookings in the order they happened.
func TestAirtimeLedgerSurvivesAFlushCycle(t *testing.T) {
	ctx, store := openStore(t)

	gw := mustEUI(t, "AA55555555555555")
	suffix := time.Now().UnixNano() % 0xFFFFF
	// A frequency no other test uses, so the assertions below cannot see each
	// other's rows against a shared database.
	minHz := uint32(900_000_000 + suffix)

	bookings := []engine.AirtimeRecord{
		{Gateway: gw, Region: "EU868", MinHz: minHz,
			Start: time.Now().Add(-2 * time.Second), Airtime: 1500 * time.Millisecond},
		{Gateway: gw, Region: "EU868", MinHz: minHz,
			Start: time.Now().Add(-1 * time.Second), Airtime: 900 * time.Millisecond},
	}
	if err := store.SaveAirtime(ctx, bookings, engine.DefaultDutyCycleWindow); err != nil {
		t.Fatalf("SaveAirtime: %v", err)
	}

	loaded, err := store.LoadAirtime(ctx, engine.DefaultDutyCycleWindow)
	if err != nil {
		t.Fatalf("LoadAirtime: %v", err)
	}
	mine := filterByFreq(loaded, minHz)
	if len(mine) != 2 {
		t.Fatalf("the ledger holds %d bookings for this test's frequency, want 2", len(mine))
	}
	// Ordered by start, so a rebuilt ledger matches the order of emission.
	if mine[0].Airtime != 1500*time.Millisecond || mine[1].Airtime != 900*time.Millisecond {
		t.Errorf("airtimes came back as %v and %v, want 1.5s then 900ms",
			mine[0].Airtime, mine[1].Airtime)
	}
	if mine[0].Gateway != gw || mine[0].Region != "EU868" {
		t.Errorf("identity lost on the round trip: %+v", mine[0])
	}
	// Millisecond storage must not lose a fraction of the booking.
	if mine[0].Start.Round(time.Millisecond) != bookings[0].Start.Round(time.Millisecond) {
		t.Errorf("start came back as %v, want %v", mine[0].Start, bookings[0].Start)
	}
}

// TestAirtimeFlushIsIdempotent: the flusher writes the whole ledger each cycle,
// so the same booking is written many times. If that double-counted, a gateway
// would be told it is busier than it is and downlinks would be refused.
func TestAirtimeFlushIsIdempotent(t *testing.T) {
	ctx, store := openStore(t)

	gw := mustEUI(t, "AA55555555555555")
	minHz := uint32(910_000_000 + time.Now().UnixNano()%0xFFFFF)
	booking := []engine.AirtimeRecord{
		{Gateway: gw, Region: "EU868", MinHz: minHz, Start: time.Now(), Airtime: 2 * time.Second},
	}

	for i := 0; i < 4; i++ {
		if err := store.SaveAirtime(ctx, booking, engine.DefaultDutyCycleWindow); err != nil {
			t.Fatalf("SaveAirtime #%d: %v", i, err)
		}
	}
	loaded, err := store.LoadAirtime(ctx, engine.DefaultDutyCycleWindow)
	if err != nil {
		t.Fatalf("LoadAirtime: %v", err)
	}
	if mine := filterByFreq(loaded, minHz); len(mine) != 1 {
		t.Errorf("four flushes produced %d rows, want 1", len(mine))
	}
}

// TestAirtimePrunesExpiredBookings: the table must stay proportional to the
// window, and a restart must not inherit occupancy that has already expired.
func TestAirtimePrunesExpiredBookings(t *testing.T) {
	ctx, store := openStore(t)

	gw := mustEUI(t, "AA55555555555555")
	minHz := uint32(920_000_000 + time.Now().UnixNano()%0xFFFFF)
	old := []engine.AirtimeRecord{
		{Gateway: gw, Region: "EU868", MinHz: minHz,
			Start: time.Now().Add(-time.Hour), Airtime: time.Second},
	}
	if err := store.SaveAirtime(ctx, old, time.Minute); err != nil {
		t.Fatalf("SaveAirtime: %v", err)
	}
	// The call that wrote it also prunes outside the window, so the hour-old
	// booking must already be gone.
	loaded, err := store.LoadAirtime(ctx, time.Minute)
	if err != nil {
		t.Fatalf("LoadAirtime: %v", err)
	}
	if mine := filterByFreq(loaded, minHz); len(mine) != 0 {
		t.Errorf("an expired booking survived a one-minute window: %+v", mine)
	}
}

// TestAirtimeLedgerIsPerGatewayAndSubBand is what makes the ledger useful rather
// than a global counter: two gateways, and two sub-bands of one gateway, must not
// consume each other's budget.
func TestAirtimeLedgerIsPerGatewayAndSubBand(t *testing.T) {
	ctx, store := openStore(t)

	gwA := mustEUI(t, "AAAAAAAAAAAAAAAA")
	gwB := mustEUI(t, "BBBBBBBBBBBBBBBB")
	suffix := uint32(time.Now().UnixNano() % 0xFFFFF)
	freqA := uint32(930_000_000 + suffix)
	freqB := uint32(940_000_000 + suffix)

	err := store.SaveAirtime(ctx, []engine.AirtimeRecord{
		{Gateway: gwA, Region: "EU868", MinHz: freqA, Start: time.Now(), Airtime: time.Second},
		{Gateway: gwB, Region: "EU868", MinHz: freqA, Start: time.Now(), Airtime: 2 * time.Second},
		{Gateway: gwA, Region: "EU868", MinHz: freqB, Start: time.Now(), Airtime: 3 * time.Second},
	}, engine.DefaultDutyCycleWindow)
	if err != nil {
		t.Fatalf("SaveAirtime: %v", err)
	}

	loaded, err := store.LoadAirtime(ctx, engine.DefaultDutyCycleWindow)
	if err != nil {
		t.Fatalf("LoadAirtime: %v", err)
	}
	cases := []struct {
		name string
		eui  lorawan.EUI64
		freq uint32
		want time.Duration
	}{
		{"gateway A, sub-band A", gwA, freqA, time.Second},
		{"gateway B, sub-band A", gwB, freqA, 2 * time.Second},
		{"gateway A, sub-band B", gwA, freqB, 3 * time.Second},
	}
	for _, tc := range cases {
		var total time.Duration
		for _, r := range loaded {
			if r.Gateway.String() == tc.eui.String() && r.MinHz == tc.freq {
				total += r.Airtime
			}
		}
		if total != tc.want {
			t.Errorf("%s totals %v, want %v", tc.name, total, tc.want)
		}
	}
}

func filterByFreq(records []engine.AirtimeRecord, minHz uint32) []engine.AirtimeRecord {
	var out []engine.AirtimeRecord
	for _, r := range records {
		if r.MinHz == minHz {
			out = append(out, r)
		}
	}
	return out
}
