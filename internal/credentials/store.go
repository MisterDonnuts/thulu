package credentials

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
	"github.com/MisterDonnuts/thulu/internal/strx"
)

var (
	ErrNotFound error = apperr.NotFound("credentials.errors.not_found")
	ErrExists   error = apperr.Conflict("credentials.errors.name_taken")
)

// Per-provider HKDF label so one provider's ciphertext cannot decrypt as another's.
// Changing it makes every stored payload undecryptable.
func secretLabel(provider string) string {
	return "octopus-credential-" + provider + "-v1"
}

type Store struct {
	db *sql.DB
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

func (s *Store) DB() *sql.DB { return s.db }

// Never change: databases that already ran these migrations under this name would rerun all of
// them.
const MigrationComponent = "credentials"

// Append only: a published migration never changes. internal/schema reads this list for the
// migrations lock and lint.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `
			CREATE TABLE IF NOT EXISTS credentials (
				id                TEXT PRIMARY KEY,
				workspace_id      TEXT NOT NULL,
				name              TEXT NOT NULL,
				provider          TEXT NOT NULL,
				kind              TEXT NOT NULL,
				payload_encrypted TEXT NOT NULL,
				hint              TEXT NOT NULL DEFAULT '',
				identity          TEXT NOT NULL DEFAULT '',
				state             TEXT NOT NULL DEFAULT 'unknown',
				capabilities      TEXT NOT NULL DEFAULT '',
				introspect_error  TEXT NOT NULL DEFAULT '',
				disabled          INTEGER NOT NULL DEFAULT 0,
				validated_at      TIMESTAMP,
				expires_at        TIMESTAMP,
				created_at        TIMESTAMP NOT NULL,
				updated_at        TIMESTAMP NOT NULL,
				created_by        TEXT NOT NULL DEFAULT '',
				UNIQUE (workspace_id, name)
			);
			CREATE INDEX IF NOT EXISTS idx_credentials_workspace ON credentials(workspace_id);
			CREATE TABLE IF NOT EXISTS credential_grants (
				credential_id TEXT NOT NULL,
				subject_type  TEXT NOT NULL,
				subject_id    TEXT NOT NULL DEFAULT '',
				created_at    TIMESTAMP NOT NULL,
				PRIMARY KEY (credential_id, subject_type, subject_id)
			);
			CREATE INDEX IF NOT EXISTS idx_credential_grants_subject
				ON credential_grants(subject_type, subject_id);
			CREATE TABLE IF NOT EXISTS credential_tool_policy (
				credential_id TEXT NOT NULL,
				server        TEXT NOT NULL,
				tool          TEXT NOT NULL,
				enabled       INTEGER NOT NULL DEFAULT 1,
				updated_at    TIMESTAMP NOT NULL,
				updated_by    TEXT NOT NULL DEFAULT '',
				PRIMARY KEY (credential_id, server, tool)
			);
			CREATE TABLE IF NOT EXISTS credential_decisions (
				id            TEXT PRIMARY KEY,
				workspace_id  TEXT NOT NULL,
				credential_id TEXT NOT NULL DEFAULT '',
				task_id       TEXT NOT NULL DEFAULT '',
				agent         TEXT NOT NULL DEFAULT '',
				stage         TEXT NOT NULL DEFAULT '',
				server        TEXT NOT NULL DEFAULT '',
				tool          TEXT NOT NULL DEFAULT '',
				allowed       INTEGER NOT NULL DEFAULT 0,
				reason        TEXT NOT NULL DEFAULT '',
				created_at    TIMESTAMP NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_credential_decisions_ws
				ON credential_decisions(workspace_id, created_at);
		`},
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "credentials.errors.create_tables")
	}
	return &Store{db: db}, nil
}

func (s *Store) Create(ctx context.Context, workspaceID string, in Input, createdBy string) (*Credential, error) {
	if workspaceID == "" {
		return nil, apperr.Invalid("credentials.errors.workspace_id_required")
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, apperr.Invalid("credentials.errors.name_required")
	}
	if strings.TrimSpace(in.Provider) == "" {
		return nil, apperr.Invalid("credentials.errors.provider_required")
	}
	if len(in.Payload) == 0 {
		return nil, apperr.Invalid("credentials.errors.payload_required")
	}
	enc, err := encryptPayload(in.Provider, in.Payload)
	if err != nil {
		return nil, err
	}
	id := newID()
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, s.rb(
		`INSERT INTO credentials
		   (id, workspace_id, name, provider, kind, payload_encrypted, hint,
		    state, created_at, updated_at, created_by)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`),
		id, workspaceID, strings.TrimSpace(in.Name), in.Provider, in.Kind, enc,
		"", string(StateUnknown), now, now, createdBy)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrExists
		}
		return nil, err
	}
	return s.Get(ctx, workspaceID, id)
}

func (s *Store) Update(ctx context.Context, workspaceID, id string, in Input) (*Credential, error) {
	cur, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = cur.Name
	}
	if len(in.Payload) == 0 {
		_, err = s.db.ExecContext(ctx, s.rb(
			`UPDATE credentials SET name = ?, updated_at = ? WHERE workspace_id = ? AND id = ?`),
			name, now, workspaceID, id)
	} else {
		var enc string
		if enc, err = encryptPayload(cur.Provider, in.Payload); err != nil {
			return nil, err
		}
		kind := in.Kind
		if kind == "" {
			kind = cur.Kind
		}
		_, err = s.db.ExecContext(ctx, s.rb(
			`UPDATE credentials
			    SET name = ?, kind = ?, payload_encrypted = ?, hint = '',
			        identity = '', state = ?, capabilities = '',
			        introspect_error = '', validated_at = NULL, expires_at = NULL,
			        updated_at = ?
			  WHERE workspace_id = ? AND id = ?`),
			name, kind, enc, string(StateUnknown), now, workspaceID, id)
	}
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrExists
		}
		return nil, err
	}
	return s.Get(ctx, workspaceID, id)
}

// Unlike Update, keeps the verdict: for daemon-renewed secrets (OAuth tokens), clearing it would
// show a working connection as unchecked.
func (s *Store) SetPayload(ctx context.Context, workspaceID, id string, payload map[string]string, in Introspection) error {
	cur, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	enc, err := encryptPayload(cur.Provider, payload)
	if err != nil {
		return err
	}
	state := in.State
	if state == "" {
		state = StateUnknown
	}
	var expires any
	if !in.ExpiresAt.IsZero() {
		expires = in.ExpiresAt.UTC()
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE credentials
		    SET payload_encrypted = ?, identity = ?, hint = ?, state = ?,
		        capabilities = ?, introspect_error = ?, validated_at = ?,
		        expires_at = ?, updated_at = ?
		  WHERE workspace_id = ? AND id = ?`),
		enc, in.Identity, in.Hint, string(state), strings.Join(in.Capabilities, ","),
		in.Err, now, expires, now, workspaceID, id)
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

