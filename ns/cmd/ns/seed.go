package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// seedDevices provisions the devices listed in NS_SEED_DEVICES, a comma
// separated list of devEUI:joinEUI:appKey triples.
//
// It exists for bring-up: until the Fieldnode API owns provisioning (milestone
// M5) this is the only way to put a device on the bench without writing SQL.
//
// Seeding is deliberately non-destructive. engine's SaveDevice is a full upsert,
// so re-seeding a device that has already joined would reset its address, frame
// counters and join-nonce history — the device would then be rejected by its own
// session. A device that already exists is therefore left exactly as it is.
func seedDevices(ctx context.Context, log *slog.Logger, store engine.Store, spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}

	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		dev, err := parseSeedDevice(entry)
		if err != nil {
			return fmt.Errorf("NS_SEED_DEVICES entry %q: %w", entry, err)
		}

		existing, err := store.Devices.DeviceByDevEUI(ctx, dev.DevEUI)
		switch {
		case err == nil && existing != nil:
			log.Info("seed device is already provisioned, leaving it untouched",
				"dev_eui", dev.DevEUI, "otaa", existing.OTAA, "enabled", existing.Enabled)
			continue
		case err != nil && !errors.Is(err, engine.ErrNotFound):
			return fmt.Errorf("look up seed device %s: %w", dev.DevEUI, err)
		}

		if err := store.Devices.SaveDevice(ctx, dev); err != nil {
			return fmt.Errorf("provision seed device %s: %w", dev.DevEUI, err)
		}
		log.Info("seed device provisioned", "dev_eui", dev.DevEUI, "join_eui", dev.JoinEUI)
	}
	return nil
}

// parseSeedDevice reads one devEUI:joinEUI:appKey triple. Every field is
// validated so a typo fails at startup rather than showing up as an
// unexplained join rejection later.
func parseSeedDevice(entry string) (*engine.Device, error) {
	fields := strings.Split(entry, ":")
	if len(fields) != 3 {
		return nil, fmt.Errorf("want devEUI:joinEUI:appKey, got %d fields", len(fields))
	}
	devEUI, err := lorawan.ParseEUI64(strings.TrimSpace(fields[0]))
	if err != nil {
		return nil, fmt.Errorf("dev_eui: %w", err)
	}
	joinEUI, err := lorawan.ParseEUI64(strings.TrimSpace(fields[1]))
	if err != nil {
		return nil, fmt.Errorf("join_eui: %w", err)
	}
	appKey, err := lorawan.ParseAES128Key(strings.TrimSpace(fields[2]))
	if err != nil {
		return nil, fmt.Errorf("app_key: %w", err)
	}

	dev := engine.NewOTAADevice(devEUI, joinEUI, appKey)
	dev.Name = "seeded"
	return dev, nil
}
