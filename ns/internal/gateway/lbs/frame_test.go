package lbs

import (
	"encoding/hex"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// stationView builds the decoded view a station sends, from a real frame.
//
// This is what makes the reconstruction tests worth having: the input comes from
// the same codec the engine uses, so requiring reconstructFrame to reproduce the
// original bytes proves its byte order agrees with the reference implementation
// rather than with this file's expectations.
func stationView(t *testing.T, wire []byte) uplink {
	t.Helper()
	frame, err := lorawan.ParsePHYPayload(wire)
	if err != nil {
		t.Fatalf("ParsePHYPayload: %v", err)
	}
	msg := uplink{MHdr: uint(frame.MHDR.Byte()), MIC: int64(uint32(wire[len(wire)-4])) |
		int64(uint32(wire[len(wire)-3]))<<8 |
		int64(uint32(wire[len(wire)-2]))<<16 |
		int64(uint32(wire[len(wire)-1]))<<24}

	switch {
	case frame.JoinRequest != nil:
		msg.JoinEUI = eui(frame.JoinRequest.JoinEUI)
		msg.DevEUI = eui(frame.JoinRequest.DevEUI)
		msg.DevNonce = uint32(frame.JoinRequest.DevNonce)
	case frame.MAC != nil:
		msg.DevAddr = uint32(frame.MAC.FHDR.DevAddr)
		msg.FCtrl = uint(frame.MAC.FHDR.FCtrl.Byte())
		msg.FCnt = uint(frame.MAC.FHDR.FCnt)
		msg.FOpts = hex.EncodeToString(frame.MAC.FHDR.FOpts)
		if frame.MAC.FPort != nil {
			msg.FPort = int(*frame.MAC.FPort)
		}
		msg.FRMPayload = hex.EncodeToString(frame.MAC.FRMPayload)
	default:
		t.Fatalf("unexpected frame type in %x", wire)
	}
	return msg
}

func testDevice() *sim.Device {
	devEUI, _ := lorawan.ParseEUI64("0011223344556677")
	joinEUI, _ := lorawan.ParseEUI64("0102030405060708")
	appKey, _ := lorawan.ParseAES128Key("2b7e151628aed2a6abf7158809cf4f3c")
	netID, _ := lorawan.ParseNetID("010203")
	return sim.New(devEUI, joinEUI, appKey, netID)
}

// joinAcceptFor builds the join-accept that completes a simulated device's join,
// so the reconstruction tests can exercise real joined-session frames.
func joinAcceptFor(t *testing.T, dev *sim.Device) []byte {
	t.Helper()
	wire, err := lorawan.BuildJoinAccept(lorawan.JoinAcceptPayload{
		JoinNonce:  0x010203,
		NetID:      dev.NetID,
		DevAddr:    lorawan.DevAddr(0x01020304),
		DLSettings: lorawan.DLSettings{OptNeg: true},
		RxDelay:    5,
	}, dev.AppKey)
	if err != nil {
		t.Fatalf("BuildJoinAccept: %v", err)
	}
	return wire
}

// TestReconstructFrameJoinRequest checks the join-request byte order against
// bytes written out by the reference codec:
//
//	MHdr(1) | JoinEUI(8, little endian) | DevEUI(8, little endian) |
//	DevNonce(2, little endian) | MIC(4)
//
// Getting either EUI's order wrong is the classic failure of this path: the
// frame still parses, and the MIC then fails for a reason no log line connects
// to byte order.
func TestReconstructFrameJoinRequestByteOrder(t *testing.T) {
	dev := testDevice()
	wire, err := dev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	if len(wire) != 23 {
		t.Fatalf("a join-request must be 23 bytes, got %d", len(wire))
	}

	back, err := reconstructFrame(stationView(t, wire))
	if err != nil {
		t.Fatalf("reconstructFrame: %v", err)
	}
	if hex.EncodeToString(back) != hex.EncodeToString(wire) {
		t.Errorf("reconstructed\n  %x\ndo not match the original\n  %x", back, wire)
	}

	// A join-request cannot be rebuilt without the DevEUI, and saying so beats
	// emitting a 23-byte frame that fails its MIC.
	msg := stationView(t, wire)
	msg.DevEUI = eui{}
	if _, err := reconstructFrame(msg); err == nil {
		t.Error("a join-request view without a DevEUI must be refused")
	}
}

// TestReconstructFrameDataFrames covers the shapes a station reports after a
// device has joined. Each must come back byte-identical, and the MAC-only case
// is the one that catches an invented FPort.
func TestReconstructFrameDataFrames(t *testing.T) {
	dev := testDevice()
	if _, err := dev.NextJoinRequest(); err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}
	if err := dev.HandleJoinAccept(joinAcceptFor(t, dev)); err != nil {
		t.Fatalf("HandleJoinAccept: %v", err)
	}

	cases := map[string]sim.UplinkOptions{
		"application payload": {FPort: 10, Payload: []byte("hello"), ADR: true},
		"mac-only, no FPort":  {NoFPort: true, MACCommands: lorawan.MACCommandSet{{CID: lorawan.CIDLinkCheck}}},
		"payload and FOpts":   {FPort: 3, Payload: []byte{0x01, 0x02}, MACCommands: lorawan.MACCommandSet{{CID: lorawan.CIDDevStatus, Payload: []byte{0x08, 0x02}}}},
		"confirmed":           {FPort: 1, Payload: []byte("ack me"), Confirmed: true},
		"mac commands in FRM": {FPort: 0, Payload: []byte{byte(lorawan.CIDLinkCheck)}},
	}
	for name, opts := range cases {
		up, err := dev.NextUplink(opts)
		if err != nil {
			t.Fatalf("%s: NextUplink: %v", name, err)
		}
		back, err := reconstructFrame(stationView(t, up))
		if err != nil {
			t.Errorf("%s: reconstructFrame: %v", name, err)
			continue
		}
		if hex.EncodeToString(back) != hex.EncodeToString(up) {
			t.Errorf("%s:\n reconstructed %x\n original       %x", name, back, up)
		}
	}
}

