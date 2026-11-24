package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Webhook delivery defaults, applied when the corresponding WebhookConfig field
// is left at its zero value.
const (
	// DefaultWebhookTimeout bounds one attempt. It has to be long enough for a
	// receiver that does real work and short enough that a dead endpoint cannot
	// hold a delivery worker for long.
	DefaultWebhookTimeout = 5 * time.Second
	// DefaultWebhookAttempts counts the first try, so this is two retries:
	// enough to ride out a restart of the receiver, few enough that one slow
	// endpoint cannot stall the queue behind it.
	DefaultWebhookAttempts = 3
	// DefaultWebhookBackoff is the first wait between attempts, doubling after
	// each one.
	DefaultWebhookBackoff = time.Second
)

// WebhookConfig configures an HTTP sink.
type WebhookConfig struct {
	// URL is where events are POSTed, as JSON.
	URL string

	// Token, when set, is sent as "Authorization: Bearer <token>" so the
	// receiver can reject events from anything else on the network. A webhook
	// endpoint that is reachable from a LAN and accepts unauthenticated events
	// is an invitation to forge device traffic.
	Token string

	// Timeout bounds one attempt. The zero value means DefaultWebhookTimeout.
	Timeout time.Duration

	// Attempts is how many times one event is tried, including the first. The
	// zero value means DefaultWebhookAttempts. Retries happen inside Publish, so
	// they hold a worker and delay what is behind it — which is the tradeoff for
	// not losing events across a short outage.
	Attempts int

	// Backoff is the wait between attempts, doubling each time. Zero means
	// DefaultWebhookBackoff.
	Backoff time.Duration

	// Client is used when set. Injecting it lets a deployment point the webhook
	// at a service with a private CA, and lets the tests use an httptest server
	// without a real listener.
	Client *http.Client
}

// Webhook is an HTTP JSON sink.
type Webhook struct {
	url      string
	token    string
	timeout  time.Duration
	attempts int
	backoff  time.Duration
	client   *http.Client
	// host is what Name reports: the full URL repeats in every log line and
	// carries query strings that may hold a token.
	host string
}

// NewWebhook validates the configuration.
func NewWebhook(cfg WebhookConfig) (*Webhook, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("integration: webhook url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("integration: webhook url %q must be http or https", cfg.URL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("integration: webhook url %q has no host", cfg.URL)
	}
	w := &Webhook{
		url:      cfg.URL,
		token:    cfg.Token,
		timeout:  cfg.Timeout,
		attempts: cfg.Attempts,
		backoff:  cfg.Backoff,
		host:     u.Host,
	}
	if w.timeout <= 0 {
		w.timeout = DefaultWebhookTimeout
	}
	if w.attempts <= 0 {
		w.attempts = DefaultWebhookAttempts
	}
	if w.backoff <= 0 {
		w.backoff = DefaultWebhookBackoff
	}
	w.client = cfg.Client
	if w.client == nil {
		// No timeout on the client itself: each attempt gets its own deadline
		// from the context, which is what makes a shutdown interrupt a delivery
		// in progress instead of waiting out a fixed client timeout.
		w.client = &http.Client{}
	}
	return w, nil
}

// Name implements Sink.
func (w *Webhook) Name() string { return "webhook:" + w.host }

// Publish POSTs the event, retrying on transport errors and on server-side
// status codes.
//
// 4xx responses are not retried. A 404 or 415 means the receiver does not want
// this event, and hammering it three times per uplink turns a configuration
// mistake into a load problem on somebody else's service.
func (w *Webhook) Publish(ctx context.Context, ev Event) error {
	body, err := marshalEvent(ev)
	if err != nil {
		// Not transient: an event this process cannot encode will never encode.
		return fmt.Errorf("integration: encode %s event: %w", ev.Kind, err)
	}

	var lastErr error
	delay := w.backoff
	for attempt := 1; attempt <= w.attempts; attempt++ {
		if attempt > 1 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return fmt.Errorf("integration: webhook %s cancelled while retrying: %w", w.host, ctx.Err())
			}
			delay *= 2
		}
		retry, err := w.attempt(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry {
			return err
		}
	}
	return fmt.Errorf("integration: webhook %s after %d attempt(s): %w", w.host, w.attempts, lastErr)
}

// Test posts a probe event, which is what the panel's test button needs.
//
// The probe carries a distinct kind so a consumer can filter test traffic out.
// A 4xx is reported here as a failure even though Publish deliberately does not
// retry one: for delivery a 404 means "nothing dropped, the receiver does not
// want this", but for a test the operator needs to hear that the endpoint is
// wrong.
func (w *Webhook) Test(ctx context.Context) error {
	return w.Publish(ctx, Event{
		Kind:       KindIntegrationTest,
		DeviceEUI:  "0000000000000000",
		ReceivedAt: time.Now().UTC(),
		Data:       encodeBase64([]byte("fieldnode integration test")),
	})
}

// attempt performs one POST and reports whether the failure is worth retrying.
func (w *Webhook) attempt(ctx context.Context, body []byte) (retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("integration: build the webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fieldnode-ns/1.0")
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		// Transport-level failure: connection refused, DNS, deadline. All worth
		// another try.
		return true, fmt.Errorf("integration: post to %s: %w", w.host, err)
	}
	defer func() {
		// Draining and closing keeps the connection reusable; without this every
		// attempt opens a new TCP connection to the same endpoint.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return false, nil
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return true, fmt.Errorf("integration: %s returned %s", w.host, resp.Status)
	}
	return false, fmt.Errorf("integration: %s rejected the event with %s", w.host, resp.Status)
}

var _ Sink = (*Webhook)(nil)
