// Package resources exposes the same network as tools, through the resource interface
// instead.
//
// Resources exist for context a client wants *before* it starts reasoning, rather than
// something it discovers by calling tools mid-answer: a host can pin one into every
// conversation the way a system prompt is pinned. So these are the reads whose value is
// highest at the start of a session and whose cost is paid once.
//
// Two rules carried over from tools, both load-bearing:
//
// Upstream bytes go through the same redaction and capping as a tool result. A second
// door into the same data that skips the guard is not a second door, it is the hole.
//
// A URI naming something that does not exist answers ResourceNotFoundError. Returning
// an empty or partial body would let a client conclude that a device has no session
// when in fact nothing was found at all, which is the confusion this server exists to
// avoid.
package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
	"github.com/fieldnode/lorawan-server/mcp/internal/redact"
)

// Scheme is the URI scheme every resource here uses.
const Scheme = "fieldnode"

// URIs, exported so tests and the README name the same strings the server registers
// rather than restating them.
const (
	URIStatus      = "fieldnode://status"
	URIDevices     = "fieldnode://devices"
	URIVocabulary  = "fieldnode://event-vocabulary"
	URIDeviceTmpl  = "fieldnode://devices/{dev_eui}"
	URISessionTmpl = "fieldnode://devices/{dev_eui}/session"
)

// Register adds every resource and returns the URIs registered, so the list a test
// asserts on is the list this function actually produced.
func Register(s *mcp.Server, ns *nsclient.Client) []string {
	if ns == nil {
		return nil
	}

	s.AddResource(
		&mcp.Resource{
			URI:      URIStatus,
			Name:     "status",
			Title:    "Network status",
			MIMEType: "application/json",
			Description: "The live health snapshot of the whole network: band, NetID, uptime, " +
				"gateways and their counters, transports, delivery counters, and duty-cycle " +
				"usage with a denial count. Read it before concluding anything about one " +
				"device, because a gateway that is offline explains a whole group at once.",
		},
		statusReader(ns),
	)

	s.AddResource(
		&mcp.Resource{
			URI:      URIDevices,
			Name:     "devices",
			Title:    "Device inventory",
			MIMEType: "application/json",
			Description: "Every provisioned device, without credentials — the network server " +
				"does not return them. Capped at " + fmt.Sprint(redact.MaxListItems) +
				" members with the truncation carried inside the array, so a count of what is " +
				"present is never mistaken for a count of what exists.",
		},
		devicesReader(ns),
	)

	s.AddResource(vocabularyResource())

	s.AddResourceTemplate(
		&mcp.ResourceTemplate{
			URITemplate: URIDeviceTmpl,
			Name:        "device",
			Title:       "One device",
			MIMEType:    "application/json",
			Description: "One device's configuration as the network server holds it. A URI whose " +
				"dev_eui is not provisioned is refused rather than answered with an empty " +
				"body, so \"not on this network\" and \"nothing recorded\" stay two different " +
				"answers.",
		},
		deviceResource(ns),
	)

	s.AddResourceTemplate(
		&mcp.ResourceTemplate{
			URITemplate: URISessionTmpl,
			Name:        "device-session",
			Title:       "One device's session",
			MIMEType:    "application/json",
			Description: "What the network currently believes about one device's radio " +
				"parameters. Session keys are never present. A refusal means the device has " +
				"never joined, which is a different fact from a failed read.",
		},
		sessionResource(ns),
	)

	return []string{URIStatus, URIDevices, URIVocabulary, URIDeviceTmpl, URISessionTmpl}
}

func statusReader(ns *nsclient.Client) mcp.ResourceHandler {
	return func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		raw, err := ns.Status(ctx)
		if err != nil {
			return nil, err
		}
		return contents(URIStatus, capAndScrub(raw)), nil
	}
}

func devicesReader(ns *nsclient.Client) mcp.ResourceHandler {
	return func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		raw, err := ns.ListDevices(ctx)
		if err != nil {
			return nil, err
		}
		return contents(URIDevices, capAndScrub(raw)), nil
	}
}

// capAndScrub is the single place upstream bytes become safe to hand to a client.
// Keeping it in one function is what makes "the resource path skips the guard" a
// change that can be seen in review rather than one that has to be remembered.
func capAndScrub(raw json.RawMessage) []byte {
	return redact.CapList(redact.Body(raw), redact.MaxListItems)
}

func contents(uri string, body []byte) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(body),
		}},
	}
}

// devEUIFromURI pulls the {dev_eui} segment out of a URI this server issued.
//
// The width check is not pedantry: the value arrives from a client and is echoed back
// into the response URI and the error text, so a fixed-width hex rule costs nothing and
// means no path through this code ever handles arbitrary caller-supplied text.
func devEUIFromURI(uri, prefix, suffix string) (string, error) {
	rest, found := strings.CutPrefix(uri, Scheme+"://"+prefix)
	if !found {
		return "", fmt.Errorf("unrecognised resource URI %q", uri)
	}
	value, _ := strings.CutSuffix(rest, suffix)
	value = strings.ToUpper(strings.TrimSpace(value))
	if !devEUIPattern.MatchString(value) {
		return "", mcp.ResourceNotFoundError(uri)
	}
	return value, nil
}

var devEUIPattern = regexp.MustCompile(`^[0-9A-Fa-f]{16}$`)
