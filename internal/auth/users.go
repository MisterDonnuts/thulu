package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

type Role string

const (
	RoleOwner     Role = "owner"
	RoleAdmin     Role = "admin"
	RoleDeveloper Role = "developer"
	RoleViewer    Role = "viewer"
)

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	TokenVersion int       `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	OwnerID   string    `json:"owner_id"`
	CreatedAt time.Time `json:"created_at"`
}

type WorkspaceMember struct {
	WorkspaceID string    `json:"workspace_id"`
	UserID      string    `json:"user_id"`
	Email       string    `json:"email,omitempty"`
	Name        string    `json:"name,omitempty"`
	Role        Role      `json:"role"`
	InvitedAt   time.Time `json:"invited_at"`
	Owner       bool      `json:"owner,omitempty"`
}

type UserStore struct {
	db            *sql.DB
	tokenVersions *tokenVersionCache
}

func (s *UserStore) rb(q string) string { return dbpkg.Rebind(q) }

// Never change: the name keys the migration history, so another one would rerun every migration.
const MigrationComponent = "auth"

// internal/schema reads this for the migrations lock and lint; a published migration never changes.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return authMigrations(), nil
}

func NewUserStore(db *sql.DB) (*UserStore, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "auth.errors.schema")
	}
	s := &UserStore{db: db}
	s.tokenVersions = newTokenVersionCache(tokenVersionTTL, s.readTokenVersion)
	return s, nil
}

// Timestamp columns must be TIMESTAMPTZ: a bare TIMESTAMP drops the offset and breaks expiry
// comparisons.
func authMigrations() []migrate.Migration {
	return append([]migrate.Migration{
		{ID: 1, SQL: `
			CREATE TABLE IF NOT EXISTS api_keys (
				id           TEXT PRIMARY KEY,
				name         TEXT NOT NULL DEFAULT '',
				key_hash     TEXT NOT NULL UNIQUE,
				key_prefix   TEXT NOT NULL,
				created_by   TEXT NOT NULL,
				created_at   TIMESTAMPTZ NOT NULL,
				last_used_at TIMESTAMPTZ,
				revoked_at   TIMESTAMPTZ
			);
			CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);

			CREATE TABLE IF NOT EXISTS users (
				id            TEXT PRIMARY KEY,
				email         TEXT NOT NULL UNIQUE,
				name          TEXT NOT NULL DEFAULT '',
				password_hash TEXT NOT NULL DEFAULT '',
				created_at    TIMESTAMPTZ NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

			CREATE TABLE IF NOT EXISTS workspaces (
				id         TEXT PRIMARY KEY,
				name       TEXT NOT NULL,
				slug       TEXT NOT NULL UNIQUE,
				owner_id   TEXT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL,
				FOREIGN KEY (owner_id) REFERENCES users(id)
			);
			CREATE INDEX IF NOT EXISTS idx_workspaces_slug ON workspaces(slug);

			CREATE TABLE IF NOT EXISTS workspace_members (
				workspace_id TEXT NOT NULL,
				user_id      TEXT NOT NULL,
				role         TEXT NOT NULL DEFAULT 'developer',
				invited_at   TIMESTAMPTZ NOT NULL,
				PRIMARY KEY (workspace_id, user_id),
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
			);

			CREATE TABLE IF NOT EXISTS refresh_tokens (
				id           TEXT PRIMARY KEY,
				user_id      TEXT NOT NULL,
				workspace_id TEXT NOT NULL,
				token_hash   TEXT NOT NULL UNIQUE,
				expires_at   TIMESTAMPTZ NOT NULL,
				created_at   TIMESTAMPTZ NOT NULL,
				revoked_at   TIMESTAMPTZ,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
			);
			CREATE INDEX IF NOT EXISTS idx_refresh_tokens_hash ON refresh_tokens(token_hash);
			CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON refresh_tokens(user_id);

			CREATE TABLE IF NOT EXISTS oidc_identities (
				user_id    TEXT NOT NULL,
				provider   TEXT NOT NULL,
				subject    TEXT NOT NULL,
				email      TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL,
				PRIMARY KEY (provider, subject),
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
			);
			CREATE INDEX IF NOT EXISTS idx_oidc_user ON oidc_identities(user_id);

			CREATE TABLE IF NOT EXISTS mfa_secrets (
				user_id      TEXT PRIMARY KEY,
				secret       TEXT NOT NULL DEFAULT '',
				enabled      INTEGER NOT NULL DEFAULT 0,
				enabled_at   TIMESTAMPTZ,
				backup_codes TEXT NOT NULL DEFAULT '[]',
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
			);`},
		{ID: 2, SQL: `ALTER TABLE users ADD COLUMN IF NOT EXISTS failed_attempts INTEGER NOT NULL DEFAULT 0`},
		{ID: 3, SQL: `ALTER TABLE users ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ`},
		{ID: 4, SQL: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scope_task_id TEXT NOT NULL DEFAULT ''`},
		{ID: 5, SQL: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ`},
		{ID: 6, SQL: `ALTER TABLE users ADD COLUMN IF NOT EXISTS token_version INTEGER NOT NULL DEFAULT 0`},
		{ID: 7, SQL: `ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS family_id TEXT NOT NULL DEFAULT ''`},
		{ID: 8, SQL: `ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS replaced_by TEXT`},
		{ID: 9, SQL: `ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS family_started_at TIMESTAMPTZ`},
		{ID: 10, SQL: `CREATE INDEX IF NOT EXISTS idx_refresh_tokens_family ON refresh_tokens(family_id)`},
		{ID: 11, SQL: `ALTER TABLE mfa_secrets ADD COLUMN IF NOT EXISTS mfa_last_step INTEGER NOT NULL DEFAULT 0`},
		{ID: 12, SQL: `CREATE TABLE IF NOT EXISTS mfa_challenges (
				nonce      TEXT PRIMARY KEY,
				user_id    TEXT NOT NULL,
				expires_at TIMESTAMPTZ NOT NULL
			)`},
		{ID: 13, SQL: `ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS device_ua TEXT NOT NULL DEFAULT ''`},
		{ID: 14, SQL: `ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS device_ip TEXT NOT NULL DEFAULT ''`},
		{ID: 15, SQL: `CREATE TABLE IF NOT EXISTS webauthn_credentials (
				id           TEXT PRIMARY KEY,
				user_id      TEXT NOT NULL,
				name         TEXT NOT NULL DEFAULT '',
				credential   TEXT NOT NULL,
				created_at   TIMESTAMPTZ NOT NULL,
				last_used_at TIMESTAMPTZ,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
			);
			CREATE INDEX IF NOT EXISTS idx_webauthn_creds_user ON webauthn_credentials(user_id)`},
		{ID: 16, SQL: `CREATE TABLE IF NOT EXISTS webauthn_sessions (
				id         TEXT PRIMARY KEY,
				user_id    TEXT NOT NULL DEFAULT '',
				purpose    TEXT NOT NULL,
				data       TEXT NOT NULL,
				expires_at TIMESTAMPTZ NOT NULL
			)`},
		// Migrations 1–16 must stay as written for anyone mid-upgrade; this is the forward step
		// that drops what they created.
		{ID: 17, SQL: `DROP TABLE IF EXISTS webauthn_sessions;
			DROP TABLE IF EXISTS webauthn_credentials;
			DROP TABLE IF EXISTS mfa_challenges;
			DROP TABLE IF EXISTS mfa_secrets;
			ALTER TABLE users DROP COLUMN IF EXISTS password_hash;
			ALTER TABLE users DROP COLUMN IF EXISTS failed_attempts;
			ALTER TABLE users DROP COLUMN IF EXISTS locked_until`},
	}, laterAuthMigrations()...)
}

func laterAuthMigrations() []migrate.Migration {
	var out []migrate.Migration
	for _, part := range [][]migrate.Migration{
		apiKeyScopeMigrations(), runScopeMigrations(), signupMigrations(),
		accountMigrations(), invitationMigrations(), accessRequestMigrations(),
		rotationMigrations(), preferenceMigrations(), runKeyLLMMigrations(),
	} {
		out = append(out, part...)
	}
	return out
}

func apiKeyScopeMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 18, SQL: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT ''`},
		{ID: 19, SQL: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT ''`},
		{ID: 20, SQL: `ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS permissions TEXT NOT NULL DEFAULT ''`},
		{ID: 21, SQL: `CREATE INDEX IF NOT EXISTS idx_api_keys_workspace ON api_keys(workspace_id)`},
		{ID: 22, SQL: `
			UPDATE api_keys SET
			    workspace_id = COALESCE((
			        SELECT m.workspace_id FROM workspace_members m
			         WHERE m.user_id = api_keys.created_by
			         ORDER BY m.invited_at LIMIT 1), ''),
			    role = 'owner',
			    permissions = '*'
			 WHERE workspace_id = '' AND scope_task_id = '' AND revoked_at IS NULL`},
		{ID: 23, SQL: `
			UPDATE api_keys SET
			    role = 'developer',
			    permissions = 'tasks:read,tasks:write,flows:read,projects:read,runs:read,runs:write,agents:read,internal:use'
			 WHERE scope_task_id <> '' AND permissions = '' AND revoked_at IS NULL`},
		{ID: 24, SQL: `
			UPDATE api_keys SET revoked_at = CURRENT_TIMESTAMP
			 WHERE workspace_id = '' AND scope_task_id = '' AND revoked_at IS NULL`},
	}
}

func (s *UserStore) DB() *sql.DB  { return s.db }
func (s *UserStore) Close() error { return s.db.Close() }

func (s *UserStore) CreateUser(ctx context.Context, email, name string) (*User, error) {
	u := &User{
		ID:        genID(),
		Email:     strings.ToLower(strings.TrimSpace(email)),
		Name:      strings.TrimSpace(name),
		CreatedAt: time.Now(),
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO users (id, email, name, created_at) VALUES (?,?,?,?)`),
		u.ID, u.Email, u.Name, u.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *UserStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id, email, name, token_version, created_at FROM users WHERE email = ?`),
		strings.ToLower(strings.TrimSpace(email)),
	).Scan(&u.ID, &u.Email, &u.Name, &u.TokenVersion, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (s *UserStore) GetUserByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id, email, name, token_version, created_at FROM users WHERE id = ?`), id,
	).Scan(&u.ID, &u.Email, &u.Name, &u.TokenVersion, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (s *UserStore) GetTokenVersion(ctx context.Context, userID string) (int, error) {
	return s.tokenVersions.get(ctx, userID)
}

func (s *UserStore) readTokenVersion(ctx context.Context, userID string) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT token_version FROM users WHERE id = ?`), userID,
	).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errTokenVersionUserGone
	}
	return v, err
}

func (s *UserStore) BumpTokenVersion(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE users SET token_version = token_version + 1 WHERE id = ?`), userID)
	// Invalidate even on error: a statement that timed out after writing the row would leave the
	// old version cached.
	s.tokenVersions.invalidate(userID)
	return err
}

