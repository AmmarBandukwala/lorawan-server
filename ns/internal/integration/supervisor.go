package integration

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Supervisor turns stored destinations into the dispatcher's live sink set.
//
// It exists so that saving an integration takes effect without a restart while
// the radio path never touches the database: the operator's write triggers one
// Reload, the supervisor rebuilds the destination list, and the dispatcher
// swaps it atomically.
//
// Sinks are reused when their configuration is unchanged. That is not a micro-
// optimisation. Rebuilding every sink on every edit would drop and re-establish
// every MQTT connection because somebody renamed an unrelated webhook, and a
// broker sees that as a flapping client.
type Supervisor struct {
	// load reads the stored destinations. A function rather than an interface
	// because the only implementation is a method on the store, and the tests
	// want to hand back a slice.
	load func(ctx context.Context) ([]Destination, error)

	// apply installs the new sink set. This is Dispatcher.SetSinks.
	apply func([]Sink)

	// envSinks come from NS_WEBHOOK_URLS. They are always installed and never
	// removed, because ignoring a destination an operator already configured
	// would be a silent behaviour change; the panel shows them as read-only
	// instead.
	envSinks []Sink

	log *slog.Logger

	mu      sync.Mutex
	live    map[string]liveSink
	reports map[string]Report
}

type liveSink struct {
	fingerprint string
	sink        Sink
}

// Report is what the supervisor knows about one destination after a reload. The
// panel shows it, which is the difference between "saved" and "working".
type Report struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	// Error is why a destination could not be built at all. A destination that
	// built but cannot reach its endpoint is not an error here; it shows up as
	// Connected false plus the dispatcher's failure counters.
	Error string `json:"error,omitempty"`
}

// NewSupervisor wires a destination source to a sink installer.
func NewSupervisor(load func(context.Context) ([]Destination, error),
	apply func([]Sink), envSinks []Sink, log *slog.Logger) *Supervisor {
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{
		load:     load,
		apply:    apply,
		envSinks: envSinks,
		log:      log.With("component", "integrations"),
		live:     map[string]liveSink{},
		reports:  map[string]Report{},
	}
}

// Reload rebuilds the sink set from storage.
//
// A destination that fails to build is skipped and reported rather than aborting
// the reload: one bad row must not stop every other integration from delivering.
func (s *Supervisor) Reload(ctx context.Context) error {
	destinations, err := s.load(ctx)
	if err != nil {
		return fmt.Errorf("integration: load destinations: %w", err)
	}
	sort.Slice(destinations, func(i, j int) bool { return destinations[i].ID < destinations[j].ID })

	s.mu.Lock()
	defer s.mu.Unlock()

	next := make([]Sink, 0, len(destinations)+len(s.envSinks))
	nextLive := map[string]liveSink{}
	reports := map[string]Report{}

	for _, destination := range destinations {
		report := Report{ID: destination.ID, Kind: destination.Kind, Name: destination.Name, Enabled: destination.Enabled}

		// Disabled keeps its entry so the panel can still show the destination
		// exists, but holds no connection: switching a broker off should stop the
		// network server dialling it.
		if !destination.Enabled {
			reports[destination.ID] = report
			continue
		}

		fp := destination.Fingerprint()
		if prev, ok := s.live[destination.ID]; ok && prev.fingerprint == fp {
			next = append(next, prev.sink)
			nextLive[destination.ID] = prev
			report.Connected = connectedOf(prev.sink)
			reports[destination.ID] = report
			continue
		}

		sink, err := destination.BuildSink(s.log)
		if err != nil {
			// Validation should have refused this at save time; arriving here
			// means a row was written by hand or by an older build.
			report.Error = err.Error()
			reports[destination.ID] = report
			s.log.Error("cannot build a configured integration", "id", destination.ID, "err", err)
			continue
		}
		next = append(next, sink)
		nextLive[destination.ID] = liveSink{fingerprint: fp, sink: sink}
		report.Connected = connectedOf(sink)
		reports[destination.ID] = report
	}

	// Anything stored but no longer present is dropped by not appearing here,
	// and SetSinks closes what it removes.
	s.live = nextLive
	s.reports = reports

	// Env sinks go last: configured first, and the ones an operator is least
	// likely to be watching.
	s.apply(append(next, s.envSinks...))
	return nil
}

// Reports returns what is known about each destination, ordered by id.
func (s *Supervisor) Reports() []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Report, 0, len(s.reports))
	for _, r := range s.reports {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EnvironmentSinks names the destinations that came from NS_WEBHOOK_URLS rather
// than from a stored row. The panel shows them as read-only: there is nothing
// behind them to edit, and leaving them out of the list would make the page
// disagree with what is actually delivering.
func (s *Supervisor) EnvironmentSinks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.envSinks))
	for _, sk := range s.envSinks {
		out = append(out, sk.Name())
	}
	return out
}

// connectedOf asks a sink whether its endpoint is reachable right now. Only
// sinks that can say so answer: a webhook has no persistent connection, and
// reporting one as connected would be a guess dressed up as a status.
func connectedOf(sk Sink) bool {
	if c, ok := sk.(interface{ Connected() bool }); ok {
		return c.Connected()
	}
	return false
}

// Fingerprint is everything that changes what this destination delivers or how.
// Name is excluded deliberately: renaming must not reconnect a broker.
//
// The secret is included by value, so rotating a token does rebuild the sink.
func (d Destination) Fingerprint() string {
	parts := []string{
		d.Kind,
		d.Webhook.URL,
		d.MQTT.BrokerURL,
		d.MQTT.Topic,
		d.MQTT.ClientID,
		d.MQTT.Username,
		strconv.Itoa(d.MQTT.QoS),
		strconv.FormatBool(d.MQTT.Retain),
		strconv.FormatBool(d.MQTT.InsecureSkipVerify),
		d.Secret,
	}
	return strings.Join(parts, "|")
}
