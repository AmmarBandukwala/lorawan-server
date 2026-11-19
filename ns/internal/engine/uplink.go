package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// handleDataUplink processes an unconfirmed or confirmed data uplink.
//
// Sequence:
//  1. resolve the session by DevAddr and the device behind it;
//  2. reconstruct the 32-bit uplink counter and authenticate the frame — MIC
//     first, because a counter is only meaningful once the frame is trusted;
//  3. apply the replay rules (retransmission is legitimate, going backwards is
//     not, an unbounded forward jump is refused);
//  4. decrypt the FRMPayload, run the MAC engine, record the uplink;
//  5. build and schedule any downlink the uplink implies.
func (e *Engine) handleDataUplink(ctx context.Context, frame gateway.UplinkFrame, phy *lorawan.PHYPayload) error {
	fhdr := &phy.MAC.FHDR

	ses, err := e.store.Sessions.SessionByDevAddr(ctx, fhdr.DevAddr)
	if errors.Is(err, ErrNotFound) {
		return e.dropEvent(ctx, frame, phy, ReasonUnknownDevAddr,
			fmt.Sprintf("no session for DevAddr %s; the device must join first", fhdr.DevAddr))
	}
	if err != nil {
		return fmt.Errorf("engine: load session: %w", err)
	}

	dev, err := e.store.Devices.DeviceByDevEUI(ctx, ses.DevEUI)
	if errors.Is(err, ErrNotFound) {
		return e.dropEvent(ctx, frame, phy, ReasonUnknownDevice,
			fmt.Sprintf("session for DevAddr %s references unknown DevEUI %s", fhdr.DevAddr, ses.DevEUI))
	}
	if err != nil {
		return fmt.Errorf("engine: load device: %w", err)
	}
	if !dev.Enabled {
		return e.dropEvent(ctx, frame, phy, ReasonDeviceDisabled, "device is disabled")
	}

	// The session's band wins when the frame does not carry one: a device
	// activated under EU868 must not be processed under US915 rules.
	regionName := frame.RFRegion
	if regionName == "" {
		regionName = ses.Region
	}
	reg, err := e.regionFor(regionName)
	if err != nil {
		return e.dropEvent(ctx, frame, phy, "unsupported_region", err.Error())
	}
	reg = e.deviceRegion(ses, reg)

	// --- frame counter + MIC -------------------------------------------------

	fcntUp := lorawan.FullFCnt(ses.FCntUp, fhdr.FCnt)
	valid, err := phy.ValidateDataMIC(lorawan.DirUplink, fcntUp, ses.NwkSKey)
	if err != nil {
		return fmt.Errorf("engine: validate uplink MIC: %w", err)
	}
	counterReset := false
	if !valid && uint32(fhdr.FCnt) != fcntUp {
		// A device that reset its counter without re-joining produces a frame
		// whose MIC only matches the raw 16-bit value. Rejecting that would
		// strand the device until it rejoins, so the alternative MIC is tried
		// and the counter is re-based below.
		alt, err := phy.ValidateDataMIC(lorawan.DirUplink, uint32(fhdr.FCnt), ses.NwkSKey)
		if err != nil {
			return fmt.Errorf("engine: validate uplink MIC (reset case): %w", err)
		}
		if alt {
			fcntUp = uint32(fhdr.FCnt)
			counterReset = true
			valid = true
		}
	}
	if !valid {
		return e.dropEvent(ctx, frame, phy, ReasonUplinkMICFailed,
			fmt.Sprintf("MIC invalid for DevAddr %s at counter %d", fhdr.DevAddr, fcntUp))
	}

	retransmission := false
	switch {
	case fcntUp+1 == ses.FCntUp:
		// The device did not receive our previous frame or its answer; the
		// same counter is legitimate once.
		retransmission = true
	case fcntUp < ses.FCntUp:
		return e.dropEvent(ctx, frame, phy, ReasonFCntReplay,
			fmt.Sprintf("counter %d is behind the expected %d", fcntUp, ses.FCntUp))
	case fcntUp-ses.FCntUp > e.cfg.MaxFCntDelta:
		return e.dropEvent(ctx, frame, phy, ReasonFCntGapTooLarge,
			fmt.Sprintf("counter %d is more than %d ahead of the expected %d",
				fcntUp, e.cfg.MaxFCntDelta, ses.FCntUp))
	}

	// --- payload -------------------------------------------------------------

	var frmPayload []byte
	if phy.MAC.FPort != nil && len(phy.MAC.FRMPayload) > 0 {
		key := ses.AppSKey
		if *phy.MAC.FPort == 0 {
			key = ses.NwkSKey // FPort 0: network MAC commands, NwkSKey encrypted
		}
		if err := phy.DecryptFRMPayload(key, lorawan.DirUplink, fcntUp); err != nil {
			return fmt.Errorf("engine: decrypt FRMPayload: %w", err)
		}
		frmPayload = phy.MAC.FRMPayload
	}

	// MAC commands travel either inside FOpts or, for FPort 0, inside the
	// FRMPayload. In the uplink direction *both* are encrypted (1.0.x §4.3.3):
	// FOpts use NwkSKey with the FPort-0 keystream, so the frame counter has to
	// be reconstructed and authenticated before either can be read.
	var cmds lorawan.MACCommandSet
	if len(fhdr.FOpts) > 0 {
		if err := phy.DecryptFOpts(ses.NwkSKey, lorawan.DirUplink, fcntUp); err != nil {
			return fmt.Errorf("engine: decrypt FOpts: %w", err)
		}
		parsed, err := lorawan.ParseMACCommandSet(fhdr.FOpts, true)
		if err != nil {
			return e.dropEvent(ctx, frame, phy, "malformed_mac_commands", err.Error())
		}
		cmds = append(cmds, parsed...)
	}
	if phy.MAC.FPort != nil && *phy.MAC.FPort == 0 && len(frmPayload) > 0 {
		parsed, err := lorawan.ParseMACCommandSet(frmPayload, true)
		if err != nil {
			return e.dropEvent(ctx, frame, phy, "malformed_mac_commands", err.Error())
		}
		cmds = append(cmds, parsed...)
		if len(parsed) > 0 {
			// A pure MAC frame carries no application payload.
			frmPayload = nil
		}
	}

	// --- acknowledgement of the downlink we sent earlier ---------------------

	if fhdr.FCtrl.ACK {
		ses.AFCntDown = fcntUp
		if ses.Pending != nil && ses.Pending.FCntDown != 0 {
			e.recordEvent(ctx, Event{
				Kind: EventMAC, DevEUI: ses.DevEUI, DevAddr: ses.DevAddr, FCntUp: fcntUp,
				Detail: fmt.Sprintf("device acknowledged downlink FCntDown %d", ses.Pending.FCntDown),
			})
			ses.Pending = nil
		}
	}
	if counterReset {
		e.recordEvent(ctx, Event{
			Kind: EventMAC, DevEUI: ses.DevEUI, DevAddr: ses.DevAddr, FCntUp: fcntUp,
			Reason: "f_cnt_reset",
			Detail: "device counter reset without re-join; re-basing the session counter",
		})
	}

	// --- record and bookkeeping ----------------------------------------------

	fport := -1
	if phy.MAC.FPort != nil {
		fport = int(*phy.MAC.FPort)
	}
	if err := e.store.Recorder.RecordUplink(ctx, UplinkRecord{
		At:             e.now(),
		DevEUI:         ses.DevEUI,
		DevAddr:        ses.DevAddr,
		KeyEpoch:       ses.KeyEpoch,
		FCntUp:         fcntUp,
		FPort:          fport,
		FRMPayload:     append([]byte(nil), frmPayload...),
		RawPHYPayload:  append([]byte(nil), frame.PHYPayload...),
		GatewayID:      frame.GatewayID,
		FrequencyHz:    frame.FrequencyHz,
		DataRateIndex:  frame.DataRateIndex,
		RSSI:           frame.RSSI,
		SNR:            frame.SNR,
		Confirmed:      phy.MHDR.MType == lorawan.MTypeConfirmedDataUp,
		Retransmission: retransmission,
		ACK:            fhdr.FCtrl.ACK,
	}); err != nil {
		e.log.Error("record uplink failed", "dev_eui", ses.DevEUI, "err", err)
	}

	if !retransmission {
		ses.FCntUp = fcntUp + 1 // next expected
	}
	ses.LastUplinkAt = e.now()
	ses.LastRSSI = frame.RSSI
	ses.LastSNR = frame.SNR
	ses.LastGatewayID = frame.GatewayID
	ses.LastFrequencyHz = frame.FrequencyHz
	ses.LastDataRateIndex = frame.DataRateIndex

	// --- adaptive data rate ---------------------------------------------------

	// The ADR bit is the device telling us whether it is following ADR; a device
	// that clears it has decided to keep its own data rate and we respect that.
	ses.LastADRBit = fhdr.FCtrl.ADR
	// One sample per logical uplink: counting NbTrans retransmissions separately
	// would fill the window with copies of the same measurement and make the
	// loss estimate in the same window meaningless.
	if !retransmission {
		recordUplinkHistory(ses, fcntUp, frame.SNR)
	}

	// --- MAC engine ----------------------------------------------------------

	replies, err := e.processMACCommands(ctx, dev, ses, cmds, reg)
	if err != nil {
		return err
	}

	// ADR runs after the commands in this frame have been processed, so a
	// LinkADRAns that just arrived clears the outstanding request before the
	// algorithm decides whether a new one is needed.
	e.applyADR(ctx, dev, ses, reg)

	// --- downlink ------------------------------------------------------------

	if err := e.respondToUplink(ctx, frame, reg, ses, replies,
		phy.MHDR.MType == lorawan.MTypeConfirmedDataUp, fcntUp); err != nil {
		return err
	}

	if err := e.store.Sessions.SaveSession(ctx, ses); err != nil {
		return fmt.Errorf("engine: save session: %w", err)
	}
	return nil
}

