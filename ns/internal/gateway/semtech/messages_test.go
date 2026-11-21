package semtech

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// classicPushData is a PUSH_DATA body in the shape a stock Semtech packet
// forwarder sends: string datr/codr, MHz frequency, hex data.
const classicPushData = `{"rxpk":[{` +
	`"time":"2026-11-17T10:00:00.123456Z","tmms":0,"tmst":1234567890,` +
	`"freq":868.1,"chan":0,"rfch":0,"rssi":-72,"lsnr":8.5,"size":22,` +
	`"modu":"LORA","datr":"SF7BW125","codr":"4/5",` +
	`"data":"400102030405060708090a0b0c0d0e0f101112131415"}],` +
	`"stat":{"time":"2026-11-17T10:00:01Z","lati":51.5,"long":-0.12,"alti":34,` +
	`"rxnb":12,"rxok":11,"rxfw":0,"mailf":1.2,"txnb":3}}`

func TestParseUplinkBatchClassicDialect(t *testing.T) {
	batch, err := ParseUplinkBatch([]byte(classicPushData))
	if err != nil {
		t.Fatalf("ParseUplinkBatch: %v", err)
	}
	if len(batch.Uplinks) != 1 || batch.Stats == nil {
		t.Fatalf("batch = %+v", batch)
	}
	rx := batch.Uplinks[0]

	// 868.1 MHz must become exactly 868100000 Hz, not 868099999.
	if got := rx.FrequencyHz(); got != 868100000 {
		t.Errorf("FrequencyHz = %d, want 868100000", got)
	}
	if rx.RSSI != -72 || rx.SNR != 8.5 {
		t.Errorf("RSSI/SNR = %v/%v, want -72/8.5", rx.RSSI, rx.SNR)
	}
	raw, err := rx.PHYBytes()
	if err != nil {
		t.Fatalf("PHYBytes: %v", err)
	}
	if len(raw) != 22 {
		t.Errorf("PHYPayload = %d bytes, want 22", len(raw))
	}
	if at, ok := rx.ArrivalTime(); !ok ||
		!at.Equal(time.Date(2026, 11, 17, 10, 0, 0, 123456000, time.UTC)) {
		t.Errorf("ArrivalTime = %v (ok=%v)", at, ok)
	}
	if ts, ok := rx.ConcentratorTimestamp(); !ok ||
		ts != time.Duration(1234567890)*time.Microsecond {
		t.Errorf("ConcentratorTimestamp = %v (ok=%v)", ts, ok)
	}
	if got := rx.TimingSource(); got != "tmst" {
		t.Errorf("TimingSource = %q, want tmst", got)
	}

	eu868, err := region.New("EU868")
	if err != nil {
		t.Fatalf("region.New: %v", err)
	}
	dr, err := rx.DataRateIndex(eu868)
	if err != nil {
		t.Fatalf("DataRateIndex: %v", err)
	}
	if dr != 5 {
		t.Errorf("SF7BW125 on EU868 = DR%d, want DR5", dr)
	}
	if batch.Stats.RxReceived != 12 || batch.Stats.RxOK != 11 || batch.Stats.Altitude != 34 {
		t.Errorf("stats = %+v", batch.Stats)
	}
}

// TestParseUplinkBatchSx1302Dialect covers forwarders that send numeric
// lmst/lsbw/lscr instead of the datr/codr strings.
func TestParseUplinkBatchSx1302Dialect(t *testing.T) {
	body := `{"rxpk":[{"freq":868.5,"rssi":-90,"lsnr":3.0,"size":12,` +
		`"modu":"LORA","lmst":12,"lsbw":125000,"lscr":5,` +
		`"data":"400102030405060708090a0b"}]}`
	b, err := ParseUplinkBatch([]byte(body))
	if err != nil {
		t.Fatalf("ParseUplinkBatch: %v", err)
	}
	eu868, _ := region.New("EU868")
	dr, err := b.Uplinks[0].DataRateIndex(eu868)
	if err != nil {
		t.Fatalf("DataRateIndex: %v", err)
	}
	if dr != 0 {
		t.Errorf("SF12BW125 on EU868 = DR%d, want DR0", dr)
	}
}

