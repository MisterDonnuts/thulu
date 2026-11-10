package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const (
	KindVersionChanged EventKind = "system.version_changed"
	KindUpdateReported EventKind = "system.update_reported"
	KindUpdateChecked  EventKind = "system.update_checked"
	KindUpdatePackage  EventKind = "system.update_package"
)

// Serializes the version check across replicas booting together (one event per rolling update).
// Must differ from auditChainLockKey, which Log takes inside its own transaction.
const versionLockKey int64 = 0x6f63746f76657273

type VersionChange struct {
	From  string    `json:"from"`
	To    string    `json:"to"`
	Image string    `json:"image,omitempty"`
	Host  string    `json:"host,omitempty"`
	At    time.Time `json:"at"`
}

func (s *Store) RecordVersion(ctx context.Context, version, image, host string) (*VersionChange, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, apperr.Invalid("audit.errors.record_version_empty")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, apperr.Wrap(err, "audit.errors.record_version")
	}
	defer func() { logx.Warn(conn.Close(), "audit.log.record_version_release_conn") }()
	if _, lerr := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, versionLockKey); lerr != nil {
		return nil, apperr.Wrap(lerr, "audit.errors.record_version_lock")
	}
	defer func() {
		// Fresh context: the caller's may be done, and a lock left held on a pooled connection
		// blocks the next replica.
		_, uerr := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, versionLockKey)
		logx.Warn(uerr, "audit.log.record_version_unlock")
	}()

	last, err := lastRecordedVersion(ctx, conn)
	if err != nil {
		return nil, err
	}
	if last == version {
		return nil, nil
	}
	change := &VersionChange{From: last, To: version, Image: image, Host: host, At: time.Now().UTC().Truncate(time.Second)}
	summary := i18n.Text("audit.comments.version_first_recorded", "version", version)
	if last != "" {
		summary = i18n.Text("audit.comments.version_changed", "from", last, "to", version)
	}
	meta := map[string]string{"from": last, "to": version}
	if image != "" {
		meta["image"] = image
	}
	if host != "" {
		meta["host"] = host
	}
	s.Log(Entry{
		Kind:         KindVersionChanged,
		ActorID:      "system",
		ActorType:    auth.ActorSystem,
		ActorLabel:   "daemon",
		ResourceType: "install",
		ResourceID:   "octopus",
		Summary:      summary,
		Metadata:     meta,
		CreatedAt:    change.At,
	})
	return change, nil
}

func lastRecordedVersion(ctx context.Context, conn *sql.Conn) (string, error) {
	var raw string
	err := conn.QueryRowContext(ctx,
		`SELECT metadata FROM audit_log WHERE kind = $1 ORDER BY seq DESC LIMIT 1`,
		string(KindVersionChanged)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", apperr.Wrap(err, "audit.errors.record_version_read_last")
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return "", apperr.Wrap(err, "audit.errors.record_version_parse_last")
	}
	return meta["to"], nil
}

func (s *Store) UpdateHistory(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, actor_id, actor_type, actor_label, actor_email, summary, metadata, created_at
		  FROM audit_log
		 WHERE kind IN ($1, $2)
		 ORDER BY seq DESC
		 LIMIT $3`,
		string(KindVersionChanged), string(KindUpdateReported), limit)
	if err != nil {
		return nil, apperr.Wrap(err, "audit.errors.update_history")
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var (
			e    Entry
			meta string
			kind string
		)
		if err := rows.Scan(&e.ID, &kind, &e.ActorID, &e.ActorType, &e.ActorLabel, &e.ActorEmail,
			&e.Summary, &meta, &e.CreatedAt); err != nil {
			return nil, apperr.Wrap(err, "audit.errors.update_history")
		}
		e.Kind = EventKind(kind)
		if meta != "" && meta != "{}" {
			logx.Warn(json.Unmarshal([]byte(meta), &e.Metadata), "audit.log.update_history_unmarshal", "id", e.ID)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
