// Package prompts offers the procedures an operator follows, as prompts.
//
// A prompt is not a shortcut around the tools: it is the order, published. The
// troubleshooting sequence for a device that is not answering is knowledge this project
// already has, written down in ns/README.md, and an agent that has to rediscover it
// will get the order wrong in a way that costs a live network real transmissions —
// retrying a queued downlink, or asking for a battery report that occupies the one slot
// holding the frame the application actually wanted sent.
//
// So these carry no data. They are text that names the tools and the order, and the
// agent then calls them. That keeps them honest: nothing here can be stale about a
// particular device, because nothing here claims to know about one.
package prompts

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Names, exported so tests and the README name the same strings the server registers.
const (
	DiagnoseDevice = "diagnose-device"
	WhyNoDownlink  = "why-no-downlink"
	FleetHealth    = "fleet-health"
)

// Register adds every prompt and returns their names.
func Register(s *mcp.Server) []string {
	s.AddPrompt(&mcp.Prompt{
		Name:        DiagnoseDevice,
		Title:       "Diagnose one device",
		Description: "Work out why a specific device is not doing what it should, in the order that avoids making it worse.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "dev_eui",
			Title:       "DevEUI",
			Description: "The device's 16 hex digits, as printed on its label.",
			Required:    true,
		}},
	}, withDevEUI(DiagnoseDevice, diagnoseDeviceBody))

	s.AddPrompt(&mcp.Prompt{
		Name:        WhyNoDownlink,
		Title:       "Why a downlink did not go out",
		Description: "Trace a downlink that was accepted but never reached the device, without retrying it into a 409.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "dev_eui",
			Title:       "DevEUI",
			Description: "The device's 16 hex digits.",
			Required:    true,
		}},
	}, withDevEUI(WhyNoDownlink, whyNoDownlinkBody))

	s.AddPrompt(&mcp.Prompt{
		Name:        FleetHealth,
		Title:       "Fleet health sweep",
		Description: "A short pass over the whole network that separates a network-wide fault from a per-device one before anything is changed.",
	}, fleetHealthBody)

	return []string{DiagnoseDevice, WhyNoDownlink, FleetHealth}
}

// withDevEUI builds a prompt handler that refuses a malformed DevEUI rather than
// interpolating it.
//
// The argument is caller-supplied text that ends up inside a message addressed to a
// model. A fixed-width hex rule means no path through here ever puts arbitrary
// instructions from a client into the prompt body, which is cheaper to enforce than to
// reason about.
func withDevEUI(name, body string) mcp.PromptHandler {
	return func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		if req.Params == nil {
			return nil, fmt.Errorf("prompt %q was called without parameters", name)
		}
		raw := strings.TrimSpace(req.Params.Arguments["dev_eui"])
		if !devEUIPattern.MatchString(raw) {
			return nil, fmt.Errorf("the %q prompt needs a dev_eui of exactly 16 hex "+
				"digits; got %q", name, raw)
		}
		return userPrompt(strings.ReplaceAll(body, "{{dev_eui}}", strings.ToUpper(raw))), nil
	}
}

func fleetHealthBody(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return userPrompt(fleetHealthText), nil
}

func userPrompt(text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: text},
		}},
	}
}

var devEUIPattern = regexp.MustCompile(`^[0-9A-Fa-f]{16}$`)
