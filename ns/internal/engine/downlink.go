package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// txPlan is one transmission opportunity the engine may use for a downlink.
type txPlan struct {
	Window        gateway.RxWindow
	Delay         time.Duration
	FrequencyHz   uint32
	DataRateIndex int
}

// dataWindows plans the class-A receive windows that follow a data uplink.
//
// A class-A device only listens in the window belonging to the uplink it just
// sent, so an uplink received in RX2 yields an RX2-only plan. RX1 can also be
// unavailable when the band cannot map the uplink channel to a downlink channel
// (for example US915 FSK uplink channels), in which case only RX2 is planned.
func (e *Engine) dataWindows(reg region.Region, frame gateway.UplinkFrame, ses *Session) []txPlan {
	def := reg.Defaults()

	rx1Delay := ses.Rx1Delay
	if rx1Delay <= 0 {
		rx1Delay = int(def.RX1Delay / time.Second)
	}
	// LoRaWAN 1.0.3: RX2 opens one second after RX1. When the device uses the
	// regional RX1 delay, use the regional RX2 delay so both agree.
	rx2Delay := rx1Delay + 1
	if ses.Rx1Delay <= 0 && def.RX2Delay > 0 {
		rx2Delay = int(def.RX2Delay / time.Second)
	}

	var plans []txPlan
	if frame.RxWindow == gateway.RxWindow1 {
		freq, errFreq := reg.RX1FrequencyHz(frame.FrequencyHz)
		dr, errDR := reg.RX1DataRate(frame.DataRateIndex, ses.Rx1DROffset)
		if errFreq == nil && errDR == nil {
			plans = append(plans, txPlan{
				Window:        gateway.RxWindow1,
				Delay:         time.Duration(rx1Delay) * time.Second,
				FrequencyHz:   freq,
				DataRateIndex: dr,
			})
		} else {
			e.log.Debug("RX1 unavailable, planning RX2 only",
				"uplink_freq_hz", frame.FrequencyHz,
				"uplink_dr", dataRateLabel(reg, frame.DataRateIndex),
				"freq_err", errFreq, "dr_err", errDR)
		}
	}

	rx2Freq := ses.Rx2FrequencyHz
	if rx2Freq == 0 {
		rx2Freq = def.RX2FrequencyHz
	}
	rx2DR := ses.Rx2DataRate
	if rx2DR == UseRegionDefault {
		rx2DR = def.RX2DataRate
	}
	plans = append(plans, txPlan{
		Window:        gateway.RxWindow2,
		Delay:         time.Duration(rx2Delay) * time.Second,
		FrequencyHz:   rx2Freq,
		DataRateIndex: rx2DR,
	})
	return plans
}

// joinWindows plans the two join-accept transmissions. Join-accept is sent in
// both windows because the device listens in both and decodes whichever it can.
func (e *Engine) joinWindows(reg region.Region, frame gateway.UplinkFrame) []txPlan {
	def := reg.Defaults()

	var plans []txPlan
	if frame.RxWindow == gateway.RxWindow1 {
		freq, errFreq := reg.RX1FrequencyHz(frame.FrequencyHz)
		// Join-accept RX1 uses RX1DROffset 0: the device has no session yet
		// and therefore no negotiated offset.
		dr, errDR := reg.RX1DataRate(frame.DataRateIndex, 0)
		if errFreq == nil && errDR == nil {
			plans = append(plans, txPlan{
				Window:        gateway.RxWindow1,
				Delay:         def.JoinAcceptDelay1,
				FrequencyHz:   freq,
				DataRateIndex: dr,
			})
		}
	}
	plans = append(plans, txPlan{
		Window:        gateway.RxWindow2,
		Delay:         def.JoinAcceptDelay2,
		FrequencyHz:   def.RX2FrequencyHz,
		DataRateIndex: def.RX2DataRate,
	})
	return plans
}

