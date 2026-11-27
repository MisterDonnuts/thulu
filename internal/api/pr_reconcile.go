package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	ghrest "github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// The reconciler covers what webhooks miss: Docker installs with no public URL, personal tokens
// without the App, lost deliveries. It reads only the PRs of tasks waiting between the stage that
// opened them and the merge_target stage, one GET per PR.
const (
	prReconcileFirst = 2 * time.Minute
	prReconcileEvery = 10 * time.Minute
	// A repository whose webhook arrived this recently is left to its webhooks.
	prReconcileHookQuiet = 30 * time.Minute
	// GitHub reads per round, across every workspace: 6 rounds an hour stay far under the 5000
	// requests an installation gets.
	prReconcileReads = 60
	// How long a PR closed without merging, or merged into another branch, goes unread.
	prReconcileSilence = 24 * time.Hour
)

type reconcileStats struct {
	Read, Merged, Moved int
}

func (s *Server) StartPRReconciler(ctx context.Context) {
	if s.taskStore == nil || s.boards == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(prReconcileFirst):
		}
		t := time.NewTicker(prReconcileEvery)
		defer t.Stop()
		for {
			st := s.reconcileMergedPRs(ctx, time.Now())
			if st.Merged > 0 || st.Moved > 0 {
				log.Printf("[github] %s", i18n.Log("api.merges.log.reconciled", "read", st.Read, "merged", st.Merged, "moved", st.Moved))
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (s *Server) reconcileMergedPRs(ctx context.Context, now time.Time) reconcileStats {
	var st reconcileStats
	budget := prReconcileReads
	for _, b := range s.boards.BoardList() {
		if _, ok := mergeTargetOf(s.boards.Board(b.ID)); !ok {
			continue
		}
		tasks, err := s.taskStore.ListByBoard(ctx, b.ID)
		if err != nil {
			logx.Warn(err, "api.merges.log.reconcile_list", "board", b.ID)
			continue
		}
		for i := range tasks {
			if ctx.Err() != nil {
				return st
			}
			s.reconcileTask(ctx, &tasks[i], &budget, now, &st)
		}
	}
	return st
}

func (s *Server) reconcileTask(ctx context.Context, t *task.Task, budget *int, now time.Time, st *reconcileStats) {
	if !s.waitingForMerge(t) {
		return
	}
	for pid, url := range t.PRURLs {
		if url == "" || t.MergeSHAs[pid] != "" || *budget <= 0 {
			continue
		}
		if s.reconcilePR(ctx, t, pid, url, budget, now) {
			st.Merged++
		}
		st.Read = prReconcileReads - *budget
	}
	// A merge seen while a run worked on the task did not move it; move it now the run is over.
	fresh, err := s.taskStore.GetRow(ctx, t.ID)
	if err != nil || len(fresh.MergeSHAs) == 0 {
		return
	}
	by, ok := s.mergedSinceEntered(ctx, fresh)
	if !ok {
		return
	}
	if out, _ := s.advanceMerged(ctx, fresh, by); out == mergeMoved {
		st.Moved++
	}
}

// The task sits between the first stage that opens a PR and its board's merge_target stage.
func (s *Server) waitingForMerge(t *task.Task) bool {
	if t.ArchivedAt != nil || t.IsSubtask() {
		return false
	}
	reg := s.stagesFor(t)
	target, ok := mergeTargetOf(reg)
	if !ok {
		return false
	}
	cur, known := reg.Get(t.Stage)
	tgt, _ := reg.Get(target)
	return known && !cur.Terminal && cur.Position < tgt.Position && afterPROpening(reg, cur)
}

// reconcilePR reads one PR and reports whether it found it merged.
func (s *Server) reconcilePR(ctx context.Context, t *task.Task, projectID, url string, budget *int, now time.Time) bool {
	owner, repo, number, ok := ghrest.ParsePRURL(url)
	if !ok || s.prMerges.silenced(url, now) || s.prMerges.hookedSince(owner+"/"+repo, now.Add(-prReconcileHookQuiet)) {
		return false
	}
	// Never spend one workspace's credential on a PR URL another workspace's task carries.
	if p, err := s.projectStore.Get(ctx, projectID); err != nil || (p.WorkspaceID != "" && p.WorkspaceID != t.WorkspaceID) {
		return false
	}
	tok, _, err := s.prStatusToken(ctx, projectID)
	if err != nil {
		if !errors.Is(err, githubapp.ErrNoCredential) {
			logx.Warn(err, "api.pull_requests.log.token_failed", "project", projectID)
		}
		return false
	}
	*budget--
	pr, err := ghrest.GetPR(ctx, tok.Token, owner, repo, number)
	if err != nil {
		logx.Warn(err, "api.merges.log.reconcile_read", "url", url)
		return false
	}
	switch {
	case pr.Merged:
		// Nothing about these changes on its own: a merge into another branch, or of another head.
		out := s.prMergedFromAPI(ctx, t, projectID, pr, "", mergeSourceReconcile)
		if out == mergeOtherBranch || out == mergeOtherHead {
			s.prMerges.silence(url, now.Add(prReconcileSilence))
		}
		return true
	case pr.State == "closed":
		s.prMerges.silence(url, now.Add(prReconcileSilence))
	}
	return false
}

// Whether the task's last merge came after it entered its stage, and who merged. A person who moves
// a merged task back on purpose entered the stage after the merge, and the reconciler leaves it there.
// The board's own auto-advance out of a stage after the PR opened does not count: the run a merge
// waited for there ends by moving the task on, and that move must not swallow the merge. One out of
// an earlier stage does: the merge came ahead of the flow and stays recorded only.
func (s *Server) mergedSinceEntered(ctx context.Context, t *task.Task) (by string, ok bool) {
	events, err := s.taskStore.ListEvents(ctx, t.ID)
	if err != nil {
		return "", false
	}
	reg := s.stagesFor(t)
	var merged, entered time.Time
	for i := range events {
		if k := events[i].Kind; k == task.EventBranchMerged {
			merged, by = events[i].CreatedAt, events[i].Actor
		} else if k == task.EventStageEntered && !advancedPastPR(reg, events[i].Payload) {
			entered = events[i].CreatedAt
		}
	}
	return by, !merged.IsZero() && !merged.Before(entered)
}

// An auto-advance out of a stage where a merge would have moved the task.
func advancedPastPR(reg stagepkg.Registry, payload json.RawMessage) bool {
	var p struct {
		Via  string     `json:"via"`
		From task.Stage `json:"from"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Via != task.ViaAutoAdvance {
		return false
	}
	from, ok := reg.Get(p.From)
	return ok && afterPROpening(reg, from)
}
