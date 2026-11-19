package engine_test

import (
	"context"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/gateway"
	"github.com/fieldnode/lorawan-server/ns/internal/region"
	"github.com/fieldnode/lorawan-server/ns/internal/sim"
	"github.com/fieldnode/lorawan-server/ns/internal/testenv"
)

// US915 is the band Fieldnode deploys on by default, and the one whose rules
// differ most from the EU868 assumptions the rest of the suite was written
// against: RX1 is on a different frequency than the uplink, the RX1 data rate
// comes from a lookup table rather than an offset, and there are 72 uplink
// channels instead of three.
//
// These scenarios run the same engine paths the EU868 ones do, on the real band
// tables, so a US915 regression fails here rather than on a rooftop.

// TestUS915JoinPlacesRX1OnTheMappedChannel is the US915 bug this band is
// notorious for. Answering on the uplink frequency means the device never hears
// the join-accept: it joins in the server's database and stays silent forever.
func TestUS915JoinPlacesRX1OnTheMappedChannel(t *testing.T) {
	env := testenv.NewRegion(t, "US915", nil)
	simDev, frames := env.Join()

	if len(frames) < 2 {
		t.Fatalf("scheduled %d frame(s), want a join-accept in each window", len(frames))
	}
	var rx1, rx2 *gateway.DownlinkFrame
	for i := range frames {
		switch frames[i].Window {
		case gateway.RxWindow1:
			rx1 = &frames[i]
		case gateway.RxWindow2:
			rx2 = &frames[i]
		}
	}
	if rx1 == nil || rx2 == nil {
		t.Fatalf("join-accept windows = %s and %s, want both", frames[0].Window, frames[1].Window)
	}

	// Uplink on channel 0 (902.3 MHz) answers on downlink channel 0 at
	// 923.3 MHz, not on 902.3 MHz.
	if want := uint32(923_300_000); rx1.FrequencyHz != want {
		t.Errorf("RX1 frequency = %d Hz, want %d: US915 answers on the mapped "+
			"downlink channel, never on the uplink channel", rx1.FrequencyHz, want)
	}
	if rx1.FrequencyHz == env.UplinkFrequency() {
		t.Errorf("RX1 equals the uplink frequency %d Hz", rx1.FrequencyHz)
	}

	// RX1 data rate comes from the band's table: uplink DR3 with RX1DROffset 0
	// maps to DR13. This is ChirpStack's table (lrwn us915.rs
	// rx1_data_rate_table row 3 = [13, 12, 11, 10]), transcribed rather than
	// derived, so drift here means the two servers disagree about every US915
	// device's RX1.
	if want := 13; rx1.DataRateIndex != want {
		t.Errorf("RX1 data rate = DR%d, want DR%d", rx1.DataRateIndex, want)
	}

	// RX2 is the fixed US915 channel: 923.3 MHz at DR8.
	if rx2.FrequencyHz != 923_300_000 {
		t.Errorf("RX2 frequency = %d Hz, want 923300000", rx2.FrequencyHz)
	}
	if rx2.DataRateIndex != 8 {
		t.Errorf("RX2 data rate = DR%d, want DR8", rx2.DataRateIndex)
	}

	// The session must remember the band it joined under, because a later change
	// to NS_REGION must not re-interpret an existing session.
	ses := env.Session(testenv.MustEUI64(t, testenv.DevEUI))
	if ses.Region != "US915" {
		t.Errorf("session region = %q, want US915", ses.Region)
	}
	_ = simDev
}

