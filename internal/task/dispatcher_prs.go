package task

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
)

type ForcePRStatus string

const (
	ForcePRStatusOpened         ForcePRStatus = "opened"
	ForcePRStatusAlreadyOpen    ForcePRStatus = "already_open"
	ForcePRStatusNoToken        ForcePRStatus = "no_token"
	ForcePRStatusNoSecretsStore ForcePRStatus = "no_secrets_store"
	ForcePRStatusBadRemote      ForcePRStatus = "bad_remote"
	ForcePRStatusProjectMissing ForcePRStatus = "project_missing"
	ForcePRStatusOpenFailed     ForcePRStatus = "open_failed"
)

type ForcePRResult struct {
	ProjectID   string        `json:"project_id"`
	ProjectName string        `json:"project_name,omitempty"`
	Pushed      bool          `json:"pushed,omitempty"`
	Status      ForcePRStatus `json:"status"`
	Message     string        `json:"message,omitempty"`
	PRURL       string        `json:"pr_url,omitempty"`
}

func (d *Dispatcher) EnsurePRs(ctx context.Context, t *Task) (*Task, []ForcePRResult) {
	if t == nil {
		return nil, nil
	}
	if t.IsSubtask() {
		t = d.rootTask(ctx, t)
	}
	if t.PRURLs == nil {
		t.PRURLs = map[string]string{}
	}
	results := make([]ForcePRResult, 0, len(t.ProjectIDs))
	changed := false
	for _, projectID := range t.ProjectIDs {
		if d.uploadProject(ctx, projectID) {
			continue
		}
		res, wrote := d.forcePRForProject(ctx, t, projectID)
		changed = changed || wrote
		results = append(results, res)
	}
	if changed {
		if updated, err := d.store.Update(ctx, t.ID, Patch{PRURLs: t.PRURLs}); err == nil {
			return updated, results
		}
	}
	return t, results
}

func (d *Dispatcher) forcePRForProject(ctx context.Context, t *Task, projectID string) (ForcePRResult, bool) {
	pushed := d.forcePushIfNeeded(ctx, t, projectID)
	res := d.openFallbackPRForProject(ctx, t, projectID, i18n.Text("task.prompts.pr_footer_force"))
	res.Pushed = pushed
	wrote := d.recordPROutcome(ctx, t, projectID, res, "force_pr")
	return res, wrote
}

func (d *Dispatcher) recordPROutcome(ctx context.Context, t *Task, projectID string, res ForcePRResult, trigger string) bool {
	switch res.Status {
	case ForcePRStatusOpened:
		t.PRURLs[projectID] = res.PRURL
		meta := map[string]string{"project": projectID, "url": res.PRURL, "fallback": "true"}
		label, comment := "fallback-pr", NoteOf("task.comments.fallback_pr_opened", "url", res.PRURL)
		if trigger == "force_pr" {
			meta["trigger"] = trigger
			label, comment = "force-pr", NoteOf("task.comments.force_pr_opened", "url", res.PRURL)
		}
		logx.Warn(d.store.RecordEvent(ctx, t.ID, EventPROpened, res.PRURL, "octopus", res.PRURL, meta),
			"task.log.record_pr_opened", "label", label, "task", t.ID)
		d.systemComment(ctx, t.ID, comment)
		return true
	case ForcePRStatusAlreadyOpen:
		if res.PRURL == "" || t.PRURLs[projectID] != "" {
			return false
		}
		t.PRURLs[projectID] = res.PRURL
		logx.Warn(d.store.RecordEvent(ctx, t.ID, EventPROpened, res.PRURL, "octopus", res.PRURL,
			map[string]string{"project": projectID, "url": res.PRURL, "discovered": "true"}),
			"task.log.record_pr_discovered", "task", t.ID)
		d.systemComment(ctx, t.ID, NoteOf("task.comments.pr_linked_existing", "url", res.PRURL))
		return true
	default:
		if msg := forcePRComment(res); !msg.Empty() {
			d.systemComment(ctx, t.ID, msg)
		}
	}
	return false
}

