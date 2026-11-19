package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseSeedDevice(t *testing.T) {
	dev, err := parseSeedDevice("0011223344556677:0102030405060708:2b7e151628aed2a6abf7158809cf4f3c")
	if err != nil {
		t.Fatalf("parseSeedDevice: %v", err)
	}
	if dev.DevEUI != (lorawan.EUI64{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77}) {
		t.Errorf("DevEUI = %s", dev.DevEUI)
	}
	if !dev.OTAA || !dev.Enabled {
		t.Errorf("a seeded device must be OTAA and enabled, got OTAA=%v Enabled=%v", dev.OTAA, dev.Enabled)
	}
	wantKey, err := lorawan.ParseAES128Key("2b7e151628aed2a6abf7158809cf4f3c")
	if err != nil {
		t.Fatalf("ParseAES128Key: %v", err)
	}
	if dev.AppKey != wantKey {
		t.Errorf("AppKey = %s, want %s", dev.AppKey, wantKey)
	}
}

func TestParseSeedDeviceRejectsMalformedEntries(t *testing.T) {
	for _, entry := range []string{
		"0011223344556677:0102030405060708",              // app key missing
		"0011223344556677:0102030405060708:2b7e15:extra", // too many fields
		"nothex:0102030405060708:2b7e151628aed2a6abf7158809cf4f3c",
		"0011223344556677:short:2b7e151628aed2a6abf7158809cf4f3c",
		"0011223344556677:0102030405060708:tooshort",
	} {
		if _, err := parseSeedDevice(entry); err == nil {
			t.Errorf("parseSeedDevice(%q) accepted a malformed entry", entry)
		}
	}
}

func TestSeedDevicesProvisionsNewDevices(t *testing.T) {
	ctx := context.Background()
	mem := engine.NewMemStore()

	spec := "0011223344556677:0102030405060708:2b7e151628aed2a6abf7158809cf4f3c," +
		"aabbccddeeff0011:0807060504030201:000102030405060708090a0b0c0d0e0f"
	if err := seedDevices(ctx, discardLogger(), mem.Store(), spec); err != nil {
		t.Fatalf("seedDevices: %v", err)
	}
	if got := mem.DeviceCount(); got != 2 {
		t.Fatalf("DeviceCount = %d, want 2", got)
	}
	dev, err := mem.DeviceByDevEUI(ctx, lorawan.EUI64{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11})
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	if dev.Name != "seeded" || !dev.OTAA {
		t.Errorf("seeded device = %+v", dev)
	}
}

// TestSeedDevicesNeverOverwritesAJoinedDevice is the safety property that makes
// NS_SEED_DEVICES acceptable in a long-running deployment: engine.SaveDevice is a
// full upsert, so re-seeding a joined device would reset its address, counters
// and nonce history and lock the real device out of its own session.
func TestSeedDevicesNeverOverwritesAJoinedDevice(t *testing.T) {
	ctx := context.Background()
	mem := engine.NewMemStore()
	spec := "0011223344556677:0102030405060708:2b7e151628aed2a6abf7158809cf4f3c"
	if err := seedDevices(ctx, discardLogger(), mem.Store(), spec); err != nil {
		t.Fatalf("seedDevices: %v", err)
	}

	devEUI := lorawan.EUI64{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77}
	joined, err := mem.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("DeviceByDevEUI: %v", err)
	}
	// Simulate what a completed join leaves behind.
	joined.DevAddr = lorawan.DevAddr(0x01020304)
	joined.NextJoinNonce = 7
	joined.MarkDevNonceUsed(lorawan.EUI64{1, 2, 3, 4, 5, 6, 7, 8}, 0x1234)
	if err := mem.SaveDevice(ctx, joined); err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}

	if err := seedDevices(ctx, discardLogger(), mem.Store(), spec); err != nil {
		t.Fatalf("re-seeding: %v", err)
	}

	after, err := mem.DeviceByDevEUI(ctx, devEUI)
	if err != nil {
		t.Fatalf("DeviceByDevEUI after re-seed: %v", err)
	}
	if after.DevAddr != lorawan.DevAddr(0x01020304) || after.NextJoinNonce != 7 {
		t.Errorf("re-seeding wiped join state: DevAddr=%s NextJoinNonce=%d",
			after.DevAddr, after.NextJoinNonce)
	}
	if !after.DevNonceUsed(lorawan.EUI64{1, 2, 3, 4, 5, 6, 7, 8}, 0x1234) {
		t.Error("re-seeding wiped the DevNonce replay history")
	}
}

func TestSeedDevicesReportsBadEntries(t *testing.T) {
	ctx := context.Background()
	mem := engine.NewMemStore()
	err := seedDevices(ctx, discardLogger(), mem.Store(),
		"0011223344556677:0102030405060708:2b7e151628aed2a6abf7158809cf4f3c,oops")
	if err == nil {
		t.Fatal("a malformed entry must fail the whole seed")
	}
	if !strings.Contains(err.Error(), "NS_SEED_DEVICES") {
		t.Errorf("err = %q, want it to name the variable", err)
	}
}

func TestSeedDevicesWithEmptySpecIsANoOp(t *testing.T) {
	mem := engine.NewMemStore()
	if err := seedDevices(context.Background(), discardLogger(), mem.Store(), "   "); err != nil {
		t.Fatalf("seedDevices: %v", err)
	}
	if got := mem.DeviceCount(); got != 0 {
		t.Errorf("DeviceCount = %d, want 0", got)
	}
}
