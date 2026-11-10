package webhook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"

	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

type Store struct {
	db *sql.DB
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

// Never change: the name keys the migration history, so another one would rerun every migration.
const MigrationComponent = "webhook"

// A published migration never changes; internal/schema locks and lints this list.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `
			CREATE TABLE IF NOT EXISTS webhook_endpoints (
				id           TEXT PRIMARY KEY,
				workspace_id TEXT NOT NULL DEFAULT '',
				url          TEXT NOT NULL,
				secret_hash  TEXT NOT NULL DEFAULT '',
				events       TEXT NOT NULL DEFAULT '["*"]',
				active       INTEGER NOT NULL DEFAULT 1,
				created_at   TIMESTAMP NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_webhooks_workspace ON webhook_endpoints(workspace_id);

			CREATE TABLE IF NOT EXISTS webhook_deliveries (
				id          TEXT PRIMARY KEY,
				endpoint_id TEXT NOT NULL,
				event       TEXT NOT NULL,
				payload     TEXT NOT NULL,
				status_code INTEGER,
				attempt     INTEGER NOT NULL DEFAULT 1,
				duration_ms INTEGER,
				error       TEXT NOT NULL DEFAULT '',
				delivered   INTEGER NOT NULL DEFAULT 0,
				created_at  TIMESTAMP NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_deliveries_endpoint ON webhook_deliveries(endpoint_id, created_at);
			CREATE INDEX IF NOT EXISTS idx_deliveries_pending  ON webhook_deliveries(delivered, created_at);`},
		{ID: 2, SQL: `
			CREATE TABLE IF NOT EXISTS webhook_outbox (
				id              TEXT PRIMARY KEY,
				endpoint_id     TEXT NOT NULL,
				event           TEXT NOT NULL,
				payload         TEXT NOT NULL,
				status          TEXT NOT NULL DEFAULT 'pending',  -- pending|delivered|failed
				attempts        INTEGER NOT NULL DEFAULT 0,
				next_attempt_at TIMESTAMP NOT NULL,
				created_at      TIMESTAMP NOT NULL,
				updated_at      TIMESTAMP NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_outbox_due ON webhook_outbox(status, next_attempt_at);`},
		{ID: 3, SQL: `
			ALTER TABLE webhook_endpoints ADD COLUMN IF NOT EXISTS secret_encrypted TEXT NOT NULL DEFAULT '';`},
		{ID: 4, SQL: `
			ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS outbox_id TEXT NOT NULL DEFAULT '';`},
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "webhook.errors.schema")
	}
	return &Store{db: db}, nil
}

func (s *Store) DB() *sql.DB { return s.db }

// Stored sealed, not hashed: signing needs the secret itself.
func (s *Store) CreateEndpoint(ctx context.Context, workspaceID, endpointURL string, events []string) (*Endpoint, error) {
	secret := generateSecret()
	sealed, err := secretbox.Encrypt(secretbox.LabelWebhookSecret, secret)
	if err != nil {
		return nil, apperr.Wrap(err, "webhook.errors.seal_secret")
	}
	eventsJSON, err := json.Marshal(events)
	if err != nil {
		return nil, apperr.Wrap(err, "webhook.errors.marshal_events")
	}

	ep := &Endpoint{
		ID:              newID(),
		WorkspaceID:     workspaceID,
		URL:             endpointURL,
		Secret:          secret,
		SecretEncrypted: sealed,
		Events:          events,
		Active:          true,
		CreatedAt:       time.Now().UTC(),
	}

	_, err = s.db.ExecContext(ctx, s.rb(`
		INSERT INTO webhook_endpoints (id, workspace_id, url, secret_hash, secret_encrypted, events, active, created_at)
		VALUES (?,?,?,'',?,?,?,?)`),
		ep.ID, ep.WorkspaceID, ep.URL, ep.SecretEncrypted, string(eventsJSON), 1, ep.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return ep, nil
}

func (s *Store) RotateSecret(ctx context.Context, id, workspaceID string) (*Endpoint, error) {
	secret := generateSecret()
	sealed, err := secretbox.Encrypt(secretbox.LabelWebhookSecret, secret)
	if err != nil {
		return nil, apperr.Wrap(err, "webhook.errors.seal_secret")
	}
	res, err := s.db.ExecContext(ctx, s.rb(`
		UPDATE webhook_endpoints SET secret_encrypted = ?, secret_hash = ''
		WHERE id = ? AND workspace_id = ?`), sealed, id, workspaceID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	ep, err := s.GetEndpoint(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	ep.Secret = secret
	return ep, nil
}

func (s *Store) ListEndpoints(ctx context.Context, workspaceID string) ([]Endpoint, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT id, workspace_id, url, events, active, created_at,
		       (secret_encrypted = '' AND secret_hash <> '')
		FROM webhook_endpoints WHERE workspace_id = ?
		ORDER BY created_at DESC`), workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Endpoint
	for rows.Next() {
		var ep Endpoint
		var eventsJSON string
		var active int
		if err := rows.Scan(&ep.ID, &ep.WorkspaceID, &ep.URL, &eventsJSON, &active, &ep.CreatedAt, &ep.LegacySigning); err != nil {
			return nil, err
		}
		logx.Warn(json.Unmarshal([]byte(eventsJSON), &ep.Events), "webhook.log.unmarshal_events", "endpoint", ep.ID)
		ep.Active = active == 1
		out = append(out, ep)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetEndpoint(ctx context.Context, id, workspaceID string) (*Endpoint, error) {
	var ep Endpoint
	var eventsJSON string
	var active int
	err := s.db.QueryRowContext(ctx, s.rb(`
		SELECT id, workspace_id, url, secret_hash, secret_encrypted, events, active, created_at
		FROM webhook_endpoints WHERE id = ? AND workspace_id = ?`),
		id, workspaceID,
	).Scan(&ep.ID, &ep.WorkspaceID, &ep.URL, &ep.SecretHash, &ep.SecretEncrypted, &eventsJSON, &active, &ep.CreatedAt)
	if err != nil {
		return nil, err
	}
	ep.LegacySigning = ep.SecretEncrypted == "" && ep.SecretHash != ""
	if err := json.Unmarshal([]byte(eventsJSON), &ep.Events); err != nil {
		return nil, apperr.Wrap(err, "webhook.errors.unmarshal_events")
	}
	ep.Active = active == 1
	return &ep, nil
}

func (s *Store) DeleteEndpoint(ctx context.Context, id, workspaceID string) error {
	return s.deleteEndpointsWhere(ctx, `id = ? AND workspace_id = ?`, id, workspaceID)
}

// The delivery log holds every payload the workspace emitted, so erasure must delete it too.
func (s *Store) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	return s.deleteEndpointsWhere(ctx, `workspace_id = ?`, workspaceID)
}

// where is a fixed clause from this file, never caller input: it is concatenated into SQL.
func (s *Store) deleteEndpointsWhere(ctx context.Context, where string, args ...any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, child := range []string{"webhook_deliveries", "webhook_outbox"} {
		if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM `+child+
			` WHERE endpoint_id IN (SELECT id FROM webhook_endpoints WHERE `+where+`)`), args...); err != nil {
			return apperr.Wrap(err, "webhook.errors.delete_rows", "table", child)
		}
	}
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM webhook_endpoints WHERE `+where), args...); err != nil {
		return err
	}
	return tx.Commit()
}

const (
	statusPending   = "pending"
	statusDelivered = "delivered"
	statusFailed    = "failed"
)

// Must exceed the dispatcher's 10s HTTP timeout, or a slow delivery is re-claimed and sent twice.
const claimLease = 60 * time.Second

type outboxRow struct {
	ID         string
	EndpointID string
	Event      string
	Payload    Payload
	Body       []byte // Signed verbatim; never re-marshal Payload in its place.
	Attempts   int
}

func (s *Store) enqueueOutbox(ctx context.Context, endpointID string, p Payload, body []byte) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, s.rb(`
		INSERT INTO webhook_outbox
		  (id, endpoint_id, event, payload, status, attempts, next_attempt_at, created_at, updated_at)
		VALUES (?,?,?,?,?,0,?,?,?)`),
		newID(), endpointID, string(p.Event), string(body), statusPending, now, now, now,
	)
	return err
}

