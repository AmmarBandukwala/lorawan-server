// Package sim implements a software LoRaWAN 1.0.3 class-A end-device.
//
// It exists so the network server can be tested end to end — join, uplink, MAC
// command round-trips, downlink acknowledgement — without radio hardware, and
// so nsdemo can demonstrate a full session in one process. The device is
// deliberately strict: it rejects replayed join-accepts and stale downlink
// counters, which is what proves the server side is correct.
package sim

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Device is a simulated end-device.
type Device struct {
	DevEUI  lorawan.EUI64
	JoinEUI lorawan.EUI64
	AppKey  lorawan.AES128Key
	// NetID is what the device expects to see in join-accept.
	NetID lorawan.NetID

	// Session state, populated by HandleJoinAccept.
	Joined      bool
	DevAddr     lorawan.DevAddr
	NwkSKey     lorawan.AES128Key
	AppSKey     lorawan.AES128Key
	RxDelay     byte
	RX2DataRate byte

	// LastJoinNonce is the JoinNonce of the join-accept the device accepted;
	// a lower or equal value must be refused.
	LastJoinNonce uint32
	// DevNonce of the most recent join-request, needed to derive keys.
	DevNonce uint16

	// FCntUp is the counter the next uplink will use.
	FCntUp uint32
	// FCntDown is the last accepted downlink counter; 0 means none yet.
	FCntDown uint32

	// Battery and Margin are reported by DevStatusAns.
	Battery byte
	Margin  int8
}

// New returns an unjoined device.
func New(devEUI, joinEUI lorawan.EUI64, appKey lorawan.AES128Key, netID lorawan.NetID) *Device {
	return &Device{
		DevEUI:  devEUI,
		JoinEUI: joinEUI,
		AppKey:  appKey,
		NetID:   netID,
		Battery: 200,
		Margin:  7,
	}
}

// NextJoinRequest builds a join-request frame ready to hand to the gateway.
func (d *Device) NextJoinRequest() ([]byte, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("sim: DevNonce: %w", err)
	}
	d.DevNonce = binary.LittleEndian.Uint16(b[:])

	phy := &lorawan.PHYPayload{
		MHDR: lorawan.MHDR{MType: lorawan.MTypeJoinRequest, Major: 0},
		JoinRequest: &lorawan.JoinRequestPayload{
			JoinEUI:  d.JoinEUI,
			DevEUI:   d.DevEUI,
			DevNonce: d.DevNonce,
		},
	}
	if err := phy.SetJoinRequestMIC(d.AppKey); err != nil {
		return nil, fmt.Errorf("sim: join-request MIC: %w", err)
	}
	return phy.MarshalBinary()
}

// HandleJoinAccept decrypts a join-accept, refuses replays and derives the
// session keys exactly as a real device would.
func (d *Device) HandleJoinAccept(wire []byte) error {
	ja, _, err := lorawan.ParseJoinAccept(wire, d.AppKey)
	if err != nil {
		return fmt.Errorf("sim: join-accept: %w", err)
	}
	if ja.NetID != d.NetID {
		return fmt.Errorf("sim: join-accept NetID %s, expected %s", ja.NetID, d.NetID)
	}
	if d.LastJoinNonce != 0 && ja.JoinNonce <= d.LastJoinNonce {
		return fmt.Errorf("sim: join-accept JoinNonce %d does not follow %d", ja.JoinNonce, d.LastJoinNonce)
	}

	nwkSKey, err := lorawan.DeriveNwkSKey(d.AppKey, ja.JoinNonce, ja.NetID, d.DevNonce)
	if err != nil {
		return fmt.Errorf("sim: NwkSKey: %w", err)
	}
	appSKey, err := lorawan.DeriveAppSKey(d.AppKey, ja.JoinNonce, ja.NetID, d.DevNonce)
	if err != nil {
		return fmt.Errorf("sim: AppSKey: %w", err)
	}

	d.LastJoinNonce = ja.JoinNonce
	d.DevAddr = ja.DevAddr
	d.NwkSKey = nwkSKey
	d.AppSKey = appSKey
	d.RxDelay = ja.RxDelay
	d.RX2DataRate = ja.DLSettings.RX2DataRate
	d.FCntUp = 0
	d.FCntDown = 0
	d.Joined = true
	return nil
}

