package pgstore

import (
	"encoding/json"
	"fmt"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// macState is the M4 session state — the ADR measurement window, the device's
// channel plan, and the MAC requests waiting for an answer — stored as one JSONB
// document in ns_sessions.mac_state.
//
// Why a document and not columns: none of it is ever queried. The dashboard reads
// the reporting tables; the engine reads the whole thing on every uplink and
// writes it back on every save. Giving each field a column would mean a migration
// every time the MAC engine learns something new, and a wider INSERT for no
// benefit. The JSON tags are the on-disk contract, so a session written by an
// older build still loads, and a field this build does not know is left alone.
type macState struct {
	UplinkHistory []engine.UplinkHistoryItem `json:"uplink_history,omitempty"`
	ChannelMask   region.ChannelMask         `json:"channel_mask,omitempty"`
	UserChannels  map[int]region.Channel     `json:"user_channels,omitempty"`

	ADRAckCnt        int     `json:"adr_ack_cnt,omitempty"`
	LinkADRRetries   int     `json:"link_adr_retries,omitempty"`
	LastADRBit       bool    `json:"last_adr_bit,omitempty"`
	DutyCycle        float64 `json:"duty_cycle,omitempty"`
	MaxEIRPIndex     int     `json:"max_eirp_index,omitempty"`
	UplinkDwell400   bool    `json:"uplink_dwell_400ms,omitempty"`
	DownlinkDwell400 bool    `json:"downlink_dwell_400ms,omitempty"`

	PendingDutyCycle *engine.DutyCycleRequest `json:"pending_duty_cycle,omitempty"`
	PendingTXParams  *engine.TXParamsRequest  `json:"pending_tx_params,omitempty"`
	PendingRX1Delay  *engine.RX1DelayRequest  `json:"pending_rx1_delay,omitempty"`
	PendingChannel   *engine.ChannelRequest   `json:"pending_channel,omitempty"`
}

// empty reports whether there is nothing to store. Returning NULL rather than
// '{}' keeps the vast majority of rows — devices that have never been asked to
// change anything — free of a document to parse on every uplink.
func (m macState) empty() bool {
	return m.UplinkHistory == nil && m.ChannelMask == nil && m.UserChannels == nil &&
		m.ADRAckCnt == 0 && m.LinkADRRetries == 0 && !m.LastADRBit &&
		m.DutyCycle == 0 && m.MaxEIRPIndex == 0 && !m.UplinkDwell400 && !m.DownlinkDwell400 &&
		m.PendingDutyCycle == nil && m.PendingTXParams == nil &&
		m.PendingRX1Delay == nil && m.PendingChannel == nil
}

// encodeMACState renders the session's MAC state, or nil when there is none.
func encodeMACState(session *engine.Session) ([]byte, error) {
	m := macState{
		UplinkHistory:    session.UplinkHistory,
		ChannelMask:      session.ChannelMask,
		UserChannels:     session.UserChannels,
		ADRAckCnt:        session.ADRAckCnt,
		LinkADRRetries:   session.LinkADRRetries,
		LastADRBit:       session.LastADRBit,
		DutyCycle:        session.DutyCycle,
		MaxEIRPIndex:     session.MaxEIRPIndex,
		UplinkDwell400:   session.UplinkDwell400,
		DownlinkDwell400: session.DownlinkDwell400,
		PendingDutyCycle: session.PendingDutyCycle,
		PendingTXParams:  session.PendingTXParams,
		PendingRX1Delay:  session.PendingRX1Delay,
		PendingChannel:   session.PendingChannel,
	}
	if m.empty() {
		return nil, nil
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("pgstore: encode mac state: %w", err)
	}
	return out, nil
}

// decodeMACState restores the document onto a session. A NULL column leaves the
// session at its zero values, which is what a device that has never been touched
// by the MAC engine should hold.
func decodeMACState(raw []byte, session *engine.Session) error {
	if len(raw) == 0 {
		return nil
	}
	var state macState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("pgstore: decode mac_state: %w", err)
	}
	session.UplinkHistory = state.UplinkHistory
	session.ChannelMask = state.ChannelMask
	session.UserChannels = state.UserChannels
	session.ADRAckCnt = state.ADRAckCnt
	session.LinkADRRetries = state.LinkADRRetries
	session.LastADRBit = state.LastADRBit
	session.DutyCycle = state.DutyCycle
	session.MaxEIRPIndex = state.MaxEIRPIndex
	session.UplinkDwell400 = state.UplinkDwell400
	session.DownlinkDwell400 = state.DownlinkDwell400
	session.PendingDutyCycle = state.PendingDutyCycle
	session.PendingTXParams = state.PendingTXParams
	session.PendingRX1Delay = state.PendingRX1Delay
	session.PendingChannel = state.PendingChannel
	return nil
}
