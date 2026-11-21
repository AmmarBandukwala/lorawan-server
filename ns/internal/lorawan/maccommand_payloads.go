package lorawan

import "fmt"

// --- Typed payloads for the commands the Fieldnode NS uses. ---

// LinkCheckAnsPayload (DL, CID 0x02): margin in dB above noise floor and
// the number of gateways that received the request.
type LinkCheckAnsPayload struct {
	Margin int8
	GwCnt  byte
}

func (p LinkCheckAnsPayload) Marshal() []byte { return []byte{byte(p.Margin), p.GwCnt} }

// ParseLinkCheckAns decodes the two bytes a gateway cluster returns for a
// LinkCheckReq: a signed margin in dB above the demodulation floor, and how
// many gateways heard the request.
func ParseLinkCheckAns(payload []byte) (LinkCheckAnsPayload, error) {
	if len(payload) != 2 {
		return LinkCheckAnsPayload{}, errLen("LinkCheckAns", 2, len(payload))
	}
	return LinkCheckAnsPayload{Margin: int8(payload[0]), GwCnt: payload[1]}, nil
}

// LinkADRReqPayload (DL, CID 0x03).
type LinkADRReqPayload struct {
	DataRate   byte // 4 bits
	TxPower    byte // 4 bits
	ChMask     uint16
	ChMaskCntl byte // 3 bits
	NbRep      byte // 3 bits
}

func (p LinkADRReqPayload) Marshal() []byte {
	return []byte{
		p.DataRate<<4 | p.TxPower&0x0f,
		byte(p.ChMask), byte(p.ChMask >> 8),
		p.NbRep<<4 | p.ChMaskCntl&0x07,
	}
}

// ParseLinkADRReq splits a four-byte LinkADRReq into its fields. Data rate and
// transmit power share one byte, and NbRep shares another with ChMaskCntl, so
// the halves are unpacked once here rather than at every place that reads them.
func ParseLinkADRReq(payload []byte) (LinkADRReqPayload, error) {
	var p LinkADRReqPayload
	if len(payload) != 4 {
		return p, errLen("LinkADRReq", 4, len(payload))
	}
	p.DataRate = payload[0] >> 4
	p.TxPower = payload[0] & 0x0f
	p.ChMask = uint16(payload[1]) | uint16(payload[2])<<8
	p.NbRep = payload[3] >> 4
	p.ChMaskCntl = payload[3] & 0x07
	return p, nil
}

// LinkADRAnsPayload (UL, CID 0x03).
type LinkADRAnsPayload struct {
	PowerACK    bool
	DataRateACK bool
	ChannelACK  bool
}

func (p LinkADRAnsPayload) Marshal() []byte {
	var b byte
	if p.PowerACK {
		b |= 0x04
	}
	if p.DataRateACK {
		b |= 0x02
	}
	if p.ChannelACK {
		b |= 0x01
	}
	return []byte{b}
}

// ParseLinkADRAns reads the three acknowledgement bits a device sets for a
// LinkADRReq. A device that accepts part of the request clears only those bits,
// so they stay separate instead of collapsing into one yes/no.
func ParseLinkADRAns(payload []byte) (LinkADRAnsPayload, error) {
	if len(payload) != 1 {
		return LinkADRAnsPayload{}, errLen("LinkADRAns", 1, len(payload))
	}
	return LinkADRAnsPayload{
		PowerACK:    payload[0]&0x04 != 0,
		DataRateACK: payload[0]&0x02 != 0,
		ChannelACK:  payload[0]&0x01 != 0,
	}, nil
}

// DutyCycleReqPayload (DL, CID 0x04): max duty cycle as 1/2^n.
type DutyCycleReqPayload struct{ MaxDCycle byte }

func (p DutyCycleReqPayload) Marshal() []byte { return []byte{p.MaxDCycle & 0x0f} }

// DevStatusAnsPayload (UL, CID 0x06): battery level and demodulation
// margin (SNR) as a signed 6-bit value.
type DevStatusAnsPayload struct {
	Battery byte
	SNR     int8
}

func (p DevStatusAnsPayload) Marshal() []byte {
	return []byte{p.Battery, byte(p.SNR) & 0x3f}
}

// ParseDevStatusAns reads a battery level and a demodulation margin. The margin
// is a signed 6-bit two's-complement value, so it is sign-extended before
// becoming an int8; reading it as a plain byte would turn -8 into 56.
func ParseDevStatusAns(payload []byte) (DevStatusAnsPayload, error) {
	if len(payload) != 2 {
		return DevStatusAnsPayload{}, errLen("DevStatusAns", 2, len(payload))
	}
	v := payload[1] & 0x3f
	if v&0x20 != 0 {
		v |= 0xC0 // sign-extend the 6-bit two's complement value
	}
	return DevStatusAnsPayload{Battery: payload[0], SNR: int8(v)}, nil
}

func errLen(name string, want, got int) error {
	return fmt.Errorf("%s needs %d bytes, got %d", name, want, got)
}

// RXParamSetupReqPayload (DL, CID 0x05): new RX2 frequency + DL settings.
type RXParamSetupReqPayload struct {
	Frequency  uint32
	DLSettings DLSettings
}

func (p RXParamSetupReqPayload) Marshal() ([]byte, error) {
	f, err := EncodeFrequency(p.Frequency)
	if err != nil {
		return nil, fmt.Errorf("RXParamSetupReq: %w", err)
	}
	return append(f[:], p.DLSettings.Byte()), nil
}

