package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/patch"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

func mergeAssignments(base, overrides map[task.Stage]string) map[task.Stage]string {
	if len(overrides) == 0 {
		return base
	}
	out := make(map[task.Stage]string, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func keyedRefusal(ctx context.Context, fields map[string]any, key string, args ...any) map[string]any {
	out := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		out[k] = v
	}
	out["error"] = i18n.TCtx(ctx, key, args...)
	out["code"] = key
	return out
}

func errRefusal(ctx context.Context, fields map[string]any, err error) map[string]any {
	out := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		out[k] = v
	}
	out["error"] = apperr.LocalizeCtx(ctx, err)
	if code := apperr.Code(err); code != "" {
		out["code"] = code
	}
	return out
}

func (s *Server) repoRequirementError(ctx context.Context, t *task.Task, stage task.Stage, assignments map[task.Stage]string) (string, string, bool) {
	if s.hasLiveProject(ctx, t) {
		return "", "", false
	}
	agentLabel := assignments[stage]
	if agentLabel == "" {
		return "", "", false
	}
	discovered, err := s.agents(t.WorkspaceID).All()
	if err != nil {
		return "", "", false
	}
	for i := range discovered {
		if discovered[i].Type == agentLabel || discovered[i].Name == agentLabel {
			if discovered[i].WorksWithoutRepo {
				return "", "", false
			}
			return discovered[i].Name, agentLabel, true
		}
	}
	return "", "", false
}

