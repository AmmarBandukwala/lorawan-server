package integration

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// recordingSink collects what it was handed.
type recordingSink struct {
	name  string
	mu    sync.Mutex
	got   []Event
	allow func(Event) error
}

func (s *recordingSink) Name() string { return s.name }

func (s *recordingSink) Publish(_ context.Context, ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allow != nil {
		if err := s.allow(ev); err != nil {
			return err
		}
	}
	s.got = append(s.got, ev)
	return nil
}

func (s *recordingSink) events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.got...)
}

// waitFor polls until cond holds, so a test does not have to guess how long the
// worker needs.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustEUIStatic(s string) lorawan.EUI64 {
	v, _ := lorawan.ParseEUI64(s)
	return v
}

func uplinkRecord() engine.UplinkRecord {
	return engine.UplinkRecord{
		At:     time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
		DevEUI: mustEUIStatic("0011223344556677"), DevAddr: lorawan.DevAddr(0x01020304),
		FCntUp: 42, FPort: 10, FRMPayload: []byte{0xde, 0xad, 0xbe, 0xef},
		RawPHYPayload: []byte{0x40, 0x01}, GatewayID: mustEUIStatic("AA55555555555555"),
		FrequencyHz: 868100000, DataRateIndex: 5, RSSI: -71.5, SNR: 9.2,
	}
}

// TestDispatcherWritesThroughToTheStore is the property that matters most: an
// integration failure must never cost the protocol a record.
func TestDispatcherWritesThroughToTheStore(t *testing.T) {
	store := engine.NewMemStore()
	sink := &recordingSink{name: "failing", allow: func(Event) error {
		return context.DeadlineExceeded
	}}
	d := New(store, []Sink{sink}, Config{Logger: quietLogger()})
	defer d.Close()

	if err := d.RecordUplink(context.Background(), uplinkRecord()); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}

	waitFor(t, "the delivery attempt to be counted", func() bool {
		return d.Stats().Failed == 1
	})
	// The store still holds the uplink, so replay protection is unaffected.
	if len(store.Uplinks()) != 1 {
		t.Errorf("the store holds %d uplinks, want 1", len(store.Uplinks()))
	}
}

