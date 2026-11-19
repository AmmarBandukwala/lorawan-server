package semtech

import (
	"encoding/binary"
	"hash/fnv"
	"net"
	"time"
)

// Uplink token.
//
// Semtech rxpk has no token field, so a server that wants to answer an uplink
// must build its own correlation handle: the engine echoes gateway.UplinkFrame
// .UplinkID back on the downlink, and everything the transport needs to time
// that transmission has to come from those 8 bytes.
//
// Layout, all big endian:
//
//	[0:2] sequence number of the PUSH_DATA that carried the uplink
//	[2:4] hash of the gateway's UDP address at receive time
//	[4:8] concentrator timestamp of the reception, microseconds (0 = unknown)
//
// The address hash is not a security check. It tells an operator that a
// gateway's address changed between the uplink and the downlink — the classic
// symptom of a NAT rebinding or a DHCP lease, which shows up as "uplinks seen,
// no downlinks received".
//
// The timestamp is stored as uint32 microseconds, the same width and unit the
// concentrator counter uses, so it wraps identically and the addition of the
// RX delay stays correct across the ~71 minute wrap.

func makeToken(sequence uint16, addr *net.UDPAddr, ts time.Duration, hasTS bool) [8]byte {
	var t [8]byte
	binary.BigEndian.PutUint16(t[0:2], sequence)
	binary.BigEndian.PutUint16(t[2:4], addrHash(addr))
	if hasTS {
		binary.BigEndian.PutUint32(t[4:8], uint32(ts/time.Microsecond))
	}
	return t
}

// parseToken reverses makeToken.
func parseToken(t [8]byte) (sequence uint16, hash uint16, ts time.Duration, hasTS bool) {
	sequence = binary.BigEndian.Uint16(t[0:2])
	hash = binary.BigEndian.Uint16(t[2:4])
	micros := binary.BigEndian.Uint32(t[4:8])
	return sequence, hash, time.Duration(micros) * time.Microsecond, micros != 0
}

// addrHash fingerprints a gateway's UDP address into 16 bits. hash/fnv has no
// 16-bit variant, so the 32-bit FNV-1a value is folded: both halves
// contribute, which keeps the collision rate low for the handful of addresses a
// deployment sees.
func addrHash(addr *net.UDPAddr) uint16 {
	if addr == nil {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(addr.String()))
	sum := h.Sum32()
	return uint16(sum>>16) ^ uint16(sum)
}

// downlinkTimestamp derives the concentrator timestamp a downlink must be
// transmitted at. ok is false when the uplink carried no concentrator timing,
// in which case the caller must fall back to an immediate transmission.
func downlinkTimestamp(token [8]byte, delay time.Duration) (tmst uint32, ok bool) {
	_, _, ts, hasTS := parseToken(token)
	if !hasTS {
		return 0, false
	}
	// Adding the delay and truncating to 32 bits reproduces the concentrator's
	// own wrap-around behaviour.
	return uint32((ts + delay) / time.Microsecond), true
}
