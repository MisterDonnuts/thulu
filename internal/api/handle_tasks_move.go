package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/deploycontents"
	ghrest "github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// A deploy that carries more is rare, and each task can cost a compare against GitHub.
const (
	batchMoveMax     = 50
	batchMoveNoteMax = 2000
)

// Why a task of the batch stayed where it was. Part of the API: the release agent reads them.
const (
	moveSkipNotFound       = "not_found"
	moveSkipOtherBoard     = "other_board"
	moveSkipWrongStage     = "wrong_stage"
	moveSkipArchived       = "archived"
	moveSkipRunning        = "running"
	moveSkipOwnTask        = "own_task"
	moveSkipTypeSkipsStage = "type_skips_stage"
	moveSkipJumpsApprovals = "jumps_approvals"
	moveSkipNotInProject   = "not_in_project"
	moveSkipOtherProjects  = "other_projects"
	moveSkipNotInDeploy    = "not_in_deploy"
	moveSkipNoMergeSHA     = "no_merge_sha"
	moveSkipReverted       = "reverted"
	moveSkipUnknown        = "inclusion_unknown"
	moveSkipFailed         = "failed"
)

type batchMoveReq struct {
	BoardID   string     `json:"board_id"`
	TaskIDs   []string   `json:"task_ids"`
	FromStage task.Stage `json:"from_stage"`
	ToStage   task.Stage `json:"to_stage"`
	Note      string     `json:"note"`
	// The deployed commit, recorded on each task; with require_included, every task's merge_sha
	// for project_id must be one of its ancestors.
	DeployedSHA     string `json:"deployed_sha,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
	RequireIncluded bool   `json:"require_included,omitempty"`
}

type batchMoved struct {
	ID    string     `json:"id"`
	Title string     `json:"title"`
	From  task.Stage `json:"from"`
	To    task.Stage `json:"to"`
}

type batchGated struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Approvals []string `json:"approvals"`
}

type batchSkipped struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

type batchMoveResp struct {
	Moved   []batchMoved   `json:"moved"`
	Gated   []batchGated   `json:"gated"`
	Skipped []batchSkipped `json:"skipped"`
}

func (r *batchMoveResp) skip(id, reason, detail string) {
	r.Skipped = append(r.Skipped, batchSkipped{ID: id, Reason: reason, Detail: detail})
}

// batchMove is a validated request: the board's stages, the caller, and the project when the move
// is tied to a deploy.
type batchMove struct {
	batchMoveReq
	boardID string
	reg     stagepkg.Registry
	actor   string
	// The run's own task, when the caller is a run: it never moves itself.
	ownTask string
	proj    *project.Project
}

// POST /tasks/batch-move
//
// Moves several tasks of one board forward from one stage to another in a single request, each
// through the stage-exit gate: a task an approval holds is reported, never moved past it. Under
// /tasks, not /boards/{id}, because it writes tasks: a run's key holds tasks:write, never
// flows:write.
func (s *Server) handleBatchMoveTasks(w http.ResponseWriter, r *http.Request) {
	var req batchMoveReq
	if !decodeBody(w, r, &req) {
		return
	}
	boardID := strings.TrimSpace(req.BoardID)
	if boardID == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.release_batch.errors.board_required")
		return
	}
	if _, ok := s.boardForCaller(w, r, boardID); !ok {
		return
	}
	if scope := auth.APIKeyRunScope(r.Context()); scope.IsRun() && s.runOwnBoard(r.Context(), scope) != boardID {
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_own_board_only")
		return
	}
	m, ok := s.validBatchMove(w, r, boardID, req)
	if !ok {
		return
	}
	tasks, resp := s.batchCandidates(r, m)
	if m.RequireIncluded && len(tasks) > 0 {
		var err error
		if tasks, err = s.keepIncluded(r.Context(), m, tasks, &resp); err != nil {
			writeError(w, r, 0, err)
			return
		}
	}
	for _, t := range tasks {
		s.moveOneOfBatch(r.Context(), m, t, &resp)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) validBatchMove(w http.ResponseWriter, r *http.Request, boardID string, req batchMoveReq) (batchMove, bool) {
	m := batchMove{batchMoveReq: req, boardID: boardID, reg: s.boards.Board(boardID)}
	m.TaskIDs = uniqueIDs(req.TaskIDs)
	m.Note = strings.TrimSpace(req.Note)
	m.DeployedSHA = strings.TrimSpace(req.DeployedSHA)
	m.ProjectID = strings.TrimSpace(req.ProjectID)
	if key, args := batchMoveInputError(m); key != "" {
		writeErrKey(w, r, http.StatusBadRequest, key, args...)
		return m, false
	}
	if key, args := forwardMoveError(m.reg, m.FromStage, m.ToStage); key != "" {
		writeErrKey(w, r, http.StatusBadRequest, key, args...)
		return m, false
	}
	if scope := auth.APIKeyRunScope(r.Context()); scope.IsRun() && !scope.Delegated() && !fromMergeTargetOn(m.reg, m.FromStage) {
		target, _ := mergeTargetOf(m.reg)
		writeErrKey(w, r, http.StatusForbidden, "api.release_batch.errors.run_before_merge_target",
			"stage", m.FromStage, "target", target)
		return m, false
	}
	if m.ProjectID != "" {
		proj, ok := s.projectForCaller(w, r, m.ProjectID)
		if !ok {
			return m, false
		}
		m.proj = proj
	}
	actor, ok := requireCredentialAuthor(w, r)
	if !ok {
		return m, false
	}
	m.actor = actor
	if scope := auth.APIKeyRunScope(r.Context()); scope.IsRun() {
		m.ownTask = scope.TaskID
	}
	return m, true
}

func batchMoveInputError(m batchMove) (string, []any) {
	switch {
	case len(m.TaskIDs) == 0 || len(m.TaskIDs) > batchMoveMax:
		return "api.release_batch.errors.task_ids_count", []any{"max", batchMoveMax, "count", len(m.TaskIDs)}
	case m.Note == "":
		return "api.release_batch.errors.note_required", nil
	case len([]rune(m.Note)) > batchMoveNoteMax:
		return "api.release_batch.errors.note_too_long", []any{"max", batchMoveNoteMax}
	case m.DeployedSHA != "" && !ghrest.ValidCommitSHA(m.DeployedSHA):
		return "api.release_batch.errors.sha_invalid", nil
	case m.RequireIncluded && (m.DeployedSHA == "" || m.ProjectID == ""):
		return "api.release_batch.errors.included_needs_deploy", nil
	}
	return "", nil
}

// forwardMoveError refuses a destination that is not a later column of the board: a batch never
// sends work back, and a virtual stage is not a column.
func forwardMoveError(reg stagepkg.Registry, from, to task.Stage) (string, []any) {
	fromInfo, okFrom := reg.Get(from)
	toInfo, okTo := reg.Get(to)
	switch {
	case !okFrom || fromInfo.Virtual:
		return "api.release_batch.errors.unknown_stage", []any{"stage", from}
	case !okTo || toInfo.Virtual:
		return "api.release_batch.errors.unknown_stage", []any{"stage", to}
	case toInfo.Position <= fromInfo.Position:
		return "api.release_batch.errors.not_forward", []any{"from", from, "to", to}
	}
	return "", nil
}

// A run's batch starts at the merge_target stage or after it: the columns before it hold the agents
// and verdicts that check the work, and a batch from there would carry tasks past them. A board
// without a merge_target has no such column, so a run moves nothing on it.
func fromMergeTargetOn(reg stagepkg.Registry, from task.Stage) bool {
	target, ok := mergeTargetOf(reg)
	if !ok {
		return false
	}
	fromInfo, _ := reg.Get(from)
	targetInfo, _ := reg.Get(target)
	return fromInfo.Position >= targetInfo.Position
}

func uniqueIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// batchCandidates reads each task and keeps those the batch may move. Another workspace's task
// answers not_found, like a missing one.
func (s *Server) batchCandidates(r *http.Request, m batchMove) ([]*task.Task, batchMoveResp) {
	resp := batchMoveResp{Moved: []batchMoved{}, Gated: []batchGated{}, Skipped: []batchSkipped{}}
	wid, unrestricted := s.callerWorkspace(r)
	var out []*task.Task
	for _, id := range m.TaskIDs {
		t, err := s.taskStore.Get(r.Context(), id)
		if err != nil || t == nil || !workspaceVisible(t.WorkspaceID, wid, unrestricted) {
			resp.skip(id, moveSkipNotFound, "")
			continue
		}
		if reason, detail := s.batchIneligible(m, t); reason != "" {
			resp.skip(id, reason, detail)
			continue
		}
		out = append(out, t)
	}
	return out, resp
}

func (s *Server) batchIneligible(m batchMove, t *task.Task) (string, string) {
	switch {
	case t.BoardID != m.boardID:
		return moveSkipOtherBoard, t.BoardID
	case t.ID == m.ownTask:
		return moveSkipOwnTask, ""
	case t.ArchivedAt != nil:
		return moveSkipArchived, ""
	case t.Stage != m.FromStage:
		return moveSkipWrongStage, string(t.Stage)
	case t.Status == task.StatusRunning:
		// Moving it would orphan the agent working on it.
		return moveSkipRunning, ""
	case !s.stagesFor(t).Valid(m.ToStage):
		return moveSkipTypeSkipsStage, t.TypeID
	}
	if jumped := task.JumpedRequirements(s.stagesFor(t), t.TypeID, m.FromStage, m.ToStage); len(jumped) > 0 {
		return moveSkipJumpsApprovals, strings.Join(jumped, ", ")
	}
	return projectIneligible(m, t)
}

// A task that changed other repositories too is in production only when each of them deployed it,
// and one deployed SHA speaks for one project.
func projectIneligible(m batchMove, t *task.Task) (string, string) {
	if m.ProjectID == "" {
		return "", ""
	}
	if !slices.Contains(t.ProjectIDs, m.ProjectID) {
		return moveSkipNotInProject, ""
	}
	if m.RequireIncluded && len(t.ProjectIDs) > 1 {
		var others []string
		for _, p := range t.ProjectIDs {
			if p != m.ProjectID {
				others = append(others, p)
			}
		}
		return moveSkipOtherProjects, strings.Join(others, ", ")
	}
	return "", ""
}

// keepIncluded drops the tasks whose merge commit the deployed SHA does not carry, or carries and
// later reverts.
func (s *Server) keepIncluded(ctx context.Context, m batchMove, tasks []*task.Task, resp *batchMoveResp) ([]*task.Task, error) {
	repo, _, err := s.deployRepo(ctx, m.proj)
	if err != nil {
		return nil, err
	}
	cands := make([]deploycontents.Candidate, len(tasks))
	for i, t := range tasks {
		cands[i] = deploycontents.Candidate{TaskID: t.ID, Title: t.Title, Stage: string(t.Stage), BoardID: t.BoardID,
			MergeSHA: t.MergeSHAs[m.ProjectID]}
	}
	rep, err := deploycontents.Build(ctx, repo, m.DeployedSHA, "", cands)
	if err != nil {
		return nil, err
	}
	var out []*task.Task
	for i := range rep.Tasks {
		res := &rep.Tasks[i]
		if reason := inclusionSkip(*res); reason != "" {
			resp.skip(res.TaskID, reason, res.Detail)
			continue
		}
		out = append(out, tasks[i])
	}
	return out, nil
}

func inclusionSkip(res deploycontents.TaskResult) string {
	switch {
	case res.Reason == deploycontents.ReasonNoMergeSHA:
		return moveSkipNoMergeSHA
	case res.Included == deploycontents.IncludedNo:
		return moveSkipNotInDeploy
	case res.Included == deploycontents.IncludedUnknown:
		return moveSkipUnknown
	case res.Reverted:
		return moveSkipReverted
	}
	return ""
}

func (s *Server) moveOneOfBatch(ctx context.Context, m batchMove, t *task.Task, resp *batchMoveResp) {
	extra := map[string]any{"note": m.Note}
	if m.DeployedSHA != "" {
		extra["deployed_sha"] = m.DeployedSHA
	}
	if m.ProjectID != "" {
		extra["project_id"] = m.ProjectID
	}
	_, v, err := s.moveTaskStage(ctx, t, stageMove{
		To: m.ToStage, Cause: task.GateRelease, Via: task.ViaReleaseBatch, Actor: m.actor, Extra: extra,
	})
	switch {
	case task.IsStageChanged(err):
		// Another mover got there first; the batch is optimistic and never moves twice.
		resp.skip(t.ID, moveSkipWrongStage, "")
	case err != nil:
		logx.Warn(err, "api.release_batch.log.move", "task", t.ID)
		resp.skip(t.ID, moveSkipFailed, apperr.LocalizeCtx(ctx, err))
	case !v.Passed:
		labels := make([]string, len(v.Blocking))
		for i := range v.Blocking {
			labels[i] = v.Blocking[i].Label
		}
		resp.Gated = append(resp.Gated, batchGated{ID: t.ID, Title: t.Title, Approvals: labels})
	default:
		resp.Moved = append(resp.Moved, batchMoved{ID: t.ID, Title: t.Title, From: m.FromStage, To: m.ToStage})
		s.batchMoveNote(ctx, m, t)
	}
}

func (s *Server) batchMoveNote(ctx context.Context, m batchMove, t *task.Task) {
	stage := s.taskDispatcher.StageNote(t, m.ToStage)
	n := task.NoteOf("api.release_batch.comments.moved", "by", m.actor, "stage", stage, "note", m.Note)
	if m.DeployedSHA != "" {
		n = task.NoteOf("api.release_batch.comments.moved_deploy", "by", m.actor, "stage", stage, "note", m.Note,
			"sha", shortSHA(m.DeployedSHA))
	}
	if _, err := s.taskStore.AddNote(ctx, t.ID, "", task.SourceSystem, "octopus", n); err != nil {
		logx.Warn(err, "api.release_batch.log.comment", "task", t.ID)
	}
}