func (s *Store) SetDisabled(ctx context.Context, workspaceID, id string, disabled bool) error {
	flag := 0
	if disabled {
		flag = 1
	}
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE credentials SET disabled = ?, updated_at = ? WHERE workspace_id = ? AND id = ?`),
		flag, time.Now().UTC(), workspaceID, id)
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

// Must not select payload_encrypted: Payload() is the only path to the ciphertext.
//
//nolint:gosec // G101: a SELECT naming a credentials table is not a credential.
const selectCredential = `SELECT id, workspace_id, name, provider, kind, hint, identity,
	        state, capabilities, introspect_error, disabled, validated_at, expires_at,
	        created_at, updated_at, created_by
	   FROM credentials`

func (s *Store) Get(ctx context.Context, workspaceID, id string) (*Credential, error) {
	if workspaceID == "" || id == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, s.rb(
		selectCredential+` WHERE workspace_id = ? AND id = ?`), workspaceID, id)
	c, err := scanCredential(row)
	if err != nil {
		return nil, err
	}
	if c.Grants, err = s.Grants(ctx, c.ID); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Store) List(ctx context.Context, workspaceID string) ([]*Credential, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		selectCredential+` WHERE workspace_id = ? ORDER BY name`), workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Credential{}
	for rows.Next() {
		c, scanErr := scanCredential(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	for _, c := range out {
		if c.Grants, err = s.Grants(ctx, c.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) Delete(ctx context.Context, workspaceID, id string) error {
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM credentials WHERE workspace_id = ? AND id = ?`), workspaceID, id); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM credential_grants WHERE credential_id = ?`,
		`DELETE FROM credential_tool_policy WHERE credential_id = ?`,
	} {
		if _, err := s.db.ExecContext(ctx, s.rb(q), id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	ids, err := s.idsForWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	var firstErr error
	for _, id := range ids {
		if err := s.Delete(ctx, workspaceID, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM credential_decisions WHERE workspace_id = ?`), workspaceID); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *Store) idsForWorkspace(ctx context.Context, workspaceID string) ([]string, error) {
	return dbpkg.CollectStrings(ctx, s.db,
		`SELECT id FROM credentials WHERE workspace_id = ?`, workspaceID)
}

