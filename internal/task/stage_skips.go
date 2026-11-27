package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Per-task column skips. The refinement stage's agent (through flags.yaml) or a person decides that
// this task does not need a column; auto-advance then passes over it, as it does over a column
// whose skip_unless the task's surface does not meet. Any later agent may only take a skip away.

// Shares the task store's single sequence with Migrations. Expand only: a binary from before it
// never reads the column and its inserts take the default.
var stageSkipsMigration = migrate.Migration{
	ID:  59,
	SQL: `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS stage_skips TEXT NOT NULL DEFAULT '[]'`,
}

// One column the task skips.
type StageSkip struct {
	Stage  Stage  `json:"stage"`
	Reason string `json:"reason"`
	// auth.Author.Label() of who set it: "agent:thulu-refiner", an email, "key:<name>".
	By string    `json:"by"`
	At time.Time `json:"at"`
}

const (
	MaxStageSkips = 16
	// In runes, after trimming.
	MaxSkipReason = 500
)

// Values of a stage_skip_removed payload's "cause".
const (
	// Someone asked (API, MCP).
	SkipCauseRemoved = "removed"
	// The task was moved into the column it skipped.
	SkipCauseEntered = "entered"
	// The branch changes UI files and the column waits on "ui".
	SkipCauseUIDiff = "ui_diff"
	// The daemon could not read the branch diff, so the column waiting on "ui" runs.
	SkipCauseDiffUnreadable = "diff_unreadable"
	// The refinement's flags.yaml ran again and left the skip out.
	SkipCauseRefinementDropped = "refinement_dropped"
	// The task moved to another board, whose columns its skips do not name.
	SkipCauseBoardChanged = "board_changed"
)

// Values of a stage_skip_added payload's "source".
const (
	SkipSourceFlagsYAML = "flags_yaml"
	SkipSourceAPI       = "api"
)

// The author label prefix of an agent (auth.Author.Label).
const agentLabelPrefix = "agent:"

// Whether an agent set the skip; a person's or an operator key's is anything else.
func agentSkip(sk StageSkip) bool { return strings.HasPrefix(sk.By, agentLabelPrefix) }

// A skip refused while others in the same request were applied.
type SkipRefusal struct {
	Stage Stage  `json:"stage"`
	Why   string `json:"why"`
	Err   error  `json:"-"`
}

// Who asks to add or remove a skip. RunStage and RunTask come from the run key's scope (or the run
// on the Docker path); a person's request leaves them empty.
type SkipActor struct {
	By       string
	Agent    bool
	Auditor  bool
	RunStage Stage
	RunTask  string
	// A delegated chat acts for its person on any task of the workspace.
	Delegated bool
}

// What an edit of the skips did.
type SkipsEdit struct {
	Added   []StageSkip
	Removed []StageSkip
	Refused []SkipRefusal
	// The task as the edit left it, changed or not.
	Task *Task
}

// One `skip:` entry of flags.yaml.
type skipYAML struct {
	Stage  string `yaml:"stage"`
	Reason string `yaml:"reason"`
}

// UnmarshalYAML takes a bare string as a skip with no reason, which is then refused for it: the
// refusal note tells the agent what was missing, where a parse error would throw away the file.
func (s *skipYAML) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		s.Stage = value.Value
		return nil
	}
	type plain skipYAML
	return value.Decode((*plain)(s))
}

// StageSkip is the task's entry for a column, whether or not the column may still be skipped.
func (t *Task) StageSkip(id Stage) (StageSkip, bool) {
	if t == nil {
		return StageSkip{}, false
	}
	for _, sk := range t.StageSkips {
		if sk.Stage == id {
			return sk, true
		}
	}
	return StageSkip{}, false
}

// SkipsAhead counts the columns after the task's own that it skips and that may still be skipped.
func (t *Task) SkipsAhead(reg stagepkg.Registry) int {
	if t == nil {
		return 0
	}
	n := 0
	for _, sk := range t.StageSkips {
		if info, ok := reg.Get(sk.Stage); ok && stagepkg.SkippableReason(info) == "" && skipPassedIn(reg, t, sk.Stage) == skipAhead {
			n++
		}
	}
	return n
}

