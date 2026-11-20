package pgstore_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/pgstore"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// dsnEnv enables this test against a real PostgreSQL. Keeping it opt-in means
// `go test ./...` stays hermetic while the SQL still has a regression test:
//
//	docker run -d --name ns-pgtest -e POSTGRES_PASSWORD=ns -e POSTGRES_DB=ns \
//	  -p 5433:5432 postgres:16-alpine
//	NS_TEST_DSN='postgres://ns:ns@host.docker.internal:5433/ns?sslmode=disable' \
//	  go test ./internal/pgstore/
const dsnEnv = "NS_TEST_DSN"

// openStore connects to the configured PostgreSQL or skips the test.
func openStore(t *testing.T) (context.Context, *pgstore.Store) {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping the PostgreSQL round trip", dsnEnv)
	}
	ctx := context.Background()
	store, err := pgstore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open (connect + migrate): %v", err)
	}
	t.Cleanup(store.Close)
	return ctx, store
}

func mustEUI(t *testing.T, s string) lorawan.EUI64 {
	t.Helper()
	v, err := lorawan.ParseEUI64(s)
	if err != nil {
		t.Fatalf("ParseEUI64(%q): %v", s, err)
	}
	return v
}

func mustKey(t *testing.T, s string) lorawan.AES128Key {
	t.Helper()
	v, err := lorawan.ParseAES128Key(s)
	if err != nil {
		t.Fatalf("ParseAES128Key(%q): %v", s, err)
	}
	return v
}

func TestRoundTrip(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping the PostgreSQL round trip", dsnEnv)
	}
	ctx := context.Background()

	store, err := pgstore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open (connect + migrate): %v", err)
	}
	defer store.Close()

	// A per-run namespace keeps the test repeatable against a shared database.
	suffix := time.Now().UnixNano() % 0xFFFFFF
	devEUI := mustEUI(t, fmt.Sprintf("0011223344%06X", suffix))
	devAddr, err := lorawan.ParseDevAddr(fmt.Sprintf("00%06X", suffix))
	if err != nil {
		t.Fatalf("ParseDevAddr: %v", err)
	}

	// --- devices --------------------------------------------------------------

	dev := engine.NewOTAADevice(devEUI, mustEUI(t, "0102030405060708"),
		mustKey(t, "2b7e151628aed2a6abf7158809cf4f3c"))
	dev.Name = "pg-roundtrip"
	dev.NextJoinNonce = 7
	dev.MarkDevNonceUsed(dev.JoinEUI, 0x1234)
	if err := store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}
	gotDev, err := store.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if gotDev.AppKey != dev.AppKey || gotDev.JoinEUI != dev.JoinEUI ||
		gotDev.NextJoinNonce != 7 || !gotDev.Enabled || !gotDev.OTAA {
		t.Errorf("device round trip = %+v", gotDev)
	}
	if !gotDev.DevNonceUsed(dev.JoinEUI, 0x1234) {
		t.Error("used_dev_nonces did not survive the JSONB round trip")
	}
	if _, err := store.DeviceByDevEUI(ctx, mustEUI(t, "FFFFFFFFFFFFFFFF")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("unknown device error = %v, want engine.ErrNotFound", err)
	}

	// --- sessions -------------------------------------------------------------

	ses := &engine.Session{
		DevEUI: devEUI, DevAddr: devAddr,
		NwkSKey:  mustKey(t, "00112233445566778899aabbccddeeff"),
		AppSKey:  mustKey(t, "ffeeddccbbaa99887766554433221100"),
		KeyEpoch: 1, FCntUp: 3, FCntDown: 2, AFCntDown: 2,
		DataRate: 5, TxPower: 2, NbTrans: 1, Rx1Delay: 1,
		Rx2DataRate: engine.UseRegionDefault, Region: "EU868",
		HasDeviceStatus: true, Battery: 187, Margin: -4,
		LastUplinkAt: time.Now().UTC().Truncate(time.Microsecond),
		LastRSSI:     -71.5, LastSNR: 9.2,
		LastGatewayID: mustEUI(t, "aa55555555555555"), LastFrequencyHz: 868100000,
	}
	if err := store.SaveSession(ctx, ses); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	byAddr, err := store.SessionByDevAddr(ctx, devAddr)
	if err != nil {
		t.Fatalf("SessionByDevAddr: %v", err)
	}
	byEUI, err := store.SessionByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("SessionByDevEUI: %v", err)
	}
	for _, got := range []*engine.Session{byAddr, byEUI} {
		if got.NwkSKey != ses.NwkSKey || got.AppSKey != ses.AppSKey ||
			got.FCntUp != 3 || got.FCntDown != 2 || got.AFCntDown != 2 ||
			got.Rx2DataRate != engine.UseRegionDefault || got.Battery != 187 || got.Margin != -4 {
			t.Errorf("session round trip = %+v", got)
		}
		if !got.LastUplinkAt.Equal(ses.LastUplinkAt) {
			t.Errorf("LastUplinkAt = %v, want %v", got.LastUplinkAt, ses.LastUplinkAt)
		}
	}
}