// The only path from ciphertext to plaintext: callers must have authorized the request already.
// Never log the result.
func (s *Store) Payload(ctx context.Context, workspaceID, id string) (map[string]string, error) {
	var provider, enc string
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT provider, payload_encrypted FROM credentials WHERE workspace_id = ? AND id = ?`),
		workspaceID, id)
	if err := row.Scan(&provider, &enc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	raw, err := secretbox.Decrypt(secretLabel(provider), enc)
	if err != nil {
		return nil, apperr.Wrap(err, "credentials.errors.decrypt")
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, apperr.Wrap(err, "credentials.errors.unmarshal")
	}
	return payload, nil
}

type Introspection struct {
	Identity     string
	Hint         string
	State        State
	Capabilities []string
	ExpiresAt    time.Time
	Err          string
}

// Only when the provider answered; use SetIntrospectionError when it could not be reached.
func (s *Store) SetIntrospection(ctx context.Context, workspaceID, id string, in Introspection) error {
	var expires any
	if !in.ExpiresAt.IsZero() {
		expires = in.ExpiresAt.UTC()
	}
	state := in.State
	if state == "" {
		state = StateUnknown
	}
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE credentials
		    SET identity = ?, hint = ?, state = ?, capabilities = ?,
		        introspect_error = ?, validated_at = ?, expires_at = ?, updated_at = ?
		  WHERE workspace_id = ? AND id = ?`),
		in.Identity, in.Hint, string(state), strings.Join(in.Capabilities, ","),
		in.Err, time.Now().UTC(), expires, time.Now().UTC(), workspaceID, id)
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

// Must not touch capabilities: blank capabilities make HasCapability allow everything, so a network
// blip would widen the grant.
func (s *Store) SetIntrospectionError(ctx context.Context, workspaceID, id, msg string) error {
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE credentials SET introspect_error = ?, updated_at = ?
		  WHERE workspace_id = ? AND id = ?`),
		msg, time.Now().UTC(), workspaceID, id)
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

// Crosses the tenancy boundary: only for daemon-internal callers (the monitor), never on behalf of
// a request.
func (s *Store) AllCredentials(ctx context.Context) ([]*Credential, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		selectCredential+` ORDER BY workspace_id, name`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Credential{}
	for rows.Next() {
		c, scanErr := scanCredential(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Grants(ctx context.Context, credentialID string) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT subject_type, subject_id FROM credential_grants
		  WHERE credential_id = ? ORDER BY subject_type, subject_id`), credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.SubjectType, &g.SubjectID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) GrantedSubjects(ctx context.Context, workspaceID string, t SubjectType) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT g.subject_id
		   FROM credential_grants g
		   JOIN credentials c ON c.id = g.credential_id
		  WHERE c.workspace_id = ? AND g.subject_type = ?`), workspaceID, string(t))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Call when a subject is deleted, so a later resource reusing its id cannot inherit the grant.
func (s *Store) RevokeGrantsTo(ctx context.Context, workspaceID string, t SubjectType, subjectID string) error {
	if workspaceID == "" || subjectID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM credential_grants
		  WHERE subject_type = ? AND subject_id = ?
		    AND credential_id IN (SELECT id FROM credentials WHERE workspace_id = ?)`),
		string(t), subjectID, workspaceID)
	return err
}

