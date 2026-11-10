//nolint:lll // inline SQL migration strings are inherently long
package task

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/listpage"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/patch"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type Stage = types.Stage

// Never taken from a request: the API derives it from the credential (auth.Author), the daemon
// stamps its own.
// Only SourceUser launches the agents a comment mentions.
type Source string

const (
	SourceUser       Source = "user"
	SourceAgent      Source = "agent"
	SourceAuditor    Source = "auditor"
	SourceSystem     Source = "system"
	SourceAutomation Source = "automation"
)

type RunStatus string

const (
	StatusIdle              RunStatus = "idle"
	StatusRunning           RunStatus = "running"
	StatusOutOfTurns        RunStatus = "out_of_turns"
	StatusFailed            RunStatus = "failed"
	StatusBlocked           RunStatus = "blocked"
	StatusNeedsMoreTurns    RunStatus = "needs_more_turns"
	StatusAwaitingInput     RunStatus = "awaiting_input"
	StatusAwaitingApproval  RunStatus = "awaiting_approval"
	StatusQueued            RunStatus = "queued"
	StatusNotStarted        RunStatus = "not_started"
	StatusCredentialBlocked           = RunStatus(EventCredentialBlocked)
	StatusNoAgent           RunStatus = "no_agent"
	StatusStopped           RunStatus = "stopped"
	StatusResumeFailed      RunStatus = "resume_failed"
)

func (s RunStatus) HasReason() bool {
	switch s {
	case StatusFailed, StatusBlocked, StatusNotStarted, StatusCredentialBlocked, StatusNoAgent, StatusResumeFailed,
		StatusOutOfTurns, StatusNeedsMoreTurns, StatusStopped, StatusQueued:
		return true
	default:
		return false
	}
}

// The board's "Needs you" filter (web/src/lib/board-view.ts) must list the same statuses.
func (s RunStatus) NeedsPerson() bool {
	switch s {
	case StatusAwaitingInput, StatusAwaitingApproval:
		return true
	default:
		return s.Stalled()
	}
}

func (s RunStatus) Stalled() bool { return slices.Contains(stalledStatuses, s) }

var stalledStatuses = []RunStatus{
	StatusFailed, StatusOutOfTurns, StatusNeedsMoreTurns, StatusBlocked,
	StatusNotStarted, StatusCredentialBlocked, StatusNoAgent, StatusResumeFailed,
}

// These carry a reason (auditor verdict, answers awaiting resume, turns spent) a failed relaunch
// would bury;
// the others are already start failures the newer one replaces.
func (s RunStatus) keptByRelaunch() bool {
	switch s {
	case StatusBlocked, StatusNeedsMoreTurns, StatusOutOfTurns, StatusResumeFailed:
		return true
	default:
		return false
	}
}

type OrphanAgent struct {
	ContainerID string    `json:"container_id"`
	Name        string    `json:"name"`
	Agent       string    `json:"agent,omitempty"`
	Stage       string    `json:"stage,omitempty"`
	StartedAt   time.Time `json:"started_at,omitzero"`
}

type Task struct {
	ID         string   `json:"id"`
	ProjectIDs []string `json:"project_ids"`
	// One level deep: a subtask cannot have children. Never reparented. Subtasks share the parent's
	// sandbox (SandboxRootID).
	ParentID string `json:"parent_id,omitempty"`
	// Lets a run edit only the tasks it created (api.runMayChangeTask). Stamped from the key's
	// scope at creation,
	// never from the request body, never changed afterwards.
	OriginTaskID string `json:"origin_task_id,omitempty"`
	Title        string `json:"title"`
	// Empty on tasks from card-shaped lists (taskCardCols); only Get, GetRow, ListByWorkspace and
	// ListChildren carry it.
	Body             string            `json:"body"`
	Stage            Stage             `json:"stage"`
	StageAssignments map[Stage]string  `json:"stage_assignments"`
	BranchName       string            `json:"branch_name,omitempty"`
	WorktreePaths    map[string]string `json:"worktree_paths,omitempty"`
	PRURLs           map[string]string `json:"pr_urls,omitempty"`
	CurrentRunID     string            `json:"current_run_id,omitempty"`
	CurrentInstance  string            `json:"current_instance,omitempty"`
	Status           RunStatus         `json:"status,omitempty"`
	Tags             []string          `json:"tags,omitempty"`
	Priority         string            `json:"priority,omitempty"`
	// Empty on rows with no workspace; API handlers show "" only to unrestricted callers.
	WorkspaceID string `json:"workspace_id,omitempty"`
	BoardID     string `json:"board_id,omitempty"`
	TypeID      string `json:"type_id,omitempty"`
	// A session is resumable only by the same agent on the same stage. SessionID is an
	// engine-internal handle, never serialized.
	SessionID    string    `json:"-"`
	SessionAgent string    `json:"session_agent,omitempty"`
	SessionStage string    `json:"session_stage,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	Comments    []Comment    `json:"comments,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Flags       []Flag       `json:"flags,omitempty"`
	// Flags carries the same rows in the shape clients of the flags alias read.
	Approvals        []Approval `json:"approvals,omitempty"`
	PendingApprovals int        `json:"pending_approvals,omitempty"`
	StatusReason     string     `json:"status_reason,omitempty"`
	OpenQuestions    int        `json:"open_questions,omitempty"`
	TestCases        []TestCase `json:"test_cases,omitempty"`
	Questions        []Question `json:"questions,omitempty"`

	Children []Task `json:"children,omitempty"`
	// Set only by the API on Get (costs a Docker/Kubernetes call); false on every task a list
	// returns, running or not.
	RunAlive     bool          `json:"run_alive,omitempty"`
	CanSteer     bool          `json:"can_steer,omitempty"`
	OrphanAgents []OrphanAgent `json:"orphan_agents,omitempty"`

	ChildCount int `json:"child_count,omitempty"`

	SortOrder int `json:"sort_order,omitempty"`

	ArchivedAt *time.Time `json:"archived_at,omitempty"`

	// Derived from task_dependencies on every read, never persisted. Lists fill only the count;
	// BlockedBy comes from Get.
	IsBlocked      bool     `json:"is_blocked,omitempty"`
	BlockedByCount int      `json:"blocked_by_count,omitempty"`
	BlockedBy      []DepRef `json:"blocked_by,omitempty"`
	Blocking       []DepRef `json:"blocking,omitempty"`
}

func (t *Task) IsSubtask() bool { return t.ParentID != "" }

// Every sandbox acquire, worktree mount and group teardown must key off this id so subtasks share
// one `octopus-sb-<root>` container and working copy.
func (t *Task) SandboxRootID() string {
	if t.ParentID != "" {
		return t.ParentID
	}
	return t.ID
}

type Comment struct {
	ID         string            `json:"id"`
	TaskID     string            `json:"task_id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Source     Source            `json:"source"`
	Author     string            `json:"author,omitempty"`
	Body       string            `json:"body"`
	Stage      Stage             `json:"stage,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	ResolvedAt *time.Time        `json:"resolved_at,omitempty"`
	ResolvedBy string            `json:"resolved_by,omitempty"`
	Key        string            `json:"key,omitempty"`
	BodyI18n   map[string]string `json:"body_i18n,omitempty"`
}

type Attachment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	CommentID string    `json:"comment_id,omitempty"`
	Author    string    `json:"author,omitempty"`
	Filename  string    `json:"filename"`
	MimeType  string    `json:"mime_type,omitempty"`
	Size      int64     `json:"size"`
	Path      string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