func (s *UserStore) CreateWorkspace(ctx context.Context, ownerID, name, slug string) (*Workspace, error) {
	ws := &Workspace{
		ID:        genID(),
		Name:      strings.TrimSpace(name),
		Slug:      strings.ToLower(strings.TrimSpace(slug)),
		OwnerID:   ownerID,
		CreatedAt: time.Now(),
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if err := s.insertWorkspace(ctx, tx, ws); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ws, nil
}

func (s *UserStore) insertWorkspace(ctx context.Context, tx *sql.Tx, ws *Workspace) error {
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO workspaces (id, name, slug, owner_id, created_at) VALUES (?,?,?,?,?)`),
		ws.ID, ws.Name, ws.Slug, ws.OwnerID, ws.CreatedAt,
	); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO workspace_members (workspace_id, user_id, role, invited_at) VALUES (?,?,?,?)`),
		ws.ID, ws.OwnerID, RoleOwner, time.Now(),
	)
	return err
}

// Not cached: a cached "only one" keeps answering after a second workspace appears.
func (s *UserStore) SoleWorkspaceID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM workspaces WHERE (SELECT COUNT(*) FROM workspaces) = 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *UserStore) ListWorkspaceIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM workspaces ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *UserStore) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	ws := &Workspace{}
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id, name, slug, owner_id, created_at FROM workspaces WHERE id = ?`), id,
	).Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.OwnerID, &ws.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ws, err
}

func (s *UserStore) GetWorkspaceBySlug(ctx context.Context, slug string) (*Workspace, error) {
	ws := &Workspace{}
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id, name, slug, owner_id, created_at FROM workspaces WHERE slug = ?`),
		strings.ToLower(strings.TrimSpace(slug)),
	).Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.OwnerID, &ws.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ws, err
}

