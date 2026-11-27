package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Who asks, as task.MayAddSkips reads it: the author from the credential and, for a run key, the
// task and stage its run works on.
func skipActorOf(r *http.Request, author auth.Author) task.SkipActor {
	scope := auth.APIKeyRunScope(r.Context())
	return task.SkipActor{
		By: author.Label(), Agent: author.Agent, Auditor: author.IsAuditor(),
		RunStage: task.Stage(scope.Stage), RunTask: scope.TaskID, Delegated: scope.Delegated(),
	}
}

type addStageSkipRequest struct {
	Stage  task.Stage `json:"stage"`
	Reason string     `json:"reason"`
}

// POST /tasks/{id}/stage-skips: a person, or the refinement stage's agent while it runs, skips a
// column of the task. Every other agent gets a 403 and nothing changes.
func (s *Server) handleAddStageSkip(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	var req addStageSkipRequest
	if !decodeBody(w, r, &req) {
		return
	}
	res, err := s.taskStore.UpsertStageSkips(r.Context(), t.ID, skipActorOf(r, author),
		[]task.StageSkip{{Stage: req.Stage, Reason: req.Reason}}, task.SkipSourceAPI)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	if len(res.Refused) > 0 {
		writeError(w, r, 0, res.Refused[0].Err)
		return
	}
	// Nothing added when a person already skips the column: their entry stands.
	skip, _ := res.Task.StageSkip(req.Stage)
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id": t.ID, "skip": skip, "stage_skips": res.Task.StageSkips,
	})
}

type removeStageSkipRequest struct {
	Reason string `json:"reason"`
}

// POST /tasks/{id}/stage-skips/{stage}/remove: an agent whose run works on the task, or a person,
// makes a skipped column run again. A column the task already passed sends it back there.
func (s *Server) handleRemoveStageSkip(w http.ResponseWriter, r *http.Request) {
	if s.taskDispatcher == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.server.errors.dispatcher_unavailable")
		return
	}
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	var req removeStageSkipRequest
	if !decodeBody(w, r, &req) {
		return
	}
	res, err := s.taskDispatcher.RemoveStageSkip(r.Context(), t, task.Stage(r.PathValue("stage")),
		skipActorOf(r, author), req.Reason)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	skips := []task.StageSkip{}
	if res.Task != nil {
		skips = res.Task.StageSkips
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id": t.ID, "removed": res.Removed, "passed": res.Passed, "moved_back": res.MovedBack,
		"deferred": res.Deferred, "stage_skips": skips,
	})
}

// The columns this task skips, for every stage's agent and for the auditor: a later stage whose
// work makes one of them necessary removes the skip. The auditor of the refinement stage also
// learns the risk the spec gave, since skipping on a risky change is what it must question.
func (s *Server) writeBriefSkips(ctx context.Context, b *strings.Builder, t *task.Task, audit bool) {
	reg := s.stagesFor(t)
	cur, _ := reg.Get(t.Stage)
	var lines []string
	for _, sk := range t.StageSkips {
		info, ok := reg.Get(sk.Stage)
		if !ok || stagepkg.SkippableReason(info) != "" {
			continue
		}
		place := i18n.Text("api.tasks.prompts.brief_skip_ahead")
		if info.Position < cur.Position {
			place = i18n.Text("api.tasks.prompts.brief_skip_passed")
		}
		lines = append(lines, i18n.Text("api.tasks.prompts.brief_skip_line",
			"stage", string(sk.Stage), "place", place, "reason", sk.Reason, "by", sk.By))
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_skips_heading"))
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n\n")
	if audit && cur.IngestFlags {
		risk, _ := s.taskStore.LatestFlowSpec(ctx, t.ID)
		if risk == "" {
			risk = "—" //i18n:ignore placeholder for a missing value
		}
		b.WriteString(i18n.Text("api.tasks.prompts.brief_skips_audit", "risk", risk))
		return
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_skips_remove_hint"))
}

// For the stage that writes flags.yaml: which later columns its `skip:` may name, and which never.
func writeBriefSkippable(b *strings.Builder, stages []types.StageInfo, from types.StageInfo) {
	var skippable, never []string
	for i := range stages {
		st := stages[i]
		if st.Position <= from.Position || st.Terminal {
			continue
		}
		if stagepkg.SkippableReason(st) == "" {
			skippable = append(skippable, "`"+string(st.ID)+"`")
		} else {
			never = append(never, "`"+string(st.ID)+"`")
		}
	}
	if len(skippable) == 0 {
		return
	}
	if len(never) == 0 {
		never = []string{"—"} //i18n:ignore placeholder for an empty list
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_stage_skips",
		"skippable", strings.Join(skippable, ", "), "never", strings.Join(never, ", ")))
}
