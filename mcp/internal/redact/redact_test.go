package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretsAreScrubbedWhereverTheySit(t *testing.T) {
	cases := []struct{ name, in, leak string }{
		{"top level", `{"dev_eui":"0011223344556677","app_key":"00112233445566778899aabbccddeeff"}`, "00112233445566778899aabbccddeeff"},
		{"nested in an object", `{"device":{"keys":{"nwk_s_key":"aabbccddeeff00112233445566778899"}}}`, "aabbccddeeff00112233445566778899"},
		{"inside a list", `[{"dev_eui":"A"},{"app_key":"deadbeefdeadbeef"}]`, "deadbeefdeadbeef"},
		{"mixed case key", `{"App_Key":"leakme"}`, "leakme"},
		{"a key that merely contains one", `{"session_key_v2":"leakme2"}`, "leakme2"},
		{"a non-string secret", `{"token":123456}`, "123456"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(Body([]byte(tc.in)))
			if strings.Contains(got, tc.leak) {
				t.Errorf("the value survived: %s", got)
			}
			if !strings.Contains(got, Marker) {
				t.Errorf("no marker was left behind, so a reader cannot tell this was "+
					"redacted rather than absent: %s", got)
			}
		})
	}
}

// TestHarmlessBodiesAreUntouched. Rewriting a response that had nothing to hide would
// make every tool result untrustworthy: a reader could no longer tell a redacted field
// from a field the network server never sent.
func TestHarmlessBodiesAreUntouched(t *testing.T) {
	for _, in := range []string{
		`{"dev_eui":"0011223344556677","name":"kitchen","enabled":true}`,
		`{"app_key":null}`, // legitimately absent: not a value, nothing to hide
		`{"app_key":""}`,   // empty for the same reason
		`{"gateway_id":"b827ebfffe123456"}`,
		`{"uplinks":[{"f_port":2,"decoded":{"temp":21.5}}]}`,
	} {
		if got := string(Body([]byte(in))); got != in {
			t.Errorf("Body(%s) = %s, want it unchanged", in, got)
		}
	}
}

// TestNonJSONIsReturnedUnchanged: a proxy's HTML error page is the only diagnostic
// information in a failed request, and a filter that "helpfully" rewrote it would
// destroy it.
func TestNonJSONIsReturnedUnchanged(t *testing.T) {
	for _, in := range []string{
		"",
		"<html><body>502 Bad Gateway</body></html>",
		"upstream token=abc123 refused",
		`{"truncated json`,
	} {
		if got := string(Body([]byte(in))); got != in {
			t.Errorf("Body(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestScrubbingKeepsTheDocumentValid(t *testing.T) {
	in := `{"devices":[{"dev_eui":"A","app_key":"secret"},{"dev_eui":"B"}]}`
	got := Body([]byte(in))
	var parsed any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("scrubbing produced invalid JSON: %v (%s)", err, got)
	}
	list, ok := parsed.(map[string]any)
	if !ok {
		t.Fatalf("shape changed: %s", got)
	}
	devices := list["devices"].([]any)
	if len(devices) != 2 {
		t.Fatalf("a member was dropped: %s", got)
	}
	first := devices[0].(map[string]any)
	if first["dev_eui"] != "A" {
		t.Errorf("an innocent sibling field was lost: %v", first)
	}
	if first["app_key"] != Marker {
		t.Errorf("app_key = %v, want the marker", first["app_key"])
	}
}

func TestCapListStopsALargeFleetAndSaysSo(t *testing.T) {
	var items []map[string]any
	for i := 0; i < 250; i++ {
		items = append(items, map[string]any{"dev_eui": "0011223344556677"})
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := CapList(raw, MaxListItems)
	var out []map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("the capped body is not valid JSON: %v", err)
	}
	if len(out) != MaxListItems+1 {
		t.Fatalf("got %d members, want %d items plus the notice", len(out), MaxListItems)
	}

	last := out[len(out)-1]
	if last["truncated"] != true {
		t.Errorf("the last member is not the notice: %v", last)
	}
	if last["total"] != float64(250) || last["shown"] != float64(MaxListItems) {
		t.Errorf("the counts are wrong: %v", last)
	}
	note, _ := last["note"].(string)
	if !strings.Contains(note, "250") {
		t.Errorf("the notice must state the real total, got %q", note)
	}
}

// TestCapListLeavesSmallAndNonArrayBodiesAlone. The wrapped shapes ({"events":[...]})
// are endpoints that already have a limit of their own; rewriting them here would
// invent a rule this server does not own.
func TestCapListLeavesSmallAndNonArrayBodiesAlone(t *testing.T) {
	for _, tc := range []struct {
		in string
		n  int
	}{
		{`[{"a":1},{"b":2}]`, 2},              // exactly at the cap: nothing to drop
		{`[{"a":1},{"b":2}]`, 200},            // far above it
		{`[]`, 200},                           // empty
		{`{"events":[{"kind":"uplink"}]}`, 1}, // not an array: a wrapped shape
		{`not json`, 1},
		{``, 1},
	} {
		if got := string(CapList([]byte(tc.in), tc.n)); got != tc.in {
			t.Errorf("CapList(%q, %d) = %q, want it unchanged", tc.in, tc.n, got)
		}
	}
}

// TestCapListTruncatesOnlyWhenThereIsSomethingToTruncate: the boundary, since an
// off-by-one here either drops a device nobody asked to lose or lets a huge list
// through unbounded.
func TestCapListTruncatesOnlyWhenThereIsSomethingToTruncate(t *testing.T) {
	two := []byte(`[{"dev_eui":"A"},{"dev_eui":"B"}]`)
	if got := string(CapList(two, 2)); got != string(two) {
		t.Errorf("at the cap it should be untouched, got %s", got)
	}
	got := string(CapList(two, 1))
	if got == string(two) {
		t.Fatal("over the cap it must truncate")
	}
	if !strings.Contains(got, `"total":2`) || !strings.Contains(got, `"shown":1`) {
		t.Errorf("the notice must report both counts: %s", got)
	}
	if strings.Contains(got, `"dev_eui":"B"`) {
		t.Errorf("the dropped item is still present: %s", got)
	}
}

// TestCapListNoticeIsDataNotProse: a caller iterating the array must meet the notice as
// a member, so it cannot be mistaken for a device.
func TestCapListNoticeIsDataNotProse(t *testing.T) {
	raw := []string{}
	for i := 0; i < 5; i++ {
		raw = append(raw, `{"dev_eui":"A"}`)
	}
	got := string(CapList([]byte("["+strings.Join(raw, ",")+"]"), 2))
	if !strings.Contains(got, `"truncated":true`) {
		t.Errorf("the notice is missing: %s", got)
	}
	// The notice must be a member of the array, not text appended after it.
	if strings.HasSuffix(got, "}") && !strings.HasSuffix(got, "}]") {
		t.Errorf("the notice was appended outside the array: %s", got)
	}
}
