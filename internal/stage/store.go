package stage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// The read state is per process: more than one daemon replica would need cross-replica
// invalidation.
type Store struct {
	db *sql.DB

	mu    sync.RWMutex
	state *boardState
}

type boardState struct {
	boards      []BoardInfo
	byID        map[string]Registry
	byIDType    map[string]Registry
	types       map[string][]TaskTypeInfo
	typeCatalog []TaskTypeInfo
	catalog     []types.StageInfo
	defaultID   string
	// Per-workspace defaults; defaultID only answers callers with no workspace (ghost-board
	// fallbacks in task/deps.go and the dispatcher).
	defaultByWorkspace map[string]string
}

func boardTypeKey(boardID, typeID string) string { return boardID + "/" + typeID }

func (st *boardState) board(id string) Registry {
	if r, ok := st.byID[id]; ok {
		return r
	}
	if r, ok := st.byID[st.defaultID]; ok {
		return r
	}
	return Static()
}

func (st *boardState) boardFor(boardID, typeID string) Registry {
	if typeID == "" {
		return st.board(boardID)
	}
	if r, ok := st.byIDType[boardTypeKey(boardID, typeID)]; ok {
		return r
	}
	return st.board(boardID)
}

func (s *Store) rb(q string) string { return dbpkg.Rebind(q) }

func (s *Store) DB() *sql.DB { return s.db }

// needs_agent stays in both tables, unread and unwritten, so a binary rolled back to it still finds
// its rows.
const stageColumns = `auto_advance, blocked_by_flags,
	initial_stage, terminal, ingest_flags, push_branch, open_pr, wip_group,
	required_approvals, on_changes_requested, changes_kick_back_to,
	agent_verdict, lock_tests, skip_unless, release_resources, merge_target, pr_draft, pr_ready,
	no_skip`

// How many values flagArgs returns: one per column in stageColumns.
const stageColumnCount = 19

func NewStore(db *sql.DB) (*Store, error) {
	ctx := context.Background()
	migrations, err := Migrations(migrate.DBInspector(ctx, db))
	if err != nil {
		return nil, err
	}
	if err := migrate.Run(ctx, db, MigrationComponent, migrations); err != nil {
		return nil, apperr.Wrap(err, "stage.errors.create_tables")
	}
	s := &Store{db: db}
	if err := s.seedIfEmpty(context.Background()); err != nil {
		return nil, apperr.Wrap(err, "stage.errors.seed_boards")
	}
	if err := s.reload(context.Background()); err != nil {
		return nil, apperr.Wrap(err, "stage.errors.load_boards")
	}
	return s, nil
}

// Never change: databases record applied migrations under this name and would rerun all of them.
const MigrationComponent = "stages"

