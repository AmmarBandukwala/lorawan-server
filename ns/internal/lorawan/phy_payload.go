package lorawan

import "fmt"

// PHYPayload is a parsed LoRaWAN frame. Exactly one of JoinRequest,
// JoinAccept or MAC is set. JoinAccept frames are only populated after a
// successful DecryptJoinAccept (the wire form is AES-obfuscated).
type PHYPayload struct {
	MHDR        MHDR
	JoinRequest *JoinRequestPayload
	JoinAccept  *JoinAcceptPayload
	MAC         *MACPayload
	MIC         [4]byte
}

// JoinRequestPayload is the join-request body: JoinEUI | DevEUI | DevNonce.
type JoinRequestPayload struct {
	JoinEUI  EUI64
	DevEUI   EUI64
	DevNonce uint16
}

// JoinAcceptPayload is the (decrypted) join-accept body.
type JoinAcceptPayload struct {
	JoinNonce  uint32 // 24 bits
	NetID      NetID
	DevAddr    DevAddr
	DLSettings DLSettings
	RxDelay    byte
	CFList     *CFList
}

// CFList carries five additional uplink channel frequencies in join-accept
// (type 0) or the LoRaWAN 2.4 GHz / link-frequency lists (other types).
type CFList struct {
	Frequencies [5]uint32 // Hz, multiples of 100
	Type        byte
}

// MACPayload is a data frame: FHDR | FPort | FRMPayload.
type MACPayload struct {
	FHDR       FHDR
	FPort      *byte // nil when the frame carries no FRMPayload
	FRMPayload []byte
}

// ParsePHYPayload decodes a wire frame. Join-accept frames must be decoded
// with ParseJoinAccept instead because their body is encrypted.
func ParsePHYPayload(wire []byte) (*PHYPayload, error) {
	if len(wire) < 5 {
		return nil, fmt.Errorf("frame too short: %d bytes", len(wire))
	}
	mhdr, err := ParseMHDR(wire[0])
	if err != nil {
		return nil, err
	}
	p := &PHYPayload{MHDR: mhdr}
	switch mhdr.MType {
	case MTypeJoinRequest:
		// MHDR(1) + JoinEUI(8) + DevEUI(8) + DevNonce(2) + MIC(4) = 23.
		if len(wire) != 23 {
			return nil, fmt.Errorf("join-request must be 23 bytes, got %d", len(wire))
		}
		joinEUI, err := EUI64FromBytesLE(wire[1:9])
		if err != nil {
			return nil, err
		}
		devEUI, err := EUI64FromBytesLE(wire[9:17])
		if err != nil {
			return nil, err
		}
		p.JoinRequest = &JoinRequestPayload{
			JoinEUI:  joinEUI,
			DevEUI:   devEUI,
			DevNonce: uint16(wire[17]) | uint16(wire[18])<<8,
		}
		copy(p.MIC[:], wire[19:23])
		return p, nil

	case MTypeJoinAccept:
		return nil, fmt.Errorf("use ParseJoinAccept for join-accept frames")

	default:
		if mhdr.MType == MTypeProprietary {
			return nil, fmt.Errorf("proprietary frames are not supported")
		}
		fhdr, n, err := parseFHDR(wire[1:])
		if err != nil {
			return nil, err
		}
		rest := wire[1+n:]
		mac := &MACPayload{FHDR: fhdr}
		switch {
		case len(rest) == 4:
			// MIC only: no FPort, no FRMPayload.
		case len(rest) > 4:
			fport := rest[0]
			mac.FPort = &fport
			mac.FRMPayload = append([]byte(nil), rest[1:len(rest)-4]...)
		default:
			return nil, fmt.Errorf("frame truncated after FHDR")
		}
		copy(p.MIC[:], rest[len(rest)-4:])
		p.MAC = mac
		return p, nil
	}
}

// MarshalBinary encodes the frame into its wire form (including MIC).
func (p *PHYPayload) MarshalBinary() ([]byte, error) {
	body, err := p.marshalBody()
	if err != nil {
		return nil, err
	}
	return append(body, p.MIC[:]...), nil
}

func (p *PHYPayload) marshalBody() ([]byte, error) {
	switch {
	case p.JoinRequest != nil:
		if p.MHDR.MType != MTypeJoinRequest {
			return nil, fmt.Errorf("JoinRequest payload with MType %s", p.MHDR.MType)
		}
		out := []byte{p.MHDR.Byte()}
		out = append(out, p.JoinRequest.JoinEUI.BytesLE()...)
		out = append(out, p.JoinRequest.DevEUI.BytesLE()...)
		out = append(out, byte(p.JoinRequest.DevNonce), byte(p.JoinRequest.DevNonce>>8))
		return out, nil

	case p.MAC != nil:
		if p.MHDR.MType == MTypeJoinRequest || p.MHDR.MType == MTypeJoinAccept {
			return nil, fmt.Errorf("MAC payload with join MType %s", p.MHDR.MType)
		}
		fhdr, err := p.MAC.FHDR.marshal()
		if err != nil {
			return nil, err
		}
		out := append([]byte{p.MHDR.Byte()}, fhdr...)
		if p.MAC.FPort != nil {
			out = append(out, *p.MAC.FPort)
			out = append(out, p.MAC.FRMPayload...)
		} else if len(p.MAC.FRMPayload) > 0 {
			return nil, fmt.Errorf("FRMPayload present without FPort")
		}
		return out, nil

	case p.JoinAccept != nil:
		return nil, fmt.Errorf("use BuildJoinAccept to encode join-accept frames")

	default:
		return nil, fmt.Errorf("empty PHYPayload")
	}
}
