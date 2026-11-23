# MCP integration plan

An MCP server that lets an agent read and operate the Fieldnode network: which
gateways are reporting, what a device last said, why a frame was refused, and —
when an operator allows it — queue a downlink.

Planned as a third Go module, `mcp/`, beside `ns/` and `api/`.

## Decisions

**It is a client of the published API, not a reader of the database.** The same
reason `api/internal/nsclient/client.go` does not write `ns_devices`: the network
server owns the rules about what a valid device and a legal transmission are, and
a second implementation of those rules would give the agent a subtly different
opinion of what is legal from the one that runs at runtime.

**Upstream is the network server only** — the token-protected API on `:8091` plus
the unauthenticated `/status` on `:8090`. The console API on `:8080` is a superset
of reads, but it has no authentication of its own and relies on being unreachable;
an agent-facing surface should present credentials, so it presents the ones the
network server already issues.

**That left one gap, and PR 1 closes it in the network server rather than routing
around it.** `ns_events` is written by `ns/internal/pgstore/traffic.go` and read
only by `api/internal/storage/postgres/store.go`, so an agent talking to the
network server could see a device's *state* but not its *history* — which is the
thing actually needed to answer "why did this device stop reporting". A read
endpoint belongs on the token-protected `apiServer`, never on `statusServer`,
whose own comment forbids it from ever gaining anything beyond read-only health.

**Out of scope for v1, deliberately:**

- **Device provisioning.** `POST /api/v1/devices` requires an AppKey or ABP session
  keys in the body. Those bytes would transit an LLM context and land in a
  conversation transcript. The network server's own rule is that keys go in and
  never come out; a path that routes them through a model breaks the spirit of it
  even if no endpoint ever returns them. Provisioning stays console-only.
- **Integration writes.** Creating a webhook is an operator-controlled destination
  for every event on the network. In an agent's hands it is both an exfiltration
  channel and an SSRF primitive, and it needs an allowlist design before it is
  exposed to a model. Reads are fine and included.
- **Model writes.** A device model carries JavaScript that executes inside the
  network server process. Installing one is remote code execution by whoever holds
  the token. Read and test-decode only.

## Versions, verified 2026-11-21

| | |
| --- | --- |
| MCP spec revision | `2026-07-28` — stateless: no `initialize` handshake, no `Mcp-Session-Id`, `server/discover` mandatory, protocol version in per-request `_meta`, `subscriptions/listen` replaces `resources/subscribe`, roots/sampling/logging deprecated |
| Go SDK | `github.com/modelcontextprotocol/go-sdk` **v1.8.0**; v1.7.0+ implements `2026-07-28`; its `go.mod` requires **go 1.25.0** |
| Consequence | `mcp/go.mod` declares `go 1.25.0` and `mcp/Makefile` pins `GO_IMAGE ?= golang:1.25`. `ns/` and `api/` keep `go 1.24.0` and `golang:1.24` — a new module must not force a toolchain bump on two that work |

Pin the SDK and keep its types behind `mcp/internal/tools`; that revision removed
the handshake inside a twelve-month deprecation window, so an unisolated dependency
would make every handler move when the SDK moves.

## PR 1 — `GET /api/v1/events` on the network server — done

Self-contained, independently useful, and it landed before any MCP code exists.

| File | Change |
| --- | --- |
| `ns/internal/pgstore/traffic.go` | `EventRow` plus `RecentEvents(ctx, devEUI, kind string, limit int) ([]EventRow, error)`, modelled on `RecentUplinks` in the same file: limit clamped to `1..500` defaulting to 50, `WHERE ($1::text = '' OR dev_eui = $1)`, `ORDER BY occurred_at DESC`, wrapped errors that never reach a client verbatim |
| `ns/cmd/ns/api.go` | Added `RecentEvents` to `TrafficReader`; registered `GET /api/v1/events` behind `requireAuth` |
| `ns/cmd/ns/api_events.go` | `handleListEvents`, `eventView` and `newEventView` |
| `ns/cmd/ns/api_responses.go` | `msgEventListFailed = "event history unavailable"` |