// The skip auto-advance honors for a column: one the task set on a column that may still be
// skipped. A board edit that made the column unskippable leaves the entry in place, ignored.
func activeSkip(t *Task, info types.StageInfo) (StageSkip, bool) {
	sk, ok := t.StageSkip(info.ID)
	if !ok || stagepkg.SkippableReason(info) != "" {
		return StageSkip{}, false
	}
	return sk, true
}

type skipPlace int

const (
	skipOffBoard skipPlace = iota
	skipAhead
	skipCurrent
	skipPassed
)

// Where a column sits against the task's own, in the task's registry.
func skipPassedIn(reg stagepkg.Registry, t *Task, id Stage) skipPlace {
	info, ok := reg.Get(id)
	cur, curOK := reg.Get(t.Stage)
	if !ok || !curOK || info.Virtual {
		return skipOffBoard
	}
	switch {
	case info.Position > cur.Position:
		return skipAhead
	case info.Position == cur.Position:
		return skipCurrent
	}
	return skipPassed
}

// The column whose agent may add skips: the one that reads flags.yaml.
func refinementStage(reg stagepkg.Registry) Stage {
	stages := reg.List()
	for i := range stages {
		if stages[i].IngestFlags {
			return stages[i].ID
		}
	}
	return ""
}

// MayAddSkips says whether the actor may add skips to t. A person, an operator key and a dev-mode
// user may; an agent only while it runs the refinement stage (ingest_flags) of this very task. The
// auditor judges that stage and never writes its spec; a mention, a chat or a delegated chat has no
// stage, and a parent's refiner does not decide for a subtask.
func MayAddSkips(reg stagepkg.Registry, t *Task, a SkipActor) error {
	if !a.Agent {
		return nil
	}
	refused := apperr.Forbidden("task.errors.skip_add_refiner_only", "stage", string(refinementStage(reg)))
	if a.Auditor || t == nil || a.RunTask != t.ID {
		return refused
	}
	if info, ok := reg.Get(a.RunStage); !ok || !info.IngestFlags || t.Stage != a.RunStage {
		return refused
	}
	return nil
}

// MayRemoveSkips says whether the actor may take a skip away from t. A person may on any task; an
// agent only on the task its run works on, since a removal can send that task back. A run on a
// parent or a sibling reaches the task's other writes, but not its columns. A delegated chat acts
// for its person.
func MayRemoveSkips(t *Task, a SkipActor) error {
	if !a.Agent || a.Delegated || (t != nil && a.RunTask == t.ID) {
		return nil
	}
	return apperr.Forbidden("task.errors.skip_remove_own_task")
}

func validSkipReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", apperr.Invalid("task.errors.skip_reason_required")
	}
	if utf8.RuneCountInString(reason) > MaxSkipReason {
		return "", apperr.Invalid("task.errors.skip_reason_too_long", "max", MaxSkipReason)
	}
	return reason, nil
}

// clipReason keeps a reason the daemon writes itself within MaxSkipReason.
func clipReason(reason string) string {
	if utf8.RuneCountInString(reason) <= MaxSkipReason {
		return reason
	}
	r := []rune(reason)
	return string(r[:MaxSkipReason-1]) + "…"
}

// skipWhyLabel names, for the reader, why a column is never skippable (stage.SkippableReason).
func skipWhyLabel(why string) error {
	switch why {
	case stagepkg.SkipWhyInitial:
		return apperr.New("task.labels.skip_why_initial")
	case stagepkg.SkipWhyTerminal:
		return apperr.New("task.labels.skip_why_terminal")
	case stagepkg.SkipWhyVirtual:
		return apperr.New("task.labels.skip_why_virtual")
	case stagepkg.SkipWhyNoSkip:
		return apperr.New("task.labels.skip_why_no_skip")
	case stagepkg.SkipWhyRefinement:
		return apperr.New("task.labels.skip_why_refinement")
	case stagepkg.SkipWhyApprovals:
		return apperr.New("task.labels.skip_why_approvals")
	default:
		return apperr.New("task.labels.skip_why_pull_request")
	}
}