// A real lookup, not len(): links can predate the delete-project cascade (databases written by
// older builds).
func (s *Server) hasLiveProject(ctx context.Context, t *task.Task) bool {
	for _, pid := range t.ProjectIDs {
		if _, err := s.projectStore.Get(ctx, pid); err == nil {
			return true
		}
	}
	return false
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	page, ok := pageFromQuery(w, r)
	if !ok {
		return
	}
	var (
		tasks []task.Task
		next  string
		err   error
	)
	wid, unrestricted := s.callerWorkspace(r)
	if !unrestricted && wid == "" {
		// An empty workspace would reach the SQL as "every workspace".
		setNextCursor(w, "")
		writeJSON(w, http.StatusOK, []task.Task{})
		return
	}
	parentID := r.URL.Query().Get("parent_id")
	includeSubtasks := parentID != "" || r.URL.Query().Get("include_subtasks") == "true"
	if parentID != "" {
		tasks, err = s.taskStore.ListChildren(r.Context(), parentID)
	} else {
		// Workspace filter must be in the query, not applied afterwards, or the cursor leaks other
		// tenants' rows.
		f := task.CardFilter{
			Board:    r.URL.Query().Get("board"),
			Stage:    r.URL.Query().Get("stage"),
			Project:  r.URL.Query().Get("project"),
			TopLevel: !includeSubtasks,
			Archived: r.URL.Query().Get("archived") == "true",
		}
		if !unrestricted {
			f.Workspace = wid
		}
		tasks, next, err = s.taskStore.ListPage(r.Context(), f, page)
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if !unrestricted && parentID != "" {
		visible := make([]task.Task, 0, len(tasks))
		for i := range tasks {
			if workspaceVisible(tasks[i].WorkspaceID, wid, false) {
				visible = append(visible, tasks[i])
			}
		}
		tasks = visible
	}
	setNextCursor(w, next)
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title            string                `json:"title"`
		Body             string                `json:"body"`
		ProjectIDs       []string              `json:"project_ids"`
		ProjectID        string                `json:"project_id"`
		Stage            task.Stage            `json:"stage,omitempty"`
		BoardID          string                `json:"board_id,omitempty"`
		TypeID           string                `json:"type_id,omitempty"`
		StageAssignments map[task.Stage]string `json:"stage_assignments,omitempty"`
		Tags             []string              `json:"tags,omitempty"`
		Priority         string                `json:"priority,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Title == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.title_required")
		return
	}
	// Back-compat: older clients send singular project_id.
	if len(body.ProjectIDs) == 0 && body.ProjectID != "" {
		body.ProjectIDs = []string{body.ProjectID}
	}

	// Out-of-scope projects are reported exactly like missing ones.
	templates, badProject := s.projectStageTemplates(r, body.ProjectIDs)
	if badProject != "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.project_not_found", "project", badProject)
		return
	}

	boardID, boardErr := s.resolveTaskBoard(r, body.BoardID, body.ProjectIDs)
	if boardErr != nil {
		writeError(w, r, http.StatusBadRequest, boardErr)
		return
	}

	typeID, typeErr := s.resolveTaskType(body.TypeID, boardID)
	if typeErr != nil {
		writeError(w, r, http.StatusBadRequest, typeErr)
		return
	}

	wid, ok := s.newTaskWorkspace(w, r, body.ProjectIDs, body.BoardID)
	if !ok {
		return
	}
	assigns := resolveAssignments(s.boards, boardID, typeID, templates, body.StageAssignments)

	t := &task.Task{
		Title:            body.Title,
		Body:             task.UnwrapMarkdownFence(body.Body),
		ProjectIDs:       body.ProjectIDs,
		StageAssignments: assigns,
		Tags:             body.Tags,
		Priority:         body.Priority,
		WorkspaceID:      wid,
		BoardID:          boardID,
		TypeID:           typeID,
		OriginTaskID:     runOriginTask(r),
	}
	// Gates run before the insert: a refusal after it left the task created, so a retry made a
	// second one.
	moveTo := body.Stage
	if moveTo != "" {
		t.Stage = s.stagesFor(t).Initial()
		if moveTo == t.Stage {
			moveTo = ""
		} else if status, refusal, ok := s.initialStageGate(r.Context(), t, moveTo); !ok {
			writeJSON(w, status, refusal)
			return
		}
	}
	if err := s.taskStore.Create(r.Context(), t); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.recordEvent(r.Context(), t.ID, task.EventTaskCreated, "", "user", t.Title, nil)
	s.emit(r, "task.created", "task.created", t.ID, "task", i18n.Text("api.tasks.audit.task_created", "title", t.Title),
		map[string]string{"task_id": t.ID, "title": t.Title})
	s.publishTaskAutomationEvent(context.WithoutCancel(r.Context()), automation.EventTaskCreated, t, nil)
	if moveTo != "" {
		s.applyInitialStage(r, t, moveTo)
	}
	full, err := s.taskStore.Get(r.Context(), t.ID)
	logx.Warn(err, "api.tasks.log.reload_after_create", "task", t.ID)
	writeJSON(w, http.StatusCreated, full)
}

// Every candidate, including project defaults, is checked against the caller's workspace; an
// invisible board reads as unknown.
func (s *Server) resolveTaskBoard(r *http.Request, explicit string, projectIDs []string) (string, error) {
	if explicit != "" {
		if !s.boardVisibleToCaller(r, explicit) {
			return "", apperr.Invalid("api.boards.errors.unknown_board", "board", explicit)
		}
		return explicit, nil
	}
	var candidates []string
	for _, pid := range projectIDs {
		proj, err := s.projectStore.Get(r.Context(), pid)
		if err != nil || proj.DefaultBoardID == "" {
			continue
		}
		if !slices.Contains(candidates, proj.DefaultBoardID) {
			candidates = append(candidates, proj.DefaultBoardID)
		}
	}
	switch len(candidates) {
	case 0:
		return s.callerDefaultBoard(r), nil
	case 1:
		if !s.boardVisibleToCaller(r, candidates[0]) {
			return s.callerDefaultBoard(r), nil
		}
		return candidates[0], nil
	default:
		return "", apperr.Invalid("api.tasks.errors.boards_disagree", "boards", strings.Join(candidates, ", "))
	}
}

func (s *Server) resolveTaskType(explicit, boardID string) (string, error) {
	if explicit != "" {
		if s.stageAdmin != nil && !s.stageAdmin.HasBoardType(boardID, explicit) {
			return "", apperr.Invalid("api.boards.errors.board_lacks_type", "board", boardID, "type", explicit)
		}
		return explicit, nil
	}
	return stagepkg.DefaultTaskType(s.boards.BoardTypes(boardID)), nil
}

func (s *Server) initialStageGate(ctx context.Context, t *task.Task, stage task.Stage) (int, any, bool) {
	if !s.boards.Board(t.BoardID).Valid(stage) {
		return http.StatusBadRequest, keyedRefusal(ctx, nil, "api.tasks.errors.invalid_stage", "stage", stage), false
	}
	if reg := s.stagesFor(t); !reg.Valid(stage) {
		return http.StatusConflict, keyedRefusal(ctx, map[string]any{
			"task_type":        t.TypeID,
			"available_stages": reg.List(),
		}, "api.tasks.errors.stage_skipped_by_type", "stage", stage, "type", t.TypeID), false
	}
	if _, label, blocked := s.repoRequirementError(ctx, t, stage, t.StageAssignments); blocked {
		return http.StatusConflict, keyedRefusal(ctx, map[string]any{
			"requires_repo": true,
			"stage":         string(stage),
		}, "api.tasks.errors.agent_needs_repo", "agent", label), false
	}
	return s.approvalGate(ctx, t, t.Stage, stage, task.GateCreate)
}

func (s *Server) applyInitialStage(r *http.Request, t *task.Task, stage task.Stage) {
	updated, err := s.taskStore.Update(r.Context(), t.ID, task.Patch{Stage: &stage})
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.initial_stage_failed", "stage", stage, "task", t.ID, "err", err))
		return
	}
	s.recordEvent(r.Context(), t.ID, task.EventStageEntered, "", "user",
		fmt.Sprintf("%s → %s", t.Stage, updated.Stage),
		map[string]string{"from": string(t.Stage), "to": string(updated.Stage)})
	s.taskDispatcher.HandleStageEntered(launchCtx(r.Context()), updated, t.Stage)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	// Only on single-task GET: it calls the container runtime, too costly for list endpoints.
	if s.taskDispatcher != nil {
		t.RunAlive, t.CanSteer = s.taskDispatcher.SteerState(t)
	}
	t.OrphanAgents = s.orphanAgentsOf(r.Context(), t.ID)
	if !t.IsSubtask() {
		if children, err := s.taskStore.ListChildren(r.Context(), t.ID); err != nil {
			logx.Warn(err, "api.tasks.log.list_children", "task", t.ID)
		} else if len(children) > 0 {
			t.Children = children
		}
	}
	writeJSON(w, http.StatusOK, s.taskWithAttention(r, t))
}

func taskCopyBase(src *task.Task, prefix string) *task.Task {
	title := src.Title
	if !strings.HasPrefix(title, prefix) {
		title = prefix + title
	}
	assigns := map[task.Stage]string{}
	for k, v := range src.StageAssignments {
		assigns[k] = v
	}
	return &task.Task{
		Title:            title,
		Body:             src.Body,
		ProjectIDs:       append([]string(nil), src.ProjectIDs...),
		StageAssignments: assigns,
		Tags:             append([]string(nil), src.Tags...),
		Priority:         src.Priority,
		WorkspaceID:      src.WorkspaceID,
		BoardID:          src.BoardID,
		TypeID:           src.TypeID,
	}
}

func (s *Server) handleCloneTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	src, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}

	clone := taskCopyBase(src, i18n.TCtx(r.Context(), "api.tasks.labels.copy_prefix"))
	clone.OriginTaskID = runOriginTask(r)
	if err := s.taskStore.Create(r.Context(), clone); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if err := s.copyTestCasesToClone(r, src.ID, clone.ID); err != nil {
		logx.Warn(s.taskStore.Delete(r.Context(), clone.ID), "api.tasks.log.rollback_clone", "task", clone.ID)
		writeError(w, r, http.StatusInternalServerError, apperr.Wrap(err, "api.tasks.errors.copy_test_cases_failed"))
		return
	}
	s.recordEvent(r.Context(), clone.ID, task.EventTaskCreated, "", "user",
		i18n.Text("api.tasks.timeline.cloned_from", "task", src.ID), map[string]string{"cloned_from": src.ID})

	full, getErr := s.taskStore.Get(r.Context(), clone.ID)
	logx.Warn(getErr, "api.tasks.log.reload_after_clone", "task", clone.ID)
	writeJSON(w, http.StatusCreated, full)
}

func (s *Server) handleForkTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	src, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}

	fork := taskCopyBase(src, i18n.Text("api.tasks.labels.fork_prefix"))
	fork.Stage = src.Stage
	fork.Status = task.StatusIdle
	fork.OriginTaskID = runOriginTask(r)
	if err := s.taskStore.Create(r.Context(), fork); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	if err := s.taskStore.ForkData(r.Context(), src.ID, fork.ID); err != nil {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.fork_data_failed", "source", src.ID, "fork", fork.ID, "err", err))
		logx.Warn(s.taskStore.Delete(r.Context(), fork.ID), "api.tasks.log.rollback_fork", "task", fork.ID)
		if task.IsStorageQuotaExceeded(err) {
			writeError(w, r, http.StatusRequestEntityTooLarge, err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, apperr.Wrap(err, "api.tasks.errors.deep_copy_failed"))
		return
	}

	worktreePaths := map[string]string{}
	for _, projectID := range src.ProjectIDs {
		path, wErr := s.forkWorktree(r.Context(), fork.ID, projectID, src.BranchName)
		if wErr != nil {
			log.Printf("[api] %s", i18n.Log("api.tasks.log.fork_worktree_failed", "project", projectID, "err", wErr))
			s.recordEvent(r.Context(), fork.ID, task.EventTaskCreated, "", "system",
				i18n.Text("api.tasks.timeline.fork_worktree_failed", "project", projectID, "err", apperr.Text(wErr)), nil)
			continue
		}
		worktreePaths[projectID] = path
	}

	if len(worktreePaths) > 0 {
		forkBranch := "task-" + fork.ID
		_, updErr := s.taskStore.Update(r.Context(), fork.ID, task.Patch{
			WorktreePaths: worktreePaths,
			BranchName:    &forkBranch,
		})
		logx.Warn(updErr, "api.tasks.log.persist_fork_worktrees", "task", fork.ID)
	}

	s.recordEvent(r.Context(), fork.ID, task.EventTaskCreated, "", "user",
		i18n.Text("api.tasks.timeline.forked_from", "task", src.ID, "stage", src.Stage),
		map[string]string{"forked_from": src.ID, "source_stage": string(src.Stage), "source_branch": src.BranchName})

	full, getErr := s.taskStore.Get(r.Context(), fork.ID)
	logx.Warn(getErr, "api.tasks.log.reload_after_fork", "task", fork.ID)
	writeJSON(w, http.StatusCreated, full)
}

func (s *Server) forkWorktree(ctx context.Context, forkID, projectID, srcBranch string) (string, error) {
	if srcBranch != "" {
		path, _, err := s.projectStore.CreateForkedWorktree(ctx, projectID, forkID, srcBranch)
		return path, err
	}
	wt, err := s.projectStore.CreateTaskWorktreeAuth(ctx, projectID, forkID, s.fetchToken(ctx, projectID))
	if err != nil {
		return "", err
	}
	if wt.StaleBase != nil {
		s.recordEvent(ctx, forkID, task.EventTaskCreated, "", "system",
			i18n.Text("api.tasks.timeline.fork_worktree_stale_base",
				"project", s.projectName(ctx, projectID), "commit", wt.BaseCommit, "err", apperr.Text(wt.StaleBase)), nil)
	}
	return wt.Path, nil
}

func (s *Server) fetchToken(ctx context.Context, projectID string) string {
	tok, err := s.githubTokenForProjectWith(ctx, projectID, githubapp.PermsClone)
	if err != nil || tok == nil {
		if err != nil && !errors.Is(err, githubapp.ErrNoCredential) {
			logx.Warn(err, "api.tasks.log.resolve_clone_credential", "project", projectID)
		}
		return ""
	}
	return tok.Token
}

func (s *Server) handleRelaunchTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}

	var body struct {
		Turns int `json:"turns,omitempty"`
		// Optional: with one, the relaunch is posted as a decision answering the open stop.
		Reason *string `json:"reason,omitempty"`
	}
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	reason, ok := decisionReason(w, r, author, body.Reason, false, "")
	if !ok {
		return
	}
	stop := s.openStop(r.Context(), t.ID)

	turns := body.Turns
	if turns <= 0 {
		turns = s.taskDispatcher.RelaunchTurns(r.Context(), t)
	}
	if !s.relaunchNow(w, r, t, author, turns) {
		return
	}
	// A relaunch without a reason is only this event; with one, the reason is a decision in
	// Comments and the event points at it. The dispatcher records EventAgentRunStarted on launch.
	commentID := s.postDecision(r.Context(), t.ID, stop, author, reason, task.CommentMeta{
		Action: task.DecisionRelaunch, FromStage: t.Stage, ToStage: t.Stage,
	})
	s.recordRelaunchRequested(r.Context(), t, author, turns, commentID)

	full, getErr := s.taskStore.Get(r.Context(), t.ID)
	logx.Warn(getErr, "api.tasks.log.reload_after_relaunch", "task", t.ID)
	writeJSON(w, http.StatusOK, map[string]any{"task": full, "turns": turns})
}

// relaunchNow starts the task's stage again with turns. On a refusal it writes the answer and
// returns false.
func (s *Server) relaunchNow(w http.ResponseWriter, r *http.Request, t *task.Task, author auth.Author, turns int) bool {
	if err := s.taskDispatcher.RelaunchStage(launchCtx(r.Context()), t, turns); err != nil {
		var notStarted *task.NotStartedError
		if errors.As(err, &notStarted) {
			writeJSON(w, http.StatusPreconditionFailed, map[string]any{
				"error": apperr.LocalizeCtx(r.Context(), err),
				"code":  apperr.Code(err),
				"cause": notStarted.Cause,
			})
			return false
		}
		writeError(w, r, http.StatusConflict, err)
		return false
	}
	s.cancelQuestionsFor(r, t, "task.questions.reply.canceled_relaunch")
	// A person taking a task out of blocked starts the send-back count again; the move-by-hand
	// reset is in applyUpdateSideEffects.
	if t.Status == task.StatusBlocked && !author.Agent {
		s.recordEvent(r.Context(), t.ID, task.EventKickBackReset, "", author.Label(),
			i18n.Text("api.tasks.timeline.kick_back_reset"), map[string]string{"stage": string(t.Stage)})
	}
	return true
}

// recordRelaunchRequested leaves the relaunch on the timeline; commentID is the decision that
// carries its reason, "" when there was none.
func (s *Server) recordRelaunchRequested(ctx context.Context, t *task.Task, author auth.Author, turns int, commentID string) {
	payload := map[string]any{"stage": string(t.Stage), "turns": turns}
	if commentID != "" {
		payload["comment_id"] = commentID
	}
	s.recordEvent(ctx, t.ID, task.EventRelaunchRequested, "", author.Label(),
		i18n.Text("api.tasks.timeline.relaunch_requested", "stage", s.taskDispatcher.StageNote(t, t.Stage), "turns", turns),
		payload)
}

func (s *Server) handleCancelQueuedRun(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	if err := s.taskDispatcher.CancelQueued(r.Context(), t, author.Name); err != nil {
		writeError(w, r, 0, err)
		return
	}
	full, err := s.taskStore.Get(r.Context(), t.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, full)
}

func (s *Server) handleEnsurePR(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	if len(t.ProjectIDs) == 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.no_linked_projects")
		return
	}
	updated, results := s.taskDispatcher.EnsurePRs(r.Context(), t)
	full, ferr := s.taskStore.Get(r.Context(), t.ID)
	if ferr != nil || full == nil {
		full = updated
	}
	opened := 0
	for _, r := range results {
		if r.Status == task.ForcePRStatusOpened {
			opened++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task":    full,
		"opened":  opened,
		"results": results,
	})
}

func (s *Server) checkStageTransition(ctx context.Context, id string, prev *task.Task, p task.Patch) (int, any, bool) {
	board, typeID := effectiveBoard(prev, p), effectiveType(prev, p)
	if !s.boards.Board(board).Valid(*p.Stage) {
		return http.StatusBadRequest, keyedRefusal(ctx, nil, "api.tasks.errors.invalid_stage", "stage", *p.Stage), false
	}
	// Refused, not allowed: a task in a stage its type skips never auto-advances out of it.
	if reg := s.boards.BoardFor(board, typeID); !reg.Valid(*p.Stage) {
		return http.StatusConflict, keyedRefusal(ctx, map[string]any{
			"task_type":        typeID,
			"available_stages": reg.List(),
		}, "api.tasks.errors.stage_skipped_by_type", "stage", *p.Stage, "type", typeID), false
	}
	prevInfo, _ := s.stagesFor(prev).Get(prev.Stage)
	leaving := prev.Stage != *p.Stage
	// Blockers must finish first: this task's worktree is created on leaving the initial stage and
	// must branch from a main that has their work.
	if prevInfo.Initial && leaving {
		blocked, blockers, blockErr := s.taskStore.IsBlocked(ctx, id)
		logx.Warn(blockErr, "api.tasks.log.is_blocked_check", "task", id)
		if blocked {
			return http.StatusConflict, keyedRefusal(ctx, map[string]any{
				"blocked":    true,
				"blocked_by": blockers,
			}, "api.tasks.errors.blocked_by_dependencies", "stages", s.blockersFinalStages(ctx, blockers, s.stagesFor(prev))), false
		}
	}
	if leaving {
		to := *p.Stage
		if p.BoardID != nil && *p.BoardID != prev.BoardID {
			to = task.OffBoard
		}
		if status, body, ok := s.approvalGate(ctx, prev, prev.Stage, to, task.GateManual); !ok {
			return status, body, false
		}
	}
	assigns := mergeAssignments(prev.StageAssignments, p.StageAssignments)
	if agentName, label, blocked := s.repoRequirementError(ctx, prev, *p.Stage, assigns); blocked {
		return http.StatusConflict, keyedRefusal(ctx, map[string]any{
			"requires_repo": true,
			"agent":         agentName,
			"stage":         string(*p.Stage),
		}, "api.tasks.errors.agent_needs_repo", "agent", label), false
	}
	return 0, nil, true
}

// Not task.Patch: its daemon-owned fields include worktree_paths, which the launcher bind-mounts (a
// caller could mount any host path).
// Those fields are declared here only to refuse them by name; a silent drop would make the caller
// think the write took.
type taskPatchRequest struct {
	Title            *string               `json:"title,omitempty"`
	Body             *string               `json:"body,omitempty"`
	Stage            *task.Stage           `json:"stage,omitempty"`
	StageAssignments map[task.Stage]string `json:"stage_assignments,omitempty"`
	ProjectIDs       *[]string             `json:"project_ids,omitempty"`
	Status           *task.RunStatus       `json:"status,omitempty"`
	Tags             *[]string             `json:"tags,omitempty"`
	Priority         *string               `json:"priority,omitempty"`
	BoardID          *string               `json:"board_id,omitempty"`
	TypeID           *string               `json:"type_id,omitempty"`
	Archived         *bool                 `json:"archived,omitempty"`
	// A person's call: an agent sets them only through flags.yaml in a stage with ingest_flags.
	FlowFlags *[]string `json:"flow_flags,omitempty"`
	// Why the stage changes. Required from a person moving the task back (a send-back); optional
	// moving it forward. With one, it is posted as a decision answering the open stop.
	Reason *string `json:"reason,omitempty"`

	// Edited through POST /tasks/{id}/stage-skips and .../remove, which check who may add one.
	StageSkips json.RawMessage `json:"stage_skips,omitempty"`

	BranchName      json.RawMessage `json:"branch_name,omitempty"`
	WorktreePaths   json.RawMessage `json:"worktree_paths,omitempty"`
	PRURLs          json.RawMessage `json:"pr_urls,omitempty"`
	MergeSHAs       json.RawMessage `json:"merge_shas,omitempty"`
	CurrentRunID    json.RawMessage `json:"current_run_id,omitempty"`
	CurrentInstance json.RawMessage `json:"current_instance,omitempty"`
}

func (req *taskPatchRequest) serverOwnedFields() []string {
	var out []string
	for _, f := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"branch_name", req.BranchName},
		{"worktree_paths", req.WorktreePaths},
		{"pr_urls", req.PRURLs},
		{"merge_shas", req.MergeSHAs},
		{"current_run_id", req.CurrentRunID},
		{"current_instance", req.CurrentInstance},
	} {
		if len(f.raw) > 0 {
			out = append(out, f.name)
		}
	}
	return out
}

func (req *taskPatchRequest) patch() task.Patch {
	body := req.Body
	if body != nil {
		unwrapped := task.UnwrapMarkdownFence(*body)
		body = &unwrapped
	}
	return task.Patch{
		Title:            req.Title,
		Body:             body,
		Stage:            req.Stage,
		StageAssignments: req.StageAssignments,
		ProjectIDs:       req.ProjectIDs,
		Status:           req.Status,
		Tags:             req.Tags,
		Priority:         req.Priority,
		BoardID:          req.BoardID,
		TypeID:           req.TypeID,
		Archived:         req.Archived,
	}
}

// The flow flags decide which conditional stages run; an agent of a later stage could drop "ui" and
// skip its own QA, so only a person edits them here.
func personFlowFlags(w http.ResponseWriter, r *http.Request, in *[]string, p *task.Patch) bool {
	if in == nil {
		return true
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return false
	}
	if author.Agent {
		writeErrKey(w, r, http.StatusForbidden, "api.tasks.errors.flow_flags_person_only")
		return false
	}
	flags, invalid := task.NormalizeFlowFlags(*in)
	if len(invalid) > 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.flow_flags_invalid",
			"values", strings.Join(invalid, ", "), "max", task.MaxFlowFlags)
		return false
	}
	p.FlowFlags = &flags
	return true
}

// A run moving the task it works on picks which stages run after it: a forward move skips their
// agents and the approvals they would open, and the gate only holds approvals already opened.
// Retyping or moving it to another board does the same. The parent counts too: a subtask's code
// travels in its PR. A delegated chat acts for a person and keeps the person's hand.
func (s *Server) runMovesOwnTask(ctx context.Context, prev *task.Task, p task.Patch) bool {
	scope := auth.APIKeyRunScope(ctx)
	if !scope.IsRun() || scope.Delegated() || scope.TaskID == "" {
		return false
	}
	moved := p.Stage != nil && *p.Stage != prev.Stage ||
		p.TypeID != nil && *p.TypeID != prev.TypeID ||
		p.BoardID != nil && *p.BoardID != prev.BoardID
	if !moved {
		return false
	}
	if prev.ID == scope.TaskID {
		return true
	}
	own, err := s.taskStore.GetRow(ctx, scope.TaskID)
	return err != nil || own == nil || own.ParentID == prev.ID
}

// callerPatch turns the request into the patch this caller may apply: fields the daemon owns, flow
// flags from an agent and a run moving its own task are refused with the answer already written.
func (s *Server) callerPatch(w http.ResponseWriter, r *http.Request, prev *task.Task, req *taskPatchRequest) (task.Patch, bool) {
	if len(req.StageSkips) > 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.stage_skips_patch")
		return task.Patch{}, false
	}
	if owned := req.serverOwnedFields(); len(owned) > 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.daemon_owned_fields", "fields", strings.Join(owned, ", "))
		return task.Patch{}, false
	}
	p := req.patch()
	if !personFlowFlags(w, r, req.FlowFlags, &p) {
		return task.Patch{}, false
	}
	if s.runMovesOwnTask(r.Context(), prev, p) {
		writeErrKey(w, r, http.StatusForbidden, "api.tasks.errors.run_moves_own_task")
		return task.Patch{}, false
	}
	return p, true
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	prev, ok := s.taskRowForCaller(w, r, id)
	if !ok {
		return
	}

	var req taskPatchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	p, ok := s.callerPatch(w, r, prev, &req)
	if !ok {
		return
	}

	// A subtask archived alone vanished from both board and archived list, and blocked deleting its
	// parent.
	if p.Archived != nil && prev.IsSubtask() {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.archive_subtask")
		return
	}

	if badPID := s.invisiblePatchProject(r, p); badPID != "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.project_not_found", "project", badPID)
		return
	}

	// Order matters: board gate, then type gate, then stage gate; each changes what the next
	// accepts.
	if status, body, ok := s.checkBoardTransition(r, prev, &p); !ok {
		writeJSON(w, status, body)
		return
	}

	if status, body, ok := s.checkTypeTransition(r.Context(), prev, &p); !ok {
		writeJSON(w, status, body)
		return
	}
	s.retypeAssignments(r.Context(), prev, &p)

	stageChanged := p.Stage != nil && *p.Stage != prev.Stage
	decision, passed := s.moveGate(w, r, prev, p, req.Reason)
	if !passed {
		return
	}

	if stageChanged && p.Status == nil {
		idle := task.StatusIdle
		p.Status = &idle
	}
	// Moving the task into a column it skips drops that skip in the mover's name.
	p.By = authorLabel(r)

	updated, err := s.taskStore.Update(r.Context(), id, p)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	s.applyUpdateSideEffects(r, prev, updated, p, stageChanged, decision)

	full, err := s.taskStore.Get(r.Context(), id)
	logx.Warn(err, "api.tasks.log.reload_after_update", "task", id)
	writeJSON(w, http.StatusOK, full)
}

func stageLabelIn(info types.StageInfo, locale string) string {
	return stagepkg.LocalizeSeeded([]types.StageInfo{info}, locale)[0].Label
}

func (s *Server) blockersFinalStages(ctx context.Context, blockers []task.DepRef, own stagepkg.Registry) string {
	var names []string
	locale := i18n.FromContext(ctx)
	add := func(reg stagepkg.Registry) {
		for _, id := range reg.Terminals() {
			info, _ := reg.Get(id)
			label := stageLabelIn(info, locale)
			if !slices.Contains(names, label) {
				names = append(names, label)
			}
		}
	}
	for _, b := range blockers {
		if row, err := s.taskStore.GetRow(ctx, b.TaskID); err == nil {
			add(s.boards.BoardFor(row.BoardID, row.TypeID))
		}
	}
	if len(names) == 0 {
		add(own)
	}
	return strings.Join(names, " / ")
}

func effectiveBoard(prev *task.Task, p task.Patch) string {
	if p.BoardID != nil {
		return *p.BoardID
	}
	return prev.BoardID
}

func effectiveType(prev *task.Task, p task.Patch) string {
	if p.TypeID != nil {
		return *p.TypeID
	}
	return prev.TypeID
}

func (s *Server) checkTypeTransition(ctx context.Context, prev *task.Task, p *task.Patch) (int, any, bool) {
	if p.TypeID == nil || *p.TypeID == prev.TypeID {
		return 0, nil, true
	}
	board, dest := effectiveBoard(prev, *p), *p.TypeID
	if dest != "" && s.stageAdmin != nil && !s.stageAdmin.HasBoardType(board, dest) {
		return http.StatusBadRequest, keyedRefusal(ctx, nil, "api.boards.errors.board_lacks_type", "board", board, "type", dest), false
	}
	reg := s.boards.BoardFor(board, dest)
	target := prev.Stage
	patch.Set(&target, p.Stage)
	if !reg.Valid(target) || isVirtual(target) {
		return http.StatusConflict, keyedRefusal(ctx, map[string]any{
			"task_type":        dest,
			"available_stages": reg.List(),
		}, "api.tasks.errors.type_skips_stage", "type", dest, "stage", target), false
	}
	p.Stage = &target
	return 0, nil, true
}

func (s *Server) checkBoardTransition(r *http.Request, prev *task.Task, p *task.Patch) (int, any, bool) {
	if p.BoardID == nil || *p.BoardID == prev.BoardID {
		return 0, nil, true
	}
	dest := *p.BoardID
	// Another workspace's board is reported as unknown, never as existing.
	if !s.boardVisibleToCaller(r, dest) {
		return http.StatusBadRequest, keyedRefusal(r.Context(), nil, "api.boards.errors.unknown_board", "board", dest), false
	}
	if p.TypeID == nil && prev.TypeID != "" &&
		s.stageAdmin != nil && !s.stageAdmin.HasBoardType(dest, prev.TypeID) {
		untyped := ""
		p.TypeID = &untyped
	}
	reg := s.boards.BoardFor(dest, effectiveType(prev, *p))
	target := prev.Stage
	patch.Set(&target, p.Stage)
	if !reg.Valid(target) || isVirtual(target) {
		return http.StatusConflict, keyedRefusal(r.Context(), map[string]any{
			"board":            dest,
			"available_stages": reg.List(),
		}, "api.tasks.errors.stage_missing_on_board", "stage", target, "board", dest), false
	}
	p.Stage = &target
	return 0, nil, true
}

// Missing and out-of-scope projects look identical on purpose.
func (s *Server) invisiblePatchProject(r *http.Request, p task.Patch) string {
	if p.ProjectIDs == nil {
		return ""
	}
	wid, unrestricted := s.callerWorkspace(r)
	for _, pid := range *p.ProjectIDs {
		proj, perr := s.projectStore.Get(r.Context(), pid)
		if perr != nil || !workspaceVisible(proj.WorkspaceID, wid, unrestricted) {
			return pid
		}
	}
	return ""
}

// moveDecision is the reason a PATCH gave for its stage change and whether that change goes back.
type moveDecision struct {
	reason string
	back   bool
}

// moveReason reads the reason of a stage change: a person moving the task back must give one.
// moveGate runs what a stage or board change must pass, after the board and type gates: the reason
// a person owes for moving back, then the stage's exit gate (or the approvals a board change leaves
// behind). It writes the refusal and returns false when the change may not go on.
func (s *Server) moveGate(w http.ResponseWriter, r *http.Request, prev *task.Task, p task.Patch, raw *string) (moveDecision, bool) {
	stageChanged := p.Stage != nil && *p.Stage != prev.Stage
	back := stageChanged && (p.BoardID == nil || *p.BoardID == prev.BoardID) && s.movesBackOn(prev, prev.Stage, *p.Stage)
	reason, ok := s.moveReason(w, r, raw, back)
	if !ok {
		return moveDecision{}, false
	}
	switch {
	case stageChanged:
		if status, body, passed := s.checkStageTransition(r.Context(), prev.ID, prev, p); !passed {
			writeJSON(w, status, body)
			return moveDecision{}, false
		}
	case p.BoardID != nil && *p.BoardID != prev.BoardID:
		if status, body, passed := s.approvalGate(r.Context(), prev, prev.Stage, task.OffBoard, task.GateManual); !passed {
			writeJSON(w, status, body)
			return moveDecision{}, false
		}
	}
	return moveDecision{reason: reason, back: back}, true
}

func (s *Server) moveReason(w http.ResponseWriter, r *http.Request, raw *string, back bool) (string, bool) {
	if raw == nil && !back {
		return "", true
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return "", false
	}
	return decisionReason(w, r, author, raw, back, "api.tasks.errors.reason_required_send_back")
}

func (s *Server) applyUpdateSideEffects(r *http.Request, prev, updated *task.Task, p task.Patch, stageChanged bool,
	d moveDecision,
) {
	if stageChanged {
		// Only a person's move resets the board's send-back count: an agent moving its own task
		// through the API must not wipe it.
		m := stageMove{To: updated.Stage, Cause: task.GateManual, Via: task.ViaManual, Actor: "user", req: r}
		if a, err := auth.AuthorFromRequest(r); err == nil {
			if a.Agent {
				m.Actor = a.Label()
			} else {
				m.ByPerson = true
			}
			// Before stageMoved: entering the new stage resolves the stop this decision answers.
			action := task.DecisionMove
			if d.back {
				action = task.DecisionSendBack
			}
			if id := s.postDecision(r.Context(), updated.ID, s.openStop(r.Context(), updated.ID), a, d.reason,
				task.CommentMeta{Action: action, FromStage: prev.Stage, ToStage: updated.Stage}); id != "" {
				m.Extra = map[string]any{"comment_id": id}
			}
		}
		s.stageMoved(r.Context(), prev, updated, m)
		return
	}
	if agentJustAssigned(prev, updated, p) {
		s.taskDispatcher.HandleStageEntered(launchCtx(r.Context()), updated, updated.Stage)
	}
}

func agentJustAssigned(prev, updated *task.Task, p task.Patch) bool {
	return p.StageAssignments != nil &&
		prev.AssignedAgent(updated.Stage) == "" &&
		updated.AssignedAgent(updated.Stage) != "" &&
		updated.Status == task.StatusIdle
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	// Subtasks share the parent's worktree; deleting the parent alone would orphan them and lose
	// their checkout.
	if !t.IsSubtask() {
		children, err := s.taskStore.ListChildren(r.Context(), id)
		if err != nil {
			logx.Warn(err, "api.tasks.log.list_children_before_delete", "task", id)
		} else if len(children) > 0 && r.URL.Query().Get("subtasks") != "delete" {
			writeJSON(w, http.StatusConflict, keyedRefusal(r.Context(), map[string]any{
				"subtask_count": len(children),
			}, "api.tasks.errors.delete_subtasks_first"))
			return
		}
		for i := range children {
			if err := s.teardownTask(r.Context(), &children[i]); err != nil {
				writeError(w, r, http.StatusInternalServerError, err)
				return
			}
			s.emit(r, "task.deleted", "task.deleted", children[i].ID, "task",
				i18n.Text("api.tasks.audit.task_deleted", "task", children[i].ID),
				map[string]string{"task_id": children[i].ID})
		}
	}
	if err := s.teardownTask(r.Context(), t); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.emit(r, "task.deleted", "task.deleted", id, "task", i18n.Text("api.tasks.audit.task_deleted", "task", id),
		map[string]string{"task_id": id})
	writeStatus(w, "deleted")
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	var body struct {
		Body          string   `json:"body"`
		AttachmentIDs []string `json:"attachment_ids"`
		NotifyAgent   bool     `json:"notify_agent"`
		ParentID      string   `json:"parent_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Body) == "" && len(body.AttachmentIDs) == 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.body_required")
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	src := commentSource(author)
	var c *task.Comment
	var err error
	if parent := strings.TrimSpace(body.ParentID); parent != "" {
		c, err = s.taskStore.AddReply(r.Context(), id, parent, src, author.Name, body.Body)
	} else {
		c, err = s.taskStore.AddComment(r.Context(), id, src, author.Name, body.Body)
	}
	if errors.Is(err, task.ErrNoSuchThread) {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if err := s.taskStore.BindCommentAttachments(r.Context(), id, c.ID, body.AttachmentIDs); err != nil {
		logx.Warn(err, "api.tasks.log.bind_attachments", "comment", c.ID)
	}
	if s.answerFromThreadReply(r, t, c) {
		writeJSON(w, http.StatusCreated, c)
		return
	}

	go s.dispatchMentions(context.WithoutCancel(r.Context()), id, c)

	if !body.NotifyAgent || src != task.SourceUser || s.taskDispatcher == nil {
		writeJSON(w, http.StatusCreated, c)
		return
	}
	notice, notifyErr := s.taskDispatcher.NotifyComment(r.Context(), t, c)
	if notifyErr != nil {
		logx.Warn(notifyErr, "api.tasks.log.notify_run", "comment", c.ID)
		writeJSON(w, http.StatusCreated, c)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		*task.Comment
		Notice *task.ConverseResult `json:"notice,omitempty"`
	}{Comment: c, Notice: &notice})
}

