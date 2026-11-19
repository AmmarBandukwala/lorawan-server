package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fieldnode/lorawan-server/api/internal/domain"
)

// Store reads the dashboard's view of the network.
//
// Everything protocol-shaped comes from the ns_* tables the network server
// owns: ns_devices, ns_sessions, ns_uplinks, ns_downlinks, ns_events and
// ns_gateways. There used to be a second, parallel set of tables here (devices,
// uplinks, gateways, network_events) that nothing ever wrote, which is why the
// dashboard showed zeros forever: the network server had the traffic and the
// dashboard was querying an empty mirror of it.
//
// facility_profile is the exception and stays here. It is a display preference
// the network server has no use for, so giving it a home in the protocol schema
// would be the same category error in the other direction.
type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// deviceFreshness is how recently a device must have been heard from to count as
// reachable. gatewayFreshness is the same for a gateway, and tighter: a
// forwarder reports on a fixed interval, so one silent for three minutes has a
// problem rather than a sleepy battery.
const (
	deviceFreshness  = "10 minutes"
	gatewayFreshness = "3 minutes"
)

// batteryPercent converts the LoRaWAN DevStatus octet to a percentage.
//
// The octet is not a percentage: 0 means an external power source, 254 is full,
// and 255 means the device cannot tell us. Reading 255 as "empty" shows a
// perfectly healthy sensor as about to die; reading 0 as "empty" does the same
// to a mains-powered device.
const batteryPercent = `
	CASE
		WHEN NOT s.has_device_status THEN 0
		WHEN s.battery = 255 THEN 0
		WHEN s.battery = 0 THEN 100
		ELSE round(s.battery * 100.0 / 254)
	END`

// deviceStateSQL derives the dashboard's three-state health from protocol facts.
// There is no stored lifecycle column any more: "active" means it has a session
// and we have heard from it recently, which is all that column was ever trying
// to record by hand.
//
// A constant rather than a function so the aggregate queries below can splice it
// into a const string; the two derivations have to be readable side by side, and
// they cannot be if one is built at run time.
const deviceStateSQL = `
		CASE
			WHEN NOT d.enabled OR s.dev_eui IS NULL OR s.last_uplink_at IS NULL
			     OR s.last_uplink_at < now() - interval '` + deviceFreshness + `' THEN 'Offline'
			WHEN s.has_device_status AND s.battery NOT IN (0, 255)
			     AND s.battery * 100.0 / 254 < 20 THEN 'Needs attention'
			ELSE 'Online'
		END`

// GetOverview aggregates the fleet in one round trip.
//
// The device counters repeat the expressions ListDevices uses, deliberately: an
// overview that disagrees with the list beneath it is worse than either being
// wrong on its own, and keeping both derivations in one file is what lets them
// be reviewed against each other.
func (s *Store) GetOverview(ctx context.Context) (domain.Overview, error) {
	var result domain.Overview
	const metricsQuery = `
		WITH device_health AS (
			SELECT ` + deviceStateSQL + ` AS state
			FROM ns_devices d
			LEFT JOIN ns_sessions s ON s.dev_eui = d.dev_eui
		)
		SELECT
			(SELECT count(*)::int FROM device_health),
			(SELECT count(*)::int FROM device_health WHERE state = 'Online'),
			(SELECT count(*)::int FROM device_health WHERE state = 'Needs attention'),
			(SELECT count(*)::int FROM device_health WHERE state = 'Offline'),
			(SELECT count(*)::int FROM ns_gateways),
			(SELECT count(*)::int FROM ns_gateways WHERE last_seen >= now() - interval '` + gatewayFreshness + `'),
			(SELECT count(*)::int FROM ns_uplinks WHERE received_at >= now() - interval '24 hours'),
			(SELECT count(*)::int FROM ns_sessions WHERE pending_downlink IS NOT NULL),
			(SELECT count(*)::int FROM ns_downlinks WHERE created_at >= now() - interval '24 hours')`

	if err := s.pool.QueryRow(ctx, metricsQuery).Scan(
		&result.DeviceCount, &result.OnlineDevices, &result.AttentionDevices,
		&result.OfflineDevices, &result.GatewayCount, &result.OnlineGateways,
		&result.Uplinks24h, &result.QueuedDownlinks, &result.SentDownlinks24h,
	); err != nil {
		return domain.Overview{}, err
	}

	// max() over an empty table is NULL, which scans into a nil pointer and
	// renders as "Never" rather than being an error.
	if err := s.pool.QueryRow(ctx, `SELECT max(created_at) FROM ns_downlinks`).
		Scan(&result.LastDownlink); err != nil {
		return domain.Overview{}, err
	}

	// generate_series keeps an empty hour in its slot. Without it the array
	// shortens and the chart silently compresses its own time axis, which makes
	// a quiet afternoon look like a busy one.
	//
	// Two details here are easy to get wrong and both were wrong before:
	// without GROUP BY, count() collapses the twenty-four buckets into one row;
	// and the window has to be [now-(h+1), now-h), because [now-h, now-(h-1))
	// puts h=0 in the *next* hour, leaving the chart's last bar permanently empty
	// and drawing the current hour one slot early.
	rows, err := s.pool.Query(ctx, `
		SELECT count(u.uplink_id)::int
		FROM generate_series(23, 0, -1) AS h
		LEFT JOIN ns_uplinks u
			ON u.received_at >= now() - make_interval(hours => h + 1)
			AND u.received_at <  now() - make_interval(hours => h)
		GROUP BY h
		ORDER BY h DESC`)
	if err != nil {
		return domain.Overview{}, err
	}
	defer rows.Close()

	result.HourlyUplinks = make([]int, 0, 24)
	for rows.Next() {
		var count int
		if err := rows.Scan(&count); err != nil {
			return domain.Overview{}, err
		}
		result.HourlyUplinks = append(result.HourlyUplinks, count)
	}
	if err := rows.Err(); err != nil {
		return domain.Overview{}, err
	}
	return result, nil
}