// UplinkOptions describes the frame NextUplink should build.
type UplinkOptions struct {
	// FPort is ignored when NoFPort is set. FPort 0 puts MAC commands in the
	// encrypted FRMPayload instead of FOpts.
	FPort   byte
	NoFPort bool
	// Payload is the application payload; with FPort 0 it must already be the
	// marshalled MAC command set.
	Payload []byte
	// MACCommands travel in FOpts, encrypted with NwkSKey as 1.0.x §4.3.3
	// requires, unless FPort is 0 (then they ride in the FRMPayload).
	MACCommands lorawan.MACCommandSet
	Confirmed   bool
	ACK         bool
	ADR         bool
	// ForceFCnt overrides the uplink counter, for tests that build replays.
	ForceFCnt *uint32
}

// NextUplink builds a data uplink frame.
func (d *Device) NextUplink(opts UplinkOptions) ([]byte, error) {
	if !d.Joined {
		return nil, fmt.Errorf("sim: device has no session; join first")
	}

	fcnt := d.FCntUp
	if opts.ForceFCnt != nil {
		fcnt = *opts.ForceFCnt
	} else {
		d.FCntUp++
	}

	payload := append([]byte(nil), opts.Payload...)

	// MAC commands travel either in FOpts or, for FPort 0, encrypted inside
	// the FRMPayload. The choice is explicit rather than silent: dropping
	// commands on the floor would hide bugs in both the device and the tests.
	var fopts []byte
	if len(opts.MACCommands) > 0 {
		b, err := opts.MACCommands.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("sim: MAC commands: %w", err)
		}
		if opts.FPort == 0 && !opts.NoFPort {
			if len(payload) > 0 {
				return nil, fmt.Errorf("sim: FPort 0 carries MAC commands, not an application payload")
			}
			payload = b
		} else {
			if len(b) > 15 {
				return nil, fmt.Errorf("sim: FOpts overflow (%d bytes)", len(b))
			}
			fopts = b
		}
	}
	if opts.NoFPort && len(payload) > 0 {
		return nil, fmt.Errorf("sim: a frame without FPort cannot carry an application payload")
	}

	mtype := lorawan.MTypeUnconfirmedDataUp
	if opts.Confirmed {
		mtype = lorawan.MTypeConfirmedDataUp
	}

	phy := &lorawan.PHYPayload{
		MHDR: lorawan.MHDR{MType: mtype, Major: 0},
		MAC: &lorawan.MACPayload{
			FHDR: lorawan.FHDR{
				DevAddr: d.DevAddr,
				FCtrl:   lorawan.FCtrl{ADR: opts.ADR, ACK: opts.ACK, FOptsLen: byte(len(fopts))},
				FCnt:    uint16(fcnt),
				FOpts:   fopts,
			},
		},
	}

	if !opts.NoFPort {
		port := opts.FPort
		phy.MAC.FPort = &port
		phy.MAC.FRMPayload = payload

		key := d.AppSKey
		if port == 0 {
			key = d.NwkSKey
		}
		if err := phy.EncryptFRMPayload(key, lorawan.DirUplink, fcnt); err != nil {
			return nil, fmt.Errorf("sim: encrypt: %w", err)
		}
	}

	// 1.0.x §4.3.3: uplink FOpts are encrypted with NwkSKey. This happens
	// before the MIC is computed, because the MIC covers the frame as
	// transmitted — i.e. the encrypted bytes.
	if len(fopts) > 0 {
		if err := phy.EncryptFOpts(d.NwkSKey, lorawan.DirUplink, fcnt); err != nil {
			return nil, fmt.Errorf("sim: encrypt FOpts: %w", err)
		}
	}

	if err := phy.SetDataMIC(lorawan.DirUplink, fcnt, d.NwkSKey); err != nil {
		return nil, fmt.Errorf("sim: MIC: %w", err)
	}
	return phy.MarshalBinary()
}

// Downlink is a decoded downlink.
type Downlink struct {
	FCntDown    uint32
	FPort       int // -1 when the frame carries no FPort
	FRMPayload  []byte
	MACCommands lorawan.MACCommandSet
	ACK         bool
	ADR         bool
	FPending    bool
}