// TestReconstructFrameRejectsImpossibleViews guards the fallback path: a view
// that contradicts the frame type would produce bytes no device sent, and the
// MIC failure downstream is painful to trace back here.
func TestReconstructFrameRejectsImpossibleViews(t *testing.T) {
	if _, err := reconstructFrame(uplink{MHdr: 0x00, FRMPayload: "aabb"}); err == nil {
		t.Error("a join-request carrying FRMPayload must be refused")
	}
	if _, err := reconstructFrame(uplink{MHdr: 0x40, FOpts: "zz"}); err == nil {
		t.Error("non-hex FOpts must be reported, not skipped")
	}
	if _, err := reconstructFrame(uplink{MHdr: 0x20}); err == nil {
		t.Error("a frame type this server cannot rebuild must be an error")
	}
}

// TestFrameBytesPrefersDrawx: the raw frame is the frame as received, so it must
// win whenever the station sends it.
func TestFrameBytesPrefersDrawx(t *testing.T) {
	raw := []byte{0x40, 0x01, 0x02, 0x03}
	msg := uplink{Drawx: encodeDrawx(raw), MHdr: 0x40}
	got, err := frameBytes(msg)
	if err != nil {
		t.Fatalf("frameBytes: %v", err)
	}
	if hex.EncodeToString(got) != "40010203" {
		t.Errorf("frameBytes = %x, want the raw frame", got)
	}

	// Without drawx the decoded view is used.
	msg = uplink{MHdr: 0x00, JoinEUI: eui{1}, DevEUI: eui{2}, DevNonce: 0x1234, MIC: 0xdeadbeef}
	got, err = frameBytes(msg)
	if err != nil {
		t.Fatalf("frameBytes without drawx: %v", err)
	}
	if len(got) != 23 {
		t.Errorf("reconstructed join-request = %d bytes, want 23", len(got))
	}
}
