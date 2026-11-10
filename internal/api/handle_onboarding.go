package api

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
)

type onboardingStep struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Done    bool   `json:"done"`
	Detail  string `json:"detail,omitempty"`
	Effect  string `json:"effect,omitempty"`
	Finish  string `json:"finish,omitempty"`
	Href    string `json:"href,omitempty"`
	Command string `json:"command,omitempty"`
	Install bool   `json:"install,omitempty"`

	readErr error
}

type onboardingStatus struct {
	Complete bool             `json:"complete"`
	DevMode  bool             `json:"dev_mode"`
	SaaS     bool             `json:"saas"`
	Steps    []onboardingStep `json:"steps"`
}

func (s *Server) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	operator := s.callerIsOperator(r)
	var steps []onboardingStep
	// Install steps are operator-only: their details expose the IdP issuer and the workspace count.
	if operator {
		steps = append(steps, s.stepIdentity(r.Context()), s.stepWorkspace(r))
	}
	steps = append(steps,
		s.stepLLMKey(r.Context(), wsID),
		s.stepGitCredential(r, wsID),
		s.stepProject(r.Context(), wsID),
		s.stepAgents(r.Context(), wsID),
		s.stepColumnAgent(r, wsID),
		s.stepStageTemplate(r.Context(), wsID),
	)
	saas := auth.SaaSMode()
	status := onboardingStatus{Complete: true, DevMode: auth.DevMode(), SaaS: saas, Steps: steps}
	for i := range steps {
		if saas {
			steps[i].Command = ""
		}
		if !steps[i].Done {
			status.Complete = false
		}
		// Store errors can name host paths or DB addresses; redact them outside the operator
		// workspace.
		if !operator && steps[i].readErr != nil {
			log.Printf("[onboarding] %s", i18n.Log("api.onboarding.log.step_failed",
				"step", steps[i].ID, "err", steps[i].readErr))
			steps[i].Detail = i18n.TCtx(r.Context(), "api.onboarding.could_not_read_redacted")
		}
	}
	writeJSON(w, http.StatusOK, status)
}

func (step *onboardingStep) readFailed(ctx context.Context, err error) {
	step.readErr = err
	step.Detail = i18n.TCtx(ctx, "api.onboarding.could_not_read", "err", apperr.LocalizeCtx(ctx, err))
}

func (s *Server) stepIdentity(ctx context.Context) onboardingStep {
	step := onboardingStep{
		ID:      "identity",
		Install: true,
		Title:   i18n.TCtx(ctx, "api.onboarding.steps.identity.title"),
		Effect:  i18n.TCtx(ctx, "api.onboarding.steps.identity.effect"),
		Finish:  i18n.TCtx(ctx, "api.onboarding.steps.identity.finish"),
		//i18n:ignore shell command
		Command: "thulu init",
	}
	switch {
	case strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_ISSUER")) != "":
		step.Done = true
		step.Detail = "OIDC — " + os.Getenv("OCTOPUS_OIDC_ISSUER")
	case strings.TrimSpace(os.Getenv("OCTOPUS_GITHUB_CLIENT_ID")) != "":
		step.Done = true
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.identity.github_oauth")
	case auth.DevMode():
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.identity.dev_mode")
	default:
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.identity.none")
	}
	return step
}

