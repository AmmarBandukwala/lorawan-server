-- 008: gateway transport and firmware.
--
-- The server now speaks two gateway protocols at once, and "which protocol did
-- this gateway reach me through" stops being answerable from the packet once a
-- deployment has both: a station that can only speak LoRa Basics Station is
-- unreachable through the UDP transport no matter how long its address has been
-- in the registry, and that distinction is the difference between a downlink
-- problem and a provisioning problem.
--
-- Firmware and model come from the station's version handshake, which it sends
-- once per connection. When a fleet is mixed, "which gateways are still on the
-- old firmware" is the first question after a bad update, and a station will not
-- answer it on request.

ALTER TABLE ns_gateways ADD COLUMN IF NOT EXISTS transport TEXT NOT NULL DEFAULT '';
ALTER TABLE ns_gateways ADD COLUMN IF NOT EXISTS protocol TEXT NOT NULL DEFAULT '';
ALTER TABLE ns_gateways ADD COLUMN IF NOT EXISTS station_firmware TEXT NOT NULL DEFAULT '';
ALTER TABLE ns_gateways ADD COLUMN IF NOT EXISTS station_model TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN ns_gateways.transport IS
    'semtech-udp or basic-station: which protocol this gateway reached the server through';
COMMENT ON COLUMN ns_gateways.protocol IS
    'Dialect within the transport, for example "semtech-udp v2" or "basic-station"';
