package lbs

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// handleDiscovery answers a station's "where is my LNS?" query.
//
// Discovery is how a station that knows only a bootstrap host finds its traffic
// endpoint. Answering it is what lets a fleet be reflashed with one address
// instead of one per gateway. A station can also be configured with the traffic
// URI directly, which is the common setup in a single-site deployment; this
// endpoint exists so that choice stays the operator's.
func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	var q discoverQuery
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&q); err != nil {
		s.writeDiscovery(w, discoverResponse{Error: "lbs: unreadable discovery query"})
		return
	}
	if q.Router == (eui{}) {
		// A zero EUI means the station does not know its own identity. Pointing
		// it at the traffic endpoint would produce a connection that can never
		// be attributed to a gateway, so this is refused rather than accepted.
		s.writeDiscovery(w, discoverResponse{Error: "lbs: discovery query has no router EUI"})
		return
	}
	if !s.authenticate(r) {
		s.log.Warn("rejected an unauthorised discovery query", "gateway", q.Router)
		s.writeDiscovery(w, discoverResponse{Router: q.Router, Error: "lbs: unauthorised"})
		return
	}

	s.writeDiscovery(w, discoverResponse{
		Router: q.Router,
		Muxs:   "muxs",
		// The host the station asked is the host it can reach, so it is echoed
		// back rather than a configured public name: that keeps a deployment
		// behind a proxy or on an internal address working without extra config.
		URI: "ws://" + r.Host + "/connectedtcp/" + q.Router.String(),
	})
}

func (s *Server) writeDiscovery(w http.ResponseWriter, res discoverResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		s.log.Error("cannot write the discovery response", "err", err)
	}
}

// handleTraffic upgrades a station connection and runs it until it closes.
//
// The gateway identity comes from the path the station was directed to, or the
// router query parameter for a station configured with a bare URI. The protocol
// does not bind identity to the transport credential, so a station can claim any
// EUI: the token is what bounds that, and it is why Config.Token exists.
func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	if !s.authenticate(r) {
		// 401 before the upgrade: a rejected upgrade still costs a TCP
		// handshake, but it does not cost a session slot.
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}
	id, err := s.sessionEUI(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written the response.
		s.log.Warn("websocket upgrade failed", "gateway", id, "remote", r.RemoteAddr, "err", err)
		return
	}

	sess := &session{
		eui:       id,
		conn:      conn,
		remote:    r.RemoteAddr,
		srv:       s,
		write:     make(chan []byte, 64),
		done:      make(chan struct{}),
		connected: s.now(),
		tx:        newTXTracker(s.cfg.TxConfirmWindow),
	}
	sess.lastSeen.Store(s.now())
	s.register(sess)
	defer s.unregister(sess)

	go sess.writeLoop()
	sess.readLoop()
}

// sessionEUI extracts the gateway identity from the request.
func (s *Server) sessionEUI(r *http.Request) (eui, error) {
	raw := r.URL.Path
	for _, prefix := range []string{"/connectedtcp/", "/ws/"} {
		raw = strings.TrimPrefix(raw, prefix)
	}
	if raw == "" || raw == r.URL.Path {
		raw = r.URL.Query().Get("router")
	}
	if raw == "" {
		return eui{}, errors.New("lbs: the connection URL must name the gateway")
	}
	return parseEUI(raw)
}

// register installs a session, replacing any older connection for the same
// gateway.
//
// Replacing rather than refusing is the right behaviour for a gateway that
// reconnects after a power cut while the server still holds the dead socket: the
// alternative is a gateway that cannot come back until a timeout expires, which
// looks like a server fault from the field.
func (s *Server) register(sess *session) {
	s.mu.Lock()
	previous, existed := s.sessions[sess.eui]
	s.sessions[sess.eui] = sess
	s.mu.Unlock()

	if existed {
		s.log.Warn("gateway reconnected while an older connection was open; replacing it",
			"gateway", sess.eui, "old_remote", previous.remote)
		previous.shutdown()
	}
	s.log.Info("gateway connected", "gateway", sess.eui, "remote", sess.remote)
}

