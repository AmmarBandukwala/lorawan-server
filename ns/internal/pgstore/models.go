package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
)

// Device models and their payload formatters.
//
// The formatter is vendor-supplied code stored in this database and executed by
// internal/codec. Nothing here executes it; this file only moves text in and
// out. The validation that a JS model carries a script happens at save time,
// because a model that saves cleanly and then fails on the first uplink puts the
// diagnosis in a log line nobody is reading, while a refusal at save time puts it
// in front of the person who made the mistake.

const modelColumns = `model_id, name, description, codec_kind, codec_script, created_at, updated_at`

// ListModels implements engine.ModelRepo.
func (s *Store) ListModels(ctx context.Context) ([]*engine.DeviceModel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+modelColumns+` FROM ns_device_models ORDER BY model_id`)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list models: %w", err)
	}
	defer rows.Close()

	var out []*engine.DeviceModel
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ModelByID implements engine.ModelRepo.
func (s *Store) ModelByID(ctx context.Context, modelID string) (*engine.DeviceModel, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+modelColumns+` FROM ns_device_models WHERE model_id = $1`, modelID)
	return scanModel(row)
}

// SaveModel implements engine.ModelRepo, inserting or updating by model ID.
func (s *Store) SaveModel(ctx context.Context, m *engine.DeviceModel) error {
	if m == nil || m.ModelID == "" {
		return errors.New("pgstore: model has no model_id")
	}
	if m.CodecKind == "JS" && m.CodecScript == "" {
		return errors.New("pgstore: a JS model needs a payload formatter")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ns_device_models (model_id, name, description, codec_kind, codec_script)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (model_id) DO UPDATE SET
			name         = EXCLUDED.name,
			description  = EXCLUDED.description,
			codec_kind   = EXCLUDED.codec_kind,
			codec_script = EXCLUDED.codec_script,
			updated_at   = now()`,
		m.ModelID, m.Name, m.Description, m.CodecKind, m.CodecScript)
	if err != nil {
		return fmt.Errorf("pgstore: save model %s: %w", m.ModelID, err)
	}
	return nil
}

// DeleteModel implements engine.ModelRepo. The foreign key is ON DELETE SET
// NULL, so the devices that referenced it survive and simply stop being decoded.
func (s *Store) DeleteModel(ctx context.Context, modelID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ns_device_models WHERE model_id = $1`, modelID)
	if err != nil {
		return fmt.Errorf("pgstore: delete model %s: %w", modelID, err)
	}
	if tag.RowsAffected() == 0 {
		return engine.ErrNotFound
	}
	return nil
}

// DevicesOnModel counts the devices referencing a model, which is what the
// console shows before a delete so removing one is an informed act rather than a
// surprise for whoever owns those sensors.
func (s *Store) DevicesOnModel(ctx context.Context, modelID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ns_devices WHERE model_id = $1`, modelID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("pgstore: count devices on model %s: %w", modelID, err)
	}
	return n, nil
}

// modelRow is the narrow interface both QueryRow and Rows satisfy, so one scanner
// serves the single-row and list paths.
type modelRow interface {
	Scan(dest ...any) error
}

func scanModel(row modelRow) (*engine.DeviceModel, error) {
	var m engine.DeviceModel
	err := row.Scan(&m.ModelID, &m.Name, &m.Description, &m.CodecKind, &m.CodecScript,
		&m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, engine.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: scan model: %w", err)
	}
	return &m, nil
}