// respondToUplink builds and schedules the downlink implied by an uplink. A
// class-A device that has nothing to hear gets nothing: sending an empty frame
// would waste radio time and the device's RX window.
func (e *Engine) respondToUplink(ctx context.Context, frame gateway.UplinkFrame, reg region.Region,
	ses *Session, replies lorawan.MACCommandSet, confirmedUp bool, fcntUp uint32) error {

	pending := ses.Pending

	// Requests waiting for an answer ride along on every downlink. A device that
	// missed the first one gets another chance, which is the only way a lost
	// LinkADRReq or NewChannelReq is recovered without an operator noticing.
	outstanding, err := e.outstandingRequests(ses)
	if err != nil {
		return err
	}

	// Periodic battery/margin poll, only when the window would otherwise be
	// unused.
	if e.cfg.DeviceStatusEvery > 0 && pending == nil && len(replies) == 0 &&
		len(outstanding) == 0 && !confirmedUp &&
		fcntUp > 0 && fcntUp%uint32(e.cfg.DeviceStatusEvery) == 0 {
		replies = append(replies, lorawan.MACCommand{CID: lorawan.CIDDevStatus})
	}

	if pending == nil && len(replies) == 0 && len(outstanding) == 0 && !confirmedUp {
		return nil
	}

	// Answers go first: the device is waiting for them. Whatever does not fit
	// in the 15-byte FOpts field is deferred to the next window.
	precedence := make(lorawan.MACCommandSet, 0, len(replies)+len(pendingMACCommands(pending)))
	precedence = append(precedence, replies...)
	precedence = append(precedence, pendingMACCommands(pending)...)
	macs, dropped := fitMACCommands(replies, pendingMACCommands(pending),
		withoutRepeats(outstanding, precedence))
	if dropped != nil {
		e.recordEvent(ctx, Event{
			Kind: EventMAC, DevEUI: ses.DevEUI, DevAddr: ses.DevAddr, FCntUp: fcntUp,
			Reason: "mac_commands_truncated",
			Detail: fmt.Sprintf("FOpts full, deferred %d command(s): %s", dropped.count, dropped.names()),
		})
	}

	// Counting the attempt here rather than when the request was created is what
	// makes the retry cap mean "we asked this many times" instead of "we decided
	// this many times"; a request that is never attached costs nothing.
	if len(macs) > 0 && ses.PendingADR != nil {
		for _, c := range macs {
			if c.CID == lorawan.CIDLinkADR {
				ses.LinkADRRetries++
				break
			}
		}
	}

	mtype := lorawan.MTypeUnconfirmedDataDown
	if pending != nil && pending.Confirmed {
		mtype = lorawan.MTypeConfirmedDataDown
	}

	var (
		fport   *byte
		payload []byte
	)
	if pending != nil {
		if pending.FPort != nil {
			p := *pending.FPort
			fport = &p
		}
		payload = pending.FRMPayload
	}

	fcntDown := allocateDownlinkFCnt(ses, pending)
	phy, err := e.buildDataDownlink(ses, mtype, fcntDown, fport, payload, macs, confirmedUp)
	if err != nil {
		return fmt.Errorf("engine: build downlink: %w", err)
	}
	wire, err := phy.MarshalBinary()
	if err != nil {
		return fmt.Errorf("engine: encode downlink: %w", err)
	}

	rec := DownlinkRecord{
		DevEUI: ses.DevEUI, DevAddr: ses.DevAddr, GatewayID: frame.GatewayID,
		FCntDown: fcntDown, FPort: -1, FRMPayload: payload, RawPHYPayload: wire,
		ACK: confirmedUp,
	}
	if fport != nil {
		rec.FPort = int(*fport)
	}

	e.scheduleDownlink(ctx, frame, reg, e.dataWindows(reg, frame, ses), wire, rec)
	ses.LastDownlinkAt = e.now()
	e.retirePending(ctx, ses)
	return nil
}

