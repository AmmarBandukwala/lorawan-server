package prompts

// The runbooks. Each is the order an operator follows, with the reason the order is
// that way attached, because the order is the part that prevents an expensive mistake.
//
// {{dev_eui}} is substituted rather than a fmt verb, so nothing in this prose can ever
// be read as a format specifier.

const diagnoseDeviceBody = `Work out why device {{dev_eui}} is not doing what it should.

Call the tools in this order. It goes from cheapest and widest to most specific, and
each step can end the investigation before the expensive ones start.

1. get_device on {{dev_eui}}. If this is refused, stop: the network has no record of
   that DevEUI, so it cannot have a session and every frame it sends is refused.
   Nothing further will explain anything. Check the DevEUI against its label, and
   against list_devices if you are not sure which devices are on this network at all.

2. Read enabled and activation from that same answer. enabled=false means the network is
   refusing the device on purpose, which is not a fault and is not fixed by anything
   below. activation says whether a join is even expected.

3. get_device_session on {{dev_eui}}. No session means never joined, which points at
   step 4. A session means it did join and something else is wrong, which points at
   steps 5 and 6.

4. list_events with dev_eui={{dev_eui}} and kind=join_reject, then again with
   kind=join_request. A join_request with no join_accept and no join_reject means the
   frames are not reaching the network, which is a gateway or aerial problem, not a
   configuration one. For any reason you did not expect, call explain_event_reason
   before describing it.

5. list_events with dev_eui={{dev_eui}} and no kind filter. This is where refusals are
   recorded: uplink_mic_failed, f_cnt_replay, f_cnt_gap_too_large, unknown_dev_addr. The
   detail field is the evidence; the reason is only the label.

6. list_uplinks with dev_eui={{dev_eui}}. Frames present but decoded empty means the
   payload formatter is the problem, not the radio. Check has_codec before concluding
   anything: false means no formatter is configured, which is a different fact from a
   formatter that rejected the frame.

7. network_status, last, to check the ground the device stands on. A gateway offline or
   a duty-cycle denial explains several devices at once, and if it does, you are
   debugging the wrong thing.

Prefer diagnose_device for steps 1 through 7 in one call when you want the summary
rather than the full records.

Do not write anything during this procedure. Queueing a frame, cancelling one or
changing the configuration does not diagnose anything, and a battery report occupies the
device's single pending slot, which delays whatever the application wanted sent.`

const whyNoDownlinkBody = `A downlink to {{dev_eui}} was accepted and never reached the device. Work out why.

First, the thing most often mistaken for a fault: the network server answers 202 because
the frame is queued, not sent. A class-A device listens for about two seconds after it
transmits and then goes quiet until it next speaks, so a queued frame may wait hours. If
the device has not reported since the frame was queued, nothing is wrong yet and the
investigation stops here.

Do not re-send the frame. Each device holds one pending frame, so a retry is refused with
a 409 saying a frame already waits. That 409 is the correct outcome, not a failure to
work around. To replace a frame, cancel_downlink first and then enqueue_downlink.

Then, in order:

1. get_device_session on {{dev_eui}}. If pending_downlink is present, the frame is
   queued and waiting, which is the expected state. If it is absent and you queued one,
   it has already gone out or been cancelled.

2. list_events with dev_eui={{dev_eui}} and kind=downlink. Look for a downlink event
   with a reason. The three that matter:
     no_rx_window_available  no gateway could answer in either window
     downlink_send_failed    the transport refused it; see step 3
     duty_cycle_denied       a regulatory refusal, not a fault; see step 4
   Call explain_event_reason on any of them before describing it to anyone.

3. network_status, and read the gateway that served this device. A gateway counts as
   online because it recently sent PULL_DATA. If it is offline, or its last_tx_ack_error
   names TOO_LATE or TX_COL, the concentrator could not transmit. That is the answer, and
   it affects every device behind that gateway, not just this one.

4. network_status again, reading duty_cycle. A non-zero denial count with a sub-band name
   means the band's duty cycle left no room. The event detail says which limit was hit
   and when budget frees up. This resolves on its own; retrying does not speed it up.

5. Check the size. downlink_too_large_for_window means no available window could carry
   the frame at a legal data rate. A device pinned to a low data rate by disable_adr, or
   one far from every gateway, has a much smaller window than the 51-byte payload limit
   suggests.

Class C is not implemented by this network server, so there is no way to reach a device
outside its own windows. If the expectation was that it should arrive immediately, that
expectation is the finding.`

const fleetHealthText = `Take a short pass over the whole network before changing anything on any device.

1. network_status. Read it in this order and stop early when something jumps out:
     gateways_online against gateways_total   a gateway down explains a group of devices
     uptime_seconds                           a value that resets is a restart
     transports                               a transport listening on nothing means a
                                              whole class of gateway cannot connect
     duty_cycle denials                       non-zero means the band, not the network,
                                              is refusing transmissions

2. list_devices. Count what is enabled and what is not. A disabled device is not a fault
   and should not be investigated as one.

3. list_events with no dev_eui filter. This is the only way to see frames the network
   could not attribute to any session, and a cluster of them from unknown DevAddrs is a
   different problem from the same reasons on devices you recognise — usually a device
   that joined a different network, or a network that was reset and lost its sessions.

4. list_uplinks with no filter, comparing has_codec and decode_error across devices. A
   formatter that has quietly broken looks identical to a fleet with no formatters unless
   you compare those two columns.

Then decide which of the two you are looking at, and say so before acting:

  A network-wide fault    one gateway down, a transport not listening, duty-cycle
                          denials, a recent restart. Fixing one thing fixes many devices.
                          Do not touch device configuration for this.

  Per-device faults       the same reason strings on individual devices while the network
                          itself is quiet. These need diagnose-device, one device at a
                          time.

Nothing in this pass writes. A sweep that changes configuration on its way past turns a
diagnosis into an incident.`