func (s *Server) handleConverse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	var body struct {
		Text      string `json:"text"`
		Interrupt bool   `json:"interrupt"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.text_required")
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}

	if at := s.mentionedAgent(t.WorkspaceID, text); at != nil {
		res, err := s.converseMention(context.WithoutCancel(r.Context()), t, at, author, text)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, res)
		return
	}

	res, err := s.taskDispatcher.Converse(launchCtx(r.Context()), t, text, author.Name, body.Interrupt)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) mentionedAgent(ws, text string) *types.AgentType {
	if agents := s.mentionedAgents(ws, text); len(agents) > 0 {
		return agents[0]
	}
	return nil
}

func (s *Server) mentionedAgents(ws, text string) []*types.AgentType {
	matches := mentionRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	discovered, err := s.agents(ws).All()
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.discover_agents_failed", "err", err))
		return nil
	}
	var out []*types.AgentType
	seen := map[string]bool{}
	for _, m := range matches {
		for i := range discovered {
			at := &discovered[i]
			if (at.Name == m[1] || at.Type == m[1]) && !seen[at.Name] {
				seen[at.Name] = true
				out = append(out, at)
				break
			}
		}
	}
	return out
}

func (s *Server) mentionIntoTaskChat(ctx context.Context,
	t *task.Task, at *types.AgentType, author, text string, msg *task.Message,
) (task.ConverseResult, bool) {
	c, err := s.chatSvc.FindOrCreateForTask(ctx, t.WorkspaceID, at.Type, t.ID, "", t.Title)
	if err != nil {
		logx.Warn(err, "api.tasks.log.open_task_chat", "agent", at.Name, "task", t.ID)
		return task.ConverseResult{}, false
	}
	if _, err := s.chatSvc.PostMessage(ctx, c.ID, author, text); err != nil {
		logx.Warn(err, "api.tasks.log.post_mention_into_chat", "chat", c.ID)
		return task.ConverseResult{}, false
	}
	msg.Delivery, msg.ChatID, msg.Agent = string(task.DeliveryChat), c.ID, at.Name
	logx.Warn(s.taskStore.UpdateMessageDelivery(ctx, msg.ID, msg.Delivery, at.Name, "", c.ID),
		"api.tasks.log.stamp_chat_delivery", "message", msg.ID)
	return task.ConverseResult{
		Delivery: task.DeliveryChat, Agent: at.Name, Message: msg, ChatID: c.ID,
		Detail: i18n.TCtx(ctx, "api.tasks.hints.answers_in_chat", "agent", at.Name),
	}, true
}

func (s *Server) converseMention(
	ctx context.Context, t *task.Task, at *types.AgentType, author auth.Author, text string,
) (task.ConverseResult, error) {
	msg, err := s.taskStore.AddMessage(ctx, &task.Message{
		TaskID: t.ID, Role: task.RoleUser, Author: author.Name, Body: text, Agent: at.Name,
	})
	if err != nil {
		return task.ConverseResult{}, err
	}

	chatFailed := false
	if s.chatSvc != nil && at.SupportsChat() {
		res, ok := s.mentionIntoTaskChat(ctx, t, at, author.Name, text, msg)
		if ok {
			return res, nil
		}
		chatFailed = true
	}

	c, err := s.taskStore.AddComment(ctx, t.ID, commentSource(author), author.Name, text)
	if err != nil {
		return task.ConverseResult{}, err
	}
	msg.Delivery = string(task.DeliveryMention)
	logx.Warn(s.taskStore.UpdateMessageDelivery(ctx, msg.ID, msg.Delivery, at.Name, "", ""),
		"api.tasks.log.stamp_mention_delivery", "message", msg.ID)
	go s.dispatchMentions(ctx, t.ID, c)
	detail := i18n.TCtx(ctx, "api.tasks.hints.no_chat_answers_in_comments", "agent", at.Name)
	if chatFailed {
		detail = i18n.TCtx(ctx, "api.tasks.hints.chat_failed_answers_in_comments", "agent", at.Name)
	}
	return task.ConverseResult{
		Delivery: task.DeliveryMention, Agent: at.Name, Message: msg, Detail: detail,
	}, nil
}

var mentionRe = regexp.MustCompile(`@([A-Za-z0-9][A-Za-z0-9_-]*)`)

// Who a comment may launch comes from commentSource (the credential), never the body.
// An agent never falls back to the thread's last agent: that would be itself or the one it just
// answered.
func (s *Server) dispatchMentions(ctx context.Context, taskID string, c *task.Comment) {
	if c == nil {
		return
	}
	var agents []*types.AgentType
	switch c.Source {
	case task.SourceUser:
		if agents = s.mentionedAgents(s.taskWorkspace(ctx, taskID), c.Body); len(agents) == 0 {
			agents = s.threadAgent(ctx, c)
		}
	case task.SourceAgent:
		agents = s.agentMentions(ctx, c)
	case task.SourceAuditor, task.SourceSystem, task.SourceAutomation:
		return
	}
	if len(agents) == 0 {
		return
	}
	t, err := s.taskStore.Get(ctx, taskID)
	if err != nil {
		return
	}
	for _, at := range agents {
		if s.mentionPrechecked(ctx, t.ID, c.ThreadID(), at) {
			// Own goroutine: a mention parks on the conversation cap, and one busy agent must not
			// block the others named.
			go s.taskDispatcher.HandleMention(ctx, t, at, c)
		}
	}
}

func (s *Server) agentMentions(ctx context.Context, c *task.Comment) []*types.AgentType {
	var named []*types.AgentType
	for _, at := range s.mentionedAgents(s.taskWorkspace(ctx, c.TaskID), c.Body) {
		if at.Name != c.Author {
			named = append(named, at)
		}
	}
	if len(named) == 0 {
		return nil
	}
	th, ok := s.taskThread(ctx, c.TaskID, c.ThreadID())
	if !ok {
		return nil
	}
	// Stops agents mentioning each other forever on the workspace's tokens; a person's message
	// resets the count.
	limit := s.agentTurnsPerThread(ctx, s.taskWorkspace(ctx, c.TaskID))
	if th.AgentTurnsSincePerson() < limit {
		return named
	}
	handles := make([]string, len(named))
	for i, at := range named {
		handles[i] = "@" + at.Name
	}
	n := task.NoteOf("api.tasks.comments.agent_turn_limit", "turns", limit, "agents", strings.Join(handles, ", "))
	_, err := s.taskStore.PostComment(ctx, task.NewComment{
		TaskID: c.TaskID, ThreadID: th.Root.ID, Source: task.SourceSystem, Author: "octopus", Note: &n,
		Kind: task.CommentPause, Meta: &task.CommentMeta{Turns: limit},
	})
	logx.Warn(err, "api.tasks.log.note_turn_limit", "task", c.TaskID)
	return nil
}

func (s *Server) threadAgent(ctx context.Context, c *task.Comment) []*types.AgentType {
	if c.ParentID == "" {
		return nil
	}
	th, ok := s.taskThread(ctx, c.TaskID, c.ParentID)
	if !ok {
		return nil
	}
	name := th.LastAgent()
	if name == "" {
		return nil
	}
	if at := s.findAgentByHandle(s.taskWorkspace(ctx, c.TaskID), name); at != nil && at.AnswersMentions() {
		return []*types.AgentType{at}
	}
	return nil
}

func (s *Server) mentionPrechecked(ctx context.Context, taskID, threadID string, at *types.AgentType) bool {
	if at.AnswersMentionsByChat() || s.agentHasPrompt(ctx, s.taskWorkspace(ctx, taskID), at.Name, string(types.StageMention)) {
		return true
	}
	_, err := s.taskStore.AddNote(ctx, taskID, threadID, task.SourceSystem, "octopus",
		task.NoteOf("api.tasks.comments.mention_no_prompt", "agent", at.Name))
	log.Printf("[api] %s", i18n.Log("api.tasks.log.mention_skipped_no_prompt", "task", taskID, "agent", at.Name, "err", err))
	return false
}

type attachmentSpec struct {
	filename  string
	mimeType  string
	author    string
	commentID string
}

const attachmentUploadLimit = 50 << 20

func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.taskForCaller(w, r, id); !ok {
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		s.uploadAttachmentJSON(w, r, id)
		return
	}
	if err := r.ParseMultipartForm(attachmentUploadLimit); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.file_field_required")
		return
	}
	defer file.Close()

	spec, ok := s.attachmentSpecFrom(w, r, id, header.Filename, header.Header.Get("Content-Type"))
	if !ok {
		return
	}
	a, err := s.storeAttachment(r.Context(), id, spec, io.LimitReader(file, attachmentUploadLimit))
	if err != nil {
		writeError(w, r, attachmentErrStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) uploadAttachmentJSON(w http.ResponseWriter, r *http.Request, taskID string) {
	var body struct {
		Filename      string `json:"filename"`
		MimeType      string `json:"mime_type"`
		CommentID     string `json:"comment_id"`
		ContentBase64 string `json:"content_base64"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2*attachmentUploadLimit)).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.ContentBase64))
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.content_base64_invalid")
		return
	}
	if len(data) == 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.content_base64_empty")
		return
	}
	if len(data) > attachmentUploadLimit {
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.tasks.errors.file_too_large")
		return
	}
	spec, ok := s.attachmentSpecFrom(w, r, taskID, body.Filename, body.MimeType)
	if !ok {
		return
	}
	if body.CommentID != "" {
		if valid, cErr := s.commentOnTask(w, r, taskID, body.CommentID); !valid {
			return
		} else if cErr == nil {
			spec.commentID = body.CommentID
		}
	}
	a, err := s.storeAttachment(r.Context(), taskID, spec, bytes.NewReader(data))
	if err != nil {
		writeError(w, r, attachmentErrStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) attachmentSpecFrom(w http.ResponseWriter, r *http.Request, taskID, filename, mimeType string) (attachmentSpec, bool) {
	author, ok := requireAuthor(w, r)
	if !ok {
		return attachmentSpec{}, false
	}
	spec := attachmentSpec{filename: filename, mimeType: mimeType, author: author.Name}
	pick := func(key string) string {
		if v := r.URL.Query().Get(key); v != "" {
			return v
		}
		return r.FormValue(key)
	}
	if commentID := pick("comment_id"); commentID != "" {
		valid, err := s.commentOnTask(w, r, taskID, commentID)
		if !valid {
			return spec, false
		}
		if err == nil {
			spec.commentID = commentID
		}
	}
	return spec, true
}

func (s *Server) commentOnTask(w http.ResponseWriter, r *http.Request, taskID, commentID string) (valid bool, err error) {
	belongs, err := s.taskStore.CommentBelongsTo(r.Context(), taskID, commentID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return false, err
	}
	if !belongs {
		err := apperr.Invalid("api.tasks.errors.comment_not_on_task")
		writeError(w, r, http.StatusBadRequest, err)
		return false, err
	}
	return true, nil
}

// Nanosecond prefix: same filename uploads otherwise shared one path and the second overwrote the
// first.
func (s *Server) storeAttachment(ctx context.Context, taskID string, spec attachmentSpec, src io.Reader) (*task.Attachment, error) {
	attachmentsDir := s.taskStore.AttachmentsDir()
	if err := os.MkdirAll(attachmentsDir, 0o750); err != nil {
		return nil, err
	}
	if err := safepath.MkdirAll(attachmentsDir, taskID, 0o750); err != nil {
		return nil, err
	}
	filename := task.SanitizeFilename(spec.filename)
	relPath := filepath.Join(taskID, fmt.Sprintf("%d-%s", time.Now().UnixNano(), filename))
	dstPath := filepath.Join(attachmentsDir, relPath)
	guard, err := s.taskStore.ReserveAttachment(ctx, taskID)
	if err != nil {
		return nil, err
	}
	defer guard.Done()
	dst, err := safepath.Create(attachmentsDir, relPath)
	if err != nil {
		return nil, err
	}
	written, copyErr := io.Copy(dst, guard.Limit(src))
	if closeErr := dst.Close(); closeErr != nil && copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		copyErr = guard.Check(written)
	}
	if copyErr != nil {
		logx.Warn(os.Remove(dstPath), "api.tasks.log.remove_partial_attachment", "path", dstPath)
		return nil, copyErr
	}
	mimeType := spec.mimeType
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(filename))
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return s.taskStore.AddAttachment(ctx, task.NewAttachment{
		TaskID:    taskID,
		CommentID: spec.commentID,
		Author:    spec.author,
		Filename:  filename,
		MimeType:  mimeType,
		Path:      dstPath,
		Size:      written,
	})
}

