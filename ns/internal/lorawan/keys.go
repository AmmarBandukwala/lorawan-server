package lorawan

import "fmt"

// Session key derivation for LoRaWAN 1.0.x (spec §5):
//
//	NwkSKey = aes128_encrypt(AppKey, 0x01 | JoinNonce | NetID | DevNonce | pad16)
//	AppSKey = aes128_encrypt(AppKey, 0x02 | JoinNonce | NetID | DevNonce | pad16)
//
// All multi-byte fields inside the derivation block are little-endian,
// matching lrwn::get_s_key with opt_neg = false.

// DeriveSessionKey derives a session key of the given type byte.
// Types used by LoRaWAN 1.0: 0x01 NwkSKey, 0x02 AppSKey. (1.1 defines
// 0x03..0x06 as well; kept here so enabling 1.1 later is additive.)
func DeriveSessionKey(appKey AES128Key, typ byte, joinNonce uint32, netID NetID, devNonce uint16) (AES128Key, error) {
	if joinNonce > MaxJoinNonce {
		return AES128Key{}, fmt.Errorf("joinNonce %d exceeds 24 bits", joinNonce)
	}
	var b [16]byte
	b[0] = typ
	b[1] = byte(joinNonce)
	b[2] = byte(joinNonce >> 8)
	b[3] = byte(joinNonce >> 16)
	copy(b[4:7], netID.BytesLE())
	b[7] = byte(devNonce)
	b[8] = byte(devNonce >> 8)
	return appKey.EncryptBlock(b), nil
}

// DeriveNwkSKey derives the network session integrity key (NwkSKey).
func DeriveNwkSKey(appKey AES128Key, joinNonce uint32, netID NetID, devNonce uint16) (AES128Key, error) {
	return DeriveSessionKey(appKey, 0x01, joinNonce, netID, devNonce)
}

// DeriveAppSKey derives the application session encryption key (AppSKey).
func DeriveAppSKey(appKey AES128Key, joinNonce uint32, netID NetID, devNonce uint16) (AES128Key, error) {
	return DeriveSessionKey(appKey, 0x02, joinNonce, netID, devNonce)
}