func TestFSKUplinkMapsToItsDataRate(t *testing.T) {
	body := `{"rxpk":[{"freq":868.0,"rssi":-80,"lsnr":0,"size":12,"modu":"FSK",` +
		`"lsff":50000,"data":"400102030405060708090a0b"}]}`
	b, err := ParseUplinkBatch([]byte(body))
	if err != nil {
		t.Fatalf("ParseUplinkBatch: %v", err)
	}
	eu868, _ := region.New("EU868")
	dr, err := b.Uplinks[0].DataRateIndex(eu868)
	if err != nil {
		t.Fatalf("DataRateIndex: %v", err)
	}
	if dr != 7 {
		t.Errorf("FSK 50 kbit on EU868 = DR%d, want DR7", dr)
	}
}

// TestDataRateRoundTrip pins the two directions together: whatever we tell a
// gateway to transmit must map back to the same band data rate.
func TestDataRateRoundTrip(t *testing.T) {
	for _, name := range []string{"EU868", "US915", "AS923"} {
		reg, err := region.New(name)
		if err != nil {
			t.Fatalf("region.New(%s): %v", name, err)
		}
		checked := 0
		for i := 0; i <= 15; i++ {
			modu, datr, codr, err := DataRateStrings(reg, i)
			if err != nil {
				continue
			}
			rx := RadioRx{Modulation: modu, DataRate: datr, CodingRate: codr}
			got, err := rx.DataRateIndex(reg)
			if err != nil {
				t.Errorf("%s DR%d -> %s/%s -> %v", name, i, modu, datr, err)
				continue
			}
			if got == i {
				checked++
				continue
			}
			// A mismatch is only legitimate for a downlink-only rate that shares
			// its SF/bandwidth pair with an uplink rate (US915 DR12 and DR4 are
			// both SF8BW500). The reverse lookup cannot tell them apart, and
			// preferring the uplink rate is the correct choice.
			orig, errO := reg.DataRate(i)
			twin, errT := reg.DataRate(got)
			if errO != nil || errT != nil || !twin.Uplink || orig.Uplink ||
				twin.Modulation != orig.Modulation ||
				twin.SpreadingFactor != orig.SpreadingFactor ||
				twin.BandwidthHz != orig.BandwidthHz || twin.Bitrate != orig.Bitrate {
				t.Errorf("%s DR%d -> %s/%s -> DR%d", name, i, modu, datr, got)
				continue
			}
			checked++
		}
		if checked == 0 {
			t.Errorf("%s: no data rates round-tripped", name)
		}
	}
}

// TestUS915SharedSF500PairsPrefersUplink pins the resolution rule for the pairs
// US915 uses in both directions. Getting it wrong makes the engine compute the
// wrong RX1 data rate for every 500 kHz uplink.
func TestUS915SharedSF500PairsPrefersUplink(t *testing.T) {
	us915, err := region.New("US915")
	if err != nil {
		t.Fatalf("region.New: %v", err)
	}
	for _, tc := range []struct {
		datr string
		want int
	}{
		{"SF8BW500", 4}, // also DR12, downlink
		{"SF7BW500", 6}, // also DR13, downlink
	} {
		rx := RadioRx{Modulation: "LORA", DataRate: tc.datr}
		got, err := rx.DataRateIndex(us915)
		if err != nil {
			t.Fatalf("%s: %v", tc.datr, err)
		}
		if got != tc.want {
			t.Errorf("%s resolved to DR%d, want the uplink rate DR%d", tc.datr, got, tc.want)
		}
	}
}

// TestLRFHSSIsNotSchedulableAsDownlink covers EU868 DR8..DR11: LRFHSS uplink
// rates with no spreading factor. The classic dialect cannot express them, so
// the codec must refuse instead of inventing "SF0BW0".
func TestLRFHSSIsNotSchedulableAsDownlink(t *testing.T) {
	eu868, err := region.New("EU868")
	if err != nil {
		t.Fatalf("region.New: %v", err)
	}
	for _, i := range []int{8, 9, 10, 11} {
		if _, _, _, err := DataRateStrings(eu868, i); err == nil {
			t.Errorf("EU868 DR%d (LRFHSS) must not be expressible as a txpk datr", i)
		}
	}
}