type Report struct {
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	Source     Source    `json:"source"`
	Author     string    `json:"author,omitempty"`
	Stage      Stage     `json:"stage,omitempty"`
	InstanceID string    `json:"instance_id,omitempty"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title,omitempty"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

// auditor_feedback is read back: the dispatcher acts on its verdict frontmatter when the run had no
// shared output dir.
const (
	ReportKindAgentIndex      = "agent_index"
	ReportKindAuditorFeedback = "auditor_feedback"
	ReportKindEvidence        = "evidence"
)

type Patch struct {
	Title            *string           `json:"title,omitempty"`
	Body             *string           `json:"body,omitempty"`
	Stage            *Stage            `json:"stage,omitempty"`
	StageAssignments map[Stage]string  `json:"stage_assignments,omitempty"`
	ProjectIDs       *[]string         `json:"project_ids,omitempty"`
	BranchName       *string           `json:"branch_name,omitempty"`
	WorktreePaths    map[string]string `json:"worktree_paths,omitempty"`
	PRURLs           map[string]string `json:"pr_urls,omitempty"`
	CurrentRunID     *string           `json:"current_run_id,omitempty"`
	CurrentInstance  *string           `json:"current_instance,omitempty"`
	Status           *RunStatus        `json:"status,omitempty"`
	Tags             *[]string         `json:"tags,omitempty"`
	Priority         *string           `json:"priority,omitempty"`
	// The store does not check the task's stage exists on the destination board; the API handler
	// must refuse such a move.
	BoardID *string `json:"board_id,omitempty"`
	// As with BoardID, the handler must refuse a change that leaves the task in a stage the new
	// type skips.
	TypeID       *string `json:"type_id,omitempty"`
	SessionID    *string `json:"-"`
	SessionAgent *string `json:"-"`
	SessionStage *string `json:"-"`
	Archived     *bool   `json:"archived,omitempty"`
}

type Store struct {
	db                 *sql.DB
	attachmentsDir     string
	boards             stagepkg.Boards
	changes            *ChangeBus
	onApproval         func(ctx context.Context, a Approval)
	onQuestionCanceled func(ctx context.Context, q Question)
	storage            *storageQuota
}

func (s *Store) WithStages(r stagepkg.Registry) *Store {
	if r != nil {
		s.boards = stagepkg.SingleBoard(r)
	}
	return s
}

func (s *Store) WithBoards(b stagepkg.Boards) *Store {
	if b != nil {
		s.boards = b
	}
	return s
}

func (s *Store) board(id string) stagepkg.Registry {
	if id == "" {
		id = s.boards.DefaultBoardID()
	}
	return s.boards.Board(id)
}

func (s *Store) boardFor(t *Task) stagepkg.Registry {
	if t == nil {
		return s.board("")
	}
	id := t.BoardID
	if id == "" {
		id = s.boards.DefaultBoardIDFor(t.WorkspaceID)
	}
	return s.boards.BoardFor(id, t.TypeID)
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

var taskSchema = []string{
	`CREATE TABLE IF NOT EXISTS tasks (
			id                 TEXT PRIMARY KEY,
			title              TEXT NOT NULL,
			body               TEXT NOT NULL DEFAULT '',
			stage              TEXT NOT NULL,
			stage_assignments  TEXT NOT NULL DEFAULT '{}',
			branch_name        TEXT NOT NULL DEFAULT '',
			worktree_paths     TEXT NOT NULL DEFAULT '{}',
			pr_urls            TEXT NOT NULL DEFAULT '{}',
			current_run_id     TEXT NOT NULL DEFAULT '',
			current_instance   TEXT NOT NULL DEFAULT '',
			tags               TEXT NOT NULL DEFAULT '[]',
			priority           TEXT NOT NULL DEFAULT '',
			workspace_id       TEXT NOT NULL DEFAULT '',
			board_id           TEXT NOT NULL DEFAULT 'default',
			type_id            TEXT NOT NULL DEFAULT '',
			parent_id          TEXT NOT NULL DEFAULT '',
			created_at         TIMESTAMP NOT NULL,
			updated_at         TIMESTAMP NOT NULL
		)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_stage   ON tasks(stage)`,
	// Index only columns this CREATE TABLE guarantees: on existing databases it is a no-op, so a
	// later-added column
	// would not exist yet and boot fails. Those get their index from their own migration (11, 13,
	// 15, 17).

	`CREATE TABLE IF NOT EXISTS task_projects (
			task_id    TEXT NOT NULL,
			project_id TEXT NOT NULL,
			position   INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (task_id, project_id),
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
		)`,
	`CREATE INDEX IF NOT EXISTS idx_task_projects_project ON task_projects(project_id)`,
	// Link order matters: the first project defining a stage wins when templates are merged.
	`CREATE INDEX IF NOT EXISTS idx_task_projects_task ON task_projects(task_id, position)`,

	`CREATE TABLE IF NOT EXISTS task_comments (
			id         TEXT PRIMARY KEY,
			task_id    TEXT NOT NULL,
			source     TEXT NOT NULL,
			author     TEXT NOT NULL DEFAULT '',
			body       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
	`CREATE INDEX IF NOT EXISTS idx_comments_task ON task_comments(task_id, created_at)`,

	`CREATE TABLE IF NOT EXISTS task_attachments (
			id         TEXT PRIMARY KEY,
			task_id    TEXT NOT NULL,
			comment_id TEXT NOT NULL DEFAULT '',
			author     TEXT NOT NULL DEFAULT '',
			filename   TEXT NOT NULL,
			mime_type  TEXT NOT NULL DEFAULT '',
			size       INTEGER NOT NULL DEFAULT 0,
			path       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
	`CREATE INDEX IF NOT EXISTS idx_attachments_task ON task_attachments(task_id, created_at)`,

	`CREATE TABLE IF NOT EXISTS task_reports (
			id          TEXT PRIMARY KEY,
			task_id     TEXT NOT NULL,
			source      TEXT NOT NULL,
			author      TEXT NOT NULL DEFAULT '',
			stage       TEXT NOT NULL DEFAULT '',
			instance_id TEXT NOT NULL DEFAULT '',
			kind        TEXT NOT NULL,
			title       TEXT NOT NULL DEFAULT '',
			body        TEXT NOT NULL,
			created_at  TIMESTAMP NOT NULL
		)`,
	`CREATE INDEX IF NOT EXISTS idx_reports_task ON task_reports(task_id, created_at)`,

	`CREATE TABLE IF NOT EXISTS task_flags (
			id            TEXT PRIMARY KEY,
			task_id       TEXT NOT NULL,
			kind          TEXT NOT NULL,
			label         TEXT NOT NULL,
			status        TEXT NOT NULL,
			payload       TEXT NOT NULL DEFAULT '{}',
			created_by    TEXT NOT NULL DEFAULT 'agent',
			resolved_by   TEXT NOT NULL DEFAULT '',
			resolved_note TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMP NOT NULL,
			resolved_at   TIMESTAMP
		)`,
	`CREATE INDEX IF NOT EXISTS idx_flags_task ON task_flags(task_id, created_at)`,

	`CREATE TABLE IF NOT EXISTS workflow_events (
			id         TEXT PRIMARY KEY,
			task_id    TEXT NOT NULL,
			kind       TEXT NOT NULL,
			ref_id     TEXT NOT NULL DEFAULT '',
			actor      TEXT NOT NULL DEFAULT '',
			summary    TEXT NOT NULL DEFAULT '',
			payload    TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL
		)`,
	`CREATE INDEX IF NOT EXISTS idx_events_task ON workflow_events(task_id, created_at)`,

	`CREATE TABLE IF NOT EXISTS task_dependencies (
			task_id            TEXT NOT NULL,
			depends_on_task_id TEXT NOT NULL,
			kind               TEXT NOT NULL CHECK(kind IN ('hard','soft')),
			created_at         TIMESTAMP NOT NULL,
			PRIMARY KEY (task_id, depends_on_task_id),
			FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
			FOREIGN KEY (depends_on_task_id) REFERENCES tasks(id) ON DELETE CASCADE
		)`,
	`CREATE INDEX IF NOT EXISTS idx_task_deps_reverse ON task_dependencies(depends_on_task_id)`,
}

// Outside the migration list, so schema fingerprinting (internal/schema/schematest) must read this
// too.
func BaseSchema() []string { return slices.Clone(taskSchema) }

func NewStore(db *sql.DB, attachmentsDir string) (*Store, error) {
	for _, stmt := range taskSchema {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			return nil, apperr.Wrap(err, "task.errors.schema")
		}
	}
	if err := applyTaskMigrations(context.Background(), db); err != nil {
		return nil, err
	}
	s := &Store{db: db, attachmentsDir: attachmentsDir, boards: stagepkg.StaticBoards()}
	s.syncFlagsBack(context.Background())
	return s, nil
}

// Never change: the name keys the migration history, so another one would rerun every migration.
const MigrationComponent = "task"

// Every guard in Migrations is evaluated before the first migration runs, against the schema as it
// stood at boot.
func applyTaskMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := Migrations(migrate.DBInspector(ctx, db))
	if err == nil {
		err = migrate.Run(ctx, db, MigrationComponent, migrations)
	}
	if err != nil {
		return apperr.Wrap(err, "task.errors.migrations")
	}
	return nil
}

// A published migration never changes. internal/schema passes migrate.Everything to read every
// migration for the lock and lint.
func Migrations(in migrate.Inspector) ([]migrate.Migration, error) {
	migrations := []migrate.Migration{
		{ID: 1, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS project_ids TEXT NOT NULL DEFAULT '[]'`},
		{ID: 2, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS priority TEXT NOT NULL DEFAULT ''`},
		{ID: 3, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS worktree_paths TEXT NOT NULL DEFAULT '{}'`},
		{ID: 4, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS pr_urls TEXT NOT NULL DEFAULT '{}'`},
		{ID: 5, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'idle'`},
		{ID: 6, SQL: `ALTER TABLE task_comments ADD COLUMN IF NOT EXISTS stage TEXT NOT NULL DEFAULT ''`},
	}

	// These reference the singular columns, which only parse on databases that still have
	// them.
	legacy, colErr := in.ColumnExists("tasks", "project_id")
	if colErr != nil {
		return nil, colErr
	}
	if legacy {
		migrations = append(migrations,
			migrate.Migration{ID: 7, SQL: `UPDATE tasks SET project_ids = json_array(project_id) WHERE project_id IS NOT NULL AND project_id != '' AND project_ids = '[]'`},
			migrate.Migration{ID: 8, SQL: `UPDATE tasks SET worktree_paths = json_object(project_id, worktree_path) WHERE project_id IS NOT NULL AND project_id != '' AND worktree_path != '' AND worktree_paths = '{}'`},
			migrate.Migration{ID: 9, SQL: `UPDATE tasks SET pr_urls = json_object(project_id, pr_url) WHERE project_id IS NOT NULL AND project_id != '' AND pr_url != '' AND pr_urls = '{}'`},
		)
	}

	migrations = append(migrations,
		migrate.Migration{ID: 10, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT ''`},
		migrate.Migration{ID: 11, SQL: `CREATE INDEX IF NOT EXISTS idx_tasks_workspace ON tasks(workspace_id)`},
		migrate.Migration{ID: 12, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS parent_id TEXT NOT NULL DEFAULT ''`},
		migrate.Migration{ID: 13, SQL: `CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_id)`},
		// 'default' must match the board the stage migration builds from existing stages.
		migrate.Migration{ID: 14, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS board_id TEXT NOT NULL DEFAULT 'default'`},
		migrate.Migration{ID: 15, SQL: `CREATE INDEX IF NOT EXISTS idx_tasks_board ON tasks(board_id, stage)`},
		migrate.Migration{ID: 16, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS type_id TEXT NOT NULL DEFAULT ''`},
		migrate.Migration{ID: 17, SQL: `CREATE INDEX IF NOT EXISTS idx_tasks_type ON tasks(board_id, type_id)`},
	)

	linked, linkErr := in.ColumnExists("tasks", "project_ids")
	if linkErr != nil {
		return nil, linkErr
	}
	if linked {
		migrations = append(migrations,
			migrate.Migration{ID: 18, SQL: `INSERT INTO task_projects (task_id, project_id, position)
				SELECT t.id, e.pid, e.ord
				  FROM tasks t
				  CROSS JOIN LATERAL jsonb_array_elements_text(t.project_ids::jsonb) WITH ORDINALITY AS e(pid, ord)
				 WHERE t.project_ids <> '' AND t.project_ids <> '[]'
				ON CONFLICT DO NOTHING`},
			migrate.Migration{ID: 19, SQL: `ALTER TABLE tasks DROP COLUMN IF EXISTS project_ids`},
		)
	}

	// Tasks with no owned project stay unstamped: guessing would move someone's task into a
	// workspace they don't log into.
	// Guarded on projects.workspace_id: a store brought up alone (tests, tools) has nothing to read
	// it from.
	owned, ownErr := in.ColumnExists("projects", "workspace_id")
	if ownErr != nil {
		return nil, ownErr
	}
	if owned {
		migrations = append(migrations,
			migrate.Migration{ID: 20, SQL: `
			UPDATE tasks SET workspace_id = COALESCE((
			    SELECT p.workspace_id
			      FROM task_projects tp
			      JOIN projects p ON p.id = tp.project_id
			     WHERE tp.task_id = tasks.id AND p.workspace_id <> ''
			     ORDER BY tp.position
			     LIMIT 1), '')
			 WHERE workspace_id = ''`},
		)
	}

	migrations = append(migrations,
		migrate.Migration{ID: 21, SQL: `ALTER TABLE task_attachments ADD COLUMN IF NOT EXISTS comment_id TEXT NOT NULL DEFAULT ''`},
		migrate.Migration{ID: 22, SQL: `ALTER TABLE task_attachments ADD COLUMN IF NOT EXISTS author TEXT NOT NULL DEFAULT ''`},
		migrate.Migration{ID: 23, SQL: `CREATE INDEX IF NOT EXISTS idx_attachments_comment ON task_attachments(comment_id)`},

		migrate.Migration{ID: 24, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS session_id TEXT NOT NULL DEFAULT ''`},
		migrate.Migration{ID: 25, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS session_agent TEXT NOT NULL DEFAULT ''`},
		migrate.Migration{ID: 26, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS session_stage TEXT NOT NULL DEFAULT ''`},

		migrate.Migration{ID: 27, SQL: `CREATE TABLE IF NOT EXISTS task_messages (
			id          TEXT PRIMARY KEY,
			task_id     TEXT NOT NULL,
			run_id      TEXT NOT NULL DEFAULT '',
			chat_id     TEXT NOT NULL DEFAULT '',
			stage       TEXT NOT NULL DEFAULT '',
			agent       TEXT NOT NULL DEFAULT '',
			role        TEXT NOT NULL,
			author      TEXT NOT NULL DEFAULT '',
			body        TEXT NOT NULL,
			delivery    TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ NOT NULL
		)`},
		migrate.Migration{ID: 28, SQL: `CREATE INDEX IF NOT EXISTS idx_task_messages_task ON task_messages(task_id, created_at)`},

		// Ascending on purpose: Postgres scans a btree backwards at the same cost, so one index
		// serves both directions.
		migrate.Migration{ID: 29, SQL: `CREATE INDEX IF NOT EXISTS idx_tasks_board_updated ON tasks(board_id, updated_at, id)`},
		migrate.Migration{ID: 30, SQL: `CREATE INDEX IF NOT EXISTS idx_tasks_updated ON tasks(updated_at, id)`},

		// Existing rows keep '' (not created by a run), so no run can claim them.
		migrate.Migration{ID: 31, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS origin_task_id TEXT NOT NULL DEFAULT ''`},
	)
	migrations = append(migrations, testCaseMigrations...)
	migrations = append(migrations,
		migrate.Migration{ID: 37, SQL: `ALTER TABLE task_comments ADD COLUMN IF NOT EXISTS parent_id TEXT NOT NULL DEFAULT ''`},
		migrate.Migration{ID: 38, SQL: `CREATE INDEX IF NOT EXISTS idx_comments_thread ON task_comments(task_id, parent_id, created_at)`},
		migrate.Migration{ID: 39, SQL: `ALTER TABLE task_comments
			ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS resolved_by TEXT NOT NULL DEFAULT ''`},
		migrate.Migration{ID: 40, SQL: `ALTER TABLE test_case_runs ADD COLUMN IF NOT EXISTS report_id TEXT NOT NULL DEFAULT ''`},
	)
	migrations = append(migrations, questionMigrations...)
	migrations = append(migrations, approvalMigrations...)
	migrations = append(migrations,
		migrate.Migration{ID: 46, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS sort_order INTEGER NOT NULL DEFAULT 0`},
		migrate.Migration{ID: 47, SQL: `ALTER TABLE task_comments
			ADD COLUMN IF NOT EXISTS note_key TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS body_i18n TEXT NOT NULL DEFAULT ''`},
		migrate.Migration{ID: 48, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ`},
		migrate.Migration{ID: 49, SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS test_case_seq INTEGER NOT NULL DEFAULT 0`},
		// triggered_by decides whose own engine login a run may use; ListEvents must leave it out
		// so it never reaches an export bundle.
		migrate.Migration{ID: 50, SQL: `ALTER TABLE workflow_events ADD COLUMN IF NOT EXISTS triggered_by TEXT NOT NULL DEFAULT ''`},
	)
	return migrations, nil
}

// IDs share the task store's single migration sequence with the inline list in Migrations.
var testCaseMigrations = []migrate.Migration{
	{ID: 32, SQL: `CREATE TABLE IF NOT EXISTS test_cases (
			id          TEXT PRIMARY KEY,
			task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			key         TEXT NOT NULL,
			title       TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			steps       TEXT NOT NULL DEFAULT '',
			expected    TEXT NOT NULL DEFAULT '',
			priority    TEXT NOT NULL DEFAULT 'medium',
			status      TEXT NOT NULL DEFAULT 'pending',
			covered     BOOLEAN NOT NULL DEFAULT FALSE,
			position    INTEGER NOT NULL DEFAULT 0,
			created_by  TEXT NOT NULL DEFAULT '',
			updated_by  TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ NOT NULL,
			updated_at  TIMESTAMPTZ NOT NULL,
			UNIQUE (task_id, key)
		)`},
	{ID: 33, SQL: `CREATE INDEX IF NOT EXISTS idx_test_cases_task ON test_cases(task_id, position)`},
	{ID: 34, SQL: `CREATE TABLE IF NOT EXISTS test_case_runs (
			id                     TEXT PRIMARY KEY,
			test_case_id           TEXT NOT NULL REFERENCES test_cases(id) ON DELETE CASCADE,
			task_id                TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			verdict                TEXT NOT NULL,
			observed               TEXT NOT NULL DEFAULT '',
			note                   TEXT NOT NULL DEFAULT '',
			video_attachment_id    TEXT NOT NULL DEFAULT '',
			take_attachment_id     TEXT NOT NULL DEFAULT '',
			comment_id             TEXT NOT NULL DEFAULT '',
			capture_attachment_ids JSONB NOT NULL DEFAULT '[]',
			author                 TEXT NOT NULL DEFAULT '',
			ran_at                 TIMESTAMPTZ NOT NULL
		)`},
	{ID: 35, SQL: `CREATE INDEX IF NOT EXISTS idx_test_case_runs_case ON test_case_runs(test_case_id, ran_at DESC)`},
	{ID: 36, SQL: `CREATE INDEX IF NOT EXISTS idx_test_case_runs_task ON test_case_runs(task_id)`},
}

func (s *Store) DB() *sql.DB            { return s.db }
func (s *Store) AttachmentsDir() string { return s.attachmentsDir }

func generateID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", apperr.Wrap(err, "task.errors.generate_id")
	}
	return hex.EncodeToString(b), nil
}

func mustGenerateID() string {
	id, err := generateID()
	if err != nil {
		panic(err)
	}
	return id
}

type taskJSON struct {
	tags, assigns, wt, pr string
}

func (t *Task) marshalJSONCols() (taskJSON, error) {
	var out taskJSON
	for _, m := range []struct {
		src  any
		dst  *string
		name string
	}{
		{t.Tags, &out.tags, "tags"},
		{t.StageAssignments, &out.assigns, "stage_assignments"},
		{t.WorktreePaths, &out.wt, "worktree_paths"},
		{t.PRURLs, &out.pr, "pr_urls"},
	} {
		b, err := json.Marshal(m.src)
		if err != nil {
			return taskJSON{}, apperr.Wrap(err, "task.errors.marshal_column", "column", m.name)
		}
		*m.dst = string(b)
	}
	return out, nil
}

func dedupeStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func (s *Store) writeProjectLinks(ctx context.Context, ex execer, taskID string, projectIDs []string) error {
	if _, err := ex.ExecContext(ctx, s.rb(`DELETE FROM task_projects WHERE task_id = ?`), taskID); err != nil {
		return apperr.Wrap(err, "task.errors.clear_project_links", "task", taskID)
	}
	for i, pid := range projectIDs {
		// 1-based to match migration 18's WITH ORDINALITY backfill.
		if _, err := ex.ExecContext(ctx, s.rb(
			`INSERT INTO task_projects (task_id, project_id, position) VALUES (?,?,?)`),
			taskID, pid, i+1,
		); err != nil {
			return apperr.Wrap(err, "task.errors.link_project", "task", taskID, "project", pid)
		}
	}
	return nil
}

// Never nil: project_ids is serialized to the dashboard, where null and [] differ.
func (s *Store) loadProjectLinks(ctx context.Context, q rowsQueryer, taskID string) ([]string, error) {
	ids, err := dbpkg.CollectStrings(ctx, q,
		`SELECT project_id FROM task_projects WHERE task_id = ? ORDER BY position ASC, project_id ASC`, taskID)
	if ids == nil {
		ids = []string{}
	}
	return ids, err
}

func (s *Store) decorateProjectLinks(ctx context.Context, out []Task) error {
	if len(out) == 0 {
		return nil
	}
	args := make([]any, len(out))
	for i := range out {
		args[i] = out[i].ID
		out[i].ProjectIDs = []string{}
	}
	q := `SELECT task_id, project_id FROM task_projects WHERE task_id IN (` +
		placeholderList(len(args)) + `)` + ` ORDER BY position ASC, project_id ASC`
	rows, err := s.db.QueryContext(ctx, s.rb(q), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	byTask := make(map[string][]string, len(out))
	for rows.Next() {
		var taskID, projectID string
		if err := rows.Scan(&taskID, &projectID); err != nil {
			return err
		}
		byTask[taskID] = append(byTask[taskID], projectID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range out {
		if ids, ok := byTask[out[i].ID]; ok {
			out[i].ProjectIDs = ids
		}
	}
	return nil
}

func (s *Store) UnlinkProject(ctx context.Context, projectID string) (int, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM task_projects WHERE project_id = ?`), projectID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (s *Store) ListByProject(ctx context.Context, projectID string) ([]Task, error) {
	return s.listWhere(ctx,
		`WHERE id IN (SELECT task_id FROM task_projects WHERE project_id = ?) ORDER BY updated_at DESC`,
		projectID)
}

// Empty WorkspaceID is the unstamped row every tenant sees and the Kubernetes launcher turns into
// namespace "tenant-";
// refusing here keeps any new caller from skipping it.
func (s *Store) Create(ctx context.Context, t *Task) error {
	if t.WorkspaceID == "" {
		return apperr.Invalid("task.errors.workspace_required")
	}
	if t.ID == "" {
		id, idErr := generateID()
		if idErr != nil {
			return idErr
		}
		t.ID = id
	}
	if t.BoardID == "" {
		// Use the task's workspace, not the install: the instance default board is another tenant's
		// flow.
		t.BoardID = s.boards.DefaultBoardIDFor(t.WorkspaceID)
	}
	if t.Stage == "" {
		t.Stage = s.boardFor(t).Initial()
	}
	if t.StageAssignments == nil {
		t.StageAssignments = map[Stage]string{}
	}
	t.ProjectIDs = dedupeStrings(t.ProjectIDs)
	if t.WorktreePaths == nil {
		t.WorktreePaths = map[string]string{}
	}
	if t.PRURLs == nil {
		t.PRURLs = map[string]string{}
	}
	if t.Status == "" {
		t.Status = StatusIdle
	}
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now

	cols, err := t.marshalJSONCols()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "task.errors.create_begin")
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, s.rb(
		`INSERT INTO tasks (id, title, body, stage, stage_assignments,
		    branch_name, worktree_paths, pr_urls, current_run_id, current_instance,
		    status, tags, priority, workspace_id, board_id, type_id, parent_id, origin_task_id,
		    created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		t.ID, t.Title, t.Body, t.Stage, cols.assigns,
		t.BranchName, cols.wt, cols.pr, t.CurrentRunID, t.CurrentInstance,
		string(t.Status), cols.tags, t.Priority, t.WorkspaceID, t.BoardID, t.TypeID, t.ParentID,
		t.OriginTaskID, t.CreatedAt, t.UpdatedAt,
	); err != nil {
		return err
	}
	if linkErr := s.writeProjectLinks(ctx, tx, t.ID, t.ProjectIDs); linkErr != nil {
		return linkErr
	}
	if err = tx.Commit(); err != nil {
		return apperr.Wrap(err, "task.errors.create_commit")
	}
	s.stageEntered(ctx, t)
	s.notify(ChangeCreated, t)
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (*Task, error) {
	t, err := s.getRaw(ctx, id)
	if err != nil {
		return nil, err
	}
	var err2 error
	t.Comments, err2 = s.ListComments(ctx, id)
	logx.Warn(err2, "task.log.get_comments", "id", id)
	t.Attachments, err2 = s.ListAttachments(ctx, id)
	logx.Warn(err2, "task.log.get_attachments", "id", id)
	t.Approvals, err2 = s.ListApprovals(ctx, id)
	logx.Warn(err2, "task.log.get_flags", "id", id)
	t.Flags = FlagsOf(t.Approvals)
	t.TestCases, err2 = s.ListTestCases(ctx, id)
	logx.Warn(err2, "task.log.get_test_cases", "id", id)
	t.Questions, err2 = s.ListQuestions(ctx, id)
	logx.Warn(err2, "task.log.get_questions", "id", id)
	t.BlockedBy, err2 = s.GetDependencies(ctx, id)
	logx.Warn(err2, "task.log.get_dependencies", "id", id)
	t.Blocking, err2 = s.GetDependents(ctx, id)
	logx.Warn(err2, "task.log.get_dependents", "id", id)
	blocked, blockers, err2 := s.IsBlocked(ctx, id)
	logx.Warn(err2, "task.log.get_is_blocked", "id", id)
	t.IsBlocked = blocked
	t.BlockedByCount = len(blockers)
	one := []Task{*t}
	s.decorateStatusReasons(ctx, one)
	t.StatusReason = one[0].StatusReason
	return t, nil
}

func (s *Store) GetRow(ctx context.Context, id string) (*Task, error) { return s.getRaw(ctx, id) }

func (s *Store) Titles(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT id, title FROM tasks WHERE id IN (`+placeholderList(len(ids))+`)`), args...) //i18n:ignore SQL
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.list_titles")
	}
	defer rows.Close()
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, apperr.Wrap(err, "task.errors.list_titles")
		}
		out[id] = title
	}
	return out, rows.Err()
}

