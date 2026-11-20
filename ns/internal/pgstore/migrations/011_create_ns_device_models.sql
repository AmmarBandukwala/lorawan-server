-- Device models: the shape a sensor of one kind has, including how to decode it.
--
-- A payload formatter belongs to a sensor MODEL, not to a device. A deployment
-- with ten thousand cold-chain probes has one decoder, and storing it per device
-- means ten thousand copies to edit when the vendor fixes a rounding bug -- with
-- no way to know which of them you have actually reached.
--
-- The model is deliberately optional (ns_devices.model_id is nullable). A device
-- with no model still delivers its raw payload; not having a formatter is a
-- normal state, not an error to be corrected.
--
-- codec_script holds vendor-supplied JavaScript that the server executes. That
-- is the whole point and the whole risk, so it is stated here as well as in
-- internal/codec: the source of this column is not this repository. Execution is
-- bounded by a deadline and a call-stack cap, the engine exposes no I/O, and a
-- script that fails to parse is refused at save time rather than at first use.
CREATE TABLE IF NOT EXISTS ns_device_models (
    -- A slug rather than a generated id, because operators type it into gateway
    -- configs and paste it into tickets, and "cold-chain-probe-v2" survives that
    -- journey while an opaque uuid does not.
    model_id TEXT PRIMARY KEY CHECK (model_id ~ '^[a-z0-9][a-z0-9_-]{1,63}$'),
    name TEXT NOT NULL CHECK (name <> ''),
    description TEXT NOT NULL DEFAULT '',

    -- Mirrors codec.Kind. NONE is the default so a model created to group
    -- devices does not silently start executing anything.
    codec_kind TEXT NOT NULL DEFAULT 'NONE'
        CHECK (codec_kind IN ('NONE', 'JS')),

    -- Empty is meaningful alongside NONE: a JS model with no script is a
    -- half-configured formatter and should be visible as such, whereas NONE
    -- says nobody intended one.
    codec_script TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE ns_device_models IS
    'Per-model device shape and payload formatter; the formatter is vendor-supplied code';
COMMENT ON COLUMN ns_device_models.codec_script IS
    'Vendor payload formatter, executed by internal/codec under a deadline; NOT trusted code';

-- A model in use must not be removable out from under its devices, and deleting
-- one must not delete the devices. SET NULL says exactly that: the sensors carry
-- on reporting, they simply stop being decoded.
ALTER TABLE ns_devices ADD COLUMN IF NOT EXISTS model_id TEXT
    REFERENCES ns_device_models(model_id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS ns_devices_model_idx ON ns_devices (model_id);
