// Package lbs speaks the LoRa Basics Station (LNS) protocol over WebSocket.
//
// # Why this transport exists
//
// The Semtech UDP packet forwarder assumes a gateway with a LAN or internet
// connection it can hold a UDP session through. Gateways whose backhaul is
// cellular or behind aggressive NAT lose that assumption: UDP mappings expire,
// addresses change, and the gateway spends more time re-registering than
// talking. LoRa Basics Station — the firmware on MultiTech Conduit, Kerlink
// IFoR, Rise-LR, Browan and the Microchip AW-NM3xx modules — speaks WebSocket
// instead, which survives those networks because the connection is TCP, kept
// alive by the protocol itself, and can traverse a TLS-terminating proxy on a
// normal port.
//
// A station that can only speak LBS cannot be used by a UDP-only server at all,
// so this is not an optimisation but a prerequisite for those gateways.
//
// # What is different from UDP, and why it matters here
//
// The concentrator timestamp is 64-bit `xtime` rather than 32-bit `tmst`. The
// low 48 bits are microseconds since the concentrator started; the top 16 bits
// are a session identifier that changes when the concentrator restarts, and
// which the network must echo back unchanged. That session field is the
// protocol's protection against scheduling a downlink against a counter that
// has already restarted — the failure mode the 32-bit UDP counter has to be
// inferred around.
//
// Timing is also expressed differently: a downlink carries the *uplink's*
// reference timestamp plus an `RxDelay` in whole seconds, and the station adds
// them. The server never computes a transmit instant, which removes a class of
// clock-skew bug but means the delay must be a whole number of seconds — true
// for every RX window in the bands this server implements.
//
// # Scope of this implementation
//
// Class-A uplink and downlink, join-request and data frames, transmission
// confirmations, and the time-transfer exchange. It does not implement the
// remote shell (`rmtsh`), firmware update, or multicast (`dnsched`), and it
// does not push a concentrator radio plan unless one is configured — see
// Config.RadioPlanPath for why that is opt-in.
package lbs

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Message types, upstream (station to network) and downstream.
const (
	msgVersion    = "version"
	msgJoinReq    = "jreq"
	msgUplinkData = "updf"
	msgPropDF     = "propdf"
	msgTxConfirm  = "dntxed"
	msgTimeSync   = "timesync"
	msgRegister   = "register"
	msgPull       = "pull"
	msgPush       = "push"
	msgIssed      = "issed"
	msgMsed       = "msed"
	msgStatT      = "tstat"
	msgStatS      = "sstat"

	msgRouterConfig = "router_config"
	msgDownlink     = "dnmsg"
)

// xtimeSessionMask selects the session identifier in the top 16 bits of xtime.
const xtimeSessionMask = uint64(0xFFFF) << 48

// xtimeCounterMask selects the concentrator microsecond counter in the low 48
// bits. 48 bits of microseconds is a span of about 8.9 years, so unlike the
// 32-bit UDP counter this never wraps in the life of a deployment.
const xtimeCounterMask = (uint64(1) << 48) - 1

// xtime is the protocol's concentrator timestamp, and the only timing currency
// that crosses this boundary.
//
// The layout is the one Semtech defines and every implementation follows:
//
//	bits 63..48  session identifier, assigned by the station's xtimer
//	bits 47..0   microseconds since the xtimer started
//
// A downlink must carry the session identifier of the uplink it answers. The
// station drops a downlink whose session does not match, because a mismatch
// means the concentrator restarted between the uplink and the downlink and the
// counter the schedule was computed from no longer refers to anything.
type xtime uint64

// counter is the microsecond half: the concentrator timestamp proper.
func (x xtime) counter() uint64 { return uint64(x) & xtimeCounterMask }

// session is the identifier that has to be echoed back.
func (x xtime) session() uint64 { return uint64(x) >> 48 }

// withCounter returns x with its counter half replaced, keeping the session
// bits. Replacing rather than rebuilding keeps the invariant that a downlink's
// session always came from the station.
func (x xtime) withCounter(us uint64) xtime {
	return xtime(uint64(x)&xtimeSessionMask | us&xtimeCounterMask)
}

// String renders both halves, because a log line with only the combined number
// forces whoever is debugging to do the shift by hand at 2 a.m.
func (x xtime) String() string {
	return fmt.Sprintf("xtime(s=%04x t=%d)", x.session(), x.counter())
}

// eui is a Basic Station 64-bit identifier.
//
// The wire form is `"eui-0016CD0000001234"`: a prefix naming the role, a dash,
// then 16 upper-case hex digits. The prefix matters to the station and not to
// us, so parsing accepts the prefixed form, a bare hex string, and the colon-
// or dash-separated spellings operators paste from device labels.
type eui [8]byte

func (e eui) String() string { return "eui-" + strings.ToUpper(hex.EncodeToString(e[:])) }

// lorawanEUI converts to the type the rest of the server uses for identifiers.
//
// The two are the same eight bytes in the same order — both are written
// most-significant byte first when rendered — so this is a copy, not a
// translation. Keeping it a named function rather than a bare conversion is what
// makes an accidental byte reversal show up as a reviewable one-line change
// instead of a gateway that appears under a different EUI than it registered.
func lorawanEUI(e eui) lorawan.EUI64 { return lorawan.EUI64(e) }

// euiFromLorawan is the inverse.
func euiFromLorawan(e lorawan.EUI64) eui { return eui(e) }

// parseEUI decodes any of the accepted spellings.
func parseEUI(s string) (eui, error) {
	raw := strings.TrimSpace(s)
	if i := strings.LastIndex(raw, "-"); i >= 0 && strings.EqualFold(raw[:i], "eui") {
		raw = raw[i+1:]
	}
	raw = strings.NewReplacer(":", "", "-", "", " ", "").Replace(raw)
	if len(raw) != 16 {
		return eui{}, fmt.Errorf("lbs: %q is not a 64-bit identifier", s)
	}
	var out eui
	if _, err := hex.Decode(out[:], []byte(raw)); err != nil {
		return eui{}, fmt.Errorf("lbs: %q is not hex: %w", s, err)
	}
	return out, nil
}

// UnmarshalJSON accepts the quoted identifier, and the bare number some builds
// send. Rejecting the numeric form would turn a working gateway into a
// connection that never registers.
func (e *eui) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		parsed, err := parseEUI(s)
		if err != nil {
			return err
		}
		*e = parsed
		return nil
	}
	var n uint64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("lbs: cannot read an identifier from %s", b)
	}
	out := eui{}
	for i := 7; i >= 0; i-- {
		out[i] = byte(n)
		n >>= 8
	}
	*e = out
	return nil
}

// MarshalJSON writes the canonical prefixed form.
func (e eui) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(e.String())), nil
}
