package lbs

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// dispatch routes one upstream message by its msgtype.
//
// An unreadable message is logged and skipped rather than treated as a protocol
// violation: a firmware that adds a field must not be able to disconnect a
// working gateway.
func (s *Server) dispatch(sess *session, payload []byte) {
	var envelope struct {
		Type string `json:"msgtype"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		s.log.Warn("cannot parse a message from a gateway", "gateway", sess.eui, "err", err)
		return
	}

	switch envelope.Type {
	case msgVersion:
		s.handleVersion(sess, payload)
	case msgUplinkData, msgJoinReq:
		s.handleUplink(sess, payload)
	case msgTxConfirm:
		s.handleTxConfirmation(sess, payload)
	case msgTimeSync:
		s.handleTimeSync(sess, payload)
	case msgRegister, msgPull, msgPush, msgIssed, msgMsed, msgStatT, msgStatS, msgPropDF:
		// Stations send these as part of normal operation. They carry nothing
		// this server acts on, and logging each at warn level would bury the
		// messages that matter.
		s.log.Debug("ignoring a gateway message this server does not act on",
			"gateway", sess.eui, "msgtype", envelope.Type)
	default:
		s.log.Debug("unknown gateway message type",
			"gateway", sess.eui, "msgtype", envelope.Type)
	}
}

// handleVersion answers the handshake with the configuration.
func (s *Server) handleVersion(sess *session, payload []byte) {
	var msg version
	if err := json.Unmarshal(payload, &msg); err != nil {
		s.log.Warn("cannot parse a version message", "gateway", sess.eui, "err", err)
		return
	}
	// Recorded before anything can fail: which firmware a station is on is the
	// first thing an operator needs, and the station only says it once.
	sess.noteVersion(msg)

	reg, err := s.region()
	if err != nil {
		// Without a band there is no honest router_config to send, and a station
		// given a wrong one transmits on wrong frequencies. Closing the
		// connection is the correct failure.
		s.log.Error("cannot build the router config: unknown region",
			"gateway", sess.eui, "region", s.cfg.Region, "err", err)
		sess.shutdown()
		return
	}

	joinEUIs := make([]eui, 0, len(s.cfg.JoinEUIs))
	for _, raw := range s.cfg.JoinEUIs {
		id, err := parseEUI(raw)
		if err != nil {
			s.log.Error("ignoring a malformed join EUI in the configuration",
				"value", raw, "err", err)
			continue
		}
		joinEUIs = append(joinEUIs, id)
	}

	cfg, err := buildRouterConfig(reg, s.cfg.NetID, joinEUIs,
		s.radioPlan, s.cfg.AllowUnlimitedTransmit, s.now())
	if err != nil {
		s.log.Error("cannot build the router config", "gateway", sess.eui, "err", err)
		sess.shutdown()
		return
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		s.log.Error("cannot encode the router config", "gateway", sess.eui, "err", err)
		return
	}
	if err := sess.enqueue(out); err != nil {
		s.log.Warn("cannot deliver the router config", "gateway", sess.eui, "err", err)
		return
	}
	s.log.Info("gateway handshake complete",
		"gateway", sess.eui, "station", msg.Station, "model", msg.Model,
		"firmware", msg.Firmware, "protocol", msg.Protocol, "region", cfg.Region)
}

// handleTimeSync answers a station's clock query.
func (s *Server) handleTimeSync(sess *session, payload []byte) {
	var req timeSyncRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		s.log.Warn("cannot parse a timesync message", "gateway", sess.eui, "err", err)
		return
	}
	res := timeSyncResponse{
		Type:    msgTimeSync,
		TxTime:  req.TxTime,
		MuxTime: float64(s.now().UnixNano()) / 1e9,
	}
	// XTime and GPSTime let the station tie its concentrator counter to
	// wall-clock time. Without a reported xtime there is nothing to correlate,
	// and sending a zero would tell the station its counter is at the epoch.
	if x, ok := sess.currentXTime(); ok {
		res.XTime = uint64(x)
	}
	out, err := json.Marshal(res)
	if err != nil {
		s.log.Error("cannot encode the timesync response", "gateway", sess.eui, "err", err)
		return
	}
	if err := sess.enqueue(out); err != nil {
		s.log.Debug("cannot deliver the timesync response", "gateway", sess.eui, "err", err)
	}
}

// handleUplink turns an updf or jreq into the frame the engine consumes.
func (s *Server) handleUplink(sess *session, payload []byte) {
	var msg uplink
	if err := json.Unmarshal(payload, &msg); err != nil {
		s.log.Warn("cannot parse an uplink", "gateway", sess.eui, "err", err)
		return
	}

	// The xtime is recorded before anything else can fail: it is what the next
	// downlink's schedule is computed from, and a frame we cannot decode should
	// still teach the server where the station's counter is.
	if msg.UpInfo.XTime != 0 {
		sess.noteXTime(xtime(msg.UpInfo.XTime))
	} else if msg.Radio.XTime != 0 {
		sess.noteXTime(xtime(msg.Radio.XTime))
	}
	// The radio context travels with the answer, so it is captured here for the
	// same reason the xtime is: a frame we cannot decode should still teach the
	// server which RF chain the station used.
	sess.rctx.Store(msg.UpInfo.RCtx)

	raw, err := frameBytes(msg)
	if err != nil {
		s.log.Warn("cannot recover a frame from an uplink message",
			"gateway", sess.eui, "msgtype", msg.Type, "err", err)
		return
	}

	x, _ := sess.currentXTime()
	f := gateway.UplinkFrame{
		GatewayID:     lorawan.EUI64(sess.eui),
		RFRegion:      s.cfg.Region,
		FrequencyHz:   uint32(msg.Radio.Frequency),
		DataRateIndex: msg.Radio.DataRate,
		// The station does not report which receive window a frame arrived in,
		// and a device repeating in RX2 is indistinguishable from a first
		// transmission. Every uplink is therefore treated as RX1, which is safe
		// because the engine answers in both windows. Same reasoning, and same
		// limitation, as the UDP transport.
		RxWindow:   gateway.RxWindow1,
		UplinkID:   uplinkToken(x),
		PHYPayload: raw,
		RSSI:       msg.Radio.RSSI,
		SNR:        msg.Radio.SNR,
		ReceivedAt: arrivalTime(msg, s.now),
	}

	if err := s.handler.HandleUplink(context.Background(), f); err != nil {
		s.log.Error("engine failed to handle an uplink",
			"gateway", sess.eui, "freq_hz", f.FrequencyHz, "err", err)
	}
}

// frameBytes recovers the raw PHYPayload.
//
// drawx is preferred because it is the frame as received: MIC included, no
// interpretation in between. The decoded fields are the fallback, used by builds
// that do not send drawx; reconstructing from them is byte-exact provided the
// field layout is right, and the engine's MIC check is what proves it is.
func frameBytes(msg uplink) ([]byte, error) {
	if msg.Drawx != "" {
		raw, err := decodeDrawx(msg.Drawx)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return nil, errors.New("lbs: drawx decoded to nothing")
		}
		return raw, nil
	}
	return reconstructFrame(msg)
}

// reconstructFrame rebuilds a PHYPayload from the decoded view.
//
// Multi-byte fields are little-endian on air, which is the part most likely to be
// got wrong: DevAddr and FCnt are transmitted least-significant byte first, while
// the JSON carries them as numbers with no byte order implied.
func reconstructFrame(msg uplink) ([]byte, error) {
	mic := make([]byte, 4)
	binary.LittleEndian.PutUint32(mic, uint32(msg.MIC))

	fopts, err := hexBytes(msg.FOpts)
	if err != nil {
		return nil, fmt.Errorf("lbs: FOpts: %w", err)
	}
	frm, err := hexBytes(msg.FRMPayload)
	if err != nil {
		return nil, fmt.Errorf("lbs: FRMPayload: %w", err)
	}

	switch {
	case lorawan.MType(msg.MHdr) == lorawan.MTypeJoinRequest:
		// Join-request: MHdr | JoinEUI(8, LE) | DevEUI(8, LE) | DevNonce(2, LE)
		// | MIC(4).
		if len(fopts) > 0 || len(frm) > 0 {
			return nil, errors.New("lbs: a join-request carries no FOpts or FRMPayload")
		}
		// A zero DevEUI would produce a 23-byte frame that fails its MIC for a
		// reason nothing in the log connects back to a missing field, so it is
		// refused here instead.
		if msg.DevEUI == (eui{}) {
			return nil, errors.New("lbs: a join-request needs DevEui to be rebuilt; " +
				"this station sent neither drawx nor a usable decoded view")
		}
		out := []byte{byte(msg.MHdr)}
		// Both EUIs are transmitted least-significant byte first.
		for i := len(msg.JoinEUI) - 1; i >= 0; i-- {
			out = append(out, msg.JoinEUI[i])
		}
		for i := len(msg.DevEUI) - 1; i >= 0; i-- {
			out = append(out, msg.DevEUI[i])
		}
		nonce := make([]byte, 2)
		binary.LittleEndian.PutUint16(nonce, uint16(msg.DevNonce))
		return append(append(out, nonce...), mic...), nil

	case msg.MHdr&0xE0 == 0x40 || msg.MHdr&0xE0 == 0x80:
		// Data frame: MHdr | DevAddr(4, LE) | FCtrl | FCnt(2, LE) | FOpts
		//              | [FPort] | FRMPayload | MIC(4).
		//
		// FPort is present exactly when there is an FRMPayload; a MAC-only frame
		// has neither, and inventing a port here would produce a frame the engine
		// reads as application traffic.
		out := []byte{byte(msg.MHdr)}
		addr := make([]byte, 4)
		binary.LittleEndian.PutUint32(addr, msg.DevAddr)
		out = append(out, addr...)
		out = append(out, byte(msg.FCtrl))
		fcnt := make([]byte, 2)
		binary.LittleEndian.PutUint16(fcnt, uint16(msg.FCnt))
		out = append(out, fcnt...)
		out = append(out, fopts...)
		if len(frm) > 0 {
			out = append(out, byte(msg.FPort))
			out = append(out, frm...)
		}
		return append(out, mic...), nil

	default:
		return nil, fmt.Errorf("lbs: cannot reconstruct MHdr 0x%02x without drawx", msg.MHdr)
	}
}

// region resolves the configured band, caching the table.
func (s *Server) region() (region.Region, error) {
	s.regionMu.Lock()
	defer s.regionMu.Unlock()
	if s.regionCache != nil {
		return s.regionCache, nil
	}
	r, err := region.New(s.cfg.Region)
	if err != nil {
		return nil, err
	}
	s.regionCache = r
	return r, nil
}
