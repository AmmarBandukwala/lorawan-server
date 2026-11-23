# Fieldnode LoRaWAN MCP server

An MCP server that lets an agent read a Fieldnode LoRaWAN network: which gateways are
up, what a device is configured as, what it last sent, and what the network refused to
do about it.

**It is read-only unless you turn writes on.** The default profile exposes reads and
nothing else; `MCP_PROFILE=operate` adds four writes that act on a device already on the
network. Three categories — provisioning, integration targets and payload formatters —
stay out of every profile, and the reasoning is in
[What this server deliberately does not do](#what-this-server-deliberately-does-not-do).

## Why it exists

The network server already answers all of this over HTTP. What it does not do is
explain itself. It records `f_cnt_gap_too_large` and `duty_cycle_denied` because
machine-readable values are the right thing to store, and an agent reading those has to
guess — a guessed diagnosis on a live network is worse than no diagnosis.

So this server adds two things the API cannot carry:

- **What each recorded reason means**, kept honest by a test that reads the network
  server's own vocabulary file and fails when the commentary falls behind.
- **`diagnose_device`**, which assembles a device, its session, its recent events, its
  recent uplinks and the state of the network into one answer. One round trip instead of
  five, and it reports the parts that failed rather than omitting them.

## Running it

The process speaks MCP over stdio and is spawned by the host, so point your client at a
command rather than a URL.

```
NS_API_URL=http://127.0.0.1:8091 \
NS_STATUS_URL=http://127.0.0.1:8090 \
NS_API_TOKEN=<the network server's NS_API_TOKEN> \
./fieldnode-mcp
```

Two base URLs, because the network server keeps its unauthenticated health endpoint on a
different port from its authenticated API, and that split is deliberate.
`NS_STATUS_URL` is optional; leaving it unset reads `/status` from the API base, which
usually 404s, and the server warns at startup rather than failing quietly.

The token is the network server's own `NS_API_TOKEN`. It is read once from the
environment, is never a tool argument, and is never logged — which is why it is
configuration and not an input. A token a model can name in a tool call is a token it
can exfiltrate in plain text.

### Claude Desktop / any stdio client

```json
{
  "mcpServers": {
    "fieldnode": {
      "command": "/path/to/fieldnode-mcp",
      "env": {
        "NS_API_URL": "http://127.0.0.1:8091",
        "NS_STATUS_URL": "http://127.0.0.1:8090",
        "NS_API_TOKEN": "..."
      }
    }
  }
}
```

Put the token in the client's config, not in a prompt. Anything that appears in a
prompt appears in a transcript.

## The tools

| Tool | What it answers |
|---|---|
| `network_status` | Band, NetID, uptime, gateways online with per-gateway counters, transports and their ports, delivery counters, duty-cycle usage and denials |
| `list_devices` | The whole inventory. Credentials are never present — the network server does not return them |
| `get_device` | One device's configuration. A refusal here is the fastest answer to "why is this device not joining" |
| `get_device_session` | Counters, data rate, ADR, RSSI/SNR, battery and margin, any queued downlink |
| `list_uplinks` | Recent frames with decoded readings, or the reason the formatter refused |
| `list_events` | The trace: joins, uplinks, downlinks, MAC exchanges, and every refusal with its reason |
| `list_models` / `get_model` | Device models and their payload formatters |
| `list_integrations` / `get_integration` | Where events are delivered |
| `explain_event_reason` | What a recorded reason means and what usually causes it |
| `diagnose_device` | All of the above for one device, in one call |

Those twelve are always present. Four more appear only when the process is started
with `MCP_PROFILE=operate`:

| Tool | What it does | Why it needs care |
|---|---|---|
| `enqueue_downlink` | Queues an application frame for a device's next RX window | It is **not** sent now. A 202 means queued, and retrying is refused with a 409 — correctly |
| `cancel_downlink` | Discards the frame waiting in that slot | The application wanted it sent; after this it will not be, with nothing behind it |
| `request_device_status` | Queues a DevStatusReq for battery and link margin | Reads like a query, is a write: it occupies the same single slot |
| `patch_device` | Partial update to one device's configuration | `enabled:false` takes the device off the network, which looks to its owner like a broken device |

## Profiles

`MCP_PROFILE` is read at process start and cannot be changed by a tool call, because a
permission an agent can widen is not a permission. `read` is the default; `operate` adds
the four writes and nothing else. Starting with `operate` logs a warning, because the
most common way to run a server with writes enabled by accident is to forget it was ever
turned on.

`admin` is refused rather than accepted-and-ignored. The only candidate for an
admin-only tool was the console's facility settings endpoint, which is a different
upstream with no authentication at all, so there is no tier to be in.

Every write is audited at Info whether it succeeded or was refused, carrying the tool,
the DevEUI, a digest of the arguments rather than the arguments, the caller's stated
reason, the upstream status and the duration. Reads are audited at Debug, so "what did
it look at before it said that" is answerable without burying the writes.

### Reading the output without being misled

Three things in a network server's JSON mean something other than what they look like,
and the tools say so in the output rather than relying on the reader to know:

- **`battery: 255`** is the specification's "this device cannot report a level", not a
  full battery. Rendered as `255/255` it reads as the opposite of true.
- **`has_codec: false`** means no formatter is configured. A formatter that has quietly
  broken looks identical to a fleet with no formatters unless you check.
## What this server deliberately does not do

**Three categories are out of every profile**, including `operate`. The network server's
token authorises all of them, and the grounds have nothing to do with which process
asks:

- **Device provisioning.** An AppKey would have to transit an LLM context and land in a
  transcript. The network server's rule is that keys go in and never come out; routing
  them through a model breaks that even though no endpoint returns them.
- **Integration writes.** A destination the network delivers events to is both an
  exfiltration channel and an SSRF primitive.
- **Model writes.** Installing a formatter is installing JavaScript that executes in
  the network server's process.

There is a test that fails if any of those names appears in `tools/list`, so this is
enforced rather than merely asserted.

**No human-in-the-loop guarantee.** MCP can only *hint*. The annotations say a tool is
read-only or destructive; nothing here forces your client to ask permission. Real
refusal comes from which tools exist — fixed at process start, not per call — plus
whatever your host enforces. If your host auto-approves everything, `operate` means an
agent can make devices transmit radio frames without anyone reading the reason it was
asked to supply.

**No per-device isolation.** One token is full access to every device on the network,
read and write alike. There is no scoping in the network server to derive that from.

## Development

```
make test     # the suite, in Docker on the pinned toolchain
make vet
make fmt
```

`go.mod` requires **go 1.25** because the MCP Go SDK does from v1.8.0 onward. `ns/` and
`api/` stay on 1.24; the three modules are independent and nothing here imports across a
module boundary.

The Makefile mounts the **repository root**, not this directory, and the Dockerfile is
built from it (`docker build -f mcp/Dockerfile .`) and copies `ns/` in. Both because the
guards below need to read a file in `ns/`, and a guard that cannot see what it guards
must not be able to pass quietly.

### The guards that outlive a rename

Two tests read `ns/internal/engine/events.go` as **text** and compare it against this
module's copies of the vocabulary. Neither module can import the other — `ns/internal`
is closed by construction — so nothing but these tests notices when the network server
gains a ninth event kind and the tool description still lists eight.

Both were checked by mutation: renaming a vocabulary entry and deleting a kind each
produce a failure naming the exact string and the file to fix.

### Tests worth knowing about

- `internal/nsclient` pins the **path each read actually sends**, because `ns` and `mcp`
  are separate modules with no shared package: a wrong route is not a compile error, it
  is a 404 an agent reports as "that device does not exist".
- `internal/tools` drives the server over in-memory MCP transports and asserts on what a
  client receives, including that argument mistakes arrive as tool results with `isError`
  set rather than as protocol errors — the only form a model can read and correct itself
  against.
- The digest tests are mostly about **missing data**: no session, no formatter, a broken
  events endpoint, a device that does not exist. That is where a composed answer can lie
  by omission.

## Status

Read-only, stdio only, single-tenant. Resources, prompts, and a streamable HTTP
transport with its own authentication are planned; see `PLAN.md`.