func attachmentErrStatus(err error) int {
	if task.IsStorageQuotaExceeded(err) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusInternalServerError
}

func (s *Server) attachBytesToTask(ctx context.Context, taskID, filename, mimeType string, data []byte) (any, error) {
	author, err := auth.AuthorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.storeAttachment(ctx, taskID, attachmentSpec{
		filename: filename,
		mimeType: mimeType,
		author:   author.Name,
	}, bytes.NewReader(data))
}

func (s *Server) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	s.listTaskResource(w, r, func(id string) (any, error) { return s.taskStore.ListAttachments(r.Context(), id) })
}

func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	a, err := s.taskStore.GetAttachment(r.Context(), r.PathValue("attachmentId"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	if _, ok := s.taskForCaller(w, r, a.TaskID); !ok {
		return
	}
	if err := s.taskStore.DeleteAttachment(r.Context(), a.ID); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleServeAttachment(w http.ResponseWriter, r *http.Request) {
	a, err := s.taskStore.GetAttachment(r.Context(), r.PathValue("attachmentId"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	if _, ok := s.taskForCaller(w, r, a.TaskID); !ok {
		return
	}
	if a.MimeType != "" {
		w.Header().Set("Content-Type", a.MimeType)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	// The viewer iframes PDFs/text, so framing by self is allowed; `sandbox allow-scripts` without
	// allow-same-origin puts uploaded HTML
	// in an opaque origin so its scripts can't reach this origin's cookies or DOM, while Chrome's
	// PDF viewer still works.
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; frame-ancestors 'self'") //i18n:ignore HTTP header value
	// Attachment bytes never change for an ID. Private, not public: a shared cache would serve
	// authorized content to anyone.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable") //i18n:ignore HTTP header value
	http.ServeFile(w, r, a.Path)
}

func (s *Server) listTaskResource(w http.ResponseWriter, r *http.Request, fetch func(id string) (any, error)) {
	id := r.PathValue("id")
	if _, ok := s.taskForCaller(w, r, id); !ok {
		return
	}
	out, err := fetch(id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListReports(w http.ResponseWriter, r *http.Request) {
	s.listTaskResource(w, r, func(id string) (any, error) { return s.taskStore.ListReports(r.Context(), id) })
}

// auditor_feedback is excluded: its verdict moves the task and only the auditor may file it, via
// its own route.
var postableReportKinds = map[string]bool{
	task.ReportKindEvidence:   true,
	task.ReportKindAgentIndex: true,
}

const maxReportTitle = 200

func (s *Server) handleAddReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.taskForCaller(w, r, id); !ok {
		return
	}
	var body struct {
		Kind       string `json:"kind"`
		Title      string `json:"title"`
		Body       string `json:"body"`
		InstanceID string `json:"instance_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	title, instance := strings.TrimSpace(body.Title), strings.TrimSpace(body.InstanceID)
	switch {
	case !postableReportKinds[body.Kind]:
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.report_kind_invalid",
			"evidence", task.ReportKindEvidence, "agent_index", task.ReportKindAgentIndex)
	case body.Kind == task.ReportKindAgentIndex && !author.Agent:
		writeErrKey(w, r, http.StatusForbidden, "api.tasks.errors.agent_index_agents_only")
	case strings.TrimSpace(body.Body) == "":
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.body_required")
	case len(body.Body) > maxReportBody:
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.tasks.errors.report_body_too_large", "max", maxReportBody)
	case utf8.RuneCountInString(title) > maxReportTitle:
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.report_title_too_long", "max", maxReportTitle)
	case instance != "" && !task.InstanceOfTask(instance, id):
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.instance_not_of_task")
	default:
		rep, err := s.taskStore.AddRunReport(r.Context(), id, reportRunStage(r.Context(), id),
			commentSource(author), author.Name, body.Kind, title, body.Body, instance)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, rep)
	}
}

func reportRunStage(ctx context.Context, taskID string) task.Stage {
	scope := auth.APIKeyRunScope(ctx)
	if !scope.IsRun() || scope.TaskID != taskID {
		return ""
	}
	return task.Stage(scope.Stage)
}

func (s *Server) handleListFlags(w http.ResponseWriter, r *http.Request) {
	s.listTaskResource(w, r, func(id string) (any, error) {
		list, err := s.taskStore.ListFlags(r.Context(), id)
		localizeFlags(r.Context(), list)
		return list, err
	})
}

func (s *Server) handleCreateFlag(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.taskForCaller(w, r, id); !ok {
		return
	}
	var body struct {
		Kind    string          `json:"kind"`
		Label   string          `json:"label"`
		Payload json.RawMessage `json:"payload,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Label) == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.label_required")
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	if body.Kind == "" {
		body.Kind = string(task.FlagCustom)
	}
	f := &task.Flag{
		Kind:      task.FlagKind(body.Kind),
		Label:     body.Label,
		CreatedBy: author.Label(),
		Payload:   body.Payload,
	}
	if err := s.taskStore.AddFlag(r.Context(), id, f); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.recordEvent(r.Context(), id, task.EventFlagRaised, f.ID, author.Label(),
		fmt.Sprintf("%s · %s", f.Kind, f.Label), nil)
	writeJSON(w, http.StatusCreated, f)
}

const maxFlagsYAML = 256 << 10

func (s *Server) handleImportFlags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	// Author comes from the credential, so a run cannot raise flags in another agent's name.
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	// Same rule as the Docker path (postStageRun): only a stage with ingest_flags reads flags.yaml.
	// Its surface decides which stages run, so a later stage's agent must not rewrite it.
	if author.Agent && !s.runIngestsFlags(r, author, t) {
		writeErrKey(w, r, http.StatusForbidden, "api.tasks.errors.flags_import_stage",
			"stage", auth.APIKeyRunScope(r.Context()).Stage)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxFlagsYAML+1))
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.body_unreadable")
		return
	}
	if len(data) > maxFlagsYAML {
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.tasks.errors.flags_yaml_too_large", "max", maxFlagsYAML)
		return
	}
	res, err := task.ParseAndAddFlags(r.Context(), s.taskStore, id, skipActorOf(r, author), data)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id": id, "flags_created": res.Created, "flow_flags": res.FlowFlags,
		"risk": res.Risk, "size": res.Size, "invalid_surface": res.Invalid,
		"stage_skips": res.StageSkips, "skips_refused": res.SkipsRefused,
	})
}

const maxReportBody = 256 << 10

// The auditor never ingests: it judges the stage, it does not write its spec.
func (s *Server) runIngestsFlags(r *http.Request, author auth.Author, t *task.Task) bool {
	stage := auth.APIKeyRunScope(r.Context()).Stage
	if author.IsAuditor() || stage == "" {
		return false
	}
	info, ok := s.boards.BoardFor(t.BoardID, t.TypeID).Get(task.Stage(stage))
	return ok && info.IngestFlags
}

// The verdict is not applied here: postStageRun reads it after the container exits
// (Dispatcher.auditorVerdict), for both runtimes.
func (s *Server) handleImportAuditorFeedback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.taskForCaller(w, r, id); !ok {
		return
	}
	// Only the auditor's run key may file: the judged agent holds a key to the same task and must
	// not grade itself.
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	if !author.IsAuditor() {
		writeErrKey(w, r, http.StatusForbidden, "api.tasks.errors.auditor_feedback_auditor_only")
		return
	}
	instanceID := strings.TrimSpace(r.URL.Query().Get("instance"))
	if !task.IsAuditInstanceFor(instanceID, id, auth.APIKeyRunScope(r.Context()).Stage) {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.auditor_instance_mismatch")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxReportBody+1))
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.body_unreadable")
		return
	}
	if len(data) > maxReportBody {
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.tasks.errors.feedback_too_large", "max", maxReportBody)
		return
	}
	if len(bytes.TrimSpace(data)) == 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.feedback_empty")
		return
	}
	rep, err := s.taskStore.AddReport(r.Context(), id, task.SourceAuditor, author.Name,
		task.ReportKindAuditorFeedback, "", string(data), instanceID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, rep)
}

