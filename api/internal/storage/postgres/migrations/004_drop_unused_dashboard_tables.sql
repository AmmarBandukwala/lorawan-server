-- Removes the tables that no service ever wrote to.
--
-- The dashboard used to read devices, device_sessions, uplinks, uplink_gateways,
-- downlinks, gateways and network_events. Nothing ever inserted into them: the
-- network server kept the real traffic in its own ns_* tables, so the console
-- showed an empty fleet while frames were being received. The read path now
-- queries ns_* directly (see api/internal/storage/postgres/store.go), which
-- makes these tables not just unused but actively misleading — an operator
-- debugging "no devices" would find an empty devices table and conclude the
-- registration had failed when it had not.
--
-- They are empty by construction, which is what makes dropping them safe. If a
-- deployment somehow has rows here, they came from the old api write path and are
-- stale; comment the DROP out and copy them elsewhere first.
--
-- cascade: devices is referenced by device_sessions.last_gateway_id and
-- device_sessions references devices, so the set has to go together.

DROP TABLE IF EXISTS uplink_gateways CASCADE;
DROP TABLE IF EXISTS uplinks CASCADE;
DROP TABLE IF EXISTS downlinks CASCADE;
DROP TABLE IF EXISTS device_sessions CASCADE;
DROP TABLE IF EXISTS devices CASCADE;
DROP TABLE IF EXISTS gateways CASCADE;
DROP TABLE IF EXISTS network_events CASCADE;
