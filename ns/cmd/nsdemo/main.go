// Command nsdemo runs the Fieldnode network-server core end to end in one
// process: a simulated class-A device joins over the air, exchanges uplinks and
// downlinks, answers MAC requests and attempts a replay, while every engine
// decision is printed.
//
// It needs no database, no gateway and no radio:
//
//	cd ns && go run ./cmd/nsdemo -region EU868
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// consoleGateway prints the downlinks the engine schedules instead of
// transmitting them.
type consoleGateway struct {
	step func(format string, args ...any)
}

func (g *consoleGateway) SendDownlink(_ context.Context, f gateway.DownlinkFrame) error {
	g.step("gateway %s transmits in %s at %d Hz, data-rate %d, %d dBm, %d ms after the uplink (%d byte frame)",
		f.GatewayID, f.Window, f.FrequencyHz, f.DataRateIndex, f.TxPowerdBm,
		f.Delay.Milliseconds(), len(f.PHYPayload))
	return nil
}

func main() {
	regionName := flag.String("region", region.DefaultRegion,
		"regional band; one of "+strings.Join(region.Names(), ", "))
	quiet := flag.Bool("quiet", false, "only print the summary")
	flag.Parse()

	if err := run(*regionName, *quiet); err != nil {
		fmt.Fprintln(os.Stderr, "nsdemo:", err)
		os.Exit(1)
	}
}

