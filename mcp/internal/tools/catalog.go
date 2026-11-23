package tools

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// An empty identifier would reach the network server as a request for the
// collection with a trailing slash, which answers 404 for a resource that was never
// named. Saying so here is both cheaper and truer.
var (
	errModelIDRequired       = errors.New("model_id is required; list_models shows the ones that exist")
	errIntegrationIDRequired = errors.New("id is required; list_integrations shows the ones that exist")
)

type listIn struct{}

type modelIDIn struct {
	ModelID string `json:"model_id" jsonschema:"the model's slug, as shown by list_models"`
}

type integrationIDIn struct {
	ID string `json:"id" jsonschema:"the integration's identifier, as shown by list_integrations"`
}

func addListModels(s *mcp.Server, d *Deps) string {
	const name = "list_models"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "List device models",
		Description: "The device models — what a kind of sensor is, and the payload formatter " +
			"its bytes are read with — with names, descriptions and codec kinds but without " +
			"the scripts, because a fleet's worth of scripts is hundreds of kilobytes and a " +
			"picker needs names. A device with no model still joins, still reports and its " +
			"frames are still stored as hex: a model decides how bytes are read, not whether " +
			"the network accepts them.",
		Annotations: readOnly("List device models"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listIn) (*mcp.CallToolResult, any, error) {
		raw, err := d.NS.ListModels(ctx)
		return d.read(name, "", raw, err)
	})
	return name
}

func addGetModel(s *mcp.Server, d *Deps) string {
	const name = "get_model"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Get device model",
		Description: "One device model including its formatter script and timestamps. The " +
			"script is returned because it came from a vendor datasheet rather than being a " +
			"secret, and an operator who cannot see what a fleet runs cannot audit it. " +
			"Changing a model affects new uplinks only: stored readings keep whatever decoded " +
			"them, so history does not rewrite itself when a formatter is corrected. This " +
			"build cannot create or change a model.",
		Annotations: readOnly("Get device model"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in modelIDIn) (*mcp.CallToolResult, any, error) {
		if in.ModelID == "" {
			return nil, nil, errModelIDRequired
		}
		raw, err := d.NS.GetModel(ctx, in.ModelID)
		return d.read(name, "", raw, err)
	})
	return name
}

func addListIntegrations(s *mcp.Server, d *Deps) string {
	const name = "list_integrations"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "List integrations",
		Description: "Where the network delivers events: each destination's kind and endpoint, " +
			"whether it is enabled, and which destinations come from the server's own " +
			"environment and are therefore read-only even for an operator. Read this before " +
			"concluding that a downstream system is losing data — an event that was never " +
			"sent is a different problem from one that was sent and dropped. This build " +
			"cannot create, change or delete a destination.",
		Annotations: readOnly("List integrations"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listIn) (*mcp.CallToolResult, any, error) {
		raw, err := d.NS.ListIntegrations(ctx)
		return d.read(name, "", raw, err)
	})
	return name
}

func addGetIntegration(s *mcp.Server, d *Deps) string {
	const name = "get_integration"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Get integration",
		Description: "One event destination in full. Delivery counters for the whole " +
			"dispatcher, not per destination, come from network_status.",
		Annotations: readOnly("Get integration"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in integrationIDIn) (*mcp.CallToolResult, any, error) {
		if in.ID == "" {
			return nil, nil, errIntegrationIDRequired
		}
		raw, err := d.NS.GetIntegration(ctx, in.ID)
		return d.read(name, "", raw, err)
	})
	return name
}
