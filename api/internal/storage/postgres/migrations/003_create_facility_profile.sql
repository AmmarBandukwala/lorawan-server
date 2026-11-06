CREATE TABLE IF NOT EXISTS facility_profile (
    singleton_id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (singleton_id = 1),
    facility_name TEXT NOT NULL CHECK (length(facility_name) BETWEEN 1 AND 120),
    region TEXT NOT NULL CHECK (region IN ('EU868', 'US915', 'AU915', 'AS923', 'IN865', 'KR920')),
    operator_name TEXT NOT NULL CHECK (length(operator_name) BETWEEN 1 AND 120),
    operator_role TEXT NOT NULL CHECK (length(operator_role) BETWEEN 1 AND 80),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