// The stage's own agent files its verdict.md here under Kubernetes; postStageRun reads it after the
// container exits (Dispatcher.agentVerdict), as it does the auditor's feedback.
func (s *Server) handleImportStageVerdict(w http.ResponseWriter, r *http.Request) {
	s.importStageRunFile(w, r, task.ReportKindStageVerdict)
}

// locked-tests.txt of a lock_tests stage, uploaded by its pod; postStageRun reads it back like
// verdict.md.
func (s *Server) handleImportLockedTests(w http.ResponseWriter, r *http.Request) {
	s.importStageRunFile(w, r, task.ReportKindLockedTests)
}

// The test files a lock_tests stage locked, with the blob each must keep.
func (s *Server) handleListLockedFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.taskForCaller(w, r, id); !ok {
		return
	}
	files, err := s.taskStore.ListLockedFiles(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// A file the stage's own agent leaves for the daemon, stored as a report of its run. The run key's
// scope must name the instance, so the file lands only on the run that key was minted for.
func (s *Server) importStageRunFile(w http.ResponseWriter, r *http.Request, kind string) {
	id := r.PathValue("id")
	if _, ok := s.taskForCaller(w, r, id); !ok {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	// The auditor judges through auditor-feedback; letting it file here would let it skip the
	// stage's own gates.
	if !author.Agent || author.IsAuditor() {
		writeErrKey(w, r, http.StatusForbidden, "api.tasks.errors.stage_verdict_agent_only")
		return
	}
	instanceID := strings.TrimSpace(r.URL.Query().Get("instance"))
	if !task.IsStageInstanceFor(instanceID, id, auth.APIKeyRunScope(r.Context()).Stage) {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.stage_verdict_instance_mismatch")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxReportBody+1))
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.body_unreadable")
		return
	}
	if len(data) > maxReportBody {
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.tasks.errors.feedback_too_large", "max", maxReportBody)
		return
	}
	if len(bytes.TrimSpace(data)) == 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.feedback_empty")
		return
	}
	rep, err := s.taskStore.AddReport(r.Context(), id, task.SourceAgent, author.Name, kind, "", string(data), instanceID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, rep)
}

func (s *Server) handleUpdateFlag(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	flagID := r.PathValue("flagId")
	// Without this, a caller could pass another workspace's flagID under their own task id.
	if existing, err := s.taskStore.GetFlag(r.Context(), flagID); err != nil || existing.TaskID != id {
		writeErrKey(w, r, http.StatusNotFound, "api.tasks.errors.flag_not_found", "flag", flagID)
		return
	}
	var p task.FlagPatch
	if !decodeBody(w, r, &p) {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	decides := p.Status != nil && *p.Status != task.FlagPending
	if decides {
		p.ResolvedBy = &author.Name
	}
	var f *task.Flag
	var err error
	viaDispatcher := decides && s.taskDispatcher != nil
	if viaDispatcher {
		f, err = s.decideThroughFlag(r, t, flagID, p, author.Name)
	} else {
		f, err = s.taskStore.UpdateFlag(r.Context(), flagID, p)
	}
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	if decides && !viaDispatcher {
		s.recordEvent(r.Context(), id, task.EventFlagResolved, f.ID, author.Name,
			fmt.Sprintf("%s · %s → %s", f.Kind, f.Label, f.Status), nil)
	}
	activated := 0
	if p.Status != nil && *p.Status == task.FlagApproved {
		activated = s.activateMappingsForFlag(r, f.ID)
		s.approvalsSettled(r, id)
	}
	if activated > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"flag": f, "mappings_activated": activated})
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) activateMappingsForFlag(r *http.Request, flagID string) int {
	if s.envMetrics == nil {
		return 0
	}
	envID, err := s.envMetrics.EnvironmentForFlag(r.Context(), flagID)
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.environment_for_flag_failed", "flag", flagID, "err", err))
		return 0
	}
	if envID == "" {
		return 0
	}
	n, err := s.envMetrics.ApproveMappings(r.Context(), envID)
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.activate_mappings_failed", "flag", flagID, "env", envID, "err", err))
		return 0
	}
	return n
}

