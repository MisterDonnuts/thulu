package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const (
	SandboxPrioritiesKey = "sandbox.priorities"

	SandboxMaxKey = "sandbox.max"

	SandboxIdleEvictSecondsKey = "sandbox.idle_evict_seconds"

	SandboxAcquireBlockMsKey = "sandbox.acquire_block_ms"

	ChatMaxKey = "chat.max_containers"

	ChatIdleEvictSecondsKey = "chat.idle_evict_seconds"

	ChatTurnTimeoutSecondsKey = "chat.turn_timeout_seconds"

	AgentMemoryBytesKey = "agent.memory_bytes"

	AgentPidsLimitKey = "agent.pids_limit"

	AgentNanoCPUsKey = "agent.nano_cpus"

	AgentMaxContainersKey = "agent.max_containers"

	MentionMaxContainersKey = "mention.max_containers"

	AgentReapOnExitKey = "agent.reap_on_exit"

	// Empty means unset: readers must not fall back to the daemon's own host, which is unreachable
	// from the cloud the application runs in.
	IngestPublicURLKey = "ingest.public_url"

	EnvMetricsIntervalSecondsKey = "envmetrics.interval_seconds"

	RetentionRunLogsDaysKey = "retention.run_logs_days"

	RetentionSessionsDaysKey = "retention.sessions_days"

	RetentionWebhookDeliveriesDaysKey = "retention.webhook_deliveries_days"

	RetentionRunKeysDaysKey = "retention.run_keys_days"

	RetentionSandboxRowsDaysKey = "retention.sandbox_rows_days"

	RetentionCacheMetricsDaysKey = "retention.cache_metrics_days"

	RetentionStuckWorktreeDaysKey = "retention.stuck_worktree_days"

	JanitorDiskFreeMinPercentKey = "janitor.disk_free_min_percent"

	// Deliberately absent from defaults: audit retention is opt-in, unset or 0 keeps every entry.
	AuditRetentionDaysKey = "audit.retention_days"
)

const DefaultAgentMemoryBytes int64 = 2 * 1024 * 1024 * 1024

type SandboxPriorities map[string]int

func DerivePriorities(board []types.StageInfo) SandboxPriorities {
	sorted := append([]types.StageInfo(nil), board...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	out := SandboxPriorities{}
	rank := 0
	for i := range sorted {
		st := &sorted[i]
		if st.Virtual {
			continue
		}
		if st.Initial || st.Terminal {
			out[string(st.ID)] = 0
			continue
		}
		rank++
		out[string(st.ID)] = rank * 10
	}
	return out
}

// SandboxPrioritiesKey is absent on purpose: its default depends on the stage board, seeded later
// by SeedSandboxPriorities.
var defaults = map[string]any{
	SandboxMaxKey:              3,
	SandboxIdleEvictSecondsKey: 30 * 60,
	SandboxAcquireBlockMsKey:   2 * 60_000,
	ChatMaxKey:                 5,
	ChatIdleEvictSecondsKey:    30 * 60,
	ChatTurnTimeoutSecondsKey:  5 * 60,
	AgentMemoryBytesKey:        DefaultAgentMemoryBytes,
	AgentPidsLimitKey:          1024,
	AgentNanoCPUsKey:           0,
	AgentMaxContainersKey:      0,
	MentionMaxContainersKey:    3,
	AgentReapOnExitKey:         true,
	// Matches the rollup's 5-minute bucket width: faster polls overwrite the same bucket, slower
	// ones leave gaps.
	EnvMetricsIntervalSecondsKey:      5 * 60,
	IngestPublicURLKey:                "",
	RetentionRunLogsDaysKey:           30,
	RetentionSessionsDaysKey:          90,
	RetentionWebhookDeliveriesDaysKey: 30,
	RetentionRunKeysDaysKey:           7,
	RetentionSandboxRowsDaysKey:       30,
	// The cache panel's longest range is 90 days.
	RetentionCacheMetricsDaysKey:  90,
	RetentionStuckWorktreeDaysKey: 14,
	JanitorDiskFreeMinPercentKey:  10,
}

func DefaultValues() map[string]any { return maps.Clone(defaults) }

// GET /settings serves the whole table, so an unknown key one admin writes is handed to every
// caller.
func KnownKey(key string) bool {
	if key == SandboxPrioritiesKey {
		return true
	}
	if key == AuditRetentionDaysKey {
		return true
	}
	_, ok := defaults[key]
	return ok
}

var ErrNotFound error = apperr.NotFound("settings.errors.key_not_found")

type Store struct {
	db    *sql.DB
	board func() []types.StageInfo
}

// Call once at boot, before SeedSandboxPriorities.
func (s *Store) SetStageBoard(board func() []types.StageInfo) { s.board = board }

func (s *Store) currentBoard() []types.StageInfo {
	if s.board == nil {
		return nil
	}
	return s.board()
}

func (s *Store) SeedSandboxPriorities(ctx context.Context) error {
	if _, err := s.GetRaw(ctx, SandboxPrioritiesKey); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return s.PutJSON(ctx, SandboxPrioritiesKey, DerivePriorities(s.currentBoard()))
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

const MigrationComponent = "settings"

func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS settings (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`},
		{ID: 2, SQL: `CREATE TABLE IF NOT EXISTS workspace_settings (
			workspace_id TEXT NOT NULL,
			key          TEXT NOT NULL,
			value        TEXT NOT NULL,
			updated_at   TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (workspace_id, key)
		)`},
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "settings.errors.create_table")
	}
	s := &Store{db: db}
	if err := s.seedDefaults(context.Background()); err != nil {
		return nil, apperr.Wrap(err, "settings.errors.seed_defaults")
	}
	return s, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) seedDefaults(ctx context.Context) error {
	have, err := s.List(ctx)
	if err != nil {
		return err
	}
	for key, val := range defaults {
		if _, ok := have[key]; ok {
			continue
		}
		if err = s.PutJSON(ctx, key, val); err != nil {
			return apperr.Wrap(err, "settings.errors.seed_key", "key", key)
		}
	}
	return nil
}

func (s *Store) GetRaw(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT value FROM settings WHERE key = ?`), key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

func (s *Store) PutRaw(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`),
		key, value, time.Now().UTC(),
	)
	return err
}

func (s *Store) PutRawMany(ctx context.Context, values map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	q := s.rb(`INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`)
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, q, key, value, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PutJSON(ctx context.Context, key string, dest any) error {
	b, err := json.Marshal(dest)
	if err != nil {
		return err
	}
	return s.PutRaw(ctx, key, string(b))
}

func (s *Store) GetJSON(ctx context.Context, key string, dest any) error {
	raw, err := s.GetRaw(ctx, key)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), dest)
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM settings WHERE key = ?`), key)
	return err
}

