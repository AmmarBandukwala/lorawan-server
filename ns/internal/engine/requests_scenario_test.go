package engine_test

import (
	"context"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/region"
	"github.com/fieldnode/lorawan-server/ns/internal/testenv"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
)

// ask sends one uplink and returns the MAC commands the device read out of the
// downlink that followed. Every test decodes exactly one frame per uplink, which
// keeps the device's downlink counter in step with the engine's.
func ask(t *testing.T, env *testenv.Env, simDev *sim.Device, o sim.UplinkOptions) lorawan.MACCommandSet {
	t.Helper()

	env.GW.Reset()
	env.Uplink(simDev, o, gateway.RxWindow1)
	frames := env.GW.Frames()
	if len(frames) == 0 {
		return nil
	}
	dl, err := simDev.HandleDownlink(frames[0].PHYPayload)
	if err != nil {
		t.Fatalf("the device rejected the downlink: %v", err)
	}
	return dl.MACCommands
}

// findCommand picks the first command with the given CID.
func findCommand(cmds lorawan.MACCommandSet, cid lorawan.CID) (lorawan.MACCommand, bool) {
	for _, c := range cmds {
		if c.CID == cid {
			return c, true
		}
	}
	return lorawan.MACCommand{}, false
}

// answer feeds an uplink carrying the given answers to outstanding requests.
func answer(t *testing.T, env *testenv.Env, simDev *sim.Device, cmds lorawan.MACCommandSet) {
	t.Helper()
	wire, err := simDev.NextUplink(sim.UplinkOptions{ACK: true, MACCommands: cmds})
	if err != nil {
		t.Fatalf("NextUplink: %v", err)
	}
	if err := env.Engine.HandleUplink(context.Background(),
		testenv.UplinkFrame(wire, gateway.RxWindow1, testenv.EU868Ch0Hz, testenv.EU868DR5)); err != nil {
		t.Fatalf("HandleUplink: %v", err)
	}
}

// countEventsWithReason scans the recorded events for one operator-facing
// reason string.
func countEventsWithReason(env *testenv.Env, reason string) int {
	n := 0
	for _, ev := range env.Store.Events() {
		if ev.Reason == reason {
			n++
		}
	}
	return n
}

// TestNewChannelAppliesOnlyAfterTheDeviceAccepts is the whole dynamic-channel
// path: the request goes out, the device confirms it, and only then does the
// channel become part of that device's plan.
func TestNewChannelAppliesOnlyAfterTheDeviceAccepts(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	if err := env.Engine.RequestNewChannel(ctx, devEUI, 3, 867700000, 0, 5); err != nil {
		t.Fatalf("RequestNewChannel: %v", err)
	}

	cmd, ok := findCommand(ask(t, env, simDev, sim.UplinkOptions{NoFPort: true}), lorawan.CIDNewChannel)
	if !ok {
		t.Fatal("no NewChannelReq in the downlink")
	}
	req, err := lorawan.ParseNewChannelReq(cmd.Payload)
	if err != nil {
		t.Fatalf("ParseNewChannelReq: %v", err)
	}
	if req.ChIndex != 3 || req.Frequency != 867700000 || req.MinDR != 0 || req.MaxDR != 5 {
		t.Errorf("NewChannelReq = %+v, want index 3, 867700000 Hz, DR0..DR5", req)
	}

	// Until the answer arrives the channel must not be used: a device that
	// refuses would otherwise be answered on a frequency it never opened.
	if ses := env.Session(devEUI); len(ses.UserChannels) != 0 {
		t.Errorf("the channel was applied before the answer: %+v", ses.UserChannels)
	}

	answer(t, env, simDev, lorawan.MACCommandSet{
		{CID: lorawan.CIDNewChannel, Payload: []byte{0x03}}, // frequency OK, data rates OK
	})

	ses := env.Session(devEUI)
	if ses.PendingChannel != nil {
		t.Errorf("the request is still outstanding: %+v", ses.PendingChannel)
	}
	ch, ok := ses.UserChannels[3]
	if !ok {
		t.Fatalf("channel 3 was not recorded: %+v", ses.UserChannels)
	}
	if ch.FrequencyHz != 867700000 || !ch.Enabled || !ch.UserDefined {
		t.Errorf("channel 3 = %+v", ch)
	}
	if len(ch.DataRates) != 6 {
		t.Errorf("channel 3 carries %d data rates, want 6 (DR0..DR5)", len(ch.DataRates))
	}

	// The plan must now be visible to anything that schedules this device. The
	// engine builds that view internally by layering the device's dynamic
	// channels over the band plan; the same layering is available through the
	// region package, which is what a caller outside the engine can check.
	layered, err := region.WithUserChannels(testenv.MustRegion(t, "EU868"), ses.UserChannels)
	if err != nil {
		t.Fatalf("WithUserChannels: %v", err)
	}
	reg := layered
	if reg.ChannelCount() != 4 {
		t.Errorf("the device plan has %d channels, want the 3 default ones plus 1", reg.ChannelCount())
	}
	if !reg.SupportsDataRateOnChannel(3, 5) {
		t.Error("the new channel should carry DR5")
	}
}

