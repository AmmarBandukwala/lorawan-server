package lbs

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// The router_config message.
//
// A station will not exchange traffic until it receives one, so this is a
// handshake requirement rather than an optional convenience. What it contains is
// a deliberate choice, and worth stating:
//
// This server sends the parameters that govern *protocol* behaviour — region,
// NetIDs, join EUIs, the data-rate table, the RX parameters — and by default
// does not send `sx1301_conf`, the concentrator radio plan. That object
// describes a specific board's radios and channeliser; a wrong one
// mis-configures the radio in a way that is hard to diagnose and can silence the
// gateway entirely. Stations carry their own radio plan in their own
// configuration, which is where board-specific knowledge belongs. An operator
// who wants the server to own it can supply a file through
// Config.RadioPlanPath, and it is forwarded verbatim.
//
// The RX parameters are sent explicitly, and that is worth the extra fields.
// Without them a station applies its firmware's defaults, and those do not
// always agree with this server's band tables — the AS923 RX2 frequency in
// particular is a known point of disagreement between implementations. Telling
// the station which RX2 frequency and delay this server will answer on removes
// that whole class of "uplinks fine, RX2 never works" fault.

// routerConfig is the router_config message.
type routerConfig struct {
	Type   string `json:"msgtype"`
	Region string `json:"region"`

	// NetIDs are the network IDs this server joins devices into. An empty list
	// would tell the station to drop every join-request, so the configured NetID
	// is always included.
	NetID []int `json:"NetID"`
	// JoinEUIs restricts which join servers a device may reach. An empty list is
	// the documented "accept any" value, which is the right default while join
	// EUIs are per-device configuration rather than network-wide.
	JoinEUIs [][]int `json:"JoinEui"`

	FrequencyRange []uint64   `json:"freq_range"`
	DataRates      []dataRate `json:"DRs"`

	RX1Delay int    `json:"rx1dl"`
	RX2      rx2Cfg `json:"rx2"`

	// MuxTime is the server's clock when it answered. The station uses it to
	// judge how stale the rest of the configuration is.
	MuxTime float64 `json:"MuxTime"`

	// RadioPlan is the concentrator configuration, forwarded verbatim when an
	// operator supplies one and omitted otherwise. It is raw JSON precisely so
	// that this server holds no opinion about board-specific fields.
	RadioPlan json.RawMessage `json:"sx1301_conf,omitempty"`

	// NoDutyCycle and NoDwellTime tell the station not to enforce the limits
	// itself. They are only ever set alongside the server's own equivalent
	// override, which logs a warning when enabled.
	NoDutyCycle bool `json:"nodc,omitempty"`
	NoDwellTime bool `json:"nodwell,omitempty"`
}

// rx2Cfg is the fixed RX2 window the station must listen in.
type rx2Cfg struct {
	Frequency uint64 `json:"freq"` // Hz
	DataRate  int    `json:"dr"`
}

// dataRate is one `DRs` entry.
//
// Layout is [spreading factor, bandwidth kHz, bitrate]. For FSK the spreading
// factor must be 0 and the bandwidth field is ignored by the station, which is
// the opposite of what the field order suggests.
type dataRate struct {
	Index  int      `json:"DR"`
	Layout [3]int   `json:"layout"`
	Freqs  []uint64 `json:"freqs"`
}

// buildRouterConfig assembles the configuration for a band.
func buildRouterConfig(reg region.Region, netID [3]byte, joinEUIs []eui,
	radioPlan json.RawMessage, noLimits bool, now time.Time) (routerConfig, error) {

	def := reg.Defaults()

	// The data-rate table covers indices 0..15 and a band defines a subset. A
	// rate the band does not define is simply absent: listing one with a zeroed
	// layout would be worse than omitting it, because the station would read a
	// real but wrong modulation instead of "not available here".
	var drs []dataRate
	for i := 0; i <= 15; i++ {
		dr, err := reg.DataRate(i)
		if err != nil {
			continue
		}
		switch dr.Modulation {
		case region.ModulationFSK:
			drs = append(drs, dataRate{Index: i, Layout: [3]int{0, 0, dr.Bitrate}, Freqs: []uint64{0}})
		case region.ModulationLRFHSS:
			// LR-FHSS has no spreading factor and no representation in this
			// table. Omitting it means the station will not schedule it, which
			// matches this server refusing to downlink at it.
			continue
		default:
			if dr.SpreadingFactor == 0 || dr.BandwidthHz == 0 {
				continue
			}
			drs = append(drs, dataRate{
				Index:  i,
				Layout: [3]int{dr.SpreadingFactor, dr.BandwidthHz / 1000, 0},
				Freqs:  []uint64{0},
			})
		}
	}
	if len(drs) == 0 {
		return routerConfig{}, fmt.Errorf("lbs: %s exposes no representable data rate", reg.CommonName())
	}

	// freq_range bounds what the station may legally transmit. Deriving it from
	// the channel tables keeps it honest; a hand-written pair of numbers drifts
	// the first time a band's channels change.
	lo, hi := uint64(0), uint64(0)
	channels := append(append([]region.Channel{}, reg.UplinkChannels()...), reg.DownlinkChannels()...)
	for _, ch := range channels {
		if ch.FrequencyHz == 0 {
			continue
		}
		if lo == 0 || uint64(ch.FrequencyHz) < lo {
			lo = uint64(ch.FrequencyHz)
		}
		if uint64(ch.FrequencyHz) > hi {
			hi = uint64(ch.FrequencyHz)
		}
	}
	if lo == 0 {
		return routerConfig{}, fmt.Errorf("lbs: %s reports no channels", reg.CommonName())
	}

	joinList := make([][]int, 0, len(joinEUIs))
	for _, j := range joinEUIs {
		entry := make([]int, len(j))
		for i, b := range j {
			entry[i] = int(b)
		}
		joinList = append(joinList, entry)
	}

	return routerConfig{
		Type:           msgRouterConfig,
		Region:         reg.CommonName(),
		NetID:          []int{int(netID[0]), int(netID[1]), int(netID[2])},
		JoinEUIs:       joinList,
		FrequencyRange: []uint64{lo, hi},
		DataRates:      drs,
		RX1Delay:       int(def.RX1Delay / time.Second),
		RX2:            rx2Cfg{Frequency: uint64(def.RX2FrequencyHz), DataRate: def.RX2DataRate},
		MuxTime:        float64(now.UnixNano()) / 1e9,
		RadioPlan:      radioPlan,
		NoDutyCycle:    noLimits,
		NoDwellTime:    noLimits,
	}, nil
}
