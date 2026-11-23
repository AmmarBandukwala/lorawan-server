package tools

import (
	"strings"
	"testing"
)

// The digest is the one tool that composes, and therefore the one that can lie by
// omission. These tests are about what it says when a part is missing, not about
// whether it renders at all.

func TestDigestAssemblesEveryPart(t *testing.T) {
	body, res := callText(t, newTestServer(t, healthyFake(t)),
		"diagnose_device", map[string]any{"dev_eui": testEUI})
	if res.IsError {
		t.Fatalf("diagnose_device failed: %s", body)
	}

	for _, want := range []string{"configuration", "session", "events", "uplinks", "network"} {
		if !strings.Contains(body, want) {
			t.Errorf("the digest has no %q section:\n%s", want, body)
		}
	}

	for _, want := range []string{
		"Basement sensor",        // device name, from the device read
		"cold-chain",             // group
		"sensecap-c01",           // model
		"f_cnt_up 41",            // session counter
		"data rate 5",            // session data rate
		"2026-11-21T09:15:04Z",   // last uplink time
		"f_cnt_gap_too_large",    // the refusal, quoted with its reason
		"f_cnt 41 is 9000 ahead", // and its detail, which is the actual evidence
		"1 uplink_drop",          // the tally
		"DECODE FAILED",          // the second uplink's formatter failure
		"1 of 2 gateways online", // network health
		"eu868:subg_2=3",         // duty-cycle denials
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the digest omits %q:\n%s", want, body)
		}
	}

	// A complete read must not advertise gaps.
	if strings.Contains(body, "not available") {
		t.Errorf("a healthy network reported gaps:\n%s", body)
	}
}

// TestDigestBattery255IsNotAReading: the specification reserves 255 for "this device
// cannot report a level". Printed as a fraction it reads as a full battery, which is
// the opposite of true often enough to matter.
func TestDigestBattery255IsNotAReading(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/devices/"+testEUI+"/session"] =
		`{"dev_addr":"26011234","region":"EU868","f_cnt_up":4,"data_rate":2,"adr":false,` +
			`"last_rssi":-90,"last_snr":3,"battery":255,"margin_db":12}`

	body, _ := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if strings.Contains(body, "255/255") {
		t.Errorf("battery 255 was rendered as a level:\n%s", body)
	}
	if !strings.Contains(body, "cannot report") {
		t.Errorf("battery 255 should be called out as unreportable:\n%s", body)
	}
	if !strings.Contains(body, "link margin: 12 dB") {
		t.Errorf("the link margin should still be reported:\n%s", body)
	}
}

// TestDigestNoSessionIsAFindingNotAGap. "There is no session" and "I could not read
// the session" are different facts leading to different next actions; conflating them
// is how an unjoined device gets reported as an unreachable one.
func TestDigestNoSessionIsAFindingNotAGap(t *testing.T) {
	fake := healthyFake(t)
	delete(fake.responses, "/api/v1/devices/"+testEUI+"/session") // the fake answers 404

	body, res := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if res.IsError {
		t.Fatalf("a device with no session is an answer, not a failure: %s", body)
	}
	if !strings.Contains(body, "no session on this network") {
		t.Errorf("the digest should say there is no session:\n%s", body)
	}
	if strings.Contains(body, "session could not be read") {
		t.Errorf("a 404 session must not be reported as an unreadable one:\n%s", body)
	}
	if !strings.Contains(body, "join_request") {
		t.Errorf("the no-session line should point at the join events to look at:\n%s", body)
	}
}

// TestDigestOfAnUnprovisionedDeviceIsAnAnswerNotAnError, and a useful one: it is the
// fastest possible response to "why is this device not joining", and it must not come
// back as a failure the model will retry.
func TestDigestOfAnUnprovisionedDeviceIsAnAnswerNotAnError(t *testing.T) {
	body, res := callText(t, newTestServer(t, healthyFake(t)), "diagnose_device",
		map[string]any{"dev_eui": "AABBCCDDEEFF0011"})

	if res.IsError {
		t.Fatalf("an unprovisioned device is an answer, not a tool failure: %s", body)
	}
	if !strings.Contains(body, "not provisioned on this network") {
		t.Errorf("digest = %q", body)
	}
	// It must say why there is nothing else, or the caller reads silence as data loss.
	if !strings.Contains(body, "cannot have a session") {
		t.Errorf("the answer should explain that nothing else can exist: %s", body)
	}
}

// TestDigestIsDeterministic so two turns of one conversation can be compared rather
// than re-read. Map iteration order is the usual way this breaks.
func TestDigestIsDeterministic(t *testing.T) {
	var first string
	for range 6 {
		body, _ := callText(t, newTestServer(t, healthyFake(t)), "diagnose_device",
			map[string]any{"dev_eui": testEUI})
		if first == "" {
			first = body
			continue
		}
		if body != first {
			t.Fatalf("the digest changed between identical calls:\n--first--\n%s\n--second--\n%s",
				first, body)
		}
	}
}
