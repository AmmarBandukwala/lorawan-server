package lorawan

import "fmt"

// CID identifies a MAC command (LoRaWAN 1.0.3 §5.5, Table 21). The same
// CID means "request" on downlinks and "answer" on uplinks for most
// commands. Payload lengths are direction-dependent and encoded in
// macCommandLength.
type CID byte

const (
	CIDReset            CID = 0x01 // ResetInd (UL) / ResetConf (DL), 1.1
	CIDLinkCheck        CID = 0x02 // LinkCheckReq (UL) / LinkCheckAns (DL)
	CIDLinkADR          CID = 0x03 // LinkADRAns (UL) / LinkADRReq (DL)
	CIDDutyCycle        CID = 0x04 // DutyCycleAns (UL) / DutyCycleReq (DL)
	CIDRXParamSetup     CID = 0x05 // RXParamSetupAns (UL) / RXParamSetupReq (DL)
	CIDDevStatus        CID = 0x06 // DevStatusAns (UL) / DevStatusReq (DL)
	CIDNewChannel       CID = 0x07 // NewChannelAns (UL) / NewChannelReq (DL)
	CIDRXTimingSetup    CID = 0x08 // RXTimingSetupAns (UL) / RXTimingSetupReq (DL)
	CIDTXParamSetup     CID = 0x09 // TXParamSetupAns (UL) / TXParamSetupReq (DL)
	CIDDLChannel        CID = 0x0A // DLChannelAns (UL) / DLChannelReq (DL)
	CIDRekey            CID = 0x0B // RekeyInd (UL) / RekeyConf (DL), 1.1
	CIDADRParamSetup    CID = 0x0C // ADRParamSetupAns (UL) / ADRParamSetupReq (DL)
	CIDDeviceTime       CID = 0x0D // DeviceTimeReq (UL) / DeviceTimeAns (DL)
	CIDForceRejoin      CID = 0x0E // ForceRejoinReq (DL), 1.1
	CIDRejoinParamSetup CID = 0x0F // RejoinParamSetupAns (UL) / RejoinParamSetupReq (DL), 1.1
	CIDPingSlotInfo     CID = 0x10 // PingSlotInfoReq (UL) / PingSlotInfoAns (DL), class B
	CIDPingSlotChannel  CID = 0x11 // PingSlotChannelAns (UL) / PingSlotChannelReq (DL), class B
	CIDBeaconDownLink   CID = 0x12 // BeaconLinkAns (UL, deprecated) / BeaconLinkReq (DL)
	CIDBeaconTiming     CID = 0x13 // BeaconFreqAns (UL) / BeaconFreqReq (DL), class B
	CIDDeviceMode       CID = 0x20 // DeviceModeInd (UL) / DeviceModeConf (DL)
)

// String names the command the way the specification does, for logs and for the
// bench tool. Direction is not part of the name: the CID is shared and the
// payload decides which half of the pair it is.
func (c CID) String() string {
	switch c {
	case CIDReset:
		return "Reset"
	case CIDLinkCheck:
		return "LinkCheck"
	case CIDLinkADR:
		return "LinkADR"
	case CIDDutyCycle:
		return "DutyCycle"
	case CIDRXParamSetup:
		return "RXParamSetup"
	case CIDDevStatus:
		return "DevStatus"
	case CIDNewChannel:
		return "NewChannel"
	case CIDRXTimingSetup:
		return "RXTimingSetup"
	case CIDTXParamSetup:
		return "TXParamSetup"
	case CIDDLChannel:
		return "DLChannel"
	case CIDRekey:
		return "Rekey"
	case CIDADRParamSetup:
		return "ADRParamSetup"
	case CIDDeviceTime:
		return "DeviceTime"
	case CIDForceRejoin:
		return "ForceRejoin"
	case CIDRejoinParamSetup:
		return "RejoinParamSetup"
	case CIDPingSlotInfo:
		return "PingSlotInfo"
	case CIDPingSlotChannel:
		return "PingSlotChannel"
	case CIDBeaconDownLink:
		return "BeaconLink"
	case CIDBeaconTiming:
		return "BeaconFreq"
	case CIDDeviceMode:
		return "DeviceMode"
	}
	return fmt.Sprintf("Unknown(0x%02x)", byte(c))
}

