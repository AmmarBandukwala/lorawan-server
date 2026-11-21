package pgstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fieldnode/lorawan-server/ns/internal/integration"
)

// Integration destinations: where decoded events get pushed.
//
// The read path deliberately never selects the secret column. It reports
// whether one is set, which is all a panel needs to show "token configured", and
// it means a future handler cannot serialise a credential by accident — the
// failure mode this is guarding against is somebody adding `SELECT *` and
// shipping the token to a browser.
//
// The write path has the opposite problem: an operator editing a destination's
// name must not have to retype a token they cannot see, because the form cannot
// show it. replaceSecret says which of the two the caller means, rather than
// treating an empty string as "no secret", which would silently disarm a
// destination on every unrelated edit.

// integrationColumns is the read projection. Note the absence of `secret`, and
// that `SELECT *` is never used here so the omission cannot regress.
const integrationColumns = `id, kind, name, enabled, config, (secret <> '') AS has_secret`

// Integration is a stored destination plus the one derived fact the read path
// exposes about the credential it carries.
type Integration struct {
	integration.Destination
	// HasSecret reports whether a credential is stored. The value itself is
	// never returned; Secret stays empty on every read.
	HasSecret bool
}

// ListIntegrations returns every destination, ordered by id so the panel is
// stable across refreshes.
func (s *Store) ListIntegrations(ctx context.Context) ([]*Integration, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+integrationColumns+` FROM ns_integrations ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list integrations: %w", err)
	}
	defer rows.Close()

	var out []*Integration
	for rows.Next() {
		d, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// IntegrationByID returns one destination. It returns pgx.ErrNoRows when absent,
// which the API maps to 404.
func (s *Store) IntegrationByID(ctx context.Context, id string) (*Integration, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+integrationColumns+` FROM ns_integrations WHERE id = $1`, id)
	return scanIntegration(row)
}

// integrator is the subset of pgx needed by scanIntegration, so both a row and
// a query result can be scanned.
type integrator interface {
	Scan(dest ...any) error
}

func scanIntegration(row integrator) (*Integration, error) {
	var (
		id, kind, name string
		enabled        bool
		raw            []byte
		hasSecret      bool
	)
	if err := row.Scan(&id, &kind, &name, &enabled, &raw, &hasSecret); err != nil {
		return nil, err
	}

	d := integration.Destination{ID: id, Kind: kind, Name: name, Enabled: enabled}
	if err := unmarshalDestinationConfig(kind, raw, &d); err != nil {
		return nil, err
	}
	return &Integration{Destination: d, HasSecret: hasSecret}, nil
}

// unmarshalDestinationConfig decodes the JSONB blob into the typed fields for
// the row's kind.
//
// Unknown keys are rejected on purpose. A config that carries "qoss" instead of
// "qos" would otherwise decode cleanly with the default, and the operator would
// see a destination that looks configured and quietly does something else.
func unmarshalDestinationConfig(kind string, raw []byte, d *integration.Destination) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	switch kind {
	case integration.KindWebhook:
		var w integration.WebhookDestination
		if err := dec.Decode(&w); err != nil {
			return fmt.Errorf("pgstore: webhook config for %s: %w", d.ID, err)
		}
		d.Webhook = w
	case integration.KindMQTT:
		var m integration.MQTTDestination
		if err := dec.Decode(&m); err != nil {
			return fmt.Errorf("pgstore: MQTT config for %s: %w", d.ID, err)
		}
		d.MQTT = m
	default:
		return fmt.Errorf("pgstore: integration %s has unknown kind %q", d.ID, kind)
	}
	return nil
}

// SaveIntegration inserts or updates by id.
//
// replaceSecret controls what happens to a stored credential:
//
//	true  -- the destination's Secret value is written, including the empty
//	         string, so this is how an operator clears a token
//	false -- whatever is stored stays stored, which is what an edit of the name,
//	         the topic or the enabled flag means
//
// Getting this backwards on the false branch would disarm a webhook the first
// time somebody renamed it, and the symptom would be a downstream system quietly
// rejecting events with 401 long after the edit that caused it.
func (s *Store) SaveIntegration(ctx context.Context, d *integration.Destination, replaceSecret bool) error {
	if d == nil || d.ID == "" {
		return errors.New("pgstore: integration has no id")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	raw, err := marshalDestinationConfig(d)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO ns_integrations (id, kind, name, enabled, config, secret)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		ON CONFLICT (id) DO UPDATE SET
			kind       = EXCLUDED.kind,
			name       = EXCLUDED.name,
			enabled    = EXCLUDED.enabled,
			config     = EXCLUDED.config,
			secret     = CASE WHEN $7 THEN EXCLUDED.secret
			                  ELSE ns_integrations.secret END,
			updated_at = now()`,
		d.ID, d.Kind, d.Name, d.Enabled, raw, d.Secret, replaceSecret)
	if err != nil {
		return fmt.Errorf("pgstore: save integration %s: %w", d.ID, err)
	}
	return nil
}

// marshalDestinationConfig writes only the kind's own fields, so a webhook row
// never accumulates MQTT keys from a struct that happens to carry both.
func marshalDestinationConfig(d *integration.Destination) ([]byte, error) {
	switch d.Kind {
	case integration.KindWebhook:
		return json.Marshal(d.Webhook)
	case integration.KindMQTT:
		return json.Marshal(d.MQTT)
	default:
		return nil, fmt.Errorf("pgstore: integration %s has unknown kind %q", d.ID, d.Kind)
	}
}

// DeleteIntegration removes a destination. It reports whether a row was
// actually removed, so the API can answer 404 rather than reporting success for
// an id that never existed.
func (s *Store) DeleteIntegration(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ns_integrations WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("pgstore: delete integration %s: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}
