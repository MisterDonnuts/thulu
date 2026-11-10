package api

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// The GitHub token never enters the agent's pod or the sandbox: agents push to the daemon at
// /git/<task>/<project>, and the daemon publishes to GitHub with the project's token.

// Long enough that a clone and the fetch that follows it seconds later share one refresh.
const defaultRefreshEvery = 20 * time.Second

const agentPublishTimeout = 2 * time.Minute

type agentGitState struct {
	mu        sync.Mutex
	publish   map[string]*sync.Mutex
	refreshed map[string]time.Time
}

// Held around one integrate-then-push of a task's branch, so the agent's push and the end-of-stage
// push never interleave.
func (g *agentGitState) publishLock(key string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.publish == nil {
		g.publish = map[string]*sync.Mutex{}
	}
	m, ok := g.publish[key]
	if !ok {
		m = &sync.Mutex{}
		g.publish[key] = m
	}
	return m
}

func (g *agentGitState) refreshDue(projectID string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refreshed == nil {
		g.refreshed = map[string]time.Time{}
	}
	if last, ok := g.refreshed[projectID]; ok && now.Sub(last) < defaultRefreshEvery {
		return false
	}
	g.refreshed[projectID] = now
	return true
}

// Must match how resolveGitRepo picks the directory.
func (s *Server) gitRepoProject(ctx context.Context, repoPath string) (*task.Task, string, bool) {
	taskID, name, ok := strings.Cut(repoPath, "/")
	if !ok || taskID == "" || name == "" {
		return nil, "", false
	}
	t, err := s.taskStore.Get(ctx, taskID)
	if err != nil || t == nil {
		return nil, "", false
	}
	for _, pid := range t.ProjectIDs {
		if proj, perr := s.projectStore.Get(ctx, pid); perr == nil && proj != nil && filepath.Base(proj.Path) == name {
			return t, pid, true
		}
	}
	return nil, "", false
}

func (s *Server) refreshDefaultBeforeFetch(r *http.Request, repoPath string) {
	if s.kube == nil || r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/info/refs") ||
		r.URL.Query().Get("service") != "git-upload-pack" {
		return
	}
	t, projID, ok := s.gitRepoProject(r.Context(), repoPath)
	if !ok || !s.agentGit.refreshDue(projID, time.Now()) {
		return
	}
	if err := s.projectStore.FetchDefaultBranch(r.Context(), projID, s.projectGithubToken(r.Context(), projID)); err != nil {
		log.Printf("[git] %s", i18n.Log("api.git.log.refresh_default_failed", "task", t.ID, "project", projID, "err", err))
	}
}

// Runs before the handler returns, so the agent's git push finishes only after GitHub has the
// branch (a pr_create right after it depends on this).
// Git's reply is already written by then, so a failure is reported as a task comment.
func (s *Server) publishAfterAgentPush(ctx context.Context, repoPath string, cmds []receivePackCommand) {
	if s.kube == nil {
		return
	}
	t, projID, ok := s.gitRepoProject(ctx, repoPath)
	if !ok {
		return
	}
	result := "refs/heads/" + project.ResultBranchName(t.SandboxRootID())
	touched := false
	for _, c := range cmds {
		if c.Ref == result {
			touched = true
		}
	}
	if !touched {
		return
	}
	if _, _, err := s.pushTaskBranchForProject(ctx, t, projID); err != nil {
		log.Printf("[git] %s", i18n.Log("api.git.log.publish_failed", "task", t.ID, "project", projID, "err", err))
		note := task.NoteOf("api.git.comments.publish_failed", "project", projID, "err", err)
		if _, cerr := s.taskStore.AddNote(ctx, t.ID, "", task.SourceSystem, "octopus", note); cerr != nil {
			log.Printf("[git] %s", i18n.Log("api.git.log.publish_comment_failed", "task", t.ID, "err", cerr))
		}
	}
}