type outboxCandidate struct {
	id, endpointID, event, payload string
	attempts                       int
}

func (s *Store) selectDueCandidates(ctx context.Context, now time.Time, limit int) ([]outboxCandidate, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT id, endpoint_id, event, payload, attempts
		FROM webhook_outbox
		WHERE status = ? AND next_attempt_at <= ?
		ORDER BY next_attempt_at
		LIMIT ?`), statusPending, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cands []outboxCandidate
	for rows.Next() {
		var c outboxCandidate
		if err := rows.Scan(&c.id, &c.endpointID, &c.event, &c.payload, &c.attempts); err != nil {
			return nil, err
		}
		cands = append(cands, c)
	}
	return cands, rows.Err()
}

func (s *Store) claimDueDeliveries(ctx context.Context, limit int) ([]outboxRow, error) {
	now := time.Now().UTC()
	cands, err := s.selectDueCandidates(ctx, now, limit)
	if err != nil {
		return nil, err
	}

	leaseUntil := now.Add(claimLease)
	out := make([]outboxRow, 0, len(cands))
	for _, c := range cands {
		// The status/next_attempt_at guard makes the claim atomic against another worker that
		// leased the row first.
		res, err := s.db.ExecContext(ctx, s.rb(`
			UPDATE webhook_outbox SET next_attempt_at = ?, updated_at = ?
			WHERE id = ? AND status = ? AND next_attempt_at <= ?`),
			leaseUntil, now, c.id, statusPending, now)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		var p Payload
		logx.Warn(json.Unmarshal([]byte(c.payload), &p), "webhook.log.unmarshal_outbox_payload", "id", c.id)
		out = append(out, outboxRow{
			ID: c.id, EndpointID: c.endpointID, Event: c.event,
			Payload: p, Body: []byte(c.payload), Attempts: c.attempts,
		})
	}
	return out, nil
}

func (s *Store) finalizeOutbox(ctx context.Context, id, status string, attempts int) error {
	_, err := s.db.ExecContext(ctx,
		s.rb(`UPDATE webhook_outbox SET status = ?, attempts = ?, updated_at = ? WHERE id = ?`),
		status, attempts, time.Now().UTC(), id)
	return err
}

func (s *Store) rescheduleOutbox(ctx context.Context, id string, attempts int, nextAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		s.rb(`UPDATE webhook_outbox SET status = ?, attempts = ?, next_attempt_at = ?, updated_at = ? WHERE id = ?`),
		statusPending, attempts, nextAt.UTC(), time.Now().UTC(), id)
	return err
}

func (s *Store) getEndpointByID(ctx context.Context, id string) (*Endpoint, error) {
	var ep Endpoint
	var active int
	err := s.db.QueryRowContext(ctx,
		s.rb(`SELECT id, workspace_id, url, secret_hash, secret_encrypted, active FROM webhook_endpoints WHERE id = ?`), id,
	).Scan(&ep.ID, &ep.WorkspaceID, &ep.URL, &ep.SecretHash, &ep.SecretEncrypted, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ep.Active = active == 1
	return &ep, nil
}

func (s *Store) recordDelivery(
	ctx context.Context, endpointID, outboxID string, p Payload,
	statusCode, attempt int, durationMs int64, errMsg string, delivered bool,
) {
	body, err := json.Marshal(p)
	logx.Warn(err, "webhook.log.marshal_delivery_log", "endpoint", endpointID)
	deliveredInt := 0
	if delivered {
		deliveredInt = 1
	}
	_, err = s.db.ExecContext(ctx, s.rb(`
		INSERT INTO webhook_deliveries
		  (id, endpoint_id, event, payload, status_code, attempt, duration_ms, error, delivered, created_at, outbox_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`),
		newID(), endpointID, string(p.Event), string(body),
		statusCode, attempt, durationMs, errMsg, deliveredInt, time.Now().UTC(), outboxID,
	)
	if err != nil {
		log.Printf("[webhook] %s", i18n.Log("webhook.log.record_delivery_failed", "err", err))
	}
}

type Delivery struct {
	ID         string    `json:"id"`
	Event      string    `json:"event"`
	StatusCode int       `json:"status_code"`
	Attempt    int       `json:"attempt"`
	DurationMs int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
	Delivered  bool      `json:"delivered"`
	CreatedAt  time.Time `json:"created_at"`
	State      string    `json:"state"`
	Retryable  bool      `json:"retryable"`

	outboxID     string
	outboxStatus string
}

const (
	DeliveryDelivered = "delivered"
	DeliveryRetrying  = "retrying"
	DeliveryRetried   = "retried"
	DeliveryFailed    = "failed"
)

// Relies on ListDeliveries' newest-first ORDER BY: the first row seen of each series is its latest
// attempt.
func placeDeliveries(list []Delivery) {
	seen := map[string]bool{}
	for i := range list {
		d := &list[i]
		latest := d.outboxID == "" || !seen[d.outboxID]
		if d.outboxID != "" {
			seen[d.outboxID] = true
		}
		switch {
		case d.Delivered:
			d.State = DeliveryDelivered
		case !latest:
			d.State = DeliveryRetried
		case d.outboxStatus == statusPending:
			d.State = DeliveryRetrying
		default:
			d.State = DeliveryFailed
			d.Retryable = true
		}
	}
}

var ErrNotRetryable = errors.New("webhook: delivery is not retryable") //i18n:ignore sentinel, the API answers with its own key

const MaxDeliveriesListed = 200

func (s *Store) ListDeliveries(ctx context.Context, endpointID, workspaceID string, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > MaxDeliveriesListed {
		limit = MaxDeliveriesListed
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT d.id, d.event, COALESCE(d.status_code, 0), d.attempt, COALESCE(d.duration_ms, 0),
		       d.error, d.delivered, d.created_at, d.outbox_id, COALESCE(o.status, '')
		FROM webhook_deliveries d
		JOIN webhook_endpoints e ON e.id = d.endpoint_id
		LEFT JOIN webhook_outbox o ON o.id = d.outbox_id AND d.outbox_id <> ''
		WHERE d.endpoint_id = ? AND e.workspace_id = ?
		ORDER BY d.created_at DESC, d.attempt DESC
		LIMIT ?`), endpointID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var delivered int
		if err := rows.Scan(&d.ID, &d.Event, &d.StatusCode, &d.Attempt, &d.DurationMs,
			&d.Error, &delivered, &d.CreatedAt, &d.outboxID, &d.outboxStatus); err != nil {
			return nil, err
		}
		d.Delivered = delivered == 1
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	placeDeliveries(out)
	return out, nil
}

