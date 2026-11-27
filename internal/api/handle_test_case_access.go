package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// "Give the agent access": a person hands Testing what its blocked cases waited for (a test
// account, a staging login) as secrets of the project's evidence environment, and Testing runs those
// cases again. The values go to the environment's sealed store and nowhere else: the answer, the
// decision comment, the event and the brief name the secrets, never their values.

type accessRequest struct {
	Cases         []string          `json:"cases"`
	EnvironmentID string            `json:"environment_id"`
	Secrets       map[string]string `json:"secrets"`
	Reason        *string           `json:"reason,omitempty"`
}

// AccessResult is what POST /tasks/{id}/test-cases/access answers.
type AccessResult struct {
	Cases     []string `json:"cases"`
	Secrets   []string `json:"secrets"`
	CommentID string   `json:"comment_id,omitempty"`
	Turns     int      `json:"turns"`
}

// handleGiveTestCaseAccess serves POST /tasks/{id}/test-cases/access
// {cases, environment_id, secrets: {NAME: value}, reason?}.
func (s *Server) handleGiveTestCaseAccess(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	who, ok := requireCredentialAuthor(w, r)
	if !ok || !signedInPersonOnly(w, r, who, "api.tasks.errors.access_person_only") {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	var body accessRequest
	if !decodeBody(w, r, &body) {
		return
	}
	reason, ok := decisionReason(w, r, author, body.Reason, false, "")
	if !ok {
		return
	}
	if s.taskDispatcher == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.server.errors.dispatcher_unavailable")
		return
	}
	refs, ok := s.accessCases(w, r, t, body.Cases)
	if !ok {
		return
	}
	names, ok := s.storeAccessSecrets(w, r, t, body)
	if !ok {
		return
	}
	// Read before the cases leave blocked: the decision answers the stop that asked for access.
	stop := s.openStop(r.Context(), t.ID)
	_, prev, err := s.taskStore.QueueTestCases(r.Context(), t.ID, refs, who)
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	turns := s.taskDispatcher.RelaunchTurns(r.Context(), t)
	if !s.relaunchNow(w, r, t, author, turns) {
		logx.Warn(s.taskStore.RestoreTestCaseStatuses(context.WithoutCancel(r.Context()), t.ID, prev, who),
			"api.tasks.log.restore_queued_cases", "task", t.ID)
		return
	}
	s.taskStore.SettleStops(r.Context(), t.ID, who)
	commentID := s.postAccessDecision(r.Context(), t, stop, author, reason, refs, names)
	s.recordAccessGiven(r.Context(), t, author, body.EnvironmentID, refs, names, commentID)
	s.recordRelaunchRequested(r.Context(), t, author, turns, commentID)
	writeJSON(w, http.StatusOK, AccessResult{Cases: refs, Secrets: names, CommentID: commentID, Turns: turns})
}

// accessCases resolves the refs to the cases' keys, in the order given; an unknown ref refuses the
// request before anything is stored.
func (s *Server) accessCases(w http.ResponseWriter, r *http.Request, t *task.Task, refs []string) ([]string, bool) {
	var keys []string
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		i := slices.IndexFunc(t.TestCases, func(c task.TestCase) bool { return c.Key == ref || c.ID == ref })
		if i < 0 {
			writeErrKey(w, r, http.StatusNotFound, "api.tasks.errors.access_unknown_case", "case", ref)
			return nil, false
		}
		if k := t.TestCases[i].Key; !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.access_no_cases")
		return nil, false
	}
	return keys, true
}

