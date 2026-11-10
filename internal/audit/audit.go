package audit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/listpage"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/middleware"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

type EventKind string

const (
	KindUserRegistered EventKind = "user.registered"
	KindUserLoggedIn   EventKind = "user.logged_in"

	KindMemberAdded   EventKind = "workspace.member_added"
	KindMemberRemoved EventKind = "workspace.member_removed"

	KindTaskCreated      EventKind = "task.created"
	KindTaskDeleted      EventKind = "task.deleted"
	KindTaskStageChanged EventKind = "task.stage_changed"
	KindQuestionAnswered EventKind = "question.answered"
	KindQuestionCanceled EventKind = "question.canceled"
	KindApprovalDecided  EventKind = "approval.decided"
	KindApprovalWaived   EventKind = "approval.waived"
	KindApprovalDeleted  EventKind = "approval.deleted"

	KindAgentStarted  EventKind = "agent.started"
	KindAgentFinished EventKind = "agent.finished"

	KindProjectCreated           EventKind = "project.created"
	KindProjectInitScriptChanged EventKind = "project.init_script_changed"
	KindProjectRunnerChanged     EventKind = "project.runner_changed"

	KindStageCreated   EventKind = "stage.created"
	KindStageUpdated   EventKind = "stage.updated"
	KindStageDeleted   EventKind = "stage.deleted"
	KindStageReordered EventKind = "stage.reordered"
	KindStagesPruned   EventKind = "stage.pruned"

	KindBoardCreated     EventKind = "board.created"
	KindBoardUpdated     EventKind = "board.updated"
	KindBoardDeleted     EventKind = "board.deleted"
	KindBoardDefaults    EventKind = "board.default_changed"
	KindTaskTypesChanged EventKind = "board.task_types_changed"

	KindSecretAccessed EventKind = "secret.accessed"

	KindAgentContentChanged EventKind = "agent.content_changed"
	KindAgentDefChanged     EventKind = "agent.definition_changed"
	KindWorkshopApplied     EventKind = "workshop.applied"

	KindRunnerTokenCreated EventKind = "runner.token_created"
	KindRunnerRegistered   EventKind = "runner.registered"
	KindRunnerRevoked      EventKind = "runner.revoked"

	KindCredentialCreated       EventKind = "credential.created"        //nolint:gosec // G101: event name
	KindCredentialUpdated       EventKind = "credential.updated"        //nolint:gosec // G101: event name
	KindCredentialDeleted       EventKind = "credential.deleted"        //nolint:gosec // G101: event name
	KindCredentialGrantsChanged EventKind = "credential.grants_changed" //nolint:gosec // G101: event name
	KindCredentialToolsChanged  EventKind = "credential.tools_changed"  //nolint:gosec // G101: event name
	KindCredentialStateChanged  EventKind = "credential.state_changed"  //nolint:gosec // G101: event name

	KindLLMProfileCreated EventKind = "llm_profile.created"
	KindLLMProfileUpdated EventKind = "llm_profile.updated"
	KindLLMProfileDeleted EventKind = "llm_profile.deleted"
	KindLLMBindingChanged EventKind = "llm_binding.changed"
	KindLLMBindingRemoved EventKind = "llm_binding.removed"
	KindLLMBindingForced  EventKind = "llm_binding.forced"

	KindChatCreated       EventKind = "chat.created"
	KindChatDeleted       EventKind = "chat.deleted"
	KindChatReset         EventKind = "chat.reset"
	KindChatMessagePosted EventKind = "chat.message_posted"

	KindChatDelegationStarted EventKind = "chat.delegation_started"
	KindChatDelegationEnded   EventKind = "chat.delegation_ended"
	KindChatActionHeld        EventKind = "chat.action_held"
	KindChatActionConfirmed   EventKind = "chat.action_confirmed"
	KindChatActionCanceled    EventKind = "chat.action_canceled"
	KindChatDelegationPolicy  EventKind = "chat.delegation_policy_changed"
	KindChatGithubGrant       EventKind = "chat.github_granted"

	KindEngineLoginConnected    EventKind = "engine_login.connected"
	KindEngineLoginDisconnected EventKind = "engine_login.disconnected"

	KindWorkspaceSettingChanged EventKind = "workspace.setting_changed"

	KindWebhookRotated EventKind = "webhook.secret_rotated"

	KindAuditPurged EventKind = "audit.purged"
)

