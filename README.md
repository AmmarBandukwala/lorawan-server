# Fieldnode Network Server

An on-premise LoRaWAN network server and operations console. The network server
is written from scratch in Go under [`ns/`](ns/): it implements LoRaWAN 1.0.x
device authentication, session and counter management, MAC commands, adaptive
data rate, duty-cycle enforcement and the Semtech UDP gateway protocol, and it
talks to packet forwarders directly — no gateway bridge, MQTT broker or Redis in
the deployment.

Three containers and one database:

| Service | What it does |
| --- | --- |
| `ns` | Network server. UDP 6858 with gateways, status on 8090, application API on 8091. |
| `api` | The console's read API and provisioning proxy. Reads the network server's tables. |
| `web` | React console behind nginx, which proxies `/api` to `api`. |
| `db` | PostgreSQL. Each service owns its own tables through its own migration runner. |

## Run it

```sh
cp .env.example .env
# Fill in POSTGRES_PASSWORD, NETWORK_NET_ID and NS_API_TOKEN.
# Generate values with: openssl rand -hex 24
docker compose up --build -d
```

Compose refuses to start when a required variable is missing rather than
substituting a development password.

Open **http://127.0.0.1:8082** for the console.

The console binds to loopback because it has no login of its own. For LAN or
WAN access, put it behind an authenticated TLS reverse proxy — do not publish the
port directly. The gateway UDP port is the one listener that must be reachable
from the network; restrict it to the gateway subnet at the facility firewall.

## Connect a gateway

Point the packet forwarder at this host's UDP port **6858** (`server_port` in
`global_conf.json`). No gateway registration is needed: a forwarder that sends
`PULL_DATA` is registered and considered online, and its address is where
downlinks go. Check `http://127.0.0.1:8090/status` for the registry, radio health
and transmission errors reported by the concentrator (`TOO_LATE`, `TX_COL`).

## Register a device

Use **Devices → Register a device** and enter the DevEUI and AppKey from the
device label. That call goes to the network server's provisioning API, which
validates the credentials and stores them; keys are written once and never
returned by any endpoint. A device can join as soon as it appears in the list.

To provision from a script or another system instead, call the network server's
API directly (it also accepts ABP and per-device radio limits):

```sh
curl -X POST http://127.0.0.1:8091/api/v1/devices \
  -H "Authorization: Bearer $NS_API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"dev_eui":"0011223344556677","app_key":"2B7E151628AED2A6ABF7158809CF4F3C","name":"Boiler sensor"}'
```

## Try it without radio hardware

`ns/cmd/nsbench` simulates a gateway and a class-A device against a running
server — join, uplink, downlink, MAC round trip:

```sh
cd ns && make smoke
```

## Tests

```sh
cd ns && make test      # network server: codec, region, engine, transport, API
cd api && go test ./... # console API
npm install && npm run lint && npm run build
```

`ns/Makefile` runs the Go toolchain in Docker, so no local Go install is needed.
`make test-pg` runs the storage tests against a real PostgreSQL; `NS_TEST_DSN`
points them at a database.

## Layout

```
ns/            network server — protocol codec, regions, engine, gateway
               transport, integrations, provisioning API (see ns/README.md)
api/           console API: reads ns_* tables, provisions through the ns API
src/           React console
db/            database bootstrap (schema is owned by the services' migrations)
```

## Credits and licensing

The protocol behaviour follows the LoRaWAN 1.0.3 and RP002 1.0.3 specifications,
RFC 4493 and the LoRaWAN ADR application note. A small number of test vectors and
one algorithm's parameterisation were derived from an MIT-licensed LoRaWAN
implementation; [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) lists exactly
what and reproduces the required notice.

This repository does not yet carry a `LICENSE` file. Add one before distributing
the code or accepting contributions from outside.

## Stopping and data

`docker compose down` stops the stack; PostgreSQL data stays in the
`postgres-data` volume. Back that volume up — it holds device keys and join
state, which cannot be reconstructed from the devices themselves.

**Changing `POSTGRES_PASSWORD` later does not change the database's password.**
PostgreSQL applies it only when the volume is first initialised, so editing it in
`.env` afterwards leaves the services unable to connect. Either change it in both
places:

```sh
docker compose exec db psql -U fieldnode -d fieldnode -c \
  "ALTER ROLE fieldnode WITH PASSWORD 'new password'"
```

or accept the data loss and start over with `docker compose down -v`.

## Verifying an install

After `docker compose up`, a device registered through the console should appear
in the list as `Offline` / `Not joined`. To confirm the radio path end to end
without hardware, run the simulator against the running server:

```sh
cd ns && make smoke
```

Then the console should show the gateway online, the device online, and a
"Device joined" event. If the console shows devices but no traffic, the gateway
is not reaching UDP 6858 — check `http://127.0.0.1:8090/status`, which lists every
forwarder the server has heard from.

## Status

Read [`ns/README.md`](ns/README.md) for the milestone roadmap and, importantly,
the list of what the network server does not do yet (class C/B, LR-FHSS,
multi-gateway reception merging, hardware timing validation). EU868, US915 and
AS923 are implemented; the console's region selector offers what the server
enforces.
