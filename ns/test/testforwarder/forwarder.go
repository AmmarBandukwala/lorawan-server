// Package testforwarder is a Semtech packet forwarder in test form.
//
// It owns a UDP socket, sends the datagrams a real gateway sends and decodes
// what the network server pushes back, so a test can drive the whole radio path
// — device frame in one end, txpk out the other — without hardware.
//
// It speaks the protocol through the exported semtech API on purpose. That
// makes it a second, independent consumer of the wire format: if the codec's
// exported surface were ever too awkward to build a real forwarder with, this
// package would be the first place to notice.
package testforwarder

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway/semtech"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// Forwarder is one gateway's side of the link.
type Forwarder struct {
	t      *testing.T
	conn   *net.UDPConn
	server *net.UDPAddr
	eui    lorawan.EUI64
	seq    uint16
}

// New binds a socket and points it at the server's address, which may be the
// ":0" port the server reported after Start.
func New(t *testing.T, serverAddr string) *Forwarder {
	t.Helper()

	server, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		t.Fatalf("testforwarder: resolve server address %q: %v", serverAddr, err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("testforwarder: bind a gateway socket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	eui, err := lorawan.ParseEUI64(GatewayEUI)
	if err != nil {
		t.Fatalf("testforwarder: bad gateway constant: %v", err)
	}
	return &Forwarder{t: t, conn: conn, server: server, eui: eui}
}

// GatewayEUI is the identifier this forwarder claims. Tests assert on it, so it
// is fixed rather than random.
const GatewayEUI = "aa55555555555555"

// EUI is the gateway identifier sent in every header.
func (f *Forwarder) EUI() lorawan.EUI64 { return f.eui }

// Addr is this forwarder's socket address, which is what the server records as
// the gateway's location and sends downlinks to.
func (f *Forwarder) Addr() string { return f.conn.LocalAddr().String() }

// Send writes one gateway-originated packet and returns its sequence number,
// which the server is expected to echo in the acknowledgement.
func (f *Forwarder) Send(typ semtech.PacketType, body string) uint16 {
	f.t.Helper()

	f.seq++
	pkt := semtech.BuildPacket(semtech.Header{
		Version: semtech.Version2, Type: typ, Sequence: f.seq, Gateway: f.eui,
	}, []byte(body))
	if _, err := f.conn.WriteTo(pkt, f.server); err != nil {
		f.t.Fatalf("testforwarder: send %s: %v", typ, err)
	}
	return f.seq
}

// Register sends the PULL_DATA that tells the server this gateway exists and
// where it is. Without it the server has no address to send downlinks to, which
// is how the protocol behaves rather than a test limitation.
func (f *Forwarder) Register() {
	f.t.Helper()

	f.Send(semtech.PullData, "")
	if h, _ := f.Read(); h.Type != semtech.PullAck {
		f.t.Fatalf("Register: expected PULL_ACK, got %s", h.Type)
	}
}

// PushUplink sends a PUSH_DATA carrying an rxpk/stat body.
func (f *Forwarder) PushUplink(body string) {
	f.t.Helper()

	seq := f.Send(semtech.PushData, body)
	h, _ := f.Read()
	// A forwarder resends the whole batch when PUSH_ACK is missing, so the
	// acknowledgement is part of the contract, not a detail.
	if h.Type != semtech.PushAck || h.Sequence != seq {
		f.t.Fatalf("PushUplink: expected PUSH_ACK with sequence %d, got %s %d",
			seq, h.Type, h.Sequence)
	}
}

// Read waits for the next datagram from the server.
func (f *Forwarder) Read() (semtech.Header, []byte) {
	f.t.Helper()

	if err := f.conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		f.t.Fatalf("testforwarder: set a read deadline: %v", err)
	}
	buf := make([]byte, 65536)
	n, _, err := f.conn.ReadFrom(buf)
	if err != nil {
		f.t.Fatalf("testforwarder: read from the server: %v", err)
	}
	h, err := semtech.ParseHeader(buf[:n])
	if err != nil {
		f.t.Fatalf("testforwarder: parse the server reply % x: %v", buf[:n], err)
	}
	var body []byte
	if h.BodyStart < n {
		body = buf[h.BodyStart:n]
	}
	return h, body
}

// ReadWithin waits up to d for a datagram. ok is false when nothing arrived in
// time, which is how a test asserts that the server stayed silent — a read
// deadline is the only way to observe an absence over UDP.
func (f *Forwarder) ReadWithin(d time.Duration) (semtech.Header, []byte, bool) {
	f.t.Helper()

	if err := f.conn.SetReadDeadline(time.Now().Add(d)); err != nil {
		f.t.Fatalf("testforwarder: set a read deadline: %v", err)
	}
	buf := make([]byte, 65536)
	n, _, err := f.conn.ReadFrom(buf)
	if err != nil {
		return semtech.Header{}, nil, false
	}
	if err := f.conn.SetReadDeadline(time.Time{}); err != nil {
		f.t.Fatalf("testforwarder: clear the read deadline: %v", err)
	}
	h, err := semtech.ParseHeader(buf[:n])
	if err != nil {
		f.t.Fatalf("testforwarder: parse the unsolicited datagram % x: %v", buf[:n], err)
	}
	var body []byte
	if h.BodyStart < n {
		body = buf[h.BodyStart:n]
	}
	return h, body, true
}

// ReadDownlink reads the next PULL_RESP and decodes the txpk inside it. It fails
// the test on anything else, because in these tests the only thing the server
// pushes is a downlink.
func (f *Forwarder) ReadDownlink() semtech.DownlinkCommand {
	f.t.Helper()

	h, body := f.Read()
	if h.Type != semtech.PullResp {
		f.t.Fatalf("expected PULL_RESP, got %s: %s", h.Type, body)
	}
	// Decoded through the same exported type the server encoded, so a tag typo
	// on either side shows up here instead of in a field that reads as zero.
	var wrapper struct {
		Tx semtech.DownlinkCommand `json:"txpk"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		f.t.Fatalf("testforwarder: decode txpk %s: %v", body, err)
	}
	return wrapper.Tx
}