// TestUS915UplinkAndDownlinkRoundTrip runs an application exchange on US915, so
// the session keys, frame counters and both windows are exercised on the default
// band rather than only on EU868.
func TestUS915UplinkAndDownlinkRoundTrip(t *testing.T) {
	ctx := context.Background()
	env := testenv.NewRegion(t, "US915", nil)
	devEUI := testenv.MustEUI64(t, testenv.DevEUI)
	simDev, _ := env.Join()

	env.Uplink(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte("us915")}, gateway.RxWindow1)

	// FCntUp is the next counter the engine expects, so after accepting the
	// device's first frame (counter 0) it reads 1.
	if ses := env.Session(devEUI); ses.FCntUp != 1 {
		t.Errorf("FCntUp = %d, want 1", ses.FCntUp)
	}

	if err := env.Engine.EnqueueDownlink(ctx, devEUI, 10, []byte("ack"), false); err != nil {
		t.Fatalf("EnqueueDownlink: %v", err)
	}
	// A class-A device only hears a downlink in the window after an uplink, so
	// the queued frame needs one more frame to open that window. Without this
	// the newest scheduled frame is still the join-accept, and the assertion
	// below would be about the wrong packet.
	env.Uplink(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte("ping")}, gateway.RxWindow1)

	frames := env.GW.Frames()
	last := frames[len(frames)-1]
	if last.FrequencyHz == env.UplinkFrequency() {
		t.Errorf("downlink went out on the uplink frequency %d Hz", last.FrequencyHz)
	}
	if last.FrequencyHz != 923_300_000 {
		t.Errorf("downlink frequency = %d Hz, want 923300000", last.FrequencyHz)
	}

	// And the device must be able to decrypt it, which proves the session keys
	// derived under US915 rules are consistent in both directions.
	dl, err := simDev.HandleDownlink(last.PHYPayload)
	if err != nil {
		t.Fatalf("the device could not decrypt the US915 downlink: %v", err)
	}
	if dl.FPort != 10 || string(dl.FRMPayload) != "ack" {
		t.Errorf("device read FPort %d payload %q, want FPort 10 payload %q",
			dl.FPort, dl.FRMPayload, "ack")
	}
}

// TestUS915ADRCeepsToALoRa125Rate checks the ceiling ADR may move a device to.
//
// US915's maximum uplink data rate is DR4, which is SF8 on 500 kHz. ADR reasons
// in 3 dB steps of spreading factor, and jumping from a 125 kHz rate to a 500 kHz
// one is far more than a step, so the algorithm narrows the ceiling to the
// fastest 125 kHz rate (DR3). Without that narrowing a device with a clean link
// would be pushed onto a modulation the step model never accounted for.
func TestUS915ADRCeepsToALoRa125Rate(t *testing.T) {
	env := testenv.NewRegion(t, "US915", nil)
	simDev, _ := env.Join()

	// A strong signal on every uplink: the link can afford the fastest rate the
	// narrowing allows.
	for i := 0; i < 25; i++ {
		env.UplinkSNR(simDev, sim.UplinkOptions{FPort: 10, Payload: []byte{byte(i)}}, 15.0)
	}

	ses := env.Session(testenv.MustEUI64(t, testenv.DevEUI))
	if ses.DataRate > 3 {
		t.Errorf("ADR moved the device to DR%d; the ceiling for 125 kHz uplinks is DR3",
			ses.DataRate)
	}

	// Confirm the band really does allow DR4, so the assertion above is about
	// ADR's narrowing and not about a table that never had DR4.
	us915, err := region.New("US915")
	if err != nil {
		t.Fatalf("region.New: %v", err)
	}
	dr4, err := us915.DataRate(4)
	if err != nil {
		t.Fatalf("US915 DR4: %v", err)
	}
	if dr4.BandwidthHz != 500_000 {
		t.Fatalf("US915 DR4 bandwidth = %d Hz, want 500000 (the test's premise)", dr4.BandwidthHz)
	}
}

// TestEveryBandJoinsAndHearsBack runs the join on each advertised band. It is
// deliberately thin: its job is to prove no band is a constructor that cannot
// carry a session, which is the failure mode a band newly added to the registry
// would otherwise ship with.
func TestEveryBandJoinsAndHearsBack(t *testing.T) {
	for _, name := range region.Names() {
		t.Run(name, func(t *testing.T) {
			env := testenv.NewRegion(t, name, nil)
			// Join() already decrypts the join-accept inside the simulator, so a
			// successful return is the proof this test is after: the device could
			// read what the engine scheduled on this band. Handling frames[0]
			// again would trip the simulator's JoinNonce replay check.
			_, frames := env.Join()

			if len(frames) == 0 {
				t.Fatalf("no join-accept scheduled on %s", name)
			}
			ses := env.Session(testenv.MustEUI64(t, testenv.DevEUI))
			if ses.Region != name {
				t.Errorf("session region = %q, want %q", ses.Region, name)
			}

			// RX1 must land on the frequency the band maps the uplink to. Getting
			// this wrong is invisible in the database and fatal on the air.
			reg, err := region.New(name)
			if err != nil {
				t.Fatalf("region.New: %v", err)
			}
			wantRX1, err := reg.RX1FrequencyHz(env.UplinkFrequency())
			if err != nil {
				t.Fatalf("RX1FrequencyHz: %v", err)
			}
			if frames[0].Window == gateway.RxWindow1 && frames[0].FrequencyHz != wantRX1 {
				t.Errorf("RX1 = %d Hz, want %d Hz for %s",
					frames[0].FrequencyHz, wantRX1, name)
			}
		})
	}
}
