package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func quietMQTT() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseMQTTTopic(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		devEUI  bool
		kind    bool
		wantErr bool
	}{
		{in: "", want: DefaultMQTTTopic},
		{in: "  fieldnode/up  ", want: "fieldnode/up"},
		{in: "fieldnode/{dev_eui}/up", want: "fieldnode/{dev_eui}/up", devEUI: true},
		{in: "fieldnode/{dev_eui}/{kind}", want: "fieldnode/{dev_eui}/{kind}", devEUI: true, kind: true},
		// A wildcard is only valid when subscribing; publishing to one is a
		// protocol error most brokers reject at the wire level.
		{in: "fieldnode/+/up", wantErr: true},
		{in: "fieldnode/#", wantErr: true},
		// An empty level is rejected by several brokers and is nearly always a
		// concatenation bug in whatever built the topic.
		{in: "fieldnode//up", wantErr: true},
		{in: "/fieldnode", wantErr: true},
		{in: "fieldnode/", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseMQTTTopic(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseMQTTTopic(%q) accepted an invalid topic", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMQTTTopic(%q): %v", tc.in, err)
			continue
		}
		if got.raw != tc.want || got.devEUI != tc.devEUI || got.kind != tc.kind {
			t.Errorf("parseMQTTTopic(%q) = %+v", tc.in, got)
		}
	}
}

// TestMQTTRenderCannotForgeTopicLevels is the security-relevant one. The
// substituted DevEUI comes from a stored device row, but the same code path runs
// on values that originated on the wire; a "/" or wildcard in a substituted
// value would let a device place itself anywhere in the broker's topic tree and
// be delivered to subscribers that never asked for it.
func TestMQTTRenderCannotForgeTopicLevels(t *testing.T) {
	tmpl, err := parseMQTTTopic("fieldnode/{dev_eui}/{kind}")
	if err != nil {
		t.Fatalf("parseMQTTTopic: %v", err)
	}
	cases := []struct {
		devEUI, kind string
		want         string
	}{
		{"0011223344556677", "uplink", "fieldnode/0011223344556677/uplink"},
		{"a/b/c", "uplink", "fieldnode/a-b-c/uplink"},
		// Dots are legal in an MQTT topic level, so they are left alone; the
		// invariant that matters is that the level count cannot grow.
		{"..", "up", "fieldnode/../up"},
		{"", "", "fieldnode/unknown/unknown"},
	}
	for _, tc := range cases {
		got := tmpl.render(tc.devEUI, tc.kind)
		if got != tc.want {
			t.Errorf("render(%q,%q) = %q, want %q", tc.devEUI, tc.kind, got, tc.want)
		}
		if strings.ContainsAny(got, "+#") {
			t.Errorf("render(%q,%q) = %q, still contains a wildcard", tc.devEUI, tc.kind, got)
		}
		if strings.Contains(got, "//") || strings.HasPrefix(got, "/") || strings.HasSuffix(got, "/") {
			t.Errorf("render(%q,%q) = %q, has an empty level", tc.devEUI, tc.kind, got)
		}
		// Level count must never grow: that is what "forging a level" means.
		if n := strings.Count(got, "/"); n != 2 {
			t.Errorf("render(%q,%q) = %q has %d levels, want the template's 3",
				tc.devEUI, tc.kind, got, n+1)
		}
	}
}

func TestMQTTConfigValidation(t *testing.T) {
	cases := map[string]MQTTConfig{
		"empty broker":    {BrokerURL: ""},
		"no scheme":       {BrokerURL: "localhost:1883"},
		"http scheme":     {BrokerURL: "http://localhost:1883"},
		"no host":         {BrokerURL: "tcp://"},
		"qos 2":           {BrokerURL: "tcp://localhost:1883", QoS: 2},
		"wildcard topic":  {BrokerURL: "tcp://localhost:1883", Topic: "a/+/b"},
		"unparseable url": {BrokerURL: "tcp://[::1"},
	}
	for name, cfg := range cases {
		cfg.Logger = quietMQTT()
		// Nothing here should reach the network, so a tiny timeout keeps a
		// regression loud rather than slow.
		cfg.ConnectTimeout = time.Millisecond
		m, err := NewMQTT(cfg)
		if err == nil {
			_ = m.Close()
			t.Errorf("%s: NewMQTT accepted an invalid configuration", name)
		}
	}
}

