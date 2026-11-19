-- P1: every gateway that heard a frame, not only the one that won.
--
-- Before aggregation the server discarded a frame's second copy, so the uplink
-- row recorded one gateway and the deployment lost the only evidence it had of
-- where a device is actually reachable from. The chosen gateway still leads —
-- it is the one the answer went back through — but the others are recorded too,
-- because coverage is a property of the network rather than of arrival order.
--
-- One row per (uplink, gateway) rather than a JSON array: the question this table
-- exists to answer is "which gateways can reach device X", which is a join, and a
-- join needs columns. The measurement is per reception, so it lives here rather
-- than on the uplink row, which carries the best of the set.
CREATE TABLE IF NOT EXISTS ns_uplink_gateways (
    uplink_id BIGINT NOT NULL REFERENCES ns_uplinks (uplink_id) ON DELETE CASCADE,
    gateway_id VARCHAR(16) NOT NULL CHECK (gateway_id ~ '^[0-9A-F]{16}$'),

    frequency_hz BIGINT NOT NULL,
    data_rate_index INT NOT NULL,
    rssi REAL NOT NULL,
    snr REAL NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,

    -- True for the copy the engine answered through. Keeping it as a flag rather
    -- than a foreign key back to ns_uplinks.gateway_id means the two can be
    -- compared, and a disagreement is a bug worth finding.
    chosen BOOLEAN NOT NULL DEFAULT FALSE,

    PRIMARY KEY (uplink_id, gateway_id)
);

COMMENT ON TABLE ns_uplink_gateways IS
    'Every gateway that heard an uplink, with the measurement each one made';