// validateSkipAdd checks one new entry against the task as it stands. A refused entry means the
// column runs, which is the safe direction.
func validateSkipAdd(reg stagepkg.Registry, t *Task, current []StageSkip, sk StageSkip) error {
	if _, err := validSkipReason(sk.Reason); err != nil {
		return err
	}
	info, ok := reg.Get(sk.Stage)
	if !ok {
		return apperr.Invalid("task.errors.skip_stage_unknown", "stage", string(sk.Stage))
	}
	switch why := stagepkg.SkippableReason(info); why {
	case "":
	case stagepkg.SkipWhyNoSkip:
		return apperr.Conflict("task.errors.skip_no_skip", "stage", string(sk.Stage))
	default:
		return apperr.Conflict("task.errors.skip_not_skippable", "stage", string(sk.Stage),
			"why", skipWhyLabel(why))
	}
	if cur, _ := reg.Get(t.Stage); cur.Terminal {
		return apperr.Conflict("task.errors.skip_task_finished")
	}
	if skipPassedIn(reg, t, sk.Stage) != skipAhead {
		return apperr.Conflict("task.errors.skip_not_ahead", "stage", string(sk.Stage))
	}
	if !slices.ContainsFunc(current, func(c StageSkip) bool { return c.Stage == sk.Stage }) && len(current) >= MaxStageSkips {
		return apperr.Invalid("task.errors.skip_too_many", "max", MaxStageSkips)
	}
	return nil
}

func skipIndex(skips []StageSkip, id Stage) int {
	return slices.IndexFunc(skips, func(sk StageSkip) bool { return sk.Stage == id })
}

func marshalStageSkips(skips []StageSkip) (string, error) {
	if skips == nil {
		skips = []StageSkip{}
	}
	b, err := json.Marshal(skips)
	if err != nil {
		return "", apperr.Wrap(err, "task.errors.marshal_column", "column", "stage_skips")
	}
	return string(b), nil
}

func (s *Store) writeStageSkips(ctx context.Context, tx *sql.Tx, t *Task) error {
	raw, err := marshalStageSkips(t.StageSkips)
	if err != nil {
		return err
	}
	t.UpdatedAt = time.Now().UTC()
	_, err = tx.ExecContext(ctx, s.rb(`UPDATE tasks SET stage_skips = ?, updated_at = ? WHERE id = ?`), raw, t.UpdatedAt, t.ID)
	return err
}

// UpsertStageSkips adds the entries the actor may add, one by one: a wrong entry is refused alone
// and the column it names runs. An entry for a column already skipped replaces its reason, except
// that an agent leaves a person's entry as it is. The whole call fails with Forbidden when the actor may not add skips at all (MayAddSkips).
func (s *Store) UpsertStageSkips(ctx context.Context, taskID string, actor SkipActor, in []StageSkip, source string) (SkipsEdit, error) {
	return s.editStageSkips(ctx, taskID, actor, in, source, false)
}

// ReplaceAgentStageSkips is flags.yaml's edit: in becomes the agents' skips. Entries an agent set
// that in leaves out (or refuses) go; a person's stay.
func (s *Store) ReplaceAgentStageSkips(ctx context.Context, taskID string, actor SkipActor, in []StageSkip, source string) (SkipsEdit, error) {
	return s.editStageSkips(ctx, taskID, actor, in, source, true)
}

