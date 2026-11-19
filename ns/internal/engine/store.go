// Package engine is the Fieldnode network-server core. It consumes gateway
// uplinks, maintains device sessions, applies the LoRaWAN 1.0.3 MAC behaviour
// and emits scheduled downlinks.
//
// The package deliberately has no database, transport or HTTP dependency: state
// lives behind the DeviceRepo / SessionRepo / Recorder interfaces and radio I/O
// behind gateway.Gateway. That is what makes the whole join/uplink/downlink
// flow testable in a single process with a simulated device.
//
// The ambiguous cases the specification leaves open — frame-counter semantics,
// join-nonce handling, RX-window planning — are resolved the way widely
// deployed servers resolve them, so ordinary devices and gateways work without
// per-vendor special cases. THIRD_PARTY_NOTICES.md records what was consulted.
package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// ErrNotFound is returned by repositories when a device or session does not
// exist. Callers compare with errors.Is.
var ErrNotFound = errors.New("engine: not found")

// UseRegionDefault marks a device setting that falls back to the regional
// default instead of an explicit value.
const UseRegionDefault = -1

// Device is the provisioning record of an end-device: what the NS needs to
// authenticate it and to respect its capabilities. It covers the credentials, the
// radio limits and the RX-parameter overrides a device profile has to carry.
type Device struct {
	DevEUI  lorawan.EUI64
	JoinEUI lorawan.EUI64
	Name    string
	// Group is the dashboard's grouping label. The engine never reads it; it
	// lives on the device row so a device has one home rather than being split
	// between a protocol table and a dashboard table.
	Group   string
	Enabled bool

	// OTAA selects activation mode. When true the device joins with AppKey;
	// when false the ABP fields below must be populated.
	OTAA   bool
	AppKey lorawan.AES128Key

	// ABP credentials (only used when OTAA is false).
	DevAddr lorawan.DevAddr
	NwkSKey lorawan.AES128Key
	AppSKey lorawan.AES128Key

	// OTAA replay state, maintained by the join server.
	//
	// NextJoinNonce is handed out on the next join and increases by one per
	// join, which is what lets a device detect a replayed join-accept
	// (LoRaWAN 1.0.3 chapter 5.2). UsedDevNonces records every accepted
	// DevNonce per JoinEUI so a replayed join-request is refused.
	NextJoinNonce uint32
	UsedDevNonces map[lorawan.EUI64][]uint16

	// Capabilities. UseRegionDefault means "no device-specific limit".
	MaxDataRate int
	MinDataRate int
	MaxTxPower  int

	// DisableADR opts this device out of adaptive data-rate control. It is
	// phrased negatively so the zero value — and every device stored before
	// ADR existed — keeps ADR enabled, which is the LoRaWAN default.
	//
	// A device must also set FCtrl.ADR in its uplinks; a device that clears the
	// bit has decided to keep its own data rate and the server respects that.
	DisableADR bool

	// Overrides of the regional RX parameters. Rx1Delay 0 and
	// Rx2FrequencyHz 0 mean "use the region default"; Rx2DataRate uses
	// UseRegionDefault.
	Rx1Delay       int
	Rx2DataRate    int
	Rx2FrequencyHz uint32

	SupportsClassC bool
}

// NewOTAADevice returns an enabled OTAA device with regional defaults.
func NewOTAADevice(devEUI, joinEUI lorawan.EUI64, appKey lorawan.AES128Key) *Device {
	return &Device{
		DevEUI:      devEUI,
		JoinEUI:     joinEUI,
		Enabled:     true,
		OTAA:        true,
		AppKey:      appKey,
		MaxDataRate: UseRegionDefault,
		MinDataRate: UseRegionDefault,
		MaxTxPower:  UseRegionDefault,
		Rx2DataRate: UseRegionDefault,
	}
}

// NewABPDevice returns an enabled activation-by-personalisation device.
func NewABPDevice(devEUI, joinEUI lorawan.EUI64, devAddr lorawan.DevAddr, nwkSKey, appSKey lorawan.AES128Key) *Device {
	d := NewOTAADevice(devEUI, joinEUI, lorawan.AES128Key{})
	d.OTAA = false
	d.DevAddr = devAddr
	d.NwkSKey = nwkSKey
	d.AppSKey = appSKey
	return d
}

