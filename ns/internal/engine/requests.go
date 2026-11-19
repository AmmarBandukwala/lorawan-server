package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// Outstanding MAC requests and the ADR decision that drives them.
//
// A request stays on the session until the device answers it and is attached to
// every downlink in the meantime. That is what makes a lost LinkADRReq
// recoverable: send-once-and-hope leaves the server and the device disagreeing
// about radio settings indefinitely, which is one of the most common causes of
// "the device works for an hour and then stops".

// outstandingRequests renders the requests waiting to be acknowledged, in the
// order they occupy FOpts. Answers to the device's own commands are placed
// before these (see respondToUplink), so a device waiting for a reply is never
// starved by a request it has not answered.
//
// FOpts holds at most 15 bytes, so a session with more outstanding requests than
// fit sends them in order and the rest on the following downlinks. That is why
// this returns a set rather than trying to include everything at once.
func (e *Engine) outstandingRequests(ses *Session) (lorawan.MACCommandSet, error) {
	var out lorawan.MACCommandSet
	add := func(cid lorawan.CID, p []byte) { out = append(out, lorawan.MACCommand{CID: cid, Payload: p}) }

	if req := ses.PendingADR; req != nil && ses.LinkADRRetries <= e.cfg.LinkADRMaxRetries {
		add(lorawan.CIDLinkADR, req.payload().Marshal())
	}
	if req := ses.PendingRX2; req != nil {
		// OptNeg is set to 1 in the RXParamSetupReq DLSettings: in 1.0.3 that
		// bit is RFU and devices expect it set, and a 1.1 device reads it as
		// the OptNeg flag.
		p, err := lorawan.RXParamSetupReqPayload{
			Frequency:  req.FrequencyHz,
			DLSettings: lorawan.DLSettings{OptNeg: true, RX2DataRate: byte(req.DataRate)},
		}.Marshal()
		if err != nil {
			return nil, fmt.Errorf("engine: encode RXParamSetupReq: %w", err)
		}
		add(lorawan.CIDRXParamSetup, p)
	}
	if req := ses.PendingDutyCycle; req != nil {
		add(lorawan.CIDDutyCycle,
			lorawan.DutyCycleReqPayload{MaxDCycle: byte(req.MaxDutyCycleIndex)}.Marshal())
	}
	if req := ses.PendingTXParams; req != nil {
		add(lorawan.CIDTXParamSetup, lorawan.TXParamSetupReqPayload{
			UplinkDwellTime:   req.UplinkDwell400,
			DownlinkDwellTime: req.DownlinkDwell400,
			MaxEIRP:           byte(req.MaxEIRPIndex),
		}.Marshal())
	}
	if req := ses.PendingRX1Delay; req != nil {
		add(lorawan.CIDRXTimingSetup,
			lorawan.RXTimingSetupReqPayload{Delay: byte(req.DelaySeconds)}.Marshal())
	}
	if req := ses.PendingChannel; req != nil {
		p, err := lorawan.NewChannelReqPayload{
			ChIndex:   byte(req.Index),
			Frequency: req.FrequencyHz,
			MinDR:     byte(req.MinDataRate),
			MaxDR:     byte(req.MaxDataRate),
		}.Marshal()
		if err != nil {
			return nil, fmt.Errorf("engine: encode NewChannelReq: %w", err)
		}
		add(lorawan.CIDNewChannel, p)
	}
	return out, nil
}