// allocateDownlinkFCnt returns the downlink counter to transmit with.
//
// A pending downlink that was already transmitted but not acknowledged is
// retransmitted with the *same* counter: the device reconstructs its 32-bit
// downlink counter from the frame itself, so a fresh counter would make the
// retransmission unrecoverable.
// MaxDownlinkAttempts caps how often one queued frame is transmitted before the
// network gives up. It applies to confirmed downlinks awaiting an acknowledgement
// and to MAC-only requests awaiting an answer; an unconfirmed application frame
// is a single attempt regardless.
//
// Exported because the cap is observable behaviour a caller can plan around: it
// sets how long a device can be unreachable before its queued frame is dropped,
// and a test needs it to drive a session past the limit.
const MaxDownlinkAttempts = 8

func allocateDownlinkFCnt(ses *Session, pending *PendingDownlink) uint32 {
	if pending != nil && pending.FCntDown != 0 && pending.FCntDown == ses.FCntDown {
		pending.Attempts++
		return pending.FCntDown
	}
	ses.FCntDown++
	if pending != nil {
		pending.FCntDown = ses.FCntDown
		pending.Attempts++
	}
	return ses.FCntDown
}

// buildDataDownlink encodes and secures a downlink data frame.
func (e *Engine) buildDataDownlink(ses *Session, mtype lorawan.MType, fcntDown uint32,
	fport *byte, frmPayload []byte, macs lorawan.MACCommandSet, ack bool) (*lorawan.PHYPayload, error) {

	var fopts []byte
	if len(macs) > 0 {
		b, err := macs.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("engine: encode MAC commands: %w", err)
		}
		if len(b) > 15 {
			return nil, fmt.Errorf("engine: MAC commands do not fit in FOpts (%d bytes)", len(b))
		}
		fopts = b
	}

	phy := &lorawan.PHYPayload{
		MHDR: lorawan.MHDR{MType: mtype, Major: 0},
		MAC: &lorawan.MACPayload{
			FHDR: lorawan.FHDR{
				DevAddr: ses.DevAddr,
				FCtrl: lorawan.FCtrl{
					ADR:      ses.AwaitingLinkADRAns,
					ACK:      ack,
					FOptsLen: byte(len(fopts)),
				},
				FCnt:  uint16(fcntDown),
				FOpts: fopts,
			},
		},
	}

	if fport != nil {
		port := *fport
		phy.MAC.FPort = &port
		phy.MAC.FRMPayload = append([]byte(nil), frmPayload...)

		key := ses.AppSKey
		if port == 0 {
			// FPort 0 carries network MAC commands encrypted with NwkSKey.
			key = ses.NwkSKey
		}
		if err := phy.EncryptFRMPayload(key, lorawan.DirDownlink, fcntDown); err != nil {
			return nil, fmt.Errorf("engine: encrypt FRMPayload: %w", err)
		}
	}

	if err := phy.SetDataMIC(lorawan.DirDownlink, fcntDown, ses.NwkSKey); err != nil {
		return nil, fmt.Errorf("engine: MIC: %w", err)
	}
	return phy, nil
}

