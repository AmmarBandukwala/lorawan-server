package lbs

import (
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func mustEUI(t *testing.T, s string) eui {
	t.Helper()
	v, err := parseEUI(s)
	if err != nil {
		t.Fatalf("parseEUI(%q): %v", s, err)
	}
	return v
}

// TestParseEUIAcceptedSpellings covers what operators and firmwares actually
// send. Refusing a spelling a build uses turns into a gateway that connects and
// never registers, which is one of the hardest faults to see from the server side.
func TestParseEUIAcceptedSpellings(t *testing.T) {
	want := eui{0x00, 0x16, 0xCD, 0x00, 0x00, 0x00, 0x12, 0x34}
	for _, s := range []string{
		"eui-0016CD0000001234", // the canonical wire form
		"eui-0016cd0000001234", // lower case
		"EUI-0016CD0000001234", // upper-case prefix
		"0016CD0000001234",     // bare hex
		"00:16:cd:00:00:00:12:34",
		"00-16-CD-00-00-00-12-34",
		"  eui-0016CD0000001234  ",
	} {
		got, err := parseEUI(s)
		if err != nil {
			t.Errorf("parseEUI(%q): %v", s, err)
			continue
		}
		if got != want {
			t.Errorf("parseEUI(%q) = %x, want %x", s, got, want)
		}
	}
}

func TestParseEUIRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "eui-", "0016CD000000123", "0016CD00000012345", "eui-zzzzzzzzzzzzzzzz"} {
		if _, err := parseEUI(s); err == nil {
			t.Errorf("parseEUI(%q) accepted an invalid identifier", s)
		}
	}
}

// TestEUIJSONRoundTrip pins both the prefixed wire form and the numeric form some
// builds send. A numeric value must be read big-endian: reading it little-endian
// would register the gateway under a different EUI than it configured.
func TestEUIJSONRoundTrip(t *testing.T) {
	var got eui
	if err := json.Unmarshal([]byte(`"eui-0016CD0000001234"`), &got); err != nil {
		t.Fatalf("unmarshal the quoted form: %v", err)
	}
	if got != (eui{0x00, 0x16, 0xCD, 0x00, 0x00, 0x00, 0x12, 0x34}) {
		t.Errorf("quoted form decoded to %x", got)
	}

	// 6417849371333172 is 0x0016CD0000001234. A station that sends the EUI as a
	// number is unusual but legal, and reading it little-endian would register
	// the gateway under a different EUI than it configured.
	var numeric eui
	if err := json.Unmarshal([]byte(`6417849371333172`), &numeric); err != nil {
		t.Fatalf("unmarshal the numeric form: %v", err)
	}
	if want := (eui{0x00, 0x16, 0xCD, 0x00, 0x00, 0x00, 0x12, 0x34}); numeric != want {
		t.Errorf("numeric form decoded to %s, want %s (big endian)", numeric, want)
	}

	out, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `"eui-0016CD0000001234"` {
		t.Errorf("marshal = %s, want the canonical prefixed form", out)
	}
}

// TestXTimeLayout pins the split that makes Basic Station downlinks safe. The
// session identifier in the top 16 bits is what lets a station reject a schedule
// computed against a counter that has already restarted.
func TestXTimeLayout(t *testing.T) {
	const session = uint64(0xABCD)
	const counter = uint64(0x0000FFFFFFFFFFFF) // the largest counter
	x := xtime(session<<48 | counter)

	if x.session() != session {
		t.Errorf("session = %04x, want %04x", x.session(), session)
	}
	if x.counter() != counter {
		t.Errorf("counter = %x, want %x", x.counter(), counter)
	}

	// Replacing the counter must leave the session alone, and a counter wider
	// than 48 bits must be masked rather than spill into the session bits.
	replaced := x.withCounter(0x1_FFFF_FFFF_FFFF)
	if replaced.session() != session {
		t.Errorf("withCounter leaked into the session bits: %04x", replaced.session())
	}
	if replaced.counter() != counter {
		t.Errorf("withCounter = %x, want the counter masked to %x", replaced.counter(), counter)
	}

	if got := xtime(0).String(); got != "xtime(s=0000 t=0)" {
		t.Errorf("String = %q", got)
	}
}

// TestXTimeTokenRoundTrip checks the transport token carries the whole xtime.
// Losing the session half would make every downlink silently undeliverable.
func TestXTimeTokenRoundTrip(t *testing.T) {
	x := xtime(0x1234_0000_0102_0304)
	if back := xTimeFromToken(uplinkToken(x)); back != x {
		t.Errorf("token round trip = %s, want %s", back, x)
	}
}

func TestDecodeDrawx(t *testing.T) {
	raw := []byte{0x40, 0x01, 0x02, 0xff}
	encoded := encodeDrawx(raw)
	// base64url without padding is what the protocol sends.
	if got := encoded; got == "" || got[len(got)-1] == '=' {
		t.Errorf("encodeDrawx produced %q, want unpadded base64url", got)
	}
	back, err := decodeDrawx(encoded)
	if err != nil {
		t.Fatalf("decodeDrawx: %v", err)
	}
	if hex.EncodeToString(back) != hex.EncodeToString(raw) {
		t.Errorf("round trip = %x, want %x", back, raw)
	}

	// Some builds pad the value; both spellings must decode.
	padded := base64Padded(raw)
	if back, err := decodeDrawx(padded); err != nil || hex.EncodeToString(back) != "400102ff" {
		t.Errorf("padded drawx decoded to %x/%v", back, err)
	}

	if _, err := decodeDrawx(""); err == nil {
		t.Error("an empty drawx must be an error, not an empty frame")
	}
	if _, err := decodeDrawx("not base64 !!"); err == nil {
		t.Error("a malformed drawx must be an error")
	}
}

func TestArrivalTimePreferenceOrder(t *testing.T) {
	fixed := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixed.Add(time.Hour) }

	// An explicit UTC time from a GPS-locked station wins.
	msg := uplink{}
	msg.Radio.Time = "2026-11-17T10:00:00.123456Z"
	msg.UpInfo.RxTime = 1e9
	if got := arrivalTime(msg, now); got.Nanosecond() != 123456000 {
		t.Errorf("Time should win, got %v", got)
	}

	// Without it, the station's UNIX time is used, with sub-second precision.
	msg = uplink{}
	msg.UpInfo.RxTime = 1700000000.25
	got := arrivalTime(msg, now)
	if got.Unix() != 1700000000 || got.Nanosecond() != 250000000 {
		t.Errorf("RxTime decoded to %v, want 1700000000.25 UTC", got)
	}
	if got.Location() != time.UTC {
		t.Errorf("arrival time should be UTC, got %v", got.Location())
	}

	// With neither, the server's clock beats a zero time, which renders as
	// "never" on the dashboard.
	if got := arrivalTime(uplink{}, now); !got.Equal(fixed.Add(time.Hour)) {
		t.Errorf("fallback = %v, want the server clock", got)
	}
}

// base64Padded is the padded spelling of the same value, which some builds send.
// It is built by hand rather than with encoding/base64 so the test does not
// depend on the implementation's choice of encoding.
func base64Padded(raw []byte) string {
	out := encodeDrawx(raw)
	switch len(raw) % 3 {
	case 1:
		return out + "=="
	case 2:
		return out + "="
	}
	return out
}