// Column order must match scanTask's Scan destinations.
const taskCols = `id, title, body, stage, stage_assignments,
	        branch_name, worktree_paths, pr_urls, current_run_id, current_instance,
	        status, tags, priority, workspace_id, board_id, type_id, parent_id,
	        origin_task_id, session_id, session_agent, session_stage, created_at, updated_at,
	        sort_order, archived_at`

// Substitutes ” for body so scanTask stays aligned with one column list. Never use for
// ListByWorkspace
// (export re-imports whole rows; a blank body would erase descriptions) or ListChildren (detail
// payload).
var taskCardCols = strings.Replace(taskCols, ", body,", ", '' AS body,", 1)

var ErrNotFound error = apperr.NotFound("task.errors.not_found")

var ErrConflict error = apperr.Conflict("task.errors.conflict")

var ErrInvalid error = apperr.Invalid("task.errors.invalid")

func scanTask(r rowScanner) (*Task, error) {
	t := &Task{}
	var tagsJSON, assignsJSON, wtJSON, prJSON, status string
	var archived sql.NullTime
	if err := r.Scan(&t.ID, &t.Title, &t.Body, &t.Stage, &assignsJSON,
		&t.BranchName, &wtJSON, &prJSON, &t.CurrentRunID, &t.CurrentInstance,
		&status, &tagsJSON, &t.Priority, &t.WorkspaceID, &t.BoardID, &t.TypeID, &t.ParentID,
		&t.OriginTaskID, &t.SessionID, &t.SessionAgent, &t.SessionStage,
		&t.CreatedAt, &t.UpdatedAt, &t.SortOrder, &archived); err != nil {
		return nil, apperr.Wrap(err, "task.errors.scan_row")
	}
	t.Status = RunStatus(status)
	if t.Status == "" {
		t.Status = StatusIdle
	}
	if archived.Valid {
		at := archived.Time.UTC()
		t.ArchivedAt = &at
	}
	for _, u := range []struct {
		raw  string
		dst  any
		name string
	}{
		{tagsJSON, &t.Tags, "tags"},
		{assignsJSON, &t.StageAssignments, "stage_assignments"},
		{wtJSON, &t.WorktreePaths, "worktree_paths"},
		{prJSON, &t.PRURLs, "pr_urls"},
	} {
		if err := json.Unmarshal([]byte(u.raw), u.dst); err != nil {
			return nil, apperr.Wrap(err, "task.errors.unmarshal_column", "column", u.name, "task", t.ID)
		}
	}
	if t.StageAssignments == nil {
		t.StageAssignments = map[Stage]string{}
	}
	t.ProjectIDs = []string{}
	if t.WorktreePaths == nil {
		t.WorktreePaths = map[string]string{}
	}
	if t.PRURLs == nil {
		t.PRURLs = map[string]string{}
	}
	return t, nil
}