func (s *UserStore) ListWorkspacesForUser(ctx context.Context, userID string) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT w.id, w.name, w.slug, w.owner_id, w.created_at
		FROM workspaces w
		JOIN workspace_members m ON m.workspace_id = w.id
		WHERE m.user_id = ?
		ORDER BY w.created_at ASC`), userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Workspace
	for rows.Next() {
		var ws Workspace
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.OwnerID, &ws.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *UserStore) GetMemberRole(ctx context.Context, workspaceID, userID string) (Role, error) {
	var role string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`),
		workspaceID, userID,
	).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return Role(role), err
}

// The workspace's projects, tasks and secrets are not touched here; the caller cascades those.
func (s *UserStore) DeleteWorkspace(ctx context.Context, workspaceID string) (removedMembers []string, err error) {
	removedMembers, err = s.memberIDs(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if workspaceID != "" {
		// Skip '': it would delete every unstamped key across all workspaces.
		if _, err := s.db.ExecContext(ctx, s.rb(
			`DELETE FROM api_keys WHERE workspace_id = ?`), workspaceID); err != nil {
			return nil, err
		}
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE refresh_tokens SET revoked_at = ? WHERE workspace_id = ? AND revoked_at IS NULL`),
		time.Now(), workspaceID); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM workspace_members WHERE workspace_id = ?`), workspaceID); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM workspace_invitations WHERE workspace_id = ?`), workspaceID); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM workspaces WHERE id = ?`), workspaceID); err != nil {
		return nil, err
	}
	return removedMembers, nil
}

func (s *UserStore) memberIDs(ctx context.Context, workspaceID string) ([]string, error) {
	return dbpkg.CollectStrings(ctx, s.db,
		`SELECT user_id FROM workspace_members WHERE workspace_id = ?`, workspaceID)
}

