package lorawan

import "testing"

func TestFrequencyEncodeDecode(t *testing.T) {
	// 868.1 MHz -> 8681000 (100 Hz steps) -> 0x847628 -> LE 28 76 84,
	// the well-known first entry of the EU868 join-accept CFList.
	got, err := EncodeFrequency(868_100_000)
	if err != nil {
		t.Fatal(err)
	}
	want := [3]byte{0x28, 0x76, 0x84}
	if got != want {
		t.Fatalf("EncodeFrequency(868.1MHz) = %x, want %x", got, want)
	}

	for _, hz := range []uint32{868_100_000, 869_525_000, 923_300_000, 902_300_000} {
		enc, err := EncodeFrequency(hz)
		if err != nil {
			t.Fatalf("EncodeFrequency(%d): %v", hz, err)
		}
		back, err := DecodeFrequency(enc[:])
		if err != nil {
			t.Fatal(err)
		}
		if back != hz {
			t.Errorf("round-trip %d -> %d", hz, back)
		}
	}

	if _, err := EncodeFrequency(868_100_001); err == nil {
		t.Error("expected error for non-100Hz multiple")
	}
}

func TestEUI64WireOrder(t *testing.T) {
	e, err := ParseEUI64("0807060504030201")
	if err != nil {
		t.Fatal(err)
	}
	le := e.BytesLE()
	want := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	for i := range want {
		if le[i] != want[i] {
			t.Fatalf("BytesLE = %x, want %x", le, want)
		}
	}
	back, err := EUI64FromBytesLE(le)
	if err != nil || back != e {
		t.Fatalf("round-trip failed: %v %v", back, err)
	}
}
