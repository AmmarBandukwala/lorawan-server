// Package region implements the LoRaWAN regional parameter tables
// (RP002 1.0.3) for the bands Fieldnode operates in: EU868, US915 and the
// AS923 family. Every table cites the RP002 section it comes from, so a
// disagreement with a device or gateway can be settled against the
// specification rather than against another server's behaviour.
package region

import (
	"fmt"
	"strings"
	"time"
)

// Modulation kinds for a data rate.
type Modulation string

const (
	ModulationLoRa   Modulation = "lora"
	ModulationFSK    Modulation = "fsk"
	ModulationLRFHSS Modulation = "lr-fhss"
)

// DataRate describes one data-rate index of a band.
type DataRate struct {
	Index      int
	Uplink     bool
	Downlink   bool
	Modulation Modulation
	// LoRa fields:
	SpreadingFactor int
	BandwidthHz     int
	CodingRate      string
	// FSK field:
	Bitrate int
	// LRFHSS fields:
	OccupiedChannelWidthHz int
}

// Channel is one RF channel of a band.
type Channel struct {
	FrequencyHz uint32
	DataRates   []int
	Enabled     bool
	UserDefined bool
}

// Defaults are the band's RX and join-accept timing defaults.
type Defaults struct {
	RX1Delay          time.Duration
	RX2Delay          time.Duration
	RX2FrequencyHz    uint32
	RX2DataRate       int
	JoinAcceptDelay1  time.Duration
	JoinAcceptDelay2  time.Duration
	MinUplinkDataRate int
	MaxUplinkDataRate int
}

// Region is the read-only view of a band that the NS engine needs.
// Channel-mask mutation (LinkADR/NewChannel) is layered on top in the
// engine, not here, so Region values can be shared immutably.
type Region interface {
	CommonName() string
	Defaults() Defaults
	DataRate(index int) (DataRate, error)
	// RX1DataRate maps an uplink data rate + RX1DROffset to a downlink
	// data rate index.
	RX1DataRate(uplinkDataRate, rx1DROffset int) (int, error)
	UplinkChannels() []Channel
	DownlinkChannels() []Channel
	// RX1FrequencyHz maps the uplink frequency to the RX1 downlink
	// frequency (identity for EU868/AS923, channel mapping for US915).
	RX1FrequencyHz(uplinkFrequencyHz uint32) (uint32, error)
	TXPowerOffsets() []int
	DownlinkTXPowerdBm(frequencyHz uint32) int
	UplinkDwellTime() time.Duration
	DownlinkDwellTime() time.Duration
	// SubBands are the frequency ranges with their own duty-cycle limits, which
	// bound the server's own transmissions. There is deliberately no band-wide
	// duty-cycle accessor: EU868 limits differ by a factor of a hundred between
	// its ranges, so a single number for the band would be either wrong or
	// uselessly conservative. SubBandFor reports the range governing a frequency
	// and is false for a frequency the band does not define.
	SubBands() []SubBand
	SubBandFor(frequencyHz uint32) (SubBand, bool)
	// MaxDownlinkPayloadSize reports the RP002 maximum payload size for a
	// downlink data rate (MHDR+FHDR+FRMPayload+MIC). ok is false when the
	// band table has no entry for that data rate.
	MaxDownlinkPayloadSize(dataRateIndex int) (maxBytes int, ok bool)

	// Per-device channel state. A band owns the channel plan and the defaults;
	// a device's mask is a subset of it, changed by LinkADRReq and extended by
	// NewChannelReq. See mask.go for the semantics of each method.
	ChannelCount() int
	DefaultChannelMask() ChannelMask
	ApplyChannelMask(current ChannelMask, chMask uint16, chMaskCntl byte) (ChannelMask, error)
	EnabledUplinkChannels(m ChannelMask) []Channel
	// MaxDataRateForMask is the highest uplink data rate an enabled channel can
	// carry, or -1 when nothing is enabled. ADR must stay at or below it.
	MaxDataRateForMask(m ChannelMask) int
	SupportsDataRateOnChannel(channelIndex, dataRateIndex int) bool
}

// base holds the shared tables; each band file builds one.
type base struct {
	name             string
	defaults         Defaults
	dataRates        map[int]DataRate
	rx1DataTable     map[int][]int // uplink DR -> [RX1DROffset]downlink DR
	txPowerOffsets   []int
	uplinkChannels   []Channel
	downlinkChannels []Channel
	uplinkDwell      time.Duration
	downlinkDwell    time.Duration
	rx1SameFreq      bool // RX1 frequency equals uplink frequency
	// maskStyle decides how a LinkADRReq ChMask/ChMaskCntl pair is read; see
	// maskStyle in mask.go.
	maskStyle maskStyle
	// maxDownlinkPL maps a *downlink* data-rate index to the RP002 maximum
	// payload size (MPDS: air-link payload bytes, MHDR+FHDR+FRMPayload+MIC).
	// Bands that omit an entry report "unknown" and the engine falls back
	// to the RX2 window rather than risking an over-sized frame.
	maxDownlinkPL map[int]int
	// subBands are the regulatory ranges that bound our own transmissions.
	subBands []SubBand
}

func (b *base) CommonName() string { return b.name }
func (b *base) Defaults() Defaults { return b.defaults }

func (b *base) DataRate(index int) (DataRate, error) {
	dr, ok := b.dataRates[index]
	if !ok {
		return DataRate{}, fmt.Errorf("%s: unknown data-rate %d", b.name, index)
	}
	return dr, nil
}

