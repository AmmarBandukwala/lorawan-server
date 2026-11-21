package lorawan

import (
	"crypto/subtle"
	"fmt"
)

// Join-accept wire form (LoRaWAN 1.0.3 §5.2): the NS computes
// MIC = CMAC(AppKey, MHDR | payload), then "encrypts" payload||MIC by
// running AES-128 *decryption* over 16-byte blocks; the device recovers
// the plaintext by running AES *encryption*. MHDR itself stays in clear.

// BuildJoinAccept encodes a join-accept for transmission.
func BuildJoinAccept(pl JoinAcceptPayload, appKey AES128Key) ([]byte, error) {
	body, err := marshalJoinAcceptBody(pl)
	if err != nil {
		return nil, err
	}
	mhdr := MHDR{MType: MTypeJoinAccept, Major: 0}
	msg := append([]byte{mhdr.Byte()}, body...)
	sum := AES128CMAC(appKey, msg)

	enc := make([]byte, 0, len(body)+4)
	enc = append(enc, body...)
	enc = append(enc, sum[:4]...)
	if len(enc)%16 != 0 {
		return nil, fmt.Errorf("join-accept payload must pad to 16-byte blocks, got %d bytes", len(enc))
	}
	out := []byte{mhdr.Byte()}
	for i := 0; i < len(enc); i += 16 {
		var block [16]byte
		copy(block[:], enc[i:i+16])
		dec := appKey.DecryptBlock(block)
		out = append(out, dec[:]...)
	}
	return out, nil
}

// ParseJoinAccept decrypts a join-accept wire frame, verifies its MIC and
// returns the decoded payload. A MIC mismatch (wrong AppKey or corrupted
// frame) returns an error.
func ParseJoinAccept(wire []byte, appKey AES128Key) (*JoinAcceptPayload, [4]byte, error) {
	var mic [4]byte
	if len(wire) < 2 {
		return nil, mic, fmt.Errorf("join-accept too short: %d bytes", len(wire))
	}
	mhdr, err := ParseMHDR(wire[0])
	if err != nil {
		return nil, mic, err
	}
	if mhdr.MType != MTypeJoinAccept {
		return nil, mic, fmt.Errorf("not a join-accept frame: MType %s", mhdr.MType)
	}
	enc := wire[1:]
	if len(enc)%16 != 0 || len(enc) == 0 || len(enc) > 32 {
		return nil, mic, fmt.Errorf("invalid join-accept length %d", len(enc))
	}
	dec := make([]byte, 0, len(enc))
	for i := 0; i < len(enc); i += 16 {
		var block [16]byte
		copy(block[:], enc[i:i+16])
		plain := appKey.EncryptBlock(block)
		dec = append(dec, plain[:]...)
	}
	body := dec[:len(dec)-4]
	copy(mic[:], dec[len(dec)-4:])

	msg := append([]byte{wire[0]}, body...)
	sum := AES128CMAC(appKey, msg)
	if subtle.ConstantTimeCompare(sum[:4], mic[:]) != 1 {
		return nil, mic, fmt.Errorf("join-accept MIC mismatch")
	}
	pl, err := parseJoinAcceptBody(body)
	if err != nil {
		return nil, mic, err
	}
	return pl, mic, nil
}

func marshalJoinAcceptBody(pl JoinAcceptPayload) ([]byte, error) {
	if pl.JoinNonce > MaxJoinNonce {
		return nil, fmt.Errorf("JoinNonce %d exceeds 24 bits", pl.JoinNonce)
	}
	out := make([]byte, 0, 12+16)
	out = append(out, byte(pl.JoinNonce), byte(pl.JoinNonce>>8), byte(pl.JoinNonce>>16))
	out = append(out, pl.NetID.BytesLE()...)
	out = append(out, pl.DevAddr.BytesLE()...)
	out = append(out, pl.DLSettings.Byte(), pl.RxDelay)
	if pl.CFList != nil {
		for _, f := range pl.CFList.Frequencies {
			enc, err := EncodeFrequency(f)
			if err != nil {
				return nil, fmt.Errorf("CFList: %w", err)
			}
			out = append(out, enc[:]...)
		}
		out = append(out, pl.CFList.Type)
	}
	return out, nil
}

func parseJoinAcceptBody(body []byte) (*JoinAcceptPayload, error) {
	if len(body) != 12 && len(body) != 28 {
		return nil, fmt.Errorf("join-accept body must be 12 or 28 bytes, got %d", len(body))
	}
	pl := &JoinAcceptPayload{
		JoinNonce: uint32(body[0]) | uint32(body[1])<<8 | uint32(body[2])<<16,
		NetID:     NetID{body[5], body[4], body[3]}, // wire order is little-endian
		RxDelay:   body[11],
	}
	devAddr, err := DevAddrFromBytesLE(body[6:10])
	if err != nil {
		return nil, err
	}
	pl.DevAddr = devAddr
	pl.DLSettings = parseDLSettings(body[10])
	if len(body) == 28 {
		cf := &CFList{Type: body[27]}
		for i := range 5 {
			f, err := DecodeFrequency(body[12+i*3 : 15+i*3])
			if err != nil {
				return nil, fmt.Errorf("CFList: %w", err)
			}
			cf.Frequencies[i] = f
		}
		pl.CFList = cf
	}
	return pl, nil
}

func errNot(what string) error {
	return fmt.Errorf("not a %s", what)
}