// The owner's row is never rewritten (the WHERE stops an admin demoting the owner by re-adding
// them).
// invited_at is kept on re-add: it orders GetDefaultWorkspace.
func (s *UserStore) AddMember(ctx context.Context, workspaceID, userID string, role Role) error {
	if !isKnownRole(role) {
		return apperr.Invalid("auth.errors.member_role_invalid", "role", role)
	}
	if role == RoleOwner {
		return apperr.Forbidden("auth.errors.owner_not_grantable_on_add")
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO workspace_members (workspace_id, user_id, role, invited_at) VALUES (?,?,?,?)
		 ON CONFLICT(workspace_id, user_id) DO UPDATE SET role = excluded.role
		 WHERE workspace_members.user_id <> (
		     SELECT owner_id FROM workspaces WHERE id = workspace_members.workspace_id)`),
		workspaceID, userID, role, time.Now(),
	)
	return err
}

func (s *UserStore) UpdateMemberRole(ctx context.Context, workspaceID, userID string, role Role) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE workspace_members SET role = ? WHERE workspace_id = ? AND user_id = ?`),
		role, workspaceID, userID,
	)
	return memberRowTouched(res, err)
}

func (s *UserStore) RemoveMember(ctx context.Context, workspaceID, userID string) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`),
		workspaceID, userID,
	)
	return memberRowTouched(res, err)
}

var ErrNotAMember error = apperr.NotFound("auth.errors.member_not_found")

// Handlers revoke the target's sessions after this; without the row check an admin could sign out
// any user of the install.
func memberRowTouched(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotAMember
	}
	return nil
}

func (s *UserStore) ListMembers(ctx context.Context, workspaceID string) ([]WorkspaceMember, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT wm.workspace_id, wm.user_id, COALESCE(u.email,''), COALESCE(u.name,''), wm.role, wm.invited_at,
		       COALESCE(w.owner_id = wm.user_id, FALSE)
		FROM workspace_members wm
		LEFT JOIN users u ON u.id = wm.user_id
		LEFT JOIN workspaces w ON w.id = wm.workspace_id
		WHERE wm.workspace_id = ?
		ORDER BY wm.invited_at ASC`), workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []WorkspaceMember{}
	for rows.Next() {
		var m WorkspaceMember
		if err := rows.Scan(&m.WorkspaceID, &m.UserID, &m.Email, &m.Name, &m.Role, &m.InvitedAt, &m.Owner); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *UserStore) StoreRefreshTokenFamily(ctx context.Context, userID, workspaceID, tokenHash, familyID string,
	familyStart, expiresAt time.Time, deviceUA, deviceIP string) (string, error) {
	return s.storeRefreshTokenInFamily(ctx, userID, workspaceID, tokenHash, familyID, familyStart, expiresAt, deviceUA, deviceIP)
}

func (s *UserStore) storeRefreshTokenInFamily(ctx context.Context, userID, workspaceID, tokenHash, familyID string,
	familyStart, expiresAt time.Time, deviceUA, deviceIP string) (string, error) {
	now := time.Now()
	if familyID == "" {
		familyID = genID()
	}
	if familyStart.IsZero() {
		familyStart = now
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO refresh_tokens
		   (id, user_id, workspace_id, token_hash, expires_at, created_at, family_id, family_started_at, device_ua, device_ip)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`),
		genID(), userID, workspaceID, tokenHash, expiresAt, now, familyID, familyStart, deviceUA, deviceIP,
	)
	return familyID, err
}

func (s *UserStore) RevokeLiveFamiliesForDevice(ctx context.Context, userID, deviceUA, deviceIP string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE refresh_tokens SET revoked_at = ?
		   WHERE user_id = ? AND device_ua = ? AND device_ip = ?
		     AND revoked_at IS NULL AND expires_at > ?`),
		time.Now(), userID, deviceUA, deviceIP, time.Now().UTC(),
	)
	return err
}

type RefreshTokenState int

const (
	RefreshUnknown RefreshTokenState = iota
	RefreshValid
	RefreshRevoked
	RefreshExpired
)

type RefreshTokenRecord struct {
	UserID      string
	WorkspaceID string
	FamilyID    string
	FamilyStart time.Time
	State       RefreshTokenState
}

func (s *UserStore) LookupRefreshToken(ctx context.Context, tokenHash string) (*RefreshTokenRecord, error) {
	var rec RefreshTokenRecord
	var expiresAt time.Time
	var revokedAt sql.NullTime
	var familyStart sql.NullTime
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT user_id, workspace_id, COALESCE(family_id,''), family_started_at, expires_at, revoked_at
		   FROM refresh_tokens WHERE token_hash = ?`),
		tokenHash,
	).Scan(&rec.UserID, &rec.WorkspaceID, &rec.FamilyID, &familyStart, &expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &RefreshTokenRecord{State: RefreshUnknown}, nil
	}
	if err != nil {
		return nil, err
	}
	if familyStart.Valid {
		rec.FamilyStart = familyStart.Time
	}
	switch {
	case revokedAt.Valid:
		rec.State = RefreshRevoked
	case time.Now().After(expiresAt):
		rec.State = RefreshExpired
	default:
		rec.State = RefreshValid
	}
	return &rec, nil
}

func (s *UserStore) RevokeRefreshToken(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE refresh_tokens SET revoked_at = ? WHERE token_hash = ?`),
		time.Now(), tokenHash,
	)
	return err
}