// The caller is an agent and webhook automations find a PR's card through pr_urls, so only a PR on
// one of the task's linked repos is accepted:
// a card claiming a stranger's PR would receive its review comments.
func (d *Dispatcher) RecordAgentPR(ctx context.Context, t *Task, repoFullName, prURL string) (*Task, string, error) {
	if t == nil {
		return nil, "", apperr.Invalid("task.errors.task_required")
	}
	if strings.TrimSpace(prURL) == "" {
		return nil, "", apperr.Invalid("task.errors.pr_url_required")
	}
	if t.IsSubtask() {
		t = d.rootTask(ctx, t)
	}
	owner, repo, ok := parsePRURL(prURL)
	if !ok {
		return nil, "", apperr.Invalid("task.errors.pr_url_not_a_pull_request", "url", prURL)
	}
	if o, r, ok := parseRepoName(repoFullName); ok && (!strings.EqualFold(o, owner) || !strings.EqualFold(r, repo)) {
		return nil, "", apperr.Invalid("task.errors.pr_repo_mismatch", "repo", repoFullName, "url", prURL)
	}
	projectID, err := d.projectForRepo(ctx, t, owner+"/"+repo)
	if err != nil {
		return nil, "", err
	}
	if t.PRURLs == nil {
		t.PRURLs = map[string]string{}
	}
	if existing := t.PRURLs[projectID]; existing != "" {
		return t, projectID, nil
	}
	t.PRURLs[projectID] = prURL
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventPROpened, prURL, "agent", prURL,
		map[string]string{"project": projectID, "url": prURL, "source": "agent"}),
		"task.log.record_agent_pr", "task", t.ID)
	d.systemComment(ctx, t.ID, NoteOf("task.comments.agent_opened_pr", "url", prURL))
	updated, err := d.store.Update(ctx, t.ID, Patch{PRURLs: t.PRURLs})
	if err != nil {
		return nil, "", apperr.Wrap(err, "task.errors.persist_pr_url")
	}
	return updated, projectID, nil
}

// A sole linked project must still match, or a task could claim any pull request as its own.
func (d *Dispatcher) projectForRepo(ctx context.Context, t *Task, repoFullName string) (string, error) {
	if len(t.ProjectIDs) == 0 {
		return "", apperr.Invalid("task.errors.pr_no_linked_project")
	}
	wantOwner, wantRepo, ok := parseRepoName(repoFullName)
	if !ok {
		return "", apperr.Invalid("task.errors.pr_repo_ambiguous", "repo", repoFullName)
	}
	if d.projects == nil {
		return "", apperr.Invalid("task.errors.pr_repo_no_match", "repo", repoFullName)
	}
	for _, projectID := range t.ProjectIDs {
		proj, err := d.projects.Get(ctx, projectID)
		if err != nil {
			continue
		}
		owner, repo, ok := github.ParseRemote(proj.RepoURL)
		if ok && strings.EqualFold(owner, wantOwner) && strings.EqualFold(repo, wantRepo) {
			return projectID, nil
		}
	}
	return "", apperr.Invalid("task.errors.pr_repo_no_match", "repo", repoFullName)
}

func parseRepoName(s string) (owner, repo string, ok bool) {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".git")
	if parts := strings.Split(s, "/"); len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.Contains(s, ":") {
		return parts[0], parts[1], true
	}
	return github.ParseRemote(s)
}

func parsePRURL(raw string) (owner, repo string, ok bool) {
	owner, repo, _, ok = github.ParsePRURL(raw)
	return owner, repo, ok
}

// Use pushBranch, not PushBranch: only it carries the project's credential (private repos,
// Kubernetes).
func (d *Dispatcher) forcePushIfNeeded(ctx context.Context, t *Task, projectID string) bool {
	if existing, ok := t.PRURLs[projectID]; ok && existing != "" {
		return false
	}
	pushed, perr := d.pushBranch(ctx, t, projectID)
	if perr != nil {
		d.systemComment(ctx, t.ID, NoteOf("task.comments.force_pr_push_failed", "project", projectID, "err", perr))
		return false
	}
	if !pushed {
		return false
	}
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventBranchCreated, project.TaskBranchName(t.ID),
		"octopus", i18n.Text("task.comments.force_pr_branch_pushed"),
		map[string]string{"project": projectID, "branch": project.TaskBranchName(t.ID), "trigger": "force_pr"}),
		"task.log.record_force_pr_branch", "task", t.ID)
	return true
}

