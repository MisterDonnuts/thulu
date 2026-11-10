package agentstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Revision struct {
	ID      int64     `json:"id"`
	Kind    string    `json:"kind"`
	Owner   string    `json:"owner"`
	Path    string    `json:"path"`
	SHA     string    `json:"sha256,omitempty"`
	PrevSHA string    `json:"prev_sha256,omitempty"`
	Action  string    `json:"action"`
	Actor   string    `json:"actor,omitempty"`
	At      time.Time `json:"created_at"`
	Size    int       `json:"size,omitempty"`
}

type revision struct {
	Tenant, Kind, Owner, Path string
	BlobSHA, PrevSHA          sql.NullString
	Action, Actor             string
	At                        time.Time
}

func (s *Store) recordRevision(ctx context.Context, tx *sql.Tx, r revision) error {
	_, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO content_revisions
		   (tenant_id, kind, owner, path, blob_sha, prev_blob_sha, action, actor, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		r.Tenant, r.Kind, r.Owner, r.Path, r.BlobSHA, r.PrevSHA, r.Action, r.Actor, r.At)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.record_revision", "kind", r.Kind, "owner", r.Owner, "path", r.Path)
	}
	return nil
}

func (s *Store) ListRevisions(ctx context.Context, tenant, kind, owner, path string, limit int) ([]Revision, error) {
	if err := ValidateKind(kind); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT r.id, r.kind, r.owner, r.path, r.blob_sha, r.prev_blob_sha,
	             r.action, r.actor, r.created_at, COALESCE(b.size, 0)
	        FROM content_revisions r
	        LEFT JOIN content_blobs b ON b.sha256 = r.blob_sha
	       WHERE r.tenant_id = ? AND r.kind = ? AND r.owner = ?`
	args := []any{tenant, kind, owner}
	if path != "" {
		if path != SettingsPath {
			if err := ValidatePath(path); err != nil {
				return nil, err
			}
		}
		q += ` AND r.path = ?`
		args = append(args, path)
	}
	q += ` ORDER BY r.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_revisions", "kind", kind, "owner", owner)
	}
	defer func() { _ = rows.Close() }()

	var out []Revision
	for rows.Next() {
		var (
			rev            Revision
			blob, prevBlob sql.NullString
		)
		if err := rows.Scan(&rev.ID, &rev.Kind, &rev.Owner, &rev.Path, &blob, &prevBlob,
			&rev.Action, &rev.Actor, &rev.At, &rev.Size); err != nil {
			return nil, apperr.Wrap(err, "agentstore.errors.scan_revision")
		}
		rev.SHA, rev.PrevSHA = blob.String, prevBlob.String
		out = append(out, rev)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_revisions", "kind", kind, "owner", owner)
	}
	return out, nil
}

func (s *Store) ListDeleted(ctx context.Context, tenant, kind, owner string) ([]Revision, error) {
	if err := ValidateKind(kind); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT r.id, r.kind, r.owner, r.path, r.blob_sha, r.prev_blob_sha,
		        r.action, r.actor, r.created_at, COALESCE(b.size, 0)
		   FROM content_revisions r
		   LEFT JOIN content_blobs b ON b.sha256 = r.prev_blob_sha
		  WHERE r.tenant_id = ? AND r.kind = ? AND r.owner = ? AND r.action = ?
		    AND r.id = (SELECT max(x.id) FROM content_revisions x
		                 WHERE x.tenant_id = r.tenant_id AND x.kind = r.kind
		                   AND x.owner = r.owner AND x.path = r.path)
		    AND NOT EXISTS (SELECT 1 FROM content_files f
		                     WHERE f.tenant_id = r.tenant_id AND f.kind = r.kind
		                       AND f.owner = r.owner AND f.path = r.path)
		  ORDER BY r.path`), tenant, kind, owner, ActionDeleted)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_revisions", "kind", kind, "owner", owner)
	}
	defer func() { _ = rows.Close() }()

	var out []Revision
	for rows.Next() {
		var (
			rev            Revision
			blob, prevBlob sql.NullString
		)
		if err := rows.Scan(&rev.ID, &rev.Kind, &rev.Owner, &rev.Path, &blob, &prevBlob,
			&rev.Action, &rev.Actor, &rev.At, &rev.Size); err != nil {
			return nil, apperr.Wrap(err, "agentstore.errors.scan_revision")
		}
		rev.SHA, rev.PrevSHA = blob.String, prevBlob.String
		out = append(out, rev)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_revisions", "kind", kind, "owner", owner)
	}
	return out, nil
}

type DeletedOwner struct {
	Name      string    `json:"name"`
	DeletedAt time.Time `json:"deleted_at"`
}

func (s *Store) ListDeletedOwners(ctx context.Context, tenant, kind string) ([]DeletedOwner, error) {
	if err := ValidateKind(kind); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT r.owner, max(r.created_at)
		   FROM content_revisions r
		  WHERE r.tenant_id = ? AND r.kind = ?
		    AND NOT EXISTS (SELECT 1 FROM content_files f
		                     WHERE f.tenant_id = r.tenant_id AND f.kind = r.kind
		                       AND f.owner = r.owner)
		  GROUP BY r.owner
		  ORDER BY r.owner`), tenant, kind)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_deleted", "kind", kind)
	}
	defer func() { _ = rows.Close() }()

	var out []DeletedOwner
	for rows.Next() {
		var o DeletedOwner
		if err := rows.Scan(&o.Name, &o.DeletedAt); err != nil {
			return nil, apperr.Wrap(err, "agentstore.errors.list_deleted", "kind", kind)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_deleted", "kind", kind)
	}
	return out, nil
}