func (s *UserStore) RotateRefreshToken(ctx context.Context, oldHash, newHash, userID, workspaceID, familyID string,
	familyStart, newExpiresAt time.Time, deviceUA, deviceIP string) error {
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	newID := genID()
	// The revoked_at guard stops two concurrent rotations both succeeding and leaving two live
	// successors undetected as reuse.
	res, err := tx.ExecContext(ctx, s.rb(
		`UPDATE refresh_tokens SET revoked_at = ?, replaced_by = ? WHERE token_hash = ? AND revoked_at IS NULL`),
		now, newID, oldHash,
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrRefreshAlreadyRotated
	}
	if familyStart.IsZero() {
		familyStart = now
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO refresh_tokens
		   (id, user_id, workspace_id, token_hash, expires_at, created_at, family_id, family_started_at, device_ua, device_ip)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`),
		newID, userID, workspaceID, newHash, newExpiresAt, now, familyID, familyStart, deviceUA, deviceIP,
	); err != nil {
		return err
	}
	return tx.Commit()
}

var ErrRefreshAlreadyRotated error = apperr.Unauthenticated("auth.errors.refresh_token_invalid")

func (s *UserStore) RevokeRefreshTokenFamily(ctx context.Context, familyID string) error {
	if familyID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`),
		time.Now(), familyID,
	)
	return err
}

type SessionFamily struct {
	FamilyID    string    `json:"family_id"`
	WorkspaceID string    `json:"workspace_id"`
	StartedAt   time.Time `json:"started_at"`
	LastUsedAt  time.Time `json:"last_used_at"`
	DeviceUA    string    `json:"device_ua"`
	DeviceIP    string    `json:"device_ip"`
	IsCurrent   bool      `json:"is_current"`
}

func (s *UserStore) ListSessionFamilies(ctx context.Context, userID string) ([]SessionFamily, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`
		SELECT family_id,
		       MIN(workspace_id),
		       MIN(COALESCE(family_started_at, created_at)),
		       MAX(created_at),
		       MAX(device_ua),
		       MAX(device_ip)
		FROM refresh_tokens
		WHERE user_id = ?
		  AND family_id != ''
		  AND revoked_at IS NULL
		  AND expires_at > ?
		GROUP BY family_id
		ORDER BY MAX(created_at) DESC`),
		userID, time.Now().UTC(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionFamily
	for rows.Next() {
		var f SessionFamily
		if err := rows.Scan(&f.FamilyID, &f.WorkspaceID, &f.StartedAt, &f.LastUsedAt, &f.DeviceUA, &f.DeviceIP); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *UserStore) RefreshFamilyOwner(ctx context.Context, familyID string) (string, error) {
	if familyID == "" {
		return "", nil
	}
	var userID string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT user_id FROM refresh_tokens WHERE family_id = ? LIMIT 1`), familyID,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return userID, err
}

func (s *UserStore) RefreshFamilyForHash(ctx context.Context, tokenHash string) (string, error) {
	var familyID string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT COALESCE(family_id,'') FROM refresh_tokens WHERE token_hash = ?`), tokenHash,
	).Scan(&familyID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return familyID, err
}

func (s *UserStore) RevokeAllRefreshFamiliesExcept(ctx context.Context, userID, keepFamily string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE refresh_tokens SET revoked_at = ?
		   WHERE user_id = ? AND revoked_at IS NULL AND family_id != ?`),
		time.Now(), userID, keepFamily,
	)
	return err
}

func (s *UserStore) RevokeAllRefreshTokensForUser(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE refresh_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`),
		time.Now(), userID,
	)
	return err
}

func (s *UserStore) PurgeExpiredRefreshTokens(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM refresh_tokens WHERE expires_at < ? OR revoked_at IS NOT NULL`),
		time.Now().UTC(),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *UserStore) LinkOIDCIdentity(ctx context.Context, userID, provider, subject, email string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO oidc_identities (user_id, provider, subject, email, created_at) VALUES (?,?,?,?,?)
		 ON CONFLICT(provider, subject) DO NOTHING`),
		userID, provider, subject, email, time.Now(),
	)
	return err
}