// TestTrafficRecords exercises the Recorder port: the retransmission upsert, the
// NULL handling of MAC-only frames, and events that have no device context.
func TestTrafficRecords(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping the PostgreSQL round trip", dsnEnv)
	}
	ctx := context.Background()

	store, err := pgstore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	suffix := time.Now().UnixNano() % 0xFFFFFF
	devEUI := mustEUI(t, fmt.Sprintf("0011223344%06X", suffix))
	devAddr, err := lorawan.ParseDevAddr(fmt.Sprintf("00%06X", suffix))
	if err != nil {
		t.Fatalf("ParseDevAddr: %v", err)
	}
	dev := engine.NewOTAADevice(devEUI, mustEUI(t, "0102030405060708"),
		mustKey(t, "2b7e151628aed2a6abf7158809cf4f3c"))
	if err := store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}
	if err := store.SaveSession(ctx, &engine.Session{
		DevEUI: devEUI, DevAddr: devAddr, KeyEpoch: 1, NbTrans: 1,
		Rx2DataRate: engine.UseRegionDefault, Region: "EU868",
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	up := engine.UplinkRecord{
		At: time.Now(), DevEUI: devEUI, DevAddr: devAddr, KeyEpoch: 1, FCntUp: 5,
		FPort: 10, FRMPayload: []byte("hi"), RawPHYPayload: []byte{0x40, 0x05},
		GatewayID: mustEUI(t, "aa55555555555555"), FrequencyHz: 868100000,
		DataRateIndex: 5, RSSI: -71.5, SNR: 9.2, Confirmed: true,
	}
	if err := store.RecordUplink(ctx, up); err != nil {
		t.Fatalf("RecordUplink: %v", err)
	}
	// The engine records a retransmission against the same counter, so the
	// natural key repeats; the store must tally it, not reject the row.
	up.Retransmission = true
	if err := store.RecordUplink(ctx, up); err != nil {
		t.Fatalf("RecordUplink (retransmission): %v", err)
	}
	var retries int
	if err := store.Pool().QueryRow(ctx,
		`SELECT retransmissions FROM ns_uplinks WHERE dev_eui = $1 AND f_cnt_up = 5`,
		devEUI.String()).Scan(&retries); err != nil {
		t.Fatalf("read back uplink: %v", err)
	}
	if retries != 1 {
		t.Errorf("retransmissions = %d, want 1", retries)
	}

	// A MAC-only frame has neither an FPort nor a payload: both must land as
	// -1/NULL rather than as plausible-looking zero values.
	macOnly := up
	macOnly.FCntUp = 6
	macOnly.FPort = -1
	macOnly.FRMPayload = nil
	macOnly.Retransmission = false
	if err := store.RecordUplink(ctx, macOnly); err != nil {
		t.Fatalf("RecordUplink (MAC-only): %v", err)
	}
	var fport int
	var payload []byte
	if err := store.Pool().QueryRow(ctx,
		`SELECT f_port, frm_payload FROM ns_uplinks WHERE dev_eui = $1 AND f_cnt_up = 6`,
		devEUI.String()).Scan(&fport, &payload); err != nil {
		t.Fatalf("read back MAC-only uplink: %v", err)
	}
	if fport != -1 || payload != nil {
		t.Errorf("MAC-only uplink stored f_port=%d payload=%v, want -1/NULL", fport, payload)
	}

	if err := store.RecordDownlink(ctx, engine.DownlinkRecord{
		At: time.Now(), DevEUI: devEUI, DevAddr: devAddr,
		GatewayID: mustEUI(t, "aa55555555555555"), Window: "RX1",
		FrequencyHz: 868100000, DataRateIndex: 5, TxPowerdBm: 14,
		FCntDown: 1, FPort: -1, RawPHYPayload: []byte{0x80, 0x01}, ACK: true,
	}); err != nil {
		t.Fatalf("RecordDownlink: %v", err)
	}

	if err := store.RecordEvent(ctx, engine.Event{
		At: time.Now(), Kind: engine.EventJoinAccept, DevEUI: devEUI, DevAddr: devAddr,
		Detail: "session established",
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := store.RecordEvent(ctx, engine.Event{
		At: time.Now(), Kind: engine.EventUplinkDrop, Reason: engine.ReasonUnknownDevAddr,
		Detail: "no session",
	}); err != nil {
		t.Fatalf("RecordEvent (orphan): %v", err)
	}
	var orphans int
	if err := store.Pool().QueryRow(ctx,
		`SELECT count(*) FROM ns_events
		 WHERE kind = $1 AND dev_eui IS NULL AND dev_addr IS NULL AND gateway_id IS NULL`,
		engine.EventUplinkDrop).Scan(&orphans); err != nil {
		t.Fatalf("read back orphan event: %v", err)
	}
	if orphans == 0 {
		t.Error("an event without device context did not store NULL identifiers")
	}

	if err := store.DeleteSession(ctx, devEUI); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := store.SessionByDevAddr(ctx, devAddr); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("after delete, SessionByDevAddr = %v, want engine.ErrNotFound", err)
	}
}

// TestMACStateRoundTrip is the reason the M4 state has a store test at all: the
// ADR window, channel plan and outstanding requests are stored as one JSON
// document, and a typo in a column name or a parameter number would otherwise
// only surface as a device whose settings quietly reset on restart.
func TestMACStateRoundTrip(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping the PostgreSQL round trip", dsnEnv)
	}
	ctx := context.Background()

	store, err := pgstore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	suffix := time.Now().UnixNano() % 0xFFFFFF
	devEUI := mustEUI(t, fmt.Sprintf("00AABBCC%08X", suffix))
	plainDevEUI := mustEUI(t, fmt.Sprintf("00CCDDCC%08X", suffix))
	devAddr, err := lorawan.ParseDevAddr(fmt.Sprintf("0A%06X", suffix))
	if err != nil {
		t.Fatalf("ParseDevAddr: %v", err)
	}

	// Sessions reference ns_devices, so both rows need their device first.
	for _, eui := range []lorawan.EUI64{devEUI, plainDevEUI} {
		if err := store.SaveDevice(ctx, engine.NewOTAADevice(eui,
			mustEUI(t, "0102030405060708"), mustKey(t, "2b7e151628aed2a6abf7158809cf4f3c"))); err != nil {
			t.Fatalf("SaveDevice(%s): %v", eui, err)
		}
	}

	saved := &engine.Session{
		DevEUI:   devEUI,
		DevAddr:  devAddr,
		NwkSKey:  mustKey(t, "00112233445566778899aabbccddeeff"),
		AppSKey:  mustKey(t, "ffeeddccbbaa99887766554433221100"),
		Region:   "EU868",
		KeyEpoch: 1,
		DataRate: 3, TxPower: 1, NbTrans: 2,
		FCntUp: 41, FCntDown: 7,

		UplinkHistory: []engine.UplinkHistoryItem{
			{FCnt: 39, SNR: 9.5, DataRate: 3, TxPowerIndex: 1},
			{FCnt: 40, SNR: -12.25, DataRate: 3, TxPowerIndex: 1},
		},
		ChannelMask: region.ChannelMask{true, false, true},
		UserChannels: map[int]region.Channel{
			3: {FrequencyHz: 867700000, DataRates: []int{0, 1, 2}, Enabled: true, UserDefined: true},
		},
		ADRAckCnt:        17,
		LinkADRRetries:   2,
		LastADRBit:       true,
		DutyCycle:        0.0625,
		MaxEIRPIndex:     3,
		UplinkDwell400:   true,
		DownlinkDwell400: false,
		PendingDutyCycle: &engine.DutyCycleRequest{MaxDutyCycleIndex: 4},
		PendingTXParams:  &engine.TXParamsRequest{UplinkDwell400: true, MaxEIRPIndex: 3},
		PendingRX1Delay:  &engine.RX1DelayRequest{DelaySeconds: 8},
		PendingChannel:   &engine.ChannelRequest{Index: 3, FrequencyHz: 867700000, MinDataRate: 0, MaxDataRate: 5},
	}
	if err := store.SaveSession(ctx, saved); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got, err := store.SessionByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("SessionByDevEUI: %v", err)
	}

	if len(got.UplinkHistory) != 2 {
		t.Fatalf("UplinkHistory = %+v, want 2 samples", got.UplinkHistory)
	}
	if got.UplinkHistory[1].SNR != -12.25 || got.UplinkHistory[1].FCnt != 40 {
		t.Errorf("history sample = %+v", got.UplinkHistory[1])
	}
	if len(got.ChannelMask) != 3 || !got.ChannelMask.Enabled(0) || got.ChannelMask.Enabled(1) {
		t.Errorf("ChannelMask = %s, want 3 entries with 1 off", got.ChannelMask)
	}
	ch, ok := got.UserChannels[3]
	if !ok || ch.FrequencyHz != 867700000 || len(ch.DataRates) != 3 || !ch.UserDefined {
		t.Errorf("UserChannels = %+v", got.UserChannels)
	}
	if got.ADRAckCnt != 17 || got.LinkADRRetries != 2 || !got.LastADRBit {
		t.Errorf("ADR counters = %d/%d/%t", got.ADRAckCnt, got.LinkADRRetries, got.LastADRBit)
	}
	if got.DutyCycle != 0.0625 || got.MaxEIRPIndex != 3 || !got.UplinkDwell400 || got.DownlinkDwell400 {
		t.Errorf("radio limits = %+v", got)
	}
	if got.PendingDutyCycle == nil || got.PendingDutyCycle.MaxDutyCycleIndex != 4 {
		t.Errorf("PendingDutyCycle = %+v", got.PendingDutyCycle)
	}
	if got.PendingTXParams == nil || !got.PendingTXParams.UplinkDwell400 || got.PendingTXParams.MaxEIRPIndex != 3 {
		t.Errorf("PendingTXParams = %+v", got.PendingTXParams)
	}
	if got.PendingRX1Delay == nil || got.PendingRX1Delay.DelaySeconds != 8 {
		t.Errorf("PendingRX1Delay = %+v", got.PendingRX1Delay)
	}
	if got.PendingChannel == nil || got.PendingChannel.Index != 3 ||
		got.PendingChannel.FrequencyHz != 867700000 || got.PendingChannel.MaxDataRate != 5 {
		t.Errorf("PendingChannel = %+v", got.PendingChannel)
	}

	// The rest of the session must survive untouched: the MAC document is added
	// to the statement, not swapped in for it.
	if got.DataRate != 3 || got.TxPower != 1 || got.NbTrans != 2 || got.FCntUp != 41 {
		t.Errorf("core session fields = DR%d TX%d NbTrans%d FCntUp%d",
			got.DataRate, got.TxPower, got.NbTrans, got.FCntUp)
	}

	// A session with no MAC state must store NULL rather than an empty document,
	// so ordinary rows stay small and cheap to load.
	plain := &engine.Session{
		DevEUI:  mustEUI(t, fmt.Sprintf("00CCDDCC%08X", suffix)),
		DevAddr: devAddr,
		NwkSKey: saved.NwkSKey, AppSKey: saved.AppSKey,
		Region:   "EU868",
		KeyEpoch: 1,
		NbTrans:  1,
	}
	plain.DevAddr = mustDevAddrTB(t, fmt.Sprintf("0B%06X", suffix))
	if err := store.SaveSession(ctx, plain); err != nil {
		t.Fatalf("SaveSession (plain): %v", err)
	}
	var raw []byte
	if err := store.Pool().QueryRow(ctx,
		`SELECT mac_state FROM ns_sessions WHERE dev_eui = $1`, plain.DevEUI.String()).Scan(&raw); err != nil {
		t.Fatalf("query mac_state: %v", err)
	}
	if len(raw) != 0 {
		t.Errorf("a session with no MAC state stored %s, want NULL", raw)
	}
}

