# Fieldnode Network Server

An on-premise LoRaWAN operations console for small facility deployments. The current slice provides a React/Vite dashboard, a Go inventory API, and PostgreSQL in Docker Compose.

## Run locally

```sh
cp .env.example .env
# Generate distinct random values for POSTGRES_PASSWORD,
# CHIRPSTACK_POSTGRES_PASSWORD, and CHIRPSTACK_API_SECRET.
# CHIRPSTACK_API_SECRET must be at least 32 random bytes.
docker compose up --build -d
```

For example, generate a value with `openssl rand -hex 32` and put it directly in `.env`. Compose refuses to start ChirpStack if `CHIRPSTACK_API_SECRET` is empty.

Open http://127.0.0.1:8082 for Fieldnode and http://127.0.0.1:8091 for the ChirpStack network-server console. Fieldnode is loopback-only because its API has no login. For LAN access, front it with an authenticated TLS reverse proxy; do not publish the Fieldnode port directly. ChirpStack's console also binds to loopback by default. The Semtech UDP listener is published on UDP 1700 for gateway traffic; allow it only from the gateway network at the facility firewall.

The first ChirpStack database migration creates an `admin` account. Set its password interactively in the terminal (the password is read from stdin and is not stored in this repository):

```sh
docker compose exec chirpstack chirpstack --config /etc/chirpstack set-password --email admin --stdin
```

Only EU868 is configured in this first protocol integration. Semtech UDP gateways should forward to the host's UDP port 1700 and be registered in the ChirpStack console by gateway EUI. Create an application, a LoRaWAN device profile, and device OTAA credentials in ChirpStack before attempting joins.

Basic Station is available as an optional profile. Place a server certificate, private key, and client CA certificate at `certs/basic-station/server.crt`, `certs/basic-station/server.key`, and `certs/basic-station/client-ca.crt`, then start it with `docker compose --profile basic-station up -d`. The listener requires client TLS certificates; it is not enabled in the default stack.

Stop the services with `docker compose down`. PostgreSQL data stays in the `postgres-data` volume. The SQL in `db/init.sql` runs only when that volume is first created.

For frontend-only work, use `npm install` and `npm run dev`. The Vite server proxies `/api` to `localhost:8080`.

## Current functionality

- ChirpStack v4 network-server core with PostgreSQL migrations, Redis scheduling/state, and MQTT gateway integration.
- ChirpStack Gateway Bridge for Semtech UDP packet forwarders; Basic Station is available with operator-provided TLS certificates.
- EU868 regional configuration, including RX1/RX2 defaults, ADR, and the core's join/session/MAC/downlink processing.
- Fieldnode device inventory list, status filtering, search, and registration persisted through `POST /api/v1/devices`.
- Facility name, region, and operator display profile persisted through `GET` and `PUT /api/v1/settings`.
- Database-backed summary, gateway, and event endpoints at `GET /api/v1/overview`, `GET /api/v1/gateways`, and `GET /api/v1/events`.
- API health endpoint at `/healthz` and inventory endpoint at `GET /api/v1/devices`.
- Fieldnode's database and ChirpStack's protocol database are separate. Device and gateway records created in Fieldnode are not yet synchronized to ChirpStack; configure live gateways, profiles, and devices in the ChirpStack console for now.
- When the Fieldnode API is unavailable, its UI shows an unavailable state and no sample records. A fresh installation has no prefilled facility/operator/gateway/device data.

## LoRaWAN scope

The protocol engine and transport are delegated to ChirpStack, not reimplemented in Fieldnode. The default stack supports Semtech UDP with EU868. Basic Station requires certificates. The Fieldnode GUI/API is not yet a unified management interface for ChirpStack objects, and the custom Fieldnode web/API still lacks authentication. Keep its web port loopback-bound; firewall UDP 1700 to the known gateway subnet. Do not use the development password defaults in production.

The next integration milestone is to connect Fieldnode's device and gateway management API to ChirpStack's authenticated API so one workflow provisions devices, activation keys, profiles, and gateways in the protocol core. Additional regions require matching versioned ChirpStack region configuration and a gateway/channel-plan validation pass.

## Go API layout

- `api/cmd/api`: process startup, configuration wiring, and HTTP server lifecycle.
- `api/internal/httpapi`: routes, request validation, response mapping, and HTTP middleware.
- `api/internal/domain`: application records and persistence-independent errors.
- `api/internal/storage/postgres`: PostgreSQL queries and embedded, versioned migrations.

## Development checks

```sh
npm run lint
npm run build
```

The Go API unit tests run in its Docker build. To run them with a local Go 1.24+ toolchain, use `cd api && go test ./...`.
