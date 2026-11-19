-- Widens the facility region to every band the network server implements.
--
-- 005 narrowed this CHECK to three bands, which was correct at the time and is
-- now too tight: the network server implements the whole AS923 family, and an
-- operator in a territory that licenses AS923-3 could not record the band their
-- network was actually enforcing. The console reads the authoritative list from
-- the network server (GET /status -> supported_regions), so the database has to
-- accept what that list can contain or the save fails after the form succeeds.
--
-- The list is still a CHECK rather than free text. A region is a regulatory
-- setting, and a typo in a text column reads perfectly in the console while the
-- server applies another band's rules.
--
-- Existing rows hold EU868 or US915, both still permitted, so this cannot fail
-- on current data.

ALTER TABLE facility_profile DROP CONSTRAINT IF EXISTS facility_profile_region_check;
ALTER TABLE facility_profile
    ADD CONSTRAINT facility_profile_region_check
    CHECK (region IN ('US915', 'EU868', 'AS923', 'AS923-2', 'AS923-3', 'AS923-4'));