`eventView` ended up beside its handler rather than in `ns/cmd/ns/api_views.go`,
because that is what every other collection endpoint does — `deviceView` sits in
`ns/cmd/ns/api_devices.go` and `uplinkView` in `ns/cmd/ns/api_models.go`. The rule
that file states is that shapes are structs with tags rather than maps, not that
they all live in one place.

Handler behaviour, all of it precedent from `handleListUplinks` in
`ns/cmd/ns/api_models.go`: `dev_eui` parsed with `lorawan.ParseEUI64` so a
malformed identifier is a 400 and never reaches the store; `kind` validated against
the constants in `ns/internal/engine/events.go` so a typo is a 400 rather than a
silently empty 200; 503 when `traffic` is nil, which is what the in-memory store
leaves it as.

No new index. `ns_events` already carries `ns_events_occurred_idx` and
`ns_events_device_idx`; a `kind` filter on top of the first is fine, and an index
for an unmeasured need is the wrong trade.

Tests: `ns/cmd/ns/api_events_test.go`, extending the `fakeTraffic` stub already in
`ns/cmd/ns/api_models_test.go` — auth required, bad EUI 400, bad kind 400, limit
clamping, 503 on the in-memory store, and the shape of an absent port. Plus a
`RecentEvents` round trip in `TestTrafficRecords`, run against a real PostgreSQL so
the SQL is executed and not merely compiled.

`TestEveryRecordedKindIsFilterable` reads `ns/internal/engine/events.go` as text and
fails if a kind the engine records is not filterable here. `knownEventKinds` is
written with the engine's constants, so a new kind does not break a compile — it
just quietly becomes rejectable. Verified non-vacuous: deleting one entry from the
list fails the test and names the constant it missed.

### A write-side bug this exposed

`engine.Event.FPort` was an `int` with no sentinel and no documented convention.
Of the twenty-six places that build an `Event`, none set it, and `RecordEvent`
passed it straight to a `NOT NULL` column — so every event in `ns_events` recorded
`f_port = 0`, including joins and MAC exchanges that have no application port. Zero
is not "unset" in LoRaWAN: port 0 carries network MAC commands keyed with NwkSKey.
The schema's own sentinel is -1, and `UplinkRecord` and `DownlinkRecord` already
document that.

Nothing read that column before this endpoint, which is why it survived. The fix is
`*int` — nil means no port, so the twenty-six constructors are correct by default
rather than by remembering — with `eventFPort` mapping nil to -1 on the way in and a
real 0 staying 0. `TestTrafficRecords` now pins both halves, because coercing 0 to
-1 would have been the same bug one layer down.

History is not rewritten. Rows written before this change carry 0 where they meant
"no port", and the two cannot be told apart after the fact — a genuine port-0 drop
and an unset field are the same bytes. Anything that aggregates `f_port` over old
rows should treat 0 as unreliable rather than reassign it.

## PR 2 — `mcp/`, read-only, stdio — done

```
mcp/
  go.mod                        module github.com/fieldnode/lorawan-server/mcp; go 1.25.0
  Makefile                      test / vet / fmt in Docker, GO_IMAGE ?= golang:1.25
  Dockerfile                    static binary, distroless nonroot
  cmd/fieldnode-mcp/main.go     env -> profile -> tool registration -> stdio
  internal/config/config.go     NS_API_URL, NS_API_TOKEN, NS_STATUS_URL, MCP_PROFILE
  internal/nsclient/            HTTP client for the network server
  internal/tools/               one file per tool group, registered through a table
  internal/vocabulary/          event reason -> plain-English cause, plus drift test
  README.md
```