func mustDevAddrTB(t *testing.T, s string) lorawan.DevAddr {
	t.Helper()
	v, err := lorawan.ParseDevAddr(s)
	if err != nil {
		t.Fatalf("ParseDevAddr(%q): %v", s, err)
	}
	return v
}

// TestDeviceCapabilityRoundTrip pins the per-device radio limits, including
// disable_adr. These are adjacent booleans and integers in both the SELECT list
// and the argument list, so a single misplaced entry swaps two settings without
// any error — the device would silently opt out of ADR, or gain a class it does
// not have. The only place that catches it is a round trip against the real
// schema.
func TestDeviceCapabilityRoundTrip(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping the PostgreSQL round trip", dsnEnv)
	}
	ctx := context.Background()

	store, err := pgstore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open (connect + migrate): %v", err)
	}
	defer store.Close()

	suffix := time.Now().UnixNano() % 0xFFFFFF
	devEUI := mustEUI(t, fmt.Sprintf("00AA223344%06X", suffix))

	dev := engine.NewOTAADevice(devEUI, mustEUI(t, "0102030405060708"),
		mustKey(t, "2b7e151628aed2a6abf7158809cf4f3c"))
	dev.Name = "pg-capabilities"
	dev.DisableADR = true
	dev.SupportsClassC = true
	dev.MaxDataRate = 5
	dev.MinDataRate = 2
	dev.MaxTxPower = 3
	dev.Rx1Delay = 5
	dev.Rx2DataRate = 3
	dev.Rx2FrequencyHz = 869525000

	if err := store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}
	got, err := store.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}

	// Every field is checked individually rather than with one compound
	// condition: when this fails, the message should name the field that moved.
	for _, tc := range []struct {
		name string
		got  any
		want any
	}{
		{"DisableADR", got.DisableADR, true},
		{"SupportsClassC", got.SupportsClassC, true},
		{"MaxDataRate", got.MaxDataRate, 5},
		{"MinDataRate", got.MinDataRate, 2},
		{"MaxTxPower", got.MaxTxPower, 3},
		{"Rx1Delay", got.Rx1Delay, 5},
		{"Rx2DataRate", got.Rx2DataRate, 3},
		{"Rx2FrequencyHz", got.Rx2FrequencyHz, uint32(869525000)},
		{"Name", got.Name, "pg-capabilities"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}

	// The other direction matters just as much: a device stored with the
	// defaults must read back as "no device-specific limit" rather than 0, which
	// would pin it to DR0 and maximum power.
	plain := engine.NewOTAADevice(mustEUI(t, fmt.Sprintf("00BB223344%06X", suffix)),
		mustEUI(t, "0102030405060708"), mustKey(t, "2b7e151628aed2a6abf7158809cf4f3c"))
	if err := store.SaveDevice(ctx, plain); err != nil {
		t.Fatalf("SaveDevice (defaults): %v", err)
	}
	gotPlain, err := store.DeviceByDevEUI(ctx, plain.DevEUI)
	if err != nil {
		t.Fatalf("DeviceByDevEUI (defaults): %v", err)
	}
	if gotPlain.DisableADR || gotPlain.SupportsClassC {
		t.Errorf("default flags = %+v, want ADR enabled and class A only", gotPlain)
	}
	for _, tc := range []struct {
		name string
		got  int
	}{
		{"MaxDataRate", gotPlain.MaxDataRate},
		{"MinDataRate", gotPlain.MinDataRate},
		{"MaxTxPower", gotPlain.MaxTxPower},
		{"Rx2DataRate", gotPlain.Rx2DataRate},
	} {
		if tc.got != engine.UseRegionDefault {
			t.Errorf("%s = %d, want UseRegionDefault (%d)",
				tc.name, tc.got, engine.UseRegionDefault)
		}
	}
}

