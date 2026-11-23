// Package tools exposes the network server to an MCP client.
//
// Which tools exist is fixed at process start by MCP_PROFILE and can only be widened
// by restarting the process. The default profile is read-only. The operate profile
// adds four writes that act on a device already on the network — queue a frame, cancel
// one, ask for a battery report, change a configuration.
//
// Three categories stay out of every profile, on grounds that have nothing to do with
// which process asks:
//
//   - Provisioning. An AppKey would have to transit an LLM context and land in a
//     transcript. The network server's rule is that keys go in and never come out, and
//     routing them through a model breaks that even though no endpoint returns them.
//   - Integration writes. A destination the network delivers events to is an
//     exfiltration channel and an SSRF primitive at once.
//   - Model writes. Installing a payload formatter is installing JavaScript that
//     executes in the network server's own process.
//
// Three rules run through the package.
//
// Upstream JSON is passed through rather than re-modelled. Re-shaping the responses
// into local structs would make a field rename on the ns show up as a blank column in
// a tool result, and an agent reporting "this device has no data rate" about a device
// that has one is worse than an agent seeing an unfamiliar field name.
//
// Argument mistakes are caught here with a sentence, before the round trip. The ns
// would reject them too, but its message is written for an operator reading curl;
// these are written for a model that has to correct itself on the next turn.
//
// Writes say what they will do before they do it, and record that they did it. The
// annotations are hints and nothing in MCP can force a host to prompt, so the honest
// controls are which tools exist at all and an audit line for every write.
package tools

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/audit"
	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
	"github.com/fieldnode/lorawan-server/mcp/internal/redact"
)

// Deps are the ports every tool goes through.
type Deps struct {
	NS *nsclient.Client

	// Audit records every write. A nil *audit.Logger is valid and silent, so tests
	// and any deployment that does not want auditing need no branch at the call site.
	Audit *audit.Logger
}

// Profile fixes which tools exist for the life of the process.
//
// It is startup configuration and cannot be changed by a tool call, because a
// permission an agent can widen is not a permission. Under protocol revision
// 2026-07-28 the tool list must not vary per connection or as a side effect of
// another request; varying by process configuration is permitted, and that is all
// this does.
type Profile string

const (
	// ProfileRead is the default and the safest: every tool is a read.
	ProfileRead Profile = "read"

	// ProfileOperate adds the four writes that act on a device already on the
	// network: queue a frame, cancel one, ask for a battery report, change a
	// configuration. It does not add provisioning, model writes or integration
	// writes — see the package comment for why those are excluded outright.
	ProfileOperate Profile = "operate"
)

// ParseProfile resolves MCP_PROFILE.
//
// An unrecognised value is an error rather than a silent fallback to read: an
// operator who typed a write profile and got a read-only server would otherwise
// conclude the writes were merely broken. "admin" is refused for the same reason —
// there is no admin-only tool in this build, because the only candidate was the
// console's facility settings endpoint, which is a different upstream with no
// authentication at all. Accepting the name would promise a tier that does not exist.
func ParseProfile(raw string) (Profile, error) {
	switch p := Profile(strings.ToLower(strings.TrimSpace(raw))); p {
	case "", ProfileRead:
		return ProfileRead, nil
	case ProfileOperate:
		return ProfileOperate, nil
	case "admin", "write":
		return "", fmt.Errorf("MCP_PROFILE %q is not implemented: this build offers "+
			"%q and %q. There is no admin tier because the only admin-only candidate was "+
			"the console's facility settings endpoint, which is an unauthenticated "+
			"upstream and not the network server", raw, ProfileRead, ProfileOperate)
	default:
		return "", fmt.Errorf("MCP_PROFILE %q is not recognised; valid values are %q and %q",
			raw, ProfileRead, ProfileOperate)
	}
}

