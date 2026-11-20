-- Storing the decoded payload next to the raw one.
--
-- The raw bytes stay authoritative and unchanged: ns_uplinks.frm_payload and
-- raw_phy_payload remain what the network actually heard, and nothing here
-- rewrites them. The two new columns are a projection of that, produced by
-- vendor code that can be wrong, can be edited tomorrow, and can fail on one
-- frame and not the next.
--
-- So the pair is read like this:
--
--   decoded NULL, decode_error NULL   no formatter configured; nothing to show
--   decoded set                       the formatter's reading of this frame
--   decoded NULL, decode_error set    attempted and failed; the reason is here
--
-- The distinction between the first and third matters on a device screen. "No
-- decoder" is a configuration fact about the fleet; "the decoder rejected frame
-- 4,213" is an event, and showing the first where the second belongs hides the
-- only symptom of a device sending a malformed frame.
--
-- Decoded values are stored per frame rather than re-derived on read. Re-running
-- a formatter at query time would mean executing vendor code to serve a page,
-- and it would silently rewrite history whenever an operator edited the script:
-- yesterday's frames would start rendering with today's interpretation, which
-- makes a reported reading impossible to reproduce or dispute.
--
-- JSONB, not TEXT: the console shows decoded values and a queryable column is
-- what makes "which probes reported a temperature above 8" answerable at all.
ALTER TABLE ns_uplinks ADD COLUMN IF NOT EXISTS decoded JSONB;

ALTER TABLE ns_uplinks ADD COLUMN IF NOT EXISTS decode_error TEXT;

-- A row cannot both claim a reading and report a failure to produce one; that
-- combination would mean the writer ignored the formatter's errors field.
ALTER TABLE ns_uplinks DROP CONSTRAINT IF EXISTS ns_uplinks_decode_outcome;
ALTER TABLE ns_uplinks ADD CONSTRAINT ns_uplinks_decode_outcome CHECK (
    decoded IS NULL OR decode_error IS NULL
);

COMMENT ON COLUMN ns_uplinks.decoded IS
    'Formatter output for this frame, kept per-frame so editing the script does '
    'not rewrite history; the raw payload remains authoritative';
COMMENT ON COLUMN ns_uplinks.decode_error IS
    'Why the formatter rejected this frame; NULL with a NULL decoded means no formatter is configured';
