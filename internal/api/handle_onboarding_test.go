package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestOnboarding_ReportsLiveState(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	req := httptest.NewRequest(http.MethodGet, "/onboarding", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /onboarding = %d: %s", w.Code, w.Body.String())
	}

	var status struct {
		Complete bool `json:"complete"`
		DevMode  bool `json:"dev_mode"`
		Steps    []struct {
			ID     string `json:"id"`
			Done   bool   `json:"done"`
			Effect string `json:"effect"`
			Finish string `json:"finish"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v — %s", err, w.Body.String())
	}

	want := map[string]bool{
		"identity": false, "workspace": false, "agents": false,
		"git_credential": false, "llm_key": false, "project": false,
		"column_agent": false, "stage_template": false,
	}
	for _, step := range status.Steps {
		if _, known := want[step.ID]; !known {
			t.Errorf("unexpected step %q", step.ID)
			continue
		}
		want[step.ID] = true
		if !step.Done && (step.Effect == "" || step.Finish == "") {
			t.Errorf("step %q is undone but says neither what it costs nor how to finish it", step.ID)
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("step %q missing from the checklist", id)
		}
	}

	if status.Complete {
		t.Error("complete = true on an install with no identity provider and no credentials")
	}
	if !status.DevMode {
		t.Error("dev_mode = false while the integration server runs with auth disabled")
	}
	if strings.Contains(w.Body.String(), "OCTOPUS_JWT_SECRET") {
		t.Error("the checklist echoed a secret env var")
	}
}

func TestOnboarding_CountsAGithubAppInstallation(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)

	if got := readOnboardingStep(t, h, "git_credential"); got.Done {
		t.Fatalf("git_credential done on a fresh install: %+v", got)
	}

	if err := srv.gh.store.SetConfig(t.Context(), githubapp.AppConfig{
		AppID: 4242, Slug: "octopus-test", PrivateKey: "-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----",
	}); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	srv.gh.app.InvalidateConfig()
	step := readOnboardingStep(t, h, "git_credential")
	if step.Done || !strings.Contains(step.Detail, "not installed") {
		t.Errorf("app configured but not installed: %+v, want undone and a detail that says so", step)
	}

	if err := srv.gh.store.UpsertInstallation(t.Context(), githubapp.Installation{
		InstallationID: 99, WorkspaceID: defaultWorkspaceID, AccountLogin: "acme",
	}); err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	step = readOnboardingStep(t, h, "git_credential")
	if !step.Done || !strings.Contains(step.Detail, "acme") {
		t.Errorf("installed app: %+v, want done and the account named", step)
	}

	if err := srv.gh.store.SetSuspended(t.Context(), 99, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if step = readOnboardingStep(t, h, "git_credential"); step.Done {
		t.Errorf("suspended installation still counts as a credential: %+v", step)
	}
}

func TestOnboarding_ColumnAgentAndTemplate(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	if step := readOnboardingStep(t, h, "column_agent"); step.Done {
		t.Fatalf("column_agent done on a fresh install: %+v", step)
	}
	if step := readOnboardingStep(t, h, "stage_template"); step.Done {
		t.Fatalf("stage_template done with no projects: %+v", step)
	}

	w := postJSON(t, h, "/projects?workspace="+defaultWorkspaceID, map[string]any{
		"name": "With", "repo_url": testgit.BareRepo(t),
		"stage_template": map[string]string{"in_progress": "sample-developer"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	w = postJSON(t, h, "/projects?workspace="+defaultWorkspaceID, map[string]any{"name": "Without", "repo_url": testgit.BareRepo(t)})
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	var without struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &without)

	col := readOnboardingStep(t, h, "column_agent")
	if !col.Done || !strings.Contains(col.Detail, "sample-developer") {
		t.Errorf("column_agent = %+v, want done and the agent named", col)
	}
	tpl := readOnboardingStep(t, h, "stage_template")
	if tpl.Done || !strings.Contains(tpl.Href, without.ID) {
		t.Errorf("stage_template = %+v, want undone and a link to %s", tpl, without.ID)
	}
}

func readOnboardingStep(t *testing.T, h http.Handler, id string) onboardingStep {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/onboarding", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /onboarding = %d: %s", w.Code, w.Body.String())
	}
	var status onboardingStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v — %s", err, w.Body.String())
	}
	for _, step := range status.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("step %q missing from the checklist", id)
	return onboardingStep{}
}

func readOnboarding(t *testing.T, h http.Handler) onboardingStatus {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/onboarding", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var status onboardingStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v — %s", err, w.Body.String())
	}
	return status
}

func TestOnboarding_OrderAndCommands(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	status := readOnboarding(t, h)
	var ids []string
	for _, s := range status.Steps {
		ids = append(ids, s.ID)
		if strings.Contains(s.Finish, "`") {
			t.Errorf("step %q: backticks in the sentence: %q", s.ID, s.Finish)
		}
	}
	if got := strings.Join(ids, ","); !strings.HasSuffix(got, "llm_key,git_credential,project,agents,column_agent,stage_template") {
		t.Errorf("order = %s", got)
	}
	if got := readOnboardingStep(t, h, "agents"); got.Command != "thulu agents import <dir|file.json>" {
		t.Errorf("agents command = %q", got.Command)
	}
	if status.SaaS {
		t.Error("saas = true outside OCTOPUS_SAAS_MODE")
	}
	for _, s := range status.Steps {
		wantInstall := s.ID == "identity" || s.ID == "workspace"
		if s.Install != wantInstall {
			t.Errorf("step %q: install = %v, want %v", s.ID, s.Install, wantInstall)
		}
	}
}

func TestOnboarding_SaaSOmitsCommands(t *testing.T) {
	t.Setenv("OCTOPUS_SAAS_MODE", "1")
	_, h := newIntegrationServer(t)
	status := readOnboarding(t, h)
	if !status.SaaS {
		t.Error("saas = false with OCTOPUS_SAAS_MODE=1")
	}
	for _, s := range status.Steps {
		if s.Command != "" {
			t.Errorf("step %q shows command %q on a hosted install", s.ID, s.Command)
		}
	}
}