// New builds the MCP server for a profile: implementation identity, the client-facing
// orientation, and the profile's tools.
//
// It exists so that main and the tests construct the same server. When they each
// called mcp.NewServer themselves, the tests passed Instructions: nil and so every
// assertion about what a client learns at initialize was asserting nothing — the
// orientation meant to stop an agent making a wrong first move was never reaching
// anyone. A nil logger leaves the SDK's own logging off.
func New(d *Deps, profile Profile, version string, logger *slog.Logger) (*mcp.Server, []string, error) {
	s := mcp.NewServer(
		&mcp.Implementation{
			Name:    "fieldnode-lorawan",
			Title:   "Fieldnode LoRaWAN",
			Version: version,
			Description: "Access to a Fieldnode LoRaWAN network server: gateways, devices, " +
				"sessions, decoded uplinks, and the trace of what the network refused and why.",
		},
		&mcp.ServerOptions{Instructions: instructionsFor(profile), Logger: logger},
	)
	names, err := Register(s, d, profile)
	if err != nil {
		return nil, nil, err
	}
	return s, names, nil
}

// instructionsFor returns the orientation a client receives at initialize.
//
// It varies by profile because the read-only text's first sentence — "every tool here
// is a read" — becomes a lie the moment writes are enabled, and a model told it cannot
// change anything will not look for the tools that can.
func instructionsFor(profile Profile) string {
	if profile == ProfileOperate {
		return instructions + writeInstructions
	}
	return instructions
}

// instructions is the orientation every client receives at initialize. It is the only
// text that reaches the model before it has called anything, so it carries the things
// an agent cannot infer from any single tool description and will get wrong otherwise.
const instructions = `This is a live LoRaWAN network, seen through its network server.

Start wide, then narrow: network_status describes the whole network, diagnose_device describes one device, and both fold several reads into a single answer. Reach for the single-purpose tools only when you need the full record of one thing.

Nothing here happens on demand. A class-A device listens for about two seconds after it transmits and then goes quiet for however long it next speaks, so "right now" is not a thing this network can be asked for, and an absence of recent activity is usually normal rather than a fault.

Event reason strings have precise meanings. Call explain_event_reason before paraphrasing one: several of them (duplicate_frame, adr_keepalive, duty_cycle_denied) describe the network working correctly rather than failing.

Do not infer from an absent field. has_codec, the battery level 255, and the "not available" sections of diagnose_device all exist because "no data", "no decoder" and "could not look" are three different facts.`

// writeInstructions is appended only when writes are enabled, and leads with the two
// things an agent gets wrong about a LoRaWAN downlink: that it is immediate, and that
// a 202 means it failed.
const writeInstructions = `

This deployment also allows writes, and they are not like writes to a database.

A downlink is queued, never sent. The network server answers 202 because the frame waits in the device's single pending slot for its next RX window, which for a device that reports hourly may be hours away. Never retry after a 202: the retry is refused with a 409 saying a frame already waits, and that is the correct outcome rather than a failure.

Each device holds one pending frame. Anything wanting the slot — an application frame, a battery report — is refused while something else holds it, so the sequence to replace a frame is cancel then queue, not queue again.

Every write needs a reason in your own words. It is shown to whoever approves the call and recorded in the audit log. A write that cannot be explained in a sentence is a write that should not be attempted.

Setting a device's enabled flag to false takes it off the network. From that moment the server refuses its frames, which looks to its owner exactly like a broken device.`

// adder registers one tool and returns its name, so the list Register reports is
// the list it actually registered rather than one maintained by hand.
type adder func(*mcp.Server, *Deps) string

// readAdders is the read-only tool set, grouped the way the ns groups its own
// routes: network, devices, traffic, catalog, then the two that compose.
var readAdders = []adder{
	addNetworkStatus,
	addListDevices, addGetDevice, addGetDeviceSession,
	addListUplinks, addListEvents,
	addListModels, addGetModel,
	addListIntegrations, addGetIntegration,
	addExplainEventReason,
	addDiagnoseDevice,
}

// writeAdders is what the operate profile adds. Kept separate from readAdders so the
// read-only guarantee is a property of a list and not of a comment.
var writeAdders = []adder{
	addEnqueueDownlink, addCancelDownlink, addRequestDeviceStatus, addPatchDevice,
}