func TestDataRateStringsKnownValues(t *testing.T) {
	eu868, _ := region.New("EU868")
	modu, datr, codr, err := DataRateStrings(eu868, 0)
	if err != nil {
		t.Fatalf("DataRateStrings: %v", err)
	}
	if modu != "LORA" || datr != "SF12BW125" || codr != "4/5" {
		t.Errorf("EU868 DR0 = %s/%s/%s, want LORA/SF12BW125/4 over 5", modu, datr, codr)
	}
	us915, _ := region.New("US915")
	if _, datr, _, _ := DataRateStrings(us915, 8); datr != "SF12BW500" {
		t.Errorf("US915 DR8 datr = %q, want SF12BW500", datr)
	}
}

func TestPHYBytesRejectsSizeMismatch(t *testing.T) {
	// A truncated last byte shows up as a MIC failure much later; catching it
	// here is what keeps that class of bug visible at the edge.
	rx := RadioRx{Data: "400102", Size: 4}
	if _, err := rx.PHYBytes(); err == nil {
		t.Error("a size field that disagrees with the data must be rejected")
	}
	if _, err := (RadioRx{Size: 3}).PHYBytes(); err == nil {
		t.Error("an rxpk without data must be rejected")
	}
}

func TestMarshalDownlinkWireShape(t *testing.T) {
	tmst := uint32(1234568890)
	body, err := MarshalDownlink(DownlinkCommand{
		TimestampUS: &tmst, FrequencyMHZ: 869.525, PowerdBm: 14,
		Modulation: "LORA", DataRate: "SF12BW125", CodingRate: "4/5",
		InvertPolarization: true, Size: 2, Data: "4001",
	})
	if err != nil {
		t.Fatalf("MarshalDownlink: %v", err)
	}
	s := string(body)
	for _, want := range []string{
		`"txpk":`, `"imme":false`, `"tmst":1234568890`, `"freq":869.525`,
		`"powe":14`, `"modu":"LORA"`, `"datr":"SF12BW125"`, `"codr":"4/5"`,
		`"ipol":true`, `"size":2`, `"data":"4001"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("txpk is missing %s: %s", want, s)
		}
	}
	// A forwarder that sees both tmms and tmst has to pick between them by
	// vendor, so only the timing field we actually set may appear.
	if strings.Contains(s, `"tmms"`) || strings.Contains(s, `"time"`) {
		t.Errorf("unset timing fields must be omitted: %s", s)
	}

	// FSK has no coding rate, and some forwarders reject an empty codr outright.
	fsk, err := MarshalDownlink(DownlinkCommand{
		Immediate: true, FrequencyMHZ: 868.0, PowerdBm: 14,
		Modulation: "FSK", DataRate: "FSK50000", InvertPolarization: true,
		Size: 2, Data: "4001",
	})
	if err != nil {
		t.Fatalf("MarshalDownlink FSK: %v", err)
	}
	if strings.Contains(string(fsk), `"codr"`) {
		t.Errorf("FSK txpk must omit codr: %s", fsk)
	}
	if !strings.Contains(string(fsk), `"imme":true`) {
		t.Errorf("FSK txpk must keep imme: %s", fsk)
	}
}

func TestParseTxAck(t *testing.T) {
	ok, err := ParseTxAck([]byte(`{"error":""}`))
	if err != nil || ok.Error != "" {
		t.Fatalf("ParseTxAck ok = %+v/%v", ok, err)
	}
	bad, err := ParseTxAck([]byte(`{"error":"TOO_LATE"}`))
	if err != nil || bad.Error != "TOO_LATE" {
		t.Fatalf("ParseTxAck bad = %+v/%v", bad, err)
	}
	if _, err := ParseTxAck(nil); err != nil {
		t.Errorf("an empty TX_ACK must be accepted: %v", err)
	}
	if _, err := ParseTxAck([]byte("{")); err == nil {
		t.Error("malformed TX_ACK JSON must be reported")
	}
}

// TestStationStatsTags guards the struct tags against typos: a wrong tag
// silently reports every gateway as having zero traffic.
func TestStationStatsTags(t *testing.T) {
	var stats StationStats
	if err := json.Unmarshal([]byte(`{"time":"t","lati":1.5,"long":2.5,"alti":9,`+
		`"rxnb":7,"rxok":6,"rxfw":1,"mailf":0.5,"txnb":4}`), &stats); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if stats.Latitude != 1.5 || stats.Longitude != 2.5 || stats.Altitude != 9 ||
		stats.RxReceived != 7 || stats.RxOK != 6 || stats.RxFocus != 1 ||
		stats.MailboxTime != 0.5 || stats.TxReceived != 4 {
		t.Errorf("stats = %+v", stats)
	}
}