**The client copies `api/internal/nsclient/client.go` on purpose.** That package
names its route constants in one place and pins each against the path a request
actually carries, because the two modules have no shared package and a wrong route
is neither a compile error nor a test failure — it is a 404 the caller misreads as
a missing resource. Same here: `LimitReader` on responses, `{"error": "..."}`
extraction with a truncated raw fallback, a typed error per status, and two base
URLs, since `/status` is unauthenticated on its own port.

| Tool | Upstream |
| --- | --- |
| `network_status` | `:8090/status` — region, NetID, uptime, gateways online/total, transports and listeners, integration dispatcher health, duty-cycle usage and denials |
| `list_devices`, `get_device` | `GET /api/v1/devices[/{dev_eui}]` |
| `get_device_session` | counters, data rate, ADR, RSSI/SNR, battery and margin, pending frame |
| `list_uplinks` | `GET /api/v1/uplinks` — decoded readings, `decode_error`, `has_codec` |
| `list_events` | the PR 1 endpoint |
| `list_models`, `get_model` | `GET /api/v1/models...` |
| `list_integrations`, `get_integration` | reads only |
| `explain_event_reason` | local. Turns `f_cnt_gap_too_large` into "frames were lost or the device restarted its counter" |
| `diagnose_device` | fan-out: device + session + recent events + recent uplinks + serving gateway presence, one compact payload |

`explain_event_reason` and `diagnose_device` are the two that decide whether the
agent diagnoses or guesses. The first costs a table; the second costs one function
and saves five round trips plus the context they would have consumed.

**Vocabulary drift test.** `mcp/internal/vocabulary` reads
`ns/internal/engine/events.go` as *text* — `ns/internal` is closed to other
modules, and reading a file is not importing one — extracts every `Reason*`
literal, and fails if any lacks an entry. Same instinct as `check-stale-refs.sh`:
the compiler cannot see prose, and this repository has already been bitten by that.

### What PR 2 changed relative to this plan

Four things were wrong here, and each was caught by something built to catch it.

- **The Makefile and Dockerfile mount the repository root, not `mcp/`.** Mounting
  only this module made both drift guards fail rather than skip, because the file
  they guard is in `ns/`. A guard that cannot see what it guards must not be able to
  pass quietly, so the fix was to widen the context — `docker build -f mcp/Dockerfile .`
  — and copy `ns/` into the build stage. It is 1.4 MB of source and is discarded in
  the distroless layer.
- **`tools.New` exists so `main` and the tests build the same server.** They each
  called `mcp.NewServer` themselves; the tests passed `Instructions: nil`, so every
  assertion about what a client learns at initialize was asserting nothing. The
  orientation meant to stop an agent making a wrong first move was never reaching
  anyone. Found by running the suite against the real binary.
- **`tools/list` is sorted by the SDK**, not emitted in registration order. That is a
  stronger determinism guarantee than the plan assumed, so the test pins the sort
  rather than the order.
- **`duty_cycle_denied` does not mean what the vocabulary file says it means.** The
  e2e run produced that reason for a frequency outside every EU868 sub-band, and
  `ns/internal/engine/dutycycle.go` counts four distinct denials under it:
  `duty_cycle`, `dwell_time`, `frame_exceeds_budget`, `outside_sub_band`. Only two
  are about budget, and `outside_sub_band` is a configuration error that waiting will
  never clear. The commentary now says so. **The vocabulary file's own comment is
  still narrower than the code's behaviour** — worth correcting in `ns/`, but that is
  a documentation change to the network server and does not belong in this PR.

The plan also assumed a `f_port`/payload limit would need replicating locally. It
does not: those belong to downlink submission, which is PR 3, and no read tool takes
a payload.

## PR 3 — writes, profiles, audit — done

`MCP_PROFILE=read|operate`, fixed at process start, logged loudly at start, and able
only to add tools on top of the read set. Under `2026-07-28` the tool list must not
vary per-connection or as a side effect of another request; varying by process config
is permitted, and that is all this does.

