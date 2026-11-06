package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fieldnode/lorawan-server/api/internal/domain"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) ListDevices(ctx context.Context) ([]domain.Device, error) {
	const query = `
		SELECT d.dev_eui, d.name, d.device_group,
			COALESCE(g.name, 'Not joined'), d.last_seen, d.battery_percent, d.rssi,
			CASE
				WHEN d.lifecycle_state <> 'active' OR d.last_seen IS NULL OR d.last_seen < now() - interval '10 minutes' THEN 'Offline'
				WHEN d.battery_percent < 20 THEN 'Needs attention'
				ELSE 'Online'
			END
		FROM devices d
		LEFT JOIN gateways g ON g.gateway_id = d.last_gateway_id
		ORDER BY d.last_seen DESC NULLS LAST, d.name`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	devices := make([]domain.Device, 0)
	for rows.Next() {
		var item domain.Device
		if err := rows.Scan(&item.DevEUI, &item.Name, &item.Group, &item.Gateway, &item.LastSeen, &item.Battery, &item.RSSI, &item.State); err != nil {
			return nil, err
		}
		devices = append(devices, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return devices, nil
}

func (s *Store) CreateDevice(ctx context.Context, input domain.CreateDevice) error {
	_, err := s.pool.Exec(ctx, `
		WITH created AS (
			INSERT INTO devices (dev_eui, name, device_group, lifecycle_state)
			VALUES ($1, $2, $3, 'provisioned')
			RETURNING dev_eui
		)
		INSERT INTO network_events (event_type, title, detail)
		SELECT 'info', 'Device registered', 'Device ' || dev_eui || ' added to inventory'
		FROM created`, input.DevEUI, input.Name, input.Group)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrDeviceAlreadyExists
		}
		return err
	}
	return nil
}

func (s *Store) GetOverview(ctx context.Context) (domain.Overview, error) {
	var result domain.Overview
	const metricsQuery = `
		SELECT count(*)::int,
			count(*) FILTER (WHERE lifecycle_state = 'active' AND last_seen >= now() - interval '10 minutes' AND battery_percent >= 20)::int,
			count(*) FILTER (WHERE lifecycle_state = 'active' AND last_seen >= now() - interval '10 minutes' AND battery_percent < 20)::int,
			count(*) FILTER (WHERE lifecycle_state <> 'active' OR last_seen IS NULL OR last_seen < now() - interval '10 minutes')::int,
			(SELECT count(*)::int FROM gateways),
			(SELECT count(*)::int FROM gateways WHERE last_seen >= now() - interval '3 minutes'),
			(SELECT count(*)::int FROM uplinks WHERE received_at >= now() - interval '24 hours'),
			(SELECT count(*)::int FROM downlinks WHERE state = 'queued'),
			(SELECT count(*)::int FROM downlinks WHERE sent_at >= now() - interval '24 hours')
		FROM devices`
	if err := s.pool.QueryRow(ctx, metricsQuery).Scan(
		&result.DeviceCount, &result.OnlineDevices, &result.AttentionDevices,
		&result.OfflineDevices, &result.GatewayCount, &result.OnlineGateways,
		&result.Uplinks24h, &result.QueuedDownlinks, &result.SentDownlinks24h,
	); err != nil {
		return domain.Overview{}, err
	}

	if err := s.pool.QueryRow(ctx, `SELECT max(sent_at) FROM downlinks`).Scan(&result.LastDownlink); err != nil {
		return domain.Overview{}, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT count(u.uplink_id)::int
		FROM generate_series(date_trunc('hour', now()) - interval '23 hours', date_trunc('hour', now()), interval '1 hour') AS bucket(hour)
		LEFT JOIN uplinks u ON u.received_at >= bucket.hour AND u.received_at < bucket.hour + interval '1 hour'
		GROUP BY bucket.hour
		ORDER BY bucket.hour`)
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

func (s *Store) ListGateways(ctx context.Context) ([]domain.Gateway, error) {
	const query = `
		SELECT g.gateway_id, g.name, g.region, g.last_seen,
			count(ug.uplink_id) FILTER (WHERE ug.received_at >= now() - interval '24 hours')::int,
			avg(ug.rssi) FILTER (WHERE ug.received_at >= now() - interval '24 hours'),
			avg(ug.snr) FILTER (WHERE ug.received_at >= now() - interval '24 hours')
		FROM gateways g
		LEFT JOIN uplink_gateways ug ON ug.gateway_id = g.gateway_id
		GROUP BY g.gateway_id
		ORDER BY g.name`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	gateways := make([]domain.Gateway, 0)
	for rows.Next() {
		var item domain.Gateway
		if err := rows.Scan(&item.ID, &item.Name, &item.Region, &item.LastSeen, &item.Uplinks24h, &item.AverageRSSI, &item.AverageSNR); err != nil {
			return nil, err
		}
		gateways = append(gateways, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return gateways, nil
}

func (s *Store) ListEvents(ctx context.Context) ([]domain.NetworkEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_id, event_type, title, detail, created_at
		FROM network_events
		ORDER BY created_at DESC
		LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]domain.NetworkEvent, 0)
	for rows.Next() {
		var item domain.NetworkEvent
		if err := rows.Scan(&item.ID, &item.Type, &item.Title, &item.Detail, &item.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

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