// DevNonceUsed reports whether a join-request with this DevNonce was already
// accepted for the given JoinEUI (LoRaWAN 1.0.3 chapter 6.2.4).
func (d *Device) DevNonceUsed(joinEUI lorawan.EUI64, devNonce uint16) bool {
	for _, n := range d.UsedDevNonces[joinEUI] {
		if n == devNonce {
			return true
		}
	}
	return false
}

// MarkDevNonceUsed records a DevNonce as consumed.
func (d *Device) MarkDevNonceUsed(joinEUI lorawan.EUI64, devNonce uint16) {
	if d.UsedDevNonces == nil {
		d.UsedDevNonces = map[lorawan.EUI64][]uint16{}
	}
	d.UsedDevNonces[joinEUI] = append(d.UsedDevNonces[joinEUI], devNonce)
}

// PendingDownlink is a downlink waiting for the next RX window. FRMPayload is
// kept in plaintext; encryption happens when the frame is built so a
// re-transmission can reuse the same frame counter.
type PendingDownlink struct {
	// FCntDown is 0 until the frame has actually been transmitted; a value
	// equal to Session.FCntDown means "already sent, not yet acknowledged"
	// and is retransmitted with the same counter.
	FCntDown    uint32
	FPort       *byte
	FRMPayload  []byte
	MACCommands lorawan.MACCommandSet
	// Confirmed requests a confirmed downlink, i.e. the device must answer
	// with a confirmed uplink.
	Confirmed bool
	CreatedAt time.Time
	Attempts  int
}

// ADRRequest is an outstanding LinkADRReq.
//
// The channel mask travels with it because the request has to be re-sent until
// the device answers, and re-sending the mask that was current when the decision
// was made is the only version the device can make sense of.
type ADRRequest struct {
	DataRate   int
	TxPower    int
	NbTrans    int
	ChMask     uint16
	ChMaskCntl byte
}

// payload renders the request as the on-air LinkADRReq payload.
func (r ADRRequest) payload() lorawan.LinkADRReqPayload {
	return lorawan.LinkADRReqPayload{
		DataRate:   byte(r.DataRate),
		TxPower:    byte(r.TxPower),
		ChMask:     r.ChMask,
		ChMaskCntl: r.ChMaskCntl,
		NbRep:      byte(r.NbTrans),
	}
}

// RX2Request is an outstanding RXParamSetupReq.
type RX2Request struct {
	FrequencyHz uint32
	DataRate    int
}