type (
	rowQueryer interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	}
	rowsQueryer interface {
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	}
	execer interface {
		ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	}
)

func (s *Store) getRaw(ctx context.Context, id string) (*Task, error) {
	return s.getRawFrom(ctx, s.db, id, false)
}

func (s *Store) getRawTx(ctx context.Context, tx *sql.Tx, id string) (*Task, error) {
	return s.getRawFrom(ctx, tx, id, true)
}

func (s *Store) getRawFrom(ctx context.Context, q rowQueryer, id string, forUpdate bool) (*Task, error) {
	sel := `SELECT ` + taskCols + ` FROM tasks WHERE id = ?`
	if forUpdate {
		sel += ` FOR UPDATE`
	}
	t, err := scanTask(q.QueryRowContext(ctx, s.rb(sel), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.Wrap(ErrNotFound, "task.errors.task_ref", "id", id)
	}
	if err != nil {
		return nil, err
	}
	// Same handle as the row so a read inside a transaction sees its own writes.
	links, ok := q.(rowsQueryer)
	if !ok {
		links = s.db
	}
	t.ProjectIDs, err = s.loadProjectLinks(ctx, links, id)
	return t, err
}

// where must be a compile-time constant; values travel in args.
func (s *Store) listWhere(ctx context.Context, where string, args ...any) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT `+taskCols+` FROM tasks `+where), args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.collectTasks(ctx, rows)
}

// The id tie-break makes cursor paging exact: a batch stage move stamps several tasks with the same
// updated_at.
const cardOrder = ` ORDER BY updated_at DESC, id DESC`

// cond must be a compile-time constant; values travel in args.
func (s *Store) listCards(ctx context.Context, cond string, args []any, p listpage.Request) ([]Task, string, error) {
	conds, binds := []string{}, append([]any(nil), args...)
	if cond != "" {
		conds = append(conds, cond)
	}
	if p.Cursor != "" {
		key, err := listpage.Decode(p.Cursor)
		if err != nil {
			return nil, "", err
		}
		conds = append(conds, `(updated_at, id) < (?, ?)`) //i18n:ignore SQL fragment
		binds = append(binds, key.At, key.ID)
	}

	q := `SELECT ` + taskCardCols + ` FROM tasks`
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += cardOrder
	if p.Limit > 0 {
		q += ` LIMIT ?`
		binds = append(binds, p.Limit+1)
	}

	rows, err := s.db.QueryContext(ctx, s.rb(q), binds...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out, err := s.collectTasks(ctx, rows)
	if err != nil {
		return nil, "", err
	}
	if p.Limit <= 0 || len(out) <= p.Limit {
		return out, "", nil
	}
	out = out[:p.Limit]
	last := out[len(out)-1]
	return out, listpage.Encode(listpage.Key{At: last.UpdatedAt, ID: last.ID}), nil
}

func (s *Store) List(ctx context.Context) ([]Task, error) {
	return s.ListFilter(ctx, "")
}

// Keeps the full row, body included: the export bundle re-imports these tasks.
func (s *Store) ListByWorkspace(ctx context.Context, workspaceID string) ([]Task, error) {
	return s.listWhere(ctx, `WHERE workspace_id = ?`+cardOrder, workspaceID)
}

func (s *Store) ListFilter(ctx context.Context, stage string) ([]Task, error) {
	tasks, _, err := s.ListFilterPage(ctx, stage, listpage.Request{})
	return tasks, err
}

func (s *Store) ListFilterPage(ctx context.Context, stage string, p listpage.Request) ([]Task, string, error) {
	if stage == "" {
		return s.listCards(ctx, "", nil, p)
	}
	return s.listCards(ctx, `stage = ?`, []any{stage}, p)
}

type CardFilter struct {
	// "" means every workspace; only a trusted (daemon or unrestricted) caller may ask for it.
	Workspace string
	Board     string
	Stage     string
	TopLevel  bool
	Archived  bool
}

// Every filter must be in the SQL before the LIMIT: filtering afterwards let the cursor page into
// other tenants' rows.
func (s *Store) ListPage(ctx context.Context, f CardFilter, p listpage.Request) ([]Task, string, error) {
	var conds []string
	var args []any
	if f.Workspace != "" {
		conds, args = append(conds, `workspace_id = ?`), append(args, f.Workspace) //i18n:ignore SQL fragment
	}
	if f.Board != "" {
		conds, args = append(conds, `board_id = ?`), append(args, f.Board) //i18n:ignore SQL fragment
	} else if f.Stage != "" {
		conds, args = append(conds, `stage = ?`), append(args, f.Stage) //i18n:ignore SQL fragment
	}
	if f.TopLevel {
		conds = append(conds, `parent_id = ''`) //i18n:ignore SQL fragment
	}
	if f.Archived {
		conds = append(conds, `archived_at IS NOT NULL`) //i18n:ignore SQL fragment
	} else {
		conds = append(conds, `archived_at IS NULL`) //i18n:ignore SQL fragment
	}
	return s.listCards(ctx, strings.Join(conds, ` AND `), args, p)
}

func (s *Store) ListByBoard(ctx context.Context, boardID string) ([]Task, error) {
	tasks, _, err := s.ListByBoardPage(ctx, boardID, listpage.Request{})
	return tasks, err
}

func (s *Store) ListByBoardPage(ctx context.Context, boardID string, p listpage.Request) ([]Task, string, error) {
	return s.listCards(ctx, `board_id = ?`, []any{boardID}, p)
}

func (s *Store) ListInBoardStage(ctx context.Context, boardID, stage string) ([]Task, error) {
	tasks, _, err := s.listCards(ctx, `board_id = ? AND stage = ?`,
		[]any{boardID, stage}, listpage.Request{})
	return tasks, err
}

func (s *Store) BoardStageHasAgent(ctx context.Context, workspaceID, boardID, stage, exceptID string) (bool, error) {
	var found bool
	err := s.db.QueryRowContext(ctx, s.rb(`SELECT EXISTS (
		SELECT 1 FROM tasks
		 WHERE workspace_id = ? AND board_id = ? AND id <> ?
		   AND COALESCE(stage_assignments::jsonb ->> ?, '') <> '')`),
		workspaceID, boardID, exceptID, stage).Scan(&found)
	return found, err
}

func (s *Store) ListByBoardType(ctx context.Context, boardID, typeID string) ([]Task, error) {
	tasks, _, err := s.listCards(ctx, `board_id = ? AND type_id = ?`,
		[]any{boardID, typeID}, listpage.Request{})
	return tasks, err
}

func (s *Store) ListInBoardTypeStage(ctx context.Context, boardID, typeID, stage string) ([]Task, error) {
	tasks, _, err := s.listCards(ctx, `board_id = ? AND type_id = ? AND stage = ?`,
		[]any{boardID, typeID, stage}, listpage.Request{})
	return tasks, err
}

func (s *Store) ListChildren(ctx context.Context, parentID string) ([]Task, error) {
	return s.listWhere(ctx, `WHERE parent_id = ? ORDER BY (sort_order = 0) ASC, sort_order ASC, created_at ASC, id ASC`, parentID)
}

// Leaves updated_at alone: bumping it would reshuffle the board.
func (s *Store) SetChildOrder(ctx context.Context, parentID string, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "task.errors.child_order_begin")
	}
	defer func() { _ = tx.Rollback() }()
	for i, id := range ids {
		res, err := tx.ExecContext(ctx, s.rb(
			`UPDATE tasks SET sort_order = ? WHERE id = ? AND parent_id = ?`), i+1, id, parentID)
		if err != nil {
			return apperr.Wrap(err, "task.errors.child_order_write")
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return ErrInvalid
		}
	}
	return apperr.Wrap(tx.Commit(), "task.errors.child_order_commit")
}

func (s *Store) AllInGroupDone(ctx context.Context, rootID string) (bool, error) {
	frag, args := s.notTerminalFrag("")
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT COUNT(*) FROM tasks
		 WHERE (id = ? OR parent_id = ?) AND `+frag),
		append([]any{rootID, rootID}, args...)...,
	)
	var notDone int
	if err := row.Scan(&notDone); err != nil {
		return false, err
	}
	return notDone == 0, nil
}

