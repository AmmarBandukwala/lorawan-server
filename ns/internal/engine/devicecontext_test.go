package engine

import (
	"strconv"
	"strings"
	"testing"
)

func TestNormalizeAttributesCanonicalisesKeys(t *testing.T) {
	in := map[string]string{" Line ": "3", "PURPOSE": " cold store "}
	got, err := NormalizeAttributes(in)
	if err != nil {
		t.Fatalf("NormalizeAttributes: %v", err)
	}
	if got["line"] != "3" {
		t.Errorf("got %v, want the key lower-cased and trimmed", got)
	}
	if got["purpose"] != "cold store" {
		t.Errorf("got %v, want the value trimmed but otherwise intact", got)
	}
}

// TestNormalizeAttributesEmptyIsNil: "no attributes" has one representation, so
// the API can omit the field instead of emitting {} for every device.
func TestNormalizeAttributesEmptyIsNil(t *testing.T) {
	for _, in := range []map[string]string{nil, {}} {
		got, err := NormalizeAttributes(in)
		if err != nil {
			t.Fatalf("NormalizeAttributes(%v): %v", in, err)
		}
		if got != nil {
			t.Errorf("NormalizeAttributes(%v) = %v, want nil", in, got)
		}
	}
}

func TestNormalizeAttributesRejects(t *testing.T) {
	cases := map[string]map[string]string{
		"empty key":         {"": "value"},
		"key of spaces":     {"   ": "value"},
		"key with space":    {"rooftop antenna": "1"},
		"key with dot":      {"rack.b2": "1"},
		"key with slash":    {"a/b": "1"},
		"empty value":       {"line": ""},
		"blank value":       {"line": "   "},
		"key too long":      {strings.Repeat("k", MaxAttributeKeyLen+1): "v"},
		"value too long":    {"line": strings.Repeat("v", MaxAttributeValueLen+1)},
		"one pair too many": manyAttributes(MaxAttributes + 1),
	}
	for name, in := range cases {
		if _, err := NormalizeAttributes(in); err == nil {
			t.Errorf("%s: NormalizeAttributes accepted %v", name, in)
		}
	}
}

// TestNormalizeAttributesAcceptsTheBoundaryValues guards against an off-by-one
// that would refuse the widest legitimate tag.
func TestNormalizeAttributesAcceptsTheBoundaryValues(t *testing.T) {
	got, err := NormalizeAttributes(map[string]string{
		strings.Repeat("k", MaxAttributeKeyLen): strings.Repeat("v", MaxAttributeValueLen),
	})
	if err != nil {
		t.Fatalf("boundary-length pair rejected: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d pairs, want 1", len(got))
	}
	if _, err := NormalizeAttributes(manyAttributes(MaxAttributes)); err != nil {
		t.Errorf("exactly %d pairs rejected: %v", MaxAttributes, err)
	}
}

// TestNormalizeAttributesAllowsDashAndUnderscore: these are the two separators
// an operator reaches for when a key needs two words, and refusing them would
// push people into "rackb2".
func TestNormalizeAttributesAllowsDashAndUnderscore(t *testing.T) {
	if _, err := NormalizeAttributes(map[string]string{"cold_store-3": "ok"}); err != nil {
		t.Errorf("dash and underscore are valid separators: %v", err)
	}
}

func manyAttributes(n int) map[string]string {
	out := make(map[string]string, n)
	for i := range n {
		out["key"+strconv.Itoa(i)] = "v"
	}
	return out
}

func TestNormalizeLocationAccepts(t *testing.T) {
	for _, in := range []*Location{
		nil,
		{Latitude: 0, Longitude: 0},
		{Latitude: -90, Longitude: 180},
		{Latitude: 51.5074, Longitude: -0.1278, Altitude: 12},
		{Latitude: 27.9881, Longitude: 86.9250, Altitude: 8849}, // Everest fits
		{Latitude: -33.86, Longitude: 151.21, Altitude: -5},     // a tunnel
	} {
		got, err := NormalizeLocation(in)
		if err != nil {
			t.Errorf("NormalizeLocation(%+v): %v", in, err)
			continue
		}
		if in == nil && got != nil {
			t.Error("a nil position must normalise to nil")
		}
	}
}

func TestNormalizeLocationRejects(t *testing.T) {
	cases := map[string]*Location{
		"latitude above 90":  {Latitude: 90.1, Longitude: 0},
		"latitude below -90": {Latitude: -90.1, Longitude: 0},
		"longitude above":    {Latitude: 0, Longitude: 180.1},
		"longitude below":    {Latitude: 0, Longitude: -180.1},
		"altitude above":     {Latitude: 0, Longitude: 0, Altitude: 10001},
		"altitude below":     {Latitude: 0, Longitude: 0, Altitude: -1001},
	}
	for name, in := range cases {
		if _, err := NormalizeLocation(in); err == nil {
			t.Errorf("%s: NormalizeLocation accepted %+v", name, in)
		}
	}
}

// TestNormalizeLocationDoesNotMutateTheCaller: the console holds the pointer it
// passed in, and a normalisation that edited it would change what the form shows
// without the operator having typed anything.
func TestNormalizeLocationDoesNotMutateTheCaller(t *testing.T) {
	in := &Location{Latitude: 51.5, Longitude: -0.12, Altitude: 12}
	got, err := NormalizeLocation(in)
	if err != nil {
		t.Fatalf("NormalizeLocation: %v", err)
	}
	if got == in {
		t.Error("NormalizeLocation returned the caller's pointer; it must return a copy")
	}
	got.Latitude = 0
	if in.Latitude != 51.5 {
		t.Error("writing through the result changed the caller's value")
	}
}

func TestSortedAttributeKeysIsStable(t *testing.T) {
	// Go map order is randomised, so this is run several times: a single pass
	// would pass by luck.
	for range 20 {
		got := SortedAttributeKeys(map[string]string{"line": "3", "aim": "cold", "rack": "b2"})
		if strings.Join(got, ",") != "aim,line,rack" {
			t.Fatalf("SortedAttributeKeys = %v, want alphabetical", got)
		}
	}
	if len(SortedAttributeKeys(nil)) != 0 {
		t.Error("a nil map must produce no keys")
	}
}
