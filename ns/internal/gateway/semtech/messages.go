package semtech

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/region"
)

// UplinkBatch is the JSON object a gateway sends in a PUSH_DATA packet. Both
// members are optional: a stats-only packet carries no rxpk, and a data packet
// usually carries no stat.
type UplinkBatch struct {
	Uplinks []RadioRx     `json:"rxpk"`
	Stats   *StationStats `json:"stat"`
}

// ParseUplinkBatch decodes a PUSH_DATA body.
func ParseUplinkBatch(body []byte) (UplinkBatch, error) {
	var b UplinkBatch
	if err := json.Unmarshal(body, &b); err != nil {
		return b, fmt.Errorf("semtech: decode PUSH_DATA: %w", err)
	}
	return b, nil
}

// RadioRx is one rxpk entry: a single radio reception.
//
// Two field dialects are in the wild. The classic packet forwarder sends the
// strings datr/codr ("SF7BW125", "4/5"); the sx1302-style forwarders send the
// numeric lmst/lsbw/lscr instead. Both are accepted, and the string form wins
// when a forwarder sends both.
type RadioRx struct {
	Time string `json:"time"` // UTC arrival, ISO 8601 with microseconds and 'Z'
	TMMS uint32 `json:"tmms"` // GPS milliseconds, 0 when the gateway is not GPS-locked
	// TMST is the concentrator timestamp in microseconds. Not every forwarder
	// fills it in; when absent, downlink timing falls back to TMMS or UTC.
	TMST    uint32  `json:"tmst"`
	TCXO    int32   `json:"tcxo"`
	Freq    float64 `json:"freq"` // MHz
	Chan    int8    `json:"chan"`
	RFChain int8    `json:"rfch"`
	Board   int8    `json:"board"`
	Ant     int8    `json:"ant"`
	RSSI    float64 `json:"rssi"`
	SNR     float64 `json:"lsnr"`
	Size    int     `json:"size"`
	Data    string  `json:"data"` // hex-encoded PHYPayload

	Modulation string `json:"modu"` // LORA | FSK | LORA-FHSS
	DataRate   string `json:"datr"` // SF7BW125 | FSK50000
	CodingRate string `json:"codr"` // 4/5

	LoRaSpreadingFactor int `json:"lmst"`
	LoRaBandwidthHz     int `json:"lsbw"`
	LoRaCodingRate      int `json:"lscr"` // 5 means 4/5
	FSKBitrate          int `json:"lsff"`
}

// PHYBytes decodes the received frame. The declared size is checked against the
// hex payload: forwarders have been seen dropping the final byte, and a frame
// with a missing byte fails its MIC in a way that is painful to diagnose.
func (r RadioRx) PHYBytes() ([]byte, error) {
	if r.Data == "" {
		return nil, fmt.Errorf("semtech: rxpk has no data")
	}
	raw, err := hex.DecodeString(r.Data)
	if err != nil {
		return nil, fmt.Errorf("semtech: rxpk data is not hex: %w", err)
	}
	if r.Size != 0 && r.Size != len(raw) {
		return nil, fmt.Errorf("semtech: rxpk size says %d bytes, data holds %d", r.Size, len(raw))
	}
	return raw, nil
}

// FrequencyHz converts the MHz field to the Hz the rest of the engine uses.
// Rounding matters: 868.1 must become 868100000, not 868099999.
func (r RadioRx) FrequencyHz() uint32 {
	return uint32(r.Freq*1e6 + 0.5)
}

// ArrivalTime reports the gateway's UTC reception time when it sends one.
func (r RadioRx) ArrivalTime() (time.Time, bool) {
	if r.Time == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, r.Time)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ConcentratorTimestamp returns the counter value the gateway stamped the
// reception with, which the downlink timestamp is derived from. It prefers TMST
// (microseconds since the concentrator started), then TMMS (GPS milliseconds),
// and reports false when the forwarder supplied neither.
func (r RadioRx) ConcentratorTimestamp() (time.Duration, bool) {
	switch {
	case r.TMST != 0:
		return time.Duration(r.TMST) * time.Microsecond, true
	case r.TMMS != 0:
		return time.Duration(r.TMMS) * time.Millisecond, true
	}
	return 0, false
}