type StageCount struct {
	BoardID string
	Stage   string
	Count   int
}

func (s *Store) CountByStage(ctx context.Context) ([]StageCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT board_id, stage, COUNT(*) FROM tasks GROUP BY board_id, stage`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := []StageCount{}
	for rows.Next() {
		var c StageCount
		if err := rows.Scan(&c.BoardID, &c.Stage, &c.Count); err != nil {
			return nil, err
		}
		counts = append(counts, c)
	}
	return counts, rows.Err()
}

func (s *Store) CountAssignedTo(ctx context.Context, wid string, all bool, agent string) (int, error) {
	q := `SELECT COUNT(*) FROM tasks t
	       WHERE EXISTS (SELECT 1 FROM jsonb_each_text(t.stage_assignments::jsonb) e WHERE e.value = ?)`
	args := []any{agent}
	if !all {
		q += ` AND t.workspace_id = ?`
		args = append(args, wid)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, s.rb(q), args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

type TypeCount struct {
	BoardID string
	TypeID  string
	Count   int
}

func (s *Store) CountByType(ctx context.Context) ([]TypeCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT board_id, type_id, COUNT(*) FROM tasks GROUP BY board_id, type_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := []TypeCount{}
	for rows.Next() {
		var c TypeCount
		if err := rows.Scan(&c.BoardID, &c.TypeID, &c.Count); err != nil {
			return nil, err
		}
		counts = append(counts, c)
	}
	return counts, rows.Err()
}

func (s *Store) collectTasks(ctx context.Context, rows *sql.Rows) ([]Task, error) {
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.decorateProjectLinks(ctx, out); err != nil {
		return nil, err
	}
	s.decorateBlocked(ctx, out)
	s.decorateChildCounts(ctx, out)
	s.decorateApprovalCounts(ctx, out)
	s.decorateStatusReasons(ctx, out)
	s.decorateQuestionCounts(ctx, out)
	return out, nil
}

func (s *Store) decorateStatusReasons(ctx context.Context, out []Task) {
	var ids []any
	at := map[string]int{}
	for i := range out {
		if out[i].Status.HasReason() {
			ids = append(ids, out[i].ID)
			at[out[i].ID] = i
		}
	}
	if len(ids) == 0 {
		return
	}
	q := `SELECT task_id, body, body_i18n FROM task_comments
	       WHERE source = ? AND note_key <> '' AND parent_id = '' AND task_id IN (` +
		placeholderList(len(ids)) + `)` + ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, s.rb(q), append([]any{string(SourceSystem)}, ids...)...)
	if err != nil {
		logx.Warn(err, "task.log.status_reasons")
		return
	}
	defer rows.Close()
	locale := i18n.FromContext(ctx)
	for rows.Next() {
		var c Comment
		var texts string
		if err := rows.Scan(&c.TaskID, &c.Body, &texts); err != nil {
			logx.Warn(err, "task.log.status_reasons")
			return
		}
		i := at[c.TaskID]
		if out[i].StatusReason != "" {
			continue
		}
		c.BodyI18n = decodeTexts(texts)
		out[i].StatusReason = c.textFor(locale)
	}
	logx.Warn(rows.Err(), "task.log.status_reasons")
}

func taskIDsOf(out []Task) []any {
	ids := make([]any, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	return ids
}

func (s *Store) decorateChildCounts(ctx context.Context, out []Task) {
	if len(out) == 0 {
		return
	}
	ids := taskIDsOf(out)
	q := `SELECT parent_id, COUNT(*) FROM tasks
	       WHERE parent_id IN (` + placeholderList(len(ids)) + `) GROUP BY parent_id`
	rows, err := s.db.QueryContext(ctx, s.rb(q), ids...)
	if err != nil {
		logx.Warn(err, "task.log.decorate_child_counts")
		return
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var pid string
		var n int
		if err := rows.Scan(&pid, &n); err != nil {
			logx.Warn(err, "task.log.decorate_child_counts_scan")
			return
		}
		counts[pid] = n
	}
	if err := rows.Err(); err != nil {
		logx.Warn(err, "task.log.decorate_child_counts_rows")
		return
	}
	for i := range out {
		if n, ok := counts[out[i].ID]; ok {
			out[i].ChildCount = n
		}
	}
}

// Only the dependent side is scoped to the listed rows; blockers are deliberately unscoped (one on
// another board still blocks).
func (s *Store) decorateBlocked(ctx context.Context, out []Task) {
	if len(out) == 0 {
		return
	}
	summary, err := s.blockedSummaryFor(ctx, taskIDsOf(out))
	if err != nil {
		logx.Warn(err, "task.log.decorate_blocked")
		return
	}
	for i := range out {
		if n, ok := summary[out[i].ID]; ok && n > 0 {
			out[i].IsBlocked = true
			out[i].BlockedByCount = n
		}
	}
}

func (p Patch) applyTo(t *Task) {
	patch.Set(&t.Title, p.Title)
	patch.Set(&t.Body, p.Body)
	patch.Set(&t.Stage, p.Stage)
	if p.StageAssignments != nil {
		t.StageAssignments = p.StageAssignments
	}
	patch.Set(&t.ProjectIDs, p.ProjectIDs)
	patch.Set(&t.BranchName, p.BranchName)
	if p.WorktreePaths != nil {
		t.WorktreePaths = p.WorktreePaths
	}
	if p.PRURLs != nil {
		t.PRURLs = p.PRURLs
	}
	patch.Set(&t.CurrentRunID, p.CurrentRunID)
	patch.Set(&t.CurrentInstance, p.CurrentInstance)
	patch.Set(&t.Status, p.Status)
	patch.Set(&t.Tags, p.Tags)
	patch.Set(&t.Priority, p.Priority)
	patch.Set(&t.BoardID, p.BoardID)
	patch.Set(&t.TypeID, p.TypeID)
	patch.Set(&t.SessionID, p.SessionID)
	patch.Set(&t.SessionAgent, p.SessionAgent)
	patch.Set(&t.SessionStage, p.SessionStage)
}

// Names only the patched columns: the dispatcher writes status from a post-run goroutine while a
// user edits other fields.
// Presence is nil-ness: a *string to "" or a non-nil empty map is a value that clears the column.
func (p Patch) assignments(now time.Time) (cols []string, args []any, err error) {
	str := func(name string, v *string) {
		if v != nil {
			cols, args = append(cols, name), append(args, *v)
		}
	}
	jsonCol := func(name string, present bool, v any) error {
		if !present {
			return nil
		}
		b, mErr := json.Marshal(v)
		if mErr != nil {
			return apperr.Wrap(mErr, "task.errors.marshal_column", "column", name)
		}
		cols, args = append(cols, name), append(args, string(b))
		return nil
	}

	str("title", p.Title)
	str("body", p.Body)
	if p.Stage != nil {
		cols, args = append(cols, "stage"), append(args, string(*p.Stage))
	}
	if err := jsonCol("stage_assignments", p.StageAssignments != nil, p.StageAssignments); err != nil {
		return nil, nil, err
	}
	str("branch_name", p.BranchName)
	if err := jsonCol("worktree_paths", p.WorktreePaths != nil, p.WorktreePaths); err != nil {
		return nil, nil, err
	}
	if err := jsonCol("pr_urls", p.PRURLs != nil, p.PRURLs); err != nil {
		return nil, nil, err
	}
	str("current_run_id", p.CurrentRunID)
	str("current_instance", p.CurrentInstance)
	if p.Status != nil {
		cols, args = append(cols, "status"), append(args, string(*p.Status))
	}
	if p.Tags != nil {
		if err := jsonCol("tags", true, *p.Tags); err != nil {
			return nil, nil, err
		}
	}
	str("priority", p.Priority)
	str("board_id", p.BoardID)
	str("type_id", p.TypeID)
	str("session_id", p.SessionID)
	str("session_agent", p.SessionAgent)
	str("session_stage", p.SessionStage)
	if p.Archived != nil {
		var at any
		if *p.Archived {
			at = now
		}
		cols, args = append(cols, "archived_at"), append(args, at)
	}

	cols, args = append(cols, "updated_at"), append(args, now)
	return cols, args, nil
}

func (s *Store) Update(ctx context.Context, id string, p Patch) (*Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.update_begin", "id", id)
	}
	defer func() { _ = tx.Rollback() }()

	t, err := s.getRawTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	prevStage, prevBoard := t.Stage, t.BoardID
	p.applyTo(t)
	t.UpdatedAt = time.Now().UTC()
	if p.Archived != nil {
		t.ArchivedAt = nil
		if *p.Archived {
			at := t.UpdatedAt
			t.ArchivedAt = &at
		}
	}

	cols, args, err := p.assignments(t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	set := make([]string, len(cols))
	for i, c := range cols {
		set[i] = c + "=?"
	}
	// set holds only the constant column names from assignments; values travel as bind arguments.
	q := `UPDATE tasks SET ` + strings.Join(set, ", ") + ` WHERE id=?`
	if _, err = tx.ExecContext(ctx, s.rb(q), append(args, id)...); err != nil {
		return nil, err
	}
	if p.ProjectIDs != nil {
		t.ProjectIDs = dedupeStrings(*p.ProjectIDs)
		if linkErr := s.writeProjectLinks(ctx, tx, id, t.ProjectIDs); linkErr != nil {
			return nil, linkErr
		}
	}
	if p.Archived != nil && !t.IsSubtask() {
		if _, err = tx.ExecContext(ctx, s.rb(`UPDATE tasks SET archived_at=?, updated_at=? WHERE parent_id=?`),
			t.ArchivedAt, t.UpdatedAt, id); err != nil {
			return nil, apperr.Wrap(err, "task.errors.archive_subtasks", "id", id)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, apperr.Wrap(err, "task.errors.update_commit", "id", id)
	}
	if t.Stage != prevStage || t.BoardID != prevBoard {
		s.stageEntered(ctx, t)
	}
	s.notify(ChangeUpdated, t)
	return t, nil
}

// Callers must create the destination row first, then record the forked_from event and create
// worktrees; ForkData only copies rows and files.
type (
	forkComment struct {
		id, parentID                string
		source, author, body, stage string
		createdAt                   time.Time
		resolvedAt                  sql.NullTime
		resolvedBy                  string
		noteKey, texts              string
	}
	forkEvent struct {
		kind, refID, actor, summary, payload string
		createdAt                            time.Time
	}
	forkAttachment struct {
		commentID, author        string
		filename, mimeType, path string
		size                     int64
		createdAt                time.Time
	}
)

func (s *Store) ForkData(ctx context.Context, srcID, dstID string) error {
	comments, err := s.readForkComments(ctx, srcID)
	if err != nil {
		return err
	}
	approvals, err := s.ListApprovals(ctx, srcID)
	if err != nil {
		return err
	}
	events, err := s.readForkEvents(ctx, srcID)
	if err != nil {
		return err
	}
	atts, err := s.readForkAttachments(ctx, srcID)
	if err != nil {
		return err
	}
	cases, err := s.listCases(ctx, s.db, srcID)
	if err != nil {
		return err
	}
	// Copied files count against the fork's storage quota; the guard is held until commit so a
	// parallel upload can't take the same room.
	var copyBytes int64
	for _, a := range atts {
		copyBytes += a.size
	}
	if copyBytes > 0 {
		guard, gErr := s.ReserveAttachment(ctx, dstID)
		if gErr != nil {
			return gErr
		}
		defer guard.Done()
		if checkErr := guard.Check(copyBytes); checkErr != nil {
			return checkErr
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "task.errors.fork_begin")
	}
	committed := false
	dstAttachDir := filepath.Join(s.attachmentsDir, dstID)
	defer func() {
		tx.Rollback()
		if !committed {
			logx.Warn(os.RemoveAll(dstAttachDir), "task.log.fork_cleanup_orphans", "dir", dstAttachDir)
		}
	}()

	commentRemap, err := s.insertForkComments(ctx, tx, dstID, comments)
	if err != nil {
		return err
	}
	if err := s.insertForkApprovals(ctx, tx, dstID, approvals); err != nil {
		return err
	}
	if err := s.insertForkEvents(ctx, tx, dstID, events); err != nil {
		return err
	}
	if err := s.insertForkAttachments(ctx, tx, dstID, dstAttachDir, atts, commentRemap); err != nil {
		return err
	}
	if err := s.copyTestCasesForClone(ctx, tx, dstID, "", cases, time.Now().UTC()); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return apperr.Wrap(err, "task.errors.fork_commit")
	}
	committed = true
	return nil
}

func (s *Store) readForkComments(ctx context.Context, srcID string) ([]forkComment, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, parent_id, source, author, body, stage, created_at, resolved_at, resolved_by, note_key, body_i18n
		   FROM task_comments WHERE task_id = ? ORDER BY created_at`), srcID)
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.fork_read_comments")
	}
	defer rows.Close()
	var out []forkComment
	for rows.Next() {
		var c forkComment
		if err := rows.Scan(&c.id, &c.parentID, &c.source, &c.author, &c.body, &c.stage, &c.createdAt, &c.resolvedAt, &c.resolvedBy,
			&c.noteKey, &c.texts); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) readForkEvents(ctx context.Context, srcID string) ([]forkEvent, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT kind, ref_id, actor, summary, payload, created_at FROM workflow_events WHERE task_id = ? ORDER BY created_at`), srcID)
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.fork_read_events")
	}
	defer rows.Close()
	var out []forkEvent
	for rows.Next() {
		var e forkEvent
		if err := rows.Scan(&e.kind, &e.refID, &e.actor, &e.summary, &e.payload, &e.createdAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) readForkAttachments(ctx context.Context, srcID string) ([]forkAttachment, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT comment_id, author, filename, mime_type, size, path, created_at FROM task_attachments WHERE task_id = ? ORDER BY created_at`), srcID)
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.fork_read_attachments")
	}
	defer rows.Close()
	var out []forkAttachment
	for rows.Next() {
		var a forkAttachment
		if err := rows.Scan(&a.commentID, &a.author, &a.filename, &a.mimeType, &a.size, &a.path, &a.createdAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) insertForkComments(ctx context.Context, tx *sql.Tx, dstID string, comments []forkComment) (map[string]string, error) {
	remap := make(map[string]string, len(comments))
	for i := range comments {
		remap[comments[i].id] = mustGenerateID()
	}
	for i := range comments {
		c := &comments[i]
		parent := ""
		if c.parentID != "" {
			parent = remap[c.parentID]
		}
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO task_comments (id, task_id, parent_id, source, author, body, stage, created_at, resolved_at, resolved_by,
			                            note_key, body_i18n)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`),
			remap[c.id], dstID, parent, c.source, c.author, c.body, c.stage, c.createdAt, c.resolvedAt, c.resolvedBy,
			c.noteKey, c.texts,
		); err != nil {
			return nil, apperr.Wrap(err, "task.errors.fork_insert_comment")
		}
	}
	return remap, nil
}

func (s *Store) insertForkEvents(ctx context.Context, tx *sql.Tx, dstID string, events []forkEvent) error {
	for _, e := range events {
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO workflow_events (id, task_id, kind, ref_id, actor, summary, payload, created_at) VALUES (?,?,?,?,?,?,?,?)`),
			mustGenerateID(), dstID, e.kind, e.refID, e.actor, e.summary, e.payload, e.createdAt,
		); err != nil {
			return apperr.Wrap(err, "task.errors.fork_insert_event")
		}
	}
	return nil
}