func (s *Store) editStageSkips(
	ctx context.Context, taskID string, actor SkipActor, in []StageSkip, source string, replaceAgent bool,
) (SkipsEdit, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SkipsEdit{}, apperr.Wrap(err, "task.errors.update_begin", "id", taskID)
	}
	defer func() { _ = tx.Rollback() }()
	t, err := s.getRawTx(ctx, tx, taskID)
	if err != nil {
		return SkipsEdit{}, err
	}
	reg := s.boardFor(t)
	if err := MayAddSkips(reg, t, actor); err != nil {
		return SkipsEdit{}, err
	}
	out, skips := mergeStageSkips(reg, t, actor, in, replaceAgent, time.Now().UTC())
	if len(out.Added) == 0 && len(out.Removed) == 0 {
		out.Task = t
		return out, nil
	}
	t.StageSkips = skips
	if err := s.writeStageSkips(ctx, tx, t); err != nil {
		return SkipsEdit{}, err
	}
	if err := tx.Commit(); err != nil {
		return SkipsEdit{}, apperr.Wrap(err, "task.errors.update_commit", "id", taskID)
	}
	for _, sk := range out.Added {
		logx.Warn(s.RecordEvent(ctx, taskID, EventStageSkipAdded, "", actor.By,
			i18n.Text("task.labels.stage_skip_added", "stage", string(sk.Stage), "reason", sk.Reason),
			map[string]any{"stage": string(sk.Stage), "reason": sk.Reason, "by": sk.By, "source": source}),
			"task.log.record_stage_skip", "task", taskID)
	}
	for _, sk := range out.Removed {
		s.recordSkipRemoved(ctx, taskID, sk, actor.By, SkipCauseRefinementDropped,
			i18n.Text("task.skips.reasons.refinement_dropped"), false, false)
	}
	out.Task = t
	s.notify(ChangeUpdated, t)
	return out, nil
}

// mergeStageSkips works out the task's skips after an edit, without writing them. With
// replaceAgent the agents' entries become in: the ones it leaves out or refuses go.
func mergeStageSkips(reg stagepkg.Registry, t *Task, actor SkipActor, in []StageSkip, replaceAgent bool, now time.Time) (SkipsEdit, []StageSkip) {
	var out SkipsEdit
	skips := slices.Clone(t.StageSkips)
	if replaceAgent {
		named := map[Stage]bool{}
		for _, sk := range in {
			named[sk.Stage] = true
		}
		skips = dropAgentSkips(skips, func(sk StageSkip) bool { return named[sk.Stage] }, &out.Removed)
	}
	accepted := map[Stage]bool{}
	for _, sk := range in {
		sk.Reason = strings.TrimSpace(sk.Reason)
		sk.By, sk.At = actor.By, now
		if err := validateSkipAdd(reg, t, skips, sk); err != nil {
			out.Refused = append(out.Refused, SkipRefusal{Stage: sk.Stage, Why: apperr.Text(err), Err: err})
			continue
		}
		accepted[sk.Stage] = true
		i := skipIndex(skips, sk.Stage)
		switch {
		case i < 0:
			skips = append(skips, sk)
		case actor.Agent && !agentSkip(skips[i]):
			// A person's skip stays theirs: rewritten in the agent's name, the next flags.yaml that
			// left the column out would drop it.
			continue
		case skips[i].By == sk.By && skips[i].Reason == sk.Reason:
			// The same skip again, as a refiner's rerun writes it: nothing changes or is recorded.
			continue
		default:
			skips[i] = sk
		}
		out.Added = append(out.Added, sk)
	}
	if replaceAgent {
		// An entry refused this time does not keep the one from the last run: the column runs.
		skips = dropAgentSkips(skips, func(sk StageSkip) bool { return accepted[sk.Stage] }, &out.Removed)
	}
	return out, skips
}

// dropAgentSkips keeps a person's skips and the agents' ones keep accepts; the rest go to removed.
func dropAgentSkips(skips []StageSkip, keep func(StageSkip) bool, removed *[]StageSkip) []StageSkip {
	kept := skips[:0:0]
	for _, sk := range skips {
		if agentSkip(sk) && !keep(sk) {
			*removed = append(*removed, sk)
			continue
		}
		kept = append(kept, sk)
	}
	return kept
}

