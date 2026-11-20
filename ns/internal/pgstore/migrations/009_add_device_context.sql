-- M8: device context — operator-defined attributes and a installed position.
--
-- Both are dashboard metadata: the engine never reads either column, in the same
-- way it ignores device_group. They live on the device row because a device has
-- one home here, and splitting operator metadata back out into a second table
-- keyed by DevEUI is the arrangement that previously produced a console showing
-- an empty fleet while frames were being received.
--
-- Attributes are a JSON object of string to string rather than a child table.
-- Nothing queries by attribute value yet — the console reads a device and shows
-- what it says — and a child table would buy a join and a second migration
-- before there is a question to answer. The shape is enforced by two CHECK
-- constraints, added in 010 rather than here: 009 may already be recorded as
-- applied on a development database, and a constraint bolted onto a migration
-- that has already run would leave that database permanently missing it.

ALTER TABLE ns_devices ADD COLUMN IF NOT EXISTS attributes JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN ns_devices.attributes IS
    'Operator-defined key/value context, e.g. {"line":"3","purpose":"cold store"}. '
    'The engine ignores it';

-- Position is nullable, and that distinction is the point. ns_gateways records
-- 0/0 for "the forwarder has no GPS fix", which is ambiguous: 0,0 is a real
-- place in the Gulf of Guinea, and a map that plots an unknown device there is
-- worse than one that omits it. A device position is typed by an operator
-- rather than reported by hardware, so NULL means "not recorded" unambiguously.
--
-- The ranges are the WGS84 bounds. They are enforced here as well as in the API
-- because a latitude of 91 is not a value that should be able to exist at all:
-- it renders as a number in the console and is meaningless to anything that
-- tries to use it.
ALTER TABLE ns_devices ADD COLUMN IF NOT EXISTS latitude  DOUBLE PRECISION
    CHECK (latitude  IS NULL OR (latitude  BETWEEN -90   AND 90));
ALTER TABLE ns_devices ADD COLUMN IF NOT EXISTS longitude DOUBLE PRECISION
    CHECK (longitude IS NULL OR (longitude BETWEEN -180  AND 180));
-- Metres above mean sea level. The bound spans the living and working range
-- with room to spare: the lowest road tunnel and the highest staffed station are
-- both inside it, and anything outside is a typo rather than a site.
ALTER TABLE ns_devices ADD COLUMN IF NOT EXISTS altitude  INTEGER
    CHECK (altitude  IS NULL OR (altitude  BETWEEN -1000 AND 10000));

-- A position is three numbers that only mean something together, so partial
-- rows are refused: a device with a latitude and no longitude cannot be plotted,
-- and the console would have to invent the difference between "incomplete" and
-- "absent".
ALTER TABLE ns_devices DROP CONSTRAINT IF EXISTS ns_devices_position_complete;
ALTER TABLE ns_devices ADD CONSTRAINT ns_devices_position_complete CHECK (
    (latitude  IS NULL AND longitude IS NULL AND altitude IS NULL) OR
    (latitude IS NOT NULL AND longitude IS NOT NULL)
);
