-- M7: device grouping.
--
-- The dashboard groups devices ("Cooling", "Cold store") and filters by group.
-- That is operator metadata rather than protocol state — the engine never reads
-- it — but the device row now lives here, so the label lives here too. Keeping
-- it in a separate dashboard table keyed by DevEUI would reintroduce exactly the
-- split this milestone removes: a device that exists in one place and not the
-- other is the bug class, not the design.
--
-- Nullable with a default rather than NOT NULL so an existing row is not
-- rewritten with a value nobody chose; the API renders the empty string as
-- "Unassigned".

ALTER TABLE ns_devices ADD COLUMN IF NOT EXISTS device_group TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN ns_devices.device_group IS
    'Operator-assigned grouping for the dashboard; the engine ignores it';
