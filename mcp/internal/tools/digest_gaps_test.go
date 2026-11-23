package tools

import (
	"strings"
	"testing"
)

// TestDigestReportsAnUnavailablePartRatherThanDroppingIt. This is the failure mode
// the whole design turns on: a section that silently disappears is indistinguishable
// from a section that legitimately has nothing in it, and the model will conclude
// "no events recorded" from "the events endpoint is down".
func TestDigestReportsAnUnavailablePartRatherThanDroppingIt(t *testing.T) {
	fake := healthyFake(t)
	delete(fake.responses, "/api/v1/events") // break one part only

	body, res := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if res.IsError {
		t.Fatalf("one unavailable part must not fail the whole digest: %s", body)
	}
	if !strings.Contains(body, "not available") {
		t.Errorf("the digest must name what it could not read:\n%s", body)
	}
	if !strings.Contains(body, "event history could not be read") {
		t.Errorf("the gap must be attributed to the events read:\n%s", body)
	}
	// Everything else still renders, so the caller is not left with nothing.
	for _, want := range []string{"Basement sensor", "f_cnt_up 41", "1 of 2 gateways"} {
		if !strings.Contains(body, want) {
			t.Errorf("losing the events read also lost %q:\n%s", want, body)
		}
	}
	// And it says what to do about the gap instead of leaving the absence to speak.
	if !strings.Contains(body, "A gap is not a zero") {
		t.Errorf("the digest should warn against reading a gap as a zero:\n%s", body)
	}
}

// TestDigestUnreadableStatusStillAnswersTheDeviceQuestion. The network read is the
// least important part of a per-device digest, so losing it must not cost the caller
// the device, the session or the events.
func TestDigestUnreadableStatusStillAnswersTheDeviceQuestion(t *testing.T) {
	fake := healthyFake(t)
	delete(fake.responses, "/status")

	body, res := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if res.IsError {
		t.Fatalf("losing the network read must not fail the digest: %s", body)
	}
	if !strings.Contains(body, "unknown") {
		t.Errorf("the network section should say it is unknown rather than vanish:\n%s", body)
	}
	for _, want := range []string{"Basement sensor", "f_cnt_up 41", "f_cnt_gap_too_large"} {
		if !strings.Contains(body, want) {
			t.Errorf("losing the network read also lost %q:\n%s", want, body)
		}
	}
}

// TestDigestDistinguishesNoCodecFromAFailedCodec: the distinction that hides a broken
// formatter behind an apparently undecoded fleet.
func TestDigestDistinguishesNoCodecFromAFailedCodec(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/uplinks"] = `{"count":2,"limit":5,"uplinks":[` +
		`{"received_at":"2026-11-21T09:00:00Z","f_port":2,"rssi":-90,"snr":3,"has_codec":false},` +
		`{"received_at":"2026-11-21T09:01:00Z","f_port":2,"rssi":-91,"snr":2,` +
		`"has_codec":true,"decode_error":"unexpected end of JSON input"}]}`

	body, _ := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if !strings.Contains(body, "no formatter configured") {
		t.Errorf("a device with no formatter should be labelled as one:\n%s", body)
	}
	if !strings.Contains(body, "DECODE FAILED: unexpected end of JSON input") {
		t.Errorf("a formatter that refused must be named as a failure:\n%s", body)
	}
}

// TestDigestEmptyHistorySaysNone rather than printing a bare heading. An explicit
// "none" is information; a missing section is indistinguishable from a bug.
func TestDigestEmptyHistorySaysNone(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/events"] = `{"count":0,"limit":8,"events":[]}`
	fake.responses["/api/v1/uplinks"] = `{"count":0,"limit":5,"uplinks":[]}`

	body, _ := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if !strings.Contains(body, "events (newest first, up to 8)\n  none recorded for this device") {
		t.Errorf("empty event history should be stated under its own heading:\n%s", body)
	}
	if !strings.Contains(body, "uplinks (newest first, up to 5)\n  none recorded") {
		t.Errorf("empty uplink history should be stated under its own heading:\n%s", body)
	}
}
