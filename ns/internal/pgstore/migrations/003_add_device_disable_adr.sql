-- M4 added Device.DisableADR and M5's provisioning API exposes it, but the
-- column was never created: an operator could set the flag for the run and it
-- would silently reset on restart. Adding it now, with the same default the Go
-- zero value uses, so existing devices keep adaptive data rate.

ALTER TABLE ns_devices ADD COLUMN IF NOT EXISTS disable_adr BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN ns_devices.disable_adr IS
    'When true the network never sends LinkADRReq to this device, whatever the measured link';
