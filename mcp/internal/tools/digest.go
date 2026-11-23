package tools

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// renderDigest assembles the fan-out into a fixed-order report.
//
// The order is the order an operator asks in: is it even provisioned, is it joined,
// what happened, what did it send, and is the network it is on healthy. Sections are
// never dropped for being empty — an explicit "none" is information, and a missing
// section is the one thing a reader cannot distinguish from a bug.
func renderDigest(devEUI string, device *diagDevice, session *diagSession,
	events []diagEvent, uplinks []diagUplink, status *diagStatus, gaps []string) string {

	var b strings.Builder
	fmt.Fprintf(&b, "device %s\n", devEUI)

	// --- configuration ---
	fmt.Fprintf(&b, "\nconfiguration\n  name:       %s\n", orUnnamed(device.Name))
	if device.Group != "" {
		fmt.Fprintf(&b, "  group:      %s\n", device.Group)
	}
	enabled := "no — the network refuses this device's frames on purpose"
	if device.Enabled {
		enabled = "yes"
	}
	fmt.Fprintf(&b, "  enabled:    %s\n", enabled)
	fmt.Fprintf(&b, "  activation: %s\n", orUnknown(device.Activation))
	if device.DevAddr != "" {
		fmt.Fprintf(&b, "  dev_addr:   %s\n", device.DevAddr)
	}
	model := device.ModelID
	if model == "" {
		model = "(none — its frames are stored as hex, not decoded)"
	}
	fmt.Fprintf(&b, "  model:      %s\n", model)

	// --- session ---
	b.WriteString("\nsession\n")
	switch {
	case session == nil:
		b.WriteString("  none — this device has no session on this network, so it has " +
			"never joined, or its session was removed. Look for join_request and " +
			"join_reject events below.\n")
	default:
		fmt.Fprintf(&b, "  joined on %s, data rate %d, ADR %s, f_cnt_up %d\n",
			orUnknown(session.Region), session.DataRate, yesNo(session.ADR), session.FCntUp)
		if session.DevAddr != "" {
			fmt.Fprintf(&b, "  dev_addr: %s\n", session.DevAddr)
		}
		fmt.Fprintf(&b, "  last uplink: %s, rssi %.1f dBm, snr %.1f dB\n",
			orNever(session.LastUplinkAt), session.LastRSSI, session.LastSNR)
		if line := batteryLine(session.Battery, session.MarginDB); line != "" {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		if len(session.Pending) > 0 && string(session.Pending) != "null" {
			b.WriteString("  a downlink is queued for its next RX window\n")
		}
	}

	// --- events ---
	fmt.Fprintf(&b, "\nevents (newest first, up to %d)\n", diagnoseEventWindow)
	if len(events) == 0 {
		b.WriteString("  none recorded for this device\n")
	}
	for _, ev := range events {
		reason, detail := "", ""
		if ev.Reason != "" {
			reason = " [" + ev.Reason + "]"
		}
		if ev.Detail != "" {
			detail = " — " + ev.Detail
		}
		fmt.Fprintf(&b, "  %s  %s%s%s\n", ev.OccurredAt, ev.Kind, reason, detail)
	}
	if tally := refusalTally(events); tally != "" {
		fmt.Fprintf(&b, "  in this window: %s\n", tally)
	}

	// --- uplinks ---
	fmt.Fprintf(&b, "\nuplinks (newest first, up to %d)\n", diagnoseUplinkWindow)
	if len(uplinks) == 0 {
		b.WriteString("  none recorded\n")
	}
	for _, u := range uplinks {
		fmt.Fprintf(&b, "  %s  f_port %d  rssi %.1f  snr %.1f  %s\n",
			u.ReceivedAt, u.FPort, u.RSSI, u.SNR, decodeState(u))
	}

	// --- network ---
	b.WriteString("\nnetwork\n")
	if status == nil {
		b.WriteString("  unknown — see the gaps below\n")
	} else {
		fmt.Fprintf(&b, "  band %s, %d of %d gateways online, transports [%s]\n",
			orUnknown(status.Region), status.GatewaysOnline, status.GatewaysTotal,
			strings.Join(status.Transports, ", "))
		if status.DutyCycle != nil && len(status.DutyCycle.Denials) > 0 {
			fmt.Fprintf(&b, "  duty-cycle denials: %s\n", denialsLine(status.DutyCycle.Denials))
		}
	}

	// --- gaps ---
	if len(gaps) > 0 {
		b.WriteString("\nnot available\n")
		for _, g := range gaps {
			fmt.Fprintf(&b, "  - %s\n", g)
		}
		b.WriteString("\nA gap is not a zero. Ask for that part with the single-purpose " +
			"tool before drawing a conclusion from its absence here.\n")
	}
	return b.String()
}

// refusalTally counts the kinds that mean a frame did not become application data.
// It is a count, not a verdict: duplicate_frame and duty_cycle_denied land in here
// too, and both are normal operation rather than failure.
func refusalTally(events []diagEvent) string {
	counts := map[string]int{}
	for _, ev := range events {
		if ev.Kind == "uplink_drop" || ev.Kind == "join_reject" {
			counts[ev.Kind]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return strings.Join(parts, ", ")
}

// decodeState keeps "no formatter" and "the formatter refused" apart, which is the
// distinction that hides a broken formatter behind an apparently undecoded fleet.
func decodeState(u diagUplink) string {
	switch {
	case u.DecodeError != "":
		return "DECODE FAILED: " + u.DecodeError
	case !u.HasCodec:
		return "no formatter configured"
	default:
		return "decoded"
	}
}

// batteryLine calls out 255, which the specification reserves for "this device
// cannot report a level" and which otherwise reads as a full battery.
func batteryLine(battery, margin *int) string {
	var parts []string
	switch {
	case battery == nil:
	case *battery == 255:
		parts = append(parts, "battery: cannot report")
	default:
		parts = append(parts, fmt.Sprintf("battery: %d/255", *battery))
	}
	if margin != nil {
		parts = append(parts, fmt.Sprintf("link margin: %d dB", *margin))
	}
	return strings.Join(parts, ", ")
}

// denialsLine sorts so the same state renders identically twice, which is what lets
// two turns of a conversation be compared rather than re-read.
func denialsLine(denials map[string]uint64) string {
	keys := make([]string, 0, len(denials))
	for k := range denials {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, denials[k]))
	}
	return strings.Join(parts, " ")
}

func orUnnamed(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unnamed)"
	}
	return s
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// orNever distinguishes "the device has not reported since this session began" from
// a zero timestamp, which would read as a date in year 1.
func orNever(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "never in this session"
	}
	return t.UTC().Format(time.RFC3339)
}

func yesNo(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
