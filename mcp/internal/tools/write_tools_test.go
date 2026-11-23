package tools

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The write tests. The one that matters most is TestPatchSendsOnlyTheFieldsSupplied:
// it is the difference between a tool that renames a device and one that renames a
// device and silently takes it off the network.

func TestWriteToolsAreAbsentFromTheReadOnlyBuild(t *testing.T) {
	advertised := strings.Join(advertisedNames(t, newTestServer(t, healthyFake(t))), ",")
	for _, name := range WriteTools(ProfileOperate) {
		if strings.Contains(advertised, name) {
			t.Errorf("%s is advertised under the read profile", name)
		}
	}
	if got := WriteTools(ProfileRead); got != nil {
		t.Errorf("WriteTools(read) = %v, want none", got)
	}
}

func TestWriteToolsAreAdvertisedWithHonestAnnotations(t *testing.T) {
	cs := newOperateServer(t, healthyFake(t))
	got, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	for _, tool := range got.Tools {
		if !slices.Contains(WriteTools(ProfileOperate), tool.Name) {
			continue
		}
		if tool.Annotations == nil {
			t.Errorf("%s: no annotations", tool.Name)
			continue
		}
		if tool.Annotations.ReadOnlyHint {
			t.Errorf("%s: marked read-only, but it writes", tool.Name)
		}
		// A write reaches a radio and a physical device. Claiming otherwise is how a
		// host ends up auto-approving something it should have asked about.
		if tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
			t.Errorf("%s: OpenWorldHint should be true for a tool that transmits", tool.Name)
		}
		if len(tool.Description) < 200 {
			t.Errorf("%s: description is %d characters; a write has to carry its queue "+
				"semantics or the model will retry a 202", tool.Name, len(tool.Description))
		}
	}
}

// TestPatchSendsOnlyTheFieldsSupplied is the load-bearing test of this profile.
//
// The network server applies every field present in the PATCH body. If this server
// serialised a zero-valued bool for a field the caller never mentioned, a call that
// meant to rename a device would also send enabled:false and take it off the network —
// and would report success while doing it.
func TestPatchSendsOnlyTheFieldsSupplied(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/devices/"+testEUI] =
		`{"dev_eui":"0011223344556677","name":"Renamed","enabled":true,"activation":"otaa"}`

	_, res := callText(t, newOperateServer(t, fake), "patch_device", map[string]any{
		"dev_eui": testEUI, "name": "Renamed", "reason": "the owner asked for a clearer label",
	})
	if res.IsError {
		t.Fatalf("patch_device failed: %v", fake.bodies["/api/v1/devices/"+testEUI])
	}

	sent := fake.bodies["/api/v1/devices/"+testEUI]
	if len(sent) != 1 {
		t.Fatalf("the device endpoint saw %d bodies, want 1", len(sent))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(sent[0]), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}

	if _, present := body["enabled"]; present {
		t.Errorf("enabled was sent although the caller never mentioned it: %s\n"+
			"a zero value here silently takes a live device off the network", sent[0])
	}
	for _, must := range []string{"disable_adr", "supports_class_c", "max_data_rate",
		"min_data_rate", "max_tx_power", "attributes", "location", "model_id", "group"} {
		if _, present := body[must]; present {
			t.Errorf("%s was sent although the caller never mentioned it: %s", must, sent[0])
		}
	}
	if body["name"] != "Renamed" {
		t.Errorf("name = %v, want Renamed", body["name"])
	}
	// The reason is this server's audit field, not part of the network server's body.
	if _, present := body["reason"]; present {
		t.Errorf("reason leaked into the upstream body: %s", sent[0])
	}
}

// TestPatchNullLocationClearsAndOmissionLeaves: the network server tells these apart,
// and a typed field would collapse them into the same nil.
func TestPatchNullLocationClearsAndOmissionLeaves(t *testing.T) {
	path := "/api/v1/devices/" + testEUI

	clearCase := healthyFake(t)
	_, _ = callText(t, newOperateServer(t, clearCase), "patch_device", map[string]any{
		"dev_eui": testEUI, "reason": "the sensor moved indoors",
		"location": json.RawMessage("null"),
	})
	sent := clearCase.bodies[path]
	if len(sent) != 1 || !strings.Contains(sent[0], `"location":null`) {
		t.Errorf("an explicit null location should go upstream as null, got %v", sent)
	}

	leaveCase := healthyFake(t)
	_, _ = callText(t, newOperateServer(t, leaveCase), "patch_device", map[string]any{
		"dev_eui": testEUI, "reason": "just a rename", "name": "Still here",
	})
	sent = leaveCase.bodies[path]
	if len(sent) != 1 || strings.Contains(sent[0], "location") {
		t.Errorf("an omitted location must not appear at all, got %v", sent)
	}
}

