package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listDevicesIn struct{}

type devEUIIn struct {
	DevEUI string `json:"dev_eui" jsonschema:"the device's 16-hex-digit DevEUI, as printed on its label"`
}

func addListDevices(s *mcp.Server, d *Deps) string {
	const name = "list_devices"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "List devices",
		Description: "Every provisioned device: DevEUI, join EUI, name, group, whether it is " +
			"enabled, OTAA or ABP, its DevAddr if ABP, the device model it is an instance of, " +
			"its radio limits, and any operator attributes and location. Credentials are " +
			"never present — the network server does not return them, so asking again will " +
			"not produce them. This is the whole inventory in one response with no paging; " +
			"for a large fleet prefer get_device with a DevEUI you already have.",
		Annotations: readOnly("List devices"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listDevicesIn) (*mcp.CallToolResult, any, error) {
		// The one read the network server does not page, so the one that needs a cap
		// here. The cap announces itself in the payload rather than shortening the
		// list quietly: an agent that counted the items would otherwise report a
		// truncated fleet as the whole one.
		raw, err := d.NS.ListDevices(ctx)
		return d.cappedRead(name, raw, err)
	})
	return name
}

func addGetDevice(s *mcp.Server, d *Deps) string {
	const name = "get_device"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Get device",
		Description: "One device's configuration — the settings an edit would have to show, " +
			"which the inventory list deliberately omits. A refusal here saying the DevEUI is " +
			"not provisioned is the fastest possible answer to 'why is this device not " +
			"joining', and it means the device is not on this network at all rather than " +
			"that it is broken.",
		Annotations: readOnly("Get device"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devEUIIn) (*mcp.CallToolResult, any, error) {
		devEUI, err := cleanDevEUI(in.DevEUI)
		if err != nil {
			return nil, nil, err
		}
		raw, err := d.NS.GetDevice(ctx, devEUI)
		return d.read(name, devEUI, raw, err)
	})
	return name
}

func addGetDeviceSession(s *mcp.Server, d *Deps) string {
	const name = "get_device_session"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Get device session",
		Description: "What the network currently believes about one device's radio parameters: " +
			"DevAddr, band, both frame counters, data rate, TX power index, NbTrans, whether " +
			"ADR is active, the RX window settings, the device's duty-cycle fraction, the last " +
			"RSSI and SNR, when it last reported, battery and link margin if it ever answered " +
			"a DevStatusReq, and any downlink waiting for its next window. Session keys are " +
			"never returned. Battery 255 means the device cannot report a level, not that it " +
			"is empty. A refusal saying there is no session means the device has never joined " +
			"— a different fact from a failed request, and look at list_events for why.",
		Annotations: readOnly("Get device session"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devEUIIn) (*mcp.CallToolResult, any, error) {
		devEUI, err := cleanDevEUI(in.DevEUI)
		if err != nil {
			return nil, nil, err
		}
		raw, err := d.NS.Session(ctx, devEUI)
		return d.read(name, devEUI, raw, err)
	})
	return name
}