// internal/schema calls this with migrate.Everything for the lock and lint. A published migration
// never changes.
func Migrations(in migrate.Inspector) ([]migrate.Migration, error) {
	adopt, err := adoptionSQL(in)
	if err != nil {
		return nil, err
	}
	return []migrate.Migration{
		{ID: 1, SQL: `CREATE TABLE IF NOT EXISTS stages (
			id               TEXT PRIMARY KEY,
			label            TEXT NOT NULL,
			color            TEXT NOT NULL DEFAULT '',
			position         INTEGER NOT NULL,
			auto_advance     INTEGER NOT NULL DEFAULT 0,
			blocked_by_flags INTEGER NOT NULL DEFAULT 0,
			needs_agent      INTEGER NOT NULL DEFAULT 0,
			initial_stage    INTEGER NOT NULL DEFAULT 0,
			terminal         INTEGER NOT NULL DEFAULT 0,
			ingest_flags     INTEGER NOT NULL DEFAULT 0,
			push_branch      INTEGER NOT NULL DEFAULT 0,
			open_pr          INTEGER NOT NULL DEFAULT 0,
			wip_group        TEXT NOT NULL DEFAULT ''
		)`},
		// Additive only: stages keeps every column so a rollback to the previous binary still finds
		// its board.
		{ID: 2, SQL: `CREATE TABLE IF NOT EXISTS boards (
			id           TEXT PRIMARY KEY,
			label        TEXT NOT NULL,
			description  TEXT NOT NULL DEFAULT '',
			workspace_id TEXT NOT NULL DEFAULT '',
			position     INTEGER NOT NULL DEFAULT 0,
			is_default   INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS board_stages (
			board_id         TEXT NOT NULL,
			stage_id         TEXT NOT NULL,
			position         INTEGER NOT NULL,
			auto_advance     INTEGER NOT NULL DEFAULT 0,
			blocked_by_flags INTEGER NOT NULL DEFAULT 0,
			needs_agent      INTEGER NOT NULL DEFAULT 0,
			initial_stage    INTEGER NOT NULL DEFAULT 0,
			terminal         INTEGER NOT NULL DEFAULT 0,
			ingest_flags     INTEGER NOT NULL DEFAULT 0,
			push_branch      INTEGER NOT NULL DEFAULT 0,
			open_pr          INTEGER NOT NULL DEFAULT 0,
			wip_group        TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (board_id, stage_id)
		);
		CREATE INDEX IF NOT EXISTS idx_board_stages_board ON board_stages(board_id, position);`},
		{ID: 3, SQL: `CREATE TABLE IF NOT EXISTS task_types (
			id           TEXT PRIMARY KEY,
			label        TEXT NOT NULL,
			icon         TEXT NOT NULL DEFAULT '',
			color        TEXT NOT NULL DEFAULT '',
			workspace_id TEXT NOT NULL DEFAULT '',
			position     INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS board_task_types (
			board_id   TEXT NOT NULL,
			type_id    TEXT NOT NULL,
			position   INTEGER NOT NULL,
			is_default INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (board_id, type_id)
		);
		CREATE TABLE IF NOT EXISTS board_type_skips (
			board_id TEXT NOT NULL,
			type_id  TEXT NOT NULL,
			stage_id TEXT NOT NULL,
			PRIMARY KEY (board_id, type_id, stage_id)
		);
		CREATE INDEX IF NOT EXISTS idx_board_task_types_board ON board_task_types(board_id, position);`},
		{ID: 4, SQL: adopt},
		// Additive only: older binaries still read label/color from the stages and task_types
		// catalog rows.
		{ID: 5, SQL: `ALTER TABLE board_stages ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT '';
		ALTER TABLE board_stages ADD COLUMN IF NOT EXISTS color TEXT NOT NULL DEFAULT '';
		UPDATE board_stages SET label = s.label, color = s.color
		  FROM stages s WHERE s.id = board_stages.stage_id AND board_stages.label = '';
		ALTER TABLE board_task_types ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT '';
		ALTER TABLE board_task_types ADD COLUMN IF NOT EXISTS icon TEXT NOT NULL DEFAULT '';
		ALTER TABLE board_task_types ADD COLUMN IF NOT EXISTS color TEXT NOT NULL DEFAULT '';
		UPDATE board_task_types SET label = t.label, icon = t.icon, color = t.color
		  FROM task_types t WHERE t.id = board_task_types.type_id AND board_task_types.label = '';`},
		{ID: 6, SQL: `ALTER TABLE stages
			ADD COLUMN IF NOT EXISTS required_approvals   TEXT NOT NULL DEFAULT '[]',
			ADD COLUMN IF NOT EXISTS on_changes_requested TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS changes_kick_back_to TEXT NOT NULL DEFAULT '';
		ALTER TABLE board_stages
			ADD COLUMN IF NOT EXISTS required_approvals   TEXT NOT NULL DEFAULT '[]',
			ADD COLUMN IF NOT EXISTS on_changes_requested TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS changes_kick_back_to TEXT NOT NULL DEFAULT '';`},
		// One migration for every flag and board setting of the Thulu flow, so the pieces that
		// act on them never race for an ID. default_agent names a workspace's agent, so only the
		// per-board table carries it: the stages catalog is shared by every tenant.
		{ID: 7, SQL: `ALTER TABLE stages
			ADD COLUMN IF NOT EXISTS agent_verdict     INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS lock_tests        INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS skip_unless       TEXT    NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS release_resources INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS merge_target      INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS pr_draft          INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS pr_ready          INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE board_stages
			ADD COLUMN IF NOT EXISTS agent_verdict     INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS lock_tests        INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS skip_unless       TEXT    NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS release_resources INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS merge_target      INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS pr_draft          INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS pr_ready          INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS default_agent     TEXT    NOT NULL DEFAULT '';
		ALTER TABLE boards
			ADD COLUMN IF NOT EXISTS max_kick_backs INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS flow           TEXT    NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS flow_revision  INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS flow_hash      TEXT    NOT NULL DEFAULT '';
		CREATE TABLE IF NOT EXISTS board_type_agents (
			board_id TEXT NOT NULL,
			type_id  TEXT NOT NULL,
			stage_id TEXT NOT NULL,
			agent    TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (board_id, type_id, stage_id)
		);`},
		// Additive only: a binary from before it never reads the column and its inserts take the
		// default, so the column keeps letting tasks skip it.
		{ID: 8, SQL: `ALTER TABLE stages ADD COLUMN IF NOT EXISTS no_skip INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE board_stages ADD COLUMN IF NOT EXISTS no_skip INTEGER NOT NULL DEFAULT 0;`},
	}, nil
}

// Must equal api.defaultWorkspaceID, or boards created by unscoped callers become invisible to
// them.
const FallbackWorkspaceID = "default"

// Empty workspace_id is read as visible to every tenant, so no row may stay empty. Majority ties
// break on workspace id
// so concurrent replicas reach the same answer.
func adoptionSQL(in migrate.Inspector) (string, error) {
	hasBoard, err := in.ColumnExists("tasks", "board_id")
	if err != nil {
		return "", apperr.Wrap(err, "stage.errors.inspect_column", "column", "tasks.board_id")
	}
	hasWorkspace, err := in.ColumnExists("tasks", "workspace_id")
	if err != nil {
		return "", apperr.Wrap(err, "stage.errors.inspect_column", "column", "tasks.workspace_id")
	}
	hasWorkspaces, err := in.ColumnExists("workspaces", "id")
	if err != nil {
		return "", apperr.Wrap(err, "stage.errors.inspect_column", "column", "workspaces.id")
	}
	ownerless := ownerlessWorkspace(hasBoard && hasWorkspace, hasWorkspaces)

	byTasks := ""
	if hasBoard && hasWorkspace {
		byTasks = `
		UPDATE boards SET workspace_id = claim.workspace_id
		  FROM (SELECT DISTINCT ON (board_id) board_id, workspace_id
		          FROM (SELECT board_id, workspace_id, COUNT(*) AS n
		                  FROM tasks WHERE workspace_id <> ''
		                 GROUP BY board_id, workspace_id) tallied
		         ORDER BY board_id, n DESC, workspace_id ASC) AS claim
		 WHERE boards.workspace_id = '' AND boards.id = claim.board_id;`
	}
	return byTasks + `
		UPDATE boards SET workspace_id = ` + ownerless + ` WHERE workspace_id = '';

		UPDATE task_types SET workspace_id = claim.workspace_id
		  FROM (SELECT DISTINCT ON (type_id) type_id, workspace_id
		          FROM (SELECT bt.type_id, b.workspace_id, COUNT(*) AS n
		                  FROM board_task_types bt JOIN boards b ON b.id = bt.board_id
		                 WHERE b.workspace_id <> ''
		                 GROUP BY bt.type_id, b.workspace_id) tallied
		         ORDER BY type_id, n DESC, workspace_id ASC) AS claim
		 WHERE task_types.workspace_id = '' AND task_types.id = claim.type_id;

		UPDATE task_types SET workspace_id = ` + ownerless + ` WHERE workspace_id = '';

		WITH picked AS (SELECT DISTINCT ON (workspace_id) id FROM boards
		                 ORDER BY workspace_id, is_default DESC, position ASC, id ASC)
		UPDATE boards SET is_default = CASE WHEN id IN (SELECT id FROM picked) THEN 1 ELSE 0 END;`, nil
}