func (s *Store) RestoreDeleted(ctx context.Context, tenant, kind, owner, actor string) ([]File, error) {
	deleted, err := s.trailingDeletions(ctx, tenant, kind, owner)
	if err != nil {
		return nil, err
	}
	bodies := make([][]byte, len(deleted))
	for i := range deleted {
		if bodies[i], err = s.blobBody(ctx, deleted[i].PrevSHA); err != nil {
			return nil, err
		}
	}
	var out []File
	err = s.InTx(ctx, func(tx *Tx) error {
		var txErr error
		out, txErr = s.putRestored(ctx, tx, tenant, kind, owner, actor, deleted, bodies)
		return txErr
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) trailingDeletions(ctx context.Context, tenant, kind, owner string) ([]Revision, error) {
	all, err := s.ListDeleted(ctx, tenant, kind, owner)
	if err != nil {
		return nil, err
	}
	var lastKept sql.NullInt64
	err = s.db.QueryRowContext(ctx, s.rb(
		`SELECT max(id) FROM content_revisions
		  WHERE tenant_id = ? AND kind = ? AND owner = ? AND action <> ?`),
		tenant, kind, owner, ActionDeleted).Scan(&lastKept)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_revisions", "kind", kind, "owner", owner)
	}
	var deleted []Revision
	for i := range all {
		if all[i].ID > lastKept.Int64 {
			deleted = append(deleted, all[i])
		}
	}
	if len(deleted) == 0 {
		return nil, ErrNotFound
	}
	return deleted, nil
}

func (s *Store) putRestored(ctx context.Context, tx *Tx, tenant, kind, owner, actor string, deleted []Revision, bodies [][]byte) ([]File, error) {
	out := make([]File, 0, len(deleted))
	for i := range deleted {
		if err := validateFile(kind, owner, deleted[i].Path, bodies[i]); err != nil {
			return nil, err
		}
		f, err := s.putFileTx(ctx, tx.tx, tenant, kind, owner, deleted[i].Path, bodies[i], actor, ActionRestored)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func (s *Store) GetRevision(ctx context.Context, tenant string, id int64) (Revision, error) {
	var (
		rev            Revision
		blob, prevBlob sql.NullString
	)
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT r.id, r.kind, r.owner, r.path, r.blob_sha, r.prev_blob_sha,
		        r.action, r.actor, r.created_at, COALESCE(b.size, 0)
		   FROM content_revisions r
		   LEFT JOIN content_blobs b ON b.sha256 = r.blob_sha
		  WHERE r.tenant_id = ? AND r.id = ?`), tenant, id).
		Scan(&rev.ID, &rev.Kind, &rev.Owner, &rev.Path, &blob, &prevBlob,
			&rev.Action, &rev.Actor, &rev.At, &rev.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, ErrNotFound
	}
	if err != nil {
		return Revision{}, apperr.Wrap(err, "agentstore.errors.get_revision", "id", id)
	}
	rev.SHA, rev.PrevSHA = blob.String, prevBlob.String
	return rev, nil
}

func (s *Store) RevisionBody(ctx context.Context, tenant string, id int64) ([]byte, error) {
	rev, err := s.GetRevision(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if rev.SHA == "" {
		return nil, ErrNotFound
	}
	return s.blobBody(ctx, rev.SHA)
}

func (s *Store) blobBody(ctx context.Context, sha string) ([]byte, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT body FROM content_blobs WHERE sha256 = ?`), sha).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.read_blob", "sha", sha)
	}
	return body, nil
}

const (
	ModeRevert = "revert"
	ModeReset  = "reset"
)

