package lorawan

// FullFCnt reconstructs the 32-bit frame counter of a frame that carries only
// its low 16 bits. LoRaWAN 1.0.3 §4.2.6 transmits only the low half, so the
// receiver has to infer the high half from the counter it is already holding;
// the rules below pin every edge case, including rollover and re-transmission.
//
// nextExpected is the counter value the caller expects to see next: for the NS
// that is Session.FCntUp, for a device it is FCntDown. Note this is the *next
// expected* value, not the last one accepted.
//
// Two outcomes need attention by the caller:
//
//   - result == nextExpected      -> a normal, in-order frame
//   - result == nextExpected - 1  -> a re-transmission of the previous frame
//
// The result may also be lower than nextExpected-1 after a device-side counter
// reset; callers must decide whether to accept that.
func FullFCnt(nextExpected uint32, truncated uint16) uint32 {
	next16 := uint16(nextExpected) // nextExpected modulo 2^16
	if truncated == next16-1 {     // uint16 arithmetic wraps like Rust's wrapping_sub
		return nextExpected - 1
	}
	gap := uint32(truncated - next16) // 16-bit gap, widened
	return nextExpected + gap         // 32-bit wrap on rollover
}
