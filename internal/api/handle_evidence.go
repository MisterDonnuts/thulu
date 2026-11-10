package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Call before Start; until then the /evidence routes answer 503.
func (s *Server) SetEvidence(store *evidence.Store, svc *evidence.Service) {
	s.evidenceStore, s.evidenceSvc = store, svc
}

func (s *Server) evidenceWorkspace(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.evidenceStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.evidence.errors.unavailable")
		return "", false
	}
	return s.targetWorkspace(w, r)
}

func writeEvidenceErr(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, r, apperr.Status(err), err)
}

func splitCSV(raw string) []string {
	var out []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func queryLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		return 0
	}
	return n
}

func (s *Server) handleEvidenceSummary(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	sum, err := s.evidenceStore.Summary(r.Context(), ws, q.Get("project_id"), q.Get("include_unassigned") == "1")
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleListEvidenceCases(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := evidence.CaseFilter{
		WorkspaceID: ws, Q: q.Get("q"), Statuses: splitCSV(q.Get("status")), ProjectID: q.Get("project_id"),
		Tags: splitCSV(q.Get("tag")), Priorities: splitCSV(q.Get("priority")),
		EnvironmentID: q.Get("environment_id"), IncludeUnassigned: q.Get("include_unassigned") == "1",
	}
	cases, counts, err := s.evidenceStore.ListCases(r.Context(), f)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	tags, err := s.evidenceStore.CaseTags(r.Context(), ws, f.ProjectID, f.IncludeUnassigned)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": cases, "counts": counts, "tags": tags})
}

type caseBody struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Steps       string          `json:"steps"`
	Expected    string          `json:"expected"`
	Priority    string          `json:"priority"`
	Tags        []string        `json:"tags"`
	ProjectID   string          `json:"project_id"`
	Take        json.RawMessage `json:"take,omitempty"`
}

func (s *Server) checkProject(w http.ResponseWriter, r *http.Request, ws, projectID string) bool {
	if projectID == "" || s.projectStore == nil {
		return true
	}
	p, err := s.projectStore.Get(r.Context(), projectID)
	if err != nil || (p.WorkspaceID != "" && p.WorkspaceID != ws) {
		writeErrKey(w, r, http.StatusBadRequest, "api.evidence.errors.project_not_found", "id", projectID)
		return false
	}
	return true
}

func (s *Server) handleCreateEvidenceCase(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	author, ok := requireCredentialAuthor(w, r)
	if !ok {
		return
	}
	var body caseBody
	if !decodeBody(w, r, &body) || !s.checkProject(w, r, ws, body.ProjectID) {
		return
	}
	in := evidence.Case{Title: body.Title, Description: body.Description, Steps: body.Steps, Expected: body.Expected,
		Priority: body.Priority, Tags: body.Tags, ProjectID: body.ProjectID}
	if len(body.Take) > 0 && string(body.Take) != "null" {
		take, err := evidence.ValidateTake(body.Take)
		if err != nil {
			writeEvidenceErr(w, r, err)
			return
		}
		in.Take, in.TakeSource = take, evidence.SourceUpload
	}
	c, err := s.evidenceStore.CreateCase(r.Context(), ws, author, in)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleGetEvidenceCase(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	c, err := s.evidenceStore.GetCase(r.Context(), ws, r.PathValue("case"), true)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	runs := evidence.RunFilter{WorkspaceID: ws, CaseID: c.ID, Limit: evidence.RunHistory}
	if c.Runs, err = s.evidenceStore.ListRuns(r.Context(), runs); err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	recs, err := s.evidenceStore.ListRecordings(r.Context(), ws, c.ID)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"case": c, "recordings": recs})
}

