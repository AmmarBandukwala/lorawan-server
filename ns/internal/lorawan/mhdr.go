package lorawan

import "fmt"

// MType is the 3-bit message type of the MHDR (LoRaWAN 1.0.3 §4.2.1).
type MType byte

const (
	MTypeJoinRequest         MType = 0x00
	MTypeJoinAccept          MType = 0x01
	MTypeUnconfirmedDataUp   MType = 0x02
	MTypeConfirmedDataDown   MType = 0x03
	MTypeConfirmedDataUp     MType = 0x04
	MTypeUnconfirmedDataDown MType = 0x05
	MTypeProprietary         MType = 0x07
)

func (m MType) String() string {
	switch m {
	case MTypeJoinRequest:
		return "JoinRequest"
	case MTypeJoinAccept:
		return "JoinAccept"
	case MTypeUnconfirmedDataUp:
		return "UnconfirmedDataUp"
	case MTypeConfirmedDataDown:
		return "ConfirmedDataDown"
	case MTypeConfirmedDataUp:
		return "ConfirmedDataUp"
	case MTypeUnconfirmedDataDown:
		return "UnconfirmedDataDown"
	case MTypeProprietary:
		return "Proprietary"
	default:
		return fmt.Sprintf("Invalid(0x%02x)", byte(m))
	}
}

// IsUplink reports whether the message travels device -> network.
func (m MType) IsUplink() bool {
	return m == MTypeJoinRequest || m == MTypeUnconfirmedDataUp || m == MTypeConfirmedDataUp
}

// IsConfirmed reports confirmed (ack-requiring) message types.
func (m MType) IsConfirmed() bool {
	return m == MTypeConfirmedDataUp || m == MTypeConfirmedDataDown
}

// MHDR is the 1-byte MAC header: MType (bits 7..5), RFU (bits 4..2),
// Major (bits 1..0).
type MHDR struct {
	MType MType
	Major byte
}

func (m MHDR) Byte() byte { return byte(m.MType)<<5 | m.Major&0x03 }

// ParseMHDR decodes the MHDR byte; MType 0x06 is reserved by the spec.
func ParseMHDR(b byte) (MHDR, error) {
	m := MHDR{MType: MType(b >> 5), Major: b & 0x03}
	if m.MType == 0x06 {
		return m, fmt.Errorf("reserved MType 0x06")
	}
	return m, nil
}
