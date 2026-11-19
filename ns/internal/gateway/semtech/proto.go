// Package semtech implements the Semtech UDP packet-forwarder protocol: the
// protocol spoken by the large majority of LoRaWAN gateways (the Semtech
// reference forwarder and its many clones — RAK, IC880a, Raspberry Pi
// concentrator hats — and the dialect most commercial bridges relay).
//
// Implementing it natively removes the gateway-bridge and its MQTT broker from
// the deployment: gateways talk to the network server directly.
//
// # Wire format
//
// Every datagram starts with a header:
//
//	byte 0    protocol version (3 bits) << 5 | packet type (5 bits)
//	byte 1-2  sequence number, big endian
//	byte 3-n  gateway identifier, only for PUSH_DATA, PULL_DATA and TX_ACK:
//	          8-byte EUI in protocol v2, 6-byte MAC in v1
//
// The JSON body (rxpk/stat/txpk) follows the header, so its offset depends on
// both the version and the type. v1's shorter identifier is the single most
// common source of "no uplinks seen" bugs, so both are handled explicitly.
package semtech

import (
	"encoding/binary"
	"fmt"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// PacketType occupies the low 5 bits of the first header byte.
type PacketType byte

const (
	// PushData is gateway -> server and carries an rxpk and/or stat object.
	PushData PacketType = 0x00
	// PushAck is the server's acknowledgement of a PushData.
	PushAck PacketType = 0x01
	// PullData is the gateway saying "I am here, send me downlinks". It also
	// refreshes the address the server must use for downlinks.
	PullData PacketType = 0x02
	// PullResp is the server -> gateway downlink command (a txpk object).
	PullResp PacketType = 0x03
	// PullAck acknowledges a PullData.
	PullAck PacketType = 0x04
	// TxAck is the gateway reporting that a downlink was transmitted, or the
	// error that stopped it.
	TxAck PacketType = 0x05
)

// String names the packet type for logs.
func (t PacketType) String() string {
	switch t {
	case PushData:
		return "PUSH_DATA"
	case PushAck:
		return "PUSH_ACK"
	case PullData:
		return "PULL_DATA"
	case PullResp:
		return "PULL_RESP"
	case PullAck:
		return "PULL_ACK"
	case TxAck:
		return "TX_ACK"
	}
	return fmt.Sprintf("UNKNOWN(0x%02x)", byte(t))
}

// HasGatewayID reports whether this packet type carries the gateway identifier
// after the sequence number.
func (t PacketType) HasGatewayID() bool {
	switch t {
	case PushData, PullData, TxAck:
		return true
	}
	return false
}

// Protocol versions. v2 is what every current forwarder speaks; v1 is still
// seen on older hardware.
const (
	Version1       = 1
	Version2       = 2
	supportedFrom  = Version1
	supportedUntil = Version2
)

// IdentifierLen is the size of the gateway identifier for a protocol version:
// v1 carries a 6-byte MAC, v2 the full 8-byte EUI.
func IdentifierLen(version byte) (int, bool) {
	switch version {
	case Version1:
		return 6, true
	case Version2:
		return 8, true
	}
	return 0, false
}

// Header is a decoded Semtech UDP header.
type Header struct {
	Version  byte
	Type     PacketType
	Sequence uint16
	Gateway  lorawan.EUI64
	// MAC holds the v1 identifier. V1 forwarders announce a 6-byte MAC rather
	// than an EUI; the EUI is derived by prefixing two zero bytes, which keeps
	// one registry key shape for the whole fleet.
	MAC    [6]byte
	HasMAC bool
	// BodyStart is the offset of the JSON payload within the datagram, that is
	// the length of the header. It is 3 for the packets that carry no gateway
	// identifier — including PULL_RESP, which has no identifier but does have a
	// txpk body. A body-less acknowledgement simply ends at BodyStart.
	BodyStart int
}

// ParseHeader decodes a datagram header and reports where its JSON body starts.
// It does not touch the body, so a malformed body is this function's caller's
// problem — a gateway that sends a valid header with junk JSON must still get a
// PUSH_ACK, not silence.
func ParseHeader(pkt []byte) (Header, error) {
	if len(pkt) < 3 {
		return Header{}, fmt.Errorf("semtech: packet too short: %d bytes", len(pkt))
	}
	h := Header{
		Version:  pkt[0] >> 5,
		Type:     PacketType(pkt[0] & 0x1f),
		Sequence: binary.BigEndian.Uint16(pkt[1:3]),
	}
	if h.Version < supportedFrom || h.Version > supportedUntil {
		return Header{}, fmt.Errorf("semtech: unsupported protocol version %d (want %d or %d)",
			h.Version, supportedFrom, supportedUntil)
	}
	if !h.Type.IsKnown() {
		return Header{}, fmt.Errorf("semtech: unknown packet type 0x%02x", byte(h.Type))
	}
	if !h.Type.HasGatewayID() {
		// No identifier: the body, if any, starts right after the 3-byte
		// header. PULL_RESP lands here — a forwarder that never got its txpk
		// because we assumed "no identifier means no body" is a bug worth
		// remembering.
		h.BodyStart = 3
		return h, nil
	}

	idLen, _ := IdentifierLen(h.Version)
	if len(pkt) < 3+idLen {
		return Header{}, fmt.Errorf("semtech: %s v%d needs %d bytes of identifier, datagram has %d",
			h.Type, h.Version, idLen, len(pkt))
	}
	if h.Version == Version1 {
		copy(h.MAC[:], pkt[3:9])
		h.HasMAC = true
		h.Gateway = lorawan.EUI64{0, 0, h.MAC[0], h.MAC[1], h.MAC[2], h.MAC[3], h.MAC[4], h.MAC[5]}
		h.BodyStart = 9
		return h, nil
	}
	copy(h.Gateway[:], pkt[3:11])
	h.BodyStart = 11
	return h, nil
}

// IsKnown reports whether the packet type is one of the six defined types.
func (t PacketType) IsKnown() bool {
	switch t {
	case PushData, PushAck, PullData, PullResp, PullAck, TxAck:
		return true
	}
	return false
}

// MarshalHeader encodes the header. The caller must not set HasMAC: a v1
// datagram is written with the EUI field truncated to its low 6 bytes, which is
// the MAC the forwarder expects.
func MarshalHeader(h Header) []byte {
	out := make([]byte, 3, 11)
	out[0] = h.Version<<5 | byte(h.Type)
	binary.BigEndian.PutUint16(out[1:3], h.Sequence)
	if !h.Type.HasGatewayID() {
		return out
	}
	if h.Version == Version1 {
		return append(out, h.Gateway[2:]...)
	}
	return append(out, h.Gateway[:]...)
}

// BuildPacket assembles a header and an optional JSON body.
func BuildPacket(h Header, body []byte) []byte {
	out := MarshalHeader(h)
	return append(out, body...)
}

// String renders the header for logs, without the payload.
func (h Header) String() string {
	if h.Type.HasGatewayID() {
		return fmt.Sprintf("%s v%d seq=%d gw=%s", h.Type, h.Version, h.Sequence, h.Gateway)
	}
	return fmt.Sprintf("%s v%d seq=%d", h.Type, h.Version, h.Sequence)
}
