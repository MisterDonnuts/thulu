package chat

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

var ErrNotFound error = apperr.NotFound("chat.errors.not_found")

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Chat struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	AgentType   string `json:"agent_type"`
	TaskID      string `json:"task_id,omitempty"`
	ThreadID    string `json:"thread_id,omitempty"`
	Title       string `json:"title"`
	CreatedBy   string `json:"created_by"`
	// Stable id of the creator's credential (auth.OwnerFromContext); "" on chats created before
	// it was recorded, which VisibleTo matches by CreatedBy instead.
	Owner      string     `json:"-"`
	SessionID  string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	// Delegation counts only while DelegatedUntil is in the future (use Delegated); lapsed values
	// are left in place, not swept.
	DelegatedBy    string     `json:"delegated_by,omitempty"`
	DelegatedUntil *time.Time `json:"delegated_until,omitempty"`
}

func (c *Chat) Archived() bool { return c.ArchivedAt != nil }

type Message struct {
	ID          string            `json:"id"`
	ChatID      string            `json:"chat_id"`
	Role        Role              `json:"role"`
	Author      string            `json:"author"`
	Text        string            `json:"text"`
	DurationMs  int64             `json:"duration_ms,omitempty"`
	InputTokens int64             `json:"input_tokens,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

const MetaError = "error"

const MetaTextI18n = "text_i18n"

func (m *Message) SetTexts(texts map[string]string) {
	if len(texts) == 0 {
		return
	}
	raw, err := json.Marshal(texts)
	if err != nil {
		return
	}
	if m.Metadata == nil {
		m.Metadata = map[string]string{}
	}
	m.Metadata[MetaTextI18n] = string(raw)
}

func (m *Message) InLocale(locale string) *Message {
	if m == nil {
		return nil
	}
	raw := m.Metadata[MetaTextI18n]
	if raw == "" {
		return m
	}
	var texts map[string]string
	if json.Unmarshal([]byte(raw), &texts) != nil || texts[locale] == "" {
		return m
	}
	out := *m
	out.Text = texts[locale]
	return &out
}

// JSON array of ToolCall; the web drawer decodes it.
const MetaTools = "tools"

const MetaToolsOmitted = "tools_omitted"

type ToolCall struct {
	Name string `json:"name"`
	Hint string `json:"hint,omitempty"`
}

func EncodeTools(meta map[string]string, calls []ToolCall, limit int) {
	if len(calls) == 0 {
		return
	}
	kept := calls
	if limit > 0 && len(calls) > limit {
		kept = calls[:limit]
		meta[MetaToolsOmitted] = strconv.Itoa(len(calls) - limit)
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return
	}
	meta[MetaTools] = string(raw)
}

type Store struct {
	db *sql.DB
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

// Never change: databases that already ran these migrations under this name would rerun them all.
const MigrationComponent = "chats"

// internal/schema reads this for the migrations lock and lint; a published migration never changes.
func Migrations(in migrate.Inspector) ([]migrate.Migration, error) {
	migrations := []migrate.Migration{
		{ID: 1, SQL: `
			CREATE TABLE IF NOT EXISTS chats (
				id            TEXT PRIMARY KEY,
				workspace_id  TEXT NOT NULL DEFAULT '',
				agent_type    TEXT NOT NULL,
				task_id       TEXT NOT NULL DEFAULT '',
				title         TEXT NOT NULL DEFAULT '',
				created_by    TEXT NOT NULL DEFAULT '',
				session_id    TEXT NOT NULL DEFAULT '',
				created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				archived_at   TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_chats_ws       ON chats(workspace_id, updated_at DESC);
			CREATE INDEX IF NOT EXISTS idx_chats_task     ON chats(task_id);
			CREATE INDEX IF NOT EXISTS idx_chats_ws_agent ON chats(workspace_id, agent_type);

			CREATE TABLE IF NOT EXISTS chat_messages_v2 (
				id           TEXT PRIMARY KEY,
				chat_id      TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
				role         TEXT NOT NULL,
				author       TEXT NOT NULL DEFAULT '',
				text         TEXT NOT NULL,
				duration_ms  INTEGER NOT NULL DEFAULT 0,
				input_tokens INTEGER NOT NULL DEFAULT 0,
				metadata     TEXT NOT NULL DEFAULT '{}',
				created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_chatmsg_chat ON chat_messages_v2(chat_id, created_at);`},
		// The CREATE shim keeps this idempotent where component "chat" never ran.
		// session_id is deliberately not migrated: it names a container that is gone and cannot be
		// resumed.
		{ID: 2, SQL: `
			CREATE TABLE IF NOT EXISTS chat_messages (
				id            TEXT PRIMARY KEY,
				role          TEXT NOT NULL,
				text          TEXT NOT NULL,
				created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				duration_ms   INTEGER NOT NULL DEFAULT 0,
				raw_log_path  TEXT NOT NULL DEFAULT '',
				input_tokens  INTEGER NOT NULL DEFAULT 0
			);
			INSERT INTO chats (id, workspace_id, agent_type, title, created_by, created_at, updated_at)
				SELECT 'chat-manager-legacy', '', 'manager', 'Manager', 'system',
				       COALESCE(MIN(created_at), CURRENT_TIMESTAMP), COALESCE(MAX(created_at), CURRENT_TIMESTAMP)
				  FROM chat_messages HAVING COUNT(*) > 0;
			INSERT INTO chat_messages_v2 (id, chat_id, role, author, text, duration_ms, input_tokens, metadata, created_at)
				SELECT id, 'chat-manager-legacy', role, role, text, duration_ms, input_tokens, '{}', created_at
				  FROM chat_messages;
			DROP TABLE chat_messages;
			DELETE FROM schema_migrations WHERE component = 'chat';`},
		{ID: 3, SQL: `ALTER TABLE chats ADD COLUMN IF NOT EXISTS thread_id TEXT NOT NULL DEFAULT ''`},
		{ID: 4, SQL: `
			ALTER TABLE chats ADD COLUMN IF NOT EXISTS delegated_by    TEXT NOT NULL DEFAULT '';
			ALTER TABLE chats ADD COLUMN IF NOT EXISTS delegated_until TIMESTAMPTZ;

			CREATE TABLE IF NOT EXISTS chat_actions (
				id           TEXT PRIMARY KEY,
				chat_id      TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
				workspace_id TEXT NOT NULL,
				agent        TEXT NOT NULL DEFAULT '',
				method       TEXT NOT NULL,
				path         TEXT NOT NULL,
				body         BYTEA,
				summary      TEXT NOT NULL DEFAULT '',
				status       TEXT NOT NULL,
				result_code  INTEGER NOT NULL DEFAULT 0,
				result_error TEXT NOT NULL DEFAULT '',
				decided_by   TEXT NOT NULL DEFAULT '',
				created_at   TIMESTAMPTZ NOT NULL,
				decided_at   TIMESTAMPTZ
			);
			CREATE INDEX IF NOT EXISTS idx_chat_actions_chat ON chat_actions(chat_id, created_at);

			CREATE TABLE IF NOT EXISTS chat_policies (
				workspace_id        TEXT PRIMARY KEY,
				delegation_disabled BOOLEAN NOT NULL DEFAULT FALSE
			);`},
	}
	// Unstamped chats are hidden from every tenant; adopt task-linked ones into their task's
	// workspace.
	// Guarded on tasks: on a fresh database the chat store can migrate before it exists.
	adoptable, err := in.ColumnExists("tasks", "workspace_id")
	if err != nil {
		return nil, err
	}
	if adoptable {
		migrations = append(migrations, migrate.Migration{ID: 5, SQL: `
			UPDATE chats SET workspace_id = t.workspace_id
			  FROM tasks t
			 WHERE chats.workspace_id = '' AND chats.task_id <> ''
			   AND t.id = chats.task_id AND t.workspace_id <> ''`})
	}
	// Not backfilled: emails can't be mapped to user ids here, so existing rows keep owner '' and
	// VisibleTo falls back to created_by.
	migrations = append(migrations, migrate.Migration{ID: 6, SQL: `
		ALTER TABLE chats ADD COLUMN IF NOT EXISTS owner TEXT NOT NULL DEFAULT ''`})
	return migrations, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	migrations, err := Migrations(migrate.DBInspector(context.Background(), db))
	if err == nil {
		err = migrate.Run(context.Background(), db, MigrationComponent, migrations)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "chat.errors.init_schema")
	}
	return &Store{db: db}, nil
}

func (s *Store) DB() *sql.DB { return s.db }

func newID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

const chatCols = `id, workspace_id, agent_type, task_id, thread_id, title, created_by, owner, session_id,
	created_at, updated_at, archived_at, delegated_by, delegated_until`

func (s *Store) CreateChat(ctx context.Context, c *Chat) error {
	// "" is the unstamped row no workspace reads; a chat carries an agent transcript, so it
	// must be stamped.
	if strings.TrimSpace(c.WorkspaceID) == "" {
		return apperr.Invalid("chat.errors.workspace_required")
	}
	if c.ID == "" {
		c.ID = newID("chat-")
	}
	now := time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = c.CreatedAt
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO chats (`+chatCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, '', NULL)`),
		c.ID, c.WorkspaceID, c.AgentType, c.TaskID, c.ThreadID, c.Title, c.CreatedBy, c.Owner, c.SessionID,
		c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func scanChat(row interface{ Scan(...any) error }) (*Chat, error) {
	var c Chat
	var archived, delegatedUntil sql.NullTime
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.AgentType, &c.TaskID, &c.ThreadID, &c.Title, &c.CreatedBy,
		&c.Owner, &c.SessionID, &c.CreatedAt, &c.UpdatedAt, &archived, &c.DelegatedBy, &delegatedUntil)
	if err != nil {
		return nil, err
	}
	if archived.Valid {
		t := archived.Time
		c.ArchivedAt = &t
	}
	if delegatedUntil.Valid {
		t := delegatedUntil.Time
		c.DelegatedUntil = &t
	}
	return &c, nil
}

func (s *Store) GetChat(ctx context.Context, id string) (*Chat, error) {
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+chatCols+` FROM chats WHERE id = ?`), id)
	c, err := scanChat(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *Store) ListChats(ctx context.Context, workspaceID, taskID, agentType string, includeArchived bool) ([]Chat, error) {
	return s.listChats(ctx, workspaceID, taskID, agentType, includeArchived, "")
}

func (s *Store) listChats(
	ctx context.Context, workspaceID, taskID, agentType string, includeArchived bool, extra string, extraArgs ...any,
) ([]Chat, error) {
	var where []string
	var args []any
	if workspaceID != "" {
		where = append(where, "workspace_id = ?")
		args = append(args, workspaceID)
	}
	if taskID != "" {
		where = append(where, "task_id = ?")
		args = append(args, taskID)
	}
	if agentType != "" {
		where = append(where, "agent_type = ?")
		args = append(args, agentType)
	}
	if !includeArchived {
		where = append(where, "archived_at IS NULL") //i18n:ignore SQL condition
	}
	if extra != "" {
		where = append(where, extra)
		args = append(args, extraArgs...)
	}
	q := `SELECT ` + chatCols + ` FROM chats`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY updated_at DESC"
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) FindOrCreateDefault(ctx context.Context, workspaceID, agentType, title, createdBy string) (*Chat, error) {
	return s.FindOrCreate(ctx, &Chat{WorkspaceID: workspaceID, AgentType: agentType, Title: title, CreatedBy: createdBy})
}

func (s *Store) FindOrCreate(ctx context.Context, want *Chat) (*Chat, error) {
	c, err := s.FindLive(ctx, want)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	c = &Chat{
		WorkspaceID: want.WorkspaceID, AgentType: want.AgentType, TaskID: want.TaskID, ThreadID: want.ThreadID,
		Title: want.Title, CreatedBy: want.CreatedBy, Owner: want.Owner,
	}
	if err := s.CreateChat(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Matches only chats of want's kind: a person finds their own, the daemon its own. Crossing would
// hand one person's transcript to another, or put a private chat behind a task's mentions.
func (s *Store) FindLive(ctx context.Context, want *Chat) (*Chat, error) {
	who, arg := `owner = ? AND created_by <> ?`, []any{want.Owner, SystemCreator}
	if want.Personal() && want.Owner == "" {
		return nil, ErrNotFound
	}
	if !want.Personal() {
		who, arg = `created_by = ?`, []any{SystemCreator}
	}
	args := append([]any{want.WorkspaceID, want.AgentType, want.TaskID, want.ThreadID}, arg...)
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT `+chatCols+` FROM chats
		 WHERE workspace_id = ? AND agent_type = ? AND task_id = ? AND thread_id = ? AND archived_at IS NULL
		   AND `+who+`
		 ORDER BY updated_at DESC LIMIT 1`), args...)
	c, err := scanChat(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *Store) RenameChat(ctx context.Context, id, title string) error {
	return s.execOne(ctx, `UPDATE chats SET title = ?, updated_at = ? WHERE id = ?`, title, time.Now(), id)
}

