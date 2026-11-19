# Fieldnode Network Server (`ns/`)

A from-scratch LoRaWAN 1.0.x network-server core written in Go, and the protocol
engine this repository deploys. The semantics come from the LoRaWAN 1.0.3 and
RP002 1.0.3 specifications; where those leave a case open, the behaviour chosen is
the one interoperable devices and gateways expect, and
[`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md) records what was consulted
to settle it.

## Design goals

- **Small and readable.** Standard library only. Plain structs, explicit
  errors, and every region table carrying a comment that names the RP002
  section it comes from. A new contributor should be able to read
  `internal/lorawan` in one sitting.
- **Cross-verifiable.** Every crypto and framing decision has a test vector —
  the specification's worked examples for key derivation and frame security,
  RFC 4493 for AES-CMAC — so a change cannot silently break interoperability.
- **Scoped.** Class A, OTAA + ABP, LoRaWAN 1.0.3, regions EU868 / US915 /
  AS923(-2/-3/-4). Class B/C, 1.1, roaming and FUOTA are explicitly out of
  scope for v1; the package layout keeps them additive.

## Layout and roadmap

| Package | Purpose | Milestone |
|---|---|---|
| `internal/lorawan` | PHY payload codec, AES-128/CMAC security, session keys, MAC commands, 16→32-bit frame counters | M1 ✅ |
| `internal/region` | EU868 / US915 / AS923 tables: data rates, RX1 mapping, channels, duty cycle, dwell time, max payload sizes | M1 ✅ |
| `internal/engine` | Join server, uplink handler, FCnt/replay/dedup, RX-window planning, downlink queue, MAC engine | M2 ✅ |
| `internal/sim` | Simulated Class-A device (OTAA, MAC answers) that drives the engine in tests and the demo | M2 ✅ |
| `internal/pgstore` | PostgreSQL persistence (pgx) with embedded, advisory-locked migrations | M2 ✅ |
| `internal/gateway` | Transport contract: `UplinkFrame` / `DownlinkFrame`, RX windows, `Gateway` port | M2 ✅ (types) |
| `cmd/nsdemo` | End-to-end walkthrough with no radio: join → uplink → downlink → ADR → dedup → replay | M2 ✅ |
| `internal/gateway/semtech` | Semtech UDP packet forwarder: `push_data` / `pull_data` / `pull_resp` / `tx_ack`, gateway registry, downlink timing | M3 ✅ |
| `internal/gateway/lbs` | LoRa Basics Station over WebSocket: handshake and `router_config`, `jreq`/`updf`, `dnmsg`, `dntxed`, `timesync`, xtime scheduling, transmission tracking | M6 ✅ |
| `cmd/ns` | The network-server process: env config, UDP listener, status endpoint, graceful shutdown | M3 ✅ |
| `cmd/nsbench` | Simulated gateway + class-A device that drives a running `ns` over real UDP | M3 ✅ |
| `internal/testenv`, `internal/testforwarder` | Test-only harnesses: engine + simulated device; a Semtech packet forwarder. Imported by tests, never by the server | — |
| `internal/region` (mask, user channels) | Per-device channel plan: ChMaskCntl semantics per band, dynamic channels layered over the band table | M4 ✅ |
| `internal/engine` (adr, requests) | SNR-window ADR, outstanding MAC requests re-sent until answered, duty-cycle / TX-param / RX-timing / NewChannel loops | M4 ✅ |
| `internal/integration` | Event fan-out to application webhooks | M5 ✅ |
| `cmd/ns` (api) | Authenticated application API: downlink queue, session read, DevStatus | M5 ✅ |
| `internal/region` (toa, subband) | Time on air for LoRa and FSK; EU868 sub-band table with per-sub-band duty cycle | M6 ✅ |
| `internal/engine` (dutycycle, airtime) | Transmit ledger: accounts for ToA before scheduling, defers a frame that would breach the limit; the second file is the operator-facing usage report and the durable booking log | M6 ✅ |
| `cmd/ns` (device API) | Device provisioning: list/get/create/replace/patch/delete, keys never returned | M6 ✅ |
| `internal/engine` (aggregate) | Multi-gateway reception: copies of one frame are merged over a 200 ms window and answered through the best-SNR copy | M6 ✅ |
| `internal/region` (registry) | The one list of implemented bands, read by the operator API and the console so neither can offer a band the server rejects | M6 ✅ |
| `api` + Fieldnode UI | Device management in the console: read settings, patch them, revoke; relayed to the ns, which owns the row | M6 ✅ |

## Testing

Every test lives next to the code it exercises. There is no parallel `test/`
tree, because the reason usually given for one does not apply: an *external*
test package (`package foo_test`) sitting in the same directory as `foo` may
import a harness that imports `foo`, so cross-package tests need nowhere else to
go.

| The test… | Lives as | Because |
|---|---|---|
| reaches an unexported function, field or constant | `foo/x_test.go`, `package foo` | Go forbids the alternative |
| uses only the exported API of one package | `foo/x_test.go`, `package foo_test` | reads as a caller would |
| checks our output against a published vector | `*_vectors_test.go` beside the package under contract | the specification is the subject, so a failure names the standard |
| drives a device session through the engine | `internal/engine/*_scenario_test.go` | behaviour of the exported engine API |
| needs a real UDP socket and a gateway's side of the wire | `internal/gateway/semtech/*_test.go` | timing is only observable on the wire |
| needs PostgreSQL | `internal/pgstore/*_test.go`, skipped without `NS_TEST_DSN` | external system |

The two harnesses are packages rather than files because a shared harness
cannot live inside the package it drives — and that constraint is useful: it
forces the engine and the transports to be drivable through their exported API,
which is what `testenv` and `testforwarder` do.

`testenv` builds an engine with an in-memory store, a gateway that records
instead of transmitting, a clock that only moves when a test says so, and a
simulated device that produces spec-correct frames. `testforwarder` speaks the
Semtech protocol through the exported `semtech` API on purpose, making it a
second independent consumer of the wire format.

If a test seems to need internals for no good reason, that is a signal: usually
it can be rewritten against the exported surface, and if it cannot, the reason
is worth writing down in the file.

## Milestones

1. **M1 — codec + security + regions** (done): frames, MICs, OTAA key
   derivation, join-accept encryption, MAC command subset, region tables.
   All verified against the specification's worked examples.
2. **M2 — join + uplink + RX windows** (done): the engine authenticates,
   decrypts and records real traffic and plans class-A RX1/RX2 transmissions,
   backed by either `engine.MemStore` (tests, demo) or `pgstore` (Postgres).
   Covered by unit tests plus `cmd/nsdemo`, which narrates a whole session.
3. **M3 — Semtech UDP gateway server** (done): `internal/gateway/semtech`
   implements `gateway.Gateway` against real concentrators — protocol v1 and v2,
   both rxpk field dialects, a gateway registry tracking presence and TX-ACK
   errors, and downlink timing derived from the concentrator timestamp. `cmd/ns`
   is the process; `cmd/nsbench` proves the whole path over UDP without hardware.
4. **M4 — MAC engine + ADR** (done): `adrDecide` implements the standard
   default algorithm — a 20-sample SNR window, 3 dB per step, data rate first and
   transmit power after, with the anti-oscillation guard that refuses to raise
   power on a partial window. Decisions become an outstanding `LinkADRReq` that
   rides every downlink until the device answers, and the session only adopts the
   parameters after a positive `LinkADRAns`. DutyCycleReq, TXParamSetupReq,
   RXTimingSetupReq and NewChannelReq follow the same loop; NewChannelAns adds the
   channel to *that device's* plan (`region.WithUserChannels`), which ADR and
   downlink scheduling then honour. Channel masks implement the per-band
   `ChMaskCntl` readings, including US915's.
5. **M5 — integration + application API** (done, except the UI): decoded uplinks
   and join acceptances leave the server through `internal/integration`, and
   applications submit downlinks over an authenticated HTTP API in `cmd/ns`.
   The dispatcher implements `engine.Recorder` and writes through to the store
   before fanning out, so a dead webhook can never cost the protocol a record;
   it never blocks the radio path either — a full queue drops and counts.
   Downlinks are queued, not sent: a class-A device is only reachable for two
   seconds after it speaks, so the API answers 202 and the frame leaves in the
   next window the device opens.
   *Remaining:* none — device and key management live in the Fieldnode UI, and
   the Fieldnode API reads the `ns_*` tables.
6. **M6 — regulatory limits and provisioning** (partly done):
   *Done:* time-on-air calculation, EU868 sub-band duty cycles, and a transmit
   ledger that defers a downlink rather than emitting a frame that would breach
   the band's limit. Device provisioning over the API, which is what the
   Fieldnode console's registration form calls.
   *Remaining:* the bench pass itself — a real gateway and device per region,
   verifying downlink timing per concentrator family. The procedure for it, with
   the expected value for every band this build implements, is
   [`docs/BENCH.md`](docs/BENCH.md); it is written and ready to run, and nothing
   in it can be done without hardware, so this milestone is not closed and
   should not be described as such.

7. **M7 — what is genuinely left:**
   *Console and API:* device management now runs end to end through the console,
   and the dashboard reads the `ns_*` tables rather than its own empty inventory.
   *Still open, in rough order of consequence:*
   * **Class C is stored but not implemented.** `supports_class_c` is persisted,
     editable and reported, and the engine never reads it. The console says so on
     the toggle rather than implying a device became reachable.
   * **Station trust is one shared secret.** A Basics Station can claim any
     gateway EUI, and `NS_LBS_TOKEN` is a single value shared by every station
     rather than a per-gateway credential. Per-gateway tokens or client
     certificates are what a deployment with more than one operator needs. TLS
     also has to be terminated in front of the LNS port, because stations
     normally demand `wss://`.
   * **Five band plans, six names.** US915, EU868 and AS923 with its -2/-3/-4
     frequency offsets. AU915, CN470, IN865, RU864 and KR920 need tables, tests
     and duty-cycle rules. `region.Names()` is the only list; the operator API and
     the console both read it, so a band added there is configurable everywhere
     and one removed stops being offered.
   * **ADR steps only through 125 kHz LoRa rates**, deliberately: an FSK or
     500 kHz rate is a jump of far more than the 3 dB the algorithm reasons in.
   * **No key rotation and no device-profile objects** beyond the per-device
     fields, so a fleet-wide change is a loop over devices.
   * **The bench pass**, which needs hardware. The checklist it follows is
     written — [`docs/BENCH.md`](docs/BENCH.md) — and is the last open item.

## Downlink lifecycle

A queued frame is not simply deleted after it is written to the socket, and the
rules differ by frame type — getting them wrong is invisible until a device
behaves strangely months later:

- **Unconfirmed application frame**: one transmission. Nothing in LoRaWAN
  acknowledges it, so keeping it queued would re-send it on every uplink for the
  life of the session, each time with a fresh `FCntDown` — which means the device
  would accept every copy and hand the same payload to its application again and
  again. The interoperable behaviour is the same: a pending queue item is deleted
  once its timeout passes rather than retransmitted indefinitely.
- **Confirmed application frame**: retransmitted on each subsequent uplink with
  the *same* `FCntDown` — a new counter would make the original unrecoverable —
  until the device's confirmed uplink carries `FCtrl.ACK`.
- **MAC-only request** (`DevStatusReq` and friends): repeats until answered,
  because that is what makes a lost request recoverable.
- All retries are capped at `maxDownlinkAttempts`; past that the frame is dropped
  and a `downlink_abandoned` event is recorded, so a device that has left
  coverage does not spend every window it ever opens on a frame nobody hears.

The network also answers in *both* RX windows. That is not a duplicate delivery:
both copies share one `FCntDown`, and a device discards a frame whose downlink
counter it has already seen.

## Application API

Enabled by setting `NS_API_TOKEN` (at least 16 characters); without it the API is
not created at all rather than left open, because it can make a device transmit.

```sh
# Queue a downlink for the device's next RX window.
curl -s -X POST localhost:8091/api/v1/devices/0011223344556677/downlink \
  -H "Authorization: Bearer $NS_API_TOKEN" \
  -d '{"f_port": 10, "data": "deadbeef", "confirmed": false}'

curl -s localhost:8091/api/v1/devices/0011223344556677/session \
  -H "Authorization: Bearer $NS_API_TOKEN" | jq

curl -s -X DELETE localhost:8091/api/v1/devices/0011223344556677/downlink \
  -H "Authorization: Bearer $NS_API_TOKEN"
```

`data` is hex, because these payloads get copied out of datasheets and serial
traces. Statuses are meant to be acted on: 404 means the device is not
provisioned or has not joined, 409 means one frame is already queued, 400 means
the request itself is wrong. The session endpoint reports data rates, counters
and battery — never session keys.

### Editing a device without its keys

`PUT /api/v1/devices/{dev_eui}` is a replace, so it demands credentials again.
Since no response ever carries keys, a client that follows that rule could load a
device, change its name, and then be unable to save it. `PATCH` closes the loop:
it takes only operator-facing fields, and the read-modify-write happens inside
the process, so the keys are read from the store and written back untouched.

```sh
# Rename and stop the network from steering this device's data rate.
curl -s -X PATCH localhost:8091/api/v1/devices/0011223344556677 \
  -H "Authorization: Bearer $NS_API_TOKEN" \
  -d '{"name": "Boiler 3", "disable_adr": true}'

# Lift a data-rate limit. -1 means "whatever the band allows"; 0 would mean DR0.
curl -s -X PATCH localhost:8091/api/v1/devices/0011223344556677 \
  -H "Authorization: Bearer $NS_API_TOKEN" -d '{"max_data_rate": -1}'
```

Every field is optional and **omitted means leave alone** — that is the whole
contract, and a client that posts the fields it did not change will quietly
re-enable ADR on a device whose name was the only thing edited. The join-nonce
counter and the DevNonce replay log survive a patch for the same reason
`PUT` carries them over: they are replay protection, not operator input, and
resetting them would let a captured join-accept from months ago become valid
again.

`DELETE` removes the device **and its session**. That is the point: revoking a
device has to stop it talking, and a surviving session would let it keep
exchanging frames with keys the operator just withdrew. To pause rather than
remove, patch `enabled` to `false` instead.

Webhooks POST one JSON object per event to each `NS_WEBHOOK_URLS` entry with
`Authorization: Bearer $NS_WEBHOOK_TOKEN`. Server-side failures are retried with
backoff; 4xx responses are not, because a receiver that rejects an event does not
want it three times. `/status` publishes the dispatcher's counters, where a
growing `dropped` is the signal that a receiver is too slow.


## Shared state across processes

One forwarder delivers every radio frame to each network-server address it is
configured to send to, and duty-cycle limits are a property of the gateway and
the band rather than of the process that happens to be holding it. Neither
question — "has anyone answered this frame?" and "can this gateway still
transmit?" — has an answer that lives inside one process, so both live in
PostgreSQL:

| Table | Answers | Written |
| --- | --- | --- |
| `ns_seen` | may *this* frame be answered? | inline, one conditional `INSERT` per frame |
| `ns_airtime` | how much of the sub-band's window is spent? | on a timer, `NS_LEDGER_FLUSH_INTERVAL` |

The asymmetry is deliberate. A claim must be atomic at the moment of decision —
a `SELECT` then `INSERT` lets two instances both see a frame as new and both emit
a join-accept, and that is the normal case whenever a frame reaches two gateways,
not a rare race. So the claim is one statement whose `ON CONFLICT … WHERE` clause
encodes both "the key is new" and "the old claim has aged out", and it is the
only round trip the uplink path makes.

Airtime is the opposite shape. The decision to transmit must not wait on the
database, so it reads an in-memory ledger, and durability is about the next
restart rather than the next packet — a restart does not need the last fifteen
seconds of occupancy on disk to be honest about the hour. Bookings are therefore
flushed on a timer, whole rather than as a delta, with `ON CONFLICT DO NOTHING`
making each repeat a no-op. Airtime is rounded **up** to the millisecond: the
column is the evidence an allowance was respected, and the only safe direction to
be wrong in is the one that over-books.

Both tables age themselves out. A claim that never expired would discard a
device's legitimate retransmission forever, and a ledger that only grew would
charge a server for emissions the regulation no longer counts.

Without a database the engine falls back to process-local books, which is correct
for the single-process deployment the compose file runs. The two optional ports
are checked for at startup and logged, so "persistence is off" is a fact an
operator can see rather than infer from behaviour.

## What this server deliberately does not do yet

- **Single-gateway downlinks.** Copies of an uplink heard by several gateways are
  merged, and the downlink is answered through the copy with the best signal to
  noise. What is not done is transmitting the same downlink through *every*
  gateway that heard the device, which is the other way of raising the chance the
  device hears it — at the cost of occupying the channel once per gateway.
- **Station radio plans are trusted by default.** `router_config` is sent at the
  handshake and carries this server's band, RX2 parameters and data-rate table,
  so a station cannot disagree about where downlinks arrive. What is not pushed
  unless `NS_LBS_RADIO_PLAN` names a file is the `sx1301_conf` concentrator plan,
  so a station left with a wrong local plan still tunes the wrong RF chains.
- **Station configuration is sent once.** `router_config` goes out at the
  handshake and is not re-pushed when the server's band or RX2 settings change;
  the station has to reconnect to learn them.
- **No configuration channel for UDP forwarders.** A packet forwarder takes its
  channels from its own `global_conf.json`, and the server has no way to change
  them, which is why `NS_REGION` has to match what the gateway was configured
  with. Basics Station is the only transport here where configuration can be
  pushed at all.
- **RX-window detection.** A packet forwarder does not report which window a
  frame arrived in, so every uplink is treated as RX1. That is safe because the
  engine answers in both windows, but an RX2-only repeat is scheduled as if it
  were a first transmission.
- **Duty-cycle accounting is durable but not yet contention-aware.** Enforcement
  is live, and the ledger now survives a restart: every booking is written to
  `ns_airtime` and read back at startup, so a server that comes back up cannot
  transmit straight through an allowance it had already spent. What it does not
  do is *reserve* ahead of time across processes — two instances can each decide
  a slot is affordable and both transmit, because the booking is written on a
  timer rather than at the moment of decision. The window is the exposure: see
  `NS_LEDGER_FLUSH_INTERVAL`.
- **NewChannelReq on US915.** Its RX1-to-downlink-channel mapping is derived
  from the channel index, so an arbitrary user channel needs downlink-side work
  the server does not have; the request is refused rather than scheduled wrong.
- **ADR parameters from the device profile.** `ADRParamSetupReq` (CID 0x0C) is
  not sent; ADRACKLimit/ADRACKDelay come from server config.
- **Class B/C, LoRaWAN 1.1, roaming, FUOTA.** Out of scope for v1.

## Running the network server

`cmd/ns` is the whole network server in one process — no gateway bridge, MQTT
broker or separate cache; gateways point their packet forwarder at UDP 6858.

```sh
make run     # ns in Docker, in memory, with one seeded device
make smoke   # ns + nsbench over real UDP, then /healthz, /status, SIGTERM
```

Configuration is environment driven:

| Variable | Default | Meaning |
|---|---|---|
| `NS_LISTEN_ADDR` | `:6858` | UDP port gateways are pointed at |
| `NS_STATUS_ADDR` | `:8090` | `/healthz` and `/status`; `off` disables it |
| `NS_API_ADDR` | `:8091` | application API; only served when `NS_API_TOKEN` is set |
| `NS_API_TOKEN` | *(empty)* | bearer token for the API; empty disables it entirely |
| `NS_REGION` | `US915` | `US915`, `EU868`, `AS923`, `AS923-2`, `AS923-3`, `AS923-4` |
| `NS_NET_ID` | `000000` | 6 hex digits; sets the DevAddr prefix of joining devices |
| `NS_AGGREGATION_DELAY` | `200ms` | how long the first copy of a frame waits for other gateways to report the same transmission; `0` answers immediately and loses the other copies |
| `NS_DUTY_CYCLE_WINDOW` | `1h` | period duty-cycle occupancy is measured over |
| `NS_DISABLE_DUTY_CYCLE` | *(off)* | transmit without checking the band's duty cycle or dwell time. Only where the concentrator enforces them locally; the server logs a warning at startup |
| `NS_DATABASE_URL` | *(empty)* | Postgres DSN; empty means in-memory state |
| `NS_SEED_DEVICES` | *(empty)* | `devEUI:joinEUI:appKey` triples to provision at startup |
| `NS_WEBHOOK_URLS` | *(empty)* | comma separated HTTP endpoints that receive events |
| `NS_WEBHOOK_TOKEN` | *(empty)* | bearer token sent to every webhook endpoint |
| `NS_DOWNLINK_POWER_DBM` | `14` | TX power handed to the gateway |
| `NS_DEVICE_STATUS_EVERY` | `0` | ask every Nth uplink for battery/margin; 0 disables |
| `NS_GATEWAY_TIMEOUT` | `180` | seconds of silence before a gateway counts as offline |
| `NS_GATEWAY_FLUSH_INTERVAL` | `30s` | how often gateway presence and radio health are written to the store |
| `NS_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Without `NS_DATABASE_URL` the server warns and keeps sessions in memory: fine on
a bench, wrong in production, because a restart loses them and with them the
join-nonce replay protection.

`NS_SEED_DEVICES` is a bring-up convenience until the Fieldnode UI owns provisioning, and it is
non-destructive: a device that already exists is left untouched, because
re-seeding a joined device would reset its address, counters and nonce history
and lock the real device out of its own session.

`/status` reports gateway presence — address, protocol version, last seen,
packets, downlinks sent and refused, TX-ACK errors and the last stat report —
with absolute timestamps so a cached response stays interpretable:

```sh
curl -s localhost:8090/status | jq '.gateways[] | {gateway_eui, online, downlinks_sent, last_tx_ack_error}'
```

`/ns -healthz` is the container health probe: the image is distroless, so the
binary probes its own status endpoint rather than relying on a shell. In the
repository Compose file the server sits behind a profile, so the default
deployment is untouched:

```sh
docker compose --profile fieldnode-ns up --build   # gateways → <host>:6858/udp
```

To validate without radio hardware, `cmd/nsbench` plays both gateway and device
against a running server: it registers, reports a stat object, joins, waits for
the join-accept in both RX windows, then sends a confirmed uplink and asserts the
acknowledgement.

```sh
go run ./cmd/nsbench -addr 127.0.0.1:6858 -net-id 000000
```

## Running the tests

No local Go toolchain required (Docker only):

```sh
make test        # go test ./...
make vet         # go vet ./...
make demo        # go run ./cmd/nsdemo — narrated OTAA session, no radio
```

Every test lives beside the code it exercises. Cross-package suites — a join
driven through the engine, a frame pushed over a real UDP socket, the Postgres
store against a real database — are named `*_scenario_test.go` inside the package
they exercise, so the file name says how much of the system it touches. Shared
fixtures (a fake gateway, a simulated device, a store) live in `internal/testenv`
and `internal/sim`, which are importable by tests everywhere and by no
production binary.

To read a failure: the test file names the package, the scenario file names the
flow, and each scenario test's doc comment states the property it pins. A
`*_vectors_test.go` failure means a value taken from the specification or from
ChirpStack no longer matches, which is a different class of bug from a logic
error and usually means a transcription mistake.

The PostgreSQL store has an opt-in integration test; `go test ./...` skips it
unless a DSN is supplied:

```sh
docker run -d --name ns-pgtest -e POSTGRES_PASSWORD=ns -e POSTGRES_DB=ns \
  -p 5433:5432 postgres:16-alpine
NS_TEST_DSN='postgres://postgres:ns@host.docker.internal:5433/ns?sslmode=disable' \
  make test-pg
```

## Where to look when something is wrong

The paths below are the first place to read for each symptom, not an exhaustive
map. Every one of them is a single function or table, and the reason strings in
[`internal/engine/events.go`](internal/engine/events.go) are the vocabulary the
other three columns refer to.

| Symptom | Start here | Then |
|---|---|---|
| A frame is rejected and the event names a reason | `internal/engine/events.go` — read what the reason means | The reason's own site: join in `engine/join.go`, data in `engine/uplink.go` |
| MIC fails on an uplink that looks correct | `lorawan/phy_security.go` `ValidateDataMIC` | Session keys: `lorawan/keys.go` `DeriveSessionKey`, then the device's join state |
| Counter is refused as replay or gap | `lorawan/fcnt.go` `FullFCnt` | `engine/uplink.go`, where the accepted counter is advanced |
| Join-accept never reaches the device | `engine/downlink.go` `joinWindows` | `gateway/semtech/send.go` `SendDownlink` — check the `tmst` it logged |
| Uplinks seen, no downlinks received | `gateway/semtech/registry.go` — has the gateway sent `PULL_DATA`? | The `tmst` vs `imme` decision in `send.go`; then TX-ACK errors in the registry |
| Downlink arrives at the wrong time | `gateway/semtech/token.go` | The `tmst`/`tmms`/`time` fallback in `receive.go` `handleRx` |
| Wrong frequency or data rate | `region/frequency.go` (rounding), then the band table | `gateway/semtech/messages.go` `DataRateIndex` for what the forwarder reported |
| Device stuck at a low data rate | `engine/adr.go` `adrDecide` — the `Reason` field explains the arithmetic | `engine/requests.go` `applyADR` for whether a request was ever sent |
| A MAC change was accepted but not applied | `engine/mac.go` — the answer handling | `engine/requests.go`, where an outstanding request becomes session state |
| Transmission refused as duty cycle or dwell | `engine/dutycycle.go` `airtimeBudget.reserve` | `region/toa.go` for the airtime it computed |
| Webhook missing events | `internal/integration/dispatcher.go` `Stats` — dropped vs failed | `dispatcher.go` `deliver`, then `webhook.go` for the attempt policy |
| API returns an unexpected status | `cmd/ns/api_responses.go` `writeEngineError` | The code's meaning: `api/internal/httpapi/errors.go` on the console side |
| Dashboard shows stale or empty panels | `src/features/network/api.ts` `loadNetworkSnapshot` | `failed` in the snapshot names which section, and why |

## References

- LoRaWAN 1.0.3 specification (physical layer §4, MAC §5, OTAA §6.2.4)
- RP002 Regional Parameters 1.0.3 (EU868 §2, AS923 §3, US915 §4)
- RFC 4493 (AES-CMAC)
- LoRaWAN ADR application note (RS002) for the data-rate policy in `engine/adr.go`
- [`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md) for third-party code and
  test data this implementation was checked against
