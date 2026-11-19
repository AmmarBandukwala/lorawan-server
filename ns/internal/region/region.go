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

// New constructs a region by common name. Supported: EU868, US915, AS923,
// AS923-2 (JS1, -1.8 MHz), AS923-3 (JS2, -6.6 MHz), AS923-4 (-5.9 MHz).
func New(commonName string) (Region, error) {
	switch strings.ToUpper(strings.ReplaceAll(commonName, "_", "-")) {
	case "EU868":
		return newEU868(), nil
	case "US915":
		return newUS915(), nil
	case "AS923":
		return newAS923("AS923", 0), nil
	case "AS923-2":
		return newAS923("AS923-2", 1_800_000), nil
	case "AS923-3":
		return newAS923("AS923-3", 6_600_000), nil
	case "AS923-4":
		return newAS923("AS923-4", 5_900_000), nil
	default:
		return nil, fmt.Errorf("unsupported region %q", commonName)
	}
}
