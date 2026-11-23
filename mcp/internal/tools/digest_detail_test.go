package tools

import (
	"strings"
	"testing"
)

// TestDigestDisabledDeviceSaysSo, because "the network refuses this device's frames
// on purpose" is a different diagnosis from "the network is broken", and enabled:false
// is otherwise a bare boolean.
func TestDigestDisabledDeviceSaysSo(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/devices/"+testEUI] =
		`{"dev_eui":"0011223344556677","name":"Switched off","enabled":false,"activation":"otaa"}`

	body, _ := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if !strings.Contains(body, "refuses this device's frames on purpose") {
		t.Errorf("enabled:false must be explained, not just printed:\n%s", body)
	}
}

// TestDigestNamesAMissingModel because a device with no model still joins and still
// stores frames as hex, so "no model" is not "no device" — but it does explain why
// nothing is decoded.
func TestDigestNamesAMissingModel(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/devices/"+testEUI] =
		`{"dev_eui":"0011223344556677","name":"Plain","enabled":true,"activation":"abp",` +
			`"dev_addr":"2600AA11"}`

	body, _ := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if !strings.Contains(body, "stored as hex, not decoded") {
		t.Errorf("an absent model should be explained:\n%s", body)
	}
	if !strings.Contains(body, "dev_addr:   2600AA11") {
		t.Errorf("an ABP device's DevAddr should be shown:\n%s", body)
	}
}

// TestDigestNamesAnUnnamedDevice rather than printing a blank after the label, since
// a blank line in a report reads as a rendering bug.
func TestDigestNamesAnUnnamedDevice(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/devices/"+testEUI] =
		`{"dev_eui":"0011223344556677","enabled":true,"activation":"otaa"}`

	body, _ := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if !strings.Contains(body, "(unnamed)") {
		t.Errorf("a device with no name should say so rather than print a blank:\n%s", body)
	}
}

// TestDigestQuotesTheDetailVerbatim. The detail is the evidence, and a digest that
// summarised it would be drawing the conclusion the caller was hired to draw.
func TestDigestQuotesTheDetailVerbatim(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/events"] = `{"count":1,"limit":8,"events":[` +
		`{"occurred_at":"2026-11-21T09:14:51Z","kind":"uplink_drop",` +
		`"reason":"duty_cycle_denied",` +
		`"detail":"eu868 subg_2: 0.9% of 1.0% used, free again in 41s"}]}`

	body, _ := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if !strings.Contains(body, "free again in 41s") {
		t.Errorf("the detail must survive intact — it is the only evidence there is:\n%s", body)
	}
	// Quoted as the recorded string, not paraphrased, so it can be handed straight to
	// explain_event_reason.
	if !strings.Contains(body, "[duty_cycle_denied]") {
		t.Errorf("the reason must be quoted verbatim:\n%s", body)
	}
}

// TestDigestPendingDownlinkIsMentioned, because "there is a frame waiting for its
// next window" changes what "the device is not responding" means.
func TestDigestPendingDownlinkIsMentioned(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/devices/"+testEUI+"/session"] =
		`{"dev_addr":"26011234","region":"EU868","f_cnt_up":9,"data_rate":3,"adr":true,` +
			`"last_rssi":-88,"last_snr":5,"pending_downlink":{"f_port":10,"f_cnt":3}}`

	body, _ := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if !strings.Contains(body, "downlink is queued for its next RX window") {
		t.Errorf("a pending frame should be called out:\n%s", body)
	}
}

// TestDigestNeverUplinkedDistinguishesNeverFromGone. "never in this session" and a
// zero timestamp are the same bytes in JSON and completely different facts; a zero
// rendered as a date reads as a device that stopped reporting in the year 1.
func TestDigestNeverUplinkedDistinguishesNeverFromGone(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/devices/"+testEUI+"/session"] =
		`{"dev_addr":"26011234","region":"EU868","f_cnt_up":0,"data_rate":0,"adr":false,` +
			`"last_rssi":0,"last_snr":0,"last_uplink_at":"0001-01-01T00:00:00Z"}`

	body, _ := callText(t, newTestServer(t, fake), "diagnose_device",
		map[string]any{"dev_eui": testEUI})

	if !strings.Contains(body, "never in this session") {
		t.Errorf("a zero timestamp must be named as never, not as a date:\n%s", body)
	}
	if strings.Contains(body, "0001-01-01") {
		t.Errorf("a zero timestamp leaked through as a date:\n%s", body)
	}
}
