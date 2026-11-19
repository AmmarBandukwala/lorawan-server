package region

import "time"

// EU868 (RP002 1.0.3 §2). Standard configuration without the optional
// 1% dwell-time (repeater-compatible) relaxation tables.

type eu868 struct{ base }

func newEU868() *eu868 {
	lora := func(idx, sf int) DataRate {
		return DataRate{Index: idx, Uplink: true, Downlink: true,
			Modulation: ModulationLoRa, SpreadingFactor: sf, BandwidthHz: 125_000, CodingRate: "4/5"}
	}
	rfhss := func(idx int, cr string, width int) DataRate {
		return DataRate{Index: idx, Uplink: true, Downlink: false,
			Modulation: ModulationLRFHSS, CodingRate: cr, OccupiedChannelWidthHz: width}
	}
	ch := func(freq uint32, drs ...int) Channel {
		return Channel{FrequencyHz: freq, DataRates: drs, Enabled: true}
	}

	return &eu868{base{
		name: "EU868",
		// EU868 uses the RF-region reading of ChMaskCntl (RP002 5.5.2.2).
		maskStyle: maskStyleRFRegion,
		defaults: Defaults{
			RX1Delay:          time.Second,
			RX2Delay:          2 * time.Second,
			RX2FrequencyHz:    869_525_000,
			RX2DataRate:       0,
			JoinAcceptDelay1:  5 * time.Second,
			JoinAcceptDelay2:  6 * time.Second,
			MinUplinkDataRate: 0,
			MaxUplinkDataRate: 5,
		},
		dataRates: map[int]DataRate{
			0:  lora(0, 12),
			1:  lora(1, 11),
			2:  lora(2, 10),
			3:  lora(3, 9),
			4:  lora(4, 8),
			5:  lora(5, 7),
			6:  {Index: 6, Uplink: true, Downlink: true, Modulation: ModulationLoRa, SpreadingFactor: 7, BandwidthHz: 250_000, CodingRate: "4/5"},
			7:  {Index: 7, Uplink: true, Downlink: true, Modulation: ModulationFSK, Bitrate: 50_000},
			8:  rfhss(8, "2/6", 137_000),
			9:  rfhss(9, "4/6", 137_000),
			10: rfhss(10, "2/6", 336_000),
			11: rfhss(11, "4/6", 336_000),
			// DR12 and DR13 are RFU in RP002 for this band. lrwn's RX1 table has
			// rows for them, so they stay defined and decodable, but they are
			// deliberately not marked downlink-capable: scheduling a downlink at
			// a reserved rate, with no maximum payload size defined anywhere, is
			// a frame the device can only discard.
			12: {Index: 12, Uplink: true, Downlink: false, Modulation: ModulationLoRa, SpreadingFactor: 6, BandwidthHz: 125_000, CodingRate: "4/5"},
			13: {Index: 13, Uplink: true, Downlink: false, Modulation: ModulationLoRa, SpreadingFactor: 5, BandwidthHz: 125_000, CodingRate: "4/5"},
		},
		// lrwn eu868.rs rx1_data_rate_table (offsets 0..5; 6-7 are RFU).
		rx1DataTable: map[int][]int{
			0:  {0, 0, 0, 0, 0, 0},
			1:  {1, 0, 0, 0, 0, 0},
			2:  {2, 1, 0, 0, 0, 0},
			3:  {3, 2, 1, 0, 0, 0},
			4:  {4, 3, 2, 1, 0, 0},
			5:  {5, 4, 3, 2, 1, 0},
			6:  {6, 5, 4, 3, 2, 1},
			7:  {7, 6, 5, 4, 3, 2},
			8:  {1, 0, 0, 0, 0, 0},
			9:  {2, 1, 0, 0, 0, 0},
			10: {1, 0, 0, 0, 0, 0},
			11: {2, 1, 0, 0, 0, 0},
			12: {12, 5, 4, 3, 2, 1},
			13: {13, 12, 5, 4, 3, 2},
		},
		txPowerOffsets: []int{0, -2, -4, -6, -8, -10, -12, -14},
		// The three default channels; 867.1-867.9 MHz are delivered via
		// CFList in the join-accept (engine responsibility).
		uplinkChannels: []Channel{
			ch(868_100_000, 0, 1, 2, 3, 4, 5),
			ch(868_300_000, 0, 1, 2, 3, 4, 5),
			ch(868_500_000, 0, 1, 2, 3, 4, 5),
		},
		downlinkChannels: []Channel{
			ch(868_100_000, 0, 1, 2, 3, 4, 5),
			ch(868_300_000, 0, 1, 2, 3, 4, 5),
			ch(868_500_000, 0, 1, 2, 3, 4, 5),
		},
		subBands: eu868SubBands,
		// RP002-403 MPDS for EU868 downlink DR0..DR5 (SF12..SF7, 125 kHz).
		// Maximum downlink PHY-payload size per data rate, transcribed from
		// lrwn's max_dl_payload_size_per_dr (m, the full MHDR+FHDR+FRMPayload+MIC
		// size, not the FRMPayload alone).
		//
		// DR6 (SF7BW250) and DR7 (FSK 50 kbit) are downlink-capable in this band
		// and must be listed: a missing entry reads as "cannot carry anything",
		// which would silently cost the device its fastest downlinks.
		//
		// DR8..DR11 are LR-FHSS uplink rates and DR12/DR13 are reserved, so they
		// have no downlink entry at all.
		maxDownlinkPL: map[int]int{0: 59, 1: 59, 2: 59, 3: 123, 4: 230, 5: 230, 6: 230, 7: 230},
		rx1SameFreq:   true,
	}}
}

// DownlinkTXPowerdBm implements the EU868 band limits (lrwn eu868.rs:
// 16 dBm EIRP generally, 29 dBm on the 869.4-869.65 MHz band).
func (e *eu868) DownlinkTXPowerdBm(frequencyHz uint32) int {
	if frequencyHz >= 869_400_000 && frequencyHz <= 869_650_000 {
		return 29
	}
	return 16
}
