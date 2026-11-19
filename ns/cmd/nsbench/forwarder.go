package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway/semtech"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// forwarder is the gateway side of the exchange: it speaks the Semtech UDP
// protocol on behalf of the simulated device and hands over every downlink the
// server pushes to it.
type forwarder struct {
	conn net.Conn
	eui  lorawan.EUI64
	seq  uint16
}

func (g *forwarder) nextSeq() uint16 {
	g.seq++
	return g.seq
}

// pullData announces the gateway so the server learns the address it must send
// downlinks to, and checks the PULL_ACK comes back with our sequence number.
func (g *forwarder) pullData(conn net.Conn) error {
	pkt := semtech.BuildPacket(semtech.Header{
		Version: semtech.Version2, Type: semtech.PullData,
		Sequence: g.nextSeq(), Gateway: g.eui,
	}, nil)
	if _, err := conn.Write(pkt); err != nil {
		return fmt.Errorf("send PULL_DATA: %w", err)
	}
	h, _, err := g.read(conn, 5*time.Second)
	if err != nil {
		return fmt.Errorf("no PULL_ACK: %w", err)
	}
	if h.Type != semtech.PullAck {
		return fmt.Errorf("first reply was %s, want PULL_ACK", h.Type)
	}
	fmt.Printf("   gateway %s registered (PULL_ACK seq %d)\n", g.eui, h.Sequence)
	return nil
}

// stats sends the periodic stat object a real forwarder reports, so gateway
// health reaches the registry and the status endpoint.
func (g *forwarder) stats(conn net.Conn) error {
	body := `{"stat":{"rxnb":42,"rxok":40,"rxfw":0,"mailf":1.5,"txnb":3,` +
		`"lati":51.5074,"long":-0.1278,"alti":34}}`
	pkt := semtech.BuildPacket(semtech.Header{
		Version: semtech.Version2, Type: semtech.PushData,
		Sequence: g.nextSeq(), Gateway: g.eui,
	}, []byte(body))
	if _, err := conn.Write(pkt); err != nil {
		return fmt.Errorf("send stat PUSH_DATA: %w", err)
	}
	h, _, err := g.read(conn, 5*time.Second)
	if err != nil {
		return fmt.Errorf("no PUSH_ACK for the stat report: %w", err)
	}
	if h.Type != semtech.PushAck {
		return fmt.Errorf("stat report was answered with %s", h.Type)
	}
	fmt.Println("   gateway reported its stat object")
	return nil
}

// pushData delivers one frame as an rxpk, exactly as a concentrator would.
func (g *forwarder) pushData(conn net.Conn, phy []byte, freqMHZ float64, datr string, tmst uint32) error {
	body := fmt.Sprintf(`{"rxpk":[{"tmst":%d,"freq":%g,"rssi":-68,"lsnr":8.5,`+
		`"size":%d,"modu":"LORA","datr":"%s","codr":"4/5","data":"%s"}]}`,
		tmst, freqMHZ, len(phy), datr, hex.EncodeToString(phy))

	pkt := semtech.BuildPacket(semtech.Header{
		Version: semtech.Version2, Type: semtech.PushData,
		Sequence: g.nextSeq(), Gateway: g.eui,
	}, []byte(body))
	if _, err := conn.Write(pkt); err != nil {
		return fmt.Errorf("send PUSH_DATA: %w", err)
	}

	h, _, err := g.read(conn, 5*time.Second)
	if err != nil {
		return fmt.Errorf("no PUSH_ACK: %w", err)
	}
	if h.Type != semtech.PushAck {
		return fmt.Errorf("expected PUSH_ACK, got %s", h.Type)
	}
	fmt.Printf("   forwarder sent %d bytes and got PUSH_ACK\n", len(phy))
	return nil
}

// awaitDownlinks reads until a downlink the handler accepts arrives, or the
// timeout expires. The server answers in both RX windows, so the second copy of
// the same frame is expected and simply ignored once the first one is accepted.
func (g *forwarder) awaitDownlinks(conn net.Conn, timeout time.Duration,
	handle func(wire []byte) error) (int, error) {

	deadline := time.Now().Add(timeout)
	seen := 0
	for time.Now().Before(deadline) {
		left := time.Until(deadline)
		if left < 100*time.Millisecond {
			left = 100 * time.Millisecond
		}
		h, body, err := g.read(conn, left)
		if err != nil {
			return seen, err
		}
		switch h.Type {
		case semtech.PullResp:
			tx, err := decodeTXPK(body)
			if err != nil {
				return seen, err
			}
			seen++
			timing := "immediate"
			if tx.TMST != nil {
				timing = fmt.Sprintf("tmst %d", *tx.TMST)
			}
			fmt.Printf("   PULL_RESP #%d: %g MHz %s %d dBm ipol=%v %d bytes, %s\n",
				seen, tx.Freq, tx.Datr, tx.Powe, tx.IPol, len(tx.phy), timing)

			if err := handle(tx.phy); err != nil {
				// A duplicate copy in the second window failing is normal; the
				// first copy already moved the device's state.
				fmt.Printf("   (a later copy was refused: %v)\n", err)
				continue
			}
			return seen, nil
		case semtech.PushAck, semtech.PullAck:
			// Housekeeping from an earlier request; keep waiting.
		default:
			return seen, fmt.Errorf("unexpected %s from the server", h.Type)
		}
	}
	return seen, fmt.Errorf("timed out after %v waiting for a downlink", timeout)
}

// drain reads and discards whatever arrives within the window. After a join the
// server has answered in both RX windows, so a second copy of the join-accept is
// still on its way; reading it here keeps it from being mistaken for the response
// to the next uplink.
func (g *forwarder) drain(conn net.Conn, window time.Duration) int {
	n := 0
	for {
		h, _, err := g.read(conn, window)
		if err != nil {
			return n
		}
		if h.Type == semtech.PullResp {
			n++
			fmt.Printf("   (discarded a duplicate %s copy still in flight)\n", h.Type)
			continue
		}
		// A PUSH_ACK or PULL_ACK ends the pending requests; keep draining until
		// the socket goes quiet.
	}
}

func (g *forwarder) read(conn net.Conn, wait time.Duration) (semtech.Header, []byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return semtech.Header{}, nil, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return semtech.Header{}, nil, err
	}
	h, err := semtech.ParseHeader(buf[:n])
	if err != nil {
		return semtech.Header{}, nil, err
	}
	var body []byte
	if h.BodyStart < n {
		body = buf[h.BodyStart:n]
	}
	return h, body, nil
}

// txpkWire mirrors the JSON the server pushes, plus the decoded frame.
type txpkWire struct {
	Imme bool    `json:"imme"`
	TMST *uint32 `json:"tmst"`
	Freq float64 `json:"freq"`
	Datr string  `json:"datr"`
	Powe int8    `json:"powe"`
	IPol bool    `json:"ipol"`
	Size int     `json:"size"`
	Data string  `json:"data"`

	phy []byte
}

func decodeTXPK(body []byte) (txpkWire, error) {
	var wrapper struct {
		Tx txpkWire `json:"txpk"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return txpkWire{}, fmt.Errorf("decode txpk %s: %w", body, err)
	}
	tx := wrapper.Tx
	raw, err := hex.DecodeString(tx.Data)
	if err != nil {
		return tx, fmt.Errorf("txpk data is not hex: %w", err)
	}
	if tx.Size != len(raw) {
		return tx, fmt.Errorf("txpk size says %d but data holds %d bytes", tx.Size, len(raw))
	}
	tx.phy = raw
	return tx, nil
}
