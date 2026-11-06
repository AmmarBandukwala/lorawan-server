CREATE TABLE IF NOT EXISTS gateways (
    gateway_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    region TEXT NOT NULL CHECK (region IN ('EU868', 'US915', 'AU915', 'AS923', 'IN865', 'KR920')),
    last_seen TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS devices (
    dev_eui VARCHAR(16) PRIMARY KEY CHECK (dev_eui ~ '^[0-9A-F]{16}$'),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    device_group TEXT NOT NULL DEFAULT 'Unassigned',
    lifecycle_state TEXT NOT NULL DEFAULT 'provisioned'
        CHECK (lifecycle_state IN ('provisioned', 'joining', 'active', 'disabled')),
    activation_mode TEXT NOT NULL DEFAULT 'OTAA'
        CHECK (activation_mode IN ('OTAA', 'ABP')),
    last_gateway_id TEXT REFERENCES gateways(gateway_id),
    last_seen TIMESTAMPTZ,
    battery_percent SMALLINT NOT NULL DEFAULT 0 CHECK (battery_percent BETWEEN 0 AND 100),
    rssi SMALLINT NOT NULL DEFAULT 0 CHECK (rssi BETWEEN -200 AND 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS devices_last_seen_idx ON devices (last_seen DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS devices_group_idx ON devices (device_group);

CREATE TABLE IF NOT EXISTS device_sessions (
    dev_eui VARCHAR(16) PRIMARY KEY REFERENCES devices(dev_eui) ON DELETE CASCADE,
    dev_addr VARCHAR(8) NOT NULL UNIQUE CHECK (dev_addr ~ '^[0-9A-F]{8}$'),
    f_cnt_up BIGINT NOT NULL DEFAULT 0 CHECK (f_cnt_up >= 0),
    n_f_cnt_down BIGINT NOT NULL DEFAULT 0 CHECK (n_f_cnt_down >= 0),
    a_f_cnt_down BIGINT NOT NULL DEFAULT 0 CHECK (a_f_cnt_down >= 0),
    key_epoch BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS uplinks (
    uplink_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dev_eui VARCHAR(16) NOT NULL REFERENCES devices(dev_eui),
    f_cnt_up BIGINT NOT NULL CHECK (f_cnt_up >= 0),
    f_port SMALLINT NOT NULL CHECK (f_port BETWEEN 0 AND 223),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload BYTEA NOT NULL,
    UNIQUE (dev_eui, f_cnt_up)
);

CREATE INDEX IF NOT EXISTS uplinks_received_idx ON uplinks (received_at DESC);
CREATE INDEX IF NOT EXISTS uplinks_device_idx ON uplinks (dev_eui, received_at DESC);

CREATE TABLE IF NOT EXISTS uplink_gateways (
    uplink_id BIGINT NOT NULL REFERENCES uplinks(uplink_id) ON DELETE CASCADE,
    gateway_id TEXT NOT NULL REFERENCES gateways(gateway_id),
    rssi SMALLINT NOT NULL,
    snr REAL NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (uplink_id, gateway_id)
);

CREATE TABLE IF NOT EXISTS downlinks (
    downlink_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dev_eui VARCHAR(16) NOT NULL REFERENCES devices(dev_eui),
    f_port SMALLINT NOT NULL CHECK (f_port BETWEEN 1 AND 223),
    payload BYTEA NOT NULL,
    confirmed BOOLEAN NOT NULL DEFAULT FALSE,
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'sent', 'acknowledged', 'failed', 'expired')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    scheduled_at TIMESTAMPTZ,
    sent_at TIMESTAMPTZ,
    attempts SMALLINT NOT NULL DEFAULT 0 CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS downlinks_queue_idx ON downlinks (created_at)
    WHERE state = 'queued';