// retirePending decides what happens to a queued frame after it has been
// transmitted.
//
// An unconfirmed application downlink is a single transmission. Nothing in the
// protocol acknowledges it — the device only sets FCtrl.ACK in reply to a
// confirmed downlink — so a frame left queued here would be re-sent on every
// subsequent uplink, forever, with a fresh downlink counter each time so the
// device would dutifully deliver the same payload to its application again and
// again. Dropping it after one attempt is what "fire and forget" means.
//
// A confirmed downlink stays until the device's confirmed uplink acknowledges it
// (see the FCtrl.ACK handling in the uplink path), and a MAC-only request stays
// because those are meant to repeat until answered. Both are bounded by
// maxDownlinkAttempts: a device that has moved out of coverage must not spend
// every window it ever opens re-transmitting a frame nobody will hear.
func (e *Engine) retirePending(ctx context.Context, ses *Session) {
	p := ses.Pending
	if p == nil {
		return
	}
	if p.FPort != nil && !p.Confirmed {
		ses.Pending = nil
		return
	}
	if p.Attempts < maxDownlinkAttempts {
		return
	}
	ses.Pending = nil
	e.recordEvent(ctx, Event{
		Kind: EventMAC, DevEUI: ses.DevEUI, DevAddr: ses.DevAddr,
		Reason: "downlink_abandoned",
		Detail: fmt.Sprintf("giving up after %d attempts to deliver FCntDown %d",
			p.Attempts, p.FCntDown),
	})
}
