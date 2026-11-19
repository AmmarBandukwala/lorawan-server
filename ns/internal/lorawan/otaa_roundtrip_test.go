package lorawan

import (
	"encoding/hex"
	"testing"
)

// End-to-end M1 check: simulated device joins, derives session keys, and
// sends an uplink the NS can validate and decrypt.
func TestOTAARoundTrip(t *testing.T) {
	appKey := mustKey(t, "00112233445566778899aabbccddeeff")
	netID, _ := ParseNetID("010203")
	devAddr, _ := ParseDevAddr("03020100")
	joinEUI, _ := ParseEUI64("0101010101010101")
	devEUI, _ := ParseEUI64("0202020202020202")
	const joinNonce uint32 = 0x0A0B0C
	const devNonce uint16 = 0x1A2B

	// Device -> NS: join-request.
	req := &PHYPayload{
		MHDR:        MHDR{MType: MTypeJoinRequest},
		JoinRequest: &JoinRequestPayload{JoinEUI: joinEUI, DevEUI: devEUI, DevNonce: devNonce},
	}
	if err := req.SetJoinRequestMIC(appKey); err != nil {
		t.Fatal(err)
	}
	reqWire, err := req.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	parsedReq, err := ParsePHYPayload(reqWire)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := parsedReq.ValidateJoinRequestMIC(appKey); !ok {
		t.Fatal("join-request MIC invalid")
	}

	// NS -> device: join-accept.
	acceptWire, err := BuildJoinAccept(JoinAcceptPayload{
		JoinNonce: joinNonce,
		NetID:     netID,
		DevAddr:   devAddr,
		RxDelay:   1,
	}, appKey)
	if err != nil {
		t.Fatal(err)
	}
	accept, _, err := ParseJoinAccept(acceptWire, appKey)
	if err != nil {
		t.Fatal(err)
	}

	// Both sides derive the same session keys.
	nwk, err := DeriveNwkSKey(appKey, accept.JoinNonce, accept.NetID, devNonce)
	if err != nil {
		t.Fatal(err)
	}
	appS, err := DeriveAppSKey(appKey, accept.JoinNonce, accept.NetID, devNonce)
	if err != nil {
		t.Fatal(err)
	}

	// Device -> NS: confirmed uplink with app payload.
	fport := byte(1)
	up := &PHYPayload{
		MHDR: MHDR{MType: MTypeConfirmedDataUp},
		MAC: &MACPayload{
			FHDR:       FHDR{DevAddr: devAddr, FCnt: 1},
			FPort:      &fport,
			FRMPayload: []byte{0xDE, 0xAD, 0xBE, 0xEF},
		},
	}
	if err := up.EncryptFRMPayload(appS, DirUplink, 1); err != nil {
		t.Fatal(err)
	}
	if err := up.SetDataMIC(DirUplink, 1, nwk); err != nil {
		t.Fatal(err)
	}
	upWire, err := up.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	parsedUp, err := ParsePHYPayload(upWire)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := parsedUp.ValidateDataMIC(DirUplink, 1, nwk); !ok {
		t.Fatal("uplink MIC invalid")
	}
	if err := parsedUp.DecryptFRMPayload(appS, DirUplink, 1); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(parsedUp.MAC.FRMPayload); got != "deadbeef" {
		t.Errorf("FRMPayload = %s, want deadbeef", got)
	}
}

// Self-consistency: build a downlink, re-parse and validate (dir=1).
func TestDownlinkData_RoundTrip(t *testing.T) {
	nwkSKey := mustKey(t, "0102030405060708090a0b0c0d0e0f10")
	appSKey := mustKey(t, "100f0e0d0c0b0a090807060504030201")
	devAddr, _ := ParseDevAddr("01020304")
	fport := byte(3)

	dl := &PHYPayload{
		MHDR: MHDR{MType: MTypeUnconfirmedDataDown},
		MAC: &MACPayload{
			FHDR:       FHDR{DevAddr: devAddr, FCnt: 42},
			FPort:      &fport,
			FRMPayload: []byte{0x01, 0x02},
		},
	}
	if err := dl.EncryptFRMPayload(appSKey, DirDownlink, 42); err != nil {
		t.Fatal(err)
	}
	if err := dl.SetDataMIC(DirDownlink, 42, nwkSKey); err != nil {
		t.Fatal(err)
	}
	wire, err := dl.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePHYPayload(wire)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := parsed.ValidateDataMIC(DirDownlink, 42, nwkSKey); !ok {
		t.Fatal("downlink MIC invalid")
	}
	if err := parsed.DecryptFRMPayload(appSKey, DirDownlink, 42); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(parsed.MAC.FRMPayload); got != "0102" {
		t.Errorf("FRMPayload = %s, want 0102", got)
	}
}
