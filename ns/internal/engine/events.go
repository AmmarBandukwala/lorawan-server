package engine

// Event kinds and reasons recorded by the engine.
//
// This file is the complete vocabulary of what the network server reports about
// traffic. It lives on its own, away from the store types, because it is the
// first thing to read when the question is "why did this frame not get through":
// every string that can appear in an event's kind or reason column is here, once,
// with what it means and what usually causes it.
//
// Both values are part of the outward contract. They are written to storage,
// served by the status and device endpoints, forwarded to integrations and shown
// in the dashboard, so renaming one is a breaking change for anyone filtering on
// it. Add to this file rather than writing a new literal at the call site, and
// keep the value stable once it has been recorded anywhere.

// Event kinds.
const (
	EventJoinRequest = "join_request"
	EventJoinAccept  = "join_accept"
	EventJoinReject  = "join_reject"
	EventUplink      = "uplink"
	EventDownlink    = "downlink"
	EventUplinkDrop  = "uplink_drop"
	EventMAC         = "mac"
	EventError       = "error"
)

// Reasons recorded with a rejected join or a dropped uplink: the frame never
// became application data.
const (
	// ReasonUnknownDevice is a join-request for a DevEUI that is not provisioned.
	ReasonUnknownDevice = "unknown_device"
	// ReasonDeviceDisabled is a frame from a device an operator switched off.
	ReasonDeviceDisabled = "device_disabled"
	// ReasonNotOTAA is an OTAA join from a device provisioned for ABP.
	ReasonNotOTAA = "not_otaa_device"
	// ReasonJoinMICFailed covers a wrong AppKey and a corrupted frame alike: the
	// message authentication code is the only thing being tested.
	ReasonJoinMICFailed = "join_mic_failed"
	// ReasonDevNonceReplay is a join-request whose DevNonce was already used,
	// which is either a genuine replay or a device that lost its own state.
	ReasonDevNonceReplay = "dev_nonce_replay"
	// ReasonUnknownDevAddr is a data frame whose DevAddr matches no session.
	ReasonUnknownDevAddr = "unknown_dev_addr"
	// ReasonUplinkMICFailed usually means the session keys disagree: the device
	// and the network hold different NwkSKey values, most often after a join the
	// device did not receive.
	ReasonUplinkMICFailed = "uplink_mic_failed"
	// ReasonFCntReplay is a counter at or below the highest one accepted, so the
	// frame is old or replayed.
	ReasonFCntReplay = "f_cnt_replay"
	// ReasonFCntGapTooLarge is a forward jump beyond the configured window, which
	// means frames were lost or the device restarted its counter.
	ReasonFCntGapTooLarge = "f_cnt_gap_too_large"
	// ReasonDuplicateFrame is a second copy of a frame already handled, from a
	// gateway that received it again or retransmitted it.
	ReasonDuplicateFrame = "duplicate_frame"
	// ReasonNoRXWindow means no gateway was available to answer in either window.
	ReasonNoRXWindow = "no_rx_window_available"
	// ReasonDownlinkSendFailed is the transport refusing the transmission, for
	// example a gateway that has never sent PULL_DATA and so has no address.
	ReasonDownlinkSendFailed = "downlink_send_failed"
	// ReasonDownlinkTooLarge is a frame that no available window can carry at a
	// legal data rate, so it was never transmitted.
	ReasonDownlinkTooLarge = "downlink_too_large_for_window"
	// ReasonDutyCycleDenied means the band's duty cycle or dwell time left no
	// room for the transmission. It is a regulatory refusal, not a fault, and
	// the detail carries which limit was hit and when budget frees up again.
	ReasonDutyCycleDenied = "duty_cycle_denied"
	// ReasonJoinNonceExhausted means the server has handed out every JoinNonce
	// the 24-bit field allows. It is not recoverable by re-provisioning the
	// network: the device holds the nonce as part of its joined state, so the
	// device has to be reset and provisioned again.
	ReasonJoinNonceExhausted = "join_nonce_exhausted"
)

// Reasons recorded with EventMAC: the exchange about radio parameters, which is
// where a link that "works but slowly" or "keeps resetting" is explained.
const (
	// ReasonFCntReset is a frame counter that went backwards without a join, so
	// the device restarted its counter while the network kept the session.
	ReasonFCntReset = "f_cnt_reset"
	// ReasonADRDecision is the decision ADR took, with the SNR arithmetic in the
	// detail.
	ReasonADRDecision = "adr_decision"
	// ReasonADRKeepalive is a LinkADRReq that changes nothing, sent only to reset
	// the device's own ADR counter so it does not fall back to a lower data rate.
	ReasonADRKeepalive = "adr_keepalive"
	// ReasonADRAbandoned is a LinkADRReq given up on after the configured
	// retries; the device keeps its current parameters.
	ReasonADRAbandoned = "adr_abandoned"
	// ReasonLinkADRRejected is a negative or partial LinkADRAns: the device
	// refused the data rate, power or channel mask it was offered.
	ReasonLinkADRRejected = "link_adr_rejected"
	// ReasonNewChannelRefused is a negative NewChannelAns: the device cannot use
	// the user channel it was offered.
	ReasonNewChannelRefused = "new_channel_refused"
	// ReasonUnexpectedMACAnswer is an answer with no outstanding request behind
	// it, usually a device replying to a command that was already applied or that
	// was lost in the other direction.
	ReasonUnexpectedMACAnswer = "unexpected_mac_answer"
	// ReasonMalformedMACPayload is a MAC command whose payload does not decode,
	// so the command was ignored rather than guessed at.
	ReasonMalformedMACPayload = "malformed_mac_payload"
	// ReasonMACCommandsTruncated is a downlink whose FOpts space was too small
	// for every command queued; the detail names what was left out.
	ReasonMACCommandsTruncated = "mac_commands_truncated"
	// ReasonDownlinkAbandoned is a pending downlink given up on after its
	// attempts were used up, which for a confirmed frame means the application
	// payload was discarded.
	ReasonDownlinkAbandoned = "downlink_abandoned"
)
