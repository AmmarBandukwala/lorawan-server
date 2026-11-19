-- M6/M7: gateway presence, so the dashboard can show gateways at all.
--
-- The live registry lives in the transport (it is the thing that knows which UDP
-- address a gateway is currently speaking from, and that address changes when a
-- gateway behind NAT restarts). This table is that registry's durable shadow: a
-- periodic upsert rather than a write per packet, because a busy forwarder sends
-- a stat every 30 seconds and dozens of rxpk per minute, and none of that needs
-- to touch Postgres to answer "is this gateway up".
--
-- It is deliberately not the authority on reachability. last_seen is what makes
-- a gateway look online, so a gateway that stops reporting ages into "offline"
-- on its own, and a restart of the server does not erase the fact that a gateway
-- existed — which matters, because "I had three gateways and now the list shows
-- one" is otherwise indistinguishable from a data-loss bug.

CREATE TABLE IF NOT EXISTS ns_gateways (
    gateway_id VARCHAR(16) PRIMARY KEY CHECK (gateway_id ~ '^[0-9A-F]{16}$'),

    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL,

    -- Band the server was configured for when it last heard this gateway, and
    -- the protocol version of its last packet. Both are diagnostic: a gateway
    -- reporting v1 while the fleet is v2 is a firmware mismatch you want visible.
    region           TEXT NOT NULL DEFAULT '',
    protocol_version SMALLINT NOT NULL DEFAULT 0,

    -- The gateway's own address at last contact. Kept because "uplinks arrive,
    -- no downlink is ever received" is nearly always a changed address, and
    -- without it that diagnosis requires a packet capture.
    last_addr TEXT NOT NULL DEFAULT '',

    -- Counters from the forwarder's stat object and our own transmission book.
    packets        BIGINT NOT NULL DEFAULT 0,
    rx_received    BIGINT NOT NULL DEFAULT 0,
    rx_ok          BIGINT NOT NULL DEFAULT 0,
    tx_received    BIGINT NOT NULL DEFAULT 0,
    downlinks_sent BIGINT NOT NULL DEFAULT 0,
    downlink_errors BIGINT NOT NULL DEFAULT 0,
    -- The concentrator's own words about the last refused transmission (TOO_LATE,
    -- TX_COL, ...). Empty when the last attempt succeeded.
    last_tx_ack_error TEXT NOT NULL DEFAULT '',

    -- Reported location, degrees. 0/0 when the forwarder has no GPS fix, which
    -- is the common case indoors.
    latitude  DOUBLE PRECISION NOT NULL DEFAULT 0,
    longitude DOUBLE PRECISION NOT NULL DEFAULT 0,
    altitude  INT NOT NULL DEFAULT 0,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ns_gateways_last_seen_idx ON ns_gateways (last_seen DESC);

COMMENT ON TABLE ns_gateways IS
    'Durable shadow of the transport gateway registry; presence and radio health';