// storeAccessSecrets writes the secrets to the evidence environment of one of the task's projects,
// with the environment's own permission and limits. It returns the names, sorted.
func (s *Server) storeAccessSecrets(w http.ResponseWriter, r *http.Request, t *task.Task, body accessRequest) ([]string, bool) {
	names := make([]string, 0, len(body.Secrets))
	for name, v := range body.Secrets {
		if strings.TrimSpace(v) == "" {
			writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.access_secret_empty", "name", name)
			return nil, false
		}
		names = append(names, name)
	}
	sort.Strings(names)
	envID := strings.TrimSpace(body.EnvironmentID)
	if envID == "" {
		if len(names) > 0 {
			writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.access_environment_required")
			return nil, false
		}
		return names, true
	}
	if s.evidenceStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.evidence.errors.unavailable")
		return nil, false
	}
	ws := t.WorkspaceID
	if ws == "" {
		var ok bool
		if ws, ok = s.targetWorkspace(w, r); !ok {
			return nil, false
		}
	}
	env, ok := s.evidenceEnv(w, r, ws, envID)
	if !ok {
		return nil, false
	}
	if env.ProjectID == "" || !slices.Contains(t.ProjectIDs, env.ProjectID) {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.access_environment_other_project", "environment", env.Name)
		return nil, false
	}
	if len(names) == 0 {
		return names, true
	}
	if !allowedOn(w, r, env, evidence.ActionSecrets) {
		return nil, false
	}
	in := make(map[string]*string, len(body.Secrets))
	for name := range body.Secrets {
		v := body.Secrets[name]
		in[name] = &v
	}
	if _, err := s.evidenceStore.SetSecrets(r.Context(), ws, env.ID, in); err != nil {
		writeEvidenceErr(w, r, err)
		return nil, false
	}
	return names, true
}

// postAccessDecision answers the stop with the person's reason, or with a line naming the cases and
// the secrets when they gave none. Secret names only.
func (s *Server) postAccessDecision(ctx context.Context, t *task.Task, stopID string, author auth.Author, reason string,
	cases, secrets []string,
) string {
	in := task.NewComment{
		TaskID: t.ID, ThreadID: stopID, Source: commentSource(author), Author: author.Name, Body: reason,
		Kind: task.CommentDecision,
		Meta: &task.CommentMeta{Action: task.DecisionAccess, FromStage: t.Stage, ToStage: t.Stage, Cases: cases, Secrets: secrets},
	}
	if reason == "" {
		note := task.NoteOf("api.tasks.comments.access_given", "cases", strings.Join(cases, ", "))
		if len(secrets) > 0 {
			note = task.NoteOf("api.tasks.comments.access_given_secrets", "cases", strings.Join(cases, ", "),
				"secrets", strings.Join(secrets, ", "))
		}
		in.Note = &note
	}
	c, err := s.taskStore.PostComment(ctx, in)
	if err != nil {
		logx.Warn(err, "api.tasks.log.post_decision", "task", t.ID)
		return ""
	}
	return c.ID
}

func (s *Server) recordAccessGiven(ctx context.Context, t *task.Task, author auth.Author, envID string, cases, secrets []string,
	commentID string,
) {
	payload := map[string]any{"cases": cases, "secrets": secrets, "environment_id": envID, "stage": string(t.Stage)}
	if commentID != "" {
		payload["comment_id"] = commentID
	}
	s.recordEvent(ctx, t.ID, task.EventTestCasesAccessGiven, "", author.Label(),
		i18n.Text("api.tasks.timeline.access_given", "cases", strings.Join(cases, ", "), "count", len(secrets)), payload)
}

// accessGiven is what the brief of the relaunched Testing run reads: the cases still queued by an
// access given in the stage the task is in, and the secret names and environments people gave for
// them. A run of another stage (a developer after a kick-back) reads nothing.
type accessGiven struct {
	cases        []string
	secrets      []string
	environments []string
}

func (s *Server) accessGivenFor(ctx context.Context, t *task.Task) accessGiven {
	var out accessGiven
	var queued []string
	for i := range t.TestCases {
		if t.TestCases[i].Status == task.TestCaseQueued {
			queued = append(queued, t.TestCases[i].Key)
		}
	}
	if len(queued) == 0 {
		return out
	}
	named := map[string]bool{}
	for _, p := range s.accessesHere(ctx, t, queued) {
		for _, k := range p.Cases {
			named[k] = true
		}
		for _, n := range p.Secrets {
			if !slices.Contains(out.secrets, n) {
				out.secrets = append(out.secrets, n)
			}
		}
		if name := s.evidenceEnvName(ctx, t, p.EnvironmentID); name != "" && !slices.Contains(out.environments, name) {
			out.environments = append(out.environments, name)
		}
	}
	for _, k := range queued {
		if named[k] {
			out.cases = append(out.cases, k)
		}
	}
	sort.Strings(out.secrets)
	return out
}

