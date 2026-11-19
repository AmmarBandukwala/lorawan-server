package lorawan

import (
	"encoding/hex"
	"testing"
)

// RFC 4493 Appendix B.1 test vectors for AES-128-CMAC (key
// 2b7e151628aed2a6abf7158809cf4f3c).
func TestAES128CMAC_RFC4493(t *testing.T) {
	key, err := ParseAES128Key("2b7e151628aed2a6abf7158809cf4f3c")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		msg  string
		want string
	}{
		{"", "bb1d6929e95937287fa37d129b756746"},
		{"6bc1bee22e409f96e93d7e117393172a", "070a16b46b4d4144f79bdd9dd04a287c"},
		{"6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411", "dfa66747de9ae63030ca32611497c827"},
		{"6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411e5fbc1191a0a52eff69f2445df4f9b17ad2b417be66c3710", "51f0bebf7e3b9d92fc49741779363cfe"},
	}
	for _, tc := range tests {
		msg, err := hex.DecodeString(tc.msg)
		if err != nil {
			t.Fatal(err)
		}
		got := AES128CMAC(key, msg)
		if gotHex := hex.EncodeToString(got[:]); gotHex != tc.want {
			t.Errorf("CMAC(%s) = %s, want %s", tc.msg, gotHex, tc.want)
		}
	}
}
