package ingest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// Published prefix that secret scanners match; never change it, and keep it distinct from the API
// keys' oct_.
const TokenPrefix = "octel_"

// Stored in the clear; keep it too short to weaken the secret.
const prefixLen = len(TokenPrefix) + 8

const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

var ErrNotFound error = apperr.NotFound("ingest.errors.token_not_found")

type RejectReason string

const (
	RejectNone      RejectReason = ""
	RejectNoToken   RejectReason = "no_token"
	RejectBadToken  RejectReason = "bad_token"
	RejectRevoked   RejectReason = "revoked"
	RejectIPBlocked RejectReason = "ip_not_allowed"
)

type Token struct {
	ID            string     `json:"id"`
	EnvironmentID string     `json:"environment_id"`
	Name          string     `json:"name"`
	Prefix        string     `json:"prefix"`
	Status        string     `json:"status"`
	AllowCIDRs    []string   `json:"allow_cidrs"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
}

type Source struct {
	TokenID       string    `json:"token_id"`
	EnvironmentID string    `json:"environment_id"`
	IP            string    `json:"ip"`
	Accepted      int64     `json:"accepted"`
	Rejected      int64     `json:"rejected"`
	LastReject    string    `json:"last_reject,omitempty"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
}

type Store struct {
	db *sql.DB

	mu      sync.Mutex
	pending map[sourceKey]*sourceDelta
}

type sourceKey struct{ tokenID, ip string }

type sourceDelta struct {
	envID      string
	accepted   int64
	rejected   int64
	lastReject RejectReason
	lastSeen   time.Time
	firstSeen  time.Time
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

func (s *Store) DB() *sql.DB { return s.db }

// Never change: a database that ran these migrations under another name would run them all again.
const MigrationComponent = "ingest"

// Append only: a published migration never changes.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS ingest_tokens (
			id             TEXT PRIMARY KEY,
			environment_id TEXT NOT NULL,
			name           TEXT NOT NULL DEFAULT '',
			token_hash     TEXT NOT NULL,
			token_prefix   TEXT NOT NULL DEFAULT '',
			status         TEXT NOT NULL DEFAULT 'active',
			allow_cidrs    TEXT NOT NULL DEFAULT '[]',
			created_by     TEXT NOT NULL DEFAULT '',
			created_at     TIMESTAMP NOT NULL,
			revoked_at     TIMESTAMP,
			last_seen_at   TIMESTAMP
		)`},
		{ID: 2, SQL: `CREATE UNIQUE INDEX IF NOT EXISTS uq_ingest_tokens_hash
			ON ingest_tokens(token_hash)`},
		{ID: 3, SQL: `CREATE INDEX IF NOT EXISTS idx_ingest_tokens_env
			ON ingest_tokens(environment_id)`},

		{ID: 4, SQL: `CREATE TABLE IF NOT EXISTS ingest_sources (
			token_id       TEXT NOT NULL,
			environment_id TEXT NOT NULL,
			ip             TEXT NOT NULL,
			accepted       BIGINT NOT NULL DEFAULT 0,
			rejected       BIGINT NOT NULL DEFAULT 0,
			last_reject    TEXT NOT NULL DEFAULT '',
			first_seen     TIMESTAMP NOT NULL,
			last_seen      TIMESTAMP NOT NULL,
			PRIMARY KEY (token_id, ip)
		)`},
		{ID: 5, SQL: `CREATE INDEX IF NOT EXISTS idx_ingest_sources_env
			ON ingest_sources(environment_id)`},
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "ingest.errors.schema")
	}
	return &Store{db: db, pending: map[sourceKey]*sourceDelta{}}, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func genID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Several active tokens per environment are intended: rotation mints the new token before revoking
// the old one.
func (s *Store) CreateToken(ctx context.Context, envID, name, createdBy string, allowCIDRs []string) (string, *Token, error) {
	if strings.TrimSpace(envID) == "" {
		return "", nil, apperr.Invalid("ingest.errors.environment_required")
	}
	if err := ValidateCIDRs(allowCIDRs); err != nil {
		return "", nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	raw := TokenPrefix + hex.EncodeToString(b)

	cidrJSON, err := json.Marshal(normalizeCIDRs(allowCIDRs))
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()
	tok := &Token{
		ID:            genID(),
		EnvironmentID: envID,
		Name:          name,
		Prefix:        raw[:prefixLen],
		Status:        StatusActive,
		AllowCIDRs:    normalizeCIDRs(allowCIDRs),
		CreatedBy:     createdBy,
		CreatedAt:     now,
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO ingest_tokens
		   (id, environment_id, name, token_hash, token_prefix, status, allow_cidrs, created_by, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`),
		tok.ID, envID, name, hashToken(raw), tok.Prefix, StatusActive, string(cidrJSON), createdBy, now,
	); err != nil {
		return "", nil, err
	}
	return raw, tok, nil
}