// RemoveStageSkip takes the task's skip of a column away. passed says the task already went past
// that column, so whoever asked expects it to go back there: the caller moves it (Dispatcher
// RemoveStageSkip). deferred only marks the event, for a move that waits until the run asking ends.
func (s *Store) RemoveStageSkip(
	ctx context.Context, taskID string, stage Stage, by, reason, cause string, deferred bool,
) (StageSkip, bool, *Task, error) {
	reason, err := validSkipReason(reason)
	if err != nil {
		return StageSkip{}, false, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StageSkip{}, false, nil, apperr.Wrap(err, "task.errors.update_begin", "id", taskID)
	}
	defer func() { _ = tx.Rollback() }()
	t, err := s.getRawTx(ctx, tx, taskID)
	if err != nil {
		return StageSkip{}, false, nil, err
	}
	i := skipIndex(t.StageSkips, stage)
	if i < 0 {
		return StageSkip{}, false, nil, apperr.NotFound("task.errors.skip_not_skipped", "stage", string(stage))
	}
	reg := s.boardFor(t)
	passed := skipPassedIn(reg, t, stage) == skipPassed
	if passed {
		// The branch is merged and the worktree gone, as for a send-back from such a stage: a
		// broken deploy is a new task.
		if cur, _ := reg.Get(t.Stage); cur.Terminal || cur.ReleaseResources {
			return StageSkip{}, false, nil, apperr.Conflict("task.errors.skip_return_released", "stage", string(stage))
		}
	}
	removed := t.StageSkips[i]
	t.StageSkips = slices.Delete(slices.Clone(t.StageSkips), i, i+1)
	if err := s.writeStageSkips(ctx, tx, t); err != nil {
		return StageSkip{}, false, nil, err
	}
	if err := tx.Commit(); err != nil {
		return StageSkip{}, false, nil, apperr.Wrap(err, "task.errors.update_commit", "id", taskID)
	}
	s.recordSkipRemoved(ctx, taskID, removed, by, cause, reason, passed, deferred && passed)
	s.notify(ChangeUpdated, t)
	return removed, passed, t, nil
}

// recordSkipRemoved records that sk went: actor took it away, reason says why.
func (s *Store) recordSkipRemoved(ctx context.Context, taskID string, sk StageSkip, actor, cause, reason string, passed, deferred bool) {
	if actor == "" {
		actor = "octopus"
	}
	logx.Warn(s.RecordEvent(ctx, taskID, EventStageSkipRemoved, "", actor,
		i18n.Text("task.labels.stage_skip_removed", "stage", string(sk.Stage), "reason", reason),
		map[string]any{"stage": string(sk.Stage), "reason": reason, "by": actor, "skipped_by": sk.By,
			"cause": cause, "passed": passed, "deferred": deferred}),
		"task.log.record_stage_skip", "task", taskID)
}

// skipsLeftByMove drops what a move makes moot: every skip when the task changes board (the
// columns are another board's), else the skip of the column the task was just moved into. A task
// never sits in a column it skips; auto-advance never enters one, so any other way in (a person, a
// send-back, changes requested) means the column runs.
func skipsLeftByMove(prevStage Stage, prevBoard string, t *Task) (dropped []StageSkip, cause string) {
	if len(t.StageSkips) == 0 {
		return nil, ""
	}
	if t.BoardID != prevBoard {
		dropped, t.StageSkips = t.StageSkips, []StageSkip{}
		return dropped, SkipCauseBoardChanged
	}
	if t.Stage == prevStage {
		return nil, ""
	}
	i := skipIndex(t.StageSkips, t.Stage)
	if i < 0 {
		return nil, ""
	}
	dropped = []StageSkip{t.StageSkips[i]}
	t.StageSkips = slices.Delete(slices.Clone(t.StageSkips), i, i+1)
	return dropped, SkipCauseEntered
}

func (s *Store) recordSkipsLeftByMove(ctx context.Context, taskID, by, cause string, dropped []StageSkip) {
	who := by
	if who == "" {
		who = "octopus"
	}
	reason := i18n.Text("task.skips.reasons.entered", "who", who)
	if cause == SkipCauseBoardChanged {
		reason = i18n.Text("task.skips.reasons.board_changed")
	}
	for _, sk := range dropped {
		s.recordSkipRemoved(ctx, taskID, sk, who, cause, reason, false, false)
	}
}