func (s *Store) insertForkAttachments(ctx context.Context, tx *sql.Tx, dstID, dstAttachDir string, atts []forkAttachment, commentRemap map[string]string) error {
	if len(atts) == 0 {
		return nil
	}
	if err := os.MkdirAll(dstAttachDir, 0o750); err != nil {
		return apperr.Wrap(err, "task.errors.fork_attachment_dir")
	}
	for _, a := range atts {
		dstPath := filepath.Join(dstAttachDir, filepath.Base(a.path))
		if err := copyFile(a.path, dstPath); err != nil {
			log.Printf("[task] %s", i18n.Log("task.log.fork_copy_attachment_failed", "path", a.path, "err", err))
			continue
		}
		commentID := commentRemap[a.commentID]
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO task_attachments (id, task_id, comment_id, author, filename, mime_type, size, path, created_at)
			 VALUES (?,?,?,?,?,?,?,?,?)`),
			mustGenerateID(), dstID, commentID, a.author, a.filename, a.mimeType, a.size, dstPath, a.createdAt,
		); err != nil {
			return apperr.Wrap(err, "task.errors.fork_insert_attachment")
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // G304: src is an attachment path recorded in our own DB
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst) //nolint:gosec // G304: dst = trusted attachmentsDir + filepath.Base of source
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		logx.Warn(os.Remove(dst), "task.log.cleanup_partial_copy", "path", dst)
		return err
	}
	return nil
}

// Attachment files go after the commit, best-effort: a failed commit keeps the rows pointing at
// them, and a leftover file is only garbage.
func (s *Store) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, "task.errors.delete_begin", "id", id)
	}
	defer func() { _ = tx.Rollback() }()

	// Read before deleting: the change notification must carry the workspace, or it is broadcast to
	// every tenant as an unstamped ("") row.
	gone, _ := s.getRawTx(ctx, tx, id) //nolint:errcheck // notification detail only; absence is handled by the nil check in notify

	for _, tbl := range []string{
		"test_case_runs", "test_cases",
		"task_comments", "task_attachments", "task_flags", "task_reports",
		"workflow_events", "task_projects",
	} {
		if _, err = tx.ExecContext(ctx, s.rb(`DELETE FROM `+tbl+` WHERE task_id = ?`), id); err != nil {
			return apperr.Wrap(err, "task.errors.delete_from", "id", id, "table", tbl)
		}
	}
	if _, err = tx.ExecContext(ctx, s.rb(
		`DELETE FROM task_dependencies WHERE task_id = ? OR depends_on_task_id = ?`),
		id, id,
	); err != nil {
		return apperr.Wrap(err, "task.errors.delete_dependencies", "id", id)
	}
	if _, err = tx.ExecContext(ctx, s.rb(`DELETE FROM tasks WHERE id = ?`), id); err != nil {
		return apperr.Wrap(err, "task.errors.delete_row", "id", id)
	}
	if err = tx.Commit(); err != nil {
		return apperr.Wrap(err, "task.errors.delete_commit", "id", id)
	}
	s.removeAttachmentFiles(id)
	s.notify(ChangeDeleted, gone)
	return nil
}

func (s *Store) removeAttachmentFiles(taskID string) {
	if s.attachmentsDir == "" {
		return
	}
	if taskID == "" || filepath.Base(taskID) != taskID {
		log.Printf("[task] %s", i18n.Log("task.log.refuse_remove_attachments", "id", taskID))
		return
	}
	logx.Warn(safepath.RemoveAll(s.attachmentsDir, taskID), "task.log.remove_attachment_files", "id", taskID)
}

func (t *Task) AssignedAgent(s Stage) string {
	if t.StageAssignments == nil {
		return ""
	}
	return t.StageAssignments[s]
}

// Sub-collection writes must notify: the dashboard refetches only when told, otherwise it waits for
// the 30-second poll.
func (s *Store) touchTask(ctx context.Context, taskID string, ts time.Time) {
	var t Task
	err := s.db.QueryRowContext(ctx, s.rb(
		`UPDATE tasks SET updated_at = ? WHERE id = ? RETURNING id, workspace_id, board_id, stage, parent_id`), ts, taskID).
		Scan(&t.ID, &t.WorkspaceID, &t.BoardID, &t.Stage, &t.ParentID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		logx.Warn(err, "task.log.bump_updated_at", "id", taskID)
		return
	}
	s.notify(ChangeUpdated, &t)
}

func (s *Store) stageOf(ctx context.Context, taskID string) Stage {
	var stage Stage
	logx.Warn(s.db.QueryRowContext(ctx,
		s.rb(`SELECT stage FROM tasks WHERE id = ?`), taskID).Scan(&stage),
		"task.log.stage_snapshot", "id", taskID)
	return stage
}

var ErrNoSuchThread error = apperr.Invalid("task.errors.no_such_thread")

func (s *Store) AddComment(ctx context.Context, taskID string, src Source, author, body string) (*Comment, error) {
	return s.addComment(ctx, taskID, "", src, author, body)
}

func (s *Store) AddReply(ctx context.Context, taskID, parentID string, src Source, author, body string) (*Comment, error) {
	root, err := s.ThreadRoot(ctx, taskID, parentID)
	if err != nil {
		return nil, err
	}
	c, err := s.addComment(ctx, taskID, root, src, author, body)
	if err == nil && src == SourceUser {
		_, reopenErr := s.db.ExecContext(ctx, s.rb(
			`UPDATE task_comments SET resolved_at = NULL, resolved_by = '' WHERE id = ? AND resolved_at IS NOT NULL`), root)
		logx.Warn(reopenErr, "task.log.reopen_thread", "thread", root, "task", taskID)
	}
	return c, err
}

func (s *Store) AddToThread(ctx context.Context, taskID, threadID string, src Source, author, body string) (*Comment, error) {
	if threadID == "" {
		return s.AddComment(ctx, taskID, src, author, body)
	}
	c, err := s.AddReply(ctx, taskID, threadID, src, author, body)
	if errors.Is(err, ErrNoSuchThread) {
		return s.AddComment(ctx, taskID, src, author, body)
	}
	return c, err
}

func (s *Store) AddNote(ctx context.Context, taskID, threadID string, src Source, author string, n Note) (*Comment, error) {
	c := &Comment{TaskID: taskID, Source: src, Author: author, Body: n.Text(), Key: n.Key, BodyI18n: n.Texts()}
	if threadID != "" {
		root, err := s.ThreadRoot(ctx, taskID, threadID)
		switch {
		case err == nil:
			c.ParentID = root
		case !errors.Is(err, ErrNoSuchThread):
			return nil, err
		}
	}
	return s.insertComment(ctx, c)
}

func (s *Store) ResolveThread(ctx context.Context, taskID, rootID, by string, resolved bool) error {
	var at sql.NullTime
	if resolved {
		at = sql.NullTime{Time: time.Now().UTC(), Valid: true}
	} else {
		by = ""
	}
	res, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE task_comments SET resolved_at = ?, resolved_by = ?
		  WHERE id = ? AND task_id = ? AND parent_id = ''`), at, by, rootID, taskID)
	if err != nil {
		return apperr.Wrap(err, "task.errors.resolve_thread", "id", rootID)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ErrNoSuchThread
	}
	s.touchTask(ctx, taskID, time.Now().UTC())
	return nil
}