// TestNewChannelRefusalIsFinal: a device that says it cannot use a channel must
// not be asked again, or the exchange loops until someone intervenes.
func TestNewChannelRefusalIsFinal(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	if err := env.Engine.RequestNewChannel(ctx, devEUI, 3, 867700000, 0, 5); err != nil {
		t.Fatalf("RequestNewChannel: %v", err)
	}
	ask(t, env, simDev, sim.UplinkOptions{NoFPort: true})
	answer(t, env, simDev, lorawan.MACCommandSet{
		{CID: lorawan.CIDNewChannel, Payload: []byte{0x00}}, // both bits cleared
	})

	ses := env.Session(devEUI)
	if ses.PendingChannel != nil {
		t.Errorf("a refused request must be dropped, not retried: %+v", ses.PendingChannel)
	}
	if len(ses.UserChannels) != 0 {
		t.Errorf("a refused channel must not be added: %+v", ses.UserChannels)
	}
	if countEventsWithReason(env, "new_channel_refused") != 1 {
		t.Errorf("the refusal was not recorded: %+v", env.Store.Events())
	}
	if cmd, ok := findCommand(ask(t, env, simDev, sim.UplinkOptions{NoFPort: true}), lorawan.CIDNewChannel); ok {
		t.Errorf("the refused request was re-sent: %+v", cmd)
	}
}

// TestOutstandingRequestIsRepeatedUntilAnswered is the reason requests live on
// the session instead of riding one downlink: a lost frame must not cost the
// configuration permanently.
func TestOutstandingRequestIsRepeatedUntilAnswered(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	if err := env.Engine.RequestDutyCycle(ctx, devEUI, 4); err != nil {
		t.Fatalf("RequestDutyCycle: %v", err)
	}

	for i := 0; i < 3; i++ {
		cmd, ok := findCommand(ask(t, env, simDev, sim.UplinkOptions{NoFPort: true}), lorawan.CIDDutyCycle)
		if !ok {
			t.Fatalf("uplink %d: no DutyCycleReq in the downlink", i)
		}
		if len(cmd.Payload) != 1 || cmd.Payload[0] != 4 {
			t.Fatalf("DutyCycleReq payload = % x, want 04", cmd.Payload)
		}
	}

	answer(t, env, simDev, lorawan.MACCommandSet{{CID: lorawan.CIDDutyCycle}})

	// RP002 Table 13: index 4 is one sixteenth.
	if ses := env.Session(devEUI); ses.DutyCycle != 1.0/16 {
		t.Errorf("DutyCycle = %v, want 0.0625", ses.DutyCycle)
	}
	if cmd, ok := findCommand(ask(t, env, simDev, sim.UplinkOptions{NoFPort: true}), lorawan.CIDDutyCycle); ok {
		t.Errorf("the request was still sent after the answer: %+v", cmd)
	}
}

