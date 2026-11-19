package lorawan

import (
	"encoding/hex"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func mustKey(t *testing.T, s string) AES128Key {
	t.Helper()
	k, err := ParseAES128Key(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Vector from lrwn phy_payload.rs join-request doc example.
func TestJoinRequest_MarshalAndMIC(t *testing.T) {
	appKey := mustKey(t, "0102030405060708090a0b0c0d0e0f10")
	joinEUI, _ := ParseEUI64("0101010101010101")
	devEUI, _ := ParseEUI64("0202020202020202")

	p := &PHYPayload{
		MHDR: MHDR{MType: MTypeJoinRequest, Major: 0},
		JoinRequest: &JoinRequestPayload{
			JoinEUI:  joinEUI,
			DevEUI:   devEUI,
			DevNonce: 771,
		},
	}
	if err := p.SetJoinRequestMIC(appKey); err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(p.MIC[:]), "09b97b32"; got != want {
		t.Fatalf("MIC = %s, want %s", got, want)
	}
	got, err := p.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	want := mustHex(t, "0001010101010101010202020202020202030309b97b32")
	if hex.EncodeToString(got) != hex.EncodeToString(want) {
		t.Fatalf("marshal = %x, want %x", got, want)
	}

	parsed, err := ParsePHYPayload(want)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := parsed.ValidateJoinRequestMIC(appKey)
	if err != nil || !ok {
		t.Fatalf("ValidateJoinRequestMIC = %v, %v", ok, err)
	}
	if parsed.JoinRequest.DevNonce != 771 {
		t.Errorf("DevNonce = %d, want 771", parsed.JoinRequest.DevNonce)
	}
}

// Vector from lrwn phy_payload.rs LoRaWAN 1.0.x join-accept doc example.
func TestJoinAccept_BuildAndParse(t *testing.T) {
	appKey := mustKey(t, "0102030405060708090a0b0c0d0e0f10")
	devAddr, _ := ParseDevAddr("01020304")
	netID, _ := ParseNetID("020202")

	pl := JoinAcceptPayload{
		JoinNonce:  65793,
		NetID:      netID,
		DevAddr:    devAddr,
		DLSettings: DLSettings{RX2DataRate: 0, RX1DROffset: 0},
		RxDelay:    0,
	}
	wire, err := BuildJoinAccept(pl, appKey)
	if err != nil {
		t.Fatal(err)
	}
	want := mustHex(t, "2023cf335489aae3183c0be0baa8dee5f3")
	if hex.EncodeToString(wire) != hex.EncodeToString(want) {
		t.Fatalf("BuildJoinAccept = %x, want %x", wire, want)
	}

	parsed, mic, err := ParseJoinAccept(wire, appKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(mic[:]); got != "3449f212" {
		t.Errorf("MIC = %s, want 3449f212", got)
	}
	if parsed.JoinNonce != pl.JoinNonce || parsed.NetID != pl.NetID ||
		parsed.DevAddr != pl.DevAddr || parsed.DLSettings != pl.DLSettings ||
		parsed.RxDelay != pl.RxDelay || parsed.CFList != nil {
		t.Errorf("round-trip mismatch: %+v", parsed)
	}

	// Wrong AppKey must fail MIC validation.
	if _, _, err := ParseJoinAccept(wire, mustKey(t, "ff02030405060708090a0b0c0d0e0f10")); err == nil {
		t.Error("expected MIC failure with wrong AppKey")
	}
}

// Vector from lrwn phy_payload.rs LoRaWAN 1.0.x confirmed-uplink doc example.
func TestUplinkData_ParseValidateDecrypt(t *testing.T) {
	nwkSKey := mustKey(t, "0102030405060708090a0b0c0d0e0f10")
	appSKey := mustKey(t, "100f0e0d0c0b0a090807060504030201")
	wire := mustHex(t, "80040302010300000673070ae264d4f7e117d2c0")

	p, err := ParsePHYPayload(wire)
	if err != nil {
		t.Fatal(err)
	}
	if p.MHDR.MType != MTypeConfirmedDataUp {
		t.Errorf("MType = %s, want ConfirmedDataUp", p.MHDR.MType)
	}
	if p.MAC.FHDR.DevAddr.String() != "01020304" {
		t.Errorf("DevAddr = %s, want 01020304", p.MAC.FHDR.DevAddr)
	}
	if p.MAC.FPort == nil || *p.MAC.FPort != 10 {
		t.Errorf("FPort = %v, want 10", p.MAC.FPort)
	}

	ok, err := p.ValidateDataMIC(DirUplink, 0, nwkSKey)
	if err != nil || !ok {
		t.Fatalf("ValidateDataMIC = %v, %v", ok, err)
	}

	if err := p.DecryptFRMPayload(appSKey, DirUplink, 0); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(p.MAC.FRMPayload); got != "01020304" {
		t.Errorf("FRMPayload = %s, want 01020304", got)
	}

	// FOpts carries DevStatusAns {battery: 115, snr: 7}.
	set, err := ParseMACCommandSet(p.MAC.FHDR.FOpts, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 1 || set[0].CID != CIDDevStatus {
		t.Fatalf("FOpts = %+v", set)
	}
	ans, err := ParseDevStatusAns(set[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if ans.Battery != 115 || ans.SNR != 7 {
		t.Errorf("DevStatusAns = %+v, want {115, 7}", ans)
	}
}