func (s *Server) handleDeleteFlag(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.deleteTaskApproval(w, r, t, r.PathValue("flagId")) {
		writeStatus(w, "deleted")
	}
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	s.listTaskResource(w, r, func(id string) (any, error) {
		events, err := s.taskStore.ListEvents(r.Context(), id)
		if err != nil {
			return nil, err
		}
		return localizeEventSummaries(events, i18n.RequestLocale(r)), nil
	})
}

func (s *Server) containerRunning(ctx context.Context, containerID string) bool {
	if s.docker == nil || containerID == "" {
		return false
	}
	info, err := s.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return false
	}
	return info.State != nil && info.State.Running
}

func (s *Server) handleTaskUsage(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if _, ok := s.taskForCaller(w, r, taskID); !ok {
		return
	}
	events, err := s.taskStore.ListEvents(r.Context(), taskID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	ids := runContainerIDs(events)
	summary := s.usageStore.TotalsByIDs(r.Context(), ids)

	currentContext, live := s.currentRunContext(r.Context(), taskID, &summary)
	if currentContext == 0 {
		currentContext = s.lastFinishedContext(r.Context(), ids)
	}

	out := map[string]any{
		"sessions":              summary.Sessions,
		"turns":                 summary.TotalTurns,
		"total_tokens":          summary.TotalTokens,
		"input_tokens":          summary.InputTokens,
		"output_tokens":         summary.OutputTokens,
		"cache_read_tokens":     summary.CacheRead,
		"cache_creation_tokens": summary.CacheCreation,
		"cost_usd":              summary.TotalCostUSD,
		"estimated_cost_usd":    summary.EstimatedCostUSD,
		"unpriced_sessions":     summary.UnpricedSessions,
		"current_context":       currentContext,
		"run_ids":               ids,
		"live":                  live,
	}
	b, ok, err := s.usageStore.TaskBreakdownFor(r.Context(), taskID, ids)
	logx.Warn(err, "api.tasks.log.task_breakdown", "task", taskID)
	if ok {
		out["breakdown"] = b
	}
	writeJSON(w, http.StatusOK, out)
}

func runContainerIDs(events []task.WorkflowEvent) []string {
	seen := map[string]struct{}{}
	ids := []string{}
	for i := range events {
		kind := events[i].Kind
		if (kind != task.EventAgentRunStarted && kind != task.EventAuditorRunStarted) || len(events[i].Payload) == 0 {
			continue
		}
		var p struct {
			ContainerID string `json:"container_id"`
		}
		if err := json.Unmarshal(events[i].Payload, &p); err != nil || p.ContainerID == "" {
			continue
		}
		if _, ok := seen[p.ContainerID]; ok {
			continue
		}
		seen[p.ContainerID] = struct{}{}
		ids = append(ids, p.ContainerID)
	}
	return ids
}

func (s *Server) currentRunContext(ctx context.Context, taskID string, summary *usage.AgentSummary) (int, bool) {
	t, terr := s.taskStore.Get(ctx, taskID)
	if terr != nil || t.CurrentRunID == "" || s.usageStore.Get(ctx, t.CurrentRunID) != nil {
		return 0, false
	}
	var engineName string
	if a, err := s.registry.Get(t.CurrentRunID); err == nil {
		engineName = a.Engine
	}
	lt := usage.LiveSnapshot(ctx, s.docker, t.CurrentRunID, engineName)
	total := lt.Total()
	if total == 0 {
		return 0, false
	}
	summary.InputTokens += lt.InputTokens
	summary.OutputTokens += lt.OutputTokens
	summary.CacheRead += lt.CacheRead
	summary.CacheCreation += lt.CacheCreation
	summary.TotalTokens += total
	summary.TotalTurns += lt.Turns
	summary.Sessions++
	return lt.LastPromptSize, s.containerRunning(ctx, t.CurrentRunID)
}

func (s *Server) lastFinishedContext(ctx context.Context, ids []string) int {
	for i := len(ids) - 1; i >= 0; i-- {
		sess := s.usageStore.Get(ctx, ids[i])
		if sess == nil || sess.LogsFile == "" {
			continue
		}
		if lt := usage.FileSnapshot(sess.LogsFile, sess.Engine); lt.LastPromptSize > 0 {
			return lt.LastPromptSize
		}
	}
	return 0
}

func (s *Server) recordEvent(ctx context.Context, taskID string, kind task.EventKind, refID, actor, summary string, payload any) {
	if err := s.taskStore.RecordEvent(ctx, taskID, kind, refID, actor, summary, payload); err != nil {
		log.Printf("[task] %s", i18n.Log("api.tasks.log.record_event_failed", "err", err))
	}
}

// Limited to the caller's boards so another tenant's column labels never arrive here.
func (s *Server) handleListStages(w http.ResponseWriter, r *http.Request) {
	withVirtual := false
	if v := r.URL.Query().Get("include_virtual"); v == "1" || v == "true" {
		withVirtual = true
	}
	if board := r.URL.Query().Get("board"); board != "" {
		info, ok := s.boardForCaller(w, r, board)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, boardColumns(r, info, s.boards.Board(board)))
		return
	}
	catalog := s.visibleCatalog(r)
	if withVirtual {
		catalog = append(catalog, stagepkg.LocalizeSeeded(stagepkg.Virtuals(), i18n.RequestLocale(r))...)
	}
	writeJSON(w, http.StatusOK, catalog)
}