// withoutRepeats drops commands whose CID already appears in the sets that take
// precedence, so a request is never carried twice in one FOpts field.
//
// This matters because an operator command such as RequestLinkADR both records
// the outstanding request and attaches it to the downlink it queues. Sending the
// same LinkADRReq twice in one frame makes some stacks answer with two
// LinkADRAns and confuses devices that only parse the first.
func withoutRepeats(cmds, precedence lorawan.MACCommandSet) lorawan.MACCommandSet {
	if len(precedence) == 0 {
		return cmds
	}
	seen := map[lorawan.CID]bool{}
	for _, c := range precedence {
		seen[c.CID] = true
	}
	var out lorawan.MACCommandSet
	for _, c := range cmds {
		if seen[c.CID] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// recordUplinkHistory appends one ADR sample and keeps the window bounded.
func recordUplinkHistory(ses *Session, fCnt uint32, snr float64) {
	ses.UplinkHistory = append(ses.UplinkHistory, UplinkHistoryItem{
		FCnt:         fCnt,
		SNR:          snr,
		DataRate:     ses.DataRate,
		TxPowerIndex: ses.TxPower,
	})
	if len(ses.UplinkHistory) > ADRHistorySize {
		// Copying the tail into the same backing array avoids allocating on
		// every uplink of every device once the window is full.
		n := len(ses.UplinkHistory)
		ses.UplinkHistory = append(ses.UplinkHistory[:0], ses.UplinkHistory[n-ADRHistorySize:]...)
	}
}

// channelMask returns the device's mask, falling back to the band defaults when
// the session has never been given one.
func (e *Engine) channelMask(ses *Session, reg region.Region) region.ChannelMask {
	if len(ses.ChannelMask) == reg.ChannelCount() {
		return ses.ChannelMask
	}
	return reg.DefaultChannelMask()
}

// maskWord renders the first 16 channels of a mask as the ChMask field.
func maskWord(mask region.ChannelMask) uint16 {
	var w uint16
	for i := 0; i < 16 && i < len(mask); i++ {
		if mask.Enabled(i) {
			w |= 1 << i
		}
	}
	return w
}

// adrBounds narrows the data-rate range ADR may choose from: the band's uplink
// range, the device profile's limits, and what the enabled channels can carry.
func adrBounds(reg region.Region, dev *Device, mask region.ChannelMask) (minDR, maxDR int) {
	def := reg.Defaults()
	minDR, maxDR = def.MinUplinkDataRate, def.MaxUplinkDataRate

	if dev.MinDataRate != UseRegionDefault && dev.MinDataRate > minDR {
		minDR = dev.MinDataRate
	}
	if dev.MaxDataRate != UseRegionDefault && dev.MaxDataRate < maxDR {
		maxDR = dev.MaxDataRate
	}
	// A device restricted to channels that cannot carry a high data rate must
	// not be told to use one: it would answer with a negative LinkADRAns, and
	// the exchange would repeat until someone intervened.
	if onChannels := reg.MaxDataRateForMask(mask); onChannels >= 0 && onChannels < maxDR {
		maxDR = onChannels
	}
	if maxDR < minDR {
		// The profile and the channel plan contradict each other. Staying at the
		// band minimum beats emitting a request no device can accept.
		maxDR = minDR
	}
	return minDR, maxDR
}

// deviceRegion layers a device's dynamic channels over the band's plan. It
// returns reg unchanged for the overwhelming common case of a device with no
// user channels, so the hot path pays nothing.
//
// A device whose stored plan cannot be applied (a gap in the indices, a channel
// with no frequency — usually the residue of a hand-edited row) is served the
// band plan with a warning rather than failing the uplink: dropping live traffic
// because of a bad row in a table is worse than a device running on the default
// plan.
func (e *Engine) deviceRegion(ses *Session, reg region.Region) region.Region {
	if len(ses.UserChannels) == 0 {
		return reg
	}
	layered, err := region.WithUserChannels(reg, ses.UserChannels)
	if err != nil {
		e.log.Warn("ignoring the device's dynamic channels; using the band plan",
			"dev_eui", ses.DevEUI, "region", reg.CommonName(), "err", err)
		return reg
	}
	return layered
}

// setRequest records an outstanding MAC request on a session. Unlike setPending
// it does not queue a downlink: an outstanding request rides along on the next
// window the device opens, and is repeated until answered.
func (e *Engine) setRequest(ctx context.Context, devEUI lorawan.EUI64,
	mutate func(ses *Session) error) error {

	ses, err := e.store.Sessions.SessionByDevEUI(ctx, devEUI)
	if errors.Is(err, ErrNotFound) {
		// Wrapping rather than replacing: a caller must be able to tell "this
		// device has never joined" from an unexpected failure with errors.Is, and
		// a message is for humans.
		return fmt.Errorf("%w: no session for DevEUI %s; the device must join first",
			ErrNotFound, devEUI)
	}
	if err != nil {
		return err
	}
	if err := mutate(ses); err != nil {
		return err
	}
	return e.store.Sessions.SaveSession(ctx, ses)
}

// RequestDutyCycle asks the device to limit its own airtime. The index is the
// RP002 duty-cycle table: 0 means no restriction beyond the device's own, and
// each step halves the allowance (index 4 is 1/16).
func (e *Engine) RequestDutyCycle(ctx context.Context, devEUI lorawan.EUI64, maxDutyCycleIndex int) error {
	if maxDutyCycleIndex < 0 || maxDutyCycleIndex > 15 {
		return fmt.Errorf("engine: duty-cycle index %d outside the protocol range 0..15", maxDutyCycleIndex)
	}
	return e.setRequest(ctx, devEUI, func(ses *Session) error {
		ses.PendingDutyCycle = &DutyCycleRequest{MaxDutyCycleIndex: maxDutyCycleIndex}
		return nil
	})
}

// RequestTXParams sets the dwell-time and max-EIRP settings of the band. Only
// AS923 uses dwell time; sending it for another band is legal but pointless, so
// this refuses rather than letting a typo look like a working configuration.
func (e *Engine) RequestTXParams(ctx context.Context, devEUI lorawan.EUI64,
	uplinkDwell400, downlinkDwell400 bool, maxEIRPIndex int) error {

	if maxEIRPIndex < 0 || maxEIRPIndex > 15 {
		return fmt.Errorf("engine: max EIRP index %d outside the protocol range 0..15", maxEIRPIndex)
	}
	return e.setRequest(ctx, devEUI, func(ses *Session) error {
		reg, err := e.regionFor(ses.Region)
		if err != nil {
			return err
		}
		if reg.UplinkDwellTime() == 0 && reg.DownlinkDwellTime() == 0 {
			return fmt.Errorf("engine: %s has no dwell-time restriction, so TXParamSetupReq is not applicable",
				reg.CommonName())
		}
		ses.PendingTXParams = &TXParamsRequest{
			UplinkDwell400:   uplinkDwell400,
			DownlinkDwell400: downlinkDwell400,
			MaxEIRPIndex:     maxEIRPIndex,
		}
		return nil
	})
}

// RequestRX1Delay changes how long after an uplink the device opens RX1.
func (e *Engine) RequestRX1Delay(ctx context.Context, devEUI lorawan.EUI64, delaySeconds int) error {
	// RP002: the RXTimingSetupReq Delay field is 4 bits and 0 is not a valid
	// receive delay; the value is a whole number of seconds.
	if delaySeconds < 1 || delaySeconds > 15 {
		return fmt.Errorf("engine: RX1 delay %d s outside the protocol range 1..15", delaySeconds)
	}
	return e.setRequest(ctx, devEUI, func(ses *Session) error {
		ses.PendingRX1Delay = &RX1DelayRequest{DelaySeconds: delaySeconds}
		return nil
	})
}

// RequestNewChannel defines a dynamic channel for one device.
//
// The device must confirm it before it becomes part of its plan, because a
// channel the device refuses (a frequency outside its receiver's range, for
// instance) would otherwise leave the server scheduling downlinks the device is
// not listening for.
func (e *Engine) RequestNewChannel(ctx context.Context, devEUI lorawan.EUI64,
	index int, frequencyHz uint32, minDataRate, maxDataRate int) error {

	return e.setRequest(ctx, devEUI, func(ses *Session) error {
		reg, err := e.regionFor(ses.Region)
		if err != nil {
			return err
		}
		if reg.CommonName() == "US915" {
			// US915 uses NewChannelReq to enable auxiliary channels of its fixed
			// plan, and its RX1-to-downlink-channel mapping is derived from the
			// channel index rather than from a frequency the network chooses.
			// Defining an arbitrary channel there needs the downlink side of
			// that mapping, which this server does not implement yet; refusing
			// beats scheduling a downlink on a frequency the device will never
			// open.
			return fmt.Errorf("engine: dynamic channels are not supported on US915; " +
				"enable the fixed channels with a channel mask instead")
		}
		if index < 0 || index > region.MaxUserChannelIndex {
			return fmt.Errorf("engine: channel index %d outside 0..%d", index, region.MaxUserChannelIndex)
		}
		if frequencyHz < region.MinChannelFrequencyHz || frequencyHz > region.MaxChannelFrequencyHz {
			return fmt.Errorf("engine: channel frequency %d Hz is outside any LoRaWAN band (%d..%d Hz)",
				frequencyHz, region.MinChannelFrequencyHz, region.MaxChannelFrequencyHz)
		}
		if frequencyHz%region.ChannelFrequencyStepHz != 0 {
			// The on-air Freq field is in 100 Hz units; a finer value would be
			// silently rounded and the device would sit on a different frequency
			// from the one the operator typed.
			return fmt.Errorf("engine: channel frequency %d Hz must be a multiple of %d Hz",
				frequencyHz, region.ChannelFrequencyStepHz)
		}
		if minDataRate < 0 || maxDataRate > 15 || minDataRate > maxDataRate {
			return fmt.Errorf("engine: channel data-rate range DR%d..DR%d is not valid", minDataRate, maxDataRate)
		}
		for i := minDataRate; i <= maxDataRate; i++ {
			dr, err := reg.DataRate(i)
			if err != nil {
				return fmt.Errorf("engine: channel data rate DR%d does not exist in %s: %w", i, reg.CommonName(), err)
			}
			if !dr.Uplink {
				return fmt.Errorf("engine: DR%d is not an uplink data rate in %s", i, reg.CommonName())
			}
		}

		ses.PendingChannel = &ChannelRequest{
			Index:       index,
			FrequencyHz: frequencyHz,
			MinDataRate: minDataRate,
			MaxDataRate: maxDataRate,
		}
		return nil
	})
}

// maxTxPowerIndex is the highest transmit-power index ADR may ask for.
func maxTxPowerIndex(reg region.Region, dev *Device) int {
	last := len(reg.TXPowerOffsets()) - 1
	if dev.MaxTxPower != UseRegionDefault && dev.MaxTxPower < last {
		return dev.MaxTxPower
	}
	return last
}

// applyADR runs the ADR algorithm after an uplink has been accepted and turns
// the decision into an outstanding LinkADRReq.
//
// It never changes the session's radio settings: those move to the requested
// values only when the device's LinkADRAns confirms them, so the server cannot
// end up believing a device is running a data rate it refused.
func (e *Engine) applyADR(ctx context.Context, dev *Device, ses *Session, reg region.Region) {
	// The counter advances whatever else happens, because what it measures is
	// "uplinks since we last spoke about data rate".
	ses.ADRAckCnt++

	if dev.DisableADR || !ses.LastADRBit {
		return
	}
	// One request at a time: an unanswered LinkADRReq is already being re-sent,
	// and re-deciding on every uplink would churn the parameters.
	if ses.PendingADR != nil {
		return
	}

	mask := e.channelMask(ses, reg)
	minDR, maxDR := adrBounds(reg, dev, mask)
	decision := adrDecide(adrInput{
		Region:             reg,
		Enabled:            true,
		DataRate:           ses.DataRate,
		TxPowerIndex:       ses.TxPower,
		NbTrans:            ses.NbTrans,
		MaxTxPowerIndex:    maxTxPowerIndex(reg, dev),
		MaxDataRate:        maxDR,
		InstallationMargin: e.cfg.ADRInstallationMargin,
		History:            ses.UplinkHistory,
	})

	switch {
	case decision.Changed:
		if ses.LinkADRRetries >= e.cfg.LinkADRMaxRetries {
			e.recordEvent(ctx, Event{
				Kind: EventMAC, DevEUI: ses.DevEUI, DevAddr: ses.DevAddr,
				Reason: ReasonADRAbandoned,
				Detail: fmt.Sprintf("LinkADRReq unanswered after %d attempts; leaving DR%d TX%d",
					ses.LinkADRRetries, ses.DataRate, ses.TxPower),
			})
			ses.ADRAckCnt = 0
			return
		}
		if err := e.queueLinkADR(ses, reg, mask, decision.DataRate, decision.TxPowerIndex, decision.NbTrans); err != nil {
			e.log.Warn("cannot build the LinkADRReq that ADR decided on", "dev_eui", ses.DevEUI, "err", err)
			return
		}
		ses.ADRAckCnt = 0
		e.recordEvent(ctx, Event{
			Kind: EventMAC, DevEUI: ses.DevEUI, DevAddr: ses.DevAddr,
			Reason: ReasonADRDecision,
			Detail: fmt.Sprintf("%s; asking for DR%d TX%d NbTrans%d (allowed DR%d..DR%d)",
				decision.Reason, decision.DataRate, decision.TxPowerIndex, decision.NbTrans, minDR, maxDR),
		})

	case ses.ADRAckCnt >= e.cfg.ADRACKLimit+e.cfg.ADRACKDelay:
		// Nothing needs to change, but the device has an ADR counter of its own
		// that makes it fall back to a lower data rate when the network stops
		// answering. A LinkADRReq confirming the current settings resets it.
		if err := e.queueLinkADR(ses, reg, mask, ses.DataRate, ses.TxPower, ses.NbTrans); err != nil {
			e.log.Warn("cannot build the ADR keep-alive LinkADRReq", "dev_eui", ses.DevEUI, "err", err)
			return
		}
		ses.ADRAckCnt = 0
		e.recordEvent(ctx, Event{
			Kind: EventMAC, DevEUI: ses.DevEUI, DevAddr: ses.DevAddr,
			Reason: ReasonADRKeepalive,
			Detail: fmt.Sprintf("%d uplinks without a LinkADRReq; re-sending the current DR%d TX%d to reset the device's ADR counter",
				e.cfg.ADRACKLimit+e.cfg.ADRACKDelay, ses.DataRate, ses.TxPower),
		})
	}
}

// queueLinkADR stores an outstanding LinkADRReq. The mask carried is the one the
// device is running, so a data-rate change never silently rewrites a channel
// plan that someone configured by hand.
func (e *Engine) queueLinkADR(ses *Session, reg region.Region, mask region.ChannelMask,
	dataRate, txPower, nbTrans int) error {

	dr, err := reg.DataRate(dataRate)
	if err != nil {
		return err
	}
	if !dr.Uplink {
		return fmt.Errorf("engine: DR%d is not an uplink data rate in %s", dataRate, reg.CommonName())
	}
	offsets := reg.TXPowerOffsets()
	if txPower < 0 || txPower >= len(offsets) {
		return fmt.Errorf("engine: TX power index %d outside the %s range 0..%d",
			txPower, reg.CommonName(), len(offsets)-1)
	}
	if nbTrans < 1 || nbTrans > 15 {
		return fmt.Errorf("engine: NbTrans must be 1..15, got %d", nbTrans)
	}

	ses.PendingADR = &ADRRequest{
		DataRate: dataRate,
		TxPower:  txPower,
		NbTrans:  nbTrans,
		ChMask:   maskWord(mask),
		// Block 0 only: every band defines it, and leaving ChMaskCntl at 0 keeps
		// the other blocks untouched.
		ChMaskCntl: 0,
	}
	ses.AwaitingLinkADRAns = true
	return nil
}