func (s *Server) stepWorkspace(r *http.Request) onboardingStep {
	ctx := r.Context()
	step := onboardingStep{
		ID:      "workspace",
		Install: true,
		Title:   i18n.TCtx(ctx, "api.onboarding.steps.workspace.title"),
		Effect:  i18n.TCtx(ctx, "api.onboarding.steps.workspace.effect"),
		Finish:  i18n.TCtx(ctx, "api.onboarding.steps.workspace.finish"),
		//i18n:ignore shell command
		Command: "thulu workspace create -name … -slug … -owner-email …",
	}
	if s.authStore == nil {
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.workspace.store_unavailable")
		return step
	}
	var count int
	row := s.authStore.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces`)
	if err := row.Scan(&count); err != nil {
		step.readFailed(ctx, err)
		return step
	}
	step.Done = count > 0
	step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.workspace.count", "count", count)
	return step
}

func (s *Server) stepAgents(ctx context.Context, wsID string) onboardingStep {
	step := onboardingStep{
		ID:     "agents",
		Title:  i18n.TCtx(ctx, "api.onboarding.steps.agents.title"),
		Effect: i18n.TCtx(ctx, "api.onboarding.steps.agents.effect"),
		Finish: i18n.TCtx(ctx, "api.onboarding.steps.agents.finish"),
		Href:   "/agents/",
		//i18n:ignore shell command
		Command: "thulu agents import <dir|file.json>",
	}
	agents, err := s.agents(wsID).All()
	if err != nil {
		step.readFailed(ctx, err)
		return step
	}
	step.Done = len(agents) > 0
	step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.agents.count", "count", len(agents))
	return step
}

// Must check both legs githubapp.Resolver uses at dispatch: an App installation stores no
// credential.
func (s *Server) stepGitCredential(r *http.Request, wsID string) onboardingStep {
	ctx := r.Context()
	step := onboardingStep{
		ID:     "git_credential",
		Title:  i18n.TCtx(ctx, "api.onboarding.steps.git_credential.title"),
		Effect: i18n.TCtx(ctx, "api.onboarding.steps.git_credential.effect"),
		Finish: i18n.TCtx(ctx, "api.onboarding.steps.git_credential.finish"),
		Href:   "/credentials/",
	}
	if inst, ok := s.githubAppInstallation(ctx, wsID); ok {
		step.Done = true
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.git_credential.github_app", "accounts", inst)
		return step
	}
	if s.credStore == nil {
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.git_credential.store_disabled")
		return step
	}
	creds, err := s.credStore.List(ctx, wsID)
	if err != nil {
		step.readFailed(ctx, err)
		return step
	}
	for _, c := range creds {
		if c.Provider == "github" && !c.Disabled {
			step.Done = true
			step.Detail = c.Name + " — " + string(c.State)
			return step
		}
	}
	if s.gh.app != nil && s.gh.app.Configured(ctx) {
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.git_credential.app_not_installed")
		step.Finish = i18n.TCtx(ctx, "api.onboarding.steps.git_credential.install_app")
		return step
	}
	step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.git_credential.none")
	return step
}

// Must match Resolver.authorizeInstallation: App configured, not suspended, claimed by this
// workspace or by none.
func (s *Server) githubAppInstallation(ctx context.Context, wsID string) (string, bool) {
	if s.gh.app == nil || s.gh.store == nil || !s.gh.app.Configured(ctx) {
		return "", false
	}
	// Filter by hand: unstamped installations have workspace_id "" and the resolver still mints with
	// them; other workspaces' rows are dropped here.
	installs, err := s.gh.store.ListInstallations(ctx, wsID, true)
	if err != nil {
		return "", false
	}
	var accounts []string
	for i := range installs {
		inst := &installs[i]
		if inst.SuspendedAt != nil || (inst.WorkspaceID != "" && inst.WorkspaceID != wsID) {
			continue
		}
		accounts = append(accounts, inst.AccountLogin)
	}
	if len(accounts) == 0 {
		return "", false
	}
	return strings.Join(accounts, ", "), true
}

func (s *Server) stepLLMKey(ctx context.Context, wsID string) onboardingStep {
	step := onboardingStep{
		ID:     "llm_key",
		Title:  i18n.TCtx(ctx, "api.onboarding.steps.llm_key.title"),
		Effect: i18n.TCtx(ctx, "api.onboarding.steps.llm_key.effect"),
		Finish: i18n.TCtx(ctx, "api.onboarding.steps.llm_key.finish"),
		Href:   "/settings/#llm",
	}
	if s.secretsStore == nil {
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.llm_key.store_unavailable")
		return step
	}
	status, err := s.secretsStore.StatusLLMKey(ctx, wsID, "")
	if err != nil {
		step.readFailed(ctx, err)
		return step
	}
	step.Done = status.HasKey
	if status.HasKey {
		step.Detail = status.Engine + " — " + status.Hint
	} else {
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.llm_key.no_key", "engine", status.Engine)
	}
	return step
}

func (s *Server) stepProject(ctx context.Context, wsID string) onboardingStep {
	step := onboardingStep{
		ID:     "project",
		Title:  i18n.TCtx(ctx, "api.onboarding.steps.project.title"),
		Effect: i18n.TCtx(ctx, "api.onboarding.steps.project.effect"),
		Finish: i18n.TCtx(ctx, "api.onboarding.steps.project.finish"),
		Href:   "/projects/",
	}
	if s.projectStore == nil {
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.project.store_unavailable")
		return step
	}
	projects, err := s.projectStore.ListByWorkspace(ctx, wsID)
	if err != nil {
		step.readFailed(ctx, err)
		return step
	}
	step.Done = len(projects) > 0
	step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.project.count", "count", len(projects))
	return step
}

func (s *Server) stepColumnAgent(r *http.Request, wsID string) onboardingStep {
	ctx := r.Context()
	step := onboardingStep{
		ID:     "column_agent",
		Title:  i18n.TCtx(ctx, "api.onboarding.steps.column_agent.title"),
		Effect: i18n.TCtx(ctx, "api.onboarding.steps.column_agent.effect"),
		Finish: i18n.TCtx(ctx, "api.onboarding.steps.column_agent.finish"),
		Href:   "/settings/#boards",
	}
	if s.projectStore == nil || s.boards == nil {
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.project.store_unavailable")
		return step
	}
	board := s.callerDefaultBoard(r)
	if board != "" {
		step.Href = "/settings/?board=" + url.QueryEscape(board) + "#boards"
	}
	projects, err := s.projectStore.ListByWorkspace(ctx, wsID)
	if err != nil {
		step.readFailed(ctx, err)
		return step
	}
	cols := stagepkg.LocalizeSeeded(s.boards.Board(board).List(), i18n.FromContext(ctx))
	for i := range cols {
		if cols[i].Initial || cols[i].Terminal {
			continue
		}
		for j := range projects {
			if agent := projects[j].StageTemplate[string(cols[i].ID)]; agent != "" {
				step.Done = true
				step.Detail = cols[i].Label + " — " + agent
				return step
			}
		}
	}
	step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.column_agent.none")
	return step
}

func (s *Server) stepStageTemplate(ctx context.Context, wsID string) onboardingStep {
	step := onboardingStep{
		ID:     "stage_template",
		Title:  i18n.TCtx(ctx, "api.onboarding.steps.stage_template.title"),
		Effect: i18n.TCtx(ctx, "api.onboarding.steps.stage_template.effect"),
		Finish: i18n.TCtx(ctx, "api.onboarding.steps.stage_template.finish"),
		Href:   "/projects/",
	}
	if s.projectStore == nil {
		step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.project.store_unavailable")
		return step
	}
	projects, err := s.projectStore.ListByWorkspace(ctx, wsID)
	if err != nil {
		step.readFailed(ctx, err)
		return step
	}
	with, missing := 0, ""
	for i := range projects {
		if templateHasAgent(projects[i].StageTemplate) {
			with++
		} else if missing == "" {
			missing = projects[i].ID
		}
	}
	step.Done = len(projects) > 0 && missing == ""
	if missing != "" {
		step.Href = "/project/?id=" + url.QueryEscape(missing) + "#settings"
	}
	step.Detail = i18n.TCtx(ctx, "api.onboarding.steps.stage_template.count", "with", with, "count", len(projects))
	return step
}

func templateHasAgent(tpl map[string]string) bool {
	for _, agent := range tpl {
		if agent != "" {
			return true
		}
	}
	return false
}
