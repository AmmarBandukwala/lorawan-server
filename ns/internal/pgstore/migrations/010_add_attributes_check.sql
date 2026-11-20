-- M8 follow-up: enforce the attribute shape that 009's comment promised.
--
-- 009 documented that "the CHECK keeps the column honest: an object of strings"
-- but only added the column with a DEFAULT, so nothing enforced it. Rather than
-- edit 009 — which a development database may already have recorded as applied,
-- leaving it permanently different from a fresh one — the constraint arrives as
-- its own migration and applies to both.
--
-- Two constraints, because one cannot express the whole rule. A CHECK may not
-- contain a subquery, so the obvious jsonb_each() test is not available; the
-- jsonpath predicate does the value check instead. Together they accept exactly
-- what the store writes and reject what a hand-edited row produces:
--
--   {"line":"3"}          accepted
--   {}                    accepted
--   ["a","b"]             rejected by ns_devices_attributes_is_object
--   "text"                rejected by ns_devices_attributes_is_object
--   {"line":3}            rejected by ns_devices_attributes_values_are_strings
--   {"a":{"b":"c"}}       rejected by ns_devices_attributes_values_are_strings
--
-- Rejecting a nested object matters more than it looks: the console decodes this
-- column into map[string]string, and a nested value makes that decode fail,
-- which reads as "this device cannot be opened" rather than "one attribute is
-- odd".

ALTER TABLE ns_devices ADD CONSTRAINT ns_devices_attributes_is_object
    CHECK (jsonb_typeof(attributes) = 'object');

ALTER TABLE ns_devices ADD CONSTRAINT ns_devices_attributes_values_are_strings
    CHECK (NOT jsonb_path_exists(attributes,
        'strict $.* ? (@.type() != "string")'));

COMMENT ON CONSTRAINT ns_devices_attributes_is_object ON ns_devices IS
    'attributes is a JSON object, never an array or a scalar';

COMMENT ON CONSTRAINT ns_devices_attributes_values_are_strings ON ns_devices IS
    'every attribute value is a string; the console decodes map[string]string';
