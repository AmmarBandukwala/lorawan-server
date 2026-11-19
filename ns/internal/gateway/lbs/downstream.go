package lbs

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
)

// Downlink scheduling.
//
// The protocol inverts the UDP transport's model, and the difference is worth
// being precise about because it is where Basic Station downlinks go wrong. With
// UDP the network computes an absolute concentrator timestamp and hands it over.
// Here the network hands the station the *uplink's* reference timestamp plus a
// delay in whole seconds, and the station does the addition against its own
// counter. The server never needs to know where the station's clock is, which
// removes clock skew as a failure mode — at the cost of one-second granularity,
// which every RX window in the bands this server supports happens to fit.
//
// The reference timestamp must carry the session identifier of the uplink being
// answered. A station drops a downlink whose session bits do not match its
// current ones, because a mismatch means the concentrator restarted and the
// counter the schedule was computed from no longer refers to anything.

// deviceClassA and deviceClassC are the protocol's `dC` values. A class-A
// downlink is one the station must place relative to an uplink; class C is one it
// should emit at the next free moment.
const (
	deviceClassA = 0
	deviceClassC = 2
)

// downlinkPriority is the `priority` value sent on every dnmsg.
//
// The field is a hint about which of several queued transmissions a station
// should favour, and its numeric meaning is not fixed by any published
// conformance document. This server sends the value The Things Stack has sent to
// real stations in production for years rather than inventing one: a number that
// deployed firmware is known to accept beats a number that reads better. M6
// verifies it against each target station family.
const downlinkPriority = 25

// SendDownlink transmits a downlink by pushing a dnmsg to a connected station.
//
// Unlike the UDP transport there is no address to look up: the connection *is*
// the route, and it exists only while the station holds it. A station that has
// dropped off cannot be reached at all, which is stated rather than papered over
// because "the gateway is offline" is the answer an operator needs.
func (s *Server) SendDownlink(_ context.Context, f gateway.DownlinkFrame) error {
	if s.closed.Load() {
		return errors.New("lbs: the server is shutting down")
	}
	id := euiFromLorawan(f.GatewayID)
	sess, ok := s.lookup(id)
	if !ok {
		return fmt.Errorf("lbs: station %s is not connected; "+
			"it must hold a WebSocket connection to receive downlinks", id)
	}

	ref, err := s.transmitReference(sess, f)
	if err != nil {
		return fmt.Errorf("lbs: cannot schedule a downlink to %s: %w", id, err)
	}
	delaySeconds := wholeSecondDelay(f.Delay)

	dn := downlink{
		Type:     msgDownlink,
		DevEUI:   f.DeviceEUI.String(),
		Class:    deviceClassA,
		DIID:     sess.diid.Add(1),
		PDU:      hex.EncodeToString(f.PHYPayload),
		Priority: downlinkPriority,
		RCtx:     sess.rctx.Load(),
		MuxTime:  float64(s.now().UnixNano()) / 1e9,

		RxDelay: delaySeconds,
		Rx1DR:   f.DataRateIndex,
		Rx1Freq: uint64(f.FrequencyHz),
		XTime:   uint64(ref),
	}
	// The field names say RX1 but they mean "this transmission's data rate and
	// frequency", so an RX2 downlink needs no special encoding beyond its delay.
	// What does change is the device class: a manual-window frame is a class-C or
	// late re-transmission, and the station treats those differently when it
	// decides whether to hold or drop the frame.
	if f.Window == gateway.RxWindowManual {
		dn.Class = deviceClassC
	}

	out, err := json.Marshal(dn)
	if err != nil {
		return fmt.Errorf("lbs: cannot encode the dnmsg: %w", err)
	}
	if err := sess.enqueue(out); err != nil {
		sess.noteDownlinkFailure("the station is not reading the connection")
		return fmt.Errorf("lbs: send dnmsg to %s: %w", id, err)
	}
	sess.noteDownlinkSent(dn.DIID, f.DeviceEUI.String(), f.Window.String(), s.now())

	s.log.Info("downlink dispatched",
		"gateway", id, "device", f.DeviceEUI, "window", f.Window.String(),
		"freq_hz", f.FrequencyHz, "dr", f.DataRateIndex, "delay_s", delaySeconds,
		"ref", ref.String(), "diid", dn.DIID, "bytes", len(f.PHYPayload))
	return nil
}

// transmitReference picks the xtime a dnmsg is anchored to.
//
// The uplink's own xtime is the correct anchor and travels in the transport
// token, which is why that token carries the full 64 bits here. When the engine
// answers a frame whose token holds no xtime — one received before this server
// learned the station's counter — the most recently reported xtime is the next
// best thing, and for a class-C transmission it is the only option.
func (s *Server) transmitReference(sess *session, f gateway.DownlinkFrame) (xtime, error) {
	if f.Window != gateway.RxWindowManual {
		if ref := xTimeFromToken(f.UplinkID); ref != 0 {
			return ref, nil
		}
	}
	if current, ok := sess.currentXTime(); ok {
		if f.Window != gateway.RxWindowManual {
			s.log.Warn("uplink carried no xtime; anchoring the downlink to the station's latest one",
				"gateway", sess.eui, "window", f.Window.String(), "current", current.String())
		}
		return current, nil
	}
	return 0, errors.New("the station has never reported a concentrator timestamp, " +
		"so no downlink can be scheduled against its clock")
}

// wholeSecondDelay converts the engine's delay into the protocol's integer
// seconds.
//
// Rounding up rather than down is deliberate: transmitting a fraction of a second
// early means the station emits before the device opens its window and the frame
// is lost with no error anywhere, while arriving marginally late still lands
// inside an RX window that is open.
func wholeSecondDelay(delay time.Duration) int {
	if delay <= 0 {
		return 0
	}
	return int(math.Ceil(delay.Seconds()))
}

// handleTxConfirmation records a dntxed: the station saying it transmitted a
// dnmsg.
//
// This is the protocol's only transmission acknowledgement, and it is the
// difference between "the server sent a downlink" and "the gateway radiated it".
// A downlink that never gets one is either lost in the network or was dropped by
// the station for a reason it does not report, so the absence is counted rather
// than ignored.
func (s *Server) handleTxConfirmation(sess *session, payload []byte) {
	var msg txConfirmation
	if err := json.Unmarshal(payload, &msg); err != nil {
		s.log.Warn("cannot parse a transmission confirmation", "gateway", sess.eui, "err", err)
		return
	}
	if sess.confirmDownlink(msg.DIID, s.now()) {
		s.log.Info("gateway transmitted the downlink",
			"gateway", sess.eui, "diid", msg.DIID, "device", msg.DevEUI)
		return
	}
	// An unknown diid is usually a station that restarted and restarted its
	// numbering, or a confirmation for a downlink already counted as unconfirmed.
	s.log.Debug("transmission confirmation for a downlink this server has no record of",
		"gateway", sess.eui, "diid", msg.DIID)
}
