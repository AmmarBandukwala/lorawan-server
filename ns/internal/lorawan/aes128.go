package lorawan

import "crypto/aes"

// aes.NewCipher only fails for invalid key sizes, which is impossible for a
// fixed [16]byte key, so these helpers panic-on-impossible to keep call sites
// readable (same reasoning as the stdlib's MustCompile convention).

// EncryptBlock performs a single AES-128 ECB block encryption.
func (k AES128Key) EncryptBlock(src [16]byte) [16]byte {
	c, err := aes.NewCipher(k[:])
	if err != nil {
		panic("lorawan: invalid AES key size: " + err.Error())
	}
	var dst [16]byte
	c.Encrypt(dst[:], src[:])
	return dst
}

// DecryptBlock performs a single AES-128 ECB block decryption.
func (k AES128Key) DecryptBlock(src [16]byte) [16]byte {
	c, err := aes.NewCipher(k[:])
	if err != nil {
		panic("lorawan: invalid AES key size: " + err.Error())
	}
	var dst [16]byte
	c.Decrypt(dst[:], src[:])
	return dst
}
