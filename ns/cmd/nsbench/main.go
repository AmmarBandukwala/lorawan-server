// Command nsbench drives a running ns server over real UDP with a simulated
// class-A device behind a simulated packet forwarder. It joins, exchanges an
// uplink and a downlink, and prints what happened at each step.
//
// It is the bring-up harness for the network server: it proves the socket, the
// protocol, the join server, the session state and the RX-window scheduling all
// work before any radio hardware is involved.
//
//	cd ns && go run ./cmd/ns -listen ...            # in one terminal
//	cd ns && go run ./cmd/nsbench -app-key 2b7e...  # in another
//
// Every flag has a default that matches the ns defaults, so after seeding the
// default device the tool runs with no arguments at all.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

type options struct {
	addr       string
	region     string
	gatewayEUI string
	devEUI     string
	joinEUI    string
	appKey     string
	netID      string
	uplinkFreq float64
	dataRate   string
	payload    string
	timeout    time.Duration
}

func main() {
	var o options
	flag.StringVar(&o.addr, "addr", "127.0.0.1:6858", "UDP address of the ns server")
	flag.StringVar(&o.region, "region", "EU868", "regional band the simulated gateway uses")
	flag.StringVar(&o.gatewayEUI, "gateway-eui", "AA55555555555555", "gateway EUI to announce")
	flag.StringVar(&o.devEUI, "dev-eui", "0011223344556677", "device EUI")
	flag.StringVar(&o.joinEUI, "join-eui", "0102030405060708", "join (App) EUI")
	flag.StringVar(&o.appKey, "app-key", "2b7e151628aed2a6abf7158809cf4f3c", "OTAA AppKey")
	flag.StringVar(&o.netID, "net-id", "000000", "NetID the server is configured with")
	flag.Float64Var(&o.uplinkFreq, "freq", 868.1, "uplink channel in MHz")
	flag.StringVar(&o.dataRate, "datr", "SF12BW125", "uplink data rate as a Semtech datr string")
	flag.StringVar(&o.payload, "payload", "48656c6c6f204669656c646e6f6465", "application payload, hex")
	flag.DurationVar(&o.timeout, "timeout", 12*time.Second, "how long to wait for each downlink window")
	flag.Parse()

	if err := run(o); err != nil {
		fmt.Fprintf(os.Stderr, "\nnsbench: %v\n", err)
		os.Exit(1)
	}
}

func run(o options) error {
	// Validating the band up front means a typo is reported as a bad flag rather
	// than as a mysterious rejection later.
	if _, err := region.New(o.region); err != nil {
		return err
	}
	gwEUI, err := lorawan.ParseEUI64(o.gatewayEUI)
	if err != nil {
		return fmt.Errorf("-gateway-eui: %w", err)
	}
	devEUI, err := lorawan.ParseEUI64(o.devEUI)
	if err != nil {
		return fmt.Errorf("-dev-eui: %w", err)
	}
	joinEUI, err := lorawan.ParseEUI64(o.joinEUI)
	if err != nil {
		return fmt.Errorf("-join-eui: %w", err)
	}
	appKey, err := lorawan.ParseAES128Key(o.appKey)
	if err != nil {
		return fmt.Errorf("-app-key: %w", err)
	}
	netID, err := lorawan.ParseNetID(o.netID)
	if err != nil {
		return fmt.Errorf("-net-id: %w", err)
	}
	payload, err := hex.DecodeString(o.payload)
	if err != nil {
		return fmt.Errorf("-payload is not hex: %w", err)
	}

	conn, err := net.Dial("udp", o.addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", o.addr, err)
	}
	defer conn.Close()

	g := &forwarder{conn: conn, eui: gwEUI}
	fmt.Printf("nsbench: simulating gateway %s on %s against %s\n", gwEUI, o.region, o.addr)

	// A forwarder announces itself before it can be reached.
	if err := g.pullData(conn); err != nil {
		return err
	}
	if err := g.stats(conn); err != nil {
		return err
	}

	dev := sim.New(devEUI, joinEUI, appKey, netID)
	joinReq, err := dev.NextJoinRequest()
	if err != nil {
		return fmt.Errorf("build a join-request: %w", err)
	}
	fmt.Printf("\n1. over the air: join-request (DevEUI %s, DevNonce %04x), %d bytes\n",
		devEUI, dev.DevNonce, len(joinReq))

	tmst := uint32(time.Now().UnixMicro())
	if err := g.pushData(conn, joinReq, o.uplinkFreq, o.dataRate, tmst); err != nil {
		return err
	}

	fmt.Printf("2. waiting for the join-accept in both RX windows (up to %v)\n", o.timeout)
	copies, err := g.awaitDownlinks(conn, o.timeout, func(wire []byte) error {
		return dev.HandleJoinAccept(wire)
	})
	if err != nil {
		return fmt.Errorf("no join-accept came back: %w", err)
	}
	fmt.Printf("3. device accepted the join-accept after %d PULL_RESP(s): "+
		"DevAddr %s, NwkSKey %s, AppSKey %s\n", copies, dev.DevAddr, dev.NwkSKey, dev.AppSKey)

	// The server answers a join in both windows, so the RX2 copy is still in
	// flight; clear it before reading the next exchange.
	g.drain(conn, 3*time.Second)

	// A confirmed uplink is chosen deliberately: the server must acknowledge it,
	// which makes the next downlink something to assert rather than hope for. An
	// unconfirmed uplink with nothing pending is correctly answered with silence.
	up, err := dev.NextUplink(sim.UplinkOptions{FPort: 1, Payload: payload, Confirmed: true})
	if err != nil {
		return fmt.Errorf("build an uplink: %w", err)
	}
	fmt.Printf("\n4. over the air: confirmed uplink fCnt=%d fPort=1 %d byte payload, "+
		"%d bytes on air\n", dev.FCntUp-1, len(payload), len(up))
	if err := g.pushData(conn, up, o.uplinkFreq, o.dataRate, tmst+1_000_000); err != nil {
		return err
	}

	fmt.Printf("5. waiting for the acknowledgement (up to %v)\n", o.timeout)
	gotDownlink := false
	_, err = g.awaitDownlinks(conn, o.timeout, func(wire []byte) error {
		dl, err := dev.HandleDownlink(wire)
		if err != nil {
			return err
		}
		gotDownlink = true
		fmt.Printf("6. device accepted the downlink: fCntDown=%d fPort=%d ack=%v",
			dl.FCntDown, dl.FPort, dl.ACK)
		if len(dl.FRMPayload) > 0 {
			fmt.Printf(" payload=%q", dl.FRMPayload)
		}
		fmt.Println()
		for _, cmd := range dl.MACCommands {
			fmt.Printf("   MAC command: %s\n", cmd.CID)
		}
		if !dl.ACK {
			return fmt.Errorf("the server did not acknowledge the confirmed uplink")
		}
		return nil
	})
	if err != nil && !gotDownlink {
		return fmt.Errorf("the confirmed uplink was never acknowledged: %w", err)
	}

	fmt.Println("\nnsbench: the network server joined a device and exchanged traffic over UDP")
	return nil
}