func (b *base) RX1DataRate(uplinkDataRate, rx1DROffset int) (int, error) {
	row, ok := b.rx1DataTable[uplinkDataRate]
	if !ok {
		return 0, fmt.Errorf("%s: no RX1 data-rate for uplink DR %d", b.name, uplinkDataRate)
	}
	if rx1DROffset < 0 || rx1DROffset >= len(row) {
		return 0, fmt.Errorf("%s: RX1DROffset %d out of range", b.name, rx1DROffset)
	}
	return row[rx1DROffset], nil
}

func (b *base) UplinkChannels() []Channel        { return b.uplinkChannels }
func (b *base) DownlinkChannels() []Channel      { return b.downlinkChannels }
func (b *base) TXPowerOffsets() []int            { return b.txPowerOffsets }
func (b *base) UplinkDwellTime() time.Duration   { return b.uplinkDwell }
func (b *base) DownlinkDwellTime() time.Duration { return b.downlinkDwell }
func (b *base) SubBands() []SubBand              { return b.subBands }

func (b *base) SubBandFor(frequencyHz uint32) (SubBand, bool) {
	return subBandFor(b.subBands, frequencyHz)
}

func (b *base) MaxDownlinkPayloadSize(dataRateIndex int) (int, bool) {
	n, ok := b.maxDownlinkPL[dataRateIndex]
	return n, ok
}

func (b *base) RX1FrequencyHz(uplinkFrequencyHz uint32) (uint32, error) {
	if b.rx1SameFreq {
		return uplinkFrequencyHz, nil
	}
	idx, err := b.uplinkChannelIndex(uplinkFrequencyHz)
	if err != nil {
		return 0, err
	}
	rx1Idx, err := b.rx1ChannelIndex(idx)
	if err != nil {
		return 0, err
	}
	if rx1Idx >= len(b.downlinkChannels) {
		return 0, fmt.Errorf("%s: RX1 channel %d out of range", b.name, rx1Idx)
	}
	return b.downlinkChannels[rx1Idx].FrequencyHz, nil
}

// rx1ChannelIndex maps an uplink channel index to a downlink channel
// index; overridden by US915.
func (b *base) rx1ChannelIndex(uplinkChannelIndex int) (int, error) {
	return uplinkChannelIndex, nil
}

func (b *base) uplinkChannelIndex(frequencyHz uint32) (int, error) {
	for i, c := range b.uplinkChannels {
		if c.FrequencyHz == frequencyHz {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%s: unknown uplink frequency %d Hz", b.name, frequencyHz)
}

// registry is the set of bands this build implements, and the only place that
// knows it. New and Names both read it, so the list a client is offered cannot
// drift from the list the server accepts — the failure mode that made the
// console offer three bands while the server accepted six.
//
// Order is the order the console should offer them in: the bands a Fieldnode
// deployment is most likely to use first.
var registry = []struct {
	name string
	make func() Region
}{
	{"US915", func() Region { return newUS915() }},
	{"EU868", func() Region { return newEU868() }},
	{"AS923", func() Region { return newAS923("AS923", 0) }},
	{"AS923-2", func() Region { return newAS923("AS923-2", 1_800_000) }},
	{"AS923-3", func() Region { return newAS923("AS923-3", 6_600_000) }},
	{"AS923-4", func() Region { return newAS923("AS923-4", 5_900_000) }},
}

// Names returns the canonical names of every implemented band, in the order the
// console should offer them.
//
// This is the source of truth the operator API and the dashboard read, rather
// than each keeping its own list. A region added here becomes configurable
// everywhere; a region removed here stops being offered rather than lingering in
// a UI dropdown that the server would reject.
func Names() []string {
	out := make([]string, 0, len(registry))
	for _, r := range registry {
		out = append(out, r.name)
	}
	return out
}

// DefaultRegion is the band a deployment runs when nothing says otherwise.
//
// It is a product decision rather than a property of the band plans, but it
// lives here because this is the only package every consumer already imports,
// and a default repeated in the engine, the process config, Compose and the
// dashboard is a default that eventually disagrees with itself.
//
// US915 is the default because Fieldnode's deployments are in the US. Changing
// it here changes the fallback everywhere; it does not disturb a running
// network, which either sets NS_REGION explicitly or has sessions that carry
// the band they joined under.
const DefaultRegion = "US915"

// Supported reports whether name is an implemented band, accepting the same
// spellings New does.
func Supported(name string) bool {
	_, err := New(name)
	return err == nil
}

// New builds a band plan from its common name.
func New(commonName string) (Region, error) {
	wanted := normalizeName(commonName)
	for _, r := range registry {
		if normalizeName(r.name) == wanted {
			return r.make(), nil
		}
	}
	return nil, fmt.Errorf("unsupported region %q (supported: %s)",
		commonName, strings.Join(Names(), ", "))
}

// normalizeName folds the spellings an operator plausibly types into a .env file
// or a console field onto a comparable form: upper case, with the separators
// removed.
//
// Removing rather than converting the separator is what makes both US_915 and
// AS923_2 work. Converting underscores to hyphens fixes the second and breaks
// the first, because US915 has no separator to begin with. No two band names
// collide once separators are dropped, which TestNamesMatchConstructor keeps
// true as bands are added.
func normalizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(name)) {
		if r == '-' || r == '_' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