func (s *Server) handleGetTaskBrief(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}

	var b strings.Builder
	b.WriteString(i18n.Text("api.tasks.prompts.brief_title", "title", t.Title))
	b.WriteString(i18n.Text("api.tasks.prompts.brief_stage", "stage", t.Stage))
	s.writeBriefBoard(&b, t)
	s.writeBriefLockedTests(r.Context(), &b, t)
	s.writeBriefRepos(r.Context(), &b, t)
	if len(t.Tags) > 0 {
		b.WriteString(i18n.Text("api.tasks.prompts.brief_tags", "tags", strings.Join(t.Tags, ", ")))
	}
	if t.Body != "" {
		b.WriteString(i18n.Text("api.tasks.prompts.brief_description_heading"))
		b.WriteString(t.Body)
		b.WriteString("\n\n")
	}
	s.writeBriefSkips(r.Context(), &b, t, strings.TrimSpace(r.URL.Query().Get("audit")) != "")
	s.writeBriefHandoff(r.Context(), &b, t)
	writeBriefComments(&b, t, strings.TrimSpace(r.URL.Query().Get("thread")))
	writeBriefApprovals(&b, t)
	writeBriefTestCases(&b, t)
	s.writeBriefAccessGiven(r.Context(), &b, t)
	writeBriefQuestions(&b, r, t)
	if run := strings.TrimSpace(r.URL.Query().Get("audit")); run != "" {
		s.writeBriefAuditedRun(r.Context(), &b, t.ID, run)
	} else {
		s.writeBriefEarlierReports(r.Context(), &b, t)
	}

	w.Header().Set("Content-Type", "text/markdown")
	w.Write([]byte(b.String()))
}