// Register adds the profile's tools and returns their names in registration order.
//
// A profile can only add tools, never remove one from a lower tier: operate is read
// plus writes, never read instead of something. That keeps the tiers monotonic, so
// raising a profile can never quietly take a capability away and lower one can never
// leave a half-registered tool behind.
func Register(s *mcp.Server, d *Deps, profile Profile) ([]string, error) {
	if d == nil || d.NS == nil {
		return nil, fmt.Errorf("tools: a network server client is required")
	}
	adders := append([]adder{}, readAdders...)
	switch profile {
	case ProfileRead:
	case ProfileOperate:
		adders = append(adders, writeAdders...)
	default:
		return nil, fmt.Errorf("tools: profile %q is not implemented; valid values are "+
			"%q and %q", profile, ProfileRead, ProfileOperate)
	}
	names := make([]string, 0, len(adders))
	for _, add := range adders {
		names = append(names, add(s, d))
	}
	return names, nil
}

// WriteTools returns the write tool names for a profile, so a test can assert the
// read-only build advertises none of them without restating the list.
func WriteTools(profile Profile) []string {
	if profile == ProfileOperate {
		return []string{
			"enqueue_downlink", "cancel_downlink", "request_device_status", "patch_device",
		}
	}
	return nil
}

// ptr exists because the annotation hints that are tri-state need an addressable
// bool, and &false is not a thing in Go.
func ptr[T any](v T) *T { return &v }

// readOnly builds the annotation block for a read.
//
// OpenWorldHint is false because these tools talk to one operator-owned endpoint,
// not to an open set of external entities. The other two are the defaults, and
// stating them is cheaper than leaving a reader to guess what was meant.
func readOnly(title string) *mcp.ToolAnnotations {
	closed, no := false, false
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		IdempotentHint:  true,
		DestructiveHint: &no,
		OpenWorldHint:   &closed,
	}
}

// text returns content as the tool's text.
func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// read is the body of every read tool: hand back what the network server said, after
// scrubbing anything that looks like a credential.
//
// Returning the error rather than formatting it is deliberate. AddTool turns a returned
// error into a tool result with isError set, which puts the message where the model can
// read it and correct itself; a protocol-level error would hide it from the model
// entirely.
//
// The audit line is at Debug. A read is recoverable and mostly uninteresting, but "what
// did it look at before it said that" is a question someone does ask, and Debug is where
// it stops being unanswerable without burying the writes.
func (d *Deps) read(tool, devEUI string, raw json.RawMessage, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	d.Audit.Read(audit.Entry{Tool: tool, DevEUI: devEUI, Outcome: "ok"})
	return text(string(redact.Body(raw))), nil, nil
}

// cappedRead is read for the one list the network server does not page.
func (d *Deps) cappedRead(tool string, raw json.RawMessage, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	d.Audit.Read(audit.Entry{Tool: tool, Outcome: "ok"})
	return text(string(redact.CapList(redact.Body(raw), redact.MaxListItems))), nil, nil
}

// devEUIPattern is the same width rule the network server enforces on its path
// segment, restated here so a malformed identifier costs a sentence instead of a
// round trip.
var devEUIPattern = regexp.MustCompile(`^[0-9A-Fa-f]{16}$`)

// cleanDevEUI normalises and validates a DevEUI.
func cleanDevEUI(raw string) (string, error) {
	v := strings.ToUpper(strings.TrimSpace(raw))
	if !devEUIPattern.MatchString(v) {
		return "", fmt.Errorf("dev_eui %q is not 16 hexadecimal characters. It is printed "+
			"on the device label; list_devices shows the ones already provisioned", raw)
	}
	return v, nil
}

// cleanOptionalDevEUI allows an empty value, which means "every device" — the
// network server's own convention, not this server's invention.
func cleanOptionalDevEUI(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return cleanDevEUI(raw)
}

// maxLimit matches the clamp in the network server's store. Asking for more is
// refused here rather than quietly answered with something smaller: a tool that
// silently returns fewer rows than were asked for teaches the caller to distrust
// every count it is given.
const maxLimit = 500

// cleanLimit treats 0 as "not given" and lets the network server apply its own
// default, so the default lives in one place instead of two.
func cleanLimit(n int) (int, error) {
	if n == 0 {
		return 0, nil
	}
	if n < 0 || n > maxLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d, or omitted; got %d", maxLimit, n)
	}
	return n, nil
}
