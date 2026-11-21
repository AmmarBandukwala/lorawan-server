package lorawan

import "crypto/subtle"

// Direction bytes used inside the B0/Ai crypto blocks (LoRaWAN 1.0.3 §4.2.3).
const (
	DirUplink   byte = 0x00
	DirDownlink byte = 0x01
)

// --- Join request MIC (CMAC over the full message, AppKey) ---

// ComputeJoinRequestMIC returns the 4-byte MIC of a join-request.
func (p *PHYPayload) ComputeJoinRequestMIC(appKey AES128Key) ([4]byte, error) {
	body, err := p.marshalBody()
	if err != nil {
		return [4]byte{}, err
	}
	sum := AES128CMAC(appKey, body)
	var mic [4]byte
	copy(mic[:], sum[:4])
	return mic, nil
}

// SetJoinRequestMIC computes and stores the join-request MIC.
func (p *PHYPayload) SetJoinRequestMIC(appKey AES128Key) error {
	mic, err := p.ComputeJoinRequestMIC(appKey)
	if err != nil {
		return err
	}
	p.MIC = mic
	return nil
}

// ValidateJoinRequestMIC checks the stored MIC against the recomputed one.
func (p *PHYPayload) ValidateJoinRequestMIC(appKey AES128Key) (bool, error) {
	mic, err := p.ComputeJoinRequestMIC(appKey)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(mic[:], p.MIC[:]) == 1, nil
}

// --- Data frame MIC (B0 block + CMAC with NwkSKey) ---

// ComputeDataMIC computes the data-frame MIC. dir is DirUplink or
// DirDownlink; fCnt is the full 32-bit frame counter (the NS reconstructs
// it from the 16-bit wire value before calling this).
func (p *PHYPayload) ComputeDataMIC(dir byte, fCnt uint32, nwkSKey AES128Key) ([4]byte, error) {
	if p.MAC == nil {
		return [4]byte{}, errNoMACPayload()
	}
	body, err := p.marshalBody()
	if err != nil {
		return [4]byte{}, err
	}
	b0 := dataB0(dir, p.MAC.FHDR.DevAddr, fCnt, uint32(len(body)))
	sum := AES128CMAC(nwkSKey, append(b0[:], body...))
	var mic [4]byte
	copy(mic[:], sum[:4])
	return mic, nil
}

// SetDataMIC computes and stores the data-frame MIC.
func (p *PHYPayload) SetDataMIC(dir byte, fCnt uint32, nwkSKey AES128Key) error {
	mic, err := p.ComputeDataMIC(dir, fCnt, nwkSKey)
	if err != nil {
		return err
	}
	p.MIC = mic
	return nil
}

// ValidateDataMIC checks the stored MIC against the recomputed one.
func (p *PHYPayload) ValidateDataMIC(dir byte, fCnt uint32, nwkSKey AES128Key) (bool, error) {
	mic, err := p.ComputeDataMIC(dir, fCnt, nwkSKey)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(mic[:], p.MIC[:]) == 1, nil
}

// dataB0 builds the 16-byte B0 CMAC initialization block:
// 0x49 | 0x00*4 | Dir | DevAddr(LE4) | FCnt(LE4) | 0x00 | msglen.
func dataB0(dir byte, devAddr DevAddr, fCnt, msgLen uint32) [16]byte {
	var block [16]byte
	block[0] = 0x49
	block[5] = dir
	copy(block[6:10], devAddr.BytesLE())
	block[10] = byte(fCnt)
	block[11] = byte(fCnt >> 8)
	block[12] = byte(fCnt >> 16)
	block[13] = byte(fCnt >> 24)
	block[15] = byte(msgLen)
	return block
}

// --- FRMPayload encryption (RFC4493-style counter mode, LoRaWAN §4.2.3) ---

// EncryptFRMPayload XORs the FRMPayload with the keystream generated from
// key (NwkSKey for FPort 0, AppSKey otherwise). Decryption is identical.
func (p *PHYPayload) EncryptFRMPayload(key AES128Key, dir byte, fCnt uint32) error {
	if p.MAC == nil {
		return errNoMACPayload()
	}
	keystreamXOR(key, p.MAC.FHDR.DevAddr, dir, fCnt, p.MAC.FRMPayload)
	return nil
}

// EncryptFOpts XORs the FOpts field with the FPort-0 keystream.
//
// LoRaWAN 1.0.x §4.3.3: in the uplink direction FOpts carry encrypted MAC
// commands (NwkSKey, same keystream construction as an FPort-0 FRMPayload);
// in the downlink direction they are sent in the clear. Callers on the NS
// side therefore decrypt uplink FOpts and leave downlink FOpts untouched.
func (p *PHYPayload) EncryptFOpts(key AES128Key, dir byte, fCnt uint32) error {
	if p.MAC == nil {
		return errNoMACPayload()
	}
	keystreamXOR(key, p.MAC.FHDR.DevAddr, dir, fCnt, p.MAC.FHDR.FOpts)
	return nil
}

// DecryptFOpts is an alias of EncryptFOpts (the XOR keystream is symmetric).
func (p *PHYPayload) DecryptFOpts(key AES128Key, dir byte, fCnt uint32) error {
	return p.EncryptFOpts(key, dir, fCnt)
}

// keystreamXOR XORs buf in place with the LoRaWAN §4.3.3 nonce-mode keystream
// built from the A-block: 0x01 | 0000 | dir | DevAddr(LE) | FCnt(LE32) | 00 | i.
func keystreamXOR(key AES128Key, devAddr DevAddr, dir byte, fCnt uint32, buf []byte) {
	n := len(buf)
	for i := 1; (i-1)*16 < n; i++ {
		var input [16]byte
		input[0] = 0x01
		input[5] = dir
		copy(input[6:10], devAddr.BytesLE())
		input[10] = byte(fCnt)
		input[11] = byte(fCnt >> 8)
		input[12] = byte(fCnt >> 16)
		input[13] = byte(fCnt >> 24)
		input[15] = byte(i)
		s := key.EncryptBlock(input)
		for j := 0; j < 16 && (i-1)*16+j < n; j++ {
			buf[(i-1)*16+j] ^= s[j]
		}
	}
}

// DecryptFRMPayload is an alias of EncryptFRMPayload (XOR keystream is
// symmetric); it exists for call-site readability.
func (p *PHYPayload) DecryptFRMPayload(key AES128Key, dir byte, fCnt uint32) error {
	return p.EncryptFRMPayload(key, dir, fCnt)
}

func errNoMACPayload() error {
	return errNot("MAC data frame")
}
