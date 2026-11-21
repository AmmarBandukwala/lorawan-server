package semtech

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

const testGwEUI = "aa55555555555555"

// fakeGateway is a packet forwarder in test form: it owns a UDP socket, sends
// the packets a real forwarder sends and reads back whatever the server pushes.
type fakeGateway struct {
	t      *testing.T
	conn   *net.UDPConn
	server *net.UDPAddr
	eui    lorawan.EUI64
	seq    uint16
}

func newFakeGateway(t *testing.T, serverAddr string) *fakeGateway {
	t.Helper()
	server, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		t.Fatalf("resolve server address: %v", err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("bind the fake gateway socket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &fakeGateway{t: t, conn: conn, server: server, eui: mustEUI(t, testGwEUI)}
}

// send writes one gateway-originated packet and returns its sequence number,
// which the server must echo in the acknowledgement.
func (g *fakeGateway) send(typ PacketType, body string) uint16 {
	g.t.Helper()
	g.seq++
	pkt := BuildPacket(Header{
		Version: Version2, Type: typ, Sequence: g.seq, Gateway: g.eui,
	}, []byte(body))
	if _, err := g.conn.WriteTo(pkt, g.server); err != nil {
		g.t.Fatalf("send %s: %v", typ, err)
	}
	return g.seq
}

// read waits for the next datagram the server sends us.
func (g *fakeGateway) read() (Header, []byte) {
	g.t.Helper()
	if err := g.conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		g.t.Fatalf("set a read deadline: %v", err)
	}
	buf := make([]byte, 65536)
	n, _, err := g.conn.ReadFrom(buf)
	if err != nil {
		g.t.Fatalf("read from the server: %v", err)
	}
	h, err := ParseHeader(buf[:n])
	if err != nil {
		g.t.Fatalf("parse the server reply: %v", err)
	}
	var body []byte
	if h.BodyStart < n {
		body = buf[h.BodyStart:n]
	}
	return h, body
}

// captureHandler stands in for the network server and records what the
// transport hands it.
type captureHandler struct {
	frames chan gateway.UplinkFrame
}

func (h *captureHandler) HandleUplink(_ context.Context, f gateway.UplinkFrame) error {
	select {
	case h.frames <- f:
	default:
	}
	return nil
}

func startServer(t *testing.T, h Handler, regionName string) *Server {
	t.Helper()
	srv, err := New(Config{
		Addr:    "127.0.0.1:0",
		Region:  regionName,
		Handler: h,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("semtech.New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("semtech.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func TestPullDataIsAcknowledgedAndRegistersTheGateway(t *testing.T) {
	srv := startServer(t, &captureHandler{frames: make(chan gateway.UplinkFrame, 8)}, "EU868")
	gw := newFakeGateway(t, srv.ListenAddr())

	seq := gw.send(PullData, "")
	h, _ := gw.read()
	if h.Type != PullAck {
		t.Fatalf("reply = %s, want PULL_ACK", h.Type)
	}
	if h.Sequence != seq {
		t.Errorf("PULL_ACK sequence = %d, want %d: a forwarder matches replies by sequence",
			h.Sequence, seq)
	}

	rec, ok := srv.reg.lookup(mustEUI(t, testGwEUI))
	if !ok {
		t.Fatal("PULL_DATA did not register the gateway")
	}
	if rec.Addr != gw.conn.LocalAddr().String() {
		t.Errorf("registered address = %s, want %s", rec.Addr, gw.conn.LocalAddr())
	}
	if !rec.Online(time.Now(), time.Minute) {
		t.Error("a gateway that just sent PULL_DATA must count as online")
	}
	if rec.Packets != 1 {
		t.Errorf("packet count = %d, want 1", rec.Packets)
	}
	if srv.GatewaysOnline() != 1 {
		t.Errorf("GatewaysOnline = %d, want 1", srv.GatewaysOnline())
	}
}

func TestPushDataReachesTheHandlerWithRadioMetadata(t *testing.T) {
	sink := &captureHandler{frames: make(chan gateway.UplinkFrame, 8)}
	srv := startServer(t, sink, "EU868")
	gw := newFakeGateway(t, srv.ListenAddr())

	body := `{"rxpk":[{"tmst":1234567890,"freq":868.1,"rssi":-71.5,"lsnr":9.2,"size":12,` +
		`"modu":"LORA","datr":"SF7BW125","codr":"4/5","data":"400102030405060708090a0b"}],` +
		`"stat":{"rxnb":5,"rxok":4,"txnb":1}}`
	seq := gw.send(PushData, body)

	h, _ := gw.read()
	if h.Type != PushAck || h.Sequence != seq {
		t.Fatalf("reply = %s seq %d, want PUSH_ACK seq %d", h.Type, h.Sequence, seq)
	}

	select {
	case frame := <-sink.frames:
		if frame.GatewayID != mustEUI(t, testGwEUI) {
			t.Errorf("GatewayID = %s", frame.GatewayID)
		}
		if frame.FrequencyHz != 868100000 {
			t.Errorf("FrequencyHz = %d, want 868100000", frame.FrequencyHz)
		}
		if frame.DataRateIndex != 5 {
			t.Errorf("DataRateIndex = %d, want 5 (SF7BW125 on EU868)", frame.DataRateIndex)
		}
		if frame.RSSI != -71.5 || frame.SNR != 9.2 {
			t.Errorf("RSSI/SNR = %v/%v", frame.RSSI, frame.SNR)
		}
		if frame.RxWindow != gateway.RxWindow1 {
			t.Errorf("RxWindow = %s, want RX1", frame.RxWindow)
		}
		if len(frame.PHYPayload) != 12 {
			t.Errorf("PHYPayload = %d bytes, want 12", len(frame.PHYPayload))
		}
		// The token has to carry the concentrator timestamp, or the downlink
		// cannot be placed in the right slot.
		if _, _, ts, hasTS := parseToken(frame.UplinkID); !hasTS ||
			ts != 1234567890*time.Microsecond {
			t.Errorf("token timestamp = %v (known=%v), want 1234567890us", ts, hasTS)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never saw the uplink")
	}

	gws := srv.Gateways()
	if len(gws) != 1 {
		t.Fatalf("registry holds %d gateways, want 1", len(gws))
	}
	if gws[0].LastStats.RxReceived != 5 || gws[0].LastStats.RxOK != 4 {
		t.Errorf("stats were not recorded: %+v", gws[0].LastStats)
	}
}

func TestSendDownlinkRefusesAnUnseenGateway(t *testing.T) {
	srv := startServer(t, &captureHandler{frames: make(chan gateway.UplinkFrame, 1)}, "EU868")
	err := srv.SendDownlink(context.Background(), gateway.DownlinkFrame{
		GatewayID:     mustEUI(t, "1122334455667788"),
		FrequencyHz:   869525000,
		DataRateIndex: 0,
		Window:        gateway.RxWindow2,
		PHYPayload:    []byte{0x80, 0x00},
	})
	if err == nil || !strings.Contains(err.Error(), "never been heard from") {
		t.Errorf("err = %v, want an explicit unseen-gateway error", err)
	}
}

func TestTxAckErrorIsRecordedOnTheGateway(t *testing.T) {
	srv := startServer(t, &captureHandler{frames: make(chan gateway.UplinkFrame, 1)}, "EU868")
	gw := newFakeGateway(t, srv.ListenAddr())

	gw.send(PullData, "")
	gw.read() // PULL_ACK; also registers the gateway
	gw.send(TxAck, `{"error":"TOO_LATE"}`)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec, ok := srv.reg.lookup(mustEUI(t, testGwEUI)); ok &&
			rec.DownlinkErrors == 1 && rec.LastTxAckError == "TOO_LATE" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the TX_ACK error was not recorded; registry = %+v", srv.Gateways())
}

// TestMalformedDatagramIsIgnoredWithoutPanic matters because the port is
// reachable from the LAN: anyone can send it garbage, and that must not take the
// network server down.
func TestMalformedDatagramIsIgnoredWithoutPanic(t *testing.T) {
	srv := startServer(t, &captureHandler{frames: make(chan gateway.UplinkFrame, 1)}, "EU868")
	addr, err := net.ResolveUDPAddr("udp", srv.ListenAddr())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	for _, junk := range [][]byte{
		{}, {0x40}, {0xff, 0x00, 0x01}, []byte("not a semtech packet at all"),
	} {
		if _, err := conn.Write(junk); err != nil {
			t.Fatalf("write junk: %v", err)
		}
	}
	// A PUSH_DATA with a valid header and a broken body must still be answered,
	// or the forwarder retries the batch forever.
	gw := newFakeGateway(t, srv.ListenAddr())
	seq := gw.send(PushData, `{"rxpk":[`)
	if header, _ := gw.read(); header.Type != PushAck || header.Sequence != seq {
		t.Fatalf("a PUSH_DATA with a broken body was not acknowledged: %s seq %d", header.Type, header.Sequence)
	}
	// And the server must still be serving.
	seq = gw.send(PullData, "")
	if header, _ := gw.read(); header.Type != PullAck || header.Sequence != seq {
		t.Fatalf("the server stopped answering: got %s seq %d", header.Type, header.Sequence)
	}
}
