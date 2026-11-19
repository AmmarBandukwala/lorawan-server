package lbs

import (
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// TestJoinAcceptOverWebSocket is the whole path with no fakes in the middle: a
// simulated device's join-request leaves as a `jreq` over a real socket, the
// engine authenticates it, and the join-accept comes back as a `dnmsg` the
// device can decrypt.
//
// The timing assertions are the point of the test. A dnmsg carries the uplink's
// xtime plus a whole-second delay, and the session bits of that xtime must
// survive intact — a station drops a downlink whose session does not match,
// which is exactly how a downlink vanishes with no error anywhere.
func TestJoinAcceptOverWebSocket(t *testing.T) {
	srv, _, store := stationTest(t, nil)
	st := dialStation(t, srv.ListenAddr(), mustEUI(t, testGwEUI), nil)
	st.send(map[string]any{"msgtype": "version", "station": "2.0.5"})
	if typ, _ := st.read(); typ != msgRouterConfig {
		t.Fatalf("handshake reply = %q", typ)
	}

	devEUI := mustParse(t, testDevEUI, lorawan.ParseEUI64)
	joinEUI := mustParse(t, testJoinEUI, lorawan.ParseEUI64)
	appKey := mustParse(t, testAppKey, lorawan.ParseAES128Key)
	netID := mustParse(t, testNetID, lorawan.ParseNetID)
	dev := sim.New(devEUI, joinEUI, appKey, netID)

	wire, err := dev.NextJoinRequest()
	if err != nil {
		t.Fatalf("NextJoinRequest: %v", err)
	}

	// A concentrator counter with a session identifier in the top bits, so the
	// echo can be checked rather than assumed.
	const uplinkXTime = uint64(0x1234_0000_000F_4240)
	st.send(map[string]any{
		"msgtype": "jreq",
		"DevEui":  devEUI.String(),
		"JoinEui": joinEUI.String(),
		"rxq": map[string]any{
			"DR": 0, "Freq": 868100000, "Rssi": -70.0, "Snr": 9.0,
			"XTime": uplinkXTime, "Tmst": uint32(uplinkXTime & 0xFFFFFFFF),
		},
		"upinfo": map[string]any{"xtime": uplinkXTime, "rctx": 1},
		"drawx":  encodeDrawx(wire),
	})

	// A join-request is answered in both windows.
	rx1 := st.readDownlink()
	rx2 := st.readDownlink()

	// EU868 join-accept timing: RX1 at 5 s, RX2 at 6 s after the uplink.
	if rx1.RxDelay != 5 {
		t.Errorf("RX1 RxDelay = %d, want 5", rx1.RxDelay)
	}
	if rx2.RxDelay != 6 {
		t.Errorf("RX2 RxDelay = %d, want 6", rx2.RxDelay)
	}
	for name, dn := range map[string]downlink{"RX1": rx1, "RX2": rx2} {
		// The xtime must be the uplink's, session bits included: the station
		// drops anything whose session does not match.
		if xtime(dn.XTime) != xtime(uplinkXTime) {
			t.Errorf("%s xtime = %s, want the uplink's %s",
				name, xtime(dn.XTime), xtime(uplinkXTime))
		}
		if dn.Class != deviceClassA {
			t.Errorf("%s dC = %d, want class A (%d)", name, dn.Class, deviceClassA)
		}
		if dn.RCtx != 1 {
			t.Errorf("%s rctx = %d, want the uplink's 1", name, dn.RCtx)
		}
		if dn.DevEUI == "" {
			t.Errorf("%s must name a device; the station needs it for the ack", name)
		}
	}
	if rx1.Rx1Freq != 868100000 {
		t.Errorf("RX1 freq = %d, want the uplink channel 868100000", rx1.Rx1Freq)
	}
	if rx2.Rx1Freq != 869525000 {
		t.Errorf("RX2 freq = %d, want 869525000", rx2.Rx1Freq)
	}
	if rx1.PDU != rx2.PDU {
		t.Error("both windows must carry the same join-accept")
	}

	raw, err := hex.DecodeString(rx1.PDU)
	if err != nil {
		t.Fatalf("pdu is not hex: %v", err)
	}
	// 17 bytes: MHdr, JoinNonce, NetID, DevAddr, DLSettings, RxDelay and MIC,
	// with no CFList. That is the correct length here rather than 33 — a CFList
	// carries channels the network wants the device to add, and at join time
	// there are none: EU868's three default channels are mandatory knowledge,
	// and user channels are learned per device later through NewChannelReq. The
	// codec handles both lengths, and the 33-byte form is covered by the
	// join-accept vector in the lorawan package.
	if len(raw) != 17 {
		t.Errorf("join-accept = %d bytes, want 17 (no CFList to distribute)", len(raw))
	}
	if err := dev.HandleJoinAccept(raw); err != nil {
		t.Fatalf("the device rejected the join-accept that came over the socket: %v", err)
	}

	// The transmission confirmation path: dntxed must be attributed to its dnmsg
	// by diid, which is the only evidence that the radio actually emitted.
	if rx1.DIID == rx2.DIID {
		t.Errorf("both downlinks share diid %d; confirmations could not be told apart", rx1.DIID)
	}
	st.send(map[string]any{"msgtype": "dntxed", "diid": rx1.DIID, "DevEui": devEUI.String()})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess, ok := srv.lookup(mustEUI(t, testGwEUI)); ok {
			if confirmed, _, _ := sess.tx.snapshot(); confirmed == 1 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	sess, ok := srv.lookup(mustEUI(t, testGwEUI))
	if !ok {
		t.Fatal("the station vanished from the registry")
	}
	if confirmed, _, _ := sess.tx.snapshot(); confirmed != 1 {
		t.Errorf("confirmed downlinks = %d, want 1", confirmed)
	}
	if got := sess.downlinksSent.Load(); got != 2 {
		t.Errorf("downlinks sent = %d, want 2", got)
	}
	if len(store.EventsOfKind(engine.EventJoinAccept)) != 1 {
		t.Errorf("the join was not recorded: %+v", store.Events())
	}
}

// TestStationTimesyncIsAnswered: a station that cannot correct its clock cannot
// place a downlink accurately, so an unanswered timesync quietly degrades every
// downlink timing on that gateway.
func TestStationTimesyncIsAnswered(t *testing.T) {
	srv, _, _ := stationTest(t, nil)
	st := dialStation(t, srv.ListenAddr(), mustEUI(t, testGwEUI), nil)
	st.send(map[string]any{"msgtype": "version", "station": "2.0.5"})
	st.read()

	// Teach the server an xtime first, so the answer can correlate it.
	const x = uint64(0x0001_0000_0000_0100)
	st.send(map[string]any{
		"msgtype": "updf",
		"rxq":     map[string]any{"DR": 5, "Freq": 868100000.0, "XTime": x},
		"upinfo":  map[string]any{"xtime": x},
		// Deliberately undecodable: the xtime must still be learned.
		"drawx": encodeDrawx([]byte{0x80, 0x00, 0x00, 0x00, 0x00}),
	})

	st.send(map[string]any{"msgtype": "timesync", "txtime": 1.5})
	for i := 0; i < 10; i++ {
		typ, payload := st.read()
		if typ != msgTimeSync {
			continue
		}
		var res timeSyncResponse
		if err := json.Unmarshal(payload, &res); err != nil {
			t.Fatalf("cannot parse the timesync answer: %v", err)
		}
		if res.MuxTime == 0 {
			t.Error("MuxTime must carry the server's clock or the station cannot correct")
		}
		if res.TxTime != 1.5 {
			t.Errorf("txtime = %v, want the request echoed back", res.TxTime)
		}
		if res.XTime != x {
			t.Errorf("xtime = %x, want the station's own %x", res.XTime, x)
		}
		return
	}
	t.Fatal("the timesync request was never answered")
}
