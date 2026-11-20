package engine

import (
	"context"
	"sort"
	"sync"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// DefaultEventHistory is how many events MemStore keeps.
const DefaultEventHistory = 1000

// MemStore is an in-memory Store implementation. It is safe for concurrent use
// and backs the tests, the nsdemo binary and single-node evaluation. Nothing
// survives a restart; production uses internal/pgstore.
//
// Values are deep-copied on both read and write, so a handler cannot mutate
// stored state without going through SaveSession — the same discipline the
// Postgres store enforces by nature.
type MemStore struct {
	mu        sync.RWMutex
	devices   map[lorawan.EUI64]*Device
	sessions  map[lorawan.EUI64]*Session
	models    map[string]*DeviceModel
	uplinks   []UplinkRecord
	downlinks []DownlinkRecord
	events    []Event
	maxEvents int
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		devices:   map[lorawan.EUI64]*Device{},
		sessions:  map[lorawan.EUI64]*Session{},
		models:    map[string]*DeviceModel{},
		maxEvents: DefaultEventHistory,
	}
}

// Store wires the MemStore as the engine's Store value.
func (m *MemStore) Store() Store {
	return Store{Devices: m, Sessions: m, Recorder: m, Models: m}
}

// DeviceByDevEUI implements DeviceRepo.
func (m *MemStore) DeviceByDevEUI(_ context.Context, devEUI lorawan.EUI64) (*Device, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.devices[devEUI]
	if !ok {
		return nil, ErrNotFound
	}
	return d.clone(), nil
}

// SaveDevice implements DeviceRepo.
func (m *MemStore) SaveDevice(_ context.Context, d *Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[d.DevEUI] = d.clone()
	return nil
}

// ListDevices implements DeviceRepo. The result is ordered by DevEUI so the
// provisioning API returns a stable list without the caller having to sort it.
func (m *MemStore) ListDevices(_ context.Context) ([]*Device, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*Device, 0, len(m.devices))
	for _, d := range m.devices {
		out = append(out, d.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DevEUI.String() < out[j].DevEUI.String() })
	return out, nil
}

// DeleteDevice implements DeviceRepo, dropping the session with the device for
// the same reason the Postgres store does: a revoked device must not keep an
// active session.
func (m *MemStore) DeleteDevice(_ context.Context, devEUI lorawan.EUI64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.devices[devEUI]; !ok {
		return ErrNotFound
	}
	delete(m.devices, devEUI)
	delete(m.sessions, devEUI)
	return nil
}

// SessionByDevEUI implements SessionRepo.
func (m *MemStore) SessionByDevEUI(_ context.Context, devEUI lorawan.EUI64) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[devEUI]
	if !ok {
		return nil, ErrNotFound
	}
	return s.clone(), nil
}

// SessionByDevAddr implements SessionRepo.
func (m *MemStore) SessionByDevAddr(_ context.Context, devAddr lorawan.DevAddr) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if s.DevAddr == devAddr {
			return s.clone(), nil
		}
	}
	return nil, ErrNotFound
}

// SaveSession implements SessionRepo.
func (m *MemStore) SaveSession(_ context.Context, s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.DevEUI] = s.clone()
	return nil
}

// DeleteSession implements SessionRepo.
func (m *MemStore) DeleteSession(_ context.Context, devEUI lorawan.EUI64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, devEUI)
	return nil
}

// RecordUplink implements Recorder.
func (m *MemStore) RecordUplink(_ context.Context, r UplinkRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uplinks = append(m.uplinks, r)
	return nil
}

// RecordDownlink implements Recorder.
func (m *MemStore) RecordDownlink(_ context.Context, r DownlinkRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.downlinks = append(m.downlinks, r)
	return nil
}

// RecordEvent implements Recorder, keeping the most recent maxEvents entries.
func (m *MemStore) RecordEvent(_ context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	if len(m.events) > m.maxEvents {
		drop := len(m.events) - m.maxEvents
		m.events = append(m.events[:0], m.events[drop:]...)
	}
	return nil
}

// Uplinks returns a snapshot of recorded uplinks.
func (m *MemStore) Uplinks() []UplinkRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]UplinkRecord(nil), m.uplinks...)
}

// Downlinks returns a snapshot of recorded downlinks.
func (m *MemStore) Downlinks() []DownlinkRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]DownlinkRecord(nil), m.downlinks...)
}

// Events returns a snapshot of recorded events.
func (m *MemStore) Events() []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Event(nil), m.events...)
}

// EventsOfKind returns the recorded events of one kind.
func (m *MemStore) EventsOfKind(kind string) []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Event
	for _, e := range m.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// EventsWithReason returns the recorded events carrying a given reason.
func (m *MemStore) EventsWithReason(reason string) []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Event
	for _, e := range m.events {
		if e.Reason == reason {
			out = append(out, e)
		}
	}
	return out
}

// DeviceCount returns how many devices are provisioned.
func (m *MemStore) DeviceCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.devices)
}

// clone helpers -------------------------------------------------------------

func (d *Device) clone() *Device {
	if d == nil {
		return nil
	}
	c := *d
	if d.UsedDevNonces != nil {
		nonces := make(map[lorawan.EUI64][]uint16, len(d.UsedDevNonces))
		for joinEUI, list := range d.UsedDevNonces {
			nonces[joinEUI] = append([]uint16(nil), list...)
		}
		c.UsedDevNonces = nonces
	}
	return &c
}

func (s *Session) clone() *Session {
	if s == nil {
		return nil
	}
	c := *s
	if s.Pending != nil {
		p := *s.Pending
		p.FRMPayload = append([]byte(nil), s.Pending.FRMPayload...)
		p.MACCommands = cloneMACSet(s.Pending.MACCommands)
		c.Pending = &p
	}
	if s.PendingADR != nil {
		adr := *s.PendingADR
		c.PendingADR = &adr
	}
	if s.PendingRX2 != nil {
		rx2 := *s.PendingRX2
		c.PendingRX2 = &rx2
	}
	return &c
}
