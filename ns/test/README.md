# Tests

Two kinds of test live in this module, and the difference is not stylistic — it
is forced by the language.

**`ns/test/`** holds tests that describe behaviour *across* packages or *against
an external specification*. They import only exported API.

**Colocated `*_test.go`** next to the file they test holds white-box tests that
reach inside one package. A Go package's own tests cannot import anything that
imports that package, so these cannot live here — moving them would mean
exporting internals that production code could then call.

## Where a new test goes

Ask what the test would have to touch to exist:

| The test... | Goes in | Because |
|---|---|---|
| checks our output against a published vector or standard | `contract/` | the specification is the subject, not our code |
| drives a device session through the engine | `scenarios/` | behaviour of the exported engine API |
| needs a real UDP socket and a gateway's side of the wire | `transport/` | crosses the engine/transport boundary |
| needs PostgreSQL | `database/` | external system; skipped without `NS_TEST_DSN` |
| needs an unexported function, field or constant | next to that file | Go forbids the alternative |

If a test seems to need internals for no good reason, that is a signal: usually
it can be rewritten against the exported surface, and if it cannot, the reason
is worth writing down in the file.

## Suites

**`contract/`** — our word for what the specification says. AES-CMAC against
RFC 4493, frame encoding and key derivation against ChirpStack's own test
vectors, band tables against RP002. These fail when we drift from the standard,
which no amount of self-consistent unit testing would catch.

**`scenarios/`** — multi-frame device sessions: join, uplink, downlink,
acknowledgement, replay rejection, ADR moving a device's data rate, MAC request
round trips. Driven through `testenv`.

**`transport/`** — the whole radio path with nothing stubbed: a simulated device
frame goes in as a Semtech PUSH_DATA over a real socket, and a txpk comes back
that the device can decrypt. Timing assertions (the `tmst` a concentrator needs
to hit an RX window) live here because they are only observable on the wire.

**`database/`** — the Postgres store: migrations, round trips, JSONB nonce
replay sets, the `f_port = -1` convention for MAC-only frames. Opt-in:

```
docker run -d --name ns-pgtest -e POSTGRES_PASSWORD=ns -e POSTGRES_DB=ns \
  -p 5433:5432 postgres:16-alpine
make test-pg
docker rm -f ns-pgtest
```

**`testenv/`** and **`testforwarder/`** are harnesses, not suites — no tests of
their own.

`testenv` builds an engine with an in-memory store, a recording gateway, a clock
that only moves when told, and a simulated device. `testforwarder` is a packet
forwarder: a UDP socket that sends what a gateway sends and decodes what the
server pushes back.

Both deliberately use only exported API. That is a check in itself: if the
exported surface ever became too awkward to drive a gateway or a device with,
these two packages would be the first to notice.

## Reading a failure

- A `contract/` failure means we disagree with the standard. Check the cited
  section before changing the test.
- A `scenarios/` failure names the device and the frame; `testenv` fixtures are
  fixed constants, so events from one test cannot be mistaken for another's.
- A `transport/` failure is usually timing. The forwarder's reads have a two
  second deadline, so a hang is reported as a failure rather than a stall.
- A `database/` skip is normal without `NS_TEST_DSN`; a `database/` failure
  means the SQL itself is wrong, which no other suite can tell you.

## Conventions

- Table-driven for anything with a matrix of cases; one `t.Run` per case.
- A parse helper that returns an error is never called without failing the test.
  `testenv.MustX` exists so this is the default rather than a rule to remember.
- No sleeps to wait for asynchronous work. The engine's scheduler is injected,
  so scheduled transmissions run inline and assertions are deterministic.
- Fixed fixture identifiers (`testenv.DevEUI` and friends) rather than random
  ones, so a log line identifies the device without extra context.