func (s *Store) ThreadRoot(ctx context.Context, taskID, commentID string) (string, error) {
	var parent string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT parent_id FROM task_comments WHERE id = ? AND task_id = ?`), commentID, taskID).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoSuchThread
	}
	if err != nil {
		return "", err
	}
	if parent != "" {
		return parent, nil
	}
	return commentID, nil
}

func (s *Store) addComment(ctx context.Context, taskID, parentID string, src Source, author, body string) (*Comment, error) {
	return s.insertComment(ctx, &Comment{TaskID: taskID, ParentID: parentID, Source: src, Author: author, Body: body})
}

func (s *Store) insertComment(ctx context.Context, c *Comment) (*Comment, error) {
	c.ID = mustGenerateID()
	c.Stage = s.stageOf(ctx, c.TaskID)
	c.CreatedAt = time.Now().UTC()
	taskID := c.TaskID
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO task_comments (id, task_id, parent_id, source, author, body, stage, created_at, note_key, body_i18n)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`),
		c.ID, c.TaskID, c.ParentID, c.Source, c.Author, c.Body, c.Stage, c.CreatedAt, c.Key, encodeTexts(c.BodyI18n),
	)
	if err != nil {
		return nil, err
	}
	s.touchTask(ctx, taskID, c.CreatedAt)
	return c, nil
}