func run(regionName string, quiet bool) error {
	netID, err := lorawan.ParseNetID("010203")
	if err != nil {
		return err
	}
	devEUI, err := lorawan.ParseEUI64("0011223344556677")
	if err != nil {
		return err
	}
	joinEUI, err := lorawan.ParseEUI64("0102030405060708")
	if err != nil {
		return err
	}
	appKey, err := lorawan.ParseAES128Key("2b7e151628aed2a6abf7158809cf4f3c")
	if err != nil {
		return err
	}
	gwEUI, err := lorawan.ParseEUI64("aa55555555555555")
	if err != nil {
		return err
	}

	reg, err := region.New(regionName)
	if err != nil {
		return err
	}
	uplinkFreq, uplinkDR := firstChannel(reg)

	stepNum := 0
	step := func(format string, args ...any) {
		if quiet {
			return
		}
		stepNum++
		fmt.Printf("%2d. %s\n", stepNum, fmt.Sprintf(format, args...))
	}

	store := engine.NewMemStore()
	cfg := engine.DefaultConfig()
	cfg.NetID = netID
	cfg.Region = reg.CommonName()
	// A short de-duplication window keeps the demo's replay demonstration
	// readable: the two-gateway duplicate happens instantly (dropped), while a
	// deliberate replay after a pause reaches the nonce check.
	cfg.DedupWindow = 50 * time.Millisecond

	eng, err := engine.New(cfg, store.Store(), &consoleGateway{step: step},
		engine.WithLogger(slog.New(slog.NewTextHandler(os.Stderr,
			&slog.HandlerOptions{Level: slog.LevelWarn}))),
		// Fire scheduled transmissions immediately so the output is ordered;
		// the printed delay is the one a real gateway would honour.
		engine.WithScheduler(func(_ time.Duration, fn func()) { fn() }),
	)
	if err != nil {
		return err
	}

	ctx := context.Background()
	fmt.Printf("Fieldnode network server core — %s (NetID %s)\n", reg.CommonName(), netID)
	fmt.Printf("device %s / join-eui %s / app-key %s\n\n", devEUI, joinEUI, appKey)

	// --- provision ----------------------------------------------------------
	if err := store.SaveDevice(ctx, engine.NewOTAADevice(devEUI, joinEUI, appKey)); err != nil {
		return err
	}
	step("provisioned OTAA device %s", devEUI)

	simDev := sim.New(devEUI, joinEUI, appKey, netID)

	// --- join ---------------------------------------------------------------
	joinReq, err := simDev.NextJoinRequest()
	if err != nil {
		return err
	}
	step("device sends a join-request (DevNonce %d) on %d Hz / DR%d", simDev.DevNonce, uplinkFreq, uplinkDR)
	if err := eng.HandleUplink(ctx,
		frame(gwEUI, reg.CommonName(), joinReq, gateway.RxWindow1, uplinkFreq, uplinkDR)); err != nil {
		return err
	}

	accepts := store.Downlinks()
	if len(accepts) == 0 {
		return fmt.Errorf("the join was rejected: %+v", store.Events())
	}
	if err := simDev.HandleJoinAccept(accepts[len(accepts)-1].RawPHYPayload); err != nil {
		return err
	}
	step("device activated: DevAddr %s, NwkSKey %s, AppSKey %s",
		simDev.DevAddr, simDev.NwkSKey, simDev.AppSKey)

	// --- uplink -------------------------------------------------------------
	if err := send(eng, simDev, ctx, gwEUI, reg.CommonName(), uplinkFreq, uplinkDR,
		sim.UplinkOptions{FPort: 10, Payload: []byte("hello fieldnode")}); err != nil {
		return err
	}
	up := store.Uplinks()
	step("uplink %d accepted and decrypted: %q (RSSI %.1f dBm, SNR %.1f dB)",
		up[len(up)-1].FCntUp, string(up[len(up)-1].FRMPayload), up[len(up)-1].RSSI, up[len(up)-1].SNR)

	// --- downlink and acknowledgement ---------------------------------------
	if err := eng.EnqueueDownlink(ctx, devEUI, 10, []byte("valve: open 40%"), false); err != nil {
		return err
	}
	step("application queued a downlink; it waits for the device's next window")

	before := len(store.Downlinks())
	if err := send(eng, simDev, ctx, gwEUI, reg.CommonName(), uplinkFreq, uplinkDR,
		sim.UplinkOptions{FPort: 10, Payload: []byte("1")}); err != nil {
		return err
	}
	dls := store.Downlinks()
	if len(dls) <= before {
		return fmt.Errorf("the queued downlink was not sent: %+v", store.Events())
	}
	dl, err := simDev.HandleDownlink(dls[before].RawPHYPayload)
	if err != nil {
		return err
	}
	step("device decrypted FCntDown %d and read %q", dl.FCntDown, string(dl.FRMPayload))

	if err := send(eng, simDev, ctx, gwEUI, reg.CommonName(), uplinkFreq, uplinkDR,
		sim.UplinkOptions{FPort: 10, Payload: []byte("2"), ACK: true}); err != nil {
		return err
	}
	step("device acknowledged the downlink; session AFCntDown = %d",
		mustSession(eng, ctx, devEUI).AFCntDown)

	// --- MAC command round trips --------------------------------------------
	if err := eng.RequestDeviceStatus(ctx, devEUI); err != nil {
		return err
	}
	if err := send(eng, simDev, ctx, gwEUI, reg.CommonName(), uplinkFreq, uplinkDR,
		sim.UplinkOptions{NoFPort: true}); err != nil {
		return err
	}
	simDev.Battery, simDev.Margin = 187, -4
	statusAns, err := simDev.DevStatusAns()
	if err != nil {
		return err
	}
	if err := eng.HandleUplink(ctx,
		frame(gwEUI, reg.CommonName(), statusAns, gateway.RxWindow1, uplinkFreq, uplinkDR)); err != nil {
		return err
	}
	ses := mustSession(eng, ctx, devEUI)
	step("DevStatusReq/Ans round trip: battery %d/255, margin %d dB", ses.Battery, ses.Margin)

	if err := eng.RequestLinkADR(ctx, devEUI, adrTarget(reg), 2, 1, 0x00FF, 0); err != nil {
		return err
	}
	if err := send(eng, simDev, ctx, gwEUI, reg.CommonName(), uplinkFreq, uplinkDR,
		sim.UplinkOptions{NoFPort: true}); err != nil {
		return err
	}
	adrAns, err := simDev.LinkADRAns(true)
	if err != nil {
		return err
	}
	if err := eng.HandleUplink(ctx,
		frame(gwEUI, reg.CommonName(), adrAns, gateway.RxWindow1, uplinkFreq, uplinkDR)); err != nil {
		return err
	}
	ses = mustSession(eng, ctx, devEUI)
	step("LinkADRReq/Ans round trip: session now DR%d, TX index %d, NbTrans %d",
		ses.DataRate, ses.TxPower, ses.NbTrans)

	// --- replay attempts ----------------------------------------------------
	replay, err := simDev.NextUplink(sim.UplinkOptions{FPort: 10, Payload: []byte("replay me")})
	if err != nil {
		return err
	}
	if err := eng.HandleUplink(ctx,
		frame(gwEUI, reg.CommonName(), replay, gateway.RxWindow1, uplinkFreq, uplinkDR)); err != nil {
		return err
	}
	second := frame(gwEUI, reg.CommonName(), replay, gateway.RxWindow1, uplinkFreq, uplinkDR)
	second.GatewayID = mustParseEUI64("bb55555555555555")
	second.RSSI = -95
	if err := eng.HandleUplink(ctx, second); err != nil {
		return err
	}
	step("the same frame heard by a second gateway is dropped as %s", engine.ReasonDuplicateFrame)

	// Let the de-duplication window expire so this is a genuine replay attempt
	// rather than a multi-gateway duplicate.
	time.Sleep(2 * cfg.DedupWindow)
	if err := eng.HandleUplink(ctx,
		frame(gwEUI, reg.CommonName(), joinReq, gateway.RxWindow1, uplinkFreq, uplinkDR)); err != nil {
		return err
	}
	step("replaying the original join-request is refused with %s", engine.ReasonDevNonceReplay)

	// --- summary ------------------------------------------------------------
	ses = mustSession(eng, ctx, devEUI)
	fmt.Printf("\nsession: DevAddr %s, FCntUp %d, FCntDown %d, DR%d, region %s\n",
		ses.DevAddr, ses.FCntUp, ses.FCntDown, ses.DataRate, ses.Region)
	fmt.Printf("traffic: %d uplink(s), %d downlink transmission(s), %d event(s)\n",
		len(store.Uplinks()), len(store.Downlinks()), len(store.Events()))
	if quiet {
		return nil
	}
	fmt.Println("\nengine event log:")
	for _, ev := range store.Events() {
		reason := ""
		if ev.Reason != "" {
			reason = " [" + ev.Reason + "]"
		}
		fmt.Printf("  %-13s%s  %s\n", ev.Kind, reason, ev.Detail)
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

func frame(gwEUI lorawan.EUI64, regionName string, wire []byte,
	w gateway.RxWindow, freqHz uint32, dr int) gateway.UplinkFrame {

	return gateway.UplinkFrame{
		GatewayID:     gwEUI,
		RFRegion:      regionName,
		RxWindow:      w,
		FrequencyHz:   freqHz,
		DataRateIndex: dr,
		UplinkID:      [8]byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x00, 0x00, 0x01},
		PHYPayload:    wire,
		RSSI:          -71.5,
		SNR:           9.2,
		ReceivedAt:    time.Now(),
	}
}

func send(eng *engine.Engine, simDev *sim.Device, ctx context.Context, gwEUI lorawan.EUI64,
	regionName string, freqHz uint32, dr int, o sim.UplinkOptions) error {

	wire, err := simDev.NextUplink(o)
	if err != nil {
		return err
	}
	return eng.HandleUplink(ctx, frame(gwEUI, regionName, wire, gateway.RxWindow1, freqHz, dr))
}

func mustSession(eng *engine.Engine, ctx context.Context, devEUI lorawan.EUI64) *engine.Session {
	ses, err := eng.Session(ctx, devEUI)
	if err != nil {
		panic(fmt.Sprintf("session for %s: %v", devEUI, err))
	}
	return ses
}

func mustParseEUI64(s string) lorawan.EUI64 {
	e, err := lorawan.ParseEUI64(s)
	if err != nil {
		panic(err)
	}
	return e
}

// firstChannel returns the band's first enabled uplink channel and the lowest
// data-rate it allows, so the demo can transmit legally in any region.
func firstChannel(reg region.Region) (uint32, int) {
	for _, ch := range reg.UplinkChannels() {
		if !ch.Enabled || len(ch.DataRates) == 0 {
			continue
		}
		lowest := ch.DataRates[0]
		for _, dr := range ch.DataRates {
			if dr < lowest {
				lowest = dr
			}
		}
		return ch.FrequencyHz, lowest
	}
	def := reg.Defaults()
	return def.RX2FrequencyHz, def.MinUplinkDataRate
}

// adrTarget is the data rate a device with a clean link would settle on: the
// band's own ceiling for ordinary LoRa uplinks. Scanning the data-rate table for
// the highest entry instead would pick rates such as EU868 DR13 (SF5), which the
// regional parameters reserve and the engine therefore refuses to request.
func adrTarget(reg region.Region) int {
	return reg.Defaults().MaxUplinkDataRate
}
