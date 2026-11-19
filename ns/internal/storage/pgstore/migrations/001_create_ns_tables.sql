-- Fieldnode network-server core: protocol state.
--
-- These tables are owned by the ns module and hold what the LoRaWAN engine
-- needs to authenticate and track devices. They are deliberately separate from
-- the dashboard tables in db/init.sql: `devices` there is an inventory and UI
-- projection, while ns_devices / ns_sessions are the authoritative protocol
-- state (keys, counters, MAC parameters, pending frames). Milestone M5 makes
-- the API read from here so the two stop overlapping.
--
-- Identifier conventions follow db/init.sql: upper-case hex TEXT with CHECK
-- constraints, so values stay readable in psql and consistent with the schema.

CREATE TABLE IF NOT EXISTS ns_devices (
    dev_eui VARCHAR(16) PRIMARY KEY CHECK (dev_eui ~ '^[0-9A-F]{16}$'),
    join_eui VARCHAR(16) NOT NULL CHECK (join_eui ~ '^[0-9A-F]{16}$'),
    name TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,

    activation_mode TEXT NOT NULL DEFAULT 'OTAA'
        CHECK (activation_mode IN ('OTAA', 'ABP')),
    app_key VARCHAR(32) NOT NULL CHECK (app_key ~ '^[0-9A-F]{32}$'),

    -- ABP credentials; NULL for an OTAA device that has not joined yet.
    dev_addr VARCHAR(8) UNIQUE CHECK (dev_addr IS NULL OR dev_addr ~ '^[0-9A-F]{8}$'),
    nwk_s_key VARCHAR(32) NOT NULL DEFAULT '00000000000000000000000000000000'
        CHECK (nwk_s_key ~ '^[0-9A-F]{32}$'),
    app_s_key VARCHAR(32) NOT NULL DEFAULT '00000000000000000000000000000000'
        CHECK (app_s_key ~ '^[0-9A-F]{32}$'),

    -- OTAA replay state. used_dev_nonces maps JoinEUI -> [DevNonce], because a
    -- DevNonce may never be reused with the same JoinEUI (LoRaWAN 1.0.3 §6.2.4).
    next_join_nonce BIGINT NOT NULL DEFAULT 0 CHECK (next_join_nonce BETWEEN 0 AND 16777215),
    used_dev_nonces JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- Device capabilities; -1 means "no device limit, use the region".
    max_data_rate INT NOT NULL DEFAULT -1,
    min_data_rate INT NOT NULL DEFAULT -1,
    max_tx_power INT NOT NULL DEFAULT -1,
    rx1_delay INT NOT NULL DEFAULT 0 CHECK (rx1_delay BETWEEN 0 AND 15),
    rx2_data_rate INT NOT NULL DEFAULT -1,
    rx2_frequency_hz BIGINT NOT NULL DEFAULT 0 CHECK (rx2_frequency_hz >= 0),
    supports_class_c BOOLEAN NOT NULL DEFAULT FALSE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ns_sessions (
    dev_eui VARCHAR(16) PRIMARY KEY REFERENCES ns_devices(dev_eui) ON DELETE CASCADE,
    dev_addr VARCHAR(8) NOT NULL UNIQUE CHECK (dev_addr ~ '^[0-9A-F]{8}$'),
    nwk_s_key VARCHAR(32) NOT NULL CHECK (nwk_s_key ~ '^[0-9A-F]{32}$'),
    app_s_key VARCHAR(32) NOT NULL CHECK (app_s_key ~ '^[0-9A-F]{32}$'),

    -- Bumped on every (re)join so stored traffic can be tied to the session
    -- that produced it.
    key_epoch BIGINT NOT NULL DEFAULT 1 CHECK (key_epoch >= 1),

    -- f_cnt_up is the *next expected* uplink counter, not the last accepted one.
    f_cnt_up BIGINT NOT NULL DEFAULT 0 CHECK (f_cnt_up >= 0),
    f_cnt_down BIGINT NOT NULL DEFAULT 0 CHECK (f_cnt_down >= 0),
    a_f_cnt_down BIGINT NOT NULL DEFAULT 0 CHECK (a_f_cnt_down >= 0),

    -- MAC / RF state.
    region TEXT NOT NULL,
    data_rate INT NOT NULL DEFAULT 0,
    tx_power INT NOT NULL DEFAULT 0,
    nb_trans INT NOT NULL DEFAULT 1 CHECK (nb_trans BETWEEN 1 AND 15),
    rx1_delay INT NOT NULL DEFAULT 0 CHECK (rx1_delay BETWEEN 0 AND 15),
    rx1_dr_offset INT NOT NULL DEFAULT 0 CHECK (rx1_dr_offset BETWEEN 0 AND 7),
    rx2_data_rate INT NOT NULL DEFAULT -1,
    rx2_frequency_hz BIGINT NOT NULL DEFAULT 0 CHECK (rx2_frequency_hz >= 0),

    -- Outstanding requests: applied only once the device answers.
    awaiting_link_adr_ans BOOLEAN NOT NULL DEFAULT FALSE,
    pending_adr JSONB,
    pending_rx2 JSONB,
    pending_downlink JSONB,

    -- Device status from DevStatusAns.
    has_device_status BOOLEAN NOT NULL DEFAULT FALSE,
    battery SMALLINT NOT NULL DEFAULT 0 CHECK (battery BETWEEN 0 AND 255),
    margin SMALLINT NOT NULL DEFAULT 0 CHECK (margin BETWEEN -128 AND 127),

    -- Radio metadata of the last accepted uplink.
    last_uplink_at TIMESTAMPTZ,
    last_downlink_at TIMESTAMPTZ,
    last_rssi REAL NOT NULL DEFAULT 0,
    last_snr REAL NOT NULL DEFAULT 0,
    last_gateway_id VARCHAR(16) CHECK (last_gateway_id IS NULL OR last_gateway_id ~ '^[0-9A-F]{16}$'),
    last_frequency_hz BIGINT NOT NULL DEFAULT 0,
    last_data_rate_index INT NOT NULL DEFAULT 0,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ns_sessions_dev_addr_idx ON ns_sessions (dev_addr);

CREATE TABLE IF NOT EXISTS ns_uplinks (
    uplink_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    dev_eui VARCHAR(16) NOT NULL REFERENCES ns_devices(dev_eui) ON DELETE CASCADE,
    dev_addr VARCHAR(8) NOT NULL CHECK (dev_addr ~ '^[0-9A-F]{8}$'),
    key_epoch BIGINT NOT NULL,
    f_cnt_up BIGINT NOT NULL CHECK (f_cnt_up >= 0),
    -- -1 when the frame carries no FPort (a MAC-only frame).
    f_port SMALLINT NOT NULL CHECK (f_port BETWEEN -1 AND 223),
    frm_payload BYTEA,
    raw_phy_payload BYTEA NOT NULL,
    gateway_id VARCHAR(16) CHECK (gateway_id IS NULL OR gateway_id ~ '^[0-9A-F]{16}$'),
    frequency_hz BIGINT NOT NULL,
    data_rate_index INT NOT NULL,
    rssi REAL NOT NULL,
    snr REAL NOT NULL,
    confirmed BOOLEAN NOT NULL DEFAULT FALSE,
    retransmission BOOLEAN NOT NULL DEFAULT FALSE,
    -- How many times this counter has been received. The engine records a
    -- retransmission against the same row (see pgstore.RecordUplink) instead of
    -- inserting a duplicate, so the tally keeps the retry visible.
    retransmissions INT NOT NULL DEFAULT 0 CHECK (retransmissions >= 0),
    -- Keyed on the key epoch as well: a re-join resets the counter, so
    -- (dev_eui, f_cnt_up) alone would reject legitimate traffic.
    UNIQUE (dev_eui, key_epoch, f_cnt_up)
);

CREATE INDEX IF NOT EXISTS ns_uplinks_received_idx ON ns_uplinks (received_at DESC);
CREATE INDEX IF NOT EXISTS ns_uplinks_device_idx ON ns_uplinks (dev_eui, received_at DESC);

CREATE TABLE IF NOT EXISTS ns_downlinks (
    downlink_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    dev_eui VARCHAR(16) NOT NULL REFERENCES ns_devices(dev_eui) ON DELETE CASCADE,
    dev_addr VARCHAR(8) NOT NULL CHECK (dev_addr ~ '^[0-9A-F]{8}$'),
    gateway_id VARCHAR(16) CHECK (gateway_id IS NULL OR gateway_id ~ '^[0-9A-F]{16}$'),
    rx_window TEXT NOT NULL CHECK (rx_window IN ('RX1', 'RX2', 'manual')),
    frequency_hz BIGINT NOT NULL,
    data_rate_index INT NOT NULL,
    tx_power_dbm SMALLINT NOT NULL,
    f_cnt_down BIGINT NOT NULL CHECK (f_cnt_down >= 0),
    f_port SMALLINT NOT NULL CHECK (f_port BETWEEN -1 AND 223),
    frm_payload BYTEA,
    raw_phy_payload BYTEA NOT NULL,
    ack BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS ns_downlinks_created_idx ON ns_downlinks (created_at DESC);

CREATE TABLE IF NOT EXISTS ns_events (
    event_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind TEXT NOT NULL,
    reason TEXT,
    detail TEXT,
    dev_eui VARCHAR(16) CHECK (dev_eui IS NULL OR dev_eui ~ '^[0-9A-F]{16}$'),
    dev_addr VARCHAR(8) CHECK (dev_addr IS NULL OR dev_addr ~ '^[0-9A-F]{8}$'),
    gateway_id VARCHAR(16) CHECK (gateway_id IS NULL OR gateway_id ~ '^[0-9A-F]{16}$'),
    f_cnt_up BIGINT NOT NULL DEFAULT 0,
    f_port SMALLINT NOT NULL DEFAULT -1
);

CREATE INDEX IF NOT EXISTS ns_events_occurred_idx ON ns_events (occurred_at DESC);
CREATE INDEX IF NOT EXISTS ns_events_device_idx ON ns_events (dev_eui, occurred_at DESC);