| Tool | Profile | Notes |
| --- | --- | --- |
| `enqueue_downlink`, `cancel_downlink`, `request_device_status` | `operate` | The network server's `explanation` field is passed through verbatim: "the frame goes out in the device's next RX window" is what stops an agent retry-looping on a 202. The 409 explaining one-pending-frame-per-device is carried back in the upstream's own words |
| `patch_device` | `operate` | Pointer-typed arguments throughout, so renaming a device cannot send `enabled:false`. Marked `destructiveHint` always rather than per-call: an annotation is fixed at registration and cannot describe one invocation, and the tool *can* silence a device |

### What PR 3 changed relative to this plan

**There is no `admin` profile, and `update_settings` is not in the build.** The plan
put the facility region there, but that endpoint belongs to the console API, not the
network server — a second upstream, one that has no authentication at all. Shipping it
would have meant this process holding a path to an unauthenticated endpoint that can
change a regulatory setting, and inventing a tier to hold it would have advertised a
permission level that does not exist. `MCP_PROFILE=admin` is refused at startup with
that reason, so someone who set it from this document learns why rather than debugging
a silent fallback.

**Provisioning, integration writes and model writes are out of every profile**, not
just out of v1. The reasoning is in the package comment of `internal/tools`: an AppKey
would transit an LLM context, a delivery destination is an exfiltration channel and an
SSRF primitive, and a payload formatter is JavaScript executed inside the network
server's own process.

Plus: one audit line per call at Info, including refusals, carrying the tool, DevEUI,
an argument digest rather than the arguments, the caller's stated reason, the upstream
status and the duration. Reads are audited at Debug so "what did it look at before it
said that" is answerable without burying the writes. `app_key`, `nwk_s_key`,
`app_s_key`, `token`, `secret` and friends are scrubbed from any upstream body before
it reaches a model, and the device inventory is capped at 200 members with the
truncation carried as a member of the array rather than as prose, so a count of what
is present cannot be mistaken for a count of what exists.

## PR 4 — resources and prompts — done

Resources `fieldnode://status`, `fieldnode://devices`,
`fieldnode://devices/{dev_eui}`, `fieldnode://event-vocabulary`,
`fieldnode://devices/{dev_eui}/session`. Prompts as runbooks that name the tools they
expect: `diagnose-device`, `why-no-downlink` (gateway `PULL_DATA` presence, then
`last_tx_ack_error`, then duty-cycle budget — the order the troubleshooting table in
`ns/README.md` already prescribes), `fleet-health`.

### What PR 4 changed relative to this plan

- **`fieldnode://regions` was dropped.** It would have been a static list of band names
  that the network server already reports on `/status` and the console already reads from
  it. A resource that duplicates another source is one more thing that can disagree.
- **A session resource was added** beside the device one. The plan listed only
  `devices/{dev_eui}`, and a host pinning context for a device wants its radio state as
  much as its configuration.
- **Templates are a separate list.** `resources/list` and `resources/templates/list` are
  different calls, so the templated URIs are discoverable only through the second. The
  plan did not distinguish them; a test now asserts both halves, because a registration
  in the wrong list passes a test that only calls `ListResources`.
- **A runbook identifier guard was added**, which the plan did not call for. A prompt
  that tells an agent to call a tool that no longer exists costs a turn on a refusal —
  and if the name is close to a real one, the agent may call *that*, which on this
  network means transmitting. Every snake_case identifier in a runbook must be an
  advertised tool or a listed field, kind or reason; the allowlist may not contain a
  tool name and may not contain an unused entry, so a rename cannot hide inside it.
- **Instructions became profile-dependent.** The read-only wording was unconditional, so
  enabling writes made its first sentence false in exactly the deployment where being
  wrong costs most. A test now compares the orientation against the tool list.

## PR 5 — Streamable HTTP and compose — done

`MCP_HTTP_ADDR` as a service on the internal `backend` network with its own
`MCP_AUTH_TOKEN`, compared in constant time as `requireAuth` in `ns/cmd/ns/api.go` does.
The session removal in `2026-07-28` did make this simpler: the listener is stateless, so
there is no idle session to leak.