// The sole-workspace candidate is needed because internal/daemon/janitor.go stamps unstamped tasks 5 s
// after boot,
// so at migration time candidate 1 can find nothing.
func ownerlessWorkspace(hasTasks, hasWorkspaces bool) string {
	expr := `'` + FallbackWorkspaceID + `'`
	if hasWorkspaces {
		expr = `COALESCE((SELECT id FROM workspaces
		                   WHERE (SELECT COUNT(*) FROM workspaces) = 1 LIMIT 1), ` + expr + `)`
	}
	if hasTasks {
		expr = `COALESCE((SELECT workspace_id FROM tasks WHERE workspace_id <> ''
		                   GROUP BY workspace_id
		                   ORDER BY COUNT(*) DESC, workspace_id ASC LIMIT 1), ` + expr + `)`
	}
	return expr
}

func (s *Store) seedIfEmpty(ctx context.Context) error {
	nStages, err := s.count(ctx, `SELECT COUNT(*) FROM stages`)
	if err != nil {
		return err
	}
	if nStages == 0 {
		if seedErr := s.insertCatalog(ctx, s.db, Defaults()); seedErr != nil {
			return seedErr
		}
	}
	nBoards, err := s.count(ctx, `SELECT COUNT(*) FROM boards`)
	if err != nil {
		return err
	}
	if nBoards > 0 {
		return nil
	}
	hasWorkspaces, err := migrate.ColumnExists(ctx, s.db, "workspaces", "id")
	if err != nil {
		return apperr.Wrap(err, "stage.errors.inspect_column", "column", "workspaces.id")
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO boards (id, label, description, workspace_id, position, is_default)
		 VALUES (?,?,?,`+ownerlessWorkspace(false, hasWorkspaces)+`,?,1)`),
		DefaultBoardID, i18n.Text("stage.labels.default_board"), i18n.Text("stage.descriptions.default_board"), 10,
	); err != nil {
		return apperr.Wrap(err, "stage.errors.insert_default_board")
	}
	if _, err := s.db.ExecContext(ctx, s.rb(
		//i18n:ignore SQL statement, not a message
		`INSERT INTO board_stages (board_id, stage_id, position, label, color, `+stageColumns+`)
		 SELECT ?, id, position, label, color, `+stageColumns+` FROM stages`), DefaultBoardID,
	); err != nil {
		return apperr.Wrap(err, "stage.errors.seed_default_membership")
	}
	return nil
}

func (s *Store) count(ctx context.Context, query string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, query).Scan(&n)
	return n, err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ON CONFLICT DO NOTHING is deliberate: the catalog row is a template shared by every workspace,
// and one tenant's write must not change what another reads.
func (s *Store) insertCatalog(ctx context.Context, ex execer, stages []types.StageInfo) error {
	insert := `INSERT INTO stages (id, label, color, position, ` + stageColumns + `)
		VALUES (` + placeholders(4+stageColumnCount) + `) ON CONFLICT (id) DO NOTHING`
	for i := range stages {
		st := &stages[i]
		args, err := flagArgs(*st)
		if err != nil {
			return err
		}
		if _, err := ex.ExecContext(ctx, s.rb(insert), append(
			[]any{string(st.ID), st.Label, st.Color, st.Position}, args...)...,
		); err != nil {
			return apperr.Wrap(err, "stage.errors.insert_stage", "id", st.ID)
		}
	}
	return nil
}

// Order must match stageColumns.
func flagArgs(st types.StageInfo) ([]any, error) {
	approvals, err := encodeApprovals(st.RequiredApprovals)
	if err != nil {
		return nil, apperr.Wrap(err, "stage.errors.encode_approvals", "id", st.ID)
	}
	return []any{
		boolInt(st.AutoAdvance), boolInt(st.BlockedByFlags),
		boolInt(st.Initial), boolInt(st.Terminal), boolInt(st.IngestFlags),
		boolInt(st.PushBranch), boolInt(st.OpenPR), st.WipGroup,
		approvals, st.OnChangesRequested, string(st.ChangesKickBackTo),
		boolInt(st.AgentVerdict), boolInt(st.LockTests), encodeSkipUnless(st.SkipUnless),
		boolInt(st.ReleaseResources), boolInt(st.MergeTarget), boolInt(st.PRDraft), boolInt(st.PRReady),
		boolInt(st.NoSkip),
	}, nil
}

// "?,?,…" with n marks, so an insert's placeholder count follows its column list.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Stored comma-separated: values are slugs (validateSkipUnless), so a comma never occurs in one.
func encodeSkipUnless(values []string) string { return strings.Join(values, ",") }

func decodeSkipUnless(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

func (s *Store) SaveBoard(ctx context.Context, boardID string, board []types.StageInfo) error {
	normalized := make([]types.StageInfo, len(board))
	copy(normalized, board)
	for i := range normalized {
		normalized[i].Position = (i + 1) * 10
		normalized[i].Virtual = false
	}
	if err := ValidateBoard(normalized); err != nil {
		return err
	}

	// Serializes writers so the replace tx and state swap stay coherent; handler read-modify-write
	// is last-write-wins.
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.boardExistsLocked(boardID) {
		return apperr.NotFound("stage.errors.unknown_board", "id", boardID)
	}
	if err := ValidateApprovalTypes(normalized, s.state.types[boardID]); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.writeBoardTx(ctx, tx, boardID, normalized); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reloadLocked(ctx)
}

// Caller holds s.mu.
func (s *Store) writeBoardTx(ctx context.Context, tx *sql.Tx, boardID string, board []types.StageInfo) error {
	if err := s.insertCatalog(ctx, tx, board); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM board_stages WHERE board_id = ?`), boardID); err != nil {
		return err
	}
	insert := `INSERT INTO board_stages (board_id, stage_id, position, label, color, ` + stageColumns + `, default_agent)
		VALUES (` + placeholders(5+stageColumnCount+1) + `)`
	for i := range board {
		st := &board[i]
		args, err := flagArgs(*st)
		if err != nil {
			return err
		}
		args = append([]any{boardID, string(st.ID), st.Position, st.Label, st.Color}, args...)
		if _, err := tx.ExecContext(ctx, s.rb(insert), append(args, st.DefaultAgent)...); err != nil {
			return apperr.Wrap(err, "stage.errors.insert_board_stage", "board", boardID, "stage", st.ID)
		}
	}
	// Safe from the empty NOT IN subquery case because this board's rows were just inserted.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM stages WHERE id NOT IN (SELECT stage_id FROM board_stages)`); err != nil {
		return err
	}
	// A dangling skip or type agent would make ValidateBoardTypes reject the next type write.
	for _, q := range []string{
		`DELETE FROM board_type_skips WHERE board_id = ? AND stage_id NOT IN
		 (SELECT stage_id FROM board_stages WHERE board_id = ?)`,
		`DELETE FROM board_type_agents WHERE board_id = ? AND stage_id NOT IN
		 (SELECT stage_id FROM board_stages WHERE board_id = ?)`,
	} {
		if _, err := tx.ExecContext(ctx, s.rb(q), boardID, boardID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveBoardTypes(ctx context.Context, boardID string, tt []TaskTypeInfo) error {
	normalized := make([]TaskTypeInfo, len(tt))
	copy(normalized, tt)
	for i := range normalized {
		normalized[i].Position = (i + 1) * 10
		normalized[i].TaskCount = 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.boardExistsLocked(boardID) {
		return apperr.NotFound("stage.errors.unknown_board", "id", boardID)
	}
	if err := ValidateBoardTypes(s.state.board(boardID).List(), normalized); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.writeBoardTypesTx(ctx, tx, boardID, normalized); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reloadLocked(ctx)
}

// An existing catalog row may belong to another workspace and is left untouched. Caller holds s.mu.
func (s *Store) writeBoardTypesTx(
	ctx context.Context, tx *sql.Tx, boardID string, tt []TaskTypeInfo,
) error {
	for i := range tt {
		t := &tt[i]
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO task_types (id, label, icon, color, workspace_id, position)
			 VALUES (?,?,?,?,?,?) ON CONFLICT (id) DO NOTHING`),
			t.ID, t.Label, t.Icon, t.Color, t.WorkspaceID, t.Position,
		); err != nil {
			return apperr.Wrap(err, "stage.errors.insert_task_type", "id", t.ID)
		}
	}
	for _, q := range []string{
		`DELETE FROM board_task_types WHERE board_id = ?`,
		`DELETE FROM board_type_skips WHERE board_id = ?`,
		`DELETE FROM board_type_agents WHERE board_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, s.rb(q), boardID); err != nil {
			return err
		}
	}
	for i := range tt {
		if err := s.insertBoardType(ctx, tx, boardID, &tt[i]); err != nil {
			return err
		}
	}
	return gcTaskTypes(ctx, tx)
}

func (s *Store) insertBoardType(
	ctx context.Context, tx *sql.Tx, boardID string, t *TaskTypeInfo,
) error {
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO board_task_types (board_id, type_id, position, is_default, label, icon, color)
		 VALUES (?,?,?,?,?,?,?)`),
		boardID, t.ID, t.Position, boolInt(t.Default), t.Label, t.Icon, t.Color,
	); err != nil {
		return apperr.Wrap(err, "stage.errors.insert_board_task_type", "board", boardID, "type", t.ID)
	}
	for _, sk := range t.Skips {
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO board_type_skips (board_id, type_id, stage_id) VALUES (?,?,?)`),
			boardID, t.ID, string(sk),
		); err != nil {
			return apperr.Wrap(err, "stage.errors.insert_skip", "board", boardID, "type", t.ID, "stage", sk)
		}
	}
	for _, st := range sortedAgentStages(t.Agents) {
		if _, err := tx.ExecContext(ctx, s.rb(
			`INSERT INTO board_type_agents (board_id, type_id, stage_id, agent) VALUES (?,?,?,?)`),
			boardID, t.ID, string(st), t.Agents[st],
		); err != nil {
			return apperr.Wrap(err, "stage.errors.insert_type_agent", "board", boardID, "type", t.ID, "stage", st)
		}
	}
	return nil
}

