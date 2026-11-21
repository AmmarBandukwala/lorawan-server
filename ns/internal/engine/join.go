package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// handleJoinRequest implements the LoRaWAN 1.0.3 over-the-air activation
// server.
//
// Order of operations matters:
//  1. look the device up by DevEUI and check it may join at all;
//  2. verify the MIC with the AppKey — the only proof the requester knows the
//     device — before trusting any counter;
//  3. only then apply the JoinNonce replay rule.
//
// On success a fresh session is created, the join-accept is scheduled in both
// RX windows and the device record keeps the replay state.
func (e *Engine) handleJoinRequest(ctx context.Context, frame gateway.UplinkFrame, phy *lorawan.PHYPayload) error {
	jr := phy.JoinRequest

	reg, err := e.regionFor(frame.RFRegion)
	if err != nil {
		return e.dropEvent(ctx, frame, phy, "unsupported_region", err.Error())
	}

	e.recordEvent(ctx, Event{
		Kind: EventJoinRequest, DevEUI: jr.DevEUI, GatewayID: frame.GatewayID,
		Detail: fmt.Sprintf("join-request for JoinEUI %s on %s / %s",
			jr.JoinEUI, reg.CommonName(), dataRateLabel(reg, frame.DataRateIndex)),
	})

	dev, err := e.store.Devices.DeviceByDevEUI(ctx, jr.DevEUI)
	if errors.Is(err, ErrNotFound) {
		return e.dropEvent(ctx, frame, phy, ReasonUnknownDevice,
			fmt.Sprintf("no device provisioned with DevEUI %s", jr.DevEUI))
	}
	if err != nil {
		return fmt.Errorf("engine: load device: %w", err)
	}

	switch {
	case !dev.Enabled:
		return e.dropEvent(ctx, frame, phy, ReasonDeviceDisabled, "device is disabled")
	case !dev.OTAA:
		return e.dropEvent(ctx, frame, phy, ReasonNotOTAA,
			"device is provisioned for ABP and must not send join-requests")
	case dev.JoinEUI != (lorawan.EUI64{}) && dev.JoinEUI != jr.JoinEUI:
		return e.dropEvent(ctx, frame, phy, "join_eui_mismatch",
			fmt.Sprintf("join-request for %s, device belongs to %s", jr.JoinEUI, dev.JoinEUI))
	}

	valid, err := phy.ValidateJoinRequestMIC(dev.AppKey)
	if err != nil {
		return fmt.Errorf("engine: validate join MIC: %w", err)
	}
	if !valid {
		return e.dropEvent(ctx, frame, phy, ReasonJoinMICFailed,
			"join-request MIC does not match the stored AppKey")
	}

	// LoRaWAN 1.0.3 chapter 6.2.4: a DevNonce may never be reused, otherwise a
	// captured join-request could be replayed to re-establish an old session.
	if dev.DevNonceUsed(jr.JoinEUI, jr.DevNonce) {
		return e.dropEvent(ctx, frame, phy, ReasonDevNonceReplay,
			fmt.Sprintf("DevNonce %d was already used with JoinEUI %s", jr.DevNonce, jr.JoinEUI))
	}

	// The JoinNonce (AppNonce) handed out by the server increases by one per
	// join, so it is always new and always greater than the previous one —
	// which is what lets the device reject a replayed join-accept.
	if dev.NextJoinNonce >= lorawan.MaxJoinNonce {
		return e.dropEvent(ctx, frame, phy, ReasonJoinNonceExhausted,
			"the 24-bit JoinNonce counter is exhausted; re-provision the device")
	}
	appNonce := dev.NextJoinNonce + 1
	devAddr, err := e.generateDevAddr(ctx)
	if err != nil {
		return err
	}

	nwkSKey, err := lorawan.DeriveNwkSKey(dev.AppKey, appNonce, e.cfg.NetID, jr.DevNonce)
	if err != nil {
		return fmt.Errorf("engine: derive NwkSKey: %w", err)
	}
	appSKey, err := lorawan.DeriveAppSKey(dev.AppKey, appNonce, e.cfg.NetID, jr.DevNonce)
	if err != nil {
		return fmt.Errorf("engine: derive AppSKey: %w", err)
	}

	def := reg.Defaults()
	ja := lorawan.JoinAcceptPayload{
		JoinNonce: appNonce,
		NetID:     e.cfg.NetID,
		DevAddr:   devAddr,
		DLSettings: lorawan.DLSettings{
			OptNeg: false, // LoRaWAN 1.0.3 device
			// Ignored by 1.0.3 devices (their RX2 data rate comes from the
			// regional parameters); carried for completeness.
			RX2DataRate: byte(def.RX2DataRate),
			RX1DROffset: 0,
		},
		RxDelay: byte(def.RX1Delay / time.Second),
	}
	wire, err := lorawan.BuildJoinAccept(ja, dev.AppKey)
	if err != nil {
		return fmt.Errorf("engine: build join-accept: %w", err)
	}

	// Replay state plus the freshly derived credentials on the device record.
	dev.NextJoinNonce = appNonce
	dev.MarkDevNonceUsed(jr.JoinEUI, jr.DevNonce)
	dev.DevAddr = devAddr
	dev.NwkSKey = nwkSKey
	dev.AppSKey = appSKey
	if err := e.store.Devices.SaveDevice(ctx, dev); err != nil {
		return fmt.Errorf("engine: save device: %w", err)
	}

	// A re-join replaces the session but bumps the key epoch, so stored uplinks
	// from the old session can never be confused with the new one.
	var epoch uint32 = 1
	if prev, err := e.store.Sessions.SessionByDevEUI(ctx, dev.DevEUI); err == nil && prev != nil {
		epoch = prev.KeyEpoch + 1
	}

	session := &Session{
		DevEUI:            dev.DevEUI,
		DevAddr:           devAddr,
		NwkSKey:           nwkSKey,
		AppSKey:           appSKey,
		KeyEpoch:          epoch,
		Region:            reg.CommonName(),
		DataRate:          frame.DataRateIndex,
		TxPower:           0,
		NbTrans:           1,
		Rx1Delay:          dev.Rx1Delay,
		Rx2DataRate:       dev.Rx2DataRate,
		Rx2FrequencyHz:    dev.Rx2FrequencyHz,
		LastUplinkAt:      e.now(),
		LastRSSI:          frame.RSSI,
		LastSNR:           frame.SNR,
		LastGatewayID:     frame.GatewayID,
		LastFrequencyHz:   frame.FrequencyHz,
		LastDataRateIndex: frame.DataRateIndex,
	}
	if session.Rx2DataRate == 0 {
		session.Rx2DataRate = UseRegionDefault
	}
	if err := e.store.Sessions.SaveSession(ctx, session); err != nil {
		return fmt.Errorf("engine: save session: %w", err)
	}

	e.recordEvent(ctx, Event{
		Kind: EventJoinAccept, DevEUI: dev.DevEUI, DevAddr: devAddr,
		GatewayID: frame.GatewayID,
		Detail: fmt.Sprintf("session established on %s (JoinNonce %d, DevNonce %d)",
			reg.CommonName(), appNonce, jr.DevNonce),
	})

	e.scheduleDownlink(ctx, frame, reg, e.joinWindows(reg, frame), wire, DownlinkRecord{
		DevEUI: dev.DevEUI, DevAddr: devAddr, GatewayID: frame.GatewayID,
		FPort: -1, RawPHYPayload: wire,
	})
	return nil
}