// TestDeviceContextRoundTrip pins attributes and the installed position against
// the real schema.
//
// These two features are the ones a unit test cannot reach: attributes cross a
// JSONB boundary where a wrong decode turns a map into an empty map, and the
// position is three nullable columns whose whole meaning is the difference
// between "no position" and "position at 0,0". A SELECT list and an argument
// list that disagree by one entry produce a device parked in the Gulf of Guinea
// with no error raised anywhere, and the migration's own CHECK constraints are
// only exercised by a database that has them.
func TestDeviceContextRoundTrip(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping the PostgreSQL round trip", dsnEnv)
	}
	ctx := context.Background()

	store, err := pgstore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open (connect + migrate): %v", err)
	}
	defer store.Close()

	suffix := time.Now().UnixNano() % 0xFFFFFF
	devEUI := mustEUI(t, fmt.Sprintf("00CC223344%06X", suffix))

	dev := engine.NewOTAADevice(devEUI, mustEUI(t, "0102030405060708"),
		mustKey(t, "2b7e151628aed2a6abf7158809cf4f3c"))
	dev.Name = "pg-context"
	dev.Group = "cold-chain"
	dev.Attributes = map[string]string{"line": "3", "purpose": "cold store"}
	dev.Location = &engine.Location{Latitude: 51.5074, Longitude: -0.1278, Altitude: 14}

	if err := store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}
	got, err := store.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}

	if got.Group != "cold-chain" {
		t.Errorf("Group = %q, want cold-chain", got.Group)
	}
	if len(got.Attributes) != 2 || got.Attributes["line"] != "3" ||
		got.Attributes["purpose"] != "cold store" {
		t.Errorf("Attributes = %#v, want the two entries written", got.Attributes)
	}
	if got.Location == nil {
		t.Fatal("Location = nil, want the position that was stored")
	}
	if got.Location.Latitude != 51.5074 || got.Location.Longitude != -0.1278 {
		t.Errorf("coordinates = %v/%v, want 51.5074/-0.1278",
			got.Location.Latitude, got.Location.Longitude)
	}
	// Altitude 14 rather than 0 is the point of this assertion: an int column
	// scanned into a non-pointer would read 0 for a NULL and 0 for a real sea
	// level, and the console could not tell them apart.
	if got.Location.Altitude != 14 {
		t.Errorf("Altitude = %d, want 14", got.Location.Altitude)
	}

	// A position without an altitude is legal in the schema and must read back
	// as a position with a zero altitude, not as no position.
	noAlt := mustEUI(t, fmt.Sprintf("00CD223344%06X", suffix))
	dev2 := engine.NewOTAADevice(noAlt, mustEUI(t, "0102030405060708"),
		mustKey(t, "2b7e151628aed2a6abf7158809cf4f3c"))
	dev2.Location = &engine.Location{Latitude: 48.8566, Longitude: 2.3522}
	if err := store.SaveDevice(ctx, dev2); err != nil {
		t.Fatalf("SaveDevice (no altitude): %v", err)
	}
	got2, err := store.DeviceByDevEUI(ctx, noAlt)
	if err != nil {
		t.Fatalf("DeviceByDevEUI (no altitude): %v", err)
	}
	if got2.Location == nil {
		t.Fatal("a position without an altitude must still be a position")
	}
	if got2.Location.Altitude != 0 {
		t.Errorf("Altitude = %d, want 0", got2.Location.Altitude)
	}

	// No context at all: attributes must come back nil, not an empty map, so the
	// API does not have to distinguish two spellings of "none".
	bare := mustEUI(t, fmt.Sprintf("00CE223344%06X", suffix))
	dev3 := engine.NewOTAADevice(bare, mustEUI(t, "0102030405060708"),
		mustKey(t, "2b7e151628aed2a6abf7158809cf4f3c"))
	if err := store.SaveDevice(ctx, dev3); err != nil {
		t.Fatalf("SaveDevice (no context): %v", err)
	}
	got3, err := store.DeviceByDevEUI(ctx, bare)
	if err != nil {
		t.Fatalf("DeviceByDevEUI (no context): %v", err)
	}
	if got3.Attributes != nil {
		t.Errorf("Attributes = %#v, want nil for a device with none", got3.Attributes)
	}
	if got3.Location != nil {
		t.Errorf("Location = %+v, want nil for a device with no recorded position", got3.Location)
	}

	// Saving again replaces rather than merges. An operator who deletes an
	// attribute in the console must not find it back on the next read, and
	// clearing a position must actually clear it.
	dev.Attributes = map[string]string{"line": "4"}
	dev.Location = nil
	if err := store.SaveDevice(ctx, dev); err != nil {
		t.Fatalf("SaveDevice (replaced): %v", err)
	}
	gotAgain, err := store.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("DeviceByDevEUI (replaced): %v", err)
	}
	if len(gotAgain.Attributes) != 1 || gotAgain.Attributes["line"] != "4" {
		t.Errorf("Attributes = %#v, want only {line:4}; the old entry survived", gotAgain.Attributes)
	}
	if gotAgain.Location != nil {
		t.Errorf("Location = %+v, want nil after clearing", gotAgain.Location)
	}
}