// Session is the LoRaWAN device session: keys, counters and MAC state.
type Session struct {
	DevEUI  lorawan.EUI64
	DevAddr lorawan.DevAddr

	NwkSKey lorawan.AES128Key
	AppSKey lorawan.AES128Key
	// KeyEpoch increments on every (re)join so stored uplinks can be tied to
	// the session that produced them.
	KeyEpoch uint32

	// FCntUp is the *next expected* uplink counter: after accepting counter N
	// the session stores N+1.
	FCntUp uint32
	// FCntDown is the last used downlink counter; 0 means none sent yet.
	FCntDown uint32
	// AFCntDown is the uplink counter that carried FCtrl.ACK, i.e. the
	// downlink counter the device acknowledged.
	AFCntDown uint32

	// MAC / RF state.
	DataRate       int
	TxPower        int
	NbTrans        int
	Rx1Delay       int
	Rx1DROffset    int
	Rx2DataRate    int
	Rx2FrequencyHz uint32
	Region         string

	// AwaitingLinkADRAns records that a LinkADRReq was sent, so the next
	// LinkADRAns belongs to it.
	AwaitingLinkADRAns bool
	// PendingADR holds the parameters of the LinkADRReq the device has not
	// answered yet. They only become session state once the device
	// acknowledges, so the server never believes the device is running a
	// data-rate it refused.
	PendingADR *ADRRequest
	// PendingRX2 holds an outstanding RXParamSetupReq.
	PendingRX2 *RX2Request

	// Radio metadata of the last accepted uplink.
	LastUplinkAt      time.Time
	LastDownlinkAt    time.Time
	LastRSSI          float64
	LastSNR           float64
	LastGatewayID     lorawan.EUI64
	LastFrequencyHz   uint32
	LastDataRateIndex int

	// Device status reported by DevStatusAns.
	HasDeviceStatus bool
	Battery         byte
	Margin          int8

	// --- ADR, channel plan and outstanding MAC requests ---------------------

	// UplinkHistory holds the last adrHistorySize accepted uplinks with the SNR
	// the network measured, oldest first. ADR decides on this window.
	UplinkHistory []UplinkHistoryItem

	// ChannelMask is the device's enabled uplink channels, index-aligned with
	// the band's channel list. nil means "band defaults", which keeps sessions
	// written before channel management existed from needing a migration.
	ChannelMask region.ChannelMask

	// UserChannels are the dynamic channels this device was told to add with
	// NewChannelReq, keyed by channel index. They belong to the device, not to
	// the band: sharing them between devices would let one device's plan steer
	// another's data rate. See region.WithUserChannels.
	UserChannels map[int]region.Channel

	// ADRAckCnt counts accepted uplinks since the last LinkADRReq went out.
	// RP002 gives the device a counter that makes it fall back to a lower data
	// rate when the network stops answering; sending a LinkADRReq — even one
	// that changes nothing — resets it.
	ADRAckCnt int

	// LinkADRRetries counts retransmissions of an unanswered LinkADRReq. RP002
	// caps the attempt at 32; past that the server stops asking instead of
	// spending every downlink on a request this device will not answer.
	LinkADRRetries int

	// LastADRBit is the FCtrl.ADR bit of the last accepted uplink: the device
	// telling us whether it is currently following ADR.
	LastADRBit bool

	// Outstanding requests. Each is attached to outgoing downlinks until the
	// matching answer arrives, then applied. They are separate from Pending,
	// which is the application's downlink queue: a device that is told to change
	// its radio settings must be told again if the frame is lost, while an
	// application payload is queued exactly once.
	PendingDutyCycle *DutyCycleRequest
	PendingTXParams  *TXParamsRequest
	PendingRX1Delay  *RX1DelayRequest
	PendingChannel   *ChannelRequest

	// Radio restrictions in force, applied from the answers above.
	// DutyCycle 0 means "no restriction beyond the band's own".
	DutyCycle        float64
	MaxEIRPIndex     int
	UplinkDwell400   bool
	DownlinkDwell400 bool

	Pending *PendingDownlink
}

// DutyCycleRequest is an outstanding DutyCycleReq. The index is the RP002
// duty-cycle table index, not a percentage.
type DutyCycleRequest struct {
	MaxDutyCycleIndex int `json:"max_duty_cycle_index"`
}

// TXParamsRequest is an outstanding TXParamSetupReq: the dwell-time and max
// EIRP settings of the band.
type TXParamsRequest struct {
	UplinkDwell400   bool `json:"uplink_dwell_400ms"`
	DownlinkDwell400 bool `json:"downlink_dwell_400ms"`
	MaxEIRPIndex     int  `json:"max_eirp_index"`
}

// RX1DelayRequest is an outstanding RXTimingSetupReq.
type RX1DelayRequest struct {
	DelaySeconds int `json:"delay_seconds"`
}

// ChannelRequest is an outstanding NewChannelReq: a user channel the device is
// being asked to add or modify.
type ChannelRequest struct {
	Index       int    `json:"index"`
	FrequencyHz uint32 `json:"frequency_hz"`
	MinDataRate int    `json:"min_data_rate"`
	MaxDataRate int    `json:"max_data_rate"`
}

// UplinkRecord is an accepted uplink as handed to storage.
type UplinkRecord struct {
	At             time.Time
	DevEUI         lorawan.EUI64
	DevAddr        lorawan.DevAddr
	KeyEpoch       uint32
	FCntUp         uint32
	FPort          int // -1 when the frame carries no FPort
	FRMPayload     []byte
	RawPHYPayload  []byte
	GatewayID      lorawan.EUI64
	FrequencyHz    uint32
	DataRateIndex  int
	RSSI           float64
	SNR            float64
	Confirmed      bool
	Retransmission bool

	// ACK is FCtrl.ACK of this frame: the device confirming the downlink whose
	// counter is stored in AFCntDown. Integrations report it so a consumer can
	// learn that a confirmed downlink landed without polling the session.
	ACK bool
}

