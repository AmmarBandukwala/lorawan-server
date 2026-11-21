package integration

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTT broker sink.
//
// The client comes from paho rather than being hand-rolled, for the same reason
// the Basics Station transport uses gorilla/websocket: reconnect, TLS and the
// remaining protocol corners are where a small hand-written client fails
// quietly in production. A WAN-facing broker is exactly that case.
//
// The contract matches the webhook sink, and it matters: a broker that is down
// must not stop the network server from starting, and must not stop an uplink
// from being stored. Construction therefore fails only on a configuration the
// operator got wrong — an unparseable URL, a topic containing a wildcard, a QoS
// the protocol forbids on publish. An unreachable broker is a runtime condition,
// counted and logged like a refused webhook.

const (
	// DefaultMQTTTopic is where events go when no topic is configured. It is a
	// single shared topic; per-device routing is available by templating
	// {dev_eui} into the configured topic.
	DefaultMQTTTopic = "fieldnode/events"

	defaultMQTTConnectTimeout  = 10 * time.Second
	defaultMQTTPublishTimeout  = 10 * time.Second
	defaultMQTTPingInterval    = 30 * time.Second
	defaultMQTTCleanSession    = true
	defaultMQTTMaxReconnectInt = 10 * time.Second
)

// MQTTConfig configures one broker destination.
type MQTTConfig struct {
	// BrokerURL is a scheme-qualified broker address: tcp://host:1883,
	// tls://host:8883, mqtts://host:8883, ws:// or wss://.
	BrokerURL string

	// Topic receives every event. {dev_eui} and {kind} are substituted, so
	// "fieldnode/{dev_eui}/{kind}" fans out per device. Substitution happens
	// only in the topic string, never in the payload.
	Topic string

	// ClientID identifies this network server to the broker. Empty lets the
	// broker assign one, which some brokers reject; a stable ID makes reconnect
	// logs readable, so NewMQTT derives one when unset.
	ClientID string

	Username string
	Password string

	// QoS is 0 or 1. QoS 2 is refused: it needs a four-way handshake and durable
	// state this process does not keep, and promising exactly-once delivery we
	// cannot honour would be worse than offering at-most-once or at-least-once.
	QoS byte

	// Retain makes the broker hold the last event per topic, so a subscriber
	// that connects later immediately sees the most recent reading. Off by
	// default: retained messages survive the publisher and surprise consumers.
	Retain bool

	// InsecureSkipVerify accepts a broker certificate that does not chain to a
	// trusted root. It exists for a self-hosted broker with a self-signed
	// certificate; it is logged loudly when enabled because it means the
	// connection is not authenticated.
	InsecureSkipVerify bool

	ConnectTimeout time.Duration
	PublishTimeout time.Duration

	Logger *slog.Logger
}

// MQTT publishes events to a broker topic. It implements Sink and io.Closer.
type MQTT struct {
	cfg   MQTTConfig
	tmpl  mqttTopic
	host  string
	log   *slog.Logger
	token mqtt.Client

	// connected is maintained from paho's OnConnect / OnConnectionLost
	// callbacks rather than read from Client.IsConnected().
	//
	// That is not defensive coding: with SetConnectRetry(true), paho reports
	// IsConnected() as true while it has never completed a TCP handshake. A
	// readiness check built on it therefore lets every publish wait out the full
	// write timeout, which on a dead broker means a dispatcher worker stalls per
	// event instead of failing fast and being counted.
	connected atomic.Bool
}

// mqttTopic is a parsed topic template. Splitting it once at construction keeps
// the per-event work to string replacement and turns a wildcard in the template
// into a startup error rather than a publish failure.
type mqttTopic struct {
	raw    string
	devEUI bool
	kind   bool
}

func parseMQTTTopic(raw string) (mqttTopic, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultMQTTTopic
	}
	// A publish topic may not contain a wildcard; only subscribers use them.
	if strings.ContainsAny(raw, "+#") {
		return mqttTopic{}, fmt.Errorf(
			"integration: MQTT topic %q contains a wildcard, which is only valid on subscribe", raw)
	}
	// Empty levels ("a//b") are rejected by many brokers.
	for _, level := range strings.Split(raw, "/") {
		if level == "" {
			return mqttTopic{}, fmt.Errorf("integration: MQTT topic %q has an empty level", raw)
		}
	}
	t := mqttTopic{raw: raw}
	if strings.Contains(raw, "{dev_eui}") {
		t.devEUI = true
	}
	if strings.Contains(raw, "{kind}") {
		t.kind = true
	}
	return t, nil
}

