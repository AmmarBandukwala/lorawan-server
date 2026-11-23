package resources

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/mcp/internal/redact"
	"github.com/fieldnode/lorawan-server/mcp/internal/vocabulary"
)

// TestDeviceInventoryIsCappedThroughTheResourcePath: the ns has no paging, so the cap is
// the only thing between a large fleet and the whole conversation. It has to hold at
// both doors.
func TestDeviceInventoryIsCappedThroughTheResourcePath(t *testing.T) {
	fake := healthyFake()
	items := make([]map[string]any, 0, redact.MaxListItems+25)
	for i := 0; i < redact.MaxListItems+25; i++ {
		items = append(items, map[string]any{"dev_eui": testEUI})
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	fake.responses["/api/v1/devices"] = string(raw)

	body, err := read(t, newSession(t, fake), URIDevices)
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if !strings.Contains(body, `"truncated":true`) {
		t.Errorf("an over-cap inventory came back uncapped (%d bytes)", len(body))
	}
	if !strings.Contains(body, `"total":225`) {
		t.Errorf("the notice must report the real total:\n%s", tail(body, 300))
	}
}

// TestVocabularyResourceCoversEveryReason, and is served without touching the network
// server. The ns does not own this commentary, so asking it would be asking the wrong
// process to explain a file it does not hold.
func TestVocabularyResourceCoversEveryReason(t *testing.T) {
	fake := healthyFake()
	body, err := read(t, newSession(t, fake), URIVocabulary)
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	for _, d := range vocabulary.All() {
		if !strings.Contains(body, d.Reason) {
			t.Errorf("reason %q is missing from the vocabulary resource", d.Reason)
		}
		if !strings.Contains(body, d.Summary) {
			t.Errorf("reason %q lost its summary", d.Reason)
		}
	}
	if fake.requests["/status"] != 0 || fake.requests["/api/v1/devices"] != 0 {
		t.Errorf("reading the vocabulary hit the network server: %v", fake.requests)
	}
}

// TestVocabularyResourceIsDeterministic: a pinned resource is read repeatedly, and a
// table that reorders itself between reads defeats a client's prompt cache and makes two
// turns of a conversation impossible to compare.
func TestVocabularyResourceIsDeterministic(t *testing.T) {
	cs := newSession(t, healthyFake())
	first, err := read(t, cs, URIVocabulary)
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := read(t, cs, URIVocabulary)
		if err != nil {
			t.Fatalf("ReadResource: %v", err)
		}
		if again != first {
			t.Fatal("two reads of the vocabulary differ")
		}
	}
}

func TestDeviceResourceReadsOneDevice(t *testing.T) {
	fake := healthyFake()
	body, err := read(t, newSession(t, fake), "fieldnode://devices/"+testEUI)
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if !strings.Contains(body, `"name":"kitchen"`) {
		t.Errorf("body = %s", body)
	}
	if fake.requests["/api/v1/devices/"+testEUI] != 1 {
		t.Errorf("the device endpoint was called %d times",
			fake.requests["/api/v1/devices/"+testEUI])
	}
}

// TestSessionTemplateDoesNotReadTheDevice: two templates share a prefix, and a handler
// that parsed the URI loosely would fetch the device and report it as a session — a
// wrong answer that looks entirely plausible.
func TestSessionTemplateDoesNotReadTheDevice(t *testing.T) {
	fake := healthyFake()
	body, err := read(t, newSession(t, fake), "fieldnode://devices/"+testEUI+"/session")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if !strings.Contains(body, "data_rate") {
		t.Errorf("the session body was not returned: %s", body)
	}
	if strings.Contains(body, "kitchen") {
		t.Errorf("the device record was returned for a session URI: %s", body)
	}
	if fake.requests["/api/v1/devices/"+testEUI] != 0 {
		t.Error("a session read fetched the device record")
	}
}

// TestUnknownDeviceIsRefusedNotEmptied. An empty body would read as "this device has
// nothing recorded", which is a different claim from "this device is not here", and the
// difference is the whole answer to "why is it not joining".
func TestUnknownDeviceIsRefusedNotEmptied(t *testing.T) {
	fake := healthyFake()
	fake.statuses["/api/v1/devices/AABBCCDDEEFF0011"] = 404
	fake.responses["/api/v1/devices/AABBCCDDEEFF0011"] =
		`{"error":"dev_eui AABBCCDDEEFF0011 is not provisioned"}`

	_, err := read(t, newSession(t, fake), "fieldnode://devices/AABBCCDDEEFF0011")
	if err == nil {
		t.Fatal("an unprovisioned device was answered without an error")
	}
	if !strings.Contains(err.Error(), "not provisioned") {
		t.Errorf("the ns's own sentence should come back, got %q", err)
	}
}

// TestMalformedDevEUIIsRefusedWithoutARequest. The width rule means no path through this
// package ever sends caller-supplied text to the network server, so a crafted URI cannot
// be used to make this process issue requests on a caller's behalf.
func TestMalformedDevEUIIsRefusedWithoutARequest(t *testing.T) {
	for _, uri := range []string{
		"fieldnode://devices/zz",
		"fieldnode://devices/001122334455667",   // one digit short
		"fieldnode://devices/00112233445566778", // one digit long
		"fieldnode://devices/",                  // empty variable
		"fieldnode://devices/001122334455667g",  // not hex
		"fieldnode://devices/%2e%2e/status",     // encoded traversal
	} {
		fake := healthyFake()
		if _, err := read(t, newSession(t, fake), uri); err == nil {
			t.Errorf("%s was answered without an error", uri)
			continue
		}
		for path, n := range fake.requests {
			if n != 0 {
				t.Errorf("%s caused a request to %s", uri, path)
			}
		}
	}
}

// TestForeignURIIsRefused: a client may ask for any URI. Answering one this server never
// advertised with the status body would be a confused deputy in miniature.
func TestForeignURIIsRefused(t *testing.T) {
	for _, uri := range []string{"file:///etc/passwd", "http://example.com/", "fieldnode://nope"} {
		fake := healthyFake()
		if _, err := read(t, newSession(t, fake), uri); err == nil {
			t.Errorf("%s was answered", uri)
			continue
		}
		for path, n := range fake.requests {
			if n != 0 {
				t.Errorf("%s caused a request to %s", uri, path)
			}
		}
	}
}
