package api

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// The webhook side of approvals that wait for required checks. Checks finishing on a pull request
// re-evaluate the task parked on them, which moves on once the gate reads them green. New commits
// on the pull request's head send an approval that waits for checks back to pending: the person
// approved code that is no longer the code that would merge.

// githubChecksHook never fails the delivery; what it cannot read it skips.
func (s *Server) githubChecksHook(ctx context.Context, event string, raw []byte) {
	switch event {
	case "check_suite", "check_run", "workflow_run":
		s.checksCompleted(ctx, raw)
	case "pull_request":
		s.prSynchronized(ctx, raw)
	}
}

type checksHookPayload struct {
	Action string `json:"action"`
	prHookRefs
}

// checksCompleted re-evaluates each task in awaiting_approval whose pull request the finished
// checks ran on. ApprovalsSettled claims the task before moving it, so a webhook delivered twice,
// or a check_run and its check_suite arriving together, move it once.
func (s *Server) checksCompleted(ctx context.Context, raw []byte) {
	if s.taskDispatcher == nil {
		return
	}
	var ev checksHookPayload
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Action != "completed" || ev.Repository.FullName == "" {
		return
	}
	var numbers []int
	for _, run := range []prHookRun{ev.CheckSuite, ev.CheckRun, ev.WorkflowRun} {
		for _, pr := range run.PullRequests {
			if pr.Number > 0 && !slices.Contains(numbers, pr.Number) {
				numbers = append(numbers, pr.Number)
			}
		}
	}
	if len(numbers) == 0 {
		return
	}
	ws := s.githubHookWorkspace(ctx, raw)
	if ws == "" {
		return
	}
	// The move outlives the delivery: the next stage's agent launches from it.
	bg := context.WithoutCancel(ctx)
	for _, n := range numbers {
		t := s.taskForPR(ctx, ws, prHTMLURL(ev.Repository.FullName, n))
		if t == nil || t.Status != task.StatusAwaitingApproval {
			continue
		}
		s.taskDispatcher.ApprovalsSettled(bg, t.ID)
	}
}

type prSyncPayload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number    int       `json:"number"`
		HTMLURL   string    `json:"html_url"`
		UpdatedAt time.Time `json:"updated_at"`
		Head      struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// prSynchronized sends back to pending every approved approval of the task that waits for checks
// and that the push came after. The pull request's updated_at dates the push: a delivery that
// repeats finds the approval pending, or approved again after that time, and reopens nothing.
func (s *Server) prSynchronized(ctx context.Context, raw []byte) {
	var ev prSyncPayload
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Action != "synchronize" || ev.PullRequest.HTMLURL == "" {
		return
	}
	ws := s.githubHookWorkspace(ctx, raw)
	if ws == "" {
		return
	}
	t := s.taskForPR(ctx, ws, ev.PullRequest.HTMLURL)
	if t == nil {
		return
	}
	// GitHub dates the push to the second, rounding down: an approval decided within that second may
	// have come before the push, so the cutoff is the end of it. Approving again inside that second
	// gets reset too, which costs one more click; keeping an approval of code that is no longer the
	// head would let it merge unseen.
	pushedAt := ev.PullRequest.UpdatedAt.Truncate(time.Second).Add(time.Second)
	if ev.PullRequest.UpdatedAt.IsZero() {
		pushedAt = time.Now()
	}
	list, err := s.taskStore.ListApprovals(ctx, t.ID)
	if err != nil {
		logx.Warn(err, "api.approvals.log.checks_reset", "task", t.ID)
		return
	}
	reg := s.stagesFor(t)
	cur, curKnown := reg.Get(t.Stage)
	var ids []string
	for i := range list {
		a := &list[i]
		if a.Status != task.ApprovalApproved || !task.WaitsForChecks(reg, a) {
			continue
		}
		// An approval of a stage the task already left gates nothing any more.
		if st, ok := reg.Get(a.Stage); curKnown && ok && st.Position < cur.Position {
			continue
		}
		ids = append(ids, a.ID)
	}
	reopened, err := s.taskStore.ReopenApprovedBefore(ctx, t.ID, ids, pushedAt)
	if err != nil {
		logx.Warn(err, "api.approvals.log.checks_reset", "task", t.ID)
		return
	}
	actor := "github:" + ev.Sender.Login
	if ev.Sender.Login == "" {
		actor = "github"
	}
	for i := range reopened {
		a := &reopened[i]
		s.taskStore.RecordApprovalEvent(ctx, task.EventApprovalReset, a, actor, "task.approvals.events.reset_pushed")
		n := task.NoteOf("api.approvals.comments.reset_pushed", "approval", a.Label,
			"number", ev.PullRequest.Number, "url", ev.PullRequest.HTMLURL, "sha", shortSHA(ev.PullRequest.Head.SHA))
		if _, err := s.taskStore.AddNote(ctx, t.ID, "", task.SourceSystem, "octopus", n); err != nil {
			logx.Warn(err, "api.approvals.log.checks_note", "task", t.ID)
		}
	}
}

// githubHookWorkspace attributes a delivery to the workspace its installation belongs to.
func (s *Server) githubHookWorkspace(ctx context.Context, raw []byte) string {
	var ev githubEventPayload
	if json.Unmarshal(raw, &ev) != nil {
		return ""
	}
	return s.githubEventWorkspace(ctx, ev)
}

// prHTMLURL is the web address GitHub gives a pull request, the form a task's pr_urls keep.
func prHTMLURL(fullName string, number int) string {
	return githubcfg.WebURL() + "/" + fullName + "/pull/" + strconv.Itoa(number)
}