// unregister removes a session, but only if it is still the current one: a
// replacement connection must not be deleted when the connection it replaced
// finally notices it is dead.
func (s *Server) unregister(sess *session) {
	s.mu.Lock()
	if current, ok := s.sessions[sess.eui]; ok && current == sess {
		delete(s.sessions, sess.eui)
	}
	s.mu.Unlock()
	sess.shutdown()
	s.log.Info("gateway disconnected", "gateway", sess.eui, "remote", sess.remote)
}

// writeLoop is the only writer on the connection, which is what keeps the frame
// stream intact.
func (sess *session) writeLoop() {
	for {
		select {
		case <-sess.done:
			return
		case payload := <-sess.write:
			if err := sess.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				sess.srv.log.Warn("cannot write to the gateway", "gateway", sess.eui, "err", err)
				sess.shutdown()
				return
			}
		}
	}
}

// enqueue hands bytes to the writer. A full queue means the station is not
// reading — usually a half-dead TCP connection the read deadline has not
// noticed yet — so the message is dropped and the connection torn down rather
// than allowed to grow without bound.
func (sess *session) enqueue(payload []byte) error {
	// Checked before the send rather than as another select case: with both
	// ready, select chooses at random, and a message handed to a connection that
	// is already gone would be reported as delivered.
	select {
	case <-sess.done:
		return errors.New("lbs: the gateway connection is closed")
	default:
	}
	select {
	case sess.write <- payload:
		return nil
	default:
		sess.shutdown()
		return errors.New("lbs: the gateway is not reading; connection closed")
	}
}

// readLoop consumes the connection until it fails or the deadline expires.
func (sess *session) readLoop() {
	conn := sess.conn
	conn.SetReadLimit(65536)
	_ = conn.SetReadDeadline(sess.srv.now().Add(sess.srv.cfg.ReadTimeout))

	// A pong is the station's keepalive. Refreshing the deadline here is what
	// keeps a quiet but healthy gateway connected.
	conn.SetPongHandler(func(string) error {
		sess.touch()
		return conn.SetReadDeadline(sess.srv.now().Add(sess.srv.cfg.ReadTimeout))
	})

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			if !sess.srv.closed.Load() {
				sess.srv.log.Debug("gateway connection closed", "gateway", sess.eui, "err", err)
			}
			return
		}
		sess.touch()
		sess.packets.Add(1)
		if err := conn.SetReadDeadline(sess.srv.now().Add(sess.srv.cfg.ReadTimeout)); err != nil {
			return
		}
		sess.srv.dispatch(sess, payload)
	}
}

func (sess *session) touch() { sess.lastSeen.Store(sess.srv.now()) }

// currentXTime returns the most recent xtime the station reported.
func (sess *session) currentXTime() (xtime, bool) {
	v, ok := sess.xtime.Load().(xtime)
	return v, ok && v != 0
}

// noteXTime records the station's xtime, which every downlink schedule and every
// timesync answer is built from.
func (sess *session) noteXTime(x xtime) {
	if x == 0 {
		return
	}
	previous, had := sess.currentXTime()
	sess.xtime.Store(x)
	// A changed session identifier means the concentrator restarted, so every
	// schedule computed from the old counter is now meaningless. That is worth a
	// log line rather than silence.
	if had && previous.session() != x.session() {
		sess.srv.log.Warn("gateway concentrator session changed; earlier schedules are void",
			"gateway", sess.eui,
			"old_session", previous.session(), "new_session", x.session())
	}
}

// online reports whether a gateway has been heard from recently, for the status
// endpoint and the dashboard.
func (s *Server) online(id eui, now time.Time) bool {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	seen, ok := sess.lastSeen.Load().(time.Time)
	return ok && now.Sub(seen) <= s.cfg.GatewayTimeout
}
