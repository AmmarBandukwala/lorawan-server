package testenv

import (
	"context"
	"sync"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
)

// Gateway records downlinks instead of transmitting them. Paired with
// engine.WithScheduler (which runs scheduled work inline), a test can assert on
// the exact frames the engine scheduled without sleeping.
type Gateway struct {
	mu   sync.Mutex
	sent []gateway.DownlinkFrame
	// err makes the transport fail, to test what the engine does when a
	// gateway refuses a frame.
	err error
}

// SendDownlink implements gateway.Gateway.
func (g *Gateway) SendDownlink(_ context.Context, f gateway.DownlinkFrame) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	g.sent = append(g.sent, f)
	return nil
}

// Frames returns everything transmitted so far, oldest first.
func (g *Gateway) Frames() []gateway.DownlinkFrame {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gateway.DownlinkFrame(nil), g.sent...)
}

// LastFrame is the most recent transmission, or nil.
func (g *Gateway) LastFrame() *gateway.DownlinkFrame {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sent) == 0 {
		return nil
	}
	f := g.sent[len(g.sent)-1]
	return &f
}

// Reset forgets the recorded frames, so a test can assert on what one uplink
// caused rather than on a growing history.
func (g *Gateway) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = nil
}

// FailWith makes every later transmission return err. Pass nil to recover.
func (g *Gateway) FailWith(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err = err
}