// The payload keeps its id, so receivers deduplicating on X-Octopus-Delivery see the same event.
func (s *Store) requeueDelivery(ctx context.Context, deliveryID, endpointID, workspaceID string) error {
	var event, payload, outboxID string
	err := s.db.QueryRowContext(ctx, s.rb(`
		SELECT d.event, d.payload, d.outbox_id
		FROM webhook_deliveries d
		JOIN webhook_endpoints e ON e.id = d.endpoint_id
		WHERE d.id = ? AND d.endpoint_id = ? AND e.workspace_id = ?`),
		deliveryID, endpointID, workspaceID).Scan(&event, &payload, &outboxID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if outboxID != "" {
		var res sql.Result
		res, err = s.db.ExecContext(ctx, s.rb(`
			UPDATE webhook_outbox SET status = ?, attempts = 0, next_attempt_at = ?, updated_at = ?
			WHERE id = ? AND status = ?`),
			statusPending, now, now, outboxID, statusFailed)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil
		}
		var status string
		err = s.db.QueryRowContext(ctx, s.rb(`SELECT status FROM webhook_outbox WHERE id = ?`), outboxID).Scan(&status)
		if err == nil {
			return ErrNotRetryable
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if outboxID == "" {
		return s.startSeries(ctx, deliveryID, endpointID, event, payload, now)
	}
	res, err := s.db.ExecContext(ctx, s.rb(`
		INSERT INTO webhook_outbox
		  (id, endpoint_id, event, payload, status, attempts, next_attempt_at, created_at, updated_at)
		VALUES (?,?,?,?,?,0,?,?,?)
		ON CONFLICT (id) DO NOTHING`),
		outboxID, endpointID, event, payload, statusPending, now, now, now,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotRetryable
	}
	return nil
}

// The link is set only where outbox_id is empty, so two racing retries queue one series, not two.
func (s *Store) startSeries(ctx context.Context, deliveryID, endpointID, event, payload string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	id := newID()
	res, err := tx.ExecContext(ctx, s.rb(`
		UPDATE webhook_deliveries SET outbox_id = ? WHERE id = ? AND outbox_id = ''`), id, deliveryID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotRetryable
	}
	if _, err := tx.ExecContext(ctx, s.rb(`
		INSERT INTO webhook_outbox
		  (id, endpoint_id, event, payload, status, attempts, next_attempt_at, created_at, updated_at)
		VALUES (?,?,?,?,?,0,?,?,?)`),
		id, endpointID, event, payload, statusPending, now, now, now,
	); err != nil {
		return err
	}
	return tx.Commit()
}
