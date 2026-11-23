package assistant

// corpus is the reference material. It is written in the words an operator uses rather
// than the words the specification uses, because lexical retrieval only matches what is
// actually typed into the box.
//
// Each note states what this build does, not what LoRaWAN permits. Where the two
// differ, that is the fact worth knowing, and it is the one a model answering from
// general knowledge gets wrong.
func corpus() []passage {
	return []passage{
		{
			Title: "When a device seems silent or offline",
			Body: `A class-A device listens for about two seconds after each uplink and then
sleeps until its next scheduled transmission, which may be minutes or hours away.
Nothing reaching it in between is normal, not a fault.

Before concluding a device is broken, check three things in this order: whether the
device is enabled at all (a disabled device is refused on purpose and looks identical to
a dead one), when its session last reported, and how often it is supposed to transmit.
A device whose last uplink was two hours ago and which reports hourly is healthy.

If the device is transmitting but nothing appears, the frames are arriving and being
refused. Look at the events with no device filter as well as with it: a frame the network
cannot match to any session is recorded without a DevEUI, so filtering by device hides
exactly the case where the device is talking and the network does not know who it is.`,
		},
		{
			Title: "Join failures: OTAA, ABP, and why a device will not join",
			Body: `OTAA is the normal path. The device sends a join-request naming a JoinEUI, the
network checks the DevEUI is provisioned and that the AppKey produces a valid message
authentication code, and answers with a join-accept giving it a DevAddr and session keys.
ABP instead means the device was given a DevAddr and session keys at provisioning time and
never joins at all; it just transmits.

The common causes, each with the reason the network records:
- unknown_device: the DevEUI is not provisioned, or was typed wrong. The device is not on
  this network, and no amount of radio work will change that.
- join_mic_failed: usually a wrong AppKey. It also covers a corrupted frame, so a marginal
  link can look exactly like a wrong key.
- not_otaa_device: an OTAA join from a device provisioned for ABP.
- dev_nonce_replay: the device reused a DevNonce, either a genuine replay or a device that
  lost its own state.
- unknown_dev_addr: a data frame whose DevAddr matches no session. The device joined a
  different network, or this network was reset and lost the session while the device kept
  its own.

A device that has never joined has no session, and a session read saying so is a
different fact from a request that failed.`,
		},
		{
			Title: "Frame counters: replay, gap and reset",
			Body: `Every uplink carries a counter that must increase. The network keeps the highest
it has accepted and refuses anything that does not fit the window.

- f_cnt_replay: the counter is at or below the highest accepted, so the frame is old or
  replayed. A device that restarted its counter without rejoining produces this
  repeatedly.
- f_cnt_gap_too_large: a forward jump beyond the window. Either many frames were lost, or
  the device restarted its counter while the network kept the session.
- f_cnt_reset: recorded on the MAC channel when a counter went backwards with no join in
  between. The device and the network now disagree about where the stream is, and the
  usual fix is to make the device rejoin rather than to widen the window.

Widening the counter window to make the errors stop accepts frames that may be replays.
It hides the fault rather than resolving it.`,
		},
		{
			Title: "Data rate, ADR, and a device stuck at the slowest setting",
			Body: `ADR is the network telling a device to change its data rate, transmit power or
channel mask, based on the signal-to-noise ratio it measured. It only moves upwards when
the link margin is comfortably positive, because raising the data rate shortens range.

- adr_decision: the network chose new parameters. Normal, and the event detail carries the
  arithmetic behind the choice.
- adr_keepalive: a LinkADRReq that changes nothing, sent only to reset the device's own ADR
  counter so it does not fall back. Not a change and not a fault.
- adr_abandoned: the network gave up after its retries because the device never answered.
  This is the usual explanation for a device stuck at a low data rate.
- link_adr_rejected: the device refused what it was offered, often because it cannot
  physically transmit at the rate requested.

A device sitting at the slowest data rate is usually showing adr_abandoned or
link_adr_rejected in its event history, not a network fault. Turning ADR off for one
device is a legitimate answer when the device cannot honour the requests; it is a bad
first answer.`,
		},
		{
			Title: "Gateways: presence, PULL_DATA, and the two transports",
			Body: `A gateway counts as online because it recently sent PULL_DATA to the network.
That is presence, not health: a gateway can be online and still failing to transmit.

The classic trap is a gateway that receives perfectly and never answers. It usually means
the forwarder sent its PULL_DATA from a different address than the one it listens on, so
the network has no address to send downlink traffic back to. The symptom is uplinks
arriving with strong signal and no response ever following.

Two transports are supported: the Semtech UDP protocol on port 1700 and LoRa Basics
Station over WebSocket on port 30001. A gateway appears under one or the other, and a
gateway configured for the wrong one simply never shows up.

Transmission failures are reported by the concentrator and recorded per gateway: TOO_LATE
means the scheduled time had already passed, TX_COL means another transmission was in
progress on the same channel, and CRC_FAILURE on the receive side is a corrupted frame.`,
		},
		{
			Title: "Duty cycle and dwell time: a refusal, not a fault",
			Body: `Sub-band licences in Europe and elsewhere limit how much of the time a gateway may
transmit, typically one percent on the main sub-bands, plus a dwell limit per channel. The
network tracks this per gateway and per sub-band and refuses a transmission that would
exceed it rather than breaking the law.

duty_cycle_denied means exactly that. The event detail names which limit was hit and when
budget frees up again. It is a regulatory refusal, and no setting on the network makes it
stop.

What actually helps: fewer downlinks, shorter downlinks, moving traffic to a different
sub-band, or adding a gateway so the load is spread. One gateway serving many devices will
hit this, and the answer is more gateways rather than a different configuration.`,
		},
		{
			Title: "Downlinks: one pending frame, and a queue is not a transmission",
			Body: `A downlink is never sent on demand. The network stores it as the device's single
pending frame and transmits it in the next RX window that follows an uplink from that
device. For a device reporting hourly that may be an hour away.

Each device holds exactly one pending frame. Anything else wanting the slot is refused
while something already holds it, so replacing a frame means cancelling the pending one
first, not queueing again. A request that is accepted answers 202 with a message saying
the frame goes out in the device's next RX window; that is success, not a partial
failure.

A pending frame is visible on the session read. A frame that was abandoned after its
attempts ran out is recorded as downlink_abandoned, and for a confirmed frame that means
the application payload was discarded.`,
		},
		{
			Title: "What a session is, and what its fields mean",
			Body: `A session is what the network holds about a joined device: its DevAddr, the band
it is on, both frame counters, its data rate and transmit power, whether ADR is active,
the RX window settings, the last signal strength and signal-to-noise ratio measured, and
any downlink waiting for it.

A session that does not exist means the device has never joined this network, or its
session was removed. That is a different fact from a request that failed, and it is worth
saying which one you found.

Battery and link margin only appear if the device ever answered a DevStatusReq. A
battery value of 255 means the device cannot report a level, not that it is full. An
absent battery field means the network never asked or the device never answered.`,
		},
		{
			Title: "Payload formatters, device models, and decoded readings",
			Body: `A device model is a kind of sensor plus the JavaScript formatter that turns its
bytes into named readings. A device is an instance of a model. A device with no model
still joins, still reports and its frames are still stored as hex; a model decides how
bytes are read, not whether the network accepts them.

has_codec on an uplink is the field that separates the two very different reasons a
reading is missing: false means no formatter is configured, and a decode_error means a
formatter ran and rejected this frame. A formatter that has quietly stopped working looks
identical to a fleet with no formatters unless you check that field.

Changing a model affects new uplinks only. Stored readings keep whatever decoded them, so
history does not rewrite itself when a formatter is corrected.`,
		},
		{
			Title: "Integrations: where events go, and what delivery counters mean",
			Body: `An integration is a destination the network delivers events to: an MQTT broker, an
HTTP webhook, or a message queue. Before concluding that a downstream system is losing
data, check whether the event was ever sent: an event that was never dispatched is a
different problem from one that was dispatched and dropped.

Delivery counters are for the whole dispatcher rather than per destination, so they show
the aggregate of accepted, refused and failed deliveries rather than attributing a
failure to one target. Destinations that come from the server's own environment rather
than from configuration are read-only even for an operator.`,
		},
		{
			Title: "Regions, the NetID, and what changing the band does",
			Body: `The network runs on one regional band at a time, set at startup and changeable
through the console's facility settings. The band decides the channels, the data rates,
the duty-cycle limits and the RX window frequencies and offsets. It is a regulatory
setting, not a preference.

Changing the band does not reconfigure any device. Devices continue transmitting on the
frequencies they were programmed with, so a fleet provisioned for one band stops being
understood the moment the network moves to another. The usual symptom is every device
failing at once with unknown_dev_addr rather than anything mentioning the band.

The NetID identifies this network in join-accepts. Changing it invalidates nothing that
is already joined, but a device that rejoins will get a DevAddr from the new NetID.`,
		},
		{
			Title: "What this build does not implement",
			Body: `Do not describe any of these as available, however standard they are:
- Class C devices. Everything is class A, so there is no continuous receive window and no
  way to reach a device outside the short window after its own uplink.
- LR-FHSS modulation.
- RoT (roaming), and no external JoinServer: joining is handled locally.
- Creating a device, changing an integration or installing a payload formatter through the
  assistant. Those exist in the console, behind their own confirmation, and not here.

When asked for one of these, say it is not implemented in this build and say what the
closest supported thing is, rather than describing the standard behaviour as though it
were available.`,
		},
	}
}