// TestDeviceContextSchemaConstraints exercises the migration's own guards with
// raw SQL, bypassing the store.
//
// The API validates these too, but the constraints exist because a hand-edited
// row or a future writer must not be able to store a latitude of 91 or half a
// position. A CHECK that never fires is indistinguishable from a missing one, so
// each is fired on purpose.
func TestDeviceContextSchemaConstraints(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping the PostgreSQL round trip", dsnEnv)
	}
	ctx := context.Background()

	store, err := pgstore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open (connect + migrate): %v", err)
	}
	defer store.Close()

	suffix := time.Now().UnixNano() % 0xFFFFFF
	devEUI := fmt.Sprintf("00CF223344%06X", suffix)
	joinEUI := fmt.Sprintf("%016X", suffix)

	// A minimal row to hang the columns off; the constraints are what is under
	// test, not the device. Keys are text columns with hex CHECKs, not bytea.
	if _, err := store.Pool().Exec(ctx, `
		INSERT INTO ns_devices (dev_eui, join_eui, app_key, name)
		VALUES ($1, $2, '2B7E151628AED2A6ABF7158809CF4F3C', 'schema-probe')
		ON CONFLICT (dev_eui) DO NOTHING`,
		devEUI, joinEUI); err != nil {
		t.Fatalf("insert the probe device: %v", err)
	}
	defer func() {
		_, _ = store.Pool().Exec(ctx, `DELETE FROM ns_devices WHERE dev_eui = $1`, devEUI)
	}()

	cases := []struct {
		name  string
		set   string
		valid bool
	}{
		// Ranges. Each case starts from a clean row, so one case's columns
		// cannot change another's outcome.
		{"a latitude past the pole is refused", `latitude = 91`, false},
		{"a longitude past 180 is refused", `longitude = 181`, false},
		{"an impossible altitude is refused",
			`latitude = 51.5, longitude = -0.12, altitude = 99999`, false},
		{"sea level is a legal altitude",
			`latitude = 51.5, longitude = -0.12, altitude = 0`, true},
		{"a below-sea-level altitude is legal",
			`latitude = 51.5, longitude = -0.12, altitude = -200`, true},

		// Completeness. A position is only meaningful as a pair, so neither
		// coordinate nor altitude may arrive on its own. Altitude alone is the
		// subtle one: it is a number that looks harmless in isolation and is
		// meaningless without somewhere it belongs.
		{"a latitude with no longitude is refused", `latitude = 51.5`, false},
		{"a longitude with no latitude is refused", `longitude = -0.12`, false},
		{"an altitude with no coordinates is refused", `altitude = 12`, false},
		{"a full position is accepted", `latitude = 51.5, longitude = -0.12`, true},
		{"a position with an altitude is accepted",
			`latitude = 51.5, longitude = -0.12, altitude = 12`, true},

		// Attribute shape, enforced by 010.
		{"a non-object attribute is refused", `attributes = '["not","an","object"]'::jsonb`, false},
		{"an attribute holding a number is refused", `attributes = '{"line":3}'::jsonb`, false},
		{"a nested attribute is refused", `attributes = '{"a":{"b":"c"}}'::jsonb`, false},
		{"an empty attribute object is accepted", `attributes = '{}'::jsonb`, true},
		{"an object of strings is accepted", `attributes = '{"line":"3"}'::jsonb`, true},
	}

	for _, tc := range cases {
		// Reset first: the position columns are constrained together, so
		// leaving one set by the previous case would decide this one's outcome.
		if _, err := store.Pool().Exec(ctx,
			`UPDATE ns_devices SET latitude = NULL, longitude = NULL, altitude = NULL,
			 attributes = '{}'::jsonb WHERE dev_eui = $1`, devEUI); err != nil {
			t.Fatalf("reset the probe row: %v", err)
		}
		_, err := store.Pool().Exec(ctx,
			`UPDATE ns_devices SET `+tc.set+` WHERE dev_eui = $1`, devEUI)
		switch {
		case tc.valid && err != nil:
			t.Errorf("%s: rejected a legal value: %v", tc.name, err)
		case !tc.valid && err == nil:
			t.Errorf("%s: the schema accepted it", tc.name)
		}
	}
}
