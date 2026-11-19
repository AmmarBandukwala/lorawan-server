-- Narrows the facility region to the bands the network server implements.
--
-- The original CHECK listed six regions, which predated the network server and
-- described an aspiration: the API accepted only EU868, and the server now
-- implements EU868, US915 and AS923. A column that permits values nothing
-- supports invites a record that reads correctly in the console and does nothing
-- on the air.
--
-- Any existing row already holds EU868, because that was the only value the API
-- would accept, so the new constraint cannot fail on current data.

ALTER TABLE facility_profile DROP CONSTRAINT IF EXISTS facility_profile_region_check;
ALTER TABLE facility_profile
    ADD CONSTRAINT facility_profile_region_check
    CHECK (region IN ('EU868', 'US915', 'AS923'));
