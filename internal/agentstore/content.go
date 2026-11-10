package agentstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
)

type File struct {
	Kind      string    `json:"kind"`
	Owner     string    `json:"owner"`
	Path      string    `json:"path"`
	SHA       string    `json:"sha256"`
	Size      int       `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	Body      []byte    `json:"-"`
}

// ORDER BY path is required: the run bundle archive is written in this order and must be
// byte-identical across runs.
func (s *Store) ListOwner(ctx context.Context, tenant, kind, owner string) ([]File, error) {
	if err := ValidateKind(kind); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT f.kind, f.owner, f.path, f.blob_sha, b.size, f.updated_at, f.updated_by
		   FROM content_files f JOIN content_blobs b ON b.sha256 = f.blob_sha
		  WHERE f.tenant_id = ? AND f.kind = ? AND f.owner = ?
		  ORDER BY f.path`), tenant, kind, owner)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_content", "kind", kind, "owner", owner)
	}
	defer func() { _ = rows.Close() }()

	var out []File
	for rows.Next() {
		var f File
		if scanErr := rows.Scan(&f.Kind, &f.Owner, &f.Path, &f.SHA, &f.Size, &f.UpdatedAt, &f.UpdatedBy); scanErr != nil {
			return nil, apperr.Wrap(scanErr, "agentstore.errors.scan_content_file")
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.list_content", "kind", kind, "owner", owner)
	}
	return out, nil
}

func (s *Store) ReadOwner(ctx context.Context, tenant, kind, owner string) ([]File, error) {
	if err := ValidateKind(kind); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT f.kind, f.owner, f.path, f.blob_sha, b.size, f.updated_at, f.updated_by, b.body
		   FROM content_files f JOIN content_blobs b ON b.sha256 = f.blob_sha
		  WHERE f.tenant_id = ? AND f.kind = ? AND f.owner = ?
		  ORDER BY f.path`), tenant, kind, owner)
	if err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.read_content", "kind", kind, "owner", owner)
	}
	defer func() { _ = rows.Close() }()

	var out []File
	for rows.Next() {
		var f File
		if scanErr := rows.Scan(&f.Kind, &f.Owner, &f.Path, &f.SHA, &f.Size,
			&f.UpdatedAt, &f.UpdatedBy, &f.Body); scanErr != nil {
			return nil, apperr.Wrap(scanErr, "agentstore.errors.scan_content_file")
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, "agentstore.errors.read_content", "kind", kind, "owner", owner)
	}
	return out, nil
}

func (s *Store) ReadFile(ctx context.Context, tenant, kind, owner, path string) (File, error) {
	if err := ValidateKind(kind); err != nil {
		return File{}, err
	}
	if err := ValidatePath(path); err != nil {
		return File{}, err
	}
	var f File
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT f.kind, f.owner, f.path, f.blob_sha, b.size, f.updated_at, f.updated_by, b.body
		   FROM content_files f JOIN content_blobs b ON b.sha256 = f.blob_sha
		  WHERE f.tenant_id = ? AND f.kind = ? AND f.owner = ? AND f.path = ?`),
		tenant, kind, owner, path).
		Scan(&f.Kind, &f.Owner, &f.Path, &f.SHA, &f.Size, &f.UpdatedAt, &f.UpdatedBy, &f.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, apperr.Wrap(err, "agentstore.errors.read_file", "kind", kind, "owner", owner, "path", path)
	}
	return f, nil
}

// The secret scan lives in the store, not the handlers, so every write path (API, importer,
// restore) gets it.
func (s *Store) PutFile(ctx context.Context, tenant, kind, owner, path string, body []byte, actor string) (File, error) {
	return s.putFile(ctx, tenant, kind, owner, path, body, actor, "")
}

