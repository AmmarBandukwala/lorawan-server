// Package lorawan implements the LoRaWAN 1.0.x PHY payload encoding,
// framing and security primitives of the Fieldnode network server.
//
// The semantics follow the LoRaWAN 1.0.3 specification chapters 4 and 5 and
// RP002 Regional Parameters 1.0.3. Where the specification leaves a case open,
// THIRD_PARTY_NOTICES.md records the interoperable behaviour this package was
// checked against.
package lorawan

import (
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// MaxJoinNonce is the largest JoinNonce (called AppNonce on the wire) a network
// server may hand out: the field is 24 bits in the join-accept, and the same 24
// bits are fed into the session-key derivation.
//
// Named once because three separate checks depend on it agreeing — the key
// derivation, the join-accept marshalling and the server's own counter — and a
// disagreement would mean deriving keys for a nonce a device cannot reproduce.
const MaxJoinNonce = 0xFFFFFF

// AES128Key is a 128-bit AES key used for all LoRaWAN crypto operations.
type AES128Key [16]byte

// ParseAES128Key parses a 32-character hex string (case-insensitive).
func ParseAES128Key(s string) (AES128Key, error) {
	var k AES128Key
	b, err := parseHex(s, 16)
	if err != nil {
		return k, fmt.Errorf("invalid AES128 key: %w", err)
	}
	copy(k[:], b)
	return k, nil
}

func (k AES128Key) String() string { return strings.ToUpper(hex.EncodeToString(k[:])) }

// Equal compares keys in constant time.
func (k AES128Key) Equal(other AES128Key) bool {
	return subtle.ConstantTimeCompare(k[:], other[:]) == 1
}

// DevAddr is a 32-bit device address. The Go value is the canonical
// (big-endian interpreted) address; on the wire it is little-endian.
type DevAddr uint32

// ParseDevAddr parses an 8-character hex string such as "01020304".
func ParseDevAddr(s string) (DevAddr, error) {
	b, err := parseHex(s, 4)
	if err != nil {
		return 0, fmt.Errorf("invalid DevAddr: %w", err)
	}
	return DevAddr(binary.BigEndian.Uint32(b)), nil
}

func (d DevAddr) String() string { return fmt.Sprintf("%08X", uint32(d)) }

// BytesLE returns the 4-byte little-endian wire representation.
func (d DevAddr) BytesLE() []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(d))
	return b[:]
}

// DevAddrFromBytesLE builds a DevAddr from 4 little-endian wire bytes.
func DevAddrFromBytesLE(b []byte) (DevAddr, error) {
	if len(b) != 4 {
		return 0, fmt.Errorf("DevAddr needs 4 bytes, got %d", len(b))
	}
	return DevAddr(binary.LittleEndian.Uint32(b)), nil
}

// EUI64 is a 64-bit identifier (JoinEUI / DevEUI / GatewayID). The array is
// stored in canonical big-endian order (matching its hex string form); on
// the wire LoRaWAN transmits EUIs little-endian.
type EUI64 [8]byte

// ParseEUI64 parses a 16-character hex string.
func ParseEUI64(s string) (EUI64, error) {
	var e EUI64
	b, err := parseHex(s, 8)
	if err != nil {
		return e, fmt.Errorf("invalid EUI64: %w", err)
	}
	copy(e[:], b)
	return e, nil
}

func (e EUI64) String() string { return strings.ToUpper(hex.EncodeToString(e[:])) }

// BytesLE returns the 8-byte little-endian wire representation.
func (e EUI64) BytesLE() []byte {
	out := make([]byte, 8)
	for i, v := range e {
		out[7-i] = v
	}
	return out
}

// EUI64FromBytesLE builds an EUI64 from 8 little-endian wire bytes.
func EUI64FromBytesLE(b []byte) (EUI64, error) {
	var e EUI64
	if len(b) != 8 {
		return e, fmt.Errorf("EUI64 needs 8 bytes, got %d", len(b))
	}
	for i, v := range b {
		e[7-i] = v
	}
	return e, nil
}

// NetID is a 24-bit LoRaWAN network identifier stored in canonical
// big-endian order; on the wire it is little-endian.
type NetID [3]byte

// ParseNetID parses a 6-character hex string.
func ParseNetID(s string) (NetID, error) {
	var n NetID
	b, err := parseHex(s, 3)
	if err != nil {
		return n, fmt.Errorf("invalid NetID: %w", err)
	}
	copy(n[:], b)
	return n, nil
}

func (n NetID) String() string { return strings.ToUpper(hex.EncodeToString(n[:])) }

// BytesLE returns the 3-byte little-endian wire representation.
func (n NetID) BytesLE() []byte { return []byte{n[2], n[1], n[0]} }

// EncodeFrequency encodes a frequency in Hz into the 3-byte LoRaWAN
// representation (100 Hz steps; 200 Hz steps from 2.4 GHz upwards,
// matching lrwn::encode_freq).
func EncodeFrequency(hz uint32) ([3]byte, error) {
	var out [3]byte
	v := hz
	if v >= 2_400_000_000 {
		v /= 2
	}
	if v%100 != 0 {
		return out, fmt.Errorf("frequency %d Hz must be a multiple of 100", hz)
	}
	steps := v / 100
	if steps >= 1<<24 {
		return out, fmt.Errorf("frequency %d Hz exceeds the 24-bit encoding", hz)
	}
	out[0] = byte(steps)
	out[1] = byte(steps >> 8)
	out[2] = byte(steps >> 16)
	return out, nil
}

// DecodeFrequency decodes 3 LoRaWAN frequency bytes into Hz.
func DecodeFrequency(b []byte) (uint32, error) {
	if len(b) != 3 {
		return 0, fmt.Errorf("frequency needs 3 bytes, got %d", len(b))
	}
	steps := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
	if steps >= 12_000_000 {
		return steps * 200, nil
	}
	return steps * 100, nil
}

func parseHex(s string, n int) ([]byte, error) {
	if len(s) != n*2 {
		return nil, fmt.Errorf("expected %d hex characters, got %d", n*2, len(s))
	}
	return hex.DecodeString(s)
}
