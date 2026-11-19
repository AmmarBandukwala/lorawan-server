package engine

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// HandleUplink is the network server's single entry point: hand it one radio
// reception and it authenticates the frame, updates session state, records
// traffic and schedules any downlink.
//
// An error return means an internal fault (storage unavailable, gateway
// rejected the frame). Protocol rejections — unknown device, bad MIC, replay —
// are recorded as events and return nil, because they are routine on real radio
// and must not stop a transport read loop.
func (e *Engine) HandleUplink(ctx context.Context, frame gateway.UplinkFrame) error {
	if len(frame.PHYPayload) == 0 {
		return errors.New("engine: empty PHYPayload")
	}
	if _, err := e.regionFor(frame.RFRegion); err != nil {
		return e.dropEvent(ctx, frame, nil, "unsupported_region", err.Error())
	}

	phy, err := lorawan.ParsePHYPayload(frame.PHYPayload)
	if err != nil {
		return e.dropEvent(ctx, frame, nil, "malformed_frame", err.Error())
	}
	if !phy.MHDR.MType.IsUplink() {
		return e.dropEvent(ctx, frame, phy, "not_an_uplink",
			fmt.Sprintf("gateway delivered a %s frame", phy.MHDR.MType))
	}

	// The same frame is usually received by several gateways. Answer it once.
	key := dedupKey(phy)
	if key != "" && e.isDuplicate(key) {
		return e.dropEvent(ctx, frame, phy, ReasonDuplicateFrame,
			"identical frame already processed within the dedup window")
	}

	switch phy.MHDR.MType {
	case lorawan.MTypeJoinRequest:
		return e.handleJoinRequest(ctx, frame, phy)
	case lorawan.MTypeUnconfirmedDataUp, lorawan.MTypeConfirmedDataUp:
		return e.handleDataUplink(ctx, frame, phy)
	default:
		return e.dropEvent(ctx, frame, phy, "unsupported_mtype",
			fmt.Sprintf("%s is not supported yet", phy.MHDR.MType))
	}
}

// dedupKey identifies a frame across gateways. Including the MIC makes the key
// collide only for byte-identical frames, which is exactly the multi-gateway
// duplicate case; a device sending two distinct frames never collides.
func dedupKey(phy *lorawan.PHYPayload) string {
	switch {
	case phy.JoinRequest != nil:
		return fmt.Sprintf("join:%s:%s:%d:%x",
			phy.JoinRequest.JoinEUI, phy.JoinRequest.DevEUI, phy.JoinRequest.DevNonce, phy.MIC)
	case phy.MAC != nil:
		return fmt.Sprintf("data:%s:%d:%x",
			phy.MAC.FHDR.DevAddr, phy.MAC.FHDR.FCnt, phy.MIC)
	default:
		return ""
	}
}

// isDuplicate reports whether this key was seen inside the dedup window and
// records it otherwise.
func (e *Engine) isDuplicate(key string) bool {
	now := e.now()

	e.mu.Lock()
	defer e.mu.Unlock()

	if at, ok := e.seen[key]; ok && now.Sub(at) < e.cfg.DedupWindow {
		return true
	}
	e.seen[key] = now
	if len(e.seen) > 4096 {
		for k, at := range e.seen {
			if now.Sub(at) >= e.cfg.DedupWindow {
				delete(e.seen, k)
			}
		}
	}
	return false
}

// generateDevAddr builds a device address whose top 7 bits carry the NetID
// prefix, so frames can be routed between networks (LoRaWAN 1.0.3 chapter 10).
// The remaining 25 bits are random; the caller-supplied session store is
// consulted so an address is never handed out twice.
func (e *Engine) generateDevAddr(ctx context.Context) (lorawan.DevAddr, error) {
	prefix := (uint32(e.cfg.NetID[2]) & 0x7F) << 25

	for range 16 {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, fmt.Errorf("engine: generate DevAddr: %w", err)
		}
		addr := lorawan.DevAddr(prefix | (binary.BigEndian.Uint32(b[:]) & 0x01FFFFFF))

		_, err := e.store.Sessions.SessionByDevAddr(ctx, addr)
		if errors.Is(err, ErrNotFound) {
			return addr, nil
		}
		if err != nil {
			return 0, err
		}
	}
	return 0, errors.New("engine: unable to allocate a free DevAddr")
}

// dataRateLabel renders a data-rate index for logs and events.
func dataRateLabel(r region.Region, index int) string {
	dr, err := r.DataRate(index)
	if err != nil {
		return fmt.Sprintf("DR%d(?)", index)
	}
	if dr.Modulation == region.ModulationLoRa {
		return fmt.Sprintf("DR%d(SF%dBW%d)", index, dr.SpreadingFactor, dr.BandwidthHz/1000)
	}
	return fmt.Sprintf("DR%d(%s)", index, dr.Modulation)
}
