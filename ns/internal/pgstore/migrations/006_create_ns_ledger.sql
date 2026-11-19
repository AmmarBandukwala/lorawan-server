-- P0: a duty-cycle ledger and a de-duplication book that outlive the process.
--
-- Both exist for the same reason: the server's in-memory state is a claim about
-- the radio, and a restart silently voids it.
--
-- ns_airtime is the compliance evidence. Duty-cycle enforcement is live, so a
-- server that forgets the current window's occupancy can transmit straight
-- through an allowance it had already spent — at boot, when nobody is watching.
-- One row per booked transmission, keyed by the moment it starts, so two servers
-- sharing a gateway each contribute their own emissions and the combined view is
-- more conservative than either alone. Rows are deleted once they age out of the
-- window: an emission older than the period no longer constrains anything, and
-- keeping it would charge a server for a debt the regulation does not ask it to
-- repay.
--
-- ns_seen is the frame de-duplication book. One gateway's forwarder delivers the
-- same radio frame to every server instance it is configured to send to, and
-- exactly one of them may answer. The claim is a single conditional INSERT, so
-- two instances racing on the same frame cannot both win and emit two
-- join-accepts.

CREATE TABLE IF NOT EXISTS ns_airtime (
    gateway_id VARCHAR(16) NOT NULL CHECK (gateway_id ~ '^[0-9A-F]{16}$'),

    -- Band name plus the sub-band's lower edge, which is what the limit is
    -- measured against. The frame's own frequency is deliberately not stored:
    -- one sub-band holds many channels, and booking them separately would
    -- fragment a single budget into pieces that each look affordable.
    region   TEXT   NOT NULL,
    min_hz   BIGINT NOT NULL CHECK (min_hz > 0),

    started_at TIMESTAMPTZ NOT NULL,
    -- Airtime in milliseconds. The values are microseconds in Go, but a
    -- transmission is never shorter than a millisecond in any band we support,
    -- and an integer column keeps the sums readable in psql.
    airtime_ms BIGINT NOT NULL CHECK (airtime_ms > 0),

    PRIMARY KEY (gateway_id, region, min_hz, started_at)
);

-- The startup read is "everything inside the window", and the flush deletes by
-- the same predicate.
CREATE INDEX IF NOT EXISTS ns_airtime_started_at_idx ON ns_airtime (started_at DESC);

COMMENT ON TABLE ns_airtime IS
    'Duty-cycle ledger: one row per booked downlink transmission, per gateway and regulatory sub-band';

CREATE TABLE IF NOT EXISTS ns_seen (
    -- The frame's identity across gateways: DevAddr + FCnt + MIC, or the join
    -- tuple. Opaque to storage; the engine decides what makes two receptions
    -- the same frame.
    dedup_key TEXT PRIMARY KEY,
    seen_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ns_seen_seen_at_idx ON ns_seen (seen_at);

COMMENT ON TABLE ns_seen IS
    'Cross-process frame de-duplication book; a row means some instance has already answered this frame';