func (d *Dispatcher) openFallbackPRForProject(ctx context.Context, t *Task, projectID, footer string) ForcePRResult {
	res := ForcePRResult{ProjectID: projectID}
	if existing, ok := t.PRURLs[projectID]; ok && existing != "" {
		res.Status = ForcePRStatusAlreadyOpen
		res.PRURL = existing
		return res
	}
	proj, err := d.projects.Get(ctx, projectID)
	if err != nil {
		res.Status = ForcePRStatusProjectMissing
		res.Message = apperr.LocalizeCtx(ctx, err)
		return res
	}
	res.ProjectName = proj.Name
	owner, repo, ok := github.ParseRemote(proj.RepoURL)
	if !ok {
		res.Status = ForcePRStatusBadRemote
		res.Message = i18n.TCtx(ctx, "task.hints.pr_bad_remote", "url", proj.RepoURL)
		return res
	}
	if d.secrets == nil {
		res.Status = ForcePRStatusNoSecretsStore
		res.Message = i18n.TCtx(ctx, "task.hints.pr_no_secrets_store")
		return res
	}
	tok, err := d.secrets.GetGithubToken(ctx, projectID)
	if err != nil {
		res.Status = ForcePRStatusNoToken
		res.Message = i18n.TCtx(ctx, "task.hints.pr_no_token")
		return res
	}
	branch := project.TaskBranchName(t.ID)
	title := t.Title
	body := buildPRBody(ctx, d.store, t) + footer
	out, err := github.OpenPR(ctx, tok.Token, github.OpenPRReq{
		Owner: owner,
		Repo:  repo,
		Head:  branch,
		Base:  proj.DefaultBranch,
		Title: title,
		Body:  body,
	})
	if errors.Is(err, github.ErrAlreadyExists) {
		// GitHub's 422 for an existing PR does not say which one, so look it up by head.
		res.Status = ForcePRStatusAlreadyOpen
		res.Message = apperr.LocalizeCtx(ctx, err)
		if found, ferr := github.FindPRByHead(ctx, tok.Token, owner, repo, branch); ferr != nil {
			logx.Warn(ferr, "task.log.find_existing_pr", "task", t.ID, "project", projectID)
		} else if found != nil {
			res.PRURL = found.HTMLURL
		}
		return res
	}
	if err != nil {
		res.Status = ForcePRStatusOpenFailed
		res.Message = apperr.LocalizeCtx(ctx, err)
		return res
	}
	res.Status = ForcePRStatusOpened
	res.PRURL = out.HTMLURL
	return res
}

func forcePRComment(res ForcePRResult) Note {
	name := res.ProjectName
	if name == "" {
		name = res.ProjectID
	}
	switch res.Status {
	case ForcePRStatusProjectMissing:
		return NoteOf("task.comments.fallback_pr_project_missing", "project", name, "msg", res.Message)
	case ForcePRStatusBadRemote:
		return NoteOf("task.comments.fallback_pr_bad_remote", "msg", res.Message)
	case ForcePRStatusNoSecretsStore:
		return NoteOf("task.comments.fallback_pr_no_secrets_store")
	case ForcePRStatusNoToken:
		return NoteOf("task.comments.fallback_pr_no_token", "project", name)
	case ForcePRStatusOpenFailed:
		return NoteOf("task.comments.fallback_pr_failed", "project", name, "msg", res.Message)
	case ForcePRStatusOpened, ForcePRStatusAlreadyOpen:
	}
	return Note{}
}

func (d *Dispatcher) ensurePullRequests(ctx context.Context, t *Task, stageLabel string) *Task {
	if t.PRURLs == nil {
		t.PRURLs = map[string]string{}
	}
	changed := false
	for _, projectID := range t.ProjectIDs {
		if d.uploadProject(ctx, projectID) {
			continue
		}
		res := d.openFallbackPRForProject(ctx, t, projectID,
			i18n.Text("task.prompts.pr_footer_fallback", "stage", stageLabel))
		if d.recordPROutcome(ctx, t, projectID, res, "") {
			changed = true
		}
	}
	if changed {
		if updated, err := d.store.Update(ctx, t.ID, Patch{PRURLs: t.PRURLs}); err == nil {
			return updated
		}
	}
	return t
}

func (d *Dispatcher) uploadProject(ctx context.Context, projectID string) bool {
	if d.projects == nil {
		return false
	}
	proj, err := d.projects.Get(ctx, projectID)
	return err == nil && proj.Hosted()
}

func buildPRBody(ctx context.Context, store *Store, t *Task) string {
	var b strings.Builder
	b.WriteString(i18n.Text("task.prompts.pr_body_task", "id", t.ID))
	if t.Body != "" {
		b.WriteString(t.Body)
		b.WriteString("\n\n")
	}
	comments, err := store.ListComments(ctx, t.ID)
	logx.Warn(err, "task.log.pr_body_list_comments", "task", t.ID)
	if len(comments) > 0 {
		b.WriteString(i18n.Text("task.prompts.pr_body_workflow_notes"))
		for i := range comments {
			c := &comments[i]
			if (c.Source != SourceAgent && c.Source != SourceAuditor) || c.ParentID != "" {
				continue
			}
			fmt.Fprintf(&b, "**%s** (%s) — %s\n\n%s\n\n",
				c.Author, c.Source, c.CreatedAt.Format("2006-01-02 15:04"), c.Body)
		}
	}
	return b.String()
}