func (s *Store) SetGrants(ctx context.Context, workspaceID, id string, grants []Grant) error {
	if _, err := s.Get(ctx, workspaceID, id); err != nil {
		return err
	}
	for _, g := range grants {
		if err := g.Valid(); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.rb(
		`DELETE FROM credential_grants WHERE credential_id = ?`), id); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, g := range grants {
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO credential_grants (credential_id, subject_type, subject_id, created_at)
			 VALUES (?,?,?,?)`), id, string(g.SubjectType), g.SubjectID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Reachable(ctx context.Context, reach Reach) ([]*Credential, error) {
	all, err := s.List(ctx, reach.WorkspaceID)
	if err != nil {
		return nil, err
	}
	out := []*Credential{}
	for _, c := range all {
		for _, g := range c.Grants {
			if reach.Covers(g) {
				out = append(out, c)
				break
			}
		}
	}
	return out, nil
}

func (s *Store) ToolPolicy(ctx context.Context, credentialID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT server, tool, enabled FROM credential_tool_policy WHERE credential_id = ?`),
		credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var server, tool string
		var enabled int
		if err := rows.Scan(&server, &tool, &enabled); err != nil {
			return nil, err
		}
		out[PolicyKey(server, tool)] = enabled == 1
	}
	return out, rows.Err()
}

// Only disabled entries are stored: the table holds exceptions, so new tools default to enabled
// without a migration.
func (s *Store) SetToolPolicy(ctx context.Context, workspaceID, id string, entries []ToolPolicy, updatedBy string) error {
	if _, err := s.Get(ctx, workspaceID, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.rb(
		`DELETE FROM credential_tool_policy WHERE credential_id = ?`), id); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, e := range entries {
		if e.Enabled {
			continue
		}
		if strings.TrimSpace(e.Server) == "" || strings.TrimSpace(e.Tool) == "" {
			return apperr.Invalid("credentials.errors.policy_entry_incomplete")
		}
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO credential_tool_policy (credential_id, server, tool, enabled, updated_at, updated_by)
			 VALUES (?,?,?,0,?,?)`), id, e.Server, e.Tool, now, updatedBy); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type DecisionRecord struct {
	ID           string    `json:"id"`
	WorkspaceID  string    `json:"workspace_id"`
	CredentialID string    `json:"credential_id,omitempty"`
	TaskID       string    `json:"task_id,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	Stage        string    `json:"stage,omitempty"`
	Server       string    `json:"server,omitempty"`
	Tool         string    `json:"tool,omitempty"`
	Allowed      bool      `json:"allowed"`
	Reason       string    `json:"reason,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Callers must log and continue on error: an audit write must never fail a tool call.
func (s *Store) RecordDecision(ctx context.Context, d DecisionRecord) error {
	allowed := 0
	if d.Allowed {
		allowed = 1
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO credential_decisions
		   (id, workspace_id, credential_id, task_id, agent, stage, server, tool, allowed, reason, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`),
		newID(), d.WorkspaceID, d.CredentialID, d.TaskID, d.Agent, d.Stage,
		d.Server, d.Tool, allowed, d.Reason, time.Now().UTC())
	return err
}

