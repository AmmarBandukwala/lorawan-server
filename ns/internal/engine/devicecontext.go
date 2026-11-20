package engine

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Device context: operator metadata that describes what a device is for and
// where it sits.
//
// Neither the attributes nor the position is protocol state — the engine never
// reads either, exactly as it ignores Device.Group. They are here because a
// device has one home, in one table, and the alternative (a second dashboard
// table keyed by DevEUI) is the arrangement that once produced a console showing
// an empty fleet while frames were being received.

// Limits on operator metadata. They exist because the console has to render the
// result on a small screen and because an unbounded map is a way for one bad
// client to make every device read slow.
const (
	// MaxAttributes is how many key/value pairs one device may carry.
	MaxAttributes = 32
	// MaxAttributeKeyLen and MaxAttributeValueLen bound one pair. A value long
	// enough to be a paragraph belongs in a note, not in a tag.
	MaxAttributeKeyLen   = 40
	MaxAttributeValueLen = 200
)

// Location is where a device is installed, in WGS84 degrees and metres above
// mean sea level.
//
// It is a pointer on Device, and the distinction it carries matters: a device
// with no recorded position and a device at 0,0 are different facts. The gateway
// table cannot make that distinction because a forwarder reports 0/0 when it has
// no GPS fix; a device position is typed by a person, so "absent" is available.
type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	// Altitude is metres above mean sea level. Zero is a real altitude, so it
	// is not usable as a "not given" marker; the enclosing pointer does that.
	Altitude int `json:"altitude"`
}

// NormalizeAttributes validates and canonicalises an attribute map, returning
// the form to store.
//
// Keys are lower-cased and whitespace-trimmed so that "Line", "line " and
// "LINE" cannot become three separate tags describing one thing. The engine does
// not read these values, but the console groups and displays them, and a
// fleet-wide report that treats case variants as different keys is a bug an
// operator will attribute to the software rather than the typing.
//
// A nil or empty map is valid and means "no attributes"; it normalises to nil so
// the API can omit the field rather than emit an empty object for every device.
func NormalizeAttributes(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > MaxAttributes {
		return nil, fmt.Errorf("engine: a device may carry at most %d attributes, got %d",
			MaxAttributes, len(in))
	}

	out := make(map[string]string, len(in))
	for rawKey, rawValue := range in {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		if err := checkAttributeKey(key); err != nil {
			return nil, err
		}
		value := strings.TrimSpace(rawValue)
		if len(value) > MaxAttributeValueLen {
			return nil, fmt.Errorf("engine: attribute %q is %d characters, the limit is %d",
				key, len(value), MaxAttributeValueLen)
		}
		// An empty value is a key with no information. Dropping it silently would
		// be surprising, so it is refused: the caller can delete the pair instead.
		if value == "" {
			return nil, fmt.Errorf("engine: attribute %q has an empty value; remove it instead", key)
		}
		out[key] = value
	}
	return out, nil
}

// checkAttributeKey enforces the canonical key form. It runs after normalisation,
// so a failure here means the key contains something genuinely unusable rather
// than merely untidy.
func checkAttributeKey(key string) error {
	if key == "" {
		return fmt.Errorf("engine: an attribute key cannot be empty")
	}
	if len(key) > MaxAttributeKeyLen {
		return fmt.Errorf("engine: attribute key %q is %d characters, the limit is %d",
			key, len(key), MaxAttributeKeyLen)
	}
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("engine: attribute key %q must use letters, digits, '-' or '_' "+
			"(it is lower-cased and trimmed for you)", key)
	}
	return nil
}

// NormalizeLocation validates a position and returns the form to store. A nil
// pointer means "no position recorded" and is valid.
//
// Altitude is not part of the completeness rule: a device can be placed on a map
// from a phone without anyone knowing its height, and refusing that would push
// operators to type a plausible zero instead of leaving it blank. Latitude and
// longitude, by contrast, only mean something as a pair.
func NormalizeLocation(in *Location) (*Location, error) {
	if in == nil {
		return nil, nil
	}
	out := *in
	if out.Latitude < -90 || out.Latitude > 90 {
		return nil, fmt.Errorf("engine: latitude %g is outside -90 to 90", out.Latitude)
	}
	if out.Longitude < -180 || out.Longitude > 180 {
		return nil, fmt.Errorf("engine: longitude %g is outside -180 to 180", out.Longitude)
	}
	if out.Altitude < -1000 || out.Altitude > 10000 {
		return nil, fmt.Errorf("engine: altitude %d m is outside -1000 to 10000", out.Altitude)
	}
	return &out, nil
}

// SortedAttributeKeys returns an attribute map's keys in a stable order.
//
// JSON objects have no order and Go map iteration has none either, so a console
// that rendered them directly would reshuffle a device's tags on every refresh.
// The caller sorts; the map stays a map.
func SortedAttributeKeys(attrs map[string]string) []string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
