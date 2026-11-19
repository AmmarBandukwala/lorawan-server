package testenv

import (
	"sync"
	"time"
)

// Clock is time a test controls. The engine only ever reads it through the
// injected function, so nothing drifts when a machine is slow.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// Now implements the engine's clock injection.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves time forward, which is how a test expires the de-duplication
// window or reaches a scheduled RX window.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