// render substitutes the identifiers. Values are sanitised rather than
// URL-escaped: a topic level cannot contain "/" or a wildcard, and a DevEUI
// that does would otherwise forge extra levels in the topic tree.
func (t mqttTopic) render(devEUI, kind string) string {
	out := t.raw
	if t.devEUI {
		out = strings.ReplaceAll(out, "{dev_eui}", sanitizeTopicLevel(devEUI))
	}
	if t.kind {
		out = strings.ReplaceAll(out, "{kind}", sanitizeTopicLevel(kind))
	}
	return out
}

func sanitizeTopicLevel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "-", "+", "-", "#", "-", " ", "-")
	return replacer.Replace(s)
}

// NewMQTT validates the configuration and connects.
//
// A broker that cannot be reached is not an error here: paho keeps retrying in
// the background, and Publish reports the gap. Failing construction would mean
// an operator who saved a not-yet-reachable broker could not start the network
// server at all.
func NewMQTT(cfg MQTTConfig) (*MQTT, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = defaultMQTTConnectTimeout
	}
	if cfg.PublishTimeout <= 0 {
		cfg.PublishTimeout = defaultMQTTPublishTimeout
	}
	if cfg.QoS > 1 {
		return nil, fmt.Errorf("integration: MQTT QoS must be 0 or 1, got %d", cfg.QoS)
	}

	raw := strings.TrimSpace(cfg.BrokerURL)
	parsedURL, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("integration: MQTT broker URL %q: %w", cfg.BrokerURL, err)
	}
	switch parsedURL.Scheme {
	case "tcp", "ssl", "tls", "mqtt", "mqtts", "ws", "wss":
	default:
		return nil, fmt.Errorf(
			"integration: MQTT broker scheme %q is not supported (use tcp, tls, mqtts, ws or wss)", parsedURL.Scheme)
	}
	if parsedURL.Host == "" {
		return nil, fmt.Errorf("integration: MQTT broker URL %q has no host", cfg.BrokerURL)
	}
	secured := parsedURL.Scheme == "tls" || parsedURL.Scheme == "ssl" || parsedURL.Scheme == "mqtts" || parsedURL.Scheme == "wss"
	if secured && cfg.InsecureSkipVerify {
		// Worth a line at every start: this connection is encrypted but not
		// authenticated, so it does not protect against an active attacker.
		cfg.Logger.Warn("MQTT integration skips broker certificate verification",
			"broker", parsedURL.Host, "why", "InsecureSkipVerify is set")
	}

	tmpl, err := parseMQTTTopic(cfg.Topic)
	if err != nil {
		return nil, err
	}

	opts := mqtt.NewClientOptions().
		AddBroker(raw).
		SetClientID(mqttClientID(cfg.ClientID)).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(cfg.ConnectTimeout).
		SetMaxReconnectInterval(defaultMQTTMaxReconnectInt).
		SetKeepAlive(defaultMQTTPingInterval).
		SetCleanSession(defaultMQTTCleanSession).
		SetOrderMatters(false).
		SetWriteTimeout(cfg.PublishTimeout)
	if secured {
		opts.SetTLSConfig(&tls.Config{
			MinVersion: tls.VersionTLS12,
			// Opt-in, logged at startup, and documented as unauthenticated.
			InsecureSkipVerify: cfg.InsecureSkipVerify,
		})
	}
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}

	publisher := &MQTT{
		cfg:  cfg,
		tmpl: tmpl,
		host: parsedURL.Host,
		log:  cfg.Logger.With("component", "integration", "sink", "mqtt", "broker", parsedURL.Host),
	}

	// Connection state is tracked here rather than asked of paho, whose
	// IsConnected() reports true while ConnectRetry is still trying. See the
	// note on MQTT.connected.
	opts.OnConnect = func(mqtt.Client) {
		publisher.connected.Store(true)
		publisher.log.Info("MQTT broker connected", "topic", tmpl.raw)
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		publisher.connected.Store(false)
		publisher.log.Warn("MQTT broker connection lost", "err", err)
	}
	// Reconnecting covers the case where a connection was dropped and a new one
	// is being attempted: OnConnectionLost already cleared the flag, but a
	// ConnectRetry that has never succeeded fires this without ever having been
	// connected, and clearing twice costs nothing.
	opts.OnReconnecting = func(mqtt.Client, *mqtt.ClientOptions) {
		publisher.connected.Store(false)
	}

	client := mqtt.NewClient(opts)
	publisher.token = client

	// Ask for the connection now so a reachable broker is warm before the first
	// event. ConnectRetry makes this return without error when the broker is
	// down, which is the behaviour described above.
	if token := client.Connect(); !token.WaitTimeout(cfg.ConnectTimeout) {
		publisher.log.Info("MQTT broker not reachable yet; retrying in the background",
			"timeout", cfg.ConnectTimeout)
	} else if err := token.Error(); err != nil {
		publisher.log.Warn("MQTT broker refused the connection; retrying in the background", "err", err)
	}
	return publisher, nil
}