// ListGateways reports every gateway the server has ever heard from, with the
// radio quality of the last day.
//
// Gateways that have gone quiet stay in the list. Dropping them makes "I had
// three gateways and now there is one" look like data loss, when what happened
// is that one stopped reporting — which is the thing worth showing.
func (s *Store) ListGateways(ctx context.Context) ([]domain.Gateway, error) {
	const query = `
		SELECT g.gateway_id, g.region, g.last_seen,
			count(u.uplink_id) FILTER (WHERE u.received_at >= now() - interval '24 hours')::int,
			avg(u.rssi) FILTER (WHERE u.received_at >= now() - interval '24 hours'),
			avg(u.snr) FILTER (WHERE u.received_at >= now() - interval '24 hours')
		FROM ns_gateways g
		LEFT JOIN ns_uplinks u ON u.gateway_id = g.gateway_id
		GROUP BY g.gateway_id, g.region, g.last_seen
		ORDER BY g.last_seen DESC NULLS LAST, g.gateway_id`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	gateways := make([]domain.Gateway, 0)
	for rows.Next() {
		var item domain.Gateway
		if err := rows.Scan(&item.ID, &item.Region, &item.LastSeen,
			&item.Uplinks24h, &item.AverageRSSI, &item.AverageSNR); err != nil {
			return nil, err
		}
		// No name is stored for a gateway, so the identifier is the display
		// value.
		item.Name = item.ID
		gateways = append(gateways, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return gateways, nil
}

// ListDevices returns the inventory with live health.
//
// The gateway column carries the gateway's identifier rather than a friendly
// name: the network server has no concept of naming a gateway, and inventing a
// label here would put a display string between an operator and the one value
// they can paste into a gateway's configuration.
func (s *Store) ListDevices(ctx context.Context) ([]domain.Device, error) {
	const query = `
		SELECT d.dev_eui, d.name,
			COALESCE(NULLIF(d.device_group, ''), 'Unassigned'),
			COALESCE(s.last_gateway_id, 'Not joined'),
			s.last_uplink_at,
			COALESCE(` + batteryPercent + `, 0)::smallint,
			COALESCE(round(s.last_rssi)::smallint, 0),
			` + deviceStateSQL + `
		FROM ns_devices d
		LEFT JOIN ns_sessions s ON s.dev_eui = d.dev_eui
		ORDER BY s.last_uplink_at DESC NULLS LAST, d.name`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	devices := make([]domain.Device, 0)
	for rows.Next() {
		var item domain.Device
		if err := rows.Scan(&item.DevEUI, &item.Name, &item.Group, &item.Gateway,
			&item.LastSeen, &item.Battery, &item.RSSI, &item.State); err != nil {
			return nil, err
		}
		devices = append(devices, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return devices, nil
}

// eventPresentation is how an engine event kind appears on the dashboard.
type eventPresentation struct {
	Type  string // success | warning | info
	Title string
}

var eventKinds = map[string]eventPresentation{
	"join_request": {"info", "Join request"},
	"join_accept":  {"success", "Device joined"},
	"join_reject":  {"warning", "Join rejected"},
	"uplink":       {"info", "Uplink"},
	"downlink":     {"info", "Downlink sent"},
	"uplink_drop":  {"warning", "Uplink dropped"},
	"mac":          {"info", "MAC command"},
	"error":        {"warning", "Error"},
}

// ListEvents returns recent network activity.
//
// The engine records a kind and a machine-readable reason rather than prose, so
// the sentence is assembled here: the protocol layer should not have to know the
// dashboard renders a two-line row.
func (s *Store) ListEvents(ctx context.Context) ([]domain.NetworkEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_id, kind, COALESCE(reason, ''), COALESCE(detail, ''),
		       COALESCE(dev_eui, ''), occurred_at
		FROM ns_events
		ORDER BY occurred_at DESC
		LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]domain.NetworkEvent, 0, 50)
	for rows.Next() {
		var (
			item                      domain.NetworkEvent
			kind, reason, detail, eui string
		)
		if err := rows.Scan(&item.ID, &kind, &reason, &detail, &eui, &item.CreatedAt); err != nil {
			return nil, err
		}
		p, known := eventKinds[kind]
		if !known {
			// An unrecognised kind means the engine grew a new one faster than
			// this mapping did. Showing it beats dropping it silently.
			p = eventPresentation{"info", humanize(kind)}
		}
		item.Type = p.Type
		item.Title = p.Title
		item.Detail = eventDetail(detail, reason, eui)
		events = append(events, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// eventDetail composes the second line from whatever the engine supplied. The
// device is included because the feed is fleet-wide: an event that names no
// device is not actionable.
func eventDetail(detail, reason, devEUI string) string {
	parts := make([]string, 0, 3)
	if detail != "" {
		parts = append(parts, detail)
	}
	if reason != "" {
		parts = append(parts, reason)
	}
	if devEUI != "" {
		parts = append(parts, "device "+devEUI)
	}
	return strings.Join(parts, " · ")
}

// humanize turns "join_accept" into "Join accept".
func humanize(kind string) string {
	out := strings.ReplaceAll(kind, "_", " ")
	if out == "" {
		return out
	}
	return strings.ToUpper(out[:1]) + out[1:]
}

// GetSettings reads the facility display profile. An absent row is not an error:
// it means nobody has opened Settings yet, which is the normal state of a new
// deployment and is rendered as "not configured".
func (s *Store) GetSettings(ctx context.Context) (domain.FacilitySettings, error) {
	var settings domain.FacilitySettings
	err := s.pool.QueryRow(ctx, `
		SELECT facility_name, region, operator_name, operator_role
		FROM facility_profile WHERE singleton_id = 1`).Scan(
		&settings.FacilityName, &settings.Region, &settings.OperatorName, &settings.OperatorRole,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FacilitySettings{}, nil
	}
	if err != nil {
		return domain.FacilitySettings{}, err
	}
	settings.Configured = true
	return settings, nil
}

func (s *Store) UpdateSettings(ctx context.Context, settings domain.FacilitySettings) (domain.FacilitySettings, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO facility_profile (singleton_id, facility_name, region, operator_name, operator_role)
		VALUES (1, $1, $2, $3, $4)
		ON CONFLICT (singleton_id) DO UPDATE
		SET facility_name = EXCLUDED.facility_name,
			region = EXCLUDED.region,
			operator_name = EXCLUDED.operator_name,
			operator_role = EXCLUDED.operator_role,
			updated_at = now()
		RETURNING facility_name, region, operator_name, operator_role`,
		settings.FacilityName, settings.Region, settings.OperatorName, settings.OperatorRole,
	).Scan(&settings.FacilityName, &settings.Region, &settings.OperatorName, &settings.OperatorRole)
	if err != nil {
		return domain.FacilitySettings{}, err
	}
	settings.Configured = true
	return settings, nil
}