func (s *Store) List(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT key, value FROM settings ORDER BY key`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *Store) SandboxPriorities(ctx context.Context) (SandboxPriorities, error) {
	var p SandboxPriorities
	if err := s.GetJSON(ctx, SandboxPrioritiesKey, &p); err != nil {
		if errors.Is(err, ErrNotFound) {
			return DerivePriorities(s.currentBoard()), nil
		}
		return nil, err
	}
	return p, nil
}

func (s *Store) AddStagePriorities(ctx context.Context, board []types.StageInfo) ([]string, error) {
	prios, err := s.SandboxPriorities(ctx)
	if err != nil {
		return nil, err
	}
	if prios == nil {
		prios = SandboxPriorities{}
	}
	var added []string
	for id, w := range DerivePriorities(board) {
		if _, ok := prios[id]; ok {
			continue
		}
		prios[id] = w
		added = append(added, id)
	}
	if len(added) == 0 {
		return nil, nil
	}
	sort.Strings(added)
	return added, s.SetSandboxPriorities(ctx, prios)
}

func (s *Store) SetSandboxPriorities(ctx context.Context, p SandboxPriorities) error {
	return s.PutJSON(ctx, SandboxPrioritiesKey, p)
}

func (s *Store) SandboxMax(ctx context.Context) (int, error) {
	return s.getInt(ctx, SandboxMaxKey, 3)
}

func (s *Store) EnvMetricsIntervalSeconds(ctx context.Context) (int, error) {
	return s.getInt(ctx, EnvMetricsIntervalSecondsKey, 5*60)
}

func (s *Store) SetEnvMetricsIntervalSeconds(ctx context.Context, n int) error {
	if n != 0 && n < 60 {
		return apperr.Invalid("settings.errors.metrics_interval_too_short", "got", n)
	}
	return s.PutJSON(ctx, EnvMetricsIntervalSecondsKey, n)
}

func (s *Store) IngestPublicURL(ctx context.Context) (string, error) {
	var v string
	err := s.GetJSON(ctx, IngestPublicURLKey, &v)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(v), "/"), nil
}

func (s *Store) SetIngestPublicURL(ctx context.Context, raw string) error {
	v, err := NormalizeIngestPublicURL(raw)
	if err != nil {
		return err
	}
	return s.PutJSON(ctx, IngestPublicURLKey, v)
}

func NormalizeIngestPublicURL(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	// net/url fails a missing scheme two ways: "10.0.0.5:8585" is a parse error,
	// "localhost:8585" parses with scheme "localhost".
	// Every refusal must name the http:// fix.
	u, err := url.Parse(v)
	if err != nil {
		return "", apperr.WrapKind(apperr.KindInvalid, err, "settings.errors.ingest_url_unparsable", "value", v)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", apperr.Invalid("settings.errors.ingest_url_scheme", "value", v)
	}
	if u.Host == "" {
		return "", apperr.Invalid("settings.errors.ingest_url_no_host", "value", v)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", apperr.Invalid("settings.errors.ingest_url_not_base", "value", v)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func (s *Store) SetSandboxMax(ctx context.Context, n int) error {
	if n < 1 {
		return apperr.Invalid("settings.errors.sandbox_max_too_low")
	}
	return s.PutJSON(ctx, SandboxMaxKey, n)
}

func (s *Store) SandboxIdleEvictSeconds(ctx context.Context) (int, error) {
	return s.getInt(ctx, SandboxIdleEvictSecondsKey, 30*60)
}

func (s *Store) SandboxAcquireBlockMs(ctx context.Context) (int, error) {
	return s.getInt(ctx, SandboxAcquireBlockMsKey, 2*60_000)
}

func (s *Store) ChatMax(ctx context.Context) (int, error) {
	return s.getInt(ctx, ChatMaxKey, 5)
}

func (s *Store) ChatIdleEvictSeconds(ctx context.Context) (int, error) {
	return s.getInt(ctx, ChatIdleEvictSecondsKey, 30*60)
}

func (s *Store) ChatTurnTimeoutSeconds(ctx context.Context) (int, error) {
	return s.getInt(ctx, ChatTurnTimeoutSecondsKey, 5*60)
}

func (s *Store) AgentResourceCaps(ctx context.Context) types.ResourceCaps {
	return types.ResourceCaps{
		MemoryBytes: s.int64OrDefault(ctx, AgentMemoryBytesKey, DefaultAgentMemoryBytes),
		PidsLimit:   s.int64OrDefault(ctx, AgentPidsLimitKey, 1024),
		NanoCPUs:    s.int64OrDefault(ctx, AgentNanoCPUsKey, 0),
	}
}

func (s *Store) int64OrDefault(ctx context.Context, key string, fallback int64) int64 {
	v, err := s.getInt64(ctx, key, fallback)
	if err != nil {
		return fallback
	}
	return v
}

func (s *Store) MentionMaxContainers(ctx context.Context) (int, error) {
	return s.getInt(ctx, MentionMaxContainersKey, 3)
}

func (s *Store) AgentMaxContainers(ctx context.Context) (int, error) {
	return s.getInt(ctx, AgentMaxContainersKey, 0)
}

func (s *Store) AgentReapOnExit(ctx context.Context) (bool, error) {
	return s.getBool(ctx, AgentReapOnExitKey, true)
}

func (s *Store) RetentionDays(ctx context.Context, key string) (int, error) {
	fallback, ok := defaults[key].(int)
	if !ok {
		return 0, apperr.Invalid("settings.errors.not_retention_window", "key", key)
	}
	n, err := s.getInt(ctx, key, fallback)
	if err != nil {
		return 0, err
	}
	return max(n, 0), nil
}

func (s *Store) JanitorDiskFreeMinPercent(ctx context.Context) (int, error) {
	n, err := s.getInt(ctx, JanitorDiskFreeMinPercentKey, 10)
	if err != nil {
		return 0, err
	}
	return min(max(n, 0), 100), nil
}

func (s *Store) getInt(ctx context.Context, key string, fallback int) (int, error) {
	var n int
	err := s.GetJSON(ctx, key, &n)
	if errors.Is(err, ErrNotFound) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) getBool(ctx context.Context, key string, fallback bool) (bool, error) {
	var b bool
	err := s.GetJSON(ctx, key, &b)
	if errors.Is(err, ErrNotFound) {
		return fallback, nil
	}
	if err != nil {
		return false, err
	}
	return b, nil
}

func (s *Store) getInt64(ctx context.Context, key string, fallback int64) (int64, error) {
	var n int64
	err := s.GetJSON(ctx, key, &n)
	if errors.Is(err, ErrNotFound) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	return n, nil
}