// TestRX1DelayAppliesOnAnswer: the delay must not take effect before the device
// confirms it, or the server would listen in a window the device is not in.
func TestRX1DelayAppliesOnAnswer(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	before := env.Session(devEUI).Rx1Delay
	if err := env.Engine.RequestRX1Delay(ctx, devEUI, 8); err != nil {
		t.Fatalf("RequestRX1Delay: %v", err)
	}

	cmd, ok := findCommand(ask(t, env, simDev, sim.UplinkOptions{NoFPort: true}), lorawan.CIDRXTimingSetup)
	if !ok {
		t.Fatal("no RXTimingSetupReq in the downlink")
	}
	if len(cmd.Payload) != 1 || cmd.Payload[0] != 8 {
		t.Errorf("RXTimingSetupReq payload = % x, want 08", cmd.Payload)
	}
	if ses := env.Session(devEUI); ses.Rx1Delay != before {
		t.Errorf("RX1 delay changed before the answer: %d, was %d", ses.Rx1Delay, before)
	}

	answer(t, env, simDev, lorawan.MACCommandSet{{CID: lorawan.CIDRXTimingSetup}})
	if ses := env.Session(devEUI); ses.Rx1Delay != 8 {
		t.Errorf("Rx1Delay = %d, want 8", ses.Rx1Delay)
	}
}

// TestRequestValidation checks that nonsense is refused where it is typed rather
// than discovered on the air.
func TestRequestValidation(t *testing.T) {
	env := testenv.New(t, nil)
	ctx := context.Background()
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)
	env.Join()

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"duty cycle index too high", env.Engine.RequestDutyCycle(ctx, devEUI, 16)},
		{"duty cycle index negative", env.Engine.RequestDutyCycle(ctx, devEUI, -1)},
		{"RX1 delay of zero", env.Engine.RequestRX1Delay(ctx, devEUI, 0)},
		{"RX1 delay above 15 s", env.Engine.RequestRX1Delay(ctx, devEUI, 16)},
		{"max EIRP index too high", env.Engine.RequestTXParams(ctx, devEUI, true, true, 16)},
		// EU868 has no dwell-time rule, so the command would be noise.
		{"TX params on a band without dwell time", env.Engine.RequestTXParams(ctx, devEUI, true, true, 0)},
		// A frequency the on-air field cannot express would be silently rounded.
		{"frequency not a multiple of 100 Hz", env.Engine.RequestNewChannel(ctx, devEUI, 3, 867700050, 0, 5)},
		{"inverted data-rate range", env.Engine.RequestNewChannel(ctx, devEUI, 3, 867700000, 5, 2)},
		// DR14 does not exist in the EU868 table at all.
		{"data rate that does not exist in the band", env.Engine.RequestNewChannel(ctx, devEUI, 3, 867700000, 0, 14)},
		{"channel index above the protocol maximum", env.Engine.RequestNewChannel(ctx, devEUI, 300, 867700000, 0, 5)},
	} {
		if tc.err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}

	// A device that has not joined has no session to hold a request.
	unknown := testenv.MustEUI64(t, "0000000000000099")
	if err := env.Engine.RequestDutyCycle(ctx, unknown, 1); err == nil {
		t.Error("a request for an unknown device must be refused")
	}
}

// TestUnexpectedAnswerIsRecordedNotApplied: a device may answer a request that
// was already resolved (a duplicate retransmission, for instance). Applying it
// twice would be harmless here, but silently ignoring it would hide a device
// that is confused about its own state.
func TestUnexpectedAnswerIsRecordedNotApplied(t *testing.T) {
	env := testenv.New(t, nil)
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)

	simDev, _ := env.Join()
	answer(t, env, simDev, lorawan.MACCommandSet{
		{CID: lorawan.CIDNewChannel, Payload: []byte{0x03}},
	})

	ses := env.Session(devEUI)
	if len(ses.UserChannels) != 0 {
		t.Errorf("an answer to no request added a channel: %+v", ses.UserChannels)
	}
	if countEventsWithReason(env, "unexpected_mac_answer") != 1 {
		t.Errorf("the stray answer was not recorded: %+v", env.Store.Events())
	}
}
