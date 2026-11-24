package semtech

import (
	"net"
	"sort"
	"sync"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// GatewayRecord is what the server knows about a gateway it has heard from:
// presence and radio health, as opposed to the provisioning data in the store.
// The Fieldnode dashboard's gateway view reads this through Server.Gateways.
type GatewayRecord struct {
	EUI             lorawan.EUI64
	Addr            string
	ProtocolVersion byte
	LastSeen        time.Time
	Packets         uint64
	LastStats       StationStats
	StatsAt         time.Time

	// DownlinksSent and DownlinkErrors count transmission attempts, so a gateway
	// that refuses frames (TOO_LATE, TX_COL) is visible instead of silently
	// dropping downlinks.
	DownlinksSent  uint64
	DownlinkErrors uint64
	// LastTxAckError is the concentrator's own words about the last failure.
	LastTxAckError string
}

// Online reports whether the gateway has been heard from recently. Gateways
// send PULL_DATA at a fixed interval, so a missed couple of intervals means the
// forwarder or the link is down.
func (g GatewayRecord) Online(now time.Time, timeout time.Duration) bool {
	return !g.LastSeen.IsZero() && now.Sub(g.LastSeen) <= timeout
}

// registry tracks gateways. It is safe for concurrent use, and every read
// method returns a copy so callers never hold a live pointer into the map.
type registry struct {
	mu      sync.Mutex
	byEUI   map[lorawan.EUI64]*GatewayRecord
	timeout time.Duration
	now     func() time.Time
}

func newRegistry(timeout time.Duration, now func() time.Time) *registry {
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &registry{byEUI: map[lorawan.EUI64]*GatewayRecord{}, timeout: timeout, now: now}
}

// touch records a packet received from a gateway and returns the updated copy.
// The address is overwritten on every packet: a forwarder behind NAT gets a new
// mapping whenever it restarts, and downlinks must follow it.
func (r *registry) touch(eui lorawan.EUI64, addr *net.UDPAddr, version byte) GatewayRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.byEUI[eui]
	if !ok {
		rec = &GatewayRecord{EUI: eui}
		r.byEUI[eui] = rec
	}
	rec.Addr = addr.String()
	rec.ProtocolVersion = version
	rec.LastSeen = r.now()
	rec.Packets++
	return *rec
}

func (r *registry) lookup(eui lorawan.EUI64) (GatewayRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byEUI[eui]
	if !ok {
		return GatewayRecord{}, false
	}
	return *rec, true
}

// noteStats stores the periodic stat object with the time it arrived.
func (r *registry) noteStats(eui lorawan.EUI64, stats StationStats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.byEUI[eui]; ok {
		rec.LastStats = stats
		rec.StatsAt = r.now()
	}
}

// noteDownlinkSent counts a PULL_RESP that left the socket. Whether the
// concentrator actually transmitted it is reported later by TX_ACK.
func (r *registry) noteDownlinkSent(eui lorawan.EUI64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byEUI[eui]
	if !ok {
		rec = &GatewayRecord{EUI: eui}
		r.byEUI[eui] = rec
	}
	rec.DownlinksSent++
}

// noteTxAck records the concentrator's verdict on a transmission. An empty
// txErr clears the last error, so a gateway that recovers stops looking broken.
func (r *registry) noteTxAck(eui lorawan.EUI64, txErr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byEUI[eui]
	if !ok {
		rec = &GatewayRecord{EUI: eui}
		r.byEUI[eui] = rec
	}
	if txErr == "" {
		rec.LastTxAckError = ""
		return
	}
	rec.DownlinkErrors++
	rec.LastTxAckError = txErr
}

// list returns every gateway, ordered so logs and the dashboard are stable.
func (r *registry) list() []GatewayRecord {
	r.mu.Lock()
	out := make([]GatewayRecord, 0, len(r.byEUI))
	for _, rec := range r.byEUI {
		out = append(out, *rec)
	}
	r.mu.Unlock()

	sort.Slice(out, func(i, j int) bool { return out[i].EUI.String() < out[j].EUI.String() })
	return out
}

// onlineCount is the number the dashboard shows as "gateways online".
func (r *registry) onlineCount() int {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range r.byEUI {
		if rec.Online(now, r.timeout) {
			n++
		}
	}
	return n
}