type Entry struct {
	ID           string            `json:"id"`
	Kind         EventKind         `json:"kind"`
	WorkspaceID  string            `json:"workspace_id,omitempty"`
	ActorID      string            `json:"actor_id,omitempty"`
	ActorEmail   string            `json:"actor_email,omitempty"`
	ResourceID   string            `json:"resource_id,omitempty"`
	ResourceType string            `json:"resource_type,omitempty"`
	Summary      string            `json:"summary"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	ClientIP     string            `json:"client_ip,omitempty"`
	UserAgent    string            `json:"user_agent,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	// Empty on rows written before the column existed; the UI must show it as unknown, not guess.
	ActorType  string `json:"actor_type,omitempty"`
	ActorLabel string `json:"actor_label,omitempty"`
	PrevHash   string `json:"prev_hash,omitempty"`
	RowHash    string `json:"row_hash,omitempty"`
}

type Store struct {
	db   *sql.DB
	sink *sink
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

// Never change: the name keys the migration history, so another one would rerun every migration.
const MigrationComponent = "audit"

// A published migration never changes; internal/schema reads this for the migrations lock and lint.
func Migrations(_ migrate.Inspector) ([]migrate.Migration, error) {
	return []migrate.Migration{
		{ID: 1, SQL: `
			CREATE TABLE IF NOT EXISTS audit_log (
				id            TEXT PRIMARY KEY,
				kind          TEXT NOT NULL,
				workspace_id  TEXT NOT NULL DEFAULT '',
				actor_id      TEXT NOT NULL DEFAULT 'system',
				actor_email   TEXT NOT NULL DEFAULT '',
				resource_id   TEXT NOT NULL DEFAULT '',
				resource_type TEXT NOT NULL DEFAULT '',
				summary       TEXT NOT NULL,
				metadata      TEXT NOT NULL DEFAULT '{}',
				client_ip     TEXT NOT NULL DEFAULT '',
				user_agent    TEXT NOT NULL DEFAULT '',
				created_at    TIMESTAMP NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_audit_kind        ON audit_log(kind);
			CREATE INDEX IF NOT EXISTS idx_audit_workspace   ON audit_log(workspace_id, created_at);
			CREATE INDEX IF NOT EXISTS idx_audit_actor       ON audit_log(actor_id, created_at);
			CREATE INDEX IF NOT EXISTS idx_audit_resource    ON audit_log(resource_id, created_at);
			CREATE INDEX IF NOT EXISTS idx_audit_created_at  ON audit_log(created_at);`},

		{ID: 2, SQL: `
			CREATE INDEX IF NOT EXISTS idx_audit_created_id ON audit_log(created_at, id);
			CREATE INDEX IF NOT EXISTS idx_audit_workspace_created_id ON audit_log(workspace_id, created_at, id);`},

		// seq orders the hash chain: created_at is truncated to the second and ids are random.
		// Pre-existing rows keep empty hashes; the chain starts after them.
		{ID: 3, SQL: `
			ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS actor_type  TEXT NOT NULL DEFAULT '';
			ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS actor_label TEXT NOT NULL DEFAULT '';
			ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS prev_hash   TEXT NOT NULL DEFAULT '';
			ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS row_hash    TEXT NOT NULL DEFAULT '';
			ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS seq         BIGSERIAL;
			CREATE INDEX IF NOT EXISTS idx_audit_seq ON audit_log(seq);
			CREATE INDEX IF NOT EXISTS idx_audit_actor_type ON audit_log(workspace_id, actor_type, created_at);`},
	}, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "audit.errors.schema")
	}

	snk, err := sinkFromEnv()
	if err != nil {
		return nil, err
	}
	return &Store{db: db, sink: snk}, nil
}

func (s *Store) DB() *sql.DB { return s.db }

// The row hash covers created_at in this format; it must match what the column returns on read.
const auditTimeFormat = "2006-01-02 15:04:05"