// TimingSource names the timestamp a downlink for this reception will be
// anchored to, for logs and for the M6 hardware checklist.
func (r RadioRx) TimingSource() string {
	switch {
	case r.TMST != 0:
		return "tmst"
	case r.TMMS != 0:
		return "tmms"
	case r.Time != "":
		return "time"
	}
	return "none"
}

// DataRateIndex resolves the reception's modulation into a band data-rate index.
// Matching ignores the coding rate: the specification fixes it at 4/5 on the
// uplink but forwarders have been observed reporting other values, and spreading
// factor plus bandwidth identify the data rate on their own.
func (r RadioRx) DataRateIndex(reg region.Region) (int, error) {
	sf, bw, bitrate, mod, err := r.modulationFields()
	if err != nil {
		return 0, err
	}
	// US915 reuses SF/bandwidth pairs between directions: SF8BW500 is both DR4
	// (uplink) and DR12 (downlink). An rxpk is always an uplink reception, so
	// uplink-capable rates win; the second pass keeps working for a band where
	// a rate exists only in the other direction.
	for pass := 0; pass < 2; pass++ {
		uplinkOnly := pass == 0
		for i := 0; i <= 15; i++ {
			dr, err := reg.DataRate(i)
			if err != nil || dr.Modulation != mod {
				continue
			}
			if uplinkOnly && !dr.Uplink {
				continue
			}
			switch mod {
			case region.ModulationFSK:
				if dr.Bitrate == bitrate {
					return i, nil
				}
			default:
				if dr.SpreadingFactor == sf && dr.BandwidthHz == bw {
					return i, nil
				}
			}
		}
	}
	return 0, fmt.Errorf("semtech: %s is not a data rate of %s", r.describe(), reg.CommonName())
}

// modulationFields normalises the two field dialects into comparable numbers.
func (r RadioRx) modulationFields() (sf, bw, bitrate int, mod region.Modulation, err error) {
	if r.DataRate != "" {
		return parseDataRateString(r.DataRate)
	}
	switch strings.ToUpper(r.Modulation) {
	case "FSK":
		if r.FSKBitrate == 0 {
			return 0, 0, 0, "", fmt.Errorf("semtech: FSK reception without a bitrate")
		}
		return 0, 0, r.FSKBitrate, region.ModulationFSK, nil
	case "LORA-FHSS":
		mod = region.ModulationLRFHSS
	default:
		mod = region.ModulationLoRa
	}
	if r.LoRaSpreadingFactor == 0 || r.LoRaBandwidthHz == 0 {
		return 0, 0, 0, "", fmt.Errorf("semtech: rxpk carries no data rate (neither datr nor lmst/lsbw)")
	}
	return r.LoRaSpreadingFactor, r.LoRaBandwidthHz, 0, mod, nil
}

// describe names the reception's modulation for error messages, using whichever
// field dialect the forwarder actually sent.
func (r RadioRx) describe() string {
	if r.DataRate != "" {
		return r.Modulation + " " + r.DataRate
	}
	return fmt.Sprintf("%s SF%dBW%d", r.Modulation, r.LoRaSpreadingFactor, r.LoRaBandwidthHz)
}

// parseDataRateString reads the Semtech datr field: "SF7BW125" (bandwidth in
// kHz) or "FSK50000" (bitrate).
func parseDataRateString(s string) (sf, bw, bitrate int, mod region.Modulation, err error) {
	upper := strings.ToUpper(strings.TrimSpace(s))
	if strings.HasPrefix(upper, "FSK") {
		n, err := strconv.Atoi(strings.TrimPrefix(upper, "FSK"))
		if err != nil {
			return 0, 0, 0, "", fmt.Errorf("semtech: cannot read the FSK bitrate from %q", s)
		}
		return 0, 0, n, region.ModulationFSK, nil
	}
	i := strings.Index(upper, "BW")
	if !strings.HasPrefix(upper, "SF") || i < 0 {
		return 0, 0, 0, "", fmt.Errorf("semtech: unrecognised datr %q (want SF<n>BW<kHz> or FSK<bitrate>)", s)
	}
	sf, err = strconv.Atoi(upper[2:i])
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("semtech: unrecognised spreading factor in %q", s)
	}
	bwKHz, err := strconv.Atoi(upper[i+2:])
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("semtech: unrecognised bandwidth in %q", s)
	}
	return sf, bwKHz * 1000, 0, region.ModulationLoRa, nil
}