func (s *Store) RestoreRevision(ctx context.Context, tenant string, id int64, mode, actor string) (File, error) {
	rev, err := s.GetRevision(ctx, tenant, id)
	if err != nil {
		return File{}, err
	}
	if rev.Path == SettingsPath {
		return File{}, ErrSettingsRestore
	}
	if mode == "" {
		mode = ModeRevert
	}
	var sha string
	switch mode {
	case ModeRevert:
		sha = rev.PrevSHA
		if sha == "" {
			if rev.Action == ActionCreated {
				if delErr := s.DeleteFile(ctx, tenant, rev.Kind, rev.Owner, rev.Path, actor); delErr != nil {
					return File{}, delErr
				}
				return File{Kind: rev.Kind, Owner: rev.Owner, Path: rev.Path}, nil
			}
			return File{}, apperr.Wrap(ErrNotFound, "agentstore.errors.no_earlier_version", "id", id)
		}
	case ModeReset:
		sha = rev.SHA
		if sha == "" {
			return File{}, apperr.Wrap(ErrInvalidMode, "agentstore.errors.deletion_has_no_content", "id", id)
		}
	default:
		return File{}, ErrInvalidMode
	}

	body, err := s.blobBody(ctx, sha)
	if err != nil {
		return File{}, err
	}
	return s.putFile(ctx, tenant, rev.Kind, rev.Owner, rev.Path, body, actor, ActionRestored)
}

const DefaultKeepPerFile = 50

// Run before GCBlobs: pruning is what makes the blobs these entries pinned collectable.
func (s *Store) PruneRevisions(ctx context.Context, tenant string, keepPerFile int) (int64, error) {
	if keepPerFile < 0 {
		keepPerFile = DefaultKeepPerFile
	}
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM content_revisions
		  WHERE id IN (
		    SELECT id FROM (
		      SELECT id, row_number() OVER (
		               PARTITION BY tenant_id, kind, owner, path ORDER BY id DESC
		             ) AS rn
		        FROM content_revisions
		       WHERE tenant_id = ?
		    ) ranked
		    WHERE ranked.rn > ?
		  )`), tenant, keepPerFile)
	if err != nil {
		return 0, apperr.Wrap(err, "agentstore.errors.prune_revisions")
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) Vacuum(ctx context.Context, tenant string, keepPerFile int) (prunedRevisions, freedBlobs int64, err error) {
	prunedRevisions, err = s.PruneRevisions(ctx, tenant, keepPerFile)
	if err != nil {
		return 0, 0, err
	}
	freedBlobs, err = s.GCBlobs(ctx)
	if err != nil {
		return prunedRevisions, 0, err
	}
	return prunedRevisions, freedBlobs, nil
}

// Settings history path in content_revisions. ValidatePath refuses a leading dot, so no file can
// be written over it and the file routes cannot restore it.
const SettingsPath = ".settings"

func (t *Tx) RecordSettings(tenant, agent string, before, after []byte, actor string) error {
	var last sql.NullString
	err := t.tx.QueryRowContext(t.ctx, t.s.rb(
		`SELECT blob_sha FROM content_revisions
		  WHERE tenant_id = ? AND kind = ? AND owner = ? AND path = ?
		  ORDER BY id DESC LIMIT 1`), tenant, KindAgent, agent, SettingsPath).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return apperr.Wrap(err, "agentstore.errors.read_current", "kind", KindAgent, "owner", agent, "path", SettingsPath)
	}
	now := time.Now().UTC()
	if !last.Valid && before != nil && blobSHA(before) != blobSHA(after) {
		first, putErr := t.s.putBlob(t.ctx, t.tx, before)
		if putErr != nil {
			return putErr
		}
		if recErr := t.s.recordRevision(t.ctx, t.tx, revision{
			Tenant: tenant, Kind: KindAgent, Owner: agent, Path: SettingsPath,
			BlobSHA: sql.NullString{String: first, Valid: true},
			Action:  ActionCreated, Actor: actor, At: now,
		}); recErr != nil {
			return recErr
		}
		last = sql.NullString{String: first, Valid: true}
	}
	sha, err := t.s.putBlob(t.ctx, t.tx, after)
	if err != nil {
		return err
	}
	if last.Valid && last.String == sha {
		return nil
	}
	action := ActionUpdated
	if !last.Valid {
		action = ActionCreated
	}
	return t.s.recordRevision(t.ctx, t.tx, revision{
		Tenant: tenant, Kind: KindAgent, Owner: agent, Path: SettingsPath,
		BlobSHA: sql.NullString{String: sha, Valid: true}, PrevSHA: last,
		Action: action, Actor: actor, At: now,
	})
}
