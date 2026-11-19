package lorawan

import "testing"

// TestDeriveSessionKeys_LoRaWAN10 checks the LoRaWAN 1.0.3 §5 key derivation
// against the worked example published with that specification's family of
// implementations, so a change here cannot silently break interoperability.
func TestDeriveSessionKeys_LoRaWAN10(t *testing.T) {
	appKey, err := ParseAES128Key("01020304050607080102030405060708")
	if err != nil {
		t.Fatal(err)
	}
	netID, err := ParseNetID("010203")
	if err != nil {
		t.Fatal(err)
	}
	const (
		joinNonce = 65536
		devNonce  = 258
	)

	nwkSKey, err := DeriveNwkSKey(appKey, joinNonce, netID, devNonce)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := nwkSKey.String(), "DF53C35F3034CCCED0FF354C70DE04DF"; got != want {
		t.Errorf("NwkSKey = %s, want %s", got, want)
	}

	appSKey, err := DeriveAppSKey(appKey, joinNonce, netID, devNonce)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := appSKey.String(), "927B9C911183CFFE4CB2FF4B75545F6D"; got != want {
		t.Errorf("AppSKey = %s, want %s", got, want)
	}
}
