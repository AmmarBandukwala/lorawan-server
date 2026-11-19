package semtech

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// SendDownlink transmits a downlink by pushing a PULL_RESP to the address the
// gateway last contacted us from.
//
// The protocol has no connection: a forwarder sends PULL_DATA at a fixed
// interval, which both refreshes that address and keeps its NAT mapping alive.
// A gateway that has never been heard from therefore cannot be reached, and
// saying so beats dropping the frame in silence.
//
// Timing: when the uplink carried a concentrator timestamp the downlink gets an
// absolute tmst, so the concentrator holds the frame until the exact slot.
// Otherwise the only field every forwarder understands is imme, and the frame
// goes out as soon as it arrives, which can miss a tight RX1 window. The engine
// answers in both windows, so RX2 stays the safety net; M6 validates the timing
// path per concentrator family.
//
// The context is deliberately unused: a UDP write is not cancellable, and the
// engine hands down a context that outlives the request that scheduled it.
func (s *Server) SendDownlink(_ context.Context, f gateway.DownlinkFrame) error {
	if s.conn == nil {
		return errors.New("semtech: server is not started")
	}
	rec, ok := s.reg.lookup(f.GatewayID)
	if !ok {
		return fmt.Errorf("semtech: gateway %s has never been heard from; "+
			"it must send PULL_DATA before it can receive downlinks", f.GatewayID)
	}
	addr, err := net.ResolveUDPAddr("udp", rec.Addr)
	if err != nil {
		return fmt.Errorf("semtech: gateway %s address %q: %w", f.GatewayID, rec.Addr, err)
	}
	if _, wantHash, _, _ := parseToken(f.UplinkID); wantHash != 0 && wantHash != addrHash(addr) {
		s.log.Warn("gateway address changed between uplink and downlink; using the newest one",
			"gateway", f.GatewayID, "uplink_addr_hash", wantHash, "current_addr", rec.Addr)
	}

	reg, err := s.regionFor(f.RFRegion)
	if err != nil {
		return fmt.Errorf("semtech: region for the downlink to %s: %w", f.GatewayID, err)
	}
	modu, datr, codr, err := DataRateStrings(reg, f.DataRateIndex)
	if err != nil {
		return fmt.Errorf("semtech: downlink data rate for %s: %w", f.GatewayID, err)
	}

	tx := DownlinkCommand{
		FrequencyMHZ:       region.HertzToMHz(f.FrequencyHz),
		PowerdBm:           f.TxPowerdBm,
		Modulation:         modu,
		DataRate:           datr,
		CodingRate:         codr,
		InvertPolarization: true, // LoRaWAN always inverts downlink polarity
		Size:               len(f.PHYPayload),
		Data:               strings.ToUpper(hex.EncodeToString(f.PHYPayload)),
	}
	if tmst, okTS := downlinkTimestamp(f.UplinkID, f.Delay); okTS {
		tx.TimestampUS = &tmst
	} else {
		tx.Immediate = true
		s.log.Warn("uplink carried no concentrator timestamp; transmitting immediately",
			"gateway", f.GatewayID, "window", f.Window.String(), "delay", f.Delay)
	}

	body, err := MarshalDownlink(tx)
	if err != nil {
		return err
	}
	hdr := Header{
		Version:  wireVersion(rec.ProtocolVersion),
		Type:     PullResp,
		Sequence: uint16(s.seq.Add(1)),
	}
	if _, err := s.conn.WriteToUDP(BuildPacket(hdr, body), addr); err != nil {
		return fmt.Errorf("semtech: send PULL_RESP to %s: %w", f.GatewayID, err)
	}
	s.reg.noteDownlinkSent(f.GatewayID)
	s.log.Info("downlink dispatched",
		"gateway", f.GatewayID, "window", f.Window.String(),
		"freq_hz", f.FrequencyHz, "datr", datr, "power_dbm", f.TxPowerdBm,
		"bytes", tx.Size, "timed", tx.TimestampUS != nil)
	return nil
}

// wireVersion keeps the reply on the protocol version the gateway speaks, so a
// v1 forwarder is not handed a v2 header it will misread.
func wireVersion(v byte) byte {
	if v == Version1 || v == Version2 {
		return v
	}
	return Version2
}