func (s *Store) SetArchived(ctx context.Context, id string, archived bool) error {
	if archived {
		return s.execOne(ctx, `UPDATE chats SET archived_at = ?, updated_at = ? WHERE id = ?`, time.Now(), time.Now(), id)
	}
	return s.execOne(ctx, `UPDATE chats SET archived_at = NULL, updated_at = ? WHERE id = ?`, time.Now(), id)
}

func (s *Store) UpdateSessionID(ctx context.Context, id, sessionID string) error {
	return s.execOne(ctx, `UPDATE chats SET session_id = ?, updated_at = ? WHERE id = ?`, sessionID, time.Now(), id)
}

func (s *Store) DeleteChat(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM chats WHERE id = ?`, id)
}

func (s *Store) execOne(ctx context.Context, q string, args ...any) error {
	res, err := s.db.ExecContext(ctx, s.rb(q), args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

const msgCols = `id, chat_id, role, author, text, duration_ms, input_tokens, metadata, created_at`

func (s *Store) AddMessage(ctx context.Context, m *Message) (*Message, error) {
	if m.ID == "" {
		m.ID = newID("msg-")
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	meta := "{}"
	if len(m.Metadata) > 0 {
		b, err := json.Marshal(m.Metadata)
		if err != nil {
			return nil, apperr.Wrap(err, "chat.errors.marshal_metadata")
		}
		meta = string(b)
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO chat_messages_v2 (`+msgCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		m.ID, m.ChatID, string(m.Role), m.Author, m.Text, m.DurationMs, m.InputTokens, meta, m.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if _, bumpErr := s.db.ExecContext(ctx, s.rb(
		`UPDATE chats SET updated_at = ? WHERE id = ?`), m.CreatedAt, m.ChatID); bumpErr != nil {
		log.Print(i18n.Log("chat.log.bump_updated_at_failed", "chat", m.ChatID, "err", bumpErr))
	}
	return m, nil
}