// TestPatchEmptyMapClearsAttributes is the other half of the same distinction: an
// empty attributes object means "no attributes", which is not the same as silence.
func TestPatchEmptyMapClearsAttributes(t *testing.T) {
	fake := healthyFake(t)
	_, _ = callText(t, newOperateServer(t, fake), "patch_device", map[string]any{
		"dev_eui": testEUI, "reason": "drop stale tags", "attributes": map[string]any{},
	})
	sent := fake.bodies["/api/v1/devices/"+testEUI]
	if len(sent) != 1 || !strings.Contains(sent[0], `"attributes":{}`) {
		t.Errorf("an empty attributes map should go upstream as an empty object, got %v", sent)
	}
}

func TestPatchRefusesAnEmptyPatch(t *testing.T) {
	fake := healthyFake(t)
	body, res := callText(t, newOperateServer(t, fake), "patch_device", map[string]any{
		"dev_eui": testEUI, "reason": "nothing in particular",
	})
	if !res.IsError {
		t.Fatalf("an empty patch should be refused, got: %s", body)
	}
	if !strings.Contains(body, "no fields were supplied") {
		t.Errorf("message = %q", body)
	}
	if fake.requests["/api/v1/devices/"+testEUI] != 0 {
		t.Error("an empty patch still reached the network server")
	}
}

// TestDisablingADeviceSaysWhatItMeans: a successful PATCH otherwise reads identically
// whether it renamed a device or silenced one, and the second is the one an owner feels.
func TestDisablingADeviceSaysWhatItMeans(t *testing.T) {
	fake := healthyFake(t)
	fake.responses["/api/v1/devices/"+testEUI] =
		`{"dev_eui":"0011223344556677","name":"Off","enabled":false,"activation":"otaa"}`

	body, res := callText(t, newOperateServer(t, fake), "patch_device", map[string]any{
		"dev_eui": testEUI, "enabled": false, "reason": "decommissioning this sensor",
	})
	if res.IsError {
		t.Fatalf("the patch failed: %s", body)
	}
	for _, want := range []string{"refuses its frames", "broken device"} {
		if !strings.Contains(body, want) {
			t.Errorf("disabling a device should state the consequence; %q does not mention %q",
				body, want)
		}
	}
}

func TestRenamingADeviceDoesNotPreachAboutDisabling(t *testing.T) {
	body, _ := callText(t, newOperateServer(t, healthyFake(t)), "patch_device", map[string]any{
		"dev_eui": testEUI, "name": "New label", "reason": "a clearer name",
	})
	if strings.Contains(body, "refuses its frames") {
		t.Errorf("a rename should not arrive with a warning about disabling:\n%s", body)
	}
}

func TestDownlinkPayloadRules(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"odd number of hex digits", "010", "odd number"},
		{"separators left in", "01:02:03", "not a hex string"},
		{"over the cap", strings.Repeat("ab", 52), "limit is 51"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := healthyFake(t)
			body, res := callText(t, newOperateServer(t, fake), "enqueue_downlink", map[string]any{
				"dev_eui": testEUI, "data": tc.data, "f_port": 10, "reason": "a stated purpose",
			})
			if !res.IsError {
				t.Fatalf("%q was accepted: %s", tc.data, body)
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("message = %q, want it to mention %q", body, tc.want)
			}
			// The point of checking locally is that the network server is not asked.
			if fake.requests["/api/v1/devices/"+testEUI+"/downlink"] != 0 {
				t.Errorf("%q reached the network server", tc.data)
			}
		})
	}
}

func TestDownlinkPortRules(t *testing.T) {
	for _, port := range []int{0, 224, 1000} {
		fake := healthyFake(t)
		body, res := callText(t, newOperateServer(t, fake), "enqueue_downlink", map[string]any{
			"dev_eui": testEUI, "data": "0102", "f_port": port, "reason": "a stated purpose",
		})
		if !res.IsError {
			t.Errorf("f_port %d was accepted: %s", port, body)
			continue
		}
		if !strings.Contains(body, "1..223") {
			t.Errorf("f_port %d: message = %q, want it to name the range", port, body)
		}
		if fake.requests["/api/v1/devices/"+testEUI+"/downlink"] != 0 {
			t.Errorf("f_port %d reached the network server", port)
		}
	}
}

// TestDownlinkCarriesTheQueuedNotSentExplanation: the network server adds an
// explanation field to its 202 precisely because callers read 202 as failure. Passing
// it through is what stops a retry loop.
func TestDownlinkCarriesTheQueuedNotSentExplanation(t *testing.T) {
	body, res := callText(t, newOperateServer(t, healthyFake(t)), "enqueue_downlink", map[string]any{
		"dev_eui": testEUI, "data": "0102", "f_port": 10, "reason": "close the valve",
	})
	if res.IsError {
		t.Fatalf("the call failed: %s", body)
	}
	if !strings.Contains(body, "next RX window") {
		t.Errorf("the upstream explanation should reach the caller verbatim, got:\n%s", body)
	}
}