func (s *Store) ListDecisions(ctx context.Context, workspaceID string, limit int) ([]DecisionRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, workspace_id, credential_id, task_id, agent, stage, server, tool,
		        allowed, reason, created_at
		   FROM credential_decisions WHERE workspace_id = ?
		  ORDER BY created_at DESC LIMIT ?`), workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DecisionRecord{}
	for rows.Next() {
		var d DecisionRecord
		var allowed int
		if err := rows.Scan(&d.ID, &d.WorkspaceID, &d.CredentialID, &d.TaskID, &d.Agent,
			&d.Stage, &d.Server, &d.Tool, &allowed, &d.Reason, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.Allowed = allowed == 1
		out = append(out, d)
	}
	return out, rows.Err()
}

func encryptPayload(provider string, payload map[string]string) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", apperr.Wrap(err, "credentials.errors.marshal")
	}
	enc, err := secretbox.Encrypt(secretLabel(provider), string(raw))
	if err != nil {
		return "", apperr.Wrap(err, "credentials.errors.encrypt")
	}
	return enc, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanCredential(r rowScanner) (*Credential, error) {
	var (
		c            Credential
		capsCSV      string
		disabled     int
		validatedRaw sql.NullTime
		expiresRaw   sql.NullTime
	)
	if err := r.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Provider, &c.Kind, &c.Hint,
		&c.Identity, &c.State, &capsCSV, &c.IntrospectError, &disabled,
		&validatedRaw, &expiresRaw, &c.CreatedAt, &c.UpdatedAt, &c.CreatedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Disabled = disabled == 1
	c.Capabilities = sortedCSV(capsCSV)
	if validatedRaw.Valid {
		c.ValidatedAt = validatedRaw.Time
	}
	if expiresRaw.Valid {
		c.ExpiresAt = expiresRaw.Time
	}
	c.State = deriveState(c.State, c.ExpiresAt, time.Now().UTC())
	return &c, nil
}

func sortedCSV(s string) []string {
	out := strx.SplitCSV(s)
	sort.Strings(out)
	return out
}

func errIfNoRows(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return nil // driver doesn't report it; assume the update landed
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	//i18n:ignore driver error wording this matches, not a message
	return strings.Contains(msg, "duplicate key") ||
		//i18n:ignore driver error wording this matches, not a message
		strings.Contains(msg, "unique constraint") ||
		//i18n:ignore driver error wording this matches, not a message
		strings.Contains(msg, "unique violation")
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "cred_" + hex.EncodeToString(b)
}

type GitToken struct {
	CredentialID string
	Name         string
	Identity     string
	Token        string
}

func (s *Store) GithubCredentialForProject(ctx context.Context, workspaceID, projectID string) (*Credential, error) {
	if workspaceID == "" {
		return nil, ErrNotFound
	}
	reach := Reach{WorkspaceID: workspaceID}
	if projectID != "" {
		reach.ProjectIDs = []string{projectID}
	}
	reachable, err := s.Reachable(ctx, reach)
	if err != nil {
		return nil, err
	}
	best, bestScore := (*Credential)(nil), -1
	for _, c := range reachable {
		if c.Provider != "github" || c.Disabled || !usableForGit(c.State) {
			continue
		}
		score := grantScore(c.Grants, projectID)
		if score > bestScore || (score == bestScore && best != nil && c.UpdatedAt.After(best.UpdatedAt)) {
			best, bestScore = c, score
		}
	}
	if best == nil {
		return nil, ErrNotFound
	}
	return best, nil
}

func (s *Store) GithubTokenForProject(ctx context.Context, workspaceID, projectID string) (*GitToken, error) {
	best, err := s.GithubCredentialForProject(ctx, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	payload, err := s.Payload(ctx, workspaceID, best.ID)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(payload["token"])
	if token == "" {
		return nil, ErrNotFound
	}
	return &GitToken{
		CredentialID: best.ID,
		Name:         best.Name,
		Identity:     best.Identity,
		Token:        token,
	}, nil
}

// StateUnknown must count: every fine-grained PAT lands there because GitHub reports no scopes for
// them.
func usableForGit(st State) bool {
	return st == StateValid || st == StateExpiring || st == StateUnknown
}

func grantScore(grants []Grant, projectID string) int {
	score := 0
	for _, g := range grants {
		switch {
		case g.SubjectType == SubjectProject && projectID != "" && g.SubjectID == projectID:
			return 2
		case g.SubjectType == SubjectWorkspace:
			score = 1
		}
	}
	return score
}
