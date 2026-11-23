// Package vocabulary is the plain-English half of the event vocabulary.
//
// The network server records a machine-readable kind and reason rather than prose,
// which is the right choice for storage and wrong for a conversation: an agent that
// reads "f_cnt_gap_too_large" has to guess, and a guessed diagnosis on a live
// network is worse than no diagnosis. This package is the other half — what each
// recorded reason means and what usually causes it — written from the documentation
// in ns/internal/engine/events.go, which is the file that owns the vocabulary.
//
// The drift test in vocabulary_test.go reads that file as text and fails if a reason
// appears there without an entry here. The values are the outward contract; this is
// the commentary, and commentary that has quietly fallen behind is worse than none.
package vocabulary

import "sort"

// Entry is what one recorded reason means.
type Entry struct {
	// Summary is what the network actually recorded, in one line.
	Summary string
	// Cause is what usually produces it. It says "usually" deliberately: these are
	// diagnoses, not proofs, and the detail field on the event is the evidence.
	Cause string
	// Where names the code to read next, in the style of the troubleshooting table
	// in ns/README.md. Empty when there is no single place worth opening.
	Where string
}

// entries covers every reason the engine records. Keep it in the same order as the
// vocabulary file: refusals first, then the MAC exchange.
var entries = map[string]Entry{
	"unknown_device": {
		Summary: "a join-request arrived for a DevEUI that is not provisioned",
		Cause:   "the device was never added, or its DevEUI was typed wrong when it was",
		Where:   "ns/cmd/ns/api_devices.go",
	},
	"device_disabled": {
		Summary: "a frame was dropped from a device an operator switched off",
		Cause:   "the device is provisioned with enabled=false; this is intended, not a fault",
		Where:   "ns/cmd/ns/api_devices.go",
	},
	"not_otaa_device": {
		Summary: "an OTAA join-request came from a device provisioned for ABP",
		Cause:   "the device was given dev_addr and session keys, but it is trying to join",
		Where:   "ns/internal/engine/join.go",
	},
	"join_mic_failed": {
		Summary: "the message authentication code on a join-request did not verify",
		Cause: "usually a wrong AppKey. It also covers a corrupted frame, so a marginal " +
			"link can look exactly like a wrong key",
		Where: "ns/internal/engine/join.go",
	},
	"dev_nonce_replay": {
		Summary: "the DevNonce on a join-request had already been used by that device",
		Cause: "either a genuine replay, or a device that lost its own state and started " +
			"reusing DevNonces",
		Where: "ns/internal/engine/join.go",
	},
	"unknown_dev_addr": {
		Summary: "a data frame carried a DevAddr that matches no session",
		Cause: "the device has not joined this network, or it joined a different one, or " +
			"the network was reset and lost the session while the device kept its own",
		Where: "ns/internal/engine/uplink.go",
	},
	"uplink_mic_failed": {
		Summary: "the message authentication code on a data uplink did not verify",
		Cause: "usually the device and the network hold different NwkSKey values, most " +
			"often after a join-accept the device never received",
		Where: "ns/internal/lorawan/phy_security.go",
	},
	"f_cnt_replay": {
		Summary: "the frame counter is at or below the highest one already accepted",
		Cause: "the frame is old or replayed. A device that restarted its counter without " +
			"rejoining produces this repeatedly",
		Where: "ns/internal/lorawan/fcnt.go",
	},
	"f_cnt_gap_too_large": {
		Summary: "the frame counter jumped forward further than the accepted window allows",
		Cause: "many frames were lost, or the device restarted its counter while the " +
			"network kept the session",
		Where: "ns/internal/lorawan/fcnt.go",
	},
	"duplicate_frame": {
		Summary: "a second copy of a frame that was already handled",
		Cause: "another gateway heard the same transmission, or the device retransmitted " +
			"it. Deduplication working as designed, not a fault",
		Where: "ns/internal/engine/uplink.go",
	},
	"no_rx_window_available": {
		Summary: "no gateway could answer in either RX window",
		Cause: "the gateway that heard the uplink cannot transmit — usually it has never " +
			"sent PULL_DATA, so the server has no address to send to",
		Where: "ns/internal/engine/downlink.go",
	},
	"downlink_send_failed": {
		Summary: "the gateway transport refused the transmission",
		Cause: "classically a forwarder that has sent PULL_DATA to a different address, or " +
			"concentrator timing errors such as TOO_LATE and TX_COL",
		Where: "ns/internal/gateway/semtech/send.go",
	},
	"downlink_too_large_for_window": {
		Summary: "no available RX window could carry the frame at a legal data rate",
		Cause: "the payload is too big for the device's current data rate, so it was never " +
			"transmitted at all",
		Where: "ns/internal/engine/downlink.go",
	},
	"duty_cycle_denied": {
		Summary: "the band's regulatory limits left no room to transmit",
		Cause: "four different refusals share this reason, and only two of them are about " +
			"budget: the duty cycle was exhausted, the frame exceeded dwell time, the frame " +
			"was larger than the whole remaining budget, or the frequency fell outside every " +
			"sub-band the band allows. Read the detail to tell them apart — the last one is a " +
			"configuration error rather than congestion, and waiting will never clear it",
		Where: "ns/internal/engine/dutycycle.go",
	},
	"join_nonce_exhausted": {
		Summary: "the server has handed out every JoinNonce the 24-bit field allows",
		Cause: "the device holds the nonce as part of its joined state, so re-provisioning " +
			"the network does not recover it — the device has to be reset and joined again",
		Where: "ns/internal/engine/join.go",
	},

	// The MAC exchange: where "it works but slowly" and "it keeps resetting" live.
	"f_cnt_reset": {
		Summary: "a frame counter went backwards without a join in between",
		Cause: "the device restarted its counter while the network kept the session, so " +
			"the two now disagree about where the stream is",
		Where: "ns/internal/engine/mac.go",
	},
	"adr_decision": {
		Summary: "ADR chose new radio parameters for the device",
		Cause: "normal operation. The event's detail carries the SNR arithmetic behind the " +
			"choice, which is what makes it checkable rather than mysterious",
		Where: "ns/internal/engine/adr.go",
	},
	"adr_keepalive": {
		Summary: "a LinkADRReq was sent that changes nothing, on purpose",
		Cause: "it exists to reset the device's own ADR counter so it does not fall back to " +
			"a lower data rate. Not a change and not a fault",
		Where: "ns/internal/engine/requests.go",
	},
	"adr_abandoned": {
		Summary: "a LinkADRReq was given up on after the configured retries",
		Cause: "the device never answered it, so it keeps its current parameters. This is " +
			"the usual explanation for a device stuck at a low data rate",
		Where: "ns/internal/engine/adr.go",
	},
	"link_adr_rejected": {
		Summary: "the device answered a LinkADRReq negatively or only partly",
		Cause: "it refused the data rate, power or channel mask it was offered — often a " +
			"device that cannot physically transmit at the rate requested",
		Where: "ns/internal/engine/mac.go",
	},
	"new_channel_refused": {
		Summary: "the device answered a NewChannelReq negatively",
		Cause: "it cannot use the user channel it was offered, usually a firmware or " +
			"regional-certification limit rather than a network fault",
		Where: "ns/internal/engine/mac.go",
	},
	"unexpected_mac_answer": {
		Summary: "an answer arrived with no outstanding request behind it",
		Cause: "usually a device replying to a command that was already applied, or one " +
			"that was lost on the way to it",
		Where: "ns/internal/engine/mac.go",
	},
	"malformed_mac_payload": {
		Summary: "a MAC command's payload did not decode, so it was ignored",
		Cause: "the command was not guessed at. Either the device is sending something " +
			"non-standard or the frame was corrupted",
		Where: "ns/internal/engine/mac.go",
	},
	"mac_commands_truncated": {
		Summary: "a downlink's FOpts space was too small for every queued command",
		Cause: "too many commands were pending at once. The detail names what was left out, " +
			"and they are re-sent in a later window",
		Where: "ns/internal/engine/requests.go",
	},
	"downlink_abandoned": {
		Summary: "a pending downlink was given up on after its attempts ran out",
		Cause: "for a confirmed frame this means the application payload was discarded — " +
			"the one reason string that means data was actually lost",
		Where: "ns/internal/engine/downlink.go",
	},
}

// Lookup returns the entry for a recorded reason.
//
// The second result matters as much as the first: an unknown reason is either a
// typo or a vocabulary that grew faster than this commentary, and an agent should be
// told which rather than handed an empty explanation to improvise over.
func Lookup(reason string) (Entry, bool) {
	e, ok := entries[reason]
	return e, ok
}

// Reasons returns every reason described here, sorted, so a rejection message can
// list them in an order that does not change between calls.
func Reasons() []string {
	out := make([]string, 0, len(entries))
	for reason := range entries {
		out = append(out, reason)
	}
	sort.Strings(out)
	return out
}