type accessPayload struct {
	Cases         []string `json:"cases"`
	Secrets       []string `json:"secrets"`
	EnvironmentID string   `json:"environment_id"`
	Stage         string   `json:"stage"`
}

// accessesHere is every access given in the stage the task is in that names one of the queued
// cases. An event from before the stage was recorded counts wherever the task is.
func (s *Server) accessesHere(ctx context.Context, t *task.Task, queued []string) []accessPayload {
	events, err := s.taskStore.ListEvents(ctx, t.ID)
	if err != nil {
		logx.Warn(err, "api.tasks.log.brief_access", "task", t.ID)
		return nil
	}
	var out []accessPayload
	for i := range events {
		if events[i].Kind != task.EventTestCasesAccessGiven {
			continue
		}
		var p accessPayload
		if json.Unmarshal(events[i].Payload, &p) != nil || (p.Stage != "" && p.Stage != string(t.Stage)) ||
			!slices.ContainsFunc(p.Cases, func(k string) bool { return slices.Contains(queued, k) }) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// taskRunSecretEnv is where a task run's key finds a secret it may read: the environment of the
// newest access given to its task, in the stage the task is in, that names the secret. The key's
// stage has to be the task's stage, so a key minted for another column reads nothing, and a secret
// of the same environment that nobody gave this task stays out of reach.
func (s *Server) taskRunSecretEnv(w http.ResponseWriter, r *http.Request, scope auth.RunScope, name string) (ws, envID string, ok bool) {
	if s.evidenceStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.evidence.errors.unavailable")
		return "", "", false
	}
	ctx := r.Context()
	caller, _ := s.callerWorkspace(r)
	if t, err := s.taskStore.Get(ctx, scope.TaskID); err == nil && t.WorkspaceID == caller && string(t.Stage) == scope.Stage {
		envID = s.givenSecretEnv(ctx, t, name)
		ws = t.WorkspaceID
	}
	if envID == "" {
		writeErrKey(w, r, http.StatusNotFound, "api.evidence.errors.secret_not_found", "name", name)
		return "", "", false
	}
	return ws, envID, true
}

func (s *Server) givenSecretEnv(ctx context.Context, t *task.Task, name string) string {
	events, err := s.taskStore.ListEvents(ctx, t.ID)
	if err != nil {
		logx.Warn(err, "api.tasks.log.brief_access", "task", t.ID)
		return ""
	}
	envID := ""
	for i := range events {
		if events[i].Kind != task.EventTestCasesAccessGiven {
			continue
		}
		var p accessPayload
		if json.Unmarshal(events[i].Payload, &p) == nil && (p.Stage == "" || p.Stage == string(t.Stage)) &&
			p.EnvironmentID != "" && slices.Contains(p.Secrets, name) {
			envID = p.EnvironmentID
		}
	}
	return envID
}

func (s *Server) evidenceEnvName(ctx context.Context, t *task.Task, envID string) string {
	if envID == "" || s.evidenceStore == nil {
		return ""
	}
	env, err := s.evidenceStore.GetEnvironment(ctx, t.WorkspaceID, envID)
	if err != nil {
		return envID
	}
	if env.DisplayName != "" {
		return env.DisplayName
	}
	return env.Name
}

// writeBriefAccessGiven tells the relaunched Testing run which cases to run and which secrets a
// person just gave it.
func (s *Server) writeBriefAccessGiven(ctx context.Context, b *strings.Builder, t *task.Task) {
	acc := s.accessGivenFor(ctx, t)
	if len(acc.cases) == 0 {
		return
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_access_heading"))
	b.WriteString(i18n.Text("api.tasks.prompts.brief_access_cases", "cases", strings.Join(acc.cases, ", ")))
	if len(acc.secrets) > 0 {
		env := strings.Join(acc.environments, ", ")
		if env == "" {
			env = i18n.Text("api.tasks.prompts.brief_access_env_unknown")
		}
		b.WriteString(i18n.Text("api.tasks.prompts.brief_access_secrets", "secrets", strings.Join(acc.secrets, ", "), "environment", env))
	}
	b.WriteString("\n")
}
