package tools

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/mcp/internal/prompts"
	"github.com/fieldnode/lorawan-server/mcp/internal/resources"
)

// TestResourcesAndPromptsAreAdvertisedThroughNew is about the wiring, not the
// features.
//
// The resources and prompts packages each test themselves by building their own
// mcp.Server and registering on it, which passes whether or not anything in
// production ever calls Register. This asserts against the server main actually
// builds, so a registration dropped from New is a failure rather than a server that
// quietly offers less than its README claims.
func TestResourcesAndPromptsAreAdvertisedThroughNew(t *testing.T) {
	cs := newTestServer(t, healthyFake(t))

	listed, err := cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(listed.Resources) == 0 {
		t.Fatal("the server built by New advertises no resources")
	}
	uris := make([]string, 0, len(listed.Resources))
	for _, r := range listed.Resources {
		uris = append(uris, r.URI)
	}
	for _, want := range []string{resources.URIStatus, resources.URIDevices, resources.URIVocabulary} {
		if !slices.Contains(uris, want) {
			t.Errorf("%s is not advertised; got %v", want, uris)
		}
	}

	got, err := cs.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(got.Prompts) == 0 {
		t.Fatal("the server built by New advertises no prompts")
	}
	names := make([]string, 0, len(got.Prompts))
	for _, p := range got.Prompts {
		names = append(names, p.Name)
	}
	for _, want := range []string{prompts.DiagnoseDevice, prompts.WhyNoDownlink, prompts.FleetHealth} {
		if !slices.Contains(names, want) {
			t.Errorf("prompt %s is not advertised; got %v", want, names)
		}
	}
}

// TestTemplatedResourcesAreDiscoverable: a client cannot read what it cannot find.
//
// The per-device URIs are templates, so they are not in resources/list — they are in
// resources/templates/list, a separate call a host has to make. A registration that
// lands in the wrong list therefore looks fine in a test that only calls
// ListResources and is invisible to a host that never learns the URI exists.
func TestTemplatedResourcesAreDiscoverable(t *testing.T) {
	cs := newTestServer(t, healthyFake(t))

	templates, err := cs.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	names := make([]string, 0, len(templates.ResourceTemplates))
	for _, tmpl := range templates.ResourceTemplates {
		names = append(names, string(tmpl.URITemplate))
	}
	for _, want := range []string{resources.URIDeviceTmpl, resources.URISessionTmpl} {
		if !slices.Contains(names, want) {
			t.Errorf("%s is not advertised as a template; got %v", want, names)
		}
	}

	// And not in the plain list, where a client would try to read the literal
	// string "fieldnode://devices/{dev_eui}" as a DevEUI.
	listed, err := cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	for _, r := range listed.Resources {
		if strings.Contains(r.URI, "{") {
			t.Errorf("%s is advertised as a readable resource; a template belongs in "+
				"templates/list, and a client would read the braces as a DevEUI", r.URI)
		}
	}
}

// TestResourcesAreAdvertisedUnderEveryProfile: a resource is a read, so raising the
// profile must not remove one. The same monotonicity the profiles are held to for
// tools.
func TestResourcesAreAdvertisedUnderEveryProfile(t *testing.T) {
	readOnly, err := newTestServer(t, healthyFake(t)).
		ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	operate, err := newOperateServer(t, healthyFake(t)).
		ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(operate.Resources) != len(readOnly.Resources) {
		t.Errorf("the operate profile advertises %d resources, read advertises %d",
			len(operate.Resources), len(readOnly.Resources))
	}
}

// TestInstructionsSayWhatTheProfileAllows: the orientation is the only text a model
// reads before it has called anything, so it must not contradict the tool list.
//
// The read-only wording used to be unconditional. Once writes existed, a model told
// "every tool here is a read" would not look for the tools that can transmit — and
// the sentence would be false in the deployment where being wrong costs most.
func TestInstructionsSayWhatTheProfileAllows(t *testing.T) {
	read := initializeInstructions(t, ProfileRead)
	if !strings.Contains(read, "Every tool here is a read") {
		t.Error("the read profile should say plainly that it cannot write")
	}
	if strings.Contains(read, "also allows writes") {
		t.Error("the read profile advertises writes it does not have")
	}

	operate := initializeInstructions(t, ProfileOperate)
	if strings.Contains(operate, "Every tool here is a read") {
		t.Error("the operate profile claims it cannot write, which is false and hides " +
			"the tools that can")
	}
	// The two things an agent gets wrong about a queued frame, both of which cost a
	// live network real transmissions if left unsaid.
	for _, want := range []string{"queued, never sent", "Never retry after a 202", "one pending frame"} {
		if !strings.Contains(operate, want) {
			t.Errorf("the operate profile's instructions should state %q", want)
		}
	}
}

func initializeInstructions(t *testing.T, profile Profile) string {
	t.Helper()
	cs := newProfileServer(t, healthyFake(t), profile)
	res := cs.InitializeResult()
	if res == nil {
		t.Fatal("the session has no initialize result")
	}
	// Read off the client's own result rather than off the server, so this asserts
	// on the bytes a host receives and not on a field it may never be sent.
	return res.Instructions
}
