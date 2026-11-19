package lorawan

// AES128CMAC computes the AES-128 CMAC of msg as specified by RFC 4493.
// LoRaWAN uses CMAC for frame MICs (uplink/downlink and join messages).
func AES128CMAC(key AES128Key, msg []byte) [16]byte {
	// Step 1: generate the subkeys K1, K2 from L = AES-128(key, 0^128).
	var zero [16]byte
	l := key.EncryptBlock(zero)

	var k1 [16]byte
	shiftLeft(l, &k1)
	if l[0]&0x80 != 0 {
		k1[15] ^= 0x87
	}
	var k2 [16]byte
	shiftLeft(k1, &k2)
	if k1[0]&0x80 != 0 {
		k2[15] ^= 0x87
	}
	// Step 2: split the message into n blocks, padding the last one when
	// it is incomplete (n >= 1 even for an empty message).
	n := (len(msg) + 15) / 16
	if n == 0 {
		n = 1
	}
	lastComplete := len(msg) > 0 && len(msg)%16 == 0

	var lastBlock [16]byte
	if lastComplete {
		copy(lastBlock[:], msg[(n-1)*16:])
		xorBlock(&lastBlock, k1)
	} else {
		tail := msg[(n-1)*16:]
		copy(lastBlock[:], tail)
		lastBlock[len(tail)] = 0x80
		xorBlock(&lastBlock, k2)
	}

	// Step 3: CBC-MAC over the first n-1 blocks, then the final block.
	var x [16]byte
	for i := range n - 1 {
		var block [16]byte
		copy(block[:], msg[i*16:])
		xorBlock(&x, block)
		x = key.EncryptBlock(x)
	}
	xorBlock(&x, lastBlock)
	return key.EncryptBlock(x)
}

func shiftLeft(in [16]byte, out *[16]byte) {
	var carry byte
	for i := 15; i >= 0; i-- {
		out[i] = in[i]<<1 | carry
		carry = in[i] >> 7
	}
}

func xorBlock(dst *[16]byte, src [16]byte) {
	for i := range dst {
		dst[i] ^= src[i]
	}
}