func TestDownlinkSendsCanonicalHexAndPort(t *testing.T) {
	fake := healthyFake(t)
	_, _ = callText(t, newOperateServer(t, fake), "enqueue_downlink", map[string]any{
		"dev_eui": testEUI, "data": " 0xdeadbeef ", "f_port": 7, "confirmed": true,
		"reason": "reboot the meter",
	})
	sent := fake.bodies["/api/v1/devices/"+testEUI+"/downlink"]
	if len(sent) != 1 {
		t.Fatalf("the downlink endpoint saw %d bodies, want 1", len(sent))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(sent[0]), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["data"] != "DEADBEEF" {
		t.Errorf("data = %v, want canonical upper-case hex with the prefix stripped", body["data"])
	}
	if body["f_port"] != float64(7) || body["confirmed"] != true {
		t.Errorf("body = %s", sent[0])
	}
	if _, present := body["reason"]; present {
		t.Errorf("reason leaked into the upstream body: %s", sent[0])
	}
}

// TestConflictCarriesTheUpstreamsSentence: the 409 text is the only place that
// explains the one-frame-per-device rule, so replacing it with "conflict" would leave
// the model to invent a reason and retry.
func TestConflictCarriesTheUpstreamsSentence(t *testing.T) {
	fake := healthyFake(t)
	path := "/api/v1/devices/" + testEUI + "/downlink"
	fake.statuses[path] = http.StatusConflict
	fake.responses[path] = bodyConflict

	body, res := callText(t, newOperateServer(t, fake), "enqueue_downlink", map[string]any{
		"dev_eui": testEUI, "data": "0102", "f_port": 10, "reason": "a second attempt",
	})
	if !res.IsError {
		t.Fatal("a 409 must be reported as a tool error")
	}
	for _, want := range []string{"already has a downlink waiting", "DELETE the pending frame"} {
		if !strings.Contains(body, want) {
			t.Errorf("the 409 should carry the network server's own guidance; %q lacks %q",
				body, want)
		}
	}
}

// TestDownlinkToADeviceWithNoSessionSaysWhy: the engine's own sentence names the
// DevEUI and says what is missing, which is safe to show and much more useful than a
// bare "not found".
func TestDownlinkToADeviceWithNoSessionSaysWhy(t *testing.T) {
	body, res := callText(t, newOperateServer(t, healthyFake(t)), "enqueue_downlink", map[string]any{
		"dev_eui": "AABBCCDDEEFF0011", "data": "0102", "f_port": 10, "reason": "a stated purpose",
	})
	if !res.IsError {
		t.Fatal("want an error for a device with no session")
	}
	for _, want := range []string{"AABBCCDDEEFF0011", "must join first"} {
		if !strings.Contains(body, want) {
			t.Errorf("message = %q, want it to carry %q from the network server", body, want)
		}
	}
}

// TestEveryWriteDemandsAReason: the reason is what a human reads in the approval
// prompt and what the audit log keeps. A write that proceeds without one is the hole
// this profile was designed not to have.
//
// Arguments are per tool because the SDK's schema validation rejects unknown
// properties, and that is the right behaviour — it just means each case has to name
// the arguments that tool actually takes.
func TestEveryWriteDemandsAReason(t *testing.T) {
	cases := map[string]map[string]any{
		"enqueue_downlink":      {"dev_eui": testEUI, "data": "0102", "f_port": 10},
		"cancel_downlink":       {"dev_eui": testEUI},
		"request_device_status": {"dev_eui": testEUI},
		"patch_device":          {"dev_eui": testEUI, "name": "x"},
	}
	if len(cases) != len(WriteTools(ProfileOperate)) {
		t.Fatalf("the cases cover %d tools but the operate profile has %d; "+
			"a new write would silently escape this check",
			len(cases), len(WriteTools(ProfileOperate)))
	}

	for name, args := range cases {
		fake := healthyFake(t)
		body, res := callText(t, newOperateServer(t, fake), name, args)
		if !res.IsError {
			t.Errorf("%s: a write with no reason was accepted: %s", name, body)
			continue
		}
		if !strings.Contains(strings.ToLower(body), "reason") {
			t.Errorf("%s: message = %q, want it to name the missing argument", name, body)
		}
		// Nothing reaches the network server when the reason is missing.
		for path, n := range fake.requests {
			if n != 0 && !strings.HasSuffix(path, "/status") {
				t.Errorf("%s reached the network server at %s without a reason", name, path)
			}
		}
	}
}