func (s *Store) putFile(ctx context.Context, tenant, kind, owner, path string,
	body []byte, actor, reason string,
) (File, error) {
	if err := validateFile(kind, owner, path, body); err != nil {
		return File{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return File{}, apperr.Wrap(err, "agentstore.errors.begin_tx")
	}
	defer func() { _ = tx.Rollback() }()
	f, err := s.putFileTx(ctx, tx, tenant, kind, owner, path, body, actor, reason)
	if err != nil {
		return File{}, err
	}
	if err := tx.Commit(); err != nil {
		return File{}, apperr.Wrap(err, "agentstore.errors.commit_tx")
	}
	return f, nil
}

func validateFile(kind, owner, path string, body []byte) error {
	if err := ValidateKind(kind); err != nil {
		return err
	}
	if err := ValidateName(owner); err != nil {
		return err
	}
	if err := ValidatePath(path); err != nil {
		return err
	}
	if len(body) > maxBodyBytes {
		return ErrBodyTooLarge
	}
	return ScanForSecrets(body)
}

func (s *Store) putFileTx(ctx context.Context, tx *sql.Tx, tenant, kind, owner, path string,
	body []byte, actor, reason string,
) (File, error) {
	// Read inside the transaction so the revision cannot record a predecessor a concurrent write
	// replaced.
	var prev sql.NullString
	prevErr := tx.QueryRowContext(ctx, s.rb(
		`SELECT blob_sha FROM content_files
		  WHERE tenant_id = ? AND kind = ? AND owner = ? AND path = ?`),
		tenant, kind, owner, path).Scan(&prev)
	if prevErr != nil && !errors.Is(prevErr, sql.ErrNoRows) {
		return File{}, apperr.Wrap(prevErr, "agentstore.errors.read_current", "kind", kind, "owner", owner, "path", path)
	}

	sha, err := s.putBlob(ctx, tx, body)
	if err != nil {
		return File{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO content_files (tenant_id, kind, owner, path, blob_sha, updated_at, updated_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (tenant_id, kind, owner, path)
		 DO UPDATE SET blob_sha = EXCLUDED.blob_sha,
		               updated_at = EXCLUDED.updated_at,
		               updated_by = EXCLUDED.updated_by`),
		tenant, kind, owner, path, sha, now, actor); err != nil {
		return File{}, apperr.Wrap(err, "agentstore.errors.put_file", "kind", kind, "owner", owner, "path", path)
	}

	// Recorded in the same transaction as the change: a history with holes reads as complete.
	// Rewriting identical bytes records nothing.
	if !prev.Valid || prev.String != sha {
		action := ActionUpdated
		if !prev.Valid {
			action = ActionCreated
		}
		if reason == ActionRestored {
			action = ActionRestored
		}
		if err := s.recordRevision(ctx, tx, revision{
			Tenant: tenant, Kind: kind, Owner: owner, Path: path,
			BlobSHA: sql.NullString{String: sha, Valid: true},
			PrevSHA: prev, Action: action, Actor: actor, At: now,
		}); err != nil {
			return File{}, err
		}
	}
	return File{Kind: kind, Owner: owner, Path: path, SHA: sha,
		Size: len(body), UpdatedAt: now, UpdatedBy: actor, Body: body}, nil
}

// The blob stays: other tenants and revisions may still reference it; GCBlobs collects it.
func (s *Store) DeleteFile(ctx context.Context, tenant, kind, owner, path, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.begin_tx")
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.deleteFileTx(ctx, tx, tenant, kind, owner, path, actor); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap(err, "agentstore.errors.commit_tx")
	}
	return nil
}

func (s *Store) deleteFileTx(ctx context.Context, tx *sql.Tx, tenant, kind, owner, path, actor string) error {
	var prev sql.NullString
	err := tx.QueryRowContext(ctx, s.rb(
		`SELECT blob_sha FROM content_files
		  WHERE tenant_id = ? AND kind = ? AND owner = ? AND path = ?`),
		tenant, kind, owner, path).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return apperr.Wrap(err, "agentstore.errors.read_current", "kind", kind, "owner", owner, "path", path)
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`DELETE FROM content_files
		  WHERE tenant_id = ? AND kind = ? AND owner = ? AND path = ?`),
		tenant, kind, owner, path); err != nil {
		return apperr.Wrap(err, "agentstore.errors.delete_file", "kind", kind, "owner", owner, "path", path)
	}
	return s.recordRevision(ctx, tx, revision{
		Tenant: tenant, Kind: kind, Owner: owner, Path: path,
		PrevSHA: prev, Action: ActionDeleted, Actor: actor, At: time.Now().UTC(),
	})
}

func (s *Store) DeleteOwner(ctx context.Context, tenant, kind, owner string) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM content_files WHERE tenant_id = ? AND kind = ? AND owner = ?`),
		tenant, kind, owner)
	if err != nil {
		return 0, apperr.Wrap(err, "agentstore.errors.delete_owner", "kind", kind, "owner", owner)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) ListOwners(ctx context.Context, tenant, kind string) ([]string, error) {
	if err := ValidateKind(kind); err != nil {
		return nil, err
	}
	return dbpkg.CollectStrings(ctx, s.db,
		`SELECT DISTINCT owner FROM content_files
		  WHERE tenant_id = ? AND kind = ? ORDER BY owner`, tenant, kind)
}

// The bundle path uses this to tell whether the store is populated, which lets a daemon roll out
// before the import has run.
func (s *Store) CountFiles(ctx context.Context, tenant string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT count(*) FROM content_files WHERE tenant_id = ?`), tenant).Scan(&n)
	if err != nil {
		return 0, apperr.Wrap(err, "agentstore.errors.count_content")
	}
	return n, nil
}

// A blob goes only when no file and no revision references it: it may be the only copy a restore
// needs.
func (s *Store) GCBlobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM content_blobs b
		  WHERE NOT EXISTS (SELECT 1 FROM content_files f WHERE f.blob_sha = b.sha256)
		    AND NOT EXISTS (SELECT 1 FROM content_revisions r
		                     WHERE r.blob_sha = b.sha256 OR r.prev_blob_sha = b.sha256)`)
	if err != nil {
		return 0, apperr.Wrap(err, "agentstore.errors.gc_blobs")
	}
	n, _ := res.RowsAffected()
	return n, nil
}