// HandleDownlink decodes a downlink, authenticates it and advances the
// downlink counter. A frame whose counter is not newer than the last accepted
// one is refused, which is how the tests catch a server that reuses a counter.
func (d *Device) HandleDownlink(wire []byte) (*Downlink, error) {
	if !d.Joined {
		return nil, fmt.Errorf("sim: device has no session")
	}
	phy, err := lorawan.ParsePHYPayload(wire)
	if err != nil {
		return nil, fmt.Errorf("sim: parse downlink: %w", err)
	}
	if phy.MHDR.MType == lorawan.MTypeJoinRequest || phy.MHDR.MType == lorawan.MTypeJoinAccept {
		return nil, fmt.Errorf("sim: unexpected join frame on the downlink path")
	}

	fcnt := lorawan.FullFCnt(d.FCntDown+1, phy.MAC.FHDR.FCnt)
	valid, err := phy.ValidateDataMIC(lorawan.DirDownlink, fcnt, d.NwkSKey)
	if err != nil {
		return nil, fmt.Errorf("sim: downlink MIC: %w", err)
	}
	if !valid {
		return nil, fmt.Errorf("sim: downlink MIC mismatch at FCntDown %d", fcnt)
	}
	if fcnt <= d.FCntDown {
		return nil, fmt.Errorf("sim: stale downlink counter %d (last accepted %d)", fcnt, d.FCntDown)
	}

	out := &Downlink{
		FCntDown: fcnt,
		FPort:    -1,
		ACK:      phy.MAC.FHDR.FCtrl.ACK,
		ADR:      phy.MAC.FHDR.FCtrl.ADR,
		FPending: phy.MAC.FHDR.FCtrl.FPending,
	}
	if len(phy.MAC.FHDR.FOpts) > 0 {
		macs, err := lorawan.ParseMACCommandSet(phy.MAC.FHDR.FOpts, false)
		if err != nil {
			return nil, fmt.Errorf("sim: FOpts: %w", err)
		}
		out.MACCommands = macs
	}
	if phy.MAC.FPort != nil {
		out.FPort = int(*phy.MAC.FPort)
		key := d.AppSKey
		if out.FPort == 0 {
			key = d.NwkSKey
		}
		if err := phy.DecryptFRMPayload(key, lorawan.DirDownlink, fcnt); err != nil {
			return nil, fmt.Errorf("sim: decrypt downlink: %w", err)
		}
		out.FRMPayload = phy.MAC.FRMPayload
		if out.FPort == 0 && len(out.FRMPayload) > 0 {
			macs, err := lorawan.ParseMACCommandSet(out.FRMPayload, false)
			if err != nil {
				return nil, fmt.Errorf("sim: FPort 0 commands: %w", err)
			}
			out.MACCommands = append(out.MACCommands, macs...)
			out.FRMPayload = nil
		}
	}

	d.FCntDown = fcnt
	return out, nil
}

// DevStatusAns builds the uplink that answers a DevStatusReq. Per spec the
// answering uplink also acknowledges the downlink that carried the request.
func (d *Device) DevStatusAns() ([]byte, error) {
	return d.NextUplink(UplinkOptions{
		ACK: true,
		MACCommands: lorawan.MACCommandSet{{
			CID:     lorawan.CIDDevStatus,
			Payload: lorawan.DevStatusAnsPayload{Battery: d.Battery, SNR: d.Margin}.Marshal(),
		}},
	})
}

// LinkADRAns builds the uplink that answers a LinkADRReq.
func (d *Device) LinkADRAns(accepted bool) ([]byte, error) {
	var p lorawan.LinkADRAnsPayload
	if accepted {
		p = lorawan.LinkADRAnsPayload{PowerACK: true, DataRateACK: true, ChannelACK: true}
	}
	return d.NextUplink(UplinkOptions{
		ACK:         true,
		MACCommands: lorawan.MACCommandSet{{CID: lorawan.CIDLinkADR, Payload: p.Marshal()}},
	})
}

// RXParamSetupAns builds the uplink that answers an RXParamSetupReq.
func (d *Device) RXParamSetupAns(accepted bool) ([]byte, error) {
	var p lorawan.RXParamSetupAnsPayload
	if accepted {
		p = lorawan.RXParamSetupAnsPayload{RX2Frequency: true}
	}
	return d.NextUplink(UplinkOptions{
		ACK:         true,
		MACCommands: lorawan.MACCommandSet{{CID: lorawan.CIDRXParamSetup, Payload: p.Marshal()}},
	})
}