func (s *UserStore) GetUserByOIDCSub(ctx context.Context, provider, subject string) (*User, error) {
	var userID string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT user_id FROM oidc_identities WHERE provider = ? AND subject = ?`),
		provider, subject,
	).Scan(&userID)
	if err != nil {
		return nil, err
	}
	return s.GetUserByID(ctx, userID)
}

func (s *UserStore) CreateUserSSO(ctx context.Context, u *User) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO users (id, email, name, created_at) VALUES (?,?,?,?)`),
		u.ID, u.Email, u.Name, u.CreatedAt,
	)
	return err
}

func (s *UserStore) GetDefaultWorkspace(ctx context.Context, userID string) (wsID string, role Role, err error) {
	err = s.db.QueryRowContext(ctx, s.rb(
		`SELECT workspace_id, role FROM workspace_members WHERE user_id = ? ORDER BY invited_at ASC LIMIT 1`),
		userID,
	).Scan(&wsID, &role)
	return wsID, role, err
}

var ErrPendingAccess error = apperr.Forbidden("auth.errors.pending_access")

// New sign-ins must never auto-join an existing tenant's workspace.
// With no workspace in the install, the first user gets one anyway, or nobody could administer it.
func (s *UserStore) ProvisionSSOWorkspace(ctx context.Context, u *User) (wsID string, role Role, err error) {
	if slug := strings.ToLower(strings.TrimSpace(os.Getenv("OCTOPUS_SSO_DEFAULT_WORKSPACE"))); slug != "" {
		defaultRole, roleErr := SSODefaultRole()
		if roleErr != nil {
			return "", "", roleErr
		}
		ws, wsErr := s.GetWorkspaceBySlug(ctx, slug)
		if wsErr != nil {
			return "", "", apperr.Wrap(wsErr, "auth.errors.sso_default_workspace_lookup", "slug", slug)
		}
		if ws == nil {
			return "", "", apperr.Precondition("auth.errors.sso_default_workspace_missing", "slug", slug)
		}
		if addErr := s.AddMember(ctx, ws.ID, u.ID, defaultRole); addErr != nil {
			return "", "", apperr.Wrap(addErr, "auth.errors.sso_default_workspace_join", "slug", slug)
		}
		return ws.ID, defaultRole, nil
	}

	if !PersonalWorkspacesAllowed() || !newcomersFoundWorkspaces() {
		anyWS, countErr := s.hasAnyWorkspace(ctx)
		if countErr != nil {
			return "", "", apperr.Wrap(countErr, "auth.errors.workspaces_check")
		}
		if anyWS {
			return "", "", ErrPendingAccess
		}
	}

	slug := slugify(u.Name)
	if slug == "" {
		slug = "workspace"
	}
	name := PersonalWorkspaceName(ctx, u.Name)
	ws, createErr := s.CreateWorkspace(ctx, u.ID, name, slug)
	if createErr != nil {
		ws, createErr = s.CreateWorkspace(ctx, u.ID, name, slug+"-"+genID()[:4])
	}
	if createErr != nil {
		return "", "", apperr.Wrap(createErr, "auth.errors.personal_workspace_create")
	}
	return ws.ID, RoleOwner, nil
}

func PersonalWorkspaceName(ctx context.Context, fullName string) string {
	first := strings.TrimSpace(fullName)
	if f := strings.Fields(first); len(f) > 0 {
		first = f[0]
	}
	return i18n.TCtx(ctx, "auth.labels.personal_workspace", "name", first)
}

