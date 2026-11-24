package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/api/internal/assistant"
	"github.com/fieldnode/lorawan-server/api/internal/llm"
)

// The keepalive. A local model leaves the stream silent for the better part of a
// minute while it loads and prefills, and a proxy with an idle timeout closes a
// connection that has said nothing for 60 seconds. The page then shows an answer that
// simply stopped, with no error to explain it.

// slowAssistant holds the stream open without emitting anything, which is exactly the
// state a cold model puts it in.
type slowAssistant struct{ hold time.Duration }

func (a slowAssistant) Ask(ctx context.Context, _ []llm.Message, emit func(assistant.Event) error) error {
	select {
	case <-time.After(a.hold):
		return emit(assistant.Event{Type: assistant.EventToken, Text: "finally, an answer"})
	case <-ctx.Done():
		return ctx.Err()
	}
}

func askBody(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/ask",
		strings.NewReader(`{"question":"why is node 1 quiet"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Body.String()
}

func TestSilentStreamSendsKeepaliveComments(t *testing.T) {
	defer shortenKeepaliveInterval(t, 20*time.Millisecond)()

	body := askBody(t, askHandler(t, slowAssistant{hold: 150 * time.Millisecond}))

	if n := strings.Count(body, ": keepalive\n\n"); n == 0 {
		t.Errorf("a stream silent for 150ms at a 20ms interval sent no keepalive; body:\n%s", body)
	}
	// The comment must not displace the answer, and must not break the framing of the
	// frames around it: a page that cannot parse the stream is no better than a proxy
	// that closed it.
	got := frames(t, body)
	if len(got) == 0 {
		t.Fatalf("no events survived the keepalives; body:\n%s", body)
	}
	if !strings.Contains(body, "finally, an answer") {
		t.Error("the answer did not arrive after the keepalives")
	}
}

// A busy stream should not spam comments. Emitting one on every tick regardless of
// whether real events are flowing would double the traffic of a fast answer for no
// benefit, and would make the interval meaningless as a proxy-liveness signal.
func TestActiveStreamIsNotSpammingKeepalives(t *testing.T) {
	defer shortenKeepaliveInterval(t, 10*time.Millisecond)()

	// Events arrive faster than the interval, so the stream is never idle.
	busy := &stubAssistant{events: []assistant.Event{
		{Type: assistant.EventStatus, Text: "thinking"},
		{Type: assistant.EventToken, Text: "a"},
		{Type: assistant.EventToken, Text: "b"},
		{Type: assistant.EventToken, Text: "c"},
	}}
	if n := strings.Count(askBody(t, askHandler(t, busy)), ": keepalive"); n != 0 {
		t.Errorf("a stream that was never idle sent %d keepalives", n)
	}
}

// The ticker goroutine must be joined before the handler returns, because the
// ResponseWriter is not valid afterwards. Cancelling without waiting would let a
// keepalive race the return and write into a recycled buffer — a bug that shows up as
// corrupted frames under load and never in a quiet test.
func TestKeepaliveStopsWhenTheAnswerEnds(t *testing.T) {
	defer shortenKeepaliveInterval(t, 10*time.Millisecond)()

	for range 50 {
		body := askBody(t, askHandler(t, &stubAssistant{events: []assistant.Event{
			{Type: assistant.EventToken, Text: "done"},
		}}))
		if strings.HasSuffix(body, ": keepalive\n\n") {
			t.Fatal("a keepalive was written after the answer had finished")
		}
	}
}

// shortenKeepaliveInterval returns a restore func, so a test that forgets to put the
// production interval back cannot leak a 20ms ticker into the tests after it.
func shortenKeepaliveInterval(t *testing.T, d time.Duration) func() {
	t.Helper()
	previous := assistantKeepaliveInterval
	assistantKeepaliveInterval = d
	return func() { assistantKeepaliveInterval = previous }
}