// TestDispatcherSkipsMACOnlyFrames: a frame with no FPort carries no application
// data, and emitting it would bury real traffic in ADR chatter.
func TestDispatcherSkipsMACOnlyFrames(t *testing.T) {
	store := engine.NewMemStore()
	sink := &recordingSink{name: "sink"}
	dispatcher := New(store, []Sink{sink}, Config{Logger: quietLogger()})
	defer dispatcher.Close()

	ctx := context.Background()
	macOnly := uplinkRecord()
	macOnly.FPort = -1
	if err := dispatcher.RecordUplink(ctx, macOnly); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}
	withData := uplinkRecord()
	if err := dispatcher.RecordUplink(ctx, withData); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}
	if err := dispatcher.RecordEvent(ctx, engine.Event{
		Kind: engine.EventJoinAccept, DevEUI: withData.DevEUI, At: withData.At,
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	// A kind the application does not care about must not be forwarded.
	if err := dispatcher.RecordEvent(ctx, engine.Event{Kind: engine.EventMAC, DevEUI: withData.DevEUI}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	waitFor(t, "two events", func() bool { return len(sink.events()) == 2 })
	events := sink.events()
	if events[0].Kind != KindUplink {
		t.Errorf("first event kind = %q, want uplink", events[0].Kind)
	}
	if events[1].Kind != KindJoin {
		t.Errorf("second event kind = %q, want join", events[1].Kind)
	}
	if got := dispatcher.Stats().Queued; got != 0 {
		t.Errorf("queue depth = %d after delivery, want 0", got)
	}
}

func TestDispatcherEventFields(t *testing.T) {
	store := engine.NewMemStore()
	sink := &recordingSink{name: "sink"}
	d := New(store, []Sink{sink}, Config{Logger: quietLogger()})
	defer d.Close()

	r := uplinkRecord()
	r.ACK = true
	if err := d.RecordUplink(context.Background(), r); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}
	waitFor(t, "the event", func() bool { return len(sink.events()) == 1 })

	ev := sink.events()[0]
	if ev.DeviceEUI != "0011223344556677" || ev.DevAddr != "01020304" {
		t.Errorf("identifiers = %s / %s", ev.DeviceEUI, ev.DevAddr)
	}
	if ev.FCntUp != 42 || ev.FPort != 10 || !ev.Acknowledged {
		t.Errorf("event = %+v", ev)
	}
	if ev.RSSI != -71.5 || ev.SNR != 9.2 || ev.FrequencyHz != 868100000 || ev.DataRate != 5 {
		t.Errorf("radio metadata = %+v", ev)
	}
	if want := base64.StdEncoding.EncodeToString(r.FRMPayload); ev.Data != want {
		t.Errorf("Data = %q, want base64 %q", ev.Data, want)
	}
	if ev.RawPayload != "4001" {
		t.Errorf("RawPayload = %q, want 4001", ev.RawPayload)
	}
	if ev.ReceivedAt.IsZero() {
		t.Error("ReceivedAt must carry the reception time")
	}
}

// TestDispatcherDropsInsteadOfBlockingWhenFull is the guarantee the radio path
// depends on: a destination that stops consuming must not slow an uplink down.
func TestDispatcherDropsInsteadOfBlockingWhenFull(t *testing.T) {
	store := engine.NewMemStore()
	release := make(chan struct{})
	blocking := &recordingSink{name: "blocking", allow: func(Event) error {
		<-release
		return nil
	}}
	d := New(store, []Sink{blocking},
		Config{BufferSize: 2, Workers: 1, Logger: quietLogger()})

	start := time.Now()
	for i := 0; i < 50; i++ {
		if err := d.RecordUplink(context.Background(), uplinkRecord()); err != nil {
			t.Fatalf("RecordUplink: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("recording 50 uplinks took %v; a full queue must not block the caller", elapsed)
	}
	if d.Stats().Dropped == 0 {
		t.Errorf("overflow was not counted: %+v", d.Stats())
	}

	close(release)
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Everything accepted is either delivered or counted; nothing disappears
	// silently.
	st := d.Stats()
	if st.Published+st.Dropped != 50 {
		t.Errorf("published %d + dropped %d != 50 accepted", st.Published, st.Dropped)
	}
}

// TestCloseDeliversWhatIsQueued: a restart must not lose events that were already
// accepted before it.
func TestCloseDeliversWhatIsQueued(t *testing.T) {
	store := engine.NewMemStore()
	sink := &recordingSink{name: "sink"}
	d := New(store, []Sink{sink}, Config{BufferSize: 16, Logger: quietLogger()})
	for i := 0; i < 5; i++ {
		if err := d.RecordUplink(context.Background(), uplinkRecord()); err != nil {
			t.Fatalf("RecordUplink: %v", err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(sink.events()); got != 5 {
		t.Errorf("the sink received %d events after Close, want 5", got)
	}
	if q := d.Stats().Queued; q != 0 {
		t.Errorf("queue depth after Close = %d, want 0", q)
	}
	// Close is safe to call twice: main() defers it and the shutdown path also
	// calls it.
	if err := d.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestDispatcherWithoutSinksIsTransparent: the common deployment has no
// integrations, and then this layer must cost nothing.
func TestDispatcherWithoutSinksIsTransparent(t *testing.T) {
	store := engine.NewMemStore()
	d := New(store, nil, Config{Logger: quietLogger()})
	defer d.Close()

	if err := d.RecordUplink(context.Background(), uplinkRecord()); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}
	if len(store.Uplinks()) != 1 {
		t.Error("the store write was skipped")
	}
	if got := d.Stats(); got.Published != 0 || got.Queued != 0 {
		t.Errorf("stats with no sinks = %+v", got)
	}
	if len(d.Sinks()) != 0 {
		t.Errorf("Sinks() = %v, want empty", d.Sinks())
	}
}