func sortedAgentStages(agents map[types.Stage]string) []types.Stage {
	out := make([]types.Stage, 0, len(agents))
	for st := range agents {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func gcTaskTypes(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM task_types WHERE id NOT IN (SELECT type_id FROM board_task_types)`)
	return err
}

func (s *Store) Save(ctx context.Context, board []types.StageInfo) error {
	return s.SaveBoard(ctx, s.DefaultBoardID(), board)
}

func (s *Store) CreateBoard(ctx context.Context, info BoardInfo, board []types.StageInfo) error {
	if err := ValidateBoardInfo(info); err != nil {
		return err
	}
	normalized := make([]types.StageInfo, len(board))
	copy(normalized, board)
	for i := range normalized {
		normalized[i].Position = (i + 1) * 10
		normalized[i].Virtual = false
	}
	if err := ValidateBoard(normalized); err != nil {
		return err
	}
	if err := ValidateApprovalTypes(normalized, nil); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.boardExistsLocked(info.ID) {
		return apperr.Conflict("stage.errors.board_exists", "id", info.ID)
	}
	// Count per workspace: an install-wide cap would let one tenant lock every other out of
	// creating boards.
	if n := s.workspaceBoardCountLocked(info.WorkspaceID); n >= MaxBoards {
		return apperr.Invalid("stage.errors.too_many_boards", "count", n, "max", MaxBoards)
	}

	return s.inTxLocked(ctx, func(tx *sql.Tx) error {
		if err := s.insertBoardRowTx(ctx, tx, info); err != nil {
			return err
		}
		return s.writeBoardTx(ctx, tx, info.ID, normalized)
	})
}

func (s *Store) UpdateBoard(ctx context.Context, info BoardInfo) error {
	if err := ValidateBoardInfo(info); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.boardExistsLocked(info.ID) {
		return apperr.NotFound("stage.errors.unknown_board", "id", info.ID)
	}
	flowName, flowRevision, flowHash := info.flowColumns()
	if _, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE boards SET label = ?, description = ?, max_kick_backs = ?,
		    flow = ?, flow_revision = ?, flow_hash = ? WHERE id = ?`),
		info.Label, info.Description, info.MaxKickBacks,
		flowName, flowRevision, flowHash, info.ID,
	); err != nil {
		return err
	}
	return s.reloadLocked(ctx)
}