### What PR 5 changed relative to this plan

- **A flag became an environment variable.** `--http :8092` became `MCP_HTTP_ADDR`,
  because this process takes no other flags and the `flag` package exits on an
  unrecognised argument — which is wrong when an MCP host is the one supplying the
  command line. `-healthz` is parsed by hand for the same reason.
- **"Never published" became "loopback by default".** An endpoint nothing can reach is
  not a safer endpoint, it is a dead one, and the repo already publishes the status port
  on loopback with a documented bind override. Publishing on `0.0.0.0` is documented as
  requiring TLS in front, because a bearer token over plain HTTP is readable on the wire.
- **Reusing `NS_API_TOKEN` as `MCP_AUTH_TOKEN` is refused at startup**, not merely
  discouraged. The whole reason for a second secret is independent rotation and blast
  radius; a copy-paste that makes them equal defeats it silently.
- **The compose service is behind a profile.** Otherwise `docker compose up` would add a
  door to every existing deployment, and would fail for any that had not set a new
  required variable.
- **The binary is its own health probe**, matching `ns`: the runtime image is distroless
  and has no curl. It deliberately does not consult the network server — a probe that
  failed whenever the ns was down would have the orchestrator restart a healthy
  container while the thing needing attention sat unmentioned.
- **The default-client question was tested rather than assumed.** The SDK's client opens
  a standalone SSE stream and a stateless listener answers that GET with 405. Whether
  that is fatal is a property of client and server together. It is not fatal, so no host
  needs a special flag — but that is now a test, not a hope.
- **No rate limit**, stated rather than silently absent: a useful one needs to know which
  caller is which, and one shared bearer token cannot distinguish two of them.

## Verification

```sh
cd mcp && make test vet fmt     # Docker, golang:1.25, repo root mounted
cd ns  && make test             # PR 1 handlers, against real engine state
cd ns  && make test-pg          # RecentEvents against a real PostgreSQL
./check-stale-refs.sh
```

Protocol-level tests run over `mcp.NewInMemoryTransports()`: the tool list, the
read-only annotations, and the rule that a bad argument is an `isError` tool result
the model can read rather than a JSON-RPC error it cannot. DevEUI width is checked
locally so an agent gets one clear sentence instead of an upstream 400 to interpret.

Both drift guards were verified by mutation: renaming a vocabulary entry and deleting
an event kind each produced a failure naming the exact string and the file to fix.

End to end, against a real network server on PostgreSQL with a real OTAA join driven
over UDP: initialize, `tools/list`, `network_status`, `list_events` and
`diagnose_device` were called through the actual stdio transport with real JSON-RPC,
and the digest reported the join, the session, the refusal and its verbatim detail.

## Limits to state rather than discover

- **Human confirmation is the host's job.** Annotations are hints. What actually
  refuses is profile configuration plus whatever the host prompts, and the README
  should say so instead of implying the server enforces consent.
- **One token is full power within its profile.** No per-device or per-tenant
  isolation; that would need scopes the network server does not have.
- **Class C is unimplemented** (see `ns/README.md`), so "send this now" has no
  meaning. Every write tool's description must carry the queue semantics.
- **The in-memory store has no event history**, so `list_events` answers 503 there,
  exactly as `list_uplinks` does today.

## A note on paths in this file

`check-stale-refs.sh` resolves every repository-anchored path in prose, so a plan
that names files it intends to create fails the check it is supposed to pass. Files
that exist are written by their full path; the `mcp/` tree PR 2 would create was
outside the guard's patterns by construction, and its paths were written as planned.

PR 1's and PR 2's files now exist, so their paths are anchored — which is the point:
the guard goes green when the plan becomes code, and red if it drifts. PR 3 to PR 5
still describe files that do not exist, so anything naming one stays module-relative
until it lands.