// DownlinkRecord is a scheduled downlink as handed to storage.
type DownlinkRecord struct {
	At            time.Time
	DevEUI        lorawan.EUI64
	DevAddr       lorawan.DevAddr
	GatewayID     lorawan.EUI64
	Window        string
	FrequencyHz   uint32
	DataRateIndex int
	TxPowerdBm    int8
	FCntDown      uint32
	FPort         int // -1 when the frame carries no FPort
	FRMPayload    []byte
	RawPHYPayload []byte
	ACK           bool
}

// Event kinds recorded by the engine.
const (
	EventJoinRequest = "join_request"
	EventJoinAccept  = "join_accept"
	EventJoinReject  = "join_reject"
	EventUplink      = "uplink"
	EventDownlink    = "downlink"
	EventUplinkDrop  = "uplink_drop"
	EventMAC         = "mac"
	EventError       = "error"
)

// Reasons used with EventJoinReject and EventUplinkDrop.
const (
	ReasonUnknownDevice      = "unknown_device"
	ReasonDeviceDisabled     = "device_disabled"
	ReasonNotOTAA            = "not_otaa_device"
	ReasonJoinMICFailed      = "join_mic_failed"
	ReasonDevNonceReplay     = "dev_nonce_replay"
	ReasonUnknownDevAddr     = "unknown_dev_addr"
	ReasonUplinkMICFailed    = "uplink_mic_failed"
	ReasonFCntReplay         = "f_cnt_replay"
	ReasonFCntGapTooLarge    = "f_cnt_gap_too_large"
	ReasonDuplicateFrame     = "duplicate_frame"
	ReasonNoRXWindow         = "no_rx_window_available"
	ReasonDownlinkSendFailed = "downlink_send_failed"
	ReasonDownlinkTooLarge   = "downlink_too_large_for_window"
	// ReasonDutyCycleDenied means the band's duty cycle or dwell time left no
	// room for the transmission. It is a regulatory refusal, not a fault, and
	// the detail carries which limit was hit and when budget frees up again.
	ReasonDutyCycleDenied = "duty_cycle_denied"
)

// Event is a trace entry surfaced in the Fieldnode dashboard.
type Event struct {
	At        time.Time
	Kind      string
	Reason    string
	Detail    string
	DevEUI    lorawan.EUI64
	DevAddr   lorawan.DevAddr
	GatewayID lorawan.EUI64
	FCntUp    uint32
	FPort     int
}

// DeviceRepo stores device provisioning records.
type DeviceRepo interface {
	DeviceByDevEUI(ctx context.Context, devEUI lorawan.EUI64) (*Device, error)
	SaveDevice(ctx context.Context, d *Device) error

	// ListDevices returns the whole inventory, ordered by DevEUI. It exists for
	// the provisioning API and the operator's screens; see the Postgres
	// implementation for the size assumption behind the unbounded read.
	ListDevices(ctx context.Context) ([]*Device, error)

	// DeleteDevice removes a device and, with it, its session. Revoking a device
	// has to stop it talking, and a surviving session would let it keep doing
	// exactly that with keys the operator just withdrew.
	DeleteDevice(ctx context.Context, devEUI lorawan.EUI64) error
}

// SessionRepo stores device sessions.
type SessionRepo interface {
	SessionByDevAddr(ctx context.Context, devAddr lorawan.DevAddr) (*Session, error)
	SessionByDevEUI(ctx context.Context, devEUI lorawan.EUI64) (*Session, error)
	SaveSession(ctx context.Context, s *Session) error
	DeleteSession(ctx context.Context, devEUI lorawan.EUI64) error
}

// Recorder persists accepted traffic and engine events.
type Recorder interface {
	RecordUplink(ctx context.Context, r UplinkRecord) error
	RecordDownlink(ctx context.Context, r DownlinkRecord) error
	RecordEvent(ctx context.Context, e Event) error
}

// Store bundles the three persistence ports.
type Store struct {
	Devices  DeviceRepo
	Sessions SessionRepo
	Recorder Recorder
}

// Validate reports a missing port so misconfiguration fails at startup rather
// than on the first uplink.
func (s Store) Validate() error {
	if s.Devices == nil {
		return fmt.Errorf("engine: Store.Devices is nil")
	}
	if s.Sessions == nil {
		return fmt.Errorf("engine: Store.Sessions is nil")
	}
	if s.Recorder == nil {
		return fmt.Errorf("engine: Store.Recorder is nil")
	}
	return nil
}