func (s *Store) ListTokens(ctx context.Context, envID string) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, environment_id, name, token_prefix, status, allow_cidrs,
		        created_by, created_at, revoked_at, last_seen_at
		   FROM ingest_tokens WHERE environment_id = ? ORDER BY created_at DESC`), envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		var cidrs string
		if err := rows.Scan(&t.ID, &t.EnvironmentID, &t.Name, &t.Prefix, &t.Status,
			&cidrs, &t.CreatedBy, &t.CreatedAt, &t.RevokedAt, &t.LastSeenAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(cidrs), &t.AllowCIDRs); err != nil {
			return nil, apperr.Wrap(err, "ingest.errors.unreadable_allowlist", "id", t.ID)
		}
		if t.AllowCIDRs == nil {
			t.AllowCIDRs = []string{}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Revoked tokens stay in the table so the audit rows that reference them keep resolving.
func (s *Store) RevokeToken(ctx context.Context, envID, tokenID string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE ingest_tokens SET status = ?, revoked_at = ?
		  WHERE id = ? AND environment_id = ? AND status = ?`),
		StatusRevoked, now, tokenID, envID, StatusActive)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetAllowCIDRs(ctx context.Context, envID, tokenID string, cidrs []string) error {
	if err := ValidateCIDRs(cidrs); err != nil {
		return err
	}
	blob, err := json.Marshal(normalizeCIDRs(cidrs))
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE ingest_tokens SET allow_cidrs = ? WHERE id = ? AND environment_id = ?`),
		string(blob), tokenID, envID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type Auth struct {
	OK            bool
	Reason        RejectReason
	TokenID       string
	EnvironmentID string
}

// The environment comes only from the token; never accept it from the caller, or a staging token
// could write into production.
func (s *Store) Authenticate(ctx context.Context, rawToken, clientIP string) Auth {
	raw := strings.TrimSpace(rawToken)
	if raw == "" {
		return Auth{Reason: RejectNoToken}
	}

	var (
		id, envID, status, cidrJSON, storedHash string
	)
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id, environment_id, status, allow_cidrs, token_hash
		   FROM ingest_tokens WHERE token_hash = ?`), hashToken(raw),
	).Scan(&id, &envID, &status, &cidrJSON, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return Auth{Reason: RejectBadToken}
	}
	if err != nil {
		return Auth{Reason: RejectBadToken}
	}
	// Redundant after the hash lookup, but kept: never compare hashes with a short-circuiting
	// operator.
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashToken(raw))) != 1 {
		return Auth{Reason: RejectBadToken}
	}

	if status != StatusActive {
		s.recordSource(id, envID, clientIP, RejectRevoked)
		return Auth{Reason: RejectRevoked, TokenID: id, EnvironmentID: envID}
	}

	// An unreadable allowlist must fail closed, not allow every address.
	var cidrs []string
	if err := json.Unmarshal([]byte(cidrJSON), &cidrs); err != nil {
		s.recordSource(id, envID, clientIP, RejectIPBlocked)
		return Auth{Reason: RejectIPBlocked, TokenID: id, EnvironmentID: envID}
	}
	if !IPAllowed(clientIP, cidrs) {
		s.recordSource(id, envID, clientIP, RejectIPBlocked)
		return Auth{Reason: RejectIPBlocked, TokenID: id, EnvironmentID: envID}
	}

	s.recordSource(id, envID, clientIP, RejectNone)
	return Auth{OK: true, TokenID: id, EnvironmentID: envID}
}

