package integration

import (
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
)

// Destination is one configured place events are pushed to.
//
// It is the stored form of a sink: what the operator typed, persisted and
// validated, as opposed to the live Webhook or MQTT client built from it.
// Keeping the two apart is what lets the panel save a broker that is currently
// unreachable (a real destination) while refusing an address that could never
// work (an operator's typo), and it is why Validate exists as a separate step
// rather than being a side effect of construction.
type Destination struct {
	// ID is a slug the operator types and pastes into tickets, matching the
	// device-model convention rather than an opaque uuid.
	ID   string
	Kind string
	Name string

	// Enabled lets a destination be switched off without deleting it, which is
	// what an operator wants when a downstream system goes in for maintenance:
	// the configuration survives, and the panel still shows it exists.
	Enabled bool

	// Config holds the kind-specific fields. It is a typed struct rather than a
	// free-form map so a saved destination cannot contain a key nothing reads,
	// which is how an integration silently stops working after a rename.
	Webhook WebhookDestination
	MQTT    MQTTDestination

	// Secret is the webhook bearer token or the MQTT password. It is write-only
	// through the API: reads report whether one is set, never what it is.
	Secret string
}

// WebhookDestination is the webhook kind's configuration.
//
// The tags define the stored JSON shape, and the read path rejects unknown keys,
// so they are a persistence contract: renaming a field without a tag is how a
// working row becomes an unreadable one.
type WebhookDestination struct {
	URL string `json:"url"`
}

// MQTTDestination is the MQTT kind's configuration. The password lives in
// Destination.Secret, not here, so it follows the same write-only path as the
// webhook token instead of being one more string in a JSON blob.
type MQTTDestination struct {
	BrokerURL string `json:"broker_url"`
	Topic     string `json:"topic,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	Username  string `json:"username,omitempty"`
	QoS       int    `json:"qos,omitempty"`
	Retain    bool   `json:"retain,omitempty"`
	// InsecureSkipVerify accepts an untrusted broker certificate. Encrypted but
	// unauthenticated, so the panel has to say so out loud.
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty"`
}

// The two destination kinds. Named so the API, the store and the panel agree on
// the spelling rather than each carrying their own literal.
const (
	KindWebhook = "webhook"
	KindMQTT    = "mqtt"
)

// idPattern is the destination id shape, identical to the device-model slug rule
// so an operator who knows one knows the other. Anchored, and the store repeats
// it as a CHECK so a row inserted by hand cannot bypass it.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

// Validate reports what is wrong with this destination, or nil.
//
// It checks what could never work, not whether the remote end is currently
// reachable: a broker that is down is a runtime condition worth counting, while
// an address with no host is a typo worth refusing at save time. The distinction
// is the same one NewMQTT draws, and for the same reason — refusing the first
// would mean an operator cannot save a destination before the service behind it
// has been started.
func (d Destination) Validate() error {
	if !idPattern.MatchString(d.ID) {
		return fmt.Errorf(
			"integration: id %q must be 2-64 characters of a-z, 0-9, '_' or '-', starting with a letter or digit",
			d.ID)
	}
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("integration: destination %q needs a name", d.ID)
	}

	switch d.Kind {
	case KindWebhook:
		return d.validateWebhook()
	case KindMQTT:
		return d.validateMQTT()
	default:
		return fmt.Errorf("integration: destination %q has unknown kind %q (want %q or %q)",
			d.ID, d.Kind, KindWebhook, KindMQTT)
	}
}

func (d Destination) validateWebhook() error {
	raw := strings.TrimSpace(d.Webhook.URL)
	if raw == "" {
		return fmt.Errorf("integration: webhook %q needs a URL", d.ID)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("integration: webhook %q URL %q: %w", d.ID, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("integration: webhook %q URL must be http or https, got %q", d.ID, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("integration: webhook %q URL %q has no host", d.ID, raw)
	}
	return nil
}

func (d Destination) validateMQTT() error {
	raw := strings.TrimSpace(d.MQTT.BrokerURL)
	if raw == "" {
		return fmt.Errorf("integration: MQTT destination %q needs a broker URL", d.ID)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("integration: MQTT destination %q broker URL %q: %w", d.ID, raw, err)
	}
	switch u.Scheme {
	case "tcp", "ssl", "tls", "mqtt", "mqtts", "ws", "wss":
	default:
		return fmt.Errorf(
			"integration: MQTT destination %q scheme %q is not supported (use tcp, tls, mqtts, ws or wss)",
			d.ID, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("integration: MQTT destination %q broker URL %q has no host", d.ID, raw)
	}
	if d.MQTT.QoS < 0 || d.MQTT.QoS > 1 {
		return fmt.Errorf("integration: MQTT destination %q QoS must be 0 or 1, got %d",
			d.ID, d.MQTT.QoS)
	}
	if _, err := parseMQTTTopic(d.MQTT.Topic); err != nil {
		return fmt.Errorf("integration: MQTT destination %q: %w", d.ID, err)
	}
	return nil
}

// BuildSink constructs the live destination. A returned error means the
// configuration is invalid, not that the remote end is down.
func (d Destination) BuildSink(log *slog.Logger) (Sink, error) {
	if log == nil {
		log = slog.Default()
	}
	switch d.Kind {
	case KindWebhook:
		// No logger on WebhookConfig: it logs through the dispatcher, which
		// already names the sink.
		return NewWebhook(WebhookConfig{
			URL:   strings.TrimSpace(d.Webhook.URL),
			Token: d.Secret,
		})
	case KindMQTT:
		return NewMQTT(MQTTConfig{
			BrokerURL:          strings.TrimSpace(d.MQTT.BrokerURL),
			Topic:              strings.TrimSpace(d.MQTT.Topic),
			ClientID:           strings.TrimSpace(d.MQTT.ClientID),
			Username:           d.MQTT.Username,
			Password:           d.Secret,
			QoS:                byte(d.MQTT.QoS),
			Retain:             d.MQTT.Retain,
			InsecureSkipVerify: d.MQTT.InsecureSkipVerify,
			Logger:             log,
		})
	default:
		return nil, fmt.Errorf("integration: unknown destination kind %q", d.Kind)
	}
}