// mqttClientID derives a descriptive client ID. A broker logs client IDs, so
// "ns" is not enough to tell two network servers apart.
func mqttClientID(configured string) string {
	if id := strings.TrimSpace(configured); id != "" {
		return id
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "fieldnode-ns"
	}
	return "fieldnode-ns@" + host
}

// Name identifies this sink in logs and on the status page.
func (m *MQTT) Name() string { return "mqtt:" + m.host }

// Broker is the configured address, for the integrations panel.
func (m *MQTT) Broker() string { return m.cfg.BrokerURL }

// Topic is the configured topic template.
func (m *MQTT) Topic() string { return m.tmpl.raw }

// Connected reports whether the broker connection is currently up, from the
// connection callbacks rather than from paho's own flag.
func (m *MQTT) Connected() bool { return m.connected.Load() }

// Publish sends one event as JSON.
//
// A disconnected broker is reported as an error rather than queued silently.
// paho holds QoS 1 messages across a reconnect but drops QoS 0 ones, and a sink
// that sometimes discards without saying so is worse than one that reports a
// failure the dispatcher can count.
func (m *MQTT) Publish(ctx context.Context, ev Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.token == nil || !m.connected.Load() {
		return fmt.Errorf("integration: MQTT broker %s is not connected", m.host)
	}

	body, err := marshalEvent(ev)
	if err != nil {
		return err
	}
	topic := m.tmpl.render(ev.DeviceEUI, ev.Kind)

	// Bounded by whichever deadline is sooner, so a shutdown that cancels the
	// context does not wait out the full publish timeout.
	budget := m.cfg.PublishTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return context.DeadlineExceeded
	}

	token := m.token.Publish(topic, m.cfg.QoS, m.cfg.Retain, body)
	if !token.WaitTimeout(budget) {
		return fmt.Errorf("integration: MQTT publish to %s did not complete within %s", topic, budget)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("integration: MQTT publish to %s: %w", topic, err)
	}
	return nil
}

// Test publishes a probe, which is what the panel's test button needs. The
// probe carries a distinct kind so a consumer can tell it apart from device
// traffic, and it is never retained: a retained test message would look like a
// real reading to anything subscribing afterwards.
func (m *MQTT) Test(ctx context.Context) error {
	return m.Publish(ctx, Event{
		Kind:       KindIntegrationTest,
		DeviceEUI:  "0000000000000000",
		ReceivedAt: time.Now().UTC(),
		Data:       encodeBase64([]byte("fieldnode integration test")),
	})
}

// Close disconnects. paho's Disconnect is not context-aware, so it is asked to
// be brief and returns promptly; in-flight publishes were already bounded by the
// dispatcher's shutdown.
func (m *MQTT) Close() error {
	m.connected.Store(false)
	if m.token != nil {
		m.token.Disconnect(250)
		m.token = nil
	}
	return nil
}

var (
	_ Sink                       = (*MQTT)(nil)
	_ interface{ Close() error } = (*MQTT)(nil)
)