// macCommandLength returns the payload length (excluding the CID byte) for
// a command travelling uplink (device -> server) or downlink. ok is false
// for unknown CIDs; callers should treat those as opaque.
func macCommandLength(cid CID, uplink bool) (n int, ok bool) {
	switch cid {
	case CIDReset:
		return 1, true
	case CIDLinkCheck:
		if uplink {
			return 0, true // LinkCheckReq
		}
		return 2, true // LinkCheckAns
	case CIDLinkADR:
		if uplink {
			return 1, true // LinkADRAns
		}
		return 4, true // LinkADRReq
	case CIDDutyCycle:
		if uplink {
			return 0, true
		}
		return 1, true
	case CIDRXParamSetup:
		if uplink {
			return 1, true
		}
		return 4, true
	case CIDDevStatus:
		if uplink {
			return 2, true // DevStatusAns
		}
		return 0, true // DevStatusReq
	case CIDNewChannel:
		if uplink {
			return 1, true
		}
		return 5, true
	case CIDRXTimingSetup:
		if uplink {
			return 0, true
		}
		return 1, true
	case CIDTXParamSetup:
		if uplink {
			return 0, true
		}
		return 1, true
	case CIDDLChannel:
		if uplink {
			return 1, true
		}
		return 5, true
	case CIDRekey:
		return 1, true
	case CIDADRParamSetup:
		if uplink {
			return 0, true
		}
		return 1, true
	case CIDDeviceTime:
		if uplink {
			return 0, true // DeviceTimeReq
		}
		// DeviceTimeAns: 4-byte seconds since GPS epoch + 1-byte
		// fraction in 1/256 s (verified against lrwn encode()).
		return 5, true
	case CIDForceRejoin:
		if uplink {
			return 0, false
		}
		return 2, true
	case CIDRejoinParamSetup:
		return 1, true
	case CIDPingSlotInfo:
		if uplink {
			return 1, true
		}
		return 0, true
	case CIDPingSlotChannel:
		if uplink {
			return 1, true
		}
		return 4, true
	case CIDBeaconDownLink, CIDBeaconTiming:
		if uplink {
			return 1, true
		}
		return 3, true
	case CIDDeviceMode:
		return 1, true
	default:
		return 0, false
	}
}

// MACCommand is one FOpts command: CID plus raw payload bytes.
type MACCommand struct {
	CID     CID
	Payload []byte
}

// MACCommandSet is a sequence of FOpts commands.
type MACCommandSet []MACCommand

// ParseMACCommandSet decodes a FOpts byte sequence. uplink selects the
// direction-dependent payload length table. Unknown CIDs consume the rest
// of the buffer as a raw payload (defensive against vendor commands).
func ParseMACCommandSet(b []byte, uplink bool) (MACCommandSet, error) {
	var set MACCommandSet
	for len(b) > 0 {
		cid := CID(b[0])
		n, ok := macCommandLength(cid, uplink)
		if !ok {
			set = append(set, MACCommand{CID: cid, Payload: append([]byte(nil), b[1:]...)})
			return set, nil
		}
		if len(b)-1 < n {
			return nil, fmt.Errorf("MAC command 0x%02x truncated: need %d payload bytes, got %d", byte(cid), n, len(b)-1)
		}
		set = append(set, MACCommand{CID: cid, Payload: append([]byte(nil), b[1:1+n]...)})
		b = b[1+n:]
	}
	return set, nil
}

// MarshalBinary encodes the command set back into FOpts bytes.
func (s MACCommandSet) MarshalBinary() ([]byte, error) {
	var out []byte
	for _, c := range s {
		out = append(out, byte(c.CID))
		out = append(out, c.Payload...)
	}
	return out, nil
}
