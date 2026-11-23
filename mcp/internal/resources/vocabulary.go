package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
	"github.com/fieldnode/lorawan-server/mcp/internal/vocabulary"
)

// vocabularyResource serves the reason table from this process.
//
// It is plain text rather than JSON on purpose. This resource exists to be read by a
// model and pinned into a conversation, and a table of prose in a JSON envelope costs
// the quoting and the field names again on every reason. The machine-readable form of
// the vocabulary is the ns's own event stream, which is what list_events is for.
func vocabularyResource() (*mcp.Resource, mcp.ResourceHandler) {
	resource := &mcp.Resource{
		URI:      URIVocabulary,
		Name:     "event-vocabulary",
		Title:    "Event reason vocabulary",
		MIMEType: "text/plain",
		Description: "Every reason this network server records against an event, with what it " +
			"means and what usually causes it. Served by this server rather than the network " +
			"server, and kept honest by a test that reads the ns's own vocabulary file and " +
			"fails when the two fall out of step. Read it once instead of guessing at a " +
			"reason: several of these (duplicate_frame, adr_keepalive, duty_cycle_denied) " +
			"describe the network working correctly rather than failing.",
	}
	return resource, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if req.Params != nil && req.Params.URI != URIVocabulary {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      URIVocabulary,
				MIMEType: "text/plain",
				Text:     renderVocabulary(),
			}},
		}, nil
	}
}

// renderVocabulary is deterministic: sorted by reason, so two reads of the same build
// produce identical bytes and a client's prompt cache keeps hitting.
func renderVocabulary() string {
	described := vocabulary.All()

	var b strings.Builder
	fmt.Fprintf(&b, "Event reasons recorded by this Fieldnode network server (%d)\n\n", len(described))
	b.WriteString("A reason is a machine-readable label, not a diagnosis. The event's own\n")
	b.WriteString("detail field is the evidence; this table says what the label means.\n\n")
	for _, d := range described {
		fmt.Fprintf(&b, "%s\n", d.Reason)
		fmt.Fprintf(&b, "  means:  %s\n", d.Summary)
		fmt.Fprintf(&b, "  usually caused by: %s\n", d.Cause)
		if d.Where != "" {
			fmt.Fprintf(&b, "  start reading at: %s\n", d.Where)
		}
		b.WriteString("\n")
	}
	b.WriteString("A reason not listed here is not one this network server records.\n")
	return b.String()
}

// deviceResource reads one device by the dev_eui in the requested URI.
func deviceResource(ns *nsclient.Client) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := requestURI(req)
		devEUI, err := devEUIFromURI(uri, "devices/", "")
		if err != nil {
			return nil, err
		}
		raw, err := ns.GetDevice(ctx, devEUI)
		if err != nil {
			// The ns's own 404 is the honest answer here and it names the DevEUI, so
			// it is passed through rather than converted to a generic not-found, which
			// would lose the sentence "dev_eui ... is not provisioned".
			return nil, err
		}
		return contents(URIDeviceTmplText(devEUI), capAndScrub(raw)), nil
	}
}

// sessionResource reads one device's session.
func sessionResource(ns *nsclient.Client) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := requestURI(req)
		devEUI, err := devEUIFromURI(uri, "devices/", "/session")
		if err != nil {
			return nil, err
		}
		raw, err := ns.Session(ctx, devEUI)
		if err != nil {
			return nil, err
		}
		return contents(URIDeviceTmplText(devEUI)+"/session", capAndScrub(raw)), nil
	}
}

// URIDeviceTmplText builds the concrete URI for one device, the way a client would.
func URIDeviceTmplText(devEUI string) string {
	return Scheme + "://devices/" + devEUI
}

func requestURI(req *mcp.ReadResourceRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	return req.Params.URI
}
