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
| `internal/storage/pgstore` | PostgreSQL persistence (pgx) with embedded, advisory-locked migrations | M2 ✅ |
| `internal/gateway` | Transport contract: `UplinkFrame` / `DownlinkFrame`, RX windows, `Gateway` port | M2 ✅ (types) |
| `cmd/nsdemo` | End-to-end walkthrough with no radio: join → uplink → downlink → ADR → dedup → replay | M2 ✅ |
| `internal/gateway/semtech` | Semtech UDP packet forwarder: `push_data` / `pull_data` / `pull_resp` / `tx_ack`, gateway registry, downlink timing | M3 ✅ |
| `cmd/ns` | The network-server process: env config, UDP listener, status endpoint, graceful shutdown | M3 ✅ |
| `cmd/nsbench` | Simulated gateway + class-A device that drives a running `ns` over real UDP | M3 ✅ |
| `internal/region` (mask, user channels) | Per-device channel plan: ChMaskCntl semantics per band, dynamic channels layered over the band table | M4 ✅ |
| `internal/engine` (adr, requests) | SNR-window ADR, outstanding MAC requests re-sent until answered, duty-cycle / TX-param / RX-timing / NewChannel loops | M4 ✅ |
| `internal/integration` | Event fan-out to application webhooks | M5 ✅ |
| `cmd/ns` (api) | Authenticated application API: downlink queue, session read, DevStatus | M5 ✅ |
| `internal/region` (toa, subband) | Time on air for LoRa and FSK; EU868 sub-band table with per-sub-band duty cycle | M6 ✅ |
| `internal/engine` (dutycycle) | Transmit ledger: accounts for ToA before scheduling, defers a frame that would breach the limit | M6 ✅ |
| `cmd/ns` (device API) | Device provisioning: list/get/create/update/delete, keys never returned | M6 ✅ |

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
   verifying downlink timing per concentrator family — and the conformance
   checklist. Neither is possible without hardware, so this milestone is not
   closed and should not be described as such.

7. **Still open after M6:** device/key/profile management in the Fieldnode UI
   and pointing the Fieldnode API at the `ns_*` tables, so the dashboard reads
   live traffic instead of its own empty inventory.

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

Webhooks POST one JSON object per event to each `NS_WEBHOOK_URLS` entry with
`Authorization: Bearer $NS_WEBHOOK_TOKEN`. Server-side failures are retried with
backoff; 4xx responses are not, because a receiver that rejects an event does not
want it three times. `/status` publishes the dispatcher's counters, where a
growing `dropped` is the signal that a receiver is too slow.


## What this server deliberately does not do yet

- **Multi-gateway aggregation.** A frame heard by two gateways is deduplicated
  by dropping the second copy, rather than merging the receptions to pick the best
  transmitter. Until that changes, a downlink leaves through whichever gateway
  was heard from first.
- **Basic Station / WebSocket gateways.** Only the Semtech UDP protocol is
  spoken, so concentrators that require Basic Station still need the bridge.
- **Gateway configuration over the protocol.** Channels come from `NS_REGION`;
  nothing pushes a `router/config` update to the forwarder.
- **RX-window detection.** A packet forwarder does not report which window a
  frame arrived in, so every uplink is treated as RX1. That is safe because the
  engine answers in both windows, but an RX2-only repeat is scheduled as if it
  were a first transmission.
- **Duty-cycle accounting does not survive a restart.** Enforcement is live —
  every planned downlink is filtered through `enforceDutyCycle` before it is
  scheduled, and a window the band cannot afford is dropped with an event rather
  than transmitted — but the ledger is in memory. A restart forgets the last
  window's occupancy, and two `ns` processes sharing a gateway would each keep
  their own books. Both need the ledger persisted or shared before a
  multi-instance deployment is honest about compliance.
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
| `NS_REGION` | `EU868` | `EU868`, `US915`, `AS923`, `AS923-2`, `AS923-3`, `AS923-4` |
| `NS_NET_ID` | `000000` | 6 hex digits; sets the DevAddr prefix of joining devices |
| `NS_DATABASE_URL` | *(empty)* | Postgres DSN; empty means in-memory state |
| `NS_SEED_DEVICES` | *(empty)* | `devEUI:joinEUI:appKey` triples to provision at startup |
| `NS_WEBHOOK_URLS` | *(empty)* | comma separated HTTP endpoints that receive events |
| `NS_WEBHOOK_TOKEN` | *(empty)* | bearer token sent to every webhook endpoint |
| `NS_DOWNLINK_POWER_DBM` | `14` | TX power handed to the gateway |
| `NS_DEVICE_STATUS_EVERY` | `0` | ask every Nth uplink for battery/margin; 0 disables |
| `NS_GATEWAY_TIMEOUT` | `180` | seconds of silence before a gateway counts as offline |
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

The PostgreSQL store has an opt-in integration test; `go test ./...` skips it
unless a DSN is supplied:

```sh
docker run -d --name ns-pgtest -e POSTGRES_PASSWORD=ns -e POSTGRES_DB=ns \
  -p 5433:5432 postgres:16-alpine
NS_TEST_DSN='postgres://postgres:ns@host.docker.internal:5433/ns?sslmode=disable' \
  make test-pg
```

## References

- LoRaWAN 1.0.3 specification (physical layer §4, MAC §5, OTAA §6.2.4)
- RP002 Regional Parameters 1.0.3 (EU868 §2, AS923 §3, US915 §4)
- RFC 4493 (AES-CMAC)
- LoRaWAN ADR application note (RS002) for the data-rate policy in `engine/adr.go`
- [`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md) for third-party code and
  test data this implementation was checked against
