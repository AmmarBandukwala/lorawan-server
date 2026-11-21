-- Integration destinations: where decoded events are pushed in real time.
--
-- These rows are configuration, not protocol state. Nothing in the radio path
-- reads this table; a supervisor loads it and hands the resulting sinks to the
-- dispatcher, so an operator adding a destination over the API takes effect
-- without a restart and cannot delay an uplink.
--
-- Why this is a table and not just NS_WEBHOOK_URLS: an endpoint that can only be
-- changed by editing an environment variable and restarting the process is not
-- really configurable. It also means a broker that is briefly unreachable has to
-- be reachable at the moment the container starts, which is the wrong coupling.
CREATE TABLE IF NOT EXISTS ns_integrations (
    -- Same slug rule as ns_device_models.model_id, so one convention covers both
    -- things an operator types into a ticket.
    id TEXT PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9_-]{1,63}$'),

    kind TEXT NOT NULL CHECK (kind IN ('webhook', 'mqtt')),
    name TEXT NOT NULL CHECK (name <> ''),

    -- Switching a destination off keeps its configuration, which is what an
    -- operator wants while a downstream system is down for maintenance: the
    -- panel still shows the destination exists, and it comes back on its own.
    enabled BOOLEAN NOT NULL DEFAULT true,

    -- Kind-specific fields. JSONB rather than one nullable column per field
    -- because webhook and MQTT share almost nothing: a URL versus a broker,
    -- topic, client id, username, QoS and retain flag. A wide table would spend
    -- most of its columns NULL for every row, and adding a field to one kind
    -- would mean a migration.
    --
    -- The shape is validated in Go (integration.Destination.Validate), not here.
    -- What the schema enforces is the parts that must hold for the table itself
    -- to make sense: kind, id, name, and that config is an object.
    config JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(config) = 'object'),

    -- The webhook bearer token or the MQTT password. Kept in its own column
    -- rather than inside config so the read path can exclude it by name and
    -- cannot accidentally serialise it: a secret that lives inside a JSON blob
    -- tends to end up inside a log line.
    --
    -- It is still plaintext at rest, which is the same posture as the device
    -- keys in ns_devices; encrypting one without encrypting the other would be
    -- theatre. A deployment that needs it encrypted at rest needs it for both.
    secret TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE ns_integrations IS
    'Destinations that decoded events are pushed to; configuration, read by a supervisor rather than the radio path';
COMMENT ON COLUMN ns_integrations.secret IS
    'Webhook bearer token or MQTT password; write-only through the API, plaintext at rest like ns_devices keys';
COMMENT ON COLUMN ns_integrations.config IS
    'Kind-specific fields, validated by integration.Destination.Validate rather than by the schema';

-- Only one destination may be named the same, because the panel and the logs
-- identify destinations by name as well as id.
CREATE UNIQUE INDEX IF NOT EXISTS ns_integrations_name_key ON ns_integrations (lower(name));

-- The supervisor loads enabled rows on every change; this keeps that a scan of
-- what is actually live rather than of history.
CREATE INDEX IF NOT EXISTS ns_integrations_enabled_idx ON ns_integrations (enabled);
