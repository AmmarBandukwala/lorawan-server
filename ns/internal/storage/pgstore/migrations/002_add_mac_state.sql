-- M4: adaptive data rate, channel planning and outstanding MAC requests.
--
-- All of it is protocol state rather than reporting state: it is read and written
-- only by the engine, never queried by the dashboard. Storing it as one JSONB
-- document keeps the session statement at the columns that matter for reporting
-- and lets the MAC state evolve without a migration per field. The document is
-- self-describing (JSON tags), so a session written by an older build still
-- loads.

ALTER TABLE ns_sessions ADD COLUMN IF NOT EXISTS mac_state JSONB;

COMMENT ON COLUMN ns_sessions.mac_state IS
    'ADR window, channel plan and outstanding MAC requests, as one JSON document';