// TestMQTTAcceptsAnUnreachableBroker pins the deliberate asymmetry: a broker
// that is down is a runtime condition, not a configuration error. Failing here
// would mean an operator who saved a not-yet-reachable broker could not start
// the network server at all.
func TestMQTTAcceptsAnUnreachableBroker(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{
		// Port 1 is assigned to tcpmux and nothing listens on it locally.
		BrokerURL:      "tcp://127.0.0.1:1",
		Topic:          "fieldnode/{dev_eui}",
		ConnectTimeout: 50 * time.Millisecond,
		Logger:         quietMQTT(),
	})
	if err != nil {
		t.Fatalf("NewMQTT should accept a valid config with a dead broker: %v", err)
	}
	defer m.Close()

	if m.Connected() {
		t.Error("a broker on port 1 must not report connected")
	}
	if m.Name() != "mqtt:127.0.0.1:1" {
		t.Errorf("Name = %q", m.Name())
	}
	if m.Topic() != "fieldnode/{dev_eui}" {
		t.Errorf("Topic = %q", m.Topic())
	}

	// And publishing to it must report the gap rather than drop silently -- and
	// report it promptly. This is the regression that matters: paho reports
	// IsConnected() as true while ConnectRetry is still trying, so a readiness
	// check built on it lets every publish wait out the full write timeout and
	// stalls a dispatcher worker per event.
	start := time.Now()
	err = m.Publish(context.Background(), Event{Kind: KindUplink, DeviceEUI: "0011223344556677"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Publish to a disconnected broker must return an error")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("err = %v, want it to say the broker is not connected", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Publish took %v against a dead broker; it must fail fast, not wait out the "+
			"publish timeout (this is the paho IsConnected() trap)", elapsed)
	}
}

// TestMQTTPublishHonoursContextDeadline: shutdown cancels the dispatcher's
// context, and a sink that ignores it holds the process open.
func TestMQTTPublishHonoursContextDeadline(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{
		BrokerURL:      "tcp://127.0.0.1:1",
		PublishTimeout: 5 * time.Second,
		ConnectTimeout: 50 * time.Millisecond,
		Logger:         quietMQTT(),
	})
	if err != nil {
		t.Fatalf("NewMQTT: %v", err)
	}
	defer m.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := m.Publish(ctx, Event{Kind: KindUplink}); err == nil {
		t.Fatal("Publish with an expired context must fail")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Publish took %v; it should honour the context, not the 5s timeout", elapsed)
	}
}

func TestMQTTClientID(t *testing.T) {
	if got := mqttClientID(" plant-ns-1 "); got != "plant-ns-1" {
		t.Errorf("configured ID = %q, want it trimmed", got)
	}
	got := mqttClientID("")
	if !strings.HasPrefix(got, "fieldnode-ns@") {
		t.Errorf("derived ID = %q, want the fieldnode-ns@ prefix", got)
	}
	if got == "fieldnode-ns@" {
		t.Error("a derived ID with an empty host is useless in a broker log")
	}
}

// TestSinksShareOneEventBody is the guarantee marshalEvent exists for: an MQTT
// subscriber and a webhook receiver parse the same object.
func TestSinksShareOneEventBody(t *testing.T) {
	ev := Event{
		Kind: KindUplink, DeviceEUI: "0011223344556677", DevAddr: "01020304",
		ReceivedAt: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
		FPort:      10, Data: "3q2+7w==",
		Decoded: map[string]any{"temperature": 21.5},
	}
	body, err := marshalEvent(ev)
	if err != nil {
		t.Fatalf("marshalEvent: %v", err)
	}
	var back Event
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("the body does not round-trip: %v", err)
	}
	if back.Kind != ev.Kind || back.DeviceEUI != ev.DeviceEUI || back.FPort != ev.FPort ||
		back.Data != ev.Data || back.Decoded["temperature"] != 21.5 {
		t.Errorf("round trip lost data: %+v", back)
	}
	// Field names are the public contract; pinning a few catches a rename.
	for _, want := range []string{`"dev_eui"`, `"received_at"`, `"decoded"`, `"f_port"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the event JSON no longer carries %s: %s", want, body)
		}
	}
}
