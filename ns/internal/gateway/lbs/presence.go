package lbs

import (
	"fmt"
	"sort"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Transport name reported to the status endpoint and the dashboard, so an
// operator can tell which protocol a gateway reached the server through.
const transportName = "basic-station"

// stationInfo is what a station said about itself in the version handshake.
// Firmware and model are the two facts an operator needs first when a gateway
// misbehaves, and a station will not tell you again on request.
type stationInfo struct {
	Station  string
	Firmware string
	Model    string
	Protocol int
}

// lookup returns the live connection for a gateway, if there is one.
func (s *Server) lookup(id eui) (*session, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()
	return sess, ok
}

// noteVersion remembers what the station reported about itself.
func (sess *session) noteVersion(v version) {
	sess.info.Store(stationInfo{
		Station:  v.Station,
		Firmware: v.Firmware,
		Model:    v.Model,
		Protocol: v.Protocol,
	})
}

// stationProtocol names the LBS protocol revision a station reports, shaped like
// the UDP transport's "semtech-udp v2" so the two read the same way in the
// console. A station that has not sent its version — or a firmware that omits the
// field, where 0 means "unspecified" rather than revision zero — gets the bare
// transport name.
func stationProtocol(info stationInfo) string {
	if info.Protocol <= 0 {
		return transportName
	}
	return fmt.Sprintf("%s v%d", transportName, info.Protocol)
}

func (sess *session) infoSnapshot() stationInfo {
	if v, ok := sess.info.Load().(stationInfo); ok {
		return v
	}
	return stationInfo{}
}

// noteDownlinkSent records a dnmsg that reached the write queue and starts
// waiting for the station's confirmation.
func (sess *session) noteDownlinkSent(diid int64, device, window string, now time.Time) {
	sess.downlinksSent.Add(1)
	sess.tx.track(diid, device, window, now)
}

// noteDownlinkFailure records a downlink this server could not hand to the
// station at all. Unlike an unconfirmed downlink this one is known bad, which is
// why it is counted separately.
func (sess *session) noteDownlinkFailure(reason string) {
	sess.downlinkErrors.Add(1)
	sess.lastTxError.Store(reason)
}

// confirmDownlink marks a downlink as transmitted by the station.
func (sess *session) confirmDownlink(diid int64, now time.Time) bool {
	return sess.tx.confirm(diid, now)
}

// Presence reports every station this server has a live connection to.
//
// Only connected stations appear. The UDP transport accumulates a history of
// gateways it has heard from because a forwarder is stateless between packets;
// here the connection set *is* the gateway set, and listing stations that
// disconnected would show offline gateways with no last-seen history to explain
// themselves. The store keeps the longer history for the dashboard.
func (s *Server) Presence() []gateway.Presence {
	s.mu.RLock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.RUnlock()

	out := make([]gateway.Presence, 0, len(sessions))
	for _, sess := range sessions {
		info := sess.infoSnapshot()
		confirmed, unconfirmed, _ := sess.tx.snapshot()
		seen, _ := sess.lastSeen.Load().(time.Time)
		p := gateway.Presence{
			EUI:       lorawanEUI(sess.eui),
			Transport: transportName,
			Address:   sess.remote,
			// The station's own protocol revision, in the same shape the UDP
			// transport reports ("semtech-udp v2"). A station that has not sent
			// its version yet — or an older firmware that omits the field — gets
			// the bare transport name rather than a misleading "v0".
			Protocol:  stationProtocol(info),
			Connected: true,
			LastSeen:  seen,
			Packets:   sess.packets.Load(),

			DownlinksSent:       sess.downlinksSent.Load(),
			DownlinkErrors:      sess.downlinkErrors.Load(),
			DownlinkConfirm:     confirmed,
			DownlinkUnconfirmed: unconfirmed,
			Station:             info.Station,
			Firmware:            info.Firmware,
			Model:               info.Model,
		}
		if reason, ok := sess.lastTxError.Load().(string); ok {
			p.LastTxError = reason
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EUI.String() < out[j].EUI.String() })
	return out
}

// CanReach reports whether a station holds a live connection.
//
// This is a stronger claim than the UDP transport can make: the connection either
// exists or it does not, so a false here means a downlink genuinely cannot be
// delivered and the Router should not bother trying.
func (s *Server) CanReach(id lorawan.EUI64) bool {
	_, ok := s.lookup(euiFromLorawan(id))
	return ok
}

// OnlineTimeout is how long a station may stay silent before counting as
// offline. With a held connection this is really a TCP health question, so it is
// the read deadline the connection itself uses.
func (s *Server) OnlineTimeout() time.Duration { return s.cfg.ReadTimeout }

// GatewaysOnline is the count the dashboard shows: stations with a live
// connection right now.
func (s *Server) GatewaysOnline() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}