// ParseRXParamSetupReq decodes the three-byte RX2 frequency followed by the
// DLSettings byte.
func ParseRXParamSetupReq(payload []byte) (RXParamSetupReqPayload, error) {
	var p RXParamSetupReqPayload
	if len(payload) != 4 {
		return p, errLen("RXParamSetupReq", 4, len(payload))
	}
	freq, err := DecodeFrequency(payload[0:3])
	if err != nil {
		return p, err
	}
	p.Frequency = freq
	p.DLSettings = parseDLSettings(payload[3])
	return p, nil
}

// RXParamSetupAnsPayload (UL, CID 0x05).
type RXParamSetupAnsPayload struct {
	Channel      bool
	RX2Frequency bool
	RX1DROffset  bool
}

func (p RXParamSetupAnsPayload) Marshal() []byte {
	var b byte
	if p.Channel {
		b |= 0x04
	}
	if p.RX2Frequency {
		b |= 0x02
	}
	if p.RX1DROffset {
		b |= 0x01
	}
	return []byte{b}
}

// ParseRXParamSetupAns reads which of the three RX parameters the device
// accepted: the channel, the RX2 frequency, and the RX1 data-rate offset.
func ParseRXParamSetupAns(payload []byte) (RXParamSetupAnsPayload, error) {
	if len(payload) != 1 {
		return RXParamSetupAnsPayload{}, errLen("RXParamSetupAns", 1, len(payload))
	}
	return RXParamSetupAnsPayload{
		Channel:      payload[0]&0x04 != 0,
		RX2Frequency: payload[0]&0x02 != 0,
		RX1DROffset:  payload[0]&0x01 != 0,
	}, nil
}

// RXTimingSetupReqPayload (DL, CID 0x08): RX1 delay in seconds (0 => 1s).
type RXTimingSetupReqPayload struct{ Delay byte }

func (p RXTimingSetupReqPayload) Marshal() []byte { return []byte{p.Delay & 0x0f} }

// NewChannelReqPayload (DL, CID 0x07).
type NewChannelReqPayload struct {
	ChIndex   byte
	Frequency uint32
	MinDR     byte // 4 bits
	MaxDR     byte // 4 bits
}

func (p NewChannelReqPayload) Marshal() ([]byte, error) {
	f, err := EncodeFrequency(p.Frequency)
	if err != nil {
		return nil, fmt.Errorf("NewChannelReq: %w", err)
	}
	return []byte{p.ChIndex, f[0], f[1], f[2], p.MinDR<<4 | p.MaxDR&0x0f}, nil
}

// ParseNewChannelReq decodes a request to add or change a channel: its index,
// its frequency, and the data-rate range it may carry, which share one byte.
func ParseNewChannelReq(payload []byte) (NewChannelReqPayload, error) {
	var p NewChannelReqPayload
	if len(payload) != 5 {
		return p, errLen("NewChannelReq", 5, len(payload))
	}
	freq, err := DecodeFrequency(payload[1:4])
	if err != nil {
		return p, err
	}
	p.ChIndex = payload[0]
	p.Frequency = freq
	p.MinDR = payload[4] >> 4
	p.MaxDR = payload[4] & 0x0f
	return p, nil
}

// NewChannelAnsPayload (UL, CID 0x07).
type NewChannelAnsPayload struct {
	DataRateRangeOK bool
	ChannelFreqOK   bool
}

// ParseNewChannelAns reads whether the device accepts the channel frequency and
// the data-rate range. A device answers no to either without rejecting the
// whole command.
func ParseNewChannelAns(payload []byte) (NewChannelAnsPayload, error) {
	if len(payload) != 1 {
		return NewChannelAnsPayload{}, errLen("NewChannelAns", 1, len(payload))
	}
	return NewChannelAnsPayload{
		DataRateRangeOK: payload[0]&0x02 != 0,
		ChannelFreqOK:   payload[0]&0x01 != 0,
	}, nil
}

// TXParamSetupReqPayload (DL, CID 0x09, AS923): dwell times + max EIRP.
type TXParamSetupReqPayload struct {
	DownlinkDwellTime bool
	UplinkDwellTime   bool
	MaxEIRP           byte // 4 bits
}

func (p TXParamSetupReqPayload) Marshal() []byte {
	var b byte
	if p.DownlinkDwellTime {
		b |= 0x20
	}
	if p.UplinkDwellTime {
		b |= 0x10
	}
	return []byte{b | p.MaxEIRP&0x0f}
}

// DLChannelReqPayload (DL, CID 0x0A, AS923/EU868): per-channel RX frequency.
type DLChannelReqPayload struct {
	ChIndex   byte
	Frequency uint32
}

func (p DLChannelReqPayload) Marshal() ([]byte, error) {
	f, err := EncodeFrequency(p.Frequency)
	if err != nil {
		return nil, fmt.Errorf("DLChannelReq: %w", err)
	}
	return []byte{p.ChIndex, f[0], f[1], f[2], 0}, nil
}

// DLChannelAnsPayload (UL, CID 0x0A).
type DLChannelAnsPayload struct {
	ChannelFreqOK    bool
	UplinkFreqExists bool
}

// ParseDLChannelAns reads the two bits a device returns for a DLChannelReq:
// whether the downlink frequency is acceptable, and whether a paired uplink
// frequency exists to schedule against.
func ParseDLChannelAns(payload []byte) (DLChannelAnsPayload, error) {
	if len(payload) != 1 {
		return DLChannelAnsPayload{}, errLen("DLChannelAns", 1, len(payload))
	}
	return DLChannelAnsPayload{
		ChannelFreqOK:    payload[0]&0x01 != 0,
		UplinkFreqExists: payload[0]&0x02 != 0,
	}, nil
}