// Callers must relocate tasks first (see the API handler).
func (s *Store) DeleteBoard(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.boardInfoLocked(id)
	if !ok {
		return apperr.NotFound("stage.errors.unknown_board", "id", id)
	}
	if info.Default || id == s.state.defaultID {
		return apperr.Conflict("stage.errors.delete_default_board")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM board_stages WHERE board_id = ?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM board_task_types WHERE board_id = ?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM board_type_skips WHERE board_id = ?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM board_type_agents WHERE board_id = ?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rb(`DELETE FROM boards WHERE id = ?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM stages WHERE id NOT IN (SELECT stage_id FROM board_stages)`); err != nil {
		return err
	}
	if err := gcTaskTypes(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reloadLocked(ctx)
}

// Clear only this workspace's marker; clearing the whole table strips other tenants of their
// default.
func (s *Store) SetDefaultBoard(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.boardInfoLocked(id)
	if !ok {
		return apperr.NotFound("stage.errors.unknown_board", "id", id)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.rb(
		`UPDATE boards SET is_default = 0 WHERE workspace_id = ?`), info.WorkspaceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rb(`UPDATE boards SET is_default = 1 WHERE id = ?`), id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.reloadLocked(ctx)
}

func (s *Store) boardExistsLocked(id string) bool {
	_, ok := s.boardInfoLocked(id)
	return ok
}

func (s *Store) boardInfoLocked(id string) (BoardInfo, bool) {
	for _, b := range s.state.boards {
		if b.ID == id {
			return b, true
		}
	}
	return BoardInfo{}, false
}

func (s *Store) workspaceBoardCountLocked(wid string) int {
	n := 0
	for _, b := range s.state.boards {
		if b.WorkspaceID == wid {
			n++
		}
	}
	return n
}

func (s *Store) EnsureDefaultBoard(ctx context.Context, wid string) (BoardInfo, error) {
	if wid == "" {
		return BoardInfo{}, apperr.Invalid("stage.errors.seed_empty_workspace")
	}
	s.mu.RLock()
	b, ok := s.workspaceDefaultLocked(wid)
	s.mu.RUnlock()
	if ok {
		return b, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.workspaceDefaultLocked(wid); ok {
		return b, nil
	}
	// Adopt before inserting, or the install ends up with a duplicate "Default" board and the
	// seeded one stays unowned.
	if adopted, err := s.adoptFallbackBoardsLocked(ctx, wid); err != nil {
		return BoardInfo{}, err
	} else if adopted {
		if b, ok := s.workspaceDefaultLocked(wid); ok {
			return b, nil
		}
	}
	id, err := s.freeBoardIDLocked(wid)
	if err != nil {
		return BoardInfo{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BoardInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO boards (id, label, description, workspace_id, position, is_default)
		 VALUES (?,?,?,?,?,1)`),
		id, i18n.TCtx(ctx, "stage.labels.default_board"), i18n.TCtx(ctx, "stage.descriptions.default_board"), wid, (len(s.state.boards)+1)*10,
	); err != nil {
		return BoardInfo{}, apperr.Wrap(err, "stage.errors.seed_workspace_board", "workspace", wid)
	}
	if err := s.writeBoardTx(ctx, tx, id, s.seedStagesLocked(i18n.FromContext(ctx))); err != nil {
		return BoardInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return BoardInfo{}, err
	}
	if err := s.reloadLocked(ctx); err != nil {
		return BoardInfo{}, err
	}
	seeded, _ := s.boardInfoLocked(id)
	return seeded, nil
}

func (s *Store) AdoptFallbackBoards(ctx context.Context, wid string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adoptFallbackBoardsLocked(ctx, wid)
}

// Only adopt when the install has exactly one workspace and it is wid: with two,
// FallbackWorkspaceID is where
// unscoped writes land, and moving its boards would hand one tenant another's flow.
func (s *Store) adoptFallbackBoardsLocked(ctx context.Context, wid string) (bool, error) {
	if wid == "" || wid == FallbackWorkspaceID {
		return false, nil
	}
	hasWorkspaces, err := migrate.ColumnExists(ctx, s.db, "workspaces", "id")
	if err != nil {
		return false, apperr.Wrap(err, "stage.errors.inspect_column", "column", "workspaces.id")
	}
	if !hasWorkspaces {
		return false, nil
	}
	var sole string
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM workspaces WHERE (SELECT COUNT(*) FROM workspaces) = 1`).Scan(&sole)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, apperr.Wrap(err, "stage.errors.read_workspaces")
	case sole != wid:
		return false, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, s.rb(
		`UPDATE boards SET workspace_id = ? WHERE workspace_id = ?`), wid, FallbackWorkspaceID)
	if err != nil {
		return false, apperr.Wrap(err, "stage.errors.adopt_boards", "workspace", wid)
	}
	moved, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if moved == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`UPDATE task_types SET workspace_id = ? WHERE workspace_id = ?`), wid, FallbackWorkspaceID); err != nil {
		return false, apperr.Wrap(err, "stage.errors.adopt_task_types", "workspace", wid)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if err := s.reloadLocked(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// Never copy another board's labels here: that would leak another workspace's column names to a new
// tenant.
func (s *Store) seedStagesLocked(locale string) []types.StageInfo {
	return DefaultsIn(locale)
}

func (s *Store) workspaceDefaultLocked(wid string) (BoardInfo, bool) {
	var first BoardInfo
	found := false
	for _, b := range s.state.boards {
		if b.WorkspaceID != wid {
			continue
		}
		if b.Default {
			return b, true
		}
		if !found {
			first, found = b, true
		}
	}
	return first, found
}

func (s *Store) freeBoardIDLocked(wid string) (string, error) {
	base := DefaultBoardID
	if s.boardExistsLocked(base) {
		base = "board_" + slugWorkspace(wid)
	}
	if !s.boardExistsLocked(base) {
		return base, nil
	}
	for n := 2; n <= 9; n++ {
		if candidate := fmt.Sprintf("%s_%d", base, n); !s.boardExistsLocked(candidate) {
			return candidate, nil
		}
	}
	return "", apperr.Conflict("stage.errors.no_free_board_id", "workspace", wid)
}

func slugWorkspace(wid string) string {
	out := make([]rune, 0, len(wid))
	for _, r := range wid {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		default:
			out = append(out, '_')
		}
		if len(out) == 24 {
			break
		}
	}
	if len(out) == 0 {
		return "ws"
	}
	return string(out)
}

func (s *Store) reload(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked(ctx)
}

func (s *Store) reloadLocked(ctx context.Context) error {
	catalog, err := s.loadCatalog(ctx)
	if err != nil {
		return err
	}
	boards, err := s.loadBoards(ctx)
	if err != nil {
		return err
	}
	members, err := s.loadMemberships(ctx)
	if err != nil {
		return err
	}
	typeRows, err := s.loadTypeRows(ctx)
	if err != nil {
		return err
	}

	state := &boardState{
		boards:             boards,
		byID:               make(map[string]Registry, len(boards)),
		byIDType:           map[string]Registry{},
		types:              map[string][]TaskTypeInfo{},
		defaultByWorkspace: map[string]string{},
	}
	firstOfWorkspace := map[string]string{}
	seen := make(map[types.Stage]bool, len(catalog))
	seenType := make(map[string]bool, len(typeRows.catalog))
	for i, b := range boards {
		cols := make([]types.StageInfo, 0, len(members[b.ID]))
		for j := range members[b.ID] {
			st := members[b.ID][j]
			if ident, ok := catalog[st.ID]; ok && st.Label == "" {
				st.Label, st.Color = ident.Label, ident.Color
			}
			cols = append(cols, st)
			if !seen[st.ID] {
				seen[st.ID] = true
				// The install-wide catalog carries the template identity, never a board's: the
				// first board is some tenant's.
				entry := st
				if ident, ok := catalog[st.ID]; ok {
					entry.Label, entry.Color = ident.Label, ident.Color
				}
				state.catalog = append(state.catalog, entry)
			}
		}
		state.byID[b.ID] = NewSnapshot(cols)
		boards[i].StageCount = len(cols)
		state.noteDefault(b, firstOfWorkspace)
		state.addTypes(b.ID, b.WorkspaceID, cols, typeRows, seenType)
	}
	state.settleDefaults(firstOfWorkspace, boards)
	s.state = state
	return nil
}

func (st *boardState) noteDefault(b BoardInfo, standby map[string]string) {
	if b.Default {
		if st.defaultID == "" {
			st.defaultID = b.ID
		}
		if _, ok := st.defaultByWorkspace[b.WorkspaceID]; !ok {
			st.defaultByWorkspace[b.WorkspaceID] = b.ID
		}
	}
	if _, ok := standby[b.WorkspaceID]; !ok {
		standby[b.WorkspaceID] = b.ID
	}
}

func (st *boardState) settleDefaults(standby map[string]string, boards []BoardInfo) {
	for wid, id := range standby {
		if _, ok := st.defaultByWorkspace[wid]; !ok {
			st.defaultByWorkspace[wid] = id
		}
	}
	if st.defaultID == "" && len(boards) > 0 {
		st.defaultID = boards[0].ID
	}
}

func (s *Store) loadCatalog(ctx context.Context) (map[types.Stage]types.StageInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, color FROM stages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[types.Stage]types.StageInfo{}
	for rows.Next() {
		var st types.StageInfo
		var id string
		if err := rows.Scan(&id, &st.Label, &st.Color); err != nil {
			return nil, err
		}
		st.ID = types.Stage(id)
		out[st.ID] = st
	}
	return out, rows.Err()
}

func (s *Store) loadBoards(ctx context.Context) ([]BoardInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, label, description, workspace_id, position, is_default,
		        max_kick_backs, flow, flow_revision, flow_hash
		 FROM boards ORDER BY position ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BoardInfo
	for rows.Next() {
		var b BoardInfo
		var isDefault, flowRevision int
		var flowName, flowHash string
		if err := rows.Scan(&b.ID, &b.Label, &b.Description, &b.WorkspaceID, &b.Position, &isDefault,
			&b.MaxKickBacks, &flowName, &flowRevision, &flowHash); err != nil {
			return nil, err
		}
		b.Default = isDefault != 0
		if flowName != "" {
			b.Flow = &BoardFlow{Name: flowName, Revision: flowRevision, Hash: flowHash}
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) loadMemberships(ctx context.Context) (map[string][]types.StageInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT board_id, stage_id, position, label, color, default_agent, `+
		stageColumns+` FROM board_stages ORDER BY board_id ASC, position ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]types.StageInfo{}
	for rows.Next() {
		var boardID, id string
		var st types.StageInfo
		var autoAdv, gated, initial, terminal, ingest, push, openPR int
		var verdict, lockTests, release, mergeTarget, prDraft, prReady, noSkip int
		var approvals, kickBackTo, skipUnless string
		if err := rows.Scan(&boardID, &id, &st.Position, &st.Label, &st.Color, &st.DefaultAgent,
			&autoAdv, &gated, &initial, &terminal, &ingest, &push, &openPR,
			&st.WipGroup, &approvals, &st.OnChangesRequested, &kickBackTo,
			&verdict, &lockTests, &skipUnless, &release, &mergeTarget, &prDraft, &prReady, &noSkip); err != nil {
			return nil, err
		}
		st.AgentVerdict = verdict != 0
		st.LockTests = lockTests != 0
		st.SkipUnless = decodeSkipUnless(skipUnless)
		st.ReleaseResources = release != 0
		st.MergeTarget = mergeTarget != 0
		st.PRDraft = prDraft != 0
		st.PRReady = prReady != 0
		st.NoSkip = noSkip != 0
		reqs, err := decodeApprovals(approvals)
		if err != nil {
			return nil, apperr.Wrap(err, "stage.errors.decode_approvals", "board", boardID, "id", id)
		}
		st.RequiredApprovals = reqs
		st.ChangesKickBackTo = types.Stage(kickBackTo)
		st.ID = types.Stage(id)
		st.AutoAdvance = autoAdv != 0
		st.BlockedByFlags = gated != 0
		st.Initial = initial != 0
		st.Terminal = terminal != 0
		st.IngestFlags = ingest != 0
		st.PushBranch = push != 0
		st.OpenPR = openPR != 0
		out[boardID] = append(out[boardID], st)
	}
	return out, rows.Err()
}

// Every task-type table, read once per reload.
type typeRows struct {
	catalog map[string]TaskTypeInfo
	members map[string][]TaskTypeInfo
	skips   map[string][]types.Stage
	agents  map[string]map[types.Stage]string
}

func (s *Store) loadTypeRows(ctx context.Context) (*typeRows, error) {
	var tr typeRows
	var err error
	if tr.catalog, err = s.loadTypeCatalog(ctx); err != nil {
		return nil, err
	}
	if tr.members, err = s.loadTypeMemberships(ctx); err != nil {
		return nil, err
	}
	if tr.skips, err = s.loadTypeSkips(ctx); err != nil {
		return nil, err
	}
	if tr.agents, err = s.loadTypeAgents(ctx); err != nil {
		return nil, err
	}
	return &tr, nil
}

// Owner is the board's workspace; the catalog row's workspace_id only names whoever used the id
// first.
func (st *boardState) addTypes(boardID, workspaceID string, cols []types.StageInfo, tr *typeRows, seen map[string]bool) {
	members := tr.members[boardID]
	for i := range members {
		t := members[i]
		if ident, ok := tr.catalog[t.ID]; ok && t.Label == "" {
			t.Label, t.Icon, t.Color = ident.Label, ident.Icon, ident.Color
		}
		t.WorkspaceID = workspaceID
		if t.Label == "" {
			t.Label = t.ID
		}
		t.Skips = tr.skips[boardTypeKey(boardID, t.ID)]
		if t.Skips == nil {
			t.Skips = []types.Stage{}
		}
		t.Agents = tr.agents[boardTypeKey(boardID, t.ID)]
		st.types[boardID] = append(st.types[boardID], t)
		st.byIDType[boardTypeKey(boardID, t.ID)] = NewSnapshot(FilterStages(cols, t.Skips))
		if !seen[t.ID] {
			seen[t.ID] = true
			st.typeCatalog = append(st.typeCatalog, TaskTypeInfo{
				ID: t.ID, Label: t.Label, Icon: t.Icon, Color: t.Color,
				WorkspaceID: t.WorkspaceID, Position: t.Position, Skips: []types.Stage{},
			})
		}
	}
}

func (s *Store) loadTypeCatalog(ctx context.Context) (map[string]TaskTypeInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, label, icon, color, workspace_id, position FROM task_types`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]TaskTypeInfo{}
	for rows.Next() {
		var t TaskTypeInfo
		if err := rows.Scan(&t.ID, &t.Label, &t.Icon, &t.Color, &t.WorkspaceID, &t.Position); err != nil {
			return nil, err
		}
		out[t.ID] = t
	}
	return out, rows.Err()
}

func (s *Store) loadTypeMemberships(ctx context.Context) (map[string][]TaskTypeInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT board_id, type_id, position, is_default, label, icon, color
		 FROM board_task_types ORDER BY board_id ASC, position ASC, type_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]TaskTypeInfo{}
	for rows.Next() {
		var boardID string
		var t TaskTypeInfo
		var isDefault int
		if err := rows.Scan(&boardID, &t.ID, &t.Position, &isDefault, &t.Label, &t.Icon, &t.Color); err != nil {
			return nil, err
		}
		t.Default = isDefault != 0
		out[boardID] = append(out[boardID], t)
	}
	return out, rows.Err()
}

func (s *Store) loadTypeSkips(ctx context.Context) (map[string][]types.Stage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT board_id, type_id, stage_id FROM board_type_skips`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]types.Stage{}
	for rows.Next() {
		var boardID, typeID, stageID string
		if err := rows.Scan(&boardID, &typeID, &stageID); err != nil {
			return nil, err
		}
		key := boardTypeKey(boardID, typeID)
		out[key] = append(out[key], types.Stage(stageID))
	}
	return out, rows.Err()
}

func (s *Store) loadTypeAgents(ctx context.Context) (map[string]map[types.Stage]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT board_id, type_id, stage_id, agent FROM board_type_agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[types.Stage]string{}
	for rows.Next() {
		var boardID, typeID, stageID, agent string
		if err := rows.Scan(&boardID, &typeID, &stageID, &agent); err != nil {
			return nil, err
		}
		key := boardTypeKey(boardID, typeID)
		if out[key] == nil {
			out[key] = map[types.Stage]string{}
		}
		out[key][types.Stage(stageID)] = agent
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) current() *boardState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Store) Board(id string) Registry { return s.current().board(id) }

func (s *Store) BoardList() []BoardInfo {
	st := s.current()
	out := make([]BoardInfo, len(st.boards))
	copy(out, st.boards)
	return out
}

// May answer with another tenant's flow; callers that know the workspace must use
// DefaultBoardIDFor.
func (s *Store) DefaultBoardID() string { return s.current().defaultID }

func (s *Store) DefaultBoardIDFor(wid string) string {
	st := s.current()
	if id, ok := st.defaultByWorkspace[wid]; ok {
		return id
	}
	return st.defaultID
}

func (s *Store) Catalog() []types.StageInfo {
	st := s.current()
	out := make([]types.StageInfo, len(st.catalog))
	copy(out, st.catalog)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

func (s *Store) HasBoard(id string) bool {
	_, ok := s.current().byID[id]
	return ok
}

func (s *Store) BoardFor(boardID, typeID string) Registry {
	return s.current().boardFor(boardID, typeID)
}

func (s *Store) BoardTypes(boardID string) []TaskTypeInfo {
	return copyTypes(s.current().types[boardID])
}

func (s *Store) TypeCatalog() []TaskTypeInfo {
	return copyTypes(s.current().typeCatalog)
}

func (s *Store) HasBoardType(boardID, typeID string) bool {
	if typeID == "" {
		return true
	}
	_, ok := s.current().byIDType[boardTypeKey(boardID, typeID)]
	return ok
}

func copyTypes(in []TaskTypeInfo) []TaskTypeInfo {
	out := make([]TaskTypeInfo, len(in))
	copy(out, in)
	for i := range out {
		skips := make([]types.Stage, len(in[i].Skips))
		copy(skips, in[i].Skips)
		out[i].Skips = skips
		if in[i].Agents != nil {
			out[i].Agents = maps.Clone(in[i].Agents)
		}
	}
	return out
}

// Store must not implement Registry: a Registry-typed field would silently answer for the default
// board.
func (s *Store) DefaultBoard() Registry {
	st := s.current()
	return st.board(st.defaultID)
}
