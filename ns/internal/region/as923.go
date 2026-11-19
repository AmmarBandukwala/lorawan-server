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
			0: lora(0, 12, 125_000),
			1: lora(1, 11, 125_000),
			2: lora(2, 10, 125_000),
			3: lora(3, 9, 125_000),
			4: lora(4, 8, 125_000),
			5: lora(5, 7, 125_000),
			6: lora(6, 7, 250_000),
			7: {Index: 7, Uplink: true, Downlink: true, Modulation: ModulationFSK, Bitrate: 50_000},
			// RFU in RP002 for this band; defined so a frame claiming them can be
			// decoded, never offered as a downlink rate. See the same note in
			// eu868.go.
			12: {Index: 12, Uplink: true, Downlink: false, Modulation: ModulationLoRa, SpreadingFactor: 6, BandwidthHz: 125_000, CodingRate: "4/5"},
			13: {Index: 13, Uplink: true, Downlink: false, Modulation: ModulationLoRa, SpreadingFactor: 5, BandwidthHz: 125_000, CodingRate: "4/5"},
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
		// Maximum downlink PHY-payload size per data rate, from lrwn's
		// max_dl_payload_size_per_dr for the "no repeater, no dwell time" case.
		//
		// AS923 is the one band whose payload limits depend on dwell time: with
		// the 400 ms uplink dwell time that RP002 requires by default, lrwn's
		// table becomes {0:0, 1:0, 2:19, 3:61, 4:133, 5:250, 6:250, 7:250} — DR0
		// and DR1 are unusable because their time on air exceeds the dwell limit.
		// This build does not model dwell time (see UplinkDwellTime, which
		// reports 0), so the no-dwell table is the consistent choice. Wiring
		// TXParamSetupReq to dwell time must switch this table at the same time,
		// or the server will keep offering DR0 to a device that cannot use it.
		maxDownlinkPL: map[int]int{0: 59, 1: 59, 2: 59, 3: 123, 4: 250, 5: 250, 6: 250, 7: 250},
		rx1SameFreq:   true,
	}}
}

// DownlinkTXPowerdBm returns the AS923 gateway downlink EIRP (lrwn).
func (a *as923) DownlinkTXPowerdBm(uint32) int { return 16 }