// DataRateStrings renders a band data-rate index as the modu/datr/codr triple a
// downlink needs. It is the exact inverse of RadioRx.DataRateIndex, which is
// what makes the round trip worth testing.
func DataRateStrings(reg region.Region, index int) (modu, datr, codr string, err error) {
	dr, err := reg.DataRate(index)
	if err != nil {
		return "", "", "", err
	}
	switch dr.Modulation {
	case region.ModulationFSK:
		return "FSK", fmt.Sprintf("FSK%d", dr.Bitrate), "", nil
	case region.ModulationLRFHSS:
		// LRFHSS has no spreading factor and the classic Semtech dialect has no
		// way to express it. It is also uplink-only in every band we support, so
		// a downlink can never need it — refusing beats emitting "SF0BW0".
		return "", "", "", fmt.Errorf("semtech: %s DR%d is LRFHSS, which cannot be scheduled as a downlink",
			reg.CommonName(), index)
	default:
		if dr.SpreadingFactor == 0 || dr.BandwidthHz == 0 {
			return "", "", "", fmt.Errorf("semtech: data rate %d of %s is not a LoRa rate", index, reg.CommonName())
		}
		return "LORA", fmt.Sprintf("SF%dBW%d", dr.SpreadingFactor, dr.BandwidthHz/1000), dr.CodingRate, nil
	}
}

// DownlinkCommand is the txpk object a concentrator must execute.
//
// A class-A downlink has to land inside a window the device is already
// listening in, so the timing fields matter more than the payload: exactly one
// of Immediate, TimestampUS or UTCTime should be set. TimestampUS is what every
// forwarder understands; UTCTime is only usable when the gateway reported a UTC
// arrival time for the uplink.
type DownlinkCommand struct {
	Immediate   bool    `json:"imme"`
	TimestampUS *uint32 `json:"tmst,omitempty"`
	GPSMillis   *uint32 `json:"tmms,omitempty"`
	UTCTime     string  `json:"time,omitempty"`

	FrequencyMHZ       float64 `json:"freq"`
	RFChain            int8    `json:"rfch"`
	PowerdBm           int8    `json:"powe"`
	Modulation         string  `json:"modu"`
	DataRate           string  `json:"datr"`
	CodingRate         string  `json:"codr,omitempty"` // absent for FSK
	InvertPolarization bool    `json:"ipol"`           // always true for LoRaWAN
	Size               int     `json:"size"`
	Data               string  `json:"data"` // hex PHYPayload
	Board              int8    `json:"board,omitempty"`
	Antenna            int8    `json:"ant,omitempty"`
}

// MarshalDownlink wraps a txpk in the object form the protocol expects.
func MarshalDownlink(tx DownlinkCommand) ([]byte, error) {
	out, err := json.Marshal(struct {
		Tx DownlinkCommand `json:"txpk"`
	}{Tx: tx})
	if err != nil {
		return nil, fmt.Errorf("semtech: encode txpk: %w", err)
	}
	return out, nil
}

// StationStats is the stat object a gateway reports periodically. It is where
// the Fieldnode dashboard gets its gateway health.
type StationStats struct {
	Time        string  `json:"time"`
	Latitude    float64 `json:"lati"`
	Longitude   float64 `json:"long"`
	Altitude    int     `json:"alti"`
	RxReceived  uint32  `json:"rxnb"`
	RxOK        uint32  `json:"rxok"`
	RxFocus     uint32  `json:"rxfw"`
	MailboxTime float64 `json:"mailf"`
	TxReceived  uint32  `json:"txnb"`
}

// TxAckReport is the TX_ACK body. An empty Error means the transmission went
// out; anything else is the concentrator's reason for refusing it.
type TxAckReport struct {
	Error string          `json:"error"`
	Info  json.RawMessage `json:"txpk_info"`
}

// ParseTxAck decodes a TX_ACK body. Forwarders send an empty datagram as well as
// a JSON object, so both are accepted.
func ParseTxAck(body []byte) (TxAckReport, error) {
	var r TxAckReport
	if len(body) == 0 {
		return r, nil
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return r, fmt.Errorf("semtech: decode TX_ACK: %w", err)
	}
	return r, nil
}
