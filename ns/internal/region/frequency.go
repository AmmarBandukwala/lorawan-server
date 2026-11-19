package region

// Frequency units.
//
// The engine works in hertz, where every value is an exact integer and a
// frequency check is a plain comparison. The Semtech protocol and human
// diagnostics work in megahertz, where 868.1 cannot be represented exactly in
// binary floating point. Converting between the two is therefore the one place
// where rounding is a correctness question rather than a cosmetic one, so it is
// written once here instead of inline at each call site.

// HertzPerMHz is the scale factor between the engine's unit and the protocol's.
const HertzPerMHz = 1e6

// MHzToHertz converts a megahertz value to hertz, rounding to nearest.
//
// Truncation is the bug this guards against: 868.1 MHz is 868099999.99... in
// binary floating point, so a plain conversion yields 868099999 Hz and the
// frequency then fails to match the channel table by one hertz — a failure that
// looks nothing like a rounding error when it surfaces as "unknown channel".
func MHzToHertz(mhz float64) uint32 {
	return uint32(mhz*HertzPerMHz + 0.5)
}

// HertzToMHz converts hertz to megahertz for display and for the Semtech txpk
// freq field, which is defined in megahertz.
func HertzToMHz(hz uint32) float64 {
	return float64(hz) / HertzPerMHz
}