func (s *Store) ListComments(ctx context.Context, taskID string) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, task_id, parent_id, source, author, body, stage, created_at, resolved_at, resolved_by,
		        note_key, body_i18n
		   FROM task_comments WHERE task_id = ? ORDER BY created_at ASC`), taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	locale := i18n.FromContext(ctx)
	out := []Comment{}
	for rows.Next() {
		var (
			c        Comment
			resolved sql.NullTime
			texts    string
		)
		if err := rows.Scan(&c.ID, &c.TaskID, &c.ParentID, &c.Source, &c.Author, &c.Body, &c.Stage, &c.CreatedAt,
			&resolved, &c.ResolvedBy, &c.Key, &texts); err != nil {
			return nil, err
		}
		if resolved.Valid {
			at := resolved.Time.UTC()
			c.ResolvedAt = &at
		}
		c.BodyI18n = decodeTexts(texts)
		c.Body = c.textFor(locale)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type NewAttachment struct {
	TaskID    string
	CommentID string
	Author    string
	Filename  string
	MimeType  string
	Path      string
	Size      int64
}

func (s *Store) AddAttachment(ctx context.Context, in NewAttachment) (*Attachment, error) {
	a := &Attachment{
		ID:        mustGenerateID(),
		TaskID:    in.TaskID,
		CommentID: in.CommentID,
		Author:    in.Author,
		Filename:  in.Filename,
		MimeType:  in.MimeType,
		Size:      in.Size,
		Path:      in.Path,
		CreatedAt: time.Now().UTC(),
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO task_attachments (id, task_id, comment_id, author, filename, mime_type, size, path, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`),
		a.ID, a.TaskID, a.CommentID, a.Author, a.Filename, a.MimeType, a.Size, a.Path, a.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	s.touchTask(ctx, a.TaskID, a.CreatedAt)
	return a, nil
}

func (s *Store) ListAttachments(ctx context.Context, taskID string) ([]Attachment, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, task_id, comment_id, author, filename, mime_type, size, path, created_at FROM task_attachments
		 WHERE task_id = ? ORDER BY created_at ASC`), taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.TaskID, &a.CommentID, &a.Author, &a.Filename, &a.MimeType, &a.Size, &a.Path, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) CommentBelongsTo(ctx context.Context, taskID, commentID string) (bool, error) {
	var found string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id FROM task_comments WHERE id = ? AND task_id = ?`), commentID, taskID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, apperr.Wrap(err, "task.errors.check_comment", "id", commentID)
	}
	return true, nil
}

func (s *Store) BindCommentAttachments(ctx context.Context, taskID, commentID string, ids []string) error {
	if commentID == "" || len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		_, err := s.db.ExecContext(ctx, s.rb(
			`UPDATE task_attachments SET comment_id = ? WHERE id = ? AND task_id = ? AND comment_id = ''`),
			commentID, id, taskID,
		)
		if err != nil {
			return apperr.Wrap(err, "task.errors.bind_attachment", "id", id, "comment", commentID)
		}
	}
	return nil
}

func (s *Store) DeleteAttachment(ctx context.Context, id string) error {
	a, err := s.GetAttachment(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM task_attachments WHERE id = ?`), id); err != nil {
		return apperr.Wrap(err, "task.errors.delete_attachment", "id", id)
	}
	if a.Path != "" {
		if rmErr := os.Remove(a.Path); rmErr != nil && !os.IsNotExist(rmErr) {
			logx.Warn(rmErr, "task.log.delete_attachment_file", "path", a.Path)
		}
	}
	s.touchTask(ctx, a.TaskID, time.Now().UTC())
	return nil
}

func (s *Store) AddReport(ctx context.Context, taskID string, src Source, author, kind, title, body, instanceID string) (*Report, error) {
	return s.AddRunReport(ctx, taskID, "", src, author, kind, title, body, instanceID)
}

func (s *Store) AddRunReport(ctx context.Context, taskID string, runStage Stage, src Source, author, kind, title, body, instanceID string) (*Report, error) {
	r := &Report{
		ID:         mustGenerateID(),
		TaskID:     taskID,
		Source:     src,
		Author:     author,
		Stage:      s.reportStage(ctx, taskID, instanceID, runStage),
		InstanceID: instanceID,
		Kind:       kind,
		Title:      title,
		Body:       body,
		CreatedAt:  time.Now().UTC(),
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO task_reports (id, task_id, source, author, stage, instance_id, kind, title, body, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`),
		r.ID, r.TaskID, r.Source, r.Author, r.Stage, r.InstanceID, r.Kind, r.Title, r.Body, r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	s.touchTask(ctx, taskID, r.CreatedAt)
	return r, nil
}

func (s *Store) reportStage(ctx context.Context, taskID, instanceID string, runStage Stage) Stage {
	if stage, ok := InstanceStage(instanceID); ok {
		return stage
	}
	if runStage != "" {
		return runStage
	}
	return s.stageOf(ctx, taskID)
}

func (s *Store) ReportBelongsTo(ctx context.Context, taskID, reportID string) (bool, error) {
	var found string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id FROM task_reports WHERE id = ? AND task_id = ?`), reportID, taskID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, apperr.Wrap(err, "task.errors.check_report", "id", reportID)
	}
	return true, nil
}

func (s *Store) ListReports(ctx context.Context, taskID string) ([]Report, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, task_id, source, author, stage, instance_id, kind, title, body, created_at
		 FROM task_reports WHERE task_id = ? ORDER BY created_at ASC`), taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Report{}
	for rows.Next() {
		var r Report
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Source, &r.Author, &r.Stage, &r.InstanceID, &r.Kind, &r.Title, &r.Body, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetAttachment(ctx context.Context, id string) (*Attachment, error) {
	row := s.db.QueryRowContext(ctx, s.rb(
		`SELECT id, task_id, comment_id, author, filename, mime_type, size, path, created_at FROM task_attachments WHERE id = ?`), id,
	)
	a := &Attachment{}
	if err := row.Scan(&a.ID, &a.TaskID, &a.CommentID, &a.Author, &a.Filename, &a.MimeType, &a.Size, &a.Path, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.Wrap(ErrNotFound, "task.errors.attachment_ref", "id", id)
		}
		return nil, apperr.Wrap(err, "task.errors.read_attachment", "id", id)
	}
	return a, nil
}

func SanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "file"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}