// PendingSkipReturns lists the columns whose skip an agent removed while running stage, after the
// task had passed them: the move back waits for the end of that run. Only removals since the task
// last entered stage count, so a person moving the task meanwhile cancels them.
func (s *Store) PendingSkipReturns(ctx context.Context, taskID string, stage Stage) []Stage {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT DISTINCT payload::jsonb ->> 'stage' FROM workflow_events
		 WHERE task_id = ? AND kind = ? AND (payload::jsonb ->> 'deferred') = 'true'
		   AND created_at > COALESCE((
		     SELECT max(created_at) FROM workflow_events
		     WHERE task_id = ? AND kind = ? AND (payload::jsonb ->> 'to') = ?
		   ), '-infinity'::timestamptz)`),
		taskID, EventStageSkipRemoved, taskID, EventStageEntered, string(stage))
	if err != nil {
		logx.Warn(err, "task.log.pending_skip_returns", "task", taskID)
		return nil
	}
	defer rows.Close()
	var out []Stage
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			logx.Warn(err, "task.log.pending_skip_returns", "task", taskID)
			return out
		}
		if id.Valid && id.String != "" {
			out = append(out, Stage(id.String))
		}
	}
	logx.Warn(rows.Err(), "task.log.pending_skip_returns", "task", taskID)
	return out
}

// LatestFlowSpec is the risk and size the last flags.yaml (or person) set, from its flow_flags_set
// event; "" when none did. The event the daemon records when the diff adds "ui" carries no risk and
// is passed over, or it would wipe the risk the spec set.
func (s *Store) LatestFlowSpec(ctx context.Context, taskID string) (risk, size string) {
	var raw string
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT payload FROM workflow_events WHERE task_id = ? AND kind = ?
		   AND (payload::jsonb -> 'risk') IS NOT NULL
		 ORDER BY created_at DESC LIMIT 1`), taskID, EventFlowFlagsSet).Scan(&raw)
	if err != nil {
		return "", ""
	}
	var p struct {
		Risk string `json:"risk"`
		Size string `json:"size"`
	}
	if json.Unmarshal([]byte(raw), &p) != nil {
		return "", ""
	}
	return p.Risk, p.Size
}

// applyFlowSkips writes flags.yaml's `skip:` list. No key leaves the skips as they are; a refused
// author changes nothing and gets a note saying so.
func (s *Store) applyFlowSkips(ctx context.Context, taskID string, actor SkipActor, skip *[]skipYAML, out *FlagsImport) {
	if skip == nil {
		return
	}
	in := make([]StageSkip, 0, len(*skip))
	for _, y := range *skip {
		in = append(in, StageSkip{Stage: Stage(strings.TrimSpace(y.Stage)), Reason: y.Reason})
	}
	res, err := s.ReplaceAgentStageSkips(ctx, taskID, actor, in, SkipSourceFlagsYAML)
	if err != nil {
		if apperr.KindOf(err) != apperr.KindForbidden {
			logx.Warn(err, "task.log.stage_skips_flags", "task", taskID)
			return
		}
		names := make([]string, 0, len(in))
		for _, sk := range in {
			out.SkipsRefused = append(out.SkipsRefused, SkipRefusal{Stage: sk.Stage, Why: apperr.Text(err), Err: err})
			names = append(names, "`"+string(sk.Stage)+"`")
		}
		if len(names) > 0 {
			s.skipsNote(ctx, taskID, NoteOf("task.comments.skips_refused", "list", strings.Join(names, ", "), "why", err))
		}
		return
	}
	out.StageSkips = res.Task.StageSkips
	out.SkipsRefused = res.Refused
	if len(res.Refused) > 0 {
		lines := make([]string, 0, len(res.Refused))
		for _, r := range res.Refused {
			lines = append(lines, "- `"+string(r.Stage)+"`: "+r.Why)
		}
		s.skipsNote(ctx, taskID, NoteOf("task.comments.skips_refused_entries", "list", strings.Join(lines, "\n")))
	}
}

func (s *Store) skipsNote(ctx context.Context, taskID string, n Note) {
	_, err := s.AddNote(ctx, taskID, "", SourceSystem, "octopus", n)
	logx.Warn(err, "task.log.stage_skips_flags", "task", taskID)
}