func (s *Server) handleUpdateEvidenceCase(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	author, ok := requireCredentialAuthor(w, r)
	if !ok {
		return
	}
	var p evidence.CasePatch
	if !decodeBody(w, r, &p) {
		return
	}
	if p.ProjectID != nil && !s.checkProject(w, r, ws, *p.ProjectID) {
		return
	}
	c, err := s.evidenceStore.UpdateCase(r.Context(), ws, r.PathValue("case"), author, p)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDeleteEvidenceCase(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	_, paths, err := s.evidenceStore.DeleteCase(r.Context(), ws, r.PathValue("case"))
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	removeEvidenceFiles(paths)
	w.WriteHeader(http.StatusNoContent)
}

func removeEvidenceFiles(paths []string) {
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			logx.Warn(err, "api.evidence.log.remove_failed", "path", p)
		}
	}
}

func (s *Server) handlePutEvidenceTake(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	author, ok := requireCredentialAuthor(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.server.errors.invalid_body"))
		return
	}
	take, err := evidence.ValidateTake(raw)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	c, err := s.evidenceStore.SetTake(r.Context(), ws, r.PathValue("case"), author, take, evidence.SourceUpload)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDeleteEvidenceTake(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	author, ok := requireCredentialAuthor(w, r)
	if !ok {
		return
	}
	c, err := s.evidenceStore.SetTake(r.Context(), ws, r.PathValue("case"), author, nil, "")
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleEvidenceCaseFromTask(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	author, ok := requireCredentialAuthor(w, r)
	if !ok {
		return
	}
	var body struct {
		TaskID string `json:"task_id"`
		Case   string `json:"case"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	t, ok := s.taskForCaller(w, r, body.TaskID)
	if !ok {
		return
	}
	if t.WorkspaceID != ws {
		writeErrKey(w, r, http.StatusNotFound, "api.evidence.errors.task_not_found", "id", body.TaskID)
		return
	}
	tc, err := s.taskStore.GetTestCase(r.Context(), t.ID, body.Case)
	if err != nil {
		writeError(w, r, statusForTestCaseErr(err), err)
		return
	}
	in := evidence.Case{Title: tc.Title, Description: tc.Description, Steps: tc.Steps, Expected: tc.Expected,
		Priority: tc.Priority, SourceTaskID: t.ID, SourceCaseKey: tc.Key}
	if take := s.latestTaskTake(r, tc.Runs); take != nil {
		in.Take, in.TakeSource = take, evidence.SourceTask
	}
	c, err := s.evidenceStore.CreateCase(r.Context(), ws, author, in)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) latestTaskTake(r *http.Request, runs []task.TestCaseRun) json.RawMessage {
	sorted := append([]task.TestCaseRun(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].RanAt.After(sorted[j].RanAt) })
	for i := range sorted {
		if sorted[i].TakeAttachmentID == "" {
			continue
		}
		a, err := s.taskStore.GetAttachment(r.Context(), sorted[i].TakeAttachmentID)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(a.Path)
		if err != nil {
			continue
		}
		if take, err := evidence.ValidateTake(raw); err == nil {
			return take
		}
	}
	return nil
}

func callerRole(r *http.Request) string { return string(auth.CallerRole(r.Context())) }

// Literal keys, so the i18n catalog guard sees every one.
var forbiddenKeys = map[string]string{
	evidence.ReasonViewer:             "api.evidence.errors.forbidden_viewer",
	evidence.ReasonProduction:         "api.evidence.errors.forbidden_production",
	evidence.ReasonAdminOnly:          "api.evidence.errors.forbidden_admin_only",
	evidence.ReasonDeleteOnProject:    "api.evidence.errors.forbidden_delete_on_project",
	evidence.ReasonSandboxFixed:       "api.evidence.errors.forbidden_sandbox_fixed",
	evidence.ReasonSandboxNoRecord:    "api.evidence.errors.forbidden_sandbox_no_record",
	evidence.ReasonNoURL:              "api.evidence.errors.forbidden_no_url",
	evidence.ReasonNoInitScript:       "api.evidence.errors.forbidden_no_init_script",
	evidence.ReasonNoPreviewPort:      "api.evidence.errors.forbidden_no_preview_port",
	evidence.ReasonSandboxUnavailable: "api.evidence.errors.forbidden_sandbox_unavailable",
	evidence.ReasonRepoNotGitHub:      "api.evidence.errors.forbidden_repo_not_github",
}

func writeDenied(w http.ResponseWriter, r *http.Request, reason string) {
	status := http.StatusPreconditionFailed
	switch reason {
	case evidence.ReasonViewer, evidence.ReasonProduction, evidence.ReasonAdminOnly:
		status = http.StatusForbidden
	case evidence.ReasonDeleteOnProject, evidence.ReasonSandboxFixed, evidence.ReasonSandboxNoRecord:
		status = http.StatusConflict
	}
	key, ok := forbiddenKeys[reason]
	if !ok {
		key = forbiddenKeys[evidence.ReasonSandboxUnavailable]
	}
	writeErrKey(w, r, status, key)
}

func (s *Server) sandboxInfo(ctx context.Context, projectID, defaultBranch, envID string) *evidence.SandboxInfo {
	info := &evidence.SandboxInfo{EnvironmentID: envID, DefaultBranch: defaultBranch,
		InitScriptURL: "/project/?id=" + url.QueryEscape(projectID) + "#repo"}
	if info.DefaultBranch == "" {
		info.DefaultBranch = "main"
	}
	if s.sandbox.mgr == nil || s.projectStore == nil {
		info.Reason = evidence.ReasonSandboxUnavailable
		return info
	}
	sc, err := s.projectStore.InitScript(ctx, projectID)
	if err != nil {
		info.Reason = evidence.ReasonNoInitScript
		return info
	}
	exposed := s.sandbox.mgr.AppPorts()
	for _, p := range sc.Manifest.Ports {
		if slices.Contains(exposed, p) {
			info.Port = p
			break
		}
	}
	if info.Port == 0 {
		info.Reason = evidence.ReasonNoPreviewPort
		return info
	}
	info.Available = true
	return info
}

func (s *Server) decorateEnv(ctx context.Context, role string, e *evidence.Environment) {
	if e.Kind == evidence.KindSandbox {
		e.Sandbox = s.sandboxInfo(ctx, e.ProjectID, e.DefaultBranch, e.ID)
	}
	e.Can = evidence.EnvPermissions(role, *e)
}

func (s *Server) evidenceEnv(w http.ResponseWriter, r *http.Request, ws, id string) (*evidence.Environment, bool) {
	e, err := s.evidenceStore.GetEnvironment(r.Context(), ws, id)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return nil, false
	}
	s.decorateEnv(r.Context(), callerRole(r), e)
	return e, true
}

func allowedOn(w http.ResponseWriter, r *http.Request, e *evidence.Environment, action string) bool {
	if p := e.Can[action]; !p.Allowed {
		writeDenied(w, r, p.Reason)
		return false
	}
	return true
}

func (s *Server) projectEnvs() evidence.ProjectEnvironments {
	if s.projectStore == nil {
		return nil
	}
	return s.projectStore
}

type contextProject struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	DefaultBranch string                `json:"default_branch"`
	Cases         int                   `json:"cases"`
	Environments  int                   `json:"environments"`
	Sandbox       *evidence.SandboxInfo `json:"sandbox"`
}

func (s *Server) handleEvidenceContext(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	role := callerRole(r)
	// Project rows are otherwise only created on the periodic tick.
	if err := s.evidenceStore.EnsureProjectRows(ctx, ws); err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	envs, err := s.evidenceStore.ListEnvironments(ctx, ws, "")
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	counts, err := s.evidenceStore.CaseCountsByProject(ctx, ws)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	projects := []contextProject{}
	byID := map[string]int{}
	standalone := 0
	for i := range envs {
		e := &envs[i]
		if e.Kind == evidence.KindStandalone {
			standalone++
			continue
		}
		at, seen := byID[e.ProjectID]
		if !seen {
			at = len(projects)
			byID[e.ProjectID] = at
			projects = append(projects, contextProject{ID: e.ProjectID, Name: e.ProjectName,
				DefaultBranch: e.DefaultBranch, Cases: counts[e.ProjectID]})
		}
		switch e.Kind {
		case evidence.KindProject:
			projects[at].Environments++
		case evidence.KindSandbox:
			projects[at].Sandbox = s.sandboxInfo(ctx, e.ProjectID, e.DefaultBranch, e.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role":                    role,
		"can":                     evidence.WorkspacePermissions(role),
		"projects":                projects,
		"unassigned_cases":        counts[""],
		"standalone_environments": standalone,
		"show_project_picker":     len(projects) > 1,
	})
}

func (s *Server) handleListEvidenceEnvironments(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	// Project rows are otherwise only created on the periodic tick.
	if err := s.evidenceStore.EnsureProjectRows(ctx, ws); err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	envs, err := s.evidenceStore.ListEnvironments(ctx, ws, r.URL.Query().Get("project_id"))
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	role := callerRole(r)
	for i := range envs {
		s.decorateEnv(ctx, role, &envs[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"environments": envs})
}

func checkSchedule(w http.ResponseWriter, r *http.Request, expr, tz string) bool {
	expr, tz = strings.TrimSpace(expr), strings.TrimSpace(tz)
	if expr == "" {
		return true
	}
	if tz == "" {
		tz = "UTC"
	}
	if _, err := automation.ParseCron(expr, tz); err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.evidence.errors.schedule_invalid"))
		return false
	}
	return true
}

type envBody struct {
	ProjectID         string   `json:"project_id"`
	Name              string   `json:"name"`
	DisplayName       string   `json:"display_name"`
	BaseURL           string   `json:"base_url"`
	IsProduction      bool     `json:"is_production"`
	AllowedHosts      []string `json:"allowed_hosts"`
	Schedule          string   `json:"schedule"`
	ScheduleTZ        string   `json:"schedule_tz"`
	OnDeploy          bool     `json:"on_deploy"`
	ScheduleProjectID string   `json:"schedule_project_id"`
	ScheduleTag       string   `json:"schedule_tag"`
}

func notEditable(field string) error {
	return apperr.Invalid("evidence.errors.field_not_editable", "field", field, "kind", evidence.KindStandalone)
}

func (s *Server) handleCreateEvidenceEnvironment(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	author, ok := requireCredentialAuthor(w, r)
	if !ok {
		return
	}
	var in envBody
	if !decodeBody(w, r, &in) || !checkSchedule(w, r, in.Schedule, in.ScheduleTZ) {
		return
	}
	if in.IsProduction && !evidence.CanSetProduction(callerRole(r)) {
		writeDenied(w, r, evidence.ReasonProduction)
		return
	}
	var e *evidence.Environment
	var err error
	standalone := in.ProjectID == ""
	switch {
	case standalone && in.DisplayName != "":
		err = notEditable("display_name") //i18n:ignore JSON field name
	case standalone && in.OnDeploy:
		err = notEditable("on_deploy") //i18n:ignore JSON field name
	case standalone:
		e, err = s.evidenceStore.CreateEnvironment(r.Context(), ws, author, evidence.Environment{Name: in.Name,
			BaseURL: in.BaseURL, IsProduction: in.IsProduction, AllowedHosts: in.AllowedHosts,
			Schedule: in.Schedule, ScheduleTZ: in.ScheduleTZ, ScheduleProjectID: in.ScheduleProjectID,
			ScheduleTag: in.ScheduleTag})
	case in.ScheduleProjectID != "" || in.ScheduleTag != "":
		err = apperr.Invalid("evidence.errors.field_not_editable",
			"field", "schedule_project_id", "kind", evidence.KindProject) //i18n:ignore JSON field name
	default:
		projects := s.projectEnvs()
		if projects == nil {
			writeErrKey(w, r, http.StatusServiceUnavailable, "api.server.errors.project_store_unavailable")
			return
		}
		e, err = s.evidenceStore.CreateProjectEnvironment(r.Context(), ws, author, projects, evidence.ProjectEnvironmentInput{
			ProjectID: in.ProjectID, Name: in.Name, DisplayName: in.DisplayName, BaseURL: in.BaseURL,
			IsProduction: in.IsProduction, AllowedHosts: in.AllowedHosts, Schedule: in.Schedule,
			ScheduleTZ: in.ScheduleTZ, OnDeploy: in.OnDeploy})
	}
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	s.decorateEnv(r.Context(), callerRole(r), e)
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) handleUpdateEvidenceEnvironment(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	var p evidence.EnvironmentPatch
	if !decodeBody(w, r, &p) {
		return
	}
	cur, ok := s.evidenceEnv(w, r, ws, r.PathValue("id"))
	if !ok {
		return
	}
	if err := evidence.CheckPatch(cur.Kind, p); err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	if !allowedOn(w, r, cur, evidence.ActionEdit) {
		return
	}
	if p.IsProduction != nil && *p.IsProduction != cur.IsProduction && !evidence.CanSetProduction(callerRole(r)) {
		writeDenied(w, r, evidence.ReasonProduction)
		return
	}
	if p.Schedule != nil || p.ScheduleTZ != nil {
		expr, tz := cur.Schedule, cur.ScheduleTZ
		if p.Schedule != nil {
			expr = *p.Schedule
		}
		if p.ScheduleTZ != nil {
			tz = *p.ScheduleTZ
		}
		if !checkSchedule(w, r, expr, tz) {
			return
		}
	}
	e, err := s.evidenceStore.UpdateEnvironment(r.Context(), ws, cur.ID, p, s.projectEnvs())
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	s.decorateEnv(r.Context(), callerRole(r), e)
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) handleDeleteEvidenceEnvironment(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	cur, ok := s.evidenceEnv(w, r, ws, r.PathValue("id"))
	if !ok || !allowedOn(w, r, cur, evidence.ActionDelete) {
		return
	}
	paths, err := s.evidenceStore.DeleteEnvironment(r.Context(), ws, cur.ID)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	removeEvidenceFiles(paths)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLinkEvidenceEnvironment(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	var body struct {
		ProjectEnvironmentID string `json:"project_environment_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	cur, ok := s.evidenceEnv(w, r, ws, r.PathValue("id"))
	if !ok || !allowedOn(w, r, cur, evidence.ActionLink) {
		return
	}
	e, err := s.evidenceStore.LinkEnvironment(r.Context(), ws, cur.ID, strings.TrimSpace(body.ProjectEnvironmentID))
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	s.decorateEnv(r.Context(), callerRole(r), e)
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) handlePutEvidenceSecrets(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	var in map[string]*string
	if !decodeBody(w, r, &in) {
		return
	}
	cur, ok := s.evidenceEnv(w, r, ws, r.PathValue("id"))
	if !ok || !allowedOn(w, r, cur, evidence.ActionSecrets) {
		return
	}
	e, err := s.evidenceStore.SetSecrets(r.Context(), ws, cur.ID, in)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	s.decorateEnv(r.Context(), callerRole(r), e)
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) queueEvidence(w http.ResponseWriter, r *http.Request, ws string, spec evidence.BatchSpec) {
	author, ok := requireCredentialAuthor(w, r)
	if !ok {
		return
	}
	env, ok := s.evidenceEnv(w, r, ws, spec.EnvironmentID)
	if !ok {
		return
	}
	// ReasonNoURL is let through: the store refuses it with 412.
	if p := env.Can[evidence.ActionRun]; !p.Allowed && p.Reason != evidence.ReasonNoURL {
		writeDenied(w, r, p.Reason)
		return
	}
	if spec.TriggerRef == "" {
		spec.TriggerRef = author
	}
	spec.Locale = i18n.RequestLocale(r)
	var b *evidence.Batch
	var err error
	switch {
	case s.evidenceSvc != nil && env.Kind == evidence.KindSandbox:
		b, err = s.evidenceSvc.QueueSandbox(r.Context(), ws, author, evidence.SandboxBatchSpec{ProjectID: env.ProjectID,
			Branch: spec.Branch, CaseIDs: spec.CaseIDs, Tag: spec.Tag, Trigger: spec.Trigger, TriggerRef: spec.TriggerRef,
			Locale: spec.Locale})
	case s.evidenceSvc != nil:
		b, err = s.evidenceSvc.Queue(r.Context(), ws, author, spec)
	default:
		b, err = s.evidenceStore.CreateBatch(r.Context(), ws, author, spec)
	}
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) handleQueueEvidenceRuns(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	var spec evidence.BatchSpec
	if !decodeBody(w, r, &spec) {
		return
	}
	spec.Trigger = evidence.TriggerManual
	s.queueEvidence(w, r, ws, spec)
}

func (s *Server) handleEvidenceEnvironmentRun(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	var spec evidence.BatchSpec
	if !decodeOptionalBody(w, r, &spec) {
		return
	}
	spec.EnvironmentID, spec.Trigger = r.PathValue("id"), evidence.TriggerAPI
	s.queueEvidence(w, r, ws, spec)
}

func (s *Server) handleListEvidenceRuns(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	runs, err := s.evidenceStore.ListRuns(r.Context(), evidence.RunFilter{
		WorkspaceID: ws, CaseID: q.Get("case_id"), EnvironmentID: q.Get("environment_id"), BatchID: q.Get("batch_id"),
		Statuses: splitCSV(q.Get("status")), Limit: queryLimit(r),
	})
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleGetEvidenceRun(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	run, err := s.evidenceStore.GetRun(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleListEvidenceBatches(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	bs, err := s.evidenceStore.ListBatches(r.Context(), ws, q.Get("environment_id"), q.Get("project_id"), queryLimit(r))
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batches": bs})
}

func (s *Server) handleGetEvidenceBatch(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	b, err := s.evidenceStore.GetBatch(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleCancelEvidenceBatch(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	var b *evidence.Batch
	var err error
	if s.evidenceSvc != nil {
		b, err = s.evidenceSvc.CancelBatch(r.Context(), ws, r.PathValue("id"))
	} else {
		b, err = s.evidenceStore.CancelBatch(r.Context(), ws, r.PathValue("id"))
	}
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleServeEvidenceArtifact(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	a, err := s.evidenceStore.GetArtifact(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	if a.MimeType != "" {
		w.Header().Set("Content-Type", a.MimeType)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; frame-ancestors 'self'") //i18n:ignore HTTP header value
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")                    //i18n:ignore HTTP header value
	http.ServeFile(w, r, a.Path)
}

// Runner and recording routes check their own credential (batch runner key, recording chat key);
// the role gate only caps them at developer.
func (s *Server) registerEvidenceRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /evidence/context", rr(auth.RoleViewer, s.handleEvidenceContext))
	mux.HandleFunc("GET /evidence/summary", rr(auth.RoleViewer, s.handleEvidenceSummary))
	mux.HandleFunc("GET /evidence/cases", rr(auth.RoleViewer, s.handleListEvidenceCases))
	mux.HandleFunc("POST /evidence/cases", rr(auth.RoleDeveloper, s.handleCreateEvidenceCase))
	mux.HandleFunc("POST /evidence/cases/from-task", rr(auth.RoleDeveloper, s.handleEvidenceCaseFromTask))
	mux.HandleFunc("GET /evidence/cases/{case}", rr(auth.RoleViewer, s.handleGetEvidenceCase))
	mux.HandleFunc("PATCH /evidence/cases/{case}", rr(auth.RoleDeveloper, s.handleUpdateEvidenceCase))
	mux.HandleFunc("DELETE /evidence/cases/{case}", rr(auth.RoleDeveloper, s.handleDeleteEvidenceCase))
	mux.HandleFunc("PUT /evidence/cases/{case}/take", rr(auth.RoleDeveloper, s.handlePutEvidenceTake))
	mux.HandleFunc("DELETE /evidence/cases/{case}/take", rr(auth.RoleDeveloper, s.handleDeleteEvidenceTake))
	mux.HandleFunc("POST /evidence/cases/{case}/record", rr(auth.RoleDeveloper, s.handleRecordEvidenceCase))
	mux.HandleFunc("GET /evidence/recording-agents", rr(auth.RoleDeveloper, s.handleEvidenceRecordingAgents))
	mux.HandleFunc("GET /evidence/environments", rr(auth.RoleViewer, s.handleListEvidenceEnvironments))
	mux.HandleFunc("POST /evidence/environments", rr(auth.RoleDeveloper, s.handleCreateEvidenceEnvironment))
	mux.HandleFunc("PATCH /evidence/environments/{id}", rr(auth.RoleDeveloper, s.handleUpdateEvidenceEnvironment))
	mux.HandleFunc("DELETE /evidence/environments/{id}", rr(auth.RoleAdmin, s.handleDeleteEvidenceEnvironment))
	mux.HandleFunc("PUT /evidence/environments/{id}/secrets", rr(auth.RoleDeveloper, s.handlePutEvidenceSecrets))
	mux.HandleFunc("POST /evidence/environments/{id}/link", rr(auth.RoleAdmin, s.handleLinkEvidenceEnvironment))
	mux.HandleFunc("POST /evidence/environments/{id}/run", rr(auth.RoleDeveloper, s.handleEvidenceEnvironmentRun))
	mux.HandleFunc("POST /evidence/runs", rr(auth.RoleDeveloper, s.handleQueueEvidenceRuns))
	mux.HandleFunc("GET /evidence/runs", rr(auth.RoleViewer, s.handleListEvidenceRuns))
	mux.HandleFunc("GET /evidence/runs/{id}", rr(auth.RoleViewer, s.handleGetEvidenceRun))
	mux.HandleFunc("GET /evidence/batches", rr(auth.RoleViewer, s.handleListEvidenceBatches))
	mux.HandleFunc("GET /evidence/batches/{id}", rr(auth.RoleViewer, s.handleGetEvidenceBatch))
	mux.HandleFunc("POST /evidence/batches/{id}/cancel", rr(auth.RoleDeveloper, s.handleCancelEvidenceBatch))
	mux.HandleFunc("GET /evidence/artifacts/{id}", rr(auth.RoleViewer, s.handleServeEvidenceArtifact))

	mux.HandleFunc("POST /evidence/jobs/{batch}/next", rr(auth.RoleDeveloper, s.handleEvidenceJobNext))
	mux.HandleFunc("POST /evidence/jobs/{batch}/runs/{run}/artifacts", rr(auth.RoleDeveloper, s.handleEvidenceJobArtifact))
	mux.HandleFunc("POST /evidence/jobs/{batch}/runs/{run}/result", rr(auth.RoleDeveloper, s.handleEvidenceJobResult))
	mux.HandleFunc("POST /evidence/jobs/{batch}/unreachable", rr(auth.RoleDeveloper, s.handleEvidenceJobUnreachable))

	mux.HandleFunc("GET /evidence/recording", rr(auth.RoleDeveloper, s.handleEvidenceRecording))
	mux.HandleFunc("GET /evidence/recording/secrets/{name}", rr(auth.RoleDeveloper, s.handleEvidenceRecordingSecret))
	mux.HandleFunc("PUT /evidence/recording/take", rr(auth.RoleDeveloper, s.handleEvidenceRecordingTake))
}

func (s *Server) StartEvidence(ctx context.Context) {
	if s.evidenceSvc != nil {
		s.evidenceSvc.Start(ctx)
	}
}