func IPAllowed(ip string, cidrs []string) bool {
	if len(cidrs) == 0 {
		return true
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, c := range cidrs {
		if _, network, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			if network.Contains(parsed) {
				return true
			}
			continue
		}
		if bare := net.ParseIP(strings.TrimSpace(c)); bare != nil && bare.Equal(parsed) {
			return true
		}
	}
	return false
}

func ValidateCIDRs(cidrs []string) error {
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(c); err == nil {
			continue
		}
		if net.ParseIP(c) != nil {
			continue
		}
		return apperr.Invalid("ingest.errors.invalid_cidr", "value", c)
	}
	return nil
}

func normalizeCIDRs(in []string) []string {
	out := []string{}
	for _, c := range in {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// Buffered in memory: a DB write per request, forged ones included, would make auth itself a cheap
// DoS.
func (s *Store) recordSource(tokenID, envID, ip string, reason RejectReason) {
	if tokenID == "" || ip == "" {
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	k := sourceKey{tokenID: tokenID, ip: ip}
	d := s.pending[k]
	if d == nil {
		d = &sourceDelta{envID: envID, firstSeen: now}
		s.pending[k] = d
	}
	if reason == RejectNone {
		d.accepted++
	} else {
		d.rejected++
		d.lastReject = reason
	}
	d.lastSeen = now
}

func (s *Store) FlushSources(ctx context.Context) error {
	s.mu.Lock()
	batch := s.pending
	s.pending = map[sourceKey]*sourceDelta{}
	s.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}
	var firstErr error
	seen := map[string]time.Time{}
	for k, d := range batch {
		_, err := s.db.ExecContext(ctx, s.rb(
			`INSERT INTO ingest_sources
			   (token_id, environment_id, ip, accepted, rejected, last_reject, first_seen, last_seen)
			 VALUES (?,?,?,?,?,?,?,?)
			 ON CONFLICT (token_id, ip) DO UPDATE SET
			   accepted    = ingest_sources.accepted + excluded.accepted,
			   rejected    = ingest_sources.rejected + excluded.rejected,
			   last_reject = CASE WHEN excluded.rejected > 0
			                      THEN excluded.last_reject ELSE ingest_sources.last_reject END,
			   last_seen   = excluded.last_seen`),
			k.tokenID, d.envID, k.ip, d.accepted, d.rejected, string(d.lastReject), d.firstSeen, d.lastSeen)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if d.accepted > 0 && d.lastSeen.After(seen[k.tokenID]) {
			seen[k.tokenID] = d.lastSeen
		}
	}
	for tokenID, ts := range seen {
		if _, err := s.db.ExecContext(ctx, s.rb(
			`UPDATE ingest_tokens SET last_seen_at = ? WHERE id = ?`), ts, tokenID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Store) RunFlusher(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := s.FlushSources(flushCtx); err != nil {
				logx.Warn(err, "ingest.log.final_source_flush")
			}
			cancel()
			return
		case <-t.C:
			if err := s.FlushSources(ctx); err != nil {
				logx.Warn(err, "ingest.log.source_flush")
			}
		}
	}
}

func (s *Store) ListSources(ctx context.Context, envID string) ([]Source, error) {
	if err := s.FlushSources(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT token_id, environment_id, ip, accepted, rejected, last_reject, first_seen, last_seen
		   FROM ingest_sources WHERE environment_id = ?
		  ORDER BY last_seen DESC`), envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.TokenID, &s.EnvironmentID, &s.IP, &s.Accepted, &s.Rejected,
			&s.LastReject, &s.FirstSeen, &s.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