type APIKey struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Prefix         string       `json:"prefix"`
	CreatedBy      string       `json:"created_by"`
	CreatedByEmail string       `json:"created_by_email,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	LastUsedAt     *time.Time   `json:"last_used_at,omitempty"`
	RevokedAt      *time.Time   `json:"revoked_at,omitempty"`
	ScopeTaskID    string       `json:"scope_task_id,omitempty"`
	ScopeChatID    string       `json:"scope_chat_id,omitempty"`
	ScopeAgent     string       `json:"scope_agent,omitempty"`
	ScopeStage     string       `json:"scope_stage,omitempty"`
	ExpiresAt      *time.Time   `json:"expires_at,omitempty"`
	WorkspaceID    string       `json:"workspace_id"`
	Role           Role         `json:"role"`
	Permissions    []Permission `json:"permissions"`
	ReplacedBy     string       `json:"replaced_by,omitempty"`
}

type KeyGrant struct {
	WorkspaceID string
	Role        Role
	Permissions []Permission
	ExpiresIn   time.Duration
}

var ErrKeyTTLTooLong error = apperr.Invalid("auth.errors.key_ttl_too_long")

// 90 days: the rotation period most enterprise policies require.
const defaultAPIKeyMaxTTL = 90 * 24 * time.Hour

// `thulu keys mint` and `thulu init` read this same variable from deploy/.env when their
// environment lacks it.
const MaxKeyTTLEnv = "OCTOPUS_API_KEY_MAX_TTL"

func APIKeyMaxTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv(MaxKeyTTLEnv))
	if raw == "" {
		return defaultAPIKeyMaxTTL
	}
	if raw == "0" {
		return 0
	}
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		var n int
		if _, err := fmt.Sscanf(days, "%d", &n); err == nil && n > 0 && fmt.Sprint(n) == days {
			return time.Duration(n) * 24 * time.Hour
		}
	} else if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	logx.Warn(apperr.Invalid("auth.errors.invalid_value", "value", raw),
		"auth.log.api_key_max_ttl_invalid", "default", defaultAPIKeyMaxTTL)
	return defaultAPIKeyMaxTTL
}

func errKeyTTLTooLong(maxTTL time.Duration) error {
	const day = 24 * time.Hour
	if maxTTL >= day && maxTTL%day == 0 {
		return apperr.Wrap(ErrKeyTTLTooLong, "auth.errors.key_ttl_max_days", "count", int(maxTTL/day))
	}
	return apperr.Wrap(ErrKeyTTLTooLong, "auth.errors.key_ttl_max", "max", maxTTL)
}

var ErrKeyNeedsWorkspace error = apperr.Invalid("auth.errors.key_needs_workspace")

var ErrPermissionsAboveRole error = apperr.Invalid("auth.errors.permissions_above_role")

var ErrKeyNeedsPermissions error = apperr.Invalid("auth.errors.key_needs_permissions")

func (s *UserStore) CreateAPIKey(ctx context.Context, name, createdBy string, grant KeyGrant) (rawKey string, meta *APIKey, err error) {
	if strings.TrimSpace(grant.WorkspaceID) == "" {
		return "", nil, ErrKeyNeedsWorkspace
	}
	if len(grant.Permissions) == 0 {
		return "", nil, ErrKeyNeedsPermissions
	}
	if grant.Role == "" {
		grant.Role = RoleDeveloper
	}
	// Both human mint paths (HTTP handler, `thulu keys mint`) funnel through here; run keys
	// bypass via createAPIKey.
	if dead := PermissionsAboveRole(grant.Permissions, grant.Role); len(dead) > 0 {
		if len(dead) == 1 {
			return "", nil, apperr.Wrap(ErrPermissionsAboveRole, "auth.errors.permission_above_role_detail",
				"role", grant.Role, "permissions", joinPermissions(dead))
		}
		return "", nil, apperr.Wrap(ErrPermissionsAboveRole, "auth.errors.permissions_above_role_detail",
			"role", grant.Role, "permissions", joinPermissions(dead))
	}
	if maxTTL := APIKeyMaxTTL(); maxTTL > 0 {
		switch {
		case grant.ExpiresIn <= 0:
			grant.ExpiresIn = maxTTL
		case grant.ExpiresIn > maxTTL:
			return "", nil, errKeyTTLTooLong(maxTTL)
		}
	}
	var expiresAt *time.Time
	if grant.ExpiresIn > 0 {
		exp := time.Now().Add(grant.ExpiresIn)
		expiresAt = &exp
	}
	return s.createAPIKey(ctx, name, createdBy, RunScope{}, grant, expiresAt)
}

func (s *UserStore) createAPIKey(ctx context.Context,
	name, createdBy string, scope RunScope, grant KeyGrant, expiresAt *time.Time,
) (rawKey string, meta *APIKey, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return
	}
	rawKey = "oct_" + hex.EncodeToString(b)
	prefix := rawKey[:12]

	h := sha256Sum(rawKey)
	id := genID()
	now := time.Now()
	_, err = s.db.ExecContext(ctx, s.rb(
		`INSERT INTO api_keys (id, name, key_hash, key_prefix, created_by, created_at,
		                       scope_task_id, scope_chat_id, scope_agent, scope_stage, scope_delegated_by,
		                       scope_llm_agent, scope_llm_engine,
		                       expires_at, workspace_id, role, permissions)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		id, name, h, prefix, createdBy, now,
		scope.TaskID, scope.ChatID, scope.Agent, scope.Stage, scope.DelegatedBy,
		scope.LLMAgent, scope.LLMEngine, expiresAt,
		grant.WorkspaceID, string(grant.Role), EncodePermissions(grant.Permissions),
	)
	if err != nil {
		return
	}
	meta = &APIKey{
		ID: id, Name: name, Prefix: prefix, CreatedBy: createdBy, CreatedAt: now,
		ScopeTaskID: scope.TaskID, ScopeChatID: scope.ChatID,
		ScopeAgent: scope.Agent, ScopeStage: scope.Stage, ExpiresAt: expiresAt,
		WorkspaceID: grant.WorkspaceID, Role: grant.Role, Permissions: grant.Permissions,
	}
	return
}

type APIKeyInfo struct {
	ID          string
	Name        string
	ExpiresAt   *time.Time
	Scope       RunScope
	WorkspaceID string
	Role        Role
	Permissions []Permission
}

