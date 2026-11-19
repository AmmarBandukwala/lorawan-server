package testenv

import (
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// UplinkFrame wraps radio bytes in the metadata a gateway would report, on the
// band the EU868 fixtures are written for. Tests that care about the band use
// UplinkFrameOn or an Env method, which follow the environment's band.
func UplinkFrame(wire []byte, window gateway.RxWindow, freqHz uint32, dr int) gateway.UplinkFrame {
	return UplinkFrameOn(wire, window, "EU868", freqHz, dr)
}

// UplinkFrameOn is UplinkFrame with the band named, because the frame's region
// and its frequency have to agree or the engine is being asked to apply one
// band's rules to another band's airtime.
func UplinkFrameOn(wire []byte, window gateway.RxWindow, regionName string,
	freqHz uint32, dr int) gateway.UplinkFrame {

	return gateway.UplinkFrame{
		GatewayID:     MustEUI64Static(GatewayEUI),
		RFRegion:      regionName,
		RxWindow:      window,
		FrequencyHz:   freqHz,
		DataRateIndex: dr,
		UplinkID:      [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		PHYPayload:    wire,
		RSSI:          -72,
		SNR:           8.5,
	}
}

// CopyOf builds a second gateway's copy of the same frame: identical bytes,
// different receiver, different signal, different transport token.
//
// The token differs because that is what the wire does — each gateway derives its
// own from its own concentrator — and because the downlink has to be timed from
// the copy the engine chose, not from another gateway's. A test that reused one
// token could not tell those two things apart.
func CopyOf(wire []byte, gatewayEUI lorawan.EUI64, snr, rssi float64, token byte) gateway.UplinkFrame {
	f := UplinkFrame(wire, gateway.RxWindow1, EU868Ch0Hz, EU868DR5)
	f.GatewayID = gatewayEUI
	f.SNR = snr
	f.RSSI = rssi
	f.UplinkID = [8]byte{token, token, token, token, token, token, token, token}
	return f
}

// CopyOf is the environment's band version of the package CopyOf: a second
// gateway's reception of the same frame, placed on the channel this environment
// transmits on.
func (env *Env) CopyOf(wire []byte, gatewayEUI lorawan.EUI64, snr, rssi float64, token byte) gateway.UplinkFrame {
	f := env.frame(wire, gateway.RxWindow1)
	f.GatewayID = gatewayEUI
	f.SNR = snr
	f.RSSI = rssi
	f.UplinkID = [8]byte{token, token, token, token, token, token, token, token}
	return f
}
