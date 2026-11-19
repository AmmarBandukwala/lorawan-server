package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Session returns the current session of a device, or ErrNotFound.
func (e *Engine) Session(ctx context.Context, devEUI lorawan.EUI64) (*Session, error) {
	return e.store.Sessions.SessionByDevEUI(ctx, devEUI)
}

// EnqueueDownlink queues an application downlink for the device's next RX
// window. Class-A devices are only reachable right after they transmit, so the
// frame waits for the next uplink rather than being dropped.
func (e *Engine) EnqueueDownlink(ctx context.Context, devEUI lorawan.EUI64, fPort byte, payload []byte, confirmed bool) error {
	if fPort == 0 {
		return errors.New("engine: FPort 0 is reserved for MAC commands")
	}
	return e.setPending(ctx, devEUI, func(ses *Session) (*PendingDownlink, error) {
		port := fPort
		return &PendingDownlink{
			FPort:      &port,
			FRMPayload: append([]byte(nil), payload...),
			Confirmed:  confirmed,
			CreatedAt:  e.now(),
		}, nil
	})
}

// RequestDeviceStatus queues a DevStatusReq; the device answers with its
// battery level and demodulation margin, which land on the session.
func (e *Engine) RequestDeviceStatus(ctx context.Context, devEUI lorawan.EUI64) error {
	return e.setPending(ctx, devEUI, func(*Session) (*PendingDownlink, error) {
		return &PendingDownlink{
			MACCommands: lorawan.MACCommandSet{{CID: lorawan.CIDDevStatus}},
			CreatedAt:   e.now(),
		}, nil
	})
}

// RequestLinkADR queues a LinkADRReq, validating the request against the band
// tables and the device's declared capabilities. The new parameters only become
// session state after the device's LinkADRAns confirms them.
//
// chMask/chMaskCntl are passed through unchanged: per-device channel-mask state
// belongs to the regional engine (M4), so callers decide the mask today.
func (e *Engine) RequestLinkADR(ctx context.Context, devEUI lorawan.EUI64,
	dataRate, txPower, nbRep int, chMask uint16, chMaskCntl byte) error {

	return e.setPending(ctx, devEUI, func(ses *Session) (*PendingDownlink, error) {
		reg, err := e.regionFor(ses.Region)
		if err != nil {
			return nil, err
		}
		dev, err := e.store.Devices.DeviceByDevEUI(ctx, devEUI)
		if err != nil {
			return nil, err
		}

		// LinkADRReq carries the *uplink* data-rate the device must use.
		dr, err := reg.DataRate(dataRate)
		if err != nil {
			return nil, err
		}
		if !dr.Uplink {
			return nil, fmt.Errorf("engine: DR%d is not an uplink data-rate in %s", dataRate, reg.CommonName())
		}
		// The band's defaults bound what ADR may ask for. A data rate can exist
		// in the table and still be outside the certified uplink range — EU868
		// lists SF6/SF5 as DR12/DR13 but caps ordinary uplink ADR at DR5 — and a
		// real device answers such a request with a negative LinkADRAns.
		def := reg.Defaults()
		if dataRate < def.MinUplinkDataRate || dataRate > def.MaxUplinkDataRate {
			return nil, fmt.Errorf("engine: DR%d is outside the %s uplink range DR%d..DR%d",
				dataRate, reg.CommonName(), def.MinUplinkDataRate, def.MaxUplinkDataRate)
		}
		if dev.MaxDataRate != UseRegionDefault && dataRate > dev.MaxDataRate {
			return nil, fmt.Errorf("engine: DR%d exceeds the device maximum DR%d", dataRate, dev.MaxDataRate)
		}
		if dev.MinDataRate != UseRegionDefault && dataRate < dev.MinDataRate {
			return nil, fmt.Errorf("engine: DR%d is below the device minimum DR%d", dataRate, dev.MinDataRate)
		}

		offsets := reg.TXPowerOffsets()
		if txPower < 0 || txPower >= len(offsets) {
			return nil, fmt.Errorf("engine: TX power index %d outside %s range 0..%d",
				txPower, reg.CommonName(), len(offsets)-1)
		}
		if dev.MaxTxPower != UseRegionDefault && txPower > dev.MaxTxPower {
			return nil, fmt.Errorf("engine: TX power index %d exceeds the device maximum %d", txPower, dev.MaxTxPower)
		}
		if nbRep < 1 || nbRep > 15 {
			return nil, fmt.Errorf("engine: NbTrans must be 1..15, got %d", nbRep)
		}

		ses.PendingADR = &ADRRequest{
			DataRate:   dataRate,
			TxPower:    txPower,
			NbTrans:    nbRep,
			ChMask:     chMask,
			ChMaskCntl: chMaskCntl,
		}
		ses.AwaitingLinkADRAns = true
		return &PendingDownlink{
			MACCommands: lorawan.MACCommandSet{
				{CID: lorawan.CIDLinkADR, Payload: ses.PendingADR.payload().Marshal()},
			},
			CreatedAt: e.now(),
		}, nil
	})
}

// RequestRX2Params queues an RXParamSetupReq, changing the device's RX2
// frequency and data rate. The values are applied to the session when the
// device answers with RXParamSetupAns.
func (e *Engine) RequestRX2Params(ctx context.Context, devEUI lorawan.EUI64, frequencyHz uint32, dataRate int) error {
	return e.setPending(ctx, devEUI, func(ses *Session) (*PendingDownlink, error) {
		reg, err := e.regionFor(ses.Region)
		if err != nil {
			return nil, err
		}
		dr, err := reg.DataRate(dataRate)
		if err != nil {
			return nil, err
		}
		if !dr.Downlink {
			return nil, fmt.Errorf("engine: DR%d cannot be used for RX2 in %s", dataRate, reg.CommonName())
		}
		payload, err := lorawan.RXParamSetupReqPayload{
			Frequency:  frequencyHz,
			DLSettings: lorawan.DLSettings{RX2DataRate: byte(dataRate)},
		}.Marshal()
		if err != nil {
			return nil, err
		}
		ses.PendingRX2 = &RX2Request{FrequencyHz: frequencyHz, DataRate: dataRate}
		return &PendingDownlink{
			MACCommands: lorawan.MACCommandSet{{CID: lorawan.CIDRXParamSetup, Payload: payload}},
			CreatedAt:   e.now(),
		}, nil
	})
}

// CancelPendingDownlink drops a queued downlink that has not been transmitted.
func (e *Engine) CancelPendingDownlink(ctx context.Context, devEUI lorawan.EUI64) error {
	ses, err := e.store.Sessions.SessionByDevEUI(ctx, devEUI)
	if err != nil {
		return err
	}
	if ses.Pending == nil {
		return ErrNotFound
	}
	if ses.Pending.FCntDown != 0 {
		return errors.New("engine: downlink already transmitted; it cannot be recalled")
	}
	ses.Pending = nil
	return e.store.Sessions.SaveSession(ctx, ses)
}

// setPending loads a session, refuses to overwrite an existing pending frame
// and stores whatever mutate returns.
func (e *Engine) setPending(ctx context.Context, devEUI lorawan.EUI64,
	mutate func(ses *Session) (*PendingDownlink, error)) error {

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
	if ses.Pending != nil {
		return ErrPendingDownlinkExists
	}

	pending, err := mutate(ses)
	if err != nil {
		return err
	}
	ses.Pending = pending
	return e.store.Sessions.SaveSession(ctx, ses)
}
