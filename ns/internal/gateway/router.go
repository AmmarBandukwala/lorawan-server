package gateway

// Routing across more than one gateway protocol. See the package comment in
// frame.go for what the transport boundary is.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Router sends each downlink through whichever transport holds the gateway.
//
// Why this is needed rather than one transport per process: the same deployment
// routinely has both kinds of gateway — a few UDP forwarders already installed
// and a station that can only speak WebSocket — and the engine must not care.
// The device's session names a gateway, and the gateway's transport is a fact
// about the field, not about the device.
//
// Selection prefers a transport that claims the gateway. If none claims it, every
// transport is tried in registration order and the errors are joined, because
// "no transport knows this gateway" is a much more useful message when it lists
// what each one said.
type Router struct {
	mu         sync.RWMutex
	transports []namedTransport
}

type namedTransport struct {
	name string
	gw   Gateway
}

// NewRouter builds a Router with the given transports, in the order they are
// tried when no transport claims a gateway.
func NewRouter(transports ...NamedTransport) *Router {
	r := &Router{}
	for _, t := range transports {
		r.Add(t.Name, t.Gateway)
	}
	return r
}

// NamedTransport pairs a transport with the name it is reported under.
type NamedTransport struct {
	Name    string
	Gateway Gateway
}

// Add registers a transport. Registering the same name twice replaces it, which
// is what a test that swaps a fake transport in needs.
func (r *Router) Add(name string, gw Gateway) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, t := range r.transports {
		if t.name == name {
			r.transports[i].gw = gw
			return
		}
	}
	r.transports = append(r.transports, namedTransport{name: name, gw: gw})
}

// SendDownlink routes one downlink.
func (r *Router) SendDownlink(ctx context.Context, f DownlinkFrame) error {
	r.mu.RLock()
	transports := append([]namedTransport(nil), r.transports...)
	r.mu.RUnlock()

	if len(transports) == 0 {
		return errors.New("gateway: no transport is configured")
	}

	// Pass 1: whoever claims the gateway.
	for _, t := range transports {
		if reachable(t.gw, f.GatewayID) {
			if err := t.gw.SendDownlink(ctx, f); err != nil {
				return fmt.Errorf("gateway: send via %s: %w", t.name, err)
			}
			return nil
		}
	}

	// Pass 2: nobody claims it. Try each in turn. A transport that answers "not
	// connected" has told us something worth keeping, so the errors are joined
	// rather than the last one winning.
	var errs []error
	for _, t := range transports {
		if err := t.gw.SendDownlink(ctx, f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.name, err))
			continue
		}
		return nil
	}
	return fmt.Errorf("gateway: no transport could reach %s: %w",
		f.GatewayID, errors.Join(errs...))
}

// reachable reports whether a transport says it can route to a gateway. A
// transport that does not implement Reachable is treated as a possible route, so
// adding the interface never silently disables a transport.
func reachable(gw Gateway, id lorawan.EUI64) bool {
	r, ok := gw.(Reachable)
	return ok && r.CanReach(id)
}

// CanReach reports whether any registered transport claims the gateway.
func (r *Router) CanReach(id lorawan.EUI64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.transports {
		if reachable(t.gw, id) {
			return true
		}
	}
	return false
}

// Presence merges the gateway reports of every transport that provides one.
//
// A gateway EUI appearing on two transports is real — the same hardware can be
// configured to speak both, or be mid-migration between them — and both rows are
// kept rather than deduplicated, because which one is currently carrying traffic
// is exactly what an operator needs to see during that switch-over.
func (r *Router) Presence() []Presence {
	r.mu.RLock()
	transports := append([]namedTransport(nil), r.transports...)
	r.mu.RUnlock()

	var out []Presence
	for _, t := range transports {
		rep, ok := t.gw.(Reporter)
		if !ok {
			continue
		}
		out = append(out, rep.Presence()...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EUI.String() != out[j].EUI.String() {
			return out[i].EUI.String() < out[j].EUI.String()
		}
		return out[i].Transport < out[j].Transport
	})
	return out
}

// OnlineTimeouts returns each transport's online threshold, keyed by name. One
// number cannot serve both protocols: a UDP forwarder is judged by the gap
// between PULL_DATA packets, a station by the liveness of a held connection.
func (r *Router) OnlineTimeouts() map[string]time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]time.Duration{}
	for _, t := range r.transports {
		rep, ok := t.gw.(Reporter)
		if !ok {
			continue
		}
		out[t.name] = rep.OnlineTimeout()
	}
	return out
}

// Names lists the registered transports, for the status endpoint.
func (r *Router) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.transports))
	for _, t := range r.transports {
		out = append(out, t.name)
	}
	return out
}

// The engine needs a Gateway when it is built, and the transport needs the
// engine as its uplink handler, so one of the two must be wired in afterwards.
// Wiring it through Lazy keeps that cycle out of both constructors and makes
// the "no transport yet" case an explicit error instead of a nil dereference.
// Lazy defers to a Gateway installed after construction.
//
// The engine needs a Gateway when it is built, and each transport needs the
// engine as its uplink handler, so one of the two must be wired in afterwards.
// Wiring it through Lazy keeps that cycle out of both constructors and makes the
// "no transport yet" case an explicit error instead of a nil dereference. In
// practice the value installed is a Router, so the engine sees one transmitter
// however many protocols the deployment speaks.
// Lazy defers to whichever transport is installed after construction.
type Lazy struct {
	mu sync.RWMutex
	gw Gateway
}

// Set installs (or replaces, for tests) the transport.
func (l *Lazy) Set(gw Gateway) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gw = gw
}

// SendDownlink forwards to the installed transport.
func (l *Lazy) SendDownlink(ctx context.Context, f DownlinkFrame) error {
	l.mu.RLock()
	gw := l.gw
	l.mu.RUnlock()
	if gw == nil {
		return errors.New("gateway: no transport installed yet")
	}
	return gw.SendDownlink(ctx, f)
}
