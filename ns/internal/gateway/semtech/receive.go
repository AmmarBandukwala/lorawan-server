package semtech

import (
	"context"
	"errors"
	"net"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// readLoop owns the socket until it is closed. Packets are dispatched to
// goroutines so one slow engine call cannot stall every other gateway, with
// s.sem bounding how many may be in flight.
func (s *Server) readLoop(ctx context.Context) {
	defer s.wg.Done()
	buf := make([]byte, 65536)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if s.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Error("udp read failed", "err", err)
			continue
		}
		// The next read reuses buf, so the datagram must be copied before it is
		// handed to another goroutine.
		pkt := append([]byte(nil), buf[:n]...)
		select {
		case s.sem <- struct{}{}:
		default:
			s.log.Warn("dropping gateway packet at the concurrency limit",
				"from", from.String(), "limit", cap(s.sem))
			continue
		}
		s.wg.Add(1)
		go func() {
			defer func() { <-s.sem; s.wg.Done() }()
			s.handlePacket(ctx, pkt, from)
		}()
	}
}

func (s *Server) handlePacket(ctx context.Context, pkt []byte, from *net.UDPAddr) {
	h, err := ParseHeader(pkt)
	if err != nil {
		// Nothing can be acknowledged: a reply would need a type and a sequence
		// number we could not read.
		s.log.Warn("ignoring malformed gateway packet", "from", from.String(), "err", err)
		return
	}
	switch h.Type {
	case PushData:
		s.handlePushData(ctx, h, pkt, from)
	case PullData:
		s.reg.touch(h.Gateway, from, h.Version)
		s.writeTo(ackPacket(PullAck, h), from)
	case TxAck:
		s.handleTxAck(h, pkt, from)
	default:
		// PUSH_ACK, PULL_ACK and PULL_RESP are ours; one arriving means a
		// forwarder echoed it back, which is harmless.
		s.log.Debug("ignoring downstream packet received from a gateway",
			"packet", h.Type.String(), "from", from.String())
	}
}

func (s *Server) handlePushData(ctx context.Context, h Header, pkt []byte, from *net.UDPAddr) {
	s.reg.touch(h.Gateway, from, h.Version)
	// Acknowledge before parsing. A forwarder that misses PUSH_ACK resends the
	// whole batch, and the engine would then redo work it already did.
	s.writeTo(ackPacket(PushAck, h), from)

	if h.BodyStart >= len(pkt) {
		return
	}
	batch, err := ParseUplinkBatch(pkt[h.BodyStart:])
	if err != nil {
		s.log.Warn("cannot parse PUSH_DATA", "gateway", h.Gateway, "err", err)
		return
	}
	if batch.Stats != nil {
		s.reg.noteStats(h.Gateway, *batch.Stats)
	}
	for _, rx := range batch.Uplinks {
		s.handleRx(ctx, h, rx, from)
	}
}

// handleRx converts one rxpk into an engine uplink.
func (s *Server) handleRx(ctx context.Context, h Header, rx RadioRx, from *net.UDPAddr) {
	raw, err := rx.PHYBytes()
	if err != nil {
		s.log.Warn("dropping rxpk", "gateway", h.Gateway, "err", err)
		return
	}
	reg, err := s.regionFor("")
	if err != nil {
		s.log.Error("cannot resolve the region for a received frame", "err", err)
		return
	}
	dr, err := rx.DataRateIndex(reg)
	if err != nil {
		s.log.Warn("dropping rxpk with an unknown data rate",
			"gateway", h.Gateway, "freq_hz", rx.FrequencyHz(), "err", err)
		return
	}
	ts, hasTS := rx.ConcentratorTimestamp()
	at, ok := rx.ArrivalTime()
	if !ok {
		at = s.now()
	}

	f := gateway.UplinkFrame{
		GatewayID:     h.Gateway,
		RFRegion:      s.cfg.Region,
		FrequencyHz:   rx.FrequencyHz(),
		DataRateIndex: dr,
		// The packet forwarder does not report which receive window a frame
		// arrived in, and a device repeating in RX2 is indistinguishable from a
		// first transmission. Every uplink is therefore treated as RX1 — which
		// is safe here because the engine answers in both windows anyway.
		RxWindow:   gateway.RxWindow1,
		UplinkID:   makeToken(h.Sequence, from, ts, hasTS),
		PHYPayload: raw,
		RSSI:       rx.RSSI,
		SNR:        rx.SNR,
		ReceivedAt: at,
	}
	if err := s.cfg.Handler.HandleUplink(ctx, f); err != nil {
		s.log.Error("engine failed to handle an uplink",
			"gateway", h.Gateway, "freq_hz", f.FrequencyHz, "err", err)
	}
}

func (s *Server) handleTxAck(h Header, pkt []byte, from *net.UDPAddr) {
	s.reg.touch(h.Gateway, from, h.Version)
	var body []byte
	if h.BodyStart < len(pkt) {
		body = pkt[h.BodyStart:]
	}
	rep, err := ParseTxAck(body)
	if err != nil {
		s.log.Warn("cannot parse TX_ACK", "gateway", h.Gateway, "err", err)
		return
	}
	if rep.Error == "" {
		s.log.Debug("gateway transmitted the downlink", "gateway", h.Gateway)
		return
	}
	// TOO_LATE, TX_COL and a few concentrator-specific strings are the only
	// evidence an operator gets that a downlink never went out.
	s.reg.noteTxAck(h.Gateway, rep.Error)
	s.log.Warn("gateway refused a downlink", "gateway", h.Gateway, "error", rep.Error)
}

// writeTo sends a datagram, logging rather than returning: an acknowledgement
// that fails to reach a gateway has nowhere to go.
func (s *Server) writeTo(pkt []byte, to *net.UDPAddr) {
	if _, err := s.conn.WriteToUDP(pkt, to); err != nil && !s.closed.Load() {
		s.log.Error("udp write failed", "to", to.String(), "err", err)
	}
}

// ackPacket builds a body-less acknowledgement echoing the request's sequence
// number, which is how a forwarder matches it to what it sent.
func ackPacket(t PacketType, req Header) []byte {
	return MarshalHeader(Header{
		Version:  req.Version,
		Type:     t,
		Sequence: req.Sequence,
	})
}

// regionFor resolves a band name, caching the tables. An empty name falls back
// to the configured region, then to the band the engine will use.
func (s *Server) regionFor(name string) (region.Region, error) {
	if name == "" {
		name = s.cfg.Region
	}
	if name == "" {
		return nil, errors.New("semtech: no region configured (set Config.Region)")
	}
	s.regionMu.Lock()
	defer s.regionMu.Unlock()
	if r, ok := s.regions[name]; ok {
		return r, nil
	}
	r, err := region.New(name)
	if err != nil {
		return nil, err
	}
	s.regions[name] = r
	return r, nil
}