// scheduleDownlink hands one encoded frame to every planned window. The
// transport turns Delay into an exact concentrator timestamp; keeping the
// decision band-agnostic is what lets the same engine drive UDP and Basic
// Station gateways.
func (e *Engine) scheduleDownlink(ctx context.Context, frame gateway.UplinkFrame, reg region.Region,
	plans []txPlan, wire []byte, rec DownlinkRecord) {

	plans = e.fitWindows(ctx, reg, plans, len(wire), rec)
	plans = e.enforceDutyCycle(ctx, reg, frame.GatewayID, plans, len(wire), rec)
	if len(plans) == 0 {
		e.recordEvent(ctx, Event{
			Kind: EventUplinkDrop, Reason: ReasonNoRXWindow,
			Detail: "downlink ready but no usable RX window",
			DevEUI: rec.DevEUI, DevAddr: rec.DevAddr, GatewayID: frame.GatewayID,
		})
		return
	}

	power := e.cfg.DownlinkPowerdBm
	for _, plan := range plans {
		if max := int8(reg.DownlinkTXPowerdBm(plan.FrequencyHz)); power > max {
			power = max
		}
		df := gateway.DownlinkFrame{
			GatewayID:     frame.GatewayID,
			RFRegion:      reg.CommonName(),
			UplinkID:      frame.UplinkID,
			Window:        plan.Window,
			Delay:         plan.Delay,
			FrequencyHz:   plan.FrequencyHz,
			DataRateIndex: plan.DataRateIndex,
			TxPowerdBm:    power,
			PHYPayload:    append([]byte(nil), wire...),
		}
		txRec := rec
		txRec.Window = plan.Window.String()
		txRec.FrequencyHz = plan.FrequencyHz
		txRec.DataRateIndex = plan.DataRateIndex
		txRec.TxPowerdBm = power
		e.recordDownlink(ctx, txRec)

		// The callback runs after HandleUplink has returned, so it must not
		// inherit the caller's cancellation.
		sendCtx := context.WithoutCancel(ctx)

		// plan.Delay is measured from the uplink, and time has gone by since:
		// the aggregation window, plus however long the store took. Scheduling
		// the full delay from now would push every downlink that much later, and
		// past the point where the device has closed its window the frame is
		// simply lost. The transport's concentrator timestamp is absolute, so a
		// dispatch that is merely late still lands correctly; one that is too
		// late does not, and says so through TX_ACK.
		dispatchIn := e.remainingSlack(frame, plan.Delay)
		if dispatchIn < 0 {
			e.log.Warn("the receive window is already due; dispatching immediately",
				"gateway", frame.GatewayID, "window", plan.Window,
				"overdue_by", -dispatchIn)
			dispatchIn = 0
		}

		e.scheduleFn(dispatchIn, func() {
			if err := e.gw.SendDownlink(sendCtx, df); err != nil {
				e.log.Error("send downlink failed",
					"gateway", df.GatewayID, "window", df.Window, "err", err)
				e.recordEvent(sendCtx, Event{
					Kind: EventError, Reason: ReasonDownlinkSendFailed,
					Detail: err.Error(), DevEUI: rec.DevEUI, DevAddr: rec.DevAddr,
					GatewayID: df.GatewayID,
				})
			}
		})
	}
}

func (e *Engine) recordDownlink(ctx context.Context, r DownlinkRecord) {
	if r.At.IsZero() {
		r.At = e.now()
	}
	if err := e.store.Recorder.RecordDownlink(ctx, r); err != nil {
		e.log.Error("record downlink failed", "dev_eui", r.DevEUI, "err", err)
	}
}

// fitWindows drops the planned windows whose data rate cannot carry the frame.
//
// RP002 fixes a maximum payload size per data rate (MPDS). Exceeding it is not a
// matter of slower delivery: the frame does not fit the slot, so the device
// cannot decode it and the transmission would only burn radio time and duty
// cycle. A window whose band has no table entry is kept — an unknown limit must
// not silently block a downlink.
func (e *Engine) fitWindows(ctx context.Context, reg region.Region, plans []txPlan,
	frameLen int, rec DownlinkRecord) []txPlan {

	fits := make([]txPlan, 0, len(plans))
	for _, plan := range plans {
		limit, ok := reg.MaxDownlinkPayloadSize(plan.DataRateIndex)
		if !ok {
			e.log.Debug("no maximum payload size known for window data rate",
				"window", plan.Window.String(),
				"data_rate", dataRateLabel(reg, plan.DataRateIndex))
			fits = append(fits, plan)
			continue
		}
		if frameLen <= limit {
			fits = append(fits, plan)
			continue
		}
		e.recordEvent(ctx, Event{
			Kind:      EventUplinkDrop,
			Reason:    ReasonDownlinkTooLarge,
			DevEUI:    rec.DevEUI,
			DevAddr:   rec.DevAddr,
			GatewayID: rec.GatewayID,
			FCntUp:    rec.FCntDown,
			Detail: fmt.Sprintf("%s at %s cannot carry %d bytes (limit %d)",
				plan.Window.String(), dataRateLabel(reg, plan.DataRateIndex), frameLen, limit),
		})
	}
	return fits
}
