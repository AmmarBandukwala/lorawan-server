package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
)

// patchDeviceIn mirrors the network server's PATCH body field for field.
//
// Every field is a pointer, and that is load-bearing rather than stylistic. The
// network server applies only the fields present in the body, so a nil here means
// "leave it alone" and a value means "set it". A plain bool would serialise
// enabled:false on every call that only meant to rename a device, silently taking a
// live device off the network — the single worst outcome available to a tool of this
// shape, and one that would look like success.
type patchDeviceIn struct {
	DevEUI string `json:"dev_eui" jsonschema:"the device's 16-hex-digit DevEUI"`
	Reason string `json:"reason" jsonschema:"why this change is being made; shown to whoever approves the call and recorded in the audit log"`

	Name           *string `json:"name,omitempty" jsonschema:"rename the device; at most 120 characters"`
	Group          *string `json:"group,omitempty" jsonschema:"move the device to a different group; at most 120 characters"`
	ModelID        *string `json:"model_id,omitempty" jsonschema:"re-associate with a device model. An empty string clears the association, which stops the device being decoded without deleting anything"`
	Enabled        *bool   `json:"enabled,omitempty" jsonschema:"false takes the device off the network: the server refuses its frames from then on"`
	DisableADR     *bool   `json:"disable_adr,omitempty" jsonschema:"true pins the device to its current data rate instead of letting ADR move it"`
	SupportsClassC *bool   `json:"supports_class_c,omitempty" jsonschema:"declare class-C capability; class C is not implemented by this network server, so this changes a flag and not the behaviour"`
	MaxDataRate    *int    `json:"max_data_rate,omitempty" jsonschema:"highest data rate the device may use, within the band's range"`
	MinDataRate    *int    `json:"min_data_rate,omitempty" jsonschema:"lowest data rate the device may use; must not exceed max_data_rate"`
	MaxTxPower     *int    `json:"max_tx_power,omitempty" jsonschema:"maximum transmit power index"`

	// Attributes replaces the whole map server-side, so a pointer to a map is the
	// only way to tell "leave the attributes alone" from "the device has none now".
	Attributes *map[string]string `json:"attributes,omitempty" jsonschema:"replace all operator attributes; an empty object means no attributes, which is different from omitting this field"`

	// Location is raw so absent, null and object stay three cases: leave, clear, set.
	Location json.RawMessage `json:"location,omitempty" jsonschema:"set the position as {\"latitude\":..,\"longitude\":..}; send JSON null to clear it; omit to leave it alone"`
}

func addPatchDevice(s *mcp.Server, d *Deps) string {
	const name = "patch_device"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Change a device's configuration",
		Description: "Apply a partial update to one device and return the network server's " +
			"view of it afterwards, so you can see what actually changed rather than assume it. " +
			"Only the fields you send are touched — omit a field and it keeps its value. " +
			"Credentials cannot be read or written through this endpoint at all, which is why " +
			"PATCH exists: the server reads the stored keys and writes them back untouched. " +
			"Setting enabled:false is the destructive case and should be said plainly in the " +
			"reason: from that moment the network refuses the device's frames, which looks to " +
			"the owner like a broken device. Re-associating a model affects how future uplinks " +
			"are decoded; stored readings keep whatever decoded them.",
		Annotations: writeAnnotations("Change a device's configuration", true, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in patchDeviceIn) (*mcp.CallToolResult, any, error) {
		devEUI, err := cleanDevEUI(in.DevEUI)
		if err != nil {
			return nil, nil, err
		}
		reason, err := requireReason(in.Reason)
		if err != nil {
			return nil, nil, err
		}
		patch := nsclient.DevicePatch{
			Name: in.Name, Group: in.Group, ModelID: in.ModelID,
			Enabled: in.Enabled, DisableADR: in.DisableADR, SupportsClassC: in.SupportsClassC,
			MaxDataRate: in.MaxDataRate, MinDataRate: in.MinDataRate, MaxTxPower: in.MaxTxPower,
			Attributes: in.Attributes, LocationJSON: in.Location,
		}
		if patch.Empty() {
			return nil, nil, fmt.Errorf("no fields were supplied, so this would change " +
				"nothing. Send at least one of name, group, model_id, enabled, disable_adr, " +
				"supports_class_c, max_data_rate, min_data_rate, max_tx_power, attributes, location")
		}

		res, _, err := d.write(ctx, name, in, devEUI, reason, func() (int, json.RawMessage, error) {
			return d.NS.PatchDevice(ctx, devEUI, patch)
		})
		if err != nil {
			return nil, nil, err
		}
		// Not a refusal: a refusal would be this process setting a policy the operator
		// owns. But silencing a device is the one edit an owner feels, and the result of
		// a successful PATCH otherwise reads identically to renaming one, so the
		// consequence travels back with the confirmation.
		if patch.Enabled != nil && !*patch.Enabled {
			appendNotice(res, "This device is now disabled: the network server refuses its "+
				"frames from here on. To its owner that looks exactly like a broken device, so "+
				"if nobody asked for this, set enabled back to true now.")
		}
		return res, nil, nil
	})
	return name
}

// appendNotice adds a line to a tool result's text content.
func appendNotice(res *mcp.CallToolResult, notice string) {
	if res == nil || len(res.Content) == 0 {
		return
	}
	first, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return
	}
	first.Text = first.Text + "\n\n" + notice
}
