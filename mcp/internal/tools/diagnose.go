package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fieldnode/lorawan-server/mcp/internal/nsclient"
)

type diagnoseIn struct {
	DevEUI string `json:"dev_eui" jsonschema:"the device's 16-hex-digit DevEUI"`
}

// The shapes below read only the fields the digest reports. They are deliberately
// not a model of the API: a field the network server renames shows up here as
// "unknown", which is a visible gap, rather than as a zero, which would be a claim.
// Everything else about a device is available verbatim from the single-purpose
// tools.
type (
	diagDevice struct {
		DevEUI     string `json:"dev_eui"`
		Name       string `json:"name"`
		Group      string `json:"group"`
		Enabled    bool   `json:"enabled"`
		Activation string `json:"activation"`
		DevAddr    string `json:"dev_addr"`
		ModelID    string `json:"model_id"`
	}

	diagSession struct {
		DevAddr      string          `json:"dev_addr"`
		Region       string          `json:"region"`
		FCntUp       uint32          `json:"f_cnt_up"`
		DataRate     int             `json:"data_rate"`
		ADR          bool            `json:"adr"`
		LastRSSI     float64         `json:"last_rssi"`
		LastSNR      float64         `json:"last_snr"`
		LastUplinkAt *time.Time      `json:"last_uplink_at"`
		Battery      *int            `json:"battery"`
		MarginDB     *int            `json:"margin_db"`
		Pending      json.RawMessage `json:"pending_downlink"`
	}

	diagEvent struct {
		OccurredAt string `json:"occurred_at"`
		Kind       string `json:"kind"`
		Reason     string `json:"reason"`
		Detail     string `json:"detail"`
	}

	diagUplink struct {
		ReceivedAt  string  `json:"received_at"`
		FPort       int     `json:"f_port"`
		RSSI        float64 `json:"rssi"`
		SNR         float64 `json:"snr"`
		HasCodec    bool    `json:"has_codec"`
		DecodeError string  `json:"decode_error"`
	}

	diagStatus struct {
		Region         string   `json:"region"`
		GatewaysTotal  int      `json:"gateways_total"`
		GatewaysOnline int      `json:"gateways_online"`
		Transports     []string `json:"transports"`
		DutyCycle      *struct {
			Denials map[string]uint64 `json:"denials"`
		} `json:"duty_cycle"`
	}
)

const (
	diagnoseEventWindow  = 8
	diagnoseUplinkWindow = 5
)

func addDiagnoseDevice(s *mcp.Server, d *Deps) string {
	const name = "diagnose_device"
	mcp.AddTool(s, &mcp.Tool{
		Name:  name,
		Title: "Diagnose device",
		Description: "One device, assembled in a single call: its configuration, its session, " +
			"its recent events with refusal counts, its recent uplinks and whether their " +
			"formatters decoded, and the state of the network it sits on. Prefer this to " +
			"calling the five reads separately — it costs one round trip and far less context, " +
			"and it reports the parts that failed rather than omitting them, so a gap is " +
			"visible instead of looking like a zero. It is facts only and draws no conclusion: " +
			"why a device is failing is usually in the event detail, which is quoted here for " +
			"you to read.",
		Annotations: readOnly("Diagnose device"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in diagnoseIn) (*mcp.CallToolResult, any, error) {
		devEUI, err := cleanDevEUI(in.DevEUI)
		if err != nil {
			return nil, nil, err
		}
		report, err := d.diagnose(ctx, devEUI)
		if err != nil {
			return nil, nil, err
		}
		return text(report), nil, nil
	})
	return name
}

// diagnose fans out. Only the device read is required: without it there is nothing
// to describe. Every other read degrades into a labelled gap, because a digest that
// silently omits the part that failed is how "no session" becomes "no traffic"
// becomes a confident wrong answer.
func (d *Deps) diagnose(ctx context.Context, devEUI string) (string, error) {
	var gaps []string

	deviceRaw, err := d.NS.GetDevice(ctx, devEUI)
	if err != nil {
		// A device that is not provisioned is the most useful thing this call can
		// say, so it is reported as an answer rather than as a failure.
		var nf *nsclient.NotFoundError
		if errors.As(err, &nf) {
			return fmt.Sprintf("device %s: not provisioned on this network.\n\n"+
				"There is nothing else to report. The network has no record of this DevEUI, "+
				"so it cannot have a session and any frame it sends will be refused. If it "+
				"is transmitting, compare the DevEUI on its label with what was asked for.",
				devEUI), nil
		}
		return "", err
	}
	var device diagDevice
	if err := json.Unmarshal(deviceRaw, &device); err != nil {
		return "", fmt.Errorf("cannot read the device record: %w", err)
	}

	var session *diagSession
	if raw, err := d.NS.Session(ctx, devEUI); err != nil {
		var nf *nsclient.NotFoundError
		if !errors.As(err, &nf) {
			gaps = append(gaps, "session could not be read: "+err.Error())
		}
	} else {
		session = &diagSession{}
		if err := json.Unmarshal(raw, session); err != nil {
			gaps = append(gaps, "the session came back in a shape this server does not recognise")
			session = nil
		}
	}

	var events []diagEvent
	if raw, err := d.NS.ListEvents(ctx, devEUI, "", diagnoseEventWindow); err != nil {
		gaps = append(gaps, "event history could not be read: "+err.Error())
	} else {
		var wrapped struct {
			Events []diagEvent `json:"events"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			gaps = append(gaps, "event history came back in a shape this server does not recognise")
		}
		events = wrapped.Events
	}

	var uplinks []diagUplink
	if raw, err := d.NS.ListUplinks(ctx, devEUI, diagnoseUplinkWindow); err != nil {
		gaps = append(gaps, "uplink history could not be read: "+err.Error())
	} else {
		var wrapped struct {
			Uplinks []diagUplink `json:"uplinks"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			gaps = append(gaps, "uplink history came back in a shape this server does not recognise")
		}
		uplinks = wrapped.Uplinks
	}

	var status *diagStatus
	if raw, err := d.NS.Status(ctx); err != nil {
		gaps = append(gaps, "network status could not be read: "+err.Error())
	} else if err := json.Unmarshal(raw, &status); err != nil {
		gaps = append(gaps, "network status came back in a shape this server does not recognise")
		status = nil
	}

	return renderDigest(devEUI, &device, session, events, uplinks, status, gaps), nil
}
