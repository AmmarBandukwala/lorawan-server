package lorawan

import "fmt"

// FCtrl is the frame control field (LoRaWAN 1.0.3 §4.2.1.4).
// On downlinks the 0x10 bit means FPending; on uplinks it is the ClassB
// flag. We keep one field named after the downlink meaning and document it.
type FCtrl struct {
	ADR       bool
	ADRACKReq bool
	ACK       bool
	// FPending is FPending on downlinks and the ClassB flag on uplinks.
	FPending bool
	FOptsLen byte
}

func (f FCtrl) Byte() byte {
	b := f.FOptsLen & 0x0f
	if f.ADR {
		b |= 0x80
	}
	if f.ADRACKReq {
		b |= 0x40
	}
	if f.ACK {
		b |= 0x20
	}
	if f.FPending {
		b |= 0x10
	}
	return b
}

func parseFCtrl(b byte) FCtrl {
	return FCtrl{
		ADR:       b&0x80 != 0,
		ADRACKReq: b&0x40 != 0,
		ACK:       b&0x20 != 0,
		FPending:  b&0x10 != 0,
		FOptsLen:  b & 0x0f,
	}
}

// FHDR is the frame header: DevAddr | FCtrl | FCnt (16-bit) | FOpts.
type FHDR struct {
	DevAddr DevAddr
	FCtrl   FCtrl
	FCnt    uint16
	FOpts   []byte
}

func (f FHDR) marshal() ([]byte, error) {
	if len(f.FOpts) > 15 {
		return nil, fmt.Errorf("FOpts max length is 15 bytes, got %d", len(f.FOpts))
	}
	if int(f.FCtrl.FOptsLen) != len(f.FOpts) {
		return nil, fmt.Errorf("FCtrl.FOptsLen (%d) does not match FOpts length (%d)", f.FCtrl.FOptsLen, len(f.FOpts))
	}
	out := make([]byte, 0, 7+len(f.FOpts))
	out = append(out, f.DevAddr.BytesLE()...)
	out = append(out, f.FCtrl.Byte())
	out = append(out, byte(f.FCnt), byte(f.FCnt>>8))
	out = append(out, f.FOpts...)
	return out, nil
}

func parseFHDR(b []byte) (FHDR, int, error) {
	var f FHDR
	if len(b) < 7 {
		return f, 0, fmt.Errorf("FHDR needs at least 7 bytes, got %d", len(b))
	}
	devAddr, err := DevAddrFromBytesLE(b[0:4])
	if err != nil {
		return f, 0, err
	}
	f.DevAddr = devAddr
	f.FCtrl = parseFCtrl(b[4])
	f.FCnt = uint16(b[5]) | uint16(b[6])<<8
	n := 7 + int(f.FCtrl.FOptsLen)
	if len(b) < n {
		return f, 0, fmt.Errorf("FHDR truncated: need %d bytes, got %d", n, len(b))
	}
	if f.FCtrl.FOptsLen > 0 {
		f.FOpts = append([]byte(nil), b[7:n]...)
	}
	return f, n, nil
}

// DLSettings is the join-accept downlink settings field
// (LoRaWAN 1.0.3 §5.2): OptNeg (bit 7), RFU (bit 6), RX2DataRate
// (bits 5..2), RX1DROffset (bits 1..0).
type DLSettings struct {
	OptNeg      bool
	RX2DataRate byte
	RX1DROffset byte
}

func (d DLSettings) Byte() byte {
	var b byte
	if d.OptNeg {
		b |= 0x80
	}
	b |= (d.RX2DataRate & 0x0f) << 2
	b |= d.RX1DROffset & 0x03
	return b
}

func parseDLSettings(b byte) DLSettings {
	return DLSettings{
		OptNeg:      b&0x80 != 0,
		RX2DataRate: (b >> 2) & 0x0f,
		RX1DROffset: b & 0x03,
	}
}
