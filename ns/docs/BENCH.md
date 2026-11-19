# Bench checklist (M6)

This is the procedure that closes M6. Everything before M6 was verifiable
without radio hardware: frames against published vectors, timing against
arithmetic, behaviour against ChirpStack's own test data. This part is not,
because it asks a question no simulation can answer — **does a real concentrator
do what the timestamp we sent implies?**

The server's downlink timing rests on one assumption: that a `tmst` value means
"transmit when your concentrator counter reaches this". Every forwarder family
implements that counter, and they do not all implement it identically. A frame
that is arithmetically correct and lands 2 ms late is a frame the device will
not hear, and nothing in the test suite can see that.

Work through it once per region, once per gateway family. Record the actual
numbers, not just pass or fail: a marginal 1.5 s RX1 that works today is worth
knowing about before a firmware update makes it 1.05 s.

## What this checklist is for

| Section | Closes |
|---|---|
| [Before the radio](#before-the-radio) | basic sanity, no RF needed |
| [Common checks](#common-checks-every-region) | join, uplink, downlink, MAC |
| [Downlink timing](#downlink-timing-the-actual-point) | the M6 hardware question |
| [Per region](#per-region-expected-values) | band tables against a real device |
| [Regulatory](#regulatory-enforcement) | duty cycle and dwell time |
| [Multi-gateway](#multi-gateway) | reception merging and transmitter choice |

## Before the radio

Do these with the gateway on the bench and the device in your hand, before
anything is mounted or the antenna is connected.

- [ ] `docker compose up` starts with no restart loop. `docker compose ps` shows
      `ns` healthy.
- [ ] `curl -s http://127.0.0.1:8090/status` answers. `gateways` is empty — that
      is correct at this point.
- [ ] The gateway's forwarder is pointed at this host's UDP **6858**. Within
      ~30 s the status endpoint lists the gateway EUI with `"online": true`.
      **If it does not appear, stop here**: nothing below works, and the cause
      is addressing, not the network server.
- [ ] Register the device in the console (Devices → Register a device). It
      appears as `Offline` / `Not joined`.
- [ ] `GET /api/v1/devices/{dev_eui}` returns the device and **no key material**.
      Confirm by reading the response, not by trusting the field list.

## Common checks (every region)

Record the frequency and data rate the device actually used; the server reports
what it received, and the two must agree.

- [ ] **Join.** Power the device. It sends `JOIN_REQUEST` and gets a
      `JOIN_ACCEPT`. The console shows the device online with a "Device joined"
      event. `GET .../session` now answers, with a `dev_addr`, `f_cnt_up` and
      the region you configured.
- [ ] **Uplink.** The device's first confirmed or unconfirmed uplink is decoded:
      the console shows the payload, RSSI and SNR. RSSI should move sensibly
      when you move the device — a pinned value means the forwarder is reporting
      the concentrator, not the packet.
- [ ] **Downlink.** Queue one from the console. The device receives it. Check
      `txnb` in the gateway's `stat` increments, and that no `TX_ACK` error
      appears in the server log (`TOO_LATE` and `TX_COL` are the two you will
      see first).
- [ ] **Confirmed downlink re-delivery.** Queue a confirmed frame while the
      device is asleep; on its next uplink the server must re-send it rather
      than consider it delivered.
- [ ] **MAC round trip.** Ask for battery/margin (`POST .../devstatus`). The
      device answers `DevStatusAns` and the console shows the reported battery
      and link margin. This proves FOpts decryption, which is where a
      device-specific bug hides: a device that encrypts its FOpts differently
      works for data and fails only here.
- [ ] **Replay refusal.** Re-send a frame the device already sent (some
      simulators can). The server must drop it and log a replay, not accept it
      and advance the counter.
- [ ] **Restart persistence.** `docker compose restart ns`. The device must keep
      working without rejoining, and the counters must not reset. If it has to
      rejoin, session persistence is broken.

## Downlink timing (the actual point)

The server derives a downlink's `tmst` from the concentrator timestamp of the
uplink it is answering: RX1 at uplink + 1 s, RX2 at uplink + 2 s, join-accept at
+5 s and +6 s. All values are microseconds on a 32-bit counter.

Do these with a device that can be triggered on demand, or with two devices so
you can compare.

- [ ] **RX1 lands.** Trigger an uplink. The device must hear its downlink in
      RX1. If it only ever hears RX2, the RX1 timestamp is wrong — the device
      closed RX1 before the frame arrived. Note whether RX1 succeeds on the
      first attempt or only after retries.
- [ ] **RX2 frequency and rate.** Confirm the device hears RX2 at the region's
      RX2 frequency and data rate (table below). This is the check that catches
      a device provisioned from a different source than the server: an AS923
      node configured for 923.300 MHz RX2 will never hear this server's RX2,
      which follows ChirpStack's `lrwn` at a 923.200 MHz base. If RX2 never
      works while RX1 always does, suspect a mismatch of exactly this kind
      before suspecting timing.
- [ ] **Counter wrap.** The concentrator counter is 32-bit microseconds, so it
      wraps every ~71.6 minutes of uptime. Restart the forwarder and trigger an
      uplink immediately, then again as close to a wrap as you can manage. A
      downlink scheduled across the wrap must still be transmitted: the server
      adds the delay and truncates, which reproduces the wrap, but only hardware
      proves the forwarder agrees.
- [ ] **No-timestamp fallback.** Some forwarders do not fill in `tmst` on every
      reception. When they do not, the server transmits `imme` and logs a
      warning. Confirm the warning appears when you force it, and note whether
      the device still hears the frame — with `imme` the frame goes out
      immediately, so RX1 is usually lost and RX2 is the safety net.
- [ ] **TX_ACK honesty.** Cause a real `TOO_LATE` by queueing a downlink for a
      window that has already passed. The server must record the failure on the
      gateway rather than counting the downlink as sent.
- [ ] **Backhaul skew.** With two gateways, put one behind a deliberately slow
      link (cellular is naturally this). The late gateway must still transmit
      one second after *it* heard the frame, because its timestamp comes from
      its own reception. Confirm the chosen transmitter is the one with the
      better SNR, not simply the first to arrive.

### Per concentrator family

Record which of these you have tested. A family that has not been tested should
be stated as untested rather than assumed to work.

| Family | Tested | RX1 first try | Wrap | Notes |
|---|---|---|---|---|
| Semtech UDP, sx1301 (RPI/standard) | | | | |
| Semtech UDP, sx1302 (RPI HAT, CL) | | | | |
| UDP with `tmms` only (no `tmst`) | | | | |
| Basic Station | | | | not supported; needs the bridge |

## Per-region expected values

These are what this build implements, read out of the region tables themselves.
A device that disagrees with any of them is configured for a different plan, and
that is worth knowing before you spend a day debugging timing.

| | EU868 | US915 | AS923 | AS923-2 | AS923-3 | AS923-4 |
|---|---|---|---|---|---|---|
| Default uplink channels | 868.1 / 868.3 / 868.5 MHz | 64×125 kHz from 902.3 + 8×500 kHz | 923.2 / 923.4 MHz | −1.8 MHz | −6.6 MHz | −5.9 MHz |
| **RX2 frequency** | **869.525** | **923.300** | **923.200** | **921.400** | **916.600** | **917.300** |
| RX2 data rate | DR0 (SF12BW125) | DR8 (SF12BW500) | DR2 | DR2 | DR2 | DR2 |
| RX1 / RX2 delay | 1 s / 2 s | 1 s / 2 s | 1 s / 2 s | 1 s / 2 s | 1 s / 2 s | 1 s / 2 s |
| Join-accept RX1 / RX2 | 5 s / 6 s | 5 s / 6 s | 5 s / 6 s | 5 s / 6 s | 5 s / 6 s | 5 s / 6 s |
| ADR uplink DR range | DR0–DR5 | DR0–DR4 | DR0–DR5 | DR0–DR5 | DR0–DR5 | DR0–DR5 |
| RX1 channel rule | same channel | uplink channel % 8 | same channel | same channel | same channel | same channel |

RX2 frequencies are in MHz. The AS923 variants shift the whole plan by a fixed
offset, so their RX2 shifts with it.

- [ ] The device joins on an expected default channel, at a data rate inside the
      range above.
- [ ] RX1 comes back on the expected frequency. US915 is the one to check
      carefully: RX1 is a *different* channel from the uplink, chosen by
      `uplink channel % 8`.
- [ ] RX2 comes back at the frequency in the table.
- [ ] A device operating above the ADR range is lowered or refused, never
      scheduled at a rate the band does not define.

## Regulatory enforcement

- [ ] **EU868 duty cycle.** The 868.0–868.6 MHz sub-band allows 1 % occupancy.
      Drive uplinks fast enough to exhaust it (SF7, short intervals) and watch
      `/status` → `duty_cycle.usage`. The server must **defer** downlinks rather
      than emit them, say so in the log, and raise `denials`. A device whose
      traffic stops entirely is a different bug: deferral must not lose frames.
- [ ] **Sub-band awareness.** Confirm a downlink on RX2 (869.525 MHz, 10 %
      allowance, 29 dBm) is still permitted while the 868 MHz channels are
      exhausted. This is why the ledger is per sub-band rather than per band.
- [ ] **Ledger across restart.** Exhaust an allowance, restart `ns`, and confirm
      it does not immediately transmit into the spent allowance.
- [ ] **AS923 dwell time.** With dwell time enabled, a frame whose time on air
      exceeds 400 ms must be refused rather than transmitted.
- [ ] **US915 has no duty cycle.** Confirm occupancy is reported as
      unconstrained, not as a limit of zero — the latter would silently block
      every downlink.
- [ ] `NS_DISABLE_DUTY_CYCLE` is **not** set anywhere you intend to operate. The
      server logs a warning at startup when it is; treat that warning as a
      defect in the deployment.

## Multi-gateway

- [ ] With two gateways in range, one device transmission produces **one**
      uplink in the console, not two, and the stored record lists both
      receptions with their own RSSI and SNR.
- [ ] The downlink leaves through the gateway with the better SNR. Move the
      device to change which that is, and confirm the choice follows.
- [ ] Power one gateway off. The device must keep working through the other
      without rejoining.

## Recording the result

For each region and family, record: gateway model and forwarder build, device
model and LoRaWAN revision, the four timing checks, and every place the device
behaved differently from the table. Then update the milestone table in
`ns/README.md`.

M6 closes only when each region you intend to deploy has a line here with a name
against it. A server whose timing was verified on one concentrator family is not
a server whose timing was verified.