// Audit failures never propagate to callers. The sink gets the entry even when the insert failed,
// on purpose.
func (s *Store) Log(e Entry) {
	if e.ID == "" {
		e.ID = newID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	// Truncate to the column's precision so the stored row and its hash agree.
	e.CreatedAt = e.CreatedAt.UTC().Truncate(time.Second)

	meta, err := json.Marshal(e.Metadata)
	logx.Warn(err, "audit.log.marshal_metadata", "kind", e.Kind, "resource", e.ResourceID)

	if err := s.insertChained(&e, string(meta)); err != nil {
		log.Printf("[audit] %s", i18n.Log("audit.log.insert_failed", "kind", e.Kind, "resource", e.ResourceID, "err", err))
	}
	s.sink.write(e)
}

// The advisory lock serializes appends across every daemon sharing the database, so two inserts
// never read the same predecessor and fork the chain.
func (s *Store) insertChained(e *Entry, meta string) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, lockErr := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, auditChainLockKey); lockErr != nil {
		return apperr.Wrap(lockErr, "audit.errors.lock_chain")
	}
	var prev string
	err = tx.QueryRowContext(ctx,
		`SELECT row_hash FROM audit_log WHERE row_hash <> '' ORDER BY seq DESC LIMIT 1`).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return apperr.Wrap(err, "audit.errors.read_chain_head")
	}
	e.PrevHash = prev
	e.RowHash = rowHash(prev, e, meta)

	if _, err := tx.ExecContext(ctx, s.rb(`
		INSERT INTO audit_log
		  (id, kind, workspace_id, actor_id, actor_type, actor_label, actor_email,
		   resource_id, resource_type, summary, metadata, client_ip, user_agent, created_at,
		   prev_hash, row_hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		e.ID, string(e.Kind), e.WorkspaceID, e.ActorID, e.ActorType, e.ActorLabel, e.ActorEmail,
		e.ResourceID, e.ResourceType, e.Summary, meta,
		e.ClientIP, e.UserAgent, e.CreatedAt.Format(auditTimeFormat),
		e.PrevHash, e.RowHash,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LogRequest(r *http.Request, kind EventKind, resourceID, resourceType, summary string, metadata map[string]string) {
	s.LogRequestIn(r, "", kind, resourceID, resourceType, summary, metadata)
}

func (s *Store) LogRequestIn(
	r *http.Request, workspaceID string, kind EventKind,
	resourceID, resourceType, summary string, metadata map[string]string,
) {
	s.LogRequestAs(r, auth.ActorFromRequest(r), workspaceID, kind, resourceID, resourceType, summary, metadata)
}

func (s *Store) LogRequestAs(
	r *http.Request, actor auth.Actor, workspaceID string, kind EventKind,
	resourceID, resourceType, summary string, metadata map[string]string,
) {
	if workspaceID == "" {
		workspaceID = actor.WorkspaceID
	}
	s.Log(Entry{
		Kind:         kind,
		WorkspaceID:  workspaceID,
		ActorID:      actor.ID,
		ActorType:    actor.Type,
		ActorLabel:   actor.Label,
		ActorEmail:   actor.Email,
		ResourceID:   resourceID,
		ResourceType: resourceType,
		Summary:      summary,
		Metadata:     metadata,
		ClientIP:     clientIP(r),
		UserAgent:    r.Header.Get("User-Agent"),
	})
}

func (s *Store) LogContext(
	ctx context.Context, workspaceID string, kind EventKind,
	resourceID, resourceType, summary string, metadata map[string]string,
) {
	actor := auth.ActorFromContext(ctx)
	if workspaceID == "" {
		workspaceID = actor.WorkspaceID
	}
	s.Log(Entry{
		Kind:         kind,
		WorkspaceID:  workspaceID,
		ActorID:      actor.ID,
		ActorType:    actor.Type,
		ActorLabel:   actor.Label,
		ActorEmail:   actor.Email,
		ResourceID:   resourceID,
		ResourceType: resourceType,
		Summary:      summary,
		Metadata:     metadata,
	})
}

type ListOptions struct {
	WorkspaceID string
	ActorID     string
	ResourceID  string
	Kind        EventKind
	Since       time.Time
	Until       time.Time
	Limit       int
	Offset      int
	Cursor      string
}

func buildAuditQuery(opts ListOptions) (string, []any) {
	q := `SELECT id, kind, workspace_id, actor_id, actor_email, resource_id,
	             resource_type, summary, metadata, client_ip, user_agent, created_at,
	             actor_type, actor_label, prev_hash, row_hash
	      FROM audit_log WHERE 1=1`
	args := []any{}

	if opts.WorkspaceID != "" {
		q += " AND workspace_id = ?"
		args = append(args, opts.WorkspaceID)
	}
	if opts.ActorID != "" {
		q += " AND actor_id = ?"
		args = append(args, opts.ActorID)
	}
	if opts.ResourceID != "" {
		q += " AND resource_id = ?"
		args = append(args, opts.ResourceID)
	}
	if opts.Kind != "" {
		kindStr := string(opts.Kind)
		if !strings.Contains(kindStr, ".") {
			q += " AND kind LIKE ?"
			args = append(args, kindStr+".%")
		} else {
			q += " AND kind = ?"
			args = append(args, kindStr)
		}
	}
	if !opts.Since.IsZero() {
		q += " AND created_at >= ?"
		args = append(args, opts.Since.UTC().Format("2006-01-02 15:04:05"))
	}
	if !opts.Until.IsZero() {
		q += " AND created_at <= ?"
		args = append(args, opts.Until.UTC().Format("2006-01-02 15:04:05"))
	}
	return q, args
}

// The id tie-break is required: created_at is truncated to the second and one call can append many
// rows within it, so pages would skip entries.
const auditOrder = " ORDER BY created_at DESC, id DESC"

func (s *Store) List(opts ListOptions) ([]Entry, error) {
	out, _, err := s.ListPage(opts)
	return out, err
}

func (s *Store) ListPage(opts ListOptions) ([]Entry, string, error) {
	if opts.Limit <= 0 {
		opts.Limit = 100
	}
	if opts.Limit > 1000 {
		opts.Limit = 1000
	}

	q, args := buildAuditQuery(opts)
	if opts.Cursor != "" {
		key, err := listpage.Decode(opts.Cursor)
		if err != nil {
			return nil, "", err
		}
		q += " AND (created_at, id) < (?, ?)"
		args = append(args, key.At, key.ID)
	}
	q += auditOrder
	q += " LIMIT ?"
	args = append(args, opts.Limit+1)
	if opts.Cursor == "" && opts.Offset > 0 {
		q += " OFFSET ?"
		args = append(args, opts.Offset)
	}

	rows, err := s.db.QueryContext(context.Background(), s.rb(q), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		e, _, err := scanExportRow(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) <= opts.Limit {
		return out, "", nil
	}
	out = out[:opts.Limit]
	last := out[len(out)-1]
	return out, listpage.Encode(listpage.Key{At: last.CreatedAt, ID: last.ID}), nil
}

type ExportRow struct {
	Entry    Entry
	Metadata string
}

// Exporter is set by ee/: it writes the rows as CSV or NDJSON. Without it the audit log has no
// export, and Store.Export returns ErrExportUnavailable before writing anything.
var Exporter func(rows iter.Seq2[ExportRow, error], format string, w io.Writer) error

var ErrExportUnavailable error = apperr.Forbidden("audit.errors.export_unavailable")

func (s *Store) Export(opts ListOptions, format string, w io.Writer) error {
	if Exporter == nil {
		return ErrExportUnavailable
	}
	q, args := buildAuditQuery(opts)
	q += " ORDER BY created_at DESC, id DESC"
	rows, err := s.db.QueryContext(context.Background(), s.rb(q), args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	seq := func(yield func(ExportRow, error) bool) {
		for rows.Next() {
			e, meta, err := scanExportRow(rows)
			if !yield(ExportRow{Entry: e, Metadata: meta}, err) || err != nil {
				return
			}
		}
	}
	if err := Exporter(seq, strings.ToLower(format), w); err != nil {
		return err
	}
	return rows.Err()
}

func scanExportRow(rows *sql.Rows) (Entry, string, error) {
	var e Entry
	var metaStr string
	if err := rows.Scan(
		&e.ID, &e.Kind, &e.WorkspaceID, &e.ActorID, &e.ActorEmail,
		&e.ResourceID, &e.ResourceType, &e.Summary, &metaStr,
		&e.ClientIP, &e.UserAgent, &e.CreatedAt,
		&e.ActorType, &e.ActorLabel, &e.PrevHash, &e.RowHash,
	); err != nil {
		return Entry{}, "", err
	}
	logx.Warn(json.Unmarshal([]byte(metaStr), &e.Metadata), "audit.log.unmarshal_metadata", "id", e.ID)
	return e, metaStr, nil
}

func (s *Store) PurgeOlderThan(cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, nil
	}
	res, err := s.db.ExecContext(context.Background(),
		s.rb(`DELETE FROM audit_log WHERE created_at < ?`),
		cutoff.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.Log(Entry{
			Kind:       KindAuditPurged,
			ActorID:    "system",
			ActorType:  auth.ActorSystem,
			ActorLabel: i18n.Text("audit.labels.retention_policy"),
			Summary:    i18n.Text("audit.comments.retention_purged", "count", n, "cutoff", cutoff.UTC().Format(time.RFC3339)),
			Metadata:   map[string]string{"cutoff": cutoff.UTC().Format(time.RFC3339), "rows": fmt.Sprint(n)},
		})
	}
	return n, nil
}

// Trusted-proxy-aware: a forged X-Forwarded-For must not plant arbitrary strings in the audit
// trail.
func clientIP(r *http.Request) string {
	return middleware.ClientIP(r)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