func (s *UserStore) LookupAPIKeyInfo(ctx context.Context, rawKey string) *APIKeyInfo {
	h := sha256Sum(rawKey)
	var info APIKeyInfo
	var expires sql.NullTime
	var role, perms string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id, name, scope_task_id, scope_chat_id, scope_agent, scope_stage, scope_delegated_by,
		        scope_llm_agent, scope_llm_engine,
		        expires_at, workspace_id, role, permissions
		   FROM api_keys WHERE key_hash = ? AND revoked_at IS NULL`), h,
	).Scan(&info.ID, &info.Name, &info.Scope.TaskID, &info.Scope.ChatID, &info.Scope.Agent, &info.Scope.Stage,
		&info.Scope.DelegatedBy, &info.Scope.LLMAgent, &info.Scope.LLMEngine, &expires, &info.WorkspaceID, &role, &perms)
	if err != nil {
		return nil
	}
	info.Role = Role(role)
	info.Permissions = DecodePermissions(perms)
	if expires.Valid {
		if time.Now().After(expires.Time) {
			return nil
		}
		exp := expires.Time
		info.ExpiresAt = &exp
	}
	_, err = s.db.ExecContext(ctx, s.rb(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`), time.Now(), info.ID)
	logx.Warn(err, "auth.log.update_last_used", "id", info.ID)
	return &info
}

func (s *UserStore) ListAPIKeys(ctx context.Context, workspaceID string) ([]APIKey, error) {
	return s.listKeys(ctx, `k.scope_task_id = '' AND k.scope_chat_id = '' AND k.scope_agent = ''
		    AND k.workspace_id = ?`, workspaceID)
}

func (s *UserStore) ListRunKeys(ctx context.Context, workspaceID string) ([]APIKey, error) {
	return s.listKeys(ctx, `(k.scope_task_id <> '' OR k.scope_chat_id <> '' OR k.scope_agent <> '')
		    AND k.workspace_id = ? AND k.revoked_at IS NULL
		    AND (k.expires_at IS NULL OR k.expires_at > ?)`, workspaceID, time.Now())
}

func (s *UserStore) GetWorkspaceAPIKey(ctx context.Context, id, workspaceID string) (*APIKey, error) {
	keys, err := s.listKeys(ctx, `k.id = ? AND k.workspace_id = ?`, id, workspaceID)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	return &keys[0], nil
}

// where must be a fixed clause from this file, never caller input.
func (s *UserStore) listKeys(ctx context.Context, where string, args ...any) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT k.id, k.name, k.key_prefix, k.created_by, COALESCE(u.email, ''), k.created_at,
		        k.last_used_at, k.revoked_at, k.expires_at,
		        k.scope_task_id, k.scope_chat_id, k.scope_agent, k.scope_stage,
		        k.workspace_id, k.role, k.permissions, k.replaced_by
		   FROM api_keys k LEFT JOIN users u ON u.id = k.created_by
		  WHERE `+where+` ORDER BY k.created_at DESC`),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIKey
	for rows.Next() {
		var k APIKey
		var lastUsed, revoked, expires sql.NullTime
		var role, perms string
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedBy, &k.CreatedByEmail, &k.CreatedAt,
			&lastUsed, &revoked, &expires,
			&k.ScopeTaskID, &k.ScopeChatID, &k.ScopeAgent, &k.ScopeStage,
			&k.WorkspaceID, &role, &perms, &k.ReplacedBy); err != nil {
			return nil, err
		}
		k.Role = Role(role)
		k.Permissions = DecodePermissions(perms)
		if lastUsed.Valid {
			k.LastUsedAt = &lastUsed.Time
		}
		if revoked.Valid {
			k.RevokedAt = &revoked.Time
		}
		if expires.Valid {
			k.ExpiresAt = &expires.Time
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *UserStore) RetireAPIKey(ctx context.Context, id, workspaceID, replacedBy string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE api_keys SET expires_at = ?, replaced_by = ?
		  WHERE id = ? AND workspace_id = ? AND revoked_at IS NULL
		    AND (expires_at IS NULL OR expires_at > ?)`),
		at, replacedBy, id, workspaceID, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *UserStore) RevokeAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`UPDATE api_keys SET revoked_at = ? WHERE id = ?`), time.Now(), id)
	return err
}

// Requests from people must use RevokeWorkspaceAPIKey so one tenant cannot revoke another's key by
// id.
func (s *UserStore) RevokeWorkspaceAPIKey(ctx context.Context, id, workspaceID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE api_keys SET revoked_at = ? WHERE id = ? AND workspace_id = ?`),
		time.Now(), id, workspaceID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *UserStore) HasAnyAPIKey(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL`)).Scan(&count)
	return count > 0, err
}

func (s *UserStore) hasAnyWorkspace(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces`).Scan(&n)
	return n > 0, err
}

func (s *UserStore) HasAnyUser(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT COUNT(*) FROM users`)).Scan(&count)
	return count > 0, err
}

func genID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func sha256Sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
