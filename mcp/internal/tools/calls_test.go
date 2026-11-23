package tools

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestInstructionsReachTheModelBeforeAnyToolCall: the initialize result is the only
// place that can tell an agent "this is a live network and nothing here happens on
// demand" before it has already made a mistake.
func TestInstructionsReachTheModelBeforeAnyToolCall(t *testing.T) {
	init := newTestServer(t, healthyFake(t)).InitializeResult()
	if init == nil {
		t.Fatal("no initialize result on the session")
	}
	if !strings.Contains(init.Instructions, "live LoRaWAN network") {
		t.Errorf("Instructions = %q, want it to say what kind of system this is",
			init.Instructions)
	}
	// The three things that most often go wrong on a live network, none of which an
	// agent can infer from a tool description alone.
	for _, want := range []string{"class-A", "explain_event_reason", "has_codec"} {
		if !strings.Contains(init.Instructions, want) {
			t.Errorf("Instructions does not mention %q", want)
		}
	}
	if init.ServerInfo == nil || init.ServerInfo.Name == "" {
		t.Error("the server does not identify itself")
	}
}

// TestProfileRejectsAnUnimplementedName rather than falling back to read. An
// operator who asked for a write profile and got a read-only server would otherwise
// conclude the writes were merely broken.
//
// "admin" is refused too, and deliberately: there is no admin-only tool in this build,
// because the only candidate was the console's facility settings endpoint, which is a
// different upstream with no authentication at all. Accepting the name would advertise
// a tier that does not exist.
func TestProfileRejectsAnUnimplementedName(t *testing.T) {
	for _, raw := range []string{"admin", "write", "root", "writes"} {
		if _, err := ParseProfile(raw); err == nil {
			t.Errorf("ParseProfile(%q) accepted it", raw)
		}
	}
	// The admin rejection must say why, not just no: an operator who set admin from
	// this project's own plan deserves to learn the plan changed.
	_, err := ParseProfile("admin")
	if err == nil || !strings.Contains(err.Error(), "no admin tier") {
		t.Errorf("the admin rejection should explain itself, got %v", err)
	}

	// Empty and "read" are the same thing; case and padding are tolerated so a shell
	// default of "Read" is not a startup failure.
	for _, raw := range []string{"", "read", "  read  ", "READ"} {
		if p, err := ParseProfile(raw); err != nil || p != ProfileRead {
			t.Errorf("ParseProfile(%q) = %q, %v; want read, nil", raw, p, err)
		}
	}
	for _, raw := range []string{"operate", "OPERATE", " operate "} {
		if p, err := ParseProfile(raw); err != nil || p != ProfileOperate {
			t.Errorf("ParseProfile(%q) = %q, %v; want operate, nil", raw, p, err)
		}
	}
}

// TestProfilesAreMonotonic: operate is read plus writes, never read instead of
// something. Raising a profile must not quietly take a capability away.
func TestProfilesAreMonotonic(t *testing.T) {
	readOnly := advertisedNames(t, newTestServer(t, healthyFake(t)))

	fake := healthyFake(t)
	operate := advertisedNames(t, newOperateServer(t, fake))

	var missing []string
	for _, name := range readOnly {
		if !slices.Contains(operate, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("the operate profile lost %d read tool(s) the read profile has: %s",
			len(missing), strings.Join(missing, ", "))
	}
	if len(operate) != len(readOnly)+len(WriteTools(ProfileOperate)) {
		t.Errorf("operate advertises %d tools, read advertises %d and writes are %d",
			len(operate), len(readOnly), len(WriteTools(ProfileOperate)))
	}
}

func TestRegisterRefusesWithoutAClient(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	if _, err := Register(server, nil, ProfileRead); err == nil {
		t.Error("Register accepted a nil Deps")
	}
	if _, err := Register(server, &Deps{}, ProfileRead); err == nil {
		t.Error("Register accepted Deps with no network server client")
	}
}

// TestReadToolsReturnTheNetworkServersOwnBytes: verbatim, not re-modelled. A field
// the ns adds must still be there, which is the whole reason these tools return text
// rather than a struct.
func TestReadToolsReturnTheNetworkServersOwnBytes(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"network_status", map[string]any{}, `"net_id":"010000"`},
		{"list_devices", map[string]any{}, `"dev_eui":"0011223344556677"`},
		{"get_device", map[string]any{"dev_eui": testEUI}, `"group":"cold-chain"`},
		{"get_device_session", map[string]any{"dev_eui": testEUI}, `"f_cnt_up":41`},
		{"list_uplinks", map[string]any{}, `"has_codec":true`},
		{"list_events", map[string]any{}, `"kind":"join_accept"`},
		{"list_models", map[string]any{}, `"model_id":"sensecap-c01"`},
		{"get_model", map[string]any{"model_id": "sensecap-c01"}, `"codec":"javascript"`},
		{"list_integrations", map[string]any{}, `"kind":"mqttls"`},
		{"get_integration", map[string]any{"id": "ops"}, `"id":"ops"`},
	}

	cs := newTestServer(t, healthyFake(t))
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			body, res := callText(t, cs, tc.tool, tc.args)
			if res.IsError {
				t.Fatalf("%s reported an error: %s", tc.tool, body)
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("%s returned %s, want it to contain %s", tc.tool, body, tc.want)
			}
			if !json.Valid([]byte(body)) {
				t.Errorf("%s returned something that is not JSON: %s", tc.tool, body)
			}
		})
	}
}

// TestUnreachableNetworkServerIsReportedNotSwallowed: with the ns returning 503,
// every read must fail loudly. A tool that returned empty output here would read to
// a model as "the fleet is empty", which is the most damaging wrong answer available.
func TestUnreachableNetworkServerIsReportedNotSwallowed(t *testing.T) {
	fake := healthyFake(t)
	fake.status = http.StatusServiceUnavailable
	cs := newTestServer(t, fake)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"network_status", map[string]any{}},
		{"list_devices", map[string]any{}},
		{"get_device", map[string]any{"dev_eui": testEUI}},
		{"list_events", map[string]any{}},
	} {
		body, res := callText(t, cs, tc.tool, tc.args)
		if !res.IsError {
			t.Errorf("%s reported success while the network server was returning 503: %s",
				tc.tool, body)
		}
		if !strings.Contains(body, "simulated failure") {
			t.Errorf("%s lost the upstream reason: %q", tc.tool, body)
		}
	}
}
