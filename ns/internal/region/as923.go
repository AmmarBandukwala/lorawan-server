package region

import "time"

// AS923 (RP002 1.0.3 §3) with the frequency-offset variants:
// AS923 (0 Hz), AS923-2 / JS1 (-1.8 MHz), AS923-3 / JS2 (-6.6 MHz),
// AS923-4 (-5.9 MHz). Dwell-time tables follow the 400 ms variant
// (lrwn default for repeater-compatible networks); the engine can relax
// them per device via TXParamSetup.

type as923 struct{ base }

func newAS923(name string, freqOffsetHz uint32) *as923 {
	lora := func(idx, sf, bw int) DataRate {
		return DataRate{Index: idx, Uplink: true, Downlink: true,
			Modulation: ModulationLoRa, SpreadingFactor: sf, BandwidthHz: bw, CodingRate: "4/5"}
	}
	ch := func(freq uint32) Channel {
		return Channel{FrequencyHz: freq, DataRates: []int{0, 1, 2, 3, 4, 5}, Enabled: true}
	}

	return &as923{base{
		name: name,
		// AS923 reads ChMaskCntl 0b000 as channels 0..15 and 0b100 as "all on",
		// and defines no RF-region values (RP002 5.5.2.2 for AS923).
		maskStyle: maskStyleAllOn,
		defaults: Defaults{
			RX1Delay:          time.Second,
			RX2Delay:          2 * time.Second,
			RX2FrequencyHz:    923_200_000 - freqOffsetHz,
			RX2DataRate:       2,
			JoinAcceptDelay1:  5 * time.Second,
			JoinAcceptDelay2:  6 * time.Second,
			MinUplinkDataRate: 0,
			MaxUplinkDataRate: 5,
		},
		dataRates: map[int]DataRate{
			0:  lora(0, 12, 125_000),
			1:  lora(1, 11, 125_000),
			2:  lora(2, 10, 125_000),
			3:  lora(3, 9, 125_000),
			4:  lora(4, 8, 125_000),
			5:  lora(5, 7, 125_000),
			6:  lora(6, 7, 250_000),
			7:  {Index: 7, Uplink: true, Downlink: true, Modulation: ModulationFSK, Bitrate: 50_000},
			12: lora(12, 6, 125_000),
			13: lora(13, 5, 125_000),
		},
		// lrwn as923.rs rx1_data_rate_table, dwell_time_400ms = true.
		rx1DataTable: map[int][]int{
			0:  {2, 2, 2, 2, 2, 2, 2, 2},
			1:  {2, 2, 2, 2, 2, 2, 2, 3},
			2:  {2, 2, 2, 2, 2, 2, 3, 4},
			3:  {3, 2, 2, 2, 2, 2, 4, 5},
			4:  {4, 3, 2, 2, 2, 2, 5, 6},
			5:  {5, 4, 3, 2, 2, 2, 6, 7},
			6:  {6, 5, 4, 3, 2, 2, 7, 7},
			7:  {7, 6, 5, 4, 3, 2, 7, 7},
			12: {12, 5, 4, 3, 2, 2, 13, 13},
			13: {13, 12, 5, 4, 3, 2, 13, 13},
		},
		txPowerOffsets: []int{0, -2, -4, -6, -8, -10, -12, -14},
		uplinkChannels: []Channel{
			ch(923_200_000 - freqOffsetHz),
			ch(923_400_000 - freqOffsetHz),
		},
		downlinkChannels: []Channel{
			ch(923_200_000 - freqOffsetHz),
			ch(923_400_000 - freqOffsetHz),
		},
		// AS923 is limited by dwell time (below) and, per RP002, by a 1% duty
		// cycle in the 923.0-923.5 MHz range; several countries apply both, so
		// both are enforced and the stricter one wins.
		subBands:      shiftSubBands(as923SubBands, freqOffsetHz, true),
		uplinkDwell:   400 * time.Millisecond,
		downlinkDwell: 400 * time.Millisecond,
		// RP002-401 MPDS for AS923 downlink DR0..DR5 (SF12..SF7, 125 kHz).
		maxDownlinkPL: map[int]int{0: 59, 1: 59, 2: 59, 3: 123, 4: 255, 5: 255},
		rx1SameFreq:   true,
	}}
}

// DownlinkTXPowerdBm returns the AS923 gateway downlink EIRP (lrwn).
func (a *as923) DownlinkTXPowerdBm(uint32) int { return 16 }