func (s *Server) writeBriefBoard(b *strings.Builder, t *task.Task) {
	boardID := t.BoardID
	if boardID == "" {
		boardID = s.boards.DefaultBoardIDFor(t.WorkspaceID)
	}
	label := boardID
	for _, info := range s.boards.BoardList() {
		if info.ID == boardID {
			if info.Label != "" {
				label = info.Label
			}
			break
		}
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_board", "label", label, "board", boardID))
	stages := s.boards.BoardFor(boardID, t.TypeID).List()
	if len(stages) > 0 {
		ids := make([]string, 0, len(stages))
		for i := range stages {
			ids = append(ids, string(stages[i].ID))
		}
		b.WriteString(i18n.Text("api.tasks.prompts.brief_flow", "flow", strings.Join(ids, " → ")))
	}
	// octopus-run tells the agent to write verdict.md only when the brief says so.
	if info, ok := s.boards.BoardFor(boardID, t.TypeID).Get(t.Stage); ok {
		if info.AgentVerdict {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_stage_verdict"))
		}
		// Same rule for flags.yaml's surface and locked-tests.txt: octopus-run documents them, the
		// brief says when they count. The surface only matters on a board with conditional stages;
		// older boards ingest flags.yaml in review stages that have no spec to write.
		if info.IngestFlags && hasConditionalStage(stages) {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_stage_surface"))
		}
		if info.IngestFlags {
			writeBriefSkippable(b, stages, info)
		}
		if info.LockTests {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_stage_lock_tests"))
		}
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_board_note"))
}

func hasConditionalStage(stages []types.StageInfo) bool {
	for i := range stages {
		if len(stages[i].SkipUnless) > 0 {
			return true
		}
	}
	return false
}

// A later stage learns which tests it may not touch before it touches them, not from the approval
// its run opens.
func (s *Server) writeBriefLockedTests(ctx context.Context, b *strings.Builder, t *task.Task) {
	files, err := s.taskStore.ListLockedFiles(ctx, t.ID)
	if err != nil || len(files) == 0 {
		return
	}
	var paths []string
	for _, f := range files {
		if f.Stage == t.Stage || f.BlobSHA == "" {
			continue
		}
		paths = append(paths, "`"+f.Path+"`")
	}
	if len(paths) == 0 {
		return
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_locked_tests", "files", strings.Join(paths, ", ")))
}

func (s *Server) writeBriefRepos(ctx context.Context, b *strings.Builder, t *task.Task) {
	if len(t.ProjectIDs) == 0 {
		return
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_repos_heading"))
	b.WriteString(i18n.Text("api.tasks.prompts.brief_repos_intro"))
	for _, pid := range t.ProjectIDs {
		proj, err := s.projectStore.Get(ctx, pid)
		if err != nil {
			continue
		}
		mount := filepath.Base(proj.Path)
		fmt.Fprintf(b, "### %s\n\n", proj.Name)
		b.WriteString(i18n.Text("api.tasks.prompts.brief_repo_mount", "mount", mount))
		if proj.Hosted() {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_repo_hosted"))
		} else {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_repo_url", "repo", proj.RepoURL))
		}
		if proj.DefaultBranch != "" {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_repo_branch", "branch", proj.DefaultBranch))
			// A pod clones only the task branch, so tools there cannot find the base by branch name.
			if base := s.projectStore.WorktreeBase(ctx, pid, t.ID); base != "" {
				b.WriteString(i18n.Text("api.tasks.prompts.brief_repo_base", "sha", base, "branch", proj.DefaultBranch))
			}
		}
		if proj.Description != "" {
			fmt.Fprintf(b, "\n%s\n", proj.Description)
		}
		b.WriteString("\n")
	}
}

const briefThreadReplies = 6

// System notes stay out: they repeat the run lifecycle and verdicts the brief already carries in
// its own sections. A system note that people replied to stays as the root of their thread.
func writeBriefComments(b *strings.Builder, t *task.Task, fullThread string) {
	threads := briefThreads(t.Comments)
	if len(threads) == 0 {
		return
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_discussion_heading"))
	for i := range threads {
		th := &threads[i]
		writeBriefComment(b, "###", &th.Root)
		if len(th.Replies) == 0 {
			continue
		}
		keep := briefThreadReplies
		status := ""
		if th.Root.ResolvedAt != nil {
			keep = 1
			status = i18n.Text("api.tasks.prompts.brief_thread_resolved_by", "who", th.Root.ResolvedBy)
		}
		b.WriteString(i18n.Text("api.tasks.prompts.brief_thread_summary",
			"thread", th.Root.ID, "replies", len(th.Replies), "status", status))
		replies := th.Replies
		if th.Root.ID != fullThread && len(replies) > keep {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_replies_hidden", "hidden", len(replies)-keep))
			replies = replies[len(replies)-keep:]
		}
		for j := range replies {
			writeBriefComment(b, "#### ↳", &replies[j])
		}
	}
}

func briefThreads(comments []task.Comment) []task.Thread {
	all := task.Threads(comments)
	out := make([]task.Thread, 0, len(all))
	for i := range all {
		th := all[i]
		replies := make([]task.Comment, 0, len(th.Replies))
		for j := range th.Replies {
			if th.Replies[j].Source != task.SourceSystem {
				replies = append(replies, th.Replies[j])
			}
		}
		th.Replies = replies
		if th.Root.Source == task.SourceSystem && len(replies) == 0 {
			continue
		}
		out = append(out, th)
	}
	return out
}

func writeBriefTestCases(b *strings.Builder, t *task.Task) {
	if len(t.TestCases) == 0 {
		return
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_test_cases_heading"))
	b.WriteString(i18n.Text("api.tasks.prompts.brief_test_cases_intro"))
	for i := range t.TestCases {
		c := &t.TestCases[i]
		fmt.Fprintf(b, "- **%s** (%s) — %s", c.Key, c.Status, c.Title)
		if len(c.Covers) > 0 {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_test_case_covers", "covers", strings.Join(c.Covers, ", ")))
		}
		if expected := briefLine(c.Expected); expected != "" {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_test_case_expected", "expected", expected))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

const maxBriefLine = 200

func briefLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxBriefLine {
		return string(r[:maxBriefLine]) + "…"
	}
	return s
}

func writeBriefComment(b *strings.Builder, heading string, c *task.Comment) {
	label := string(c.Source)
	if c.Author != "" {
		label = fmt.Sprintf("%s (%s)", c.Source, c.Author)
	}
	// A decision or a stop says what it is, so the agent reads a send-back's reason as one.
	if c.Kind != "" && c.Kind != task.CommentPlain {
		kind := string(c.Kind)
		if c.Meta != nil && c.Meta.Action != "" {
			kind += ": " + c.Meta.Action
		}
		label += " · " + kind
	}
	fmt.Fprintf(b, "%s %s — %s\n\n%s\n\n", heading, label, c.CreatedAt.Format("2006-01-02 15:04"), c.Body)
}

func (s *Server) writeBriefAuditedRun(ctx context.Context, b *strings.Builder, taskID, instanceID string) {
	reports, err := s.taskStore.ListReports(ctx, taskID)
	if err != nil {
		return
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_audit_heading", "run", instanceID))
	found := false
	for i := range reports {
		if reports[i].InstanceID != instanceID || reports[i].Source == task.SourceAuditor {
			continue
		}
		found = true
		fmt.Fprintf(b, "### %s (%s)\n\n%s\n\n", reports[i].Kind, reports[i].Author, reports[i].Body)
	}
	if !found {
		b.WriteString(i18n.Text("api.tasks.prompts.brief_audit_no_report"))
	}
}

func writeBriefApprovals(b *strings.Builder, t *task.Task) {
	if len(t.Approvals) == 0 {
		return
	}
	b.WriteString(i18n.Text("api.approvals.prompts.brief_heading"))
	for i := range t.Approvals {
		a := &t.Approvals[i]
		stage := ""
		if a.Stage != "" {
			stage = i18n.Text("api.approvals.prompts.brief_stage", "stage", a.Stage)
		}
		b.WriteString(i18n.Text("api.approvals.prompts.brief_line", "label", a.Label, "status", a.Status, "stage", stage))
		if note := strings.TrimSpace(a.DecidedNote); note != "" && a.Status != task.ApprovalPending {
			b.WriteString(i18n.Text("api.approvals.prompts.brief_note", "by", a.DecidedBy,
				"note", strings.ReplaceAll(note, "\n", "\n  ")))
		}
	}
	b.WriteString("\n")
}
