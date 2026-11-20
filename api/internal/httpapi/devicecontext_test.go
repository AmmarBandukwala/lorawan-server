package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestValidateAttributes covers the guard in front of the console's key/value
// editor. The rules exist so one device cannot accumulate 300 near-duplicate tags
// and so a key that differs from another only by case or spacing is refused at
// the door rather than becoming two labels for one thing.
func TestValidateAttributes(t *testing.T) {
	ptr := func(m map[string]string) *map[string]string { return &m }
	long := strings.Repeat("x", MaxAttributeValueLen+1)
	longKey := strings.Repeat("k", MaxAttributeKeyLen+1)

	cases := []struct {
		name string
		in   *map[string]string
		want string
	}{
		{"absent is not an error", nil, ""},
		{"an empty map is accepted", ptr(map[string]string{}), ""},
		{"a normal pair is accepted", ptr(map[string]string{"line": "3"}), ""},
		{"a dash and underscore key is accepted",
			ptr(map[string]string{"cold_store-2": "chiller"}), ""},
		{"too many attributes", ptr(nAttributes(MaxAttributes + 1)), ErrTooManyAttributes},
		{"an empty key", ptr(map[string]string{"": "v"}), ErrAttributeKey},
		{"a key of only spaces", ptr(map[string]string{"   ": "v"}), ErrAttributeKey},
		{"a key with a space", ptr(map[string]string{"rooftop antenna": "v"}), ErrAttributeKey},
		{"a key with punctuation", ptr(map[string]string{"line.3": "v"}), ErrAttributeKey},
		{"an over-long key", ptr(map[string]string{longKey: "v"}), ErrAttributeKey},
		{"an empty value", ptr(map[string]string{"line": ""}), ErrAttributeValue},
		{"a value of only spaces", ptr(map[string]string{"line": "   "}), ErrAttributeValue},
		{"an over-long value", ptr(map[string]string{"line": long}), ErrAttributeValue},
	}

	for _, tc := range cases {
		if got := validateAttributes(tc.in); got != tc.want {
			t.Errorf("%s: validateAttributes = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestValidateAttributesAcceptsTheLimitBoundary pins MaxAttributes as inclusive.
// The check is `> MaxAttributes`, so exactly 32 must pass; an off-by-one here
// turns a documented limit into a surprising rejection.
func TestValidateAttributesAcceptsTheLimitBoundary(t *testing.T) {
	at := nAttributes(MaxAttributes)
	if got := validateAttributes(&at); got != "" {
		t.Errorf("exactly %d attributes rejected: %q", MaxAttributes, got)
	}
	over := nAttributes(MaxAttributes + 1)
	if got := validateAttributes(&over); got != ErrTooManyAttributes {
		t.Errorf("%d attributes accepted, want %q", MaxAttributes+1, got)
	}
}

func nAttributes(n int) map[string]string {
	m := make(map[string]string, n)
	for i := 0; i < n; i++ {
		m[string(rune('a'+i%26))+strings.Repeat("k", i/26)] = "v"
	}
	return m
}

// TestValidateLocation covers the position guard. The distinction that matters is
// between "no position supplied" and "a half position": the schema refuses a
// latitude with no longitude, so the API must refuse it too and say why, rather
// than let the request fail deeper down with a database error the console cannot
// explain.
func TestValidateLocation(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"absent", "", ""},
		{"an explicit null clears the position", "null", ""},
		{"a full position", `{"latitude":51.5,"longitude":-0.12}`, ""},
		{"with an altitude", `{"latitude":51.5,"longitude":-0.12,"altitude":14}`, ""},
		{"sea level altitude", `{"latitude":51.5,"longitude":-0.12,"altitude":0}`, ""},
		{"a negative altitude", `{"latitude":51.5,"longitude":-0.12,"altitude":-200}`, ""},
		{"the pole boundary", `{"latitude":90,"longitude":180}`, ""},
		{"the opposite pole boundary", `{"latitude":-90,"longitude":-180}`, ""},

		{"a latitude alone", `{"latitude":51.5}`, ErrLocationIncomplete},
		{"a longitude alone", `{"longitude":-0.12}`, ErrLocationIncomplete},
		{"an altitude alone", `{"altitude":14}`, ErrLocationIncomplete},
		{"an empty object", `{}`, ErrLocationIncomplete},
		{"a latitude of null", `{"latitude":null,"longitude":-0.12}`, ErrLocationIncomplete},
		{"not an object", `"somewhere"`, ErrLocationIncomplete},
		{"malformed JSON", `{"latitude":`, ErrLocationIncomplete},

		{"a latitude past the pole", `{"latitude":91,"longitude":0}`, ErrLocationRange},
		{"a latitude below the pole", `{"latitude":-91,"longitude":0}`, ErrLocationRange},
		{"a longitude past 180", `{"latitude":0,"longitude":181}`, ErrLocationRange},
		{"an impossible altitude", `{"latitude":0,"longitude":0,"altitude":10001}`, ErrLocationRange},
		{"an altitude below the floor", `{"latitude":0,"longitude":0,"altitude":-1001}`, ErrLocationRange},
	}

	for _, tc := range cases {
		got := validateLocation(json.RawMessage(tc.raw))
		if got != tc.want {
			t.Errorf("%s: validateLocation(%q) = %q, want %q", tc.name, tc.raw, got, tc.want)
		}
	}
}

// TestValidateLocationZeroIsARealCoordinate: 0,0 is a place, and a device parked
// there must validate. Treating zero as "not supplied" would make the Gulf of
// Guinea unrepresentable and, worse, would read as an incomplete position.
func TestValidateLocationZeroIsARealCoordinate(t *testing.T) {
	if got := validateLocation(json.RawMessage(`{"latitude":0,"longitude":0}`)); got != "" {
		t.Errorf("0,0 rejected: %q", got)
	}
}