func (s *Store) ListMessages(ctx context.Context, chatID string, limit int, beforeID string) ([]Message, error) {
	var cutoff *time.Time
	if beforeID != "" {
		row := s.db.QueryRowContext(ctx, s.rb(
			`SELECT created_at FROM chat_messages_v2 WHERE id = ? AND chat_id = ?`), beforeID, chatID)
		var t time.Time
		if err := row.Scan(&t); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		cutoff = &t
	}
	q := `SELECT ` + msgCols + ` FROM chat_messages_v2 WHERE chat_id = ?`
	args := []any{chatID}
	if cutoff != nil {
		q += ` AND created_at < ?`
		args = append(args, *cutoff)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var role, meta string
		if err := rows.Scan(&m.ID, &m.ChatID, &role, &m.Author, &m.Text, &m.DurationMs, &m.InputTokens, &meta, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Role = Role(role)
		if meta != "" && meta != "{}" {
			if err := json.Unmarshal([]byte(meta), &m.Metadata); err != nil {
				m.Metadata = map[string]string{"unparsed": meta}
			}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *Store) LastMessage(ctx context.Context, chatID string) (*Message, error) {
	msgs, err := s.ListMessages(ctx, chatID, 1, "")
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	return &msgs[0], nil
}

// Does not touch the engine session; chatruntime's ResetChat drops it.
func (s *Store) ClearMessages(ctx context.Context, chatID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM chat_messages_v2 WHERE chat_id = ?`), chatID)
	return err
}

func (s *Store) DeleteWorkspaceChats(ctx context.Context, workspaceID string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM chats WHERE workspace_id = ?`), workspaceID)
	return err
}
