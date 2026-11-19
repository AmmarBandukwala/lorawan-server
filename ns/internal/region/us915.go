package region

import (
	"fmt"
	"time"
)

// US915 (RP002 1.0.3 §4): 64x125 kHz + 8x500 kHz uplink channels,
// 8x500 kHz downlink channels, RX1 on downlink channel (uplink % 8).

type us915 struct{ base }

// ApplyChannelMask implements the US915 reading of ChMaskCntl, which differs
// from every other band in this package and from the generic block reading for
// two of its values (RP002 5.5.2.2 for US915):
//
//	0b000..0b101  address 16 of the 64 125 kHz channels
//	0b110         enable all 64 125 kHz channels; ChMask bits 0..7 address the
//	              eight 500 kHz channels (64..71)
//	0b111         disable all 64 125 kHz channels; bits 0..7 again address the
//	              500 kHz channels
//
// Translating 0b110/0b111 as a block selector would address channels 96..111,
// which do not exist, and silently leave a device with no enabled uplink
// channel — the classic "joins fine, never heard again" US915 bug.
func (r *us915) ApplyChannelMask(current ChannelMask, chMask uint16, chMaskCntl byte) (ChannelMask, error) {
	if chMaskCntl != 0b110 && chMaskCntl != 0b111 {
		return r.base.applyBlockMask(current.Clone(), chMask, chMaskCntl)
	}

	out := current.Clone()
	if out == nil {
		out = r.base.DefaultChannelMask()
	}
	// 0b110 turns the 125 kHz block on, 0b111 turns it off.
	for i := 0; i < 64 && i < len(out); i++ {
		out[i] = chMaskCntl == 0b110
	}
	// Either way the low 8 bits now mean the 500 kHz block.
	for i := 0; i < 8; i++ {
		idx := 64 + i
		on := chMask&(1<<i) != 0
		if idx >= len(out) {
			if on {
				return out, fmt.Errorf("region: US915 has no channel %d", idx)
			}
			continue
		}
		out[idx] = on
	}
	return out, nil
}

func newUS915() *us915 {
	lora := func(idx, sf int, up, down bool) DataRate {
		return DataRate{Index: idx, Uplink: up, Downlink: down,
			Modulation: ModulationLoRa, SpreadingFactor: sf, BandwidthHz: 500_000, CodingRate: "4/5"}
	}
	up125 := func(idx, sf int) DataRate {
		return DataRate{Index: idx, Uplink: true, Downlink: false,
			Modulation: ModulationLoRa, SpreadingFactor: sf, BandwidthHz: 125_000, CodingRate: "4/5"}
	}

	var up, down []Channel
	for i := range 64 { // 902.3 + i*0.2 MHz, DR0-3
		up = append(up, Channel{FrequencyHz: 902_300_000 + uint32(i)*200_000,
			DataRates: []int{0, 1, 2, 3}, Enabled: true})
	}
	for i := range 8 { // 903.0 + i*1.6 MHz, DR4-6
		up = append(up, Channel{FrequencyHz: 903_000_000 + uint32(i)*1_600_000,
			DataRates: []int{4, 5, 6}, Enabled: true})
	}
	for i := range 8 { // 923.3 + i*0.6 MHz, DR8-13
		down = append(down, Channel{FrequencyHz: 923_300_000 + uint32(i)*600_000,
			DataRates: []int{8, 9, 10, 11, 12, 13}, Enabled: true})
	}

	return &us915{base{
		name: "US915",
		defaults: Defaults{
			RX1Delay:          time.Second,
			RX2Delay:          2 * time.Second,
			RX2FrequencyHz:    923_300_000,
			RX2DataRate:       8,
			JoinAcceptDelay1:  5 * time.Second,
			JoinAcceptDelay2:  6 * time.Second,
			MinUplinkDataRate: 0,
			MaxUplinkDataRate: 4,
		},
		dataRates: map[int]DataRate{
			0:  up125(0, 12),
			1:  up125(1, 11),
			2:  up125(2, 10),
			3:  up125(3, 9),
			4:  {Index: 4, Uplink: true, Downlink: false, Modulation: ModulationLoRa, SpreadingFactor: 8, BandwidthHz: 500_000, CodingRate: "4/5"},
			5:  {Index: 5, Uplink: true, Downlink: false, Modulation: ModulationFSK, Bitrate: 50_000},
			6:  lora(6, 7, true, false),
			8:  lora(8, 12, false, true),
			9:  lora(9, 11, false, true),
			10: lora(10, 10, false, true),
			11: lora(11, 9, false, true),
			12: lora(12, 8, false, true),
			13: lora(13, 7, false, true),
		},
		// lrwn us915.rs rx1_data_rate_table (offsets 0..3).
		rx1DataTable: map[int][]int{
			0: {10, 9, 8, 8},
			1: {11, 10, 9, 8},
			2: {12, 11, 10, 9},
			3: {13, 12, 11, 10},
			4: {13, 13, 12, 11},
			5: {10, 9, 8, 8},
			6: {11, 10, 9, 8},
			7: {14, 13, 12, 11},
			8: {0, 14, 13, 12},
		},
		txPowerOffsets:   []int{0, -2, -4, -6, -8, -10, -12, -14, -16, -18, -20, -22, -24, -26, -28},
		uplinkChannels:   up,
		downlinkChannels: down,
		subBands:         us915SubBands, // no duty-cycle limit in US915
		// RP002-400 MPDS for US915 downlink DR8..DR13 (SF12..SF7, 500 kHz).
		maxDownlinkPL: map[int]int{8: 61, 9: 133, 10: 255, 11: 255, 12: 255, 13: 255},
	}}
}

// RX1FrequencyHz maps the uplink frequency to downlink channel (index % 8).
func (u *us915) RX1FrequencyHz(uplinkFrequencyHz uint32) (uint32, error) {
	idx, err := u.uplinkChannelIndex(uplinkFrequencyHz)
	if err != nil {
		return 0, err
	}
	return u.downlinkChannels[idx%8].FrequencyHz, nil
}

// DownlinkTXPowerdBm returns the gateway downlink EIRP used by lrwn for
// US915 scheduling.
func (u *us915) DownlinkTXPowerdBm(uint32) int { return 21 }
