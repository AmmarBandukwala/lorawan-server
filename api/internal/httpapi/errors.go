package httpapi

// Error codes and input limits this API exposes.
//
// The codes are a public contract: a client can switch on them, and the
// dashboard maps HTTP statuses to operator-facing wording around them. They are
// listed here, once, rather than written inline at each call site, so the
// question "what can this endpoint tell me" has one answer.
//
// Renaming a code breaks whoever matches on it. The values are deliberately not
// derived from the limit constants, even where they read alike: a code that
// silently changes its text when a limit is tuned is worse than one that has to
// be updated on purpose.

// Codes for a request that was well-formed JSON but could not be acted on.
const (
	// ErrInvalidJSON is a body that is not JSON, has unknown fields, or has
	// trailing content after the first object. Unknown fields are refused rather
	// than ignored: a client sending a misspelled key would otherwise get a 200
	// and believe the field had been applied.
	ErrInvalidJSON = "invalid_json"

	ErrNameRequired      = "name_must_be_1_to_120_characters"
	ErrDevEUIHex         = "deveui_must_be_16_hex_characters"
	ErrJoinEUIHex        = "joineui_must_be_16_hex_characters"
	ErrAppKeyHex         = "appkey_must_be_32_hex_characters"
	ErrGroupTooLong      = "group_must_be_at_most_60_characters"
	ErrInvalidSettings   = "invalid_settings"
	ErrUnsupportedRegion = "unsupported_region"
)

// Codes for a request that is valid but conflicts with the current state.
const (
	// ErrDeviceAlreadyRegistered is a second registration of a DevEUI that exists.
	ErrDeviceAlreadyRegistered = "device_already_registered"
	// ErrProvisioningNotConfigured means this console has no network server to
	// provision against, so the request can never succeed as configured.
	ErrProvisioningNotConfigured = "provisioning_not_configured"
)

// Codes for a failure behind the API rather than in the request.
const (
	ErrDatabaseUnavailable = "database_unavailable"
	ErrDeviceQueryFailed   = "device_query_failed"
	ErrOverviewQueryFailed = "overview_query_failed"
	ErrGatewayQueryFailed  = "gateway_query_failed"
	ErrEventQueryFailed    = "event_query_failed"
	ErrSettingsQueryFailed = "settings_query_failed"
	ErrSettingsUpdateFail  = "settings_update_failed"
	// ErrNetworkServerUnreachable and ErrNetworkServerTimeout are kept apart
	// because they call for different fixes: one is a network server that is not
	// running or not addressable, the other is one that is running and wedged.
	ErrNetworkServerUnreachable = "network_server_unreachable"
	ErrNetworkServerTimeout     = "network_server_timed_out"
)

// Input limits. Each is enforced here and mirrored by the database schema; the
// two have to agree, because a limit only the database enforces produces an
// internal error instead of a rejection the client can act on.
const (
	// MaxNameLen and MaxOperatorNameLen bound free-text display names. Counted in
	// runes, so a facility name written in a non-Latin script is not rejected at
	// a length that looks arbitrary to whoever typed it.
	MaxNameLen         = 120
	MaxOperatorNameLen = 120
	// MaxOperatorRoleLen and MaxGroupLen bound shorter labels.
	MaxOperatorRoleLen = 80
	MaxGroupLen        = 60

	// HexDigits are the on-wire widths of the identifiers, in hexadecimal digits:
	// 64-bit EUIs and a 128-bit AppKey.
	EUIHexDigits    = 16
	AppKeyHexDigits = 32

	// MaxRequestBytes bounds a registration or settings body. Both are a handful
	// of short strings, so anything larger is not the payload this API expects.
	MaxRequestBytes = 16 * 1024
)
