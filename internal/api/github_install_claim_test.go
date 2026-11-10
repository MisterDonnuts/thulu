package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/githubapp"
)

type fakeGitHub struct {
	sees map[string][]int64
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const api = "/api/v3"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/login/oauth/access_token":
		_ = r.ParseForm()
		login, ok := strings.CutPrefix(r.PostForm.Get("code"), "code-")
		if _, known := f.sees[login]; !ok || !known {
			_, _ = fmt.Fprint(w, `{"error":"bad_verification_code"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"tok-%s"}`, login)
	case strings.HasPrefix(r.URL.Path, api+"/app/installations/"):
		id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, api+"/app/installations/"), 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":%d,"account":{"login":"org-%d","type":"Organization"},"repository_selection":"all"}`, id, id)
	case r.URL.Path == api+"/user" || r.URL.Path == api+"/user/installations":
		login, ok := strings.CutPrefix(r.Header.Get("Authorization"), "token tok-")
		ids, known := f.sees[login]
		if !ok || !known {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == api+"/user" {
			_, _ = fmt.Fprintf(w, `{"login":%q}`, login)
			return
		}
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf(`{"id":%d}`, id))
		}
		_, _ = fmt.Fprintf(w, `{"total_count":%d,"installations":[%s]}`, len(ids), strings.Join(parts, ","))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newClaimServer(t *testing.T, gh *fakeGitHub) (*Server, http.Handler) {
	t.Helper()
	configureTestGithubApp(t)
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_URL", fake.URL)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL+"/api/v3")
	return newIntegrationServer(t)
}

func installLink(t *testing.T, h http.Handler, token string) (string, *http.Cookie) {
	t.Helper()
	w := getJSON(t, h, "/github/app/install-link", token)
	if w.Code != http.StatusOK {
		t.Fatalf("install-link = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		t.Fatal(err)
	}
	var binder *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == githubInstallBinder {
			binder = c
		}
	}
	if binder == nil || !binder.HttpOnly || binder.SameSite != http.SameSiteLaxMode || binder.Path != githubInstallBinderPath {
		t.Fatalf("binder cookie = %+v", binder)
	}
	return u.Query().Get("state"), binder
}

func githubReturn(t *testing.T, h http.Handler, path string, binder *http.Cookie) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if binder != nil {
		r.AddCookie(&http.Cookie{Name: binder.Name, Value: binder.Value})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("GET %s = %d: %s", path, w.Code, w.Body.String())
	}
	return w.Header().Get("Location")
}

func setupPath(id int64, state string) string {
	return fmt.Sprintf("/github/setup?installation_id=%d&setup_action=install&state=%s", id, url.QueryEscape(state))
}

func callbackPath(id int64, state, code string) string {
	return fmt.Sprintf("/github/oauth/callback?installation_id=%d&setup_action=install&state=%s&code=%s",
		id, url.QueryEscape(state), url.QueryEscape(code))
}

func installationWorkspace(t *testing.T, srv *Server, id int64) string {
	t.Helper()
	inst, err := srv.gh.store.GetInstallation(t.Context(), id)
	if err != nil {
		return "<none>"
	}
	return inst.WorkspaceID
}

func TestGithubInstall_AnotherWorkspacesInstallationIsRefused(t *testing.T) {
	srv, h := newClaimServer(t, &fakeGitHub{sees: map[string][]int64{"alice": {500}, "bob": {500}}})
	tokenA, _ := mintWorkspaceUser(t, srv, "claim-a@test.com", "ClaimA")
	tokenB, wsB := mintWorkspaceUser(t, srv, "claim-b@test.com", "ClaimB")

	stateB, binderB := installLink(t, h, tokenB)
	if got := githubReturn(t, h, callbackPath(500, stateB, "code-bob"), binderB); !strings.Contains(got, "github=connected") {
		t.Fatalf("B's own claim: %s", got)
	}
	if wid := installationWorkspace(t, srv, 500); wid != wsB {
		t.Fatalf("after B's claim wid = %q, want %q", wid, wsB)
	}

	for _, attack := range []func(state string) string{
		func(state string) string { return setupPath(500, state) },
		func(state string) string { return callbackPath(500, state, "code-alice") },
	} {
		stateA, binderA := installLink(t, h, tokenA)
		if got := githubReturn(t, h, attack(stateA), binderA); !strings.Contains(got, "reason=claimed") {
			t.Errorf("A claiming B's installation: %s, want reason=claimed", got)
		}
		if wid := installationWorkspace(t, srv, 500); wid != wsB {
			t.Fatalf("A took B's installation: wid = %q", wid)
		}
	}
	w := postJSONAuth(t, h, "/projects", tokenA, map[string]any{
		"name": "stolen", "repo_url": "https://github.com/org-500/app", "github_installation_id": 500,
	})
	if w.Code == http.StatusCreated || w.Code == http.StatusOK {
		t.Errorf("A linked a project to B's installation: %d %s", w.Code, w.Body.String())
	}
}

func TestGithubInstall_StateIsSingleUseAndBrowserBound(t *testing.T) {
	srv, h := newClaimServer(t, &fakeGitHub{sees: map[string][]int64{"alice": {600, 601}}})
	tokenA, wsA := mintWorkspaceUser(t, srv, "replay-a@test.com", "ReplayA")

	state, binder := installLink(t, h, tokenA)
	if got := githubReturn(t, h, callbackPath(600, state, "code-alice"), nil); !strings.Contains(got, "reason=state") {
		t.Errorf("no binder: %s, want reason=state", got)
	}
	if got := githubReturn(t, h, callbackPath(600, state, "code-alice"),
		&http.Cookie{Name: githubInstallBinder, Value: "forged"}); !strings.Contains(got, "reason=state") {
		t.Errorf("wrong binder: %s, want reason=state", got)
	}
	if got := githubReturn(t, h, callbackPath(600, state, "code-alice"), binder); !strings.Contains(got, "github=connected") {
		t.Fatalf("real browser: %s", got)
	}
	if wid := installationWorkspace(t, srv, 600); wid != wsA {
		t.Fatalf("wid = %q, want %q", wid, wsA)
	}
	if got := githubReturn(t, h, callbackPath(601, state, "code-alice"), binder); !strings.Contains(got, "reason=state") {
		t.Errorf("replayed state: %s, want reason=state", got)
	}
	if wid := installationWorkspace(t, srv, 601); wid != "<none>" {
		t.Errorf("the replay recorded installation 601 (wid %q)", wid)
	}
	if got := githubReturn(t, h, setupPath(601, "v1:made-up"), binder); !strings.Contains(got, "reason=state") {
		t.Errorf("forged state: %s", got)
	}
}

func TestGithubInstall_SaaSNeedsInstallerProof(t *testing.T) {
	t.Setenv("OCTOPUS_SAAS_MODE", "1")
	srv, h := newClaimServer(t, &fakeGitHub{sees: map[string][]int64{"alice": {700}, "mallory": {}}})
	tokenA, wsA := mintWorkspaceUser(t, srv, "saas-a@test.com", "SaasA")

	if err := srv.gh.store.UpsertInstallation(t.Context(), githubapp.Installation{InstallationID: 700, AccountLogin: "org-700"}); err != nil {
		t.Fatal(err)
	}
	state, binder := installLink(t, h, tokenA)
	if got := githubReturn(t, h, setupPath(700, state), binder); !strings.Contains(got, "reason=authorize") {
		t.Errorf("no code in SaaS mode: %s, want reason=authorize", got)
	}
	state, binder = installLink(t, h, tokenA)
	if got := githubReturn(t, h, callbackPath(700, state, "code-mallory"), binder); !strings.Contains(got, "reason=installer") {
		t.Errorf("code of a user who cannot see it: %s, want reason=installer", got)
	}
	state, binder = installLink(t, h, tokenA)
	if got := githubReturn(t, h, callbackPath(700, state, "code-nobody"), binder); !strings.Contains(got, "reason=oauth") {
		t.Errorf("a code GitHub refuses: %s, want reason=oauth", got)
	}
	if wid := installationWorkspace(t, srv, 700); wid != "" {
		t.Fatalf("refused claims left wid = %q", wid)
	}
	state, binder = installLink(t, h, tokenA)
	if got := githubReturn(t, h, callbackPath(700, state, "code-alice"), binder); !strings.Contains(got, "github=connected") {
		t.Fatalf("proved claim: %s", got)
	}
	inst, err := srv.gh.store.GetInstallation(t.Context(), 700)
	if err != nil || inst.WorkspaceID != wsA || inst.InstalledBy != "alice" {
		t.Fatalf("after the proved claim: %+v %v", inst, err)
	}
	state, binder = installLink(t, h, tokenA)
	if got := githubReturn(t, h, setupPath(700, state), binder); !strings.Contains(got, "github=connected") {
		t.Errorf("refresh of an owned installation: %s", got)
	}
}

func TestGithubInstall_SelfHostedClaimsWithoutCode(t *testing.T) {
	t.Setenv("OCTOPUS_SAAS_MODE", "")
	srv, h := newClaimServer(t, &fakeGitHub{sees: map[string][]int64{"mallory": {}}})
	tokenA, wsA := mintWorkspaceUser(t, srv, "self-a@test.com", "SelfA")
	state, binder := installLink(t, h, tokenA)
	if got := githubReturn(t, h, setupPath(800, state), binder); !strings.Contains(got, "github=connected&installation_id=800") {
		t.Fatalf("self-hosted claim: %s", got)
	}
	if wid := installationWorkspace(t, srv, 800); wid != wsA {
		t.Errorf("wid = %q, want %q", wid, wsA)
	}

	state, binder = installLink(t, h, tokenA)
	if got := githubReturn(t, h, callbackPath(801, state, "code-nobody"), binder); !strings.Contains(got, "github=connected&installation_id=801") {
		t.Errorf("self-hosted claim with a code GitHub refuses: %s", got)
	}
	if wid := installationWorkspace(t, srv, 801); wid != wsA {
		t.Errorf("801 wid = %q, want %q", wid, wsA)
	}

	state, binder = installLink(t, h, tokenA)
	if got := githubReturn(t, h, callbackPath(802, state, "code-mallory"), binder); !strings.Contains(got, "reason=installer") {
		t.Errorf("self-hosted claim by a user who cannot see it: %s, want reason=installer", got)
	}
	if wid := installationWorkspace(t, srv, 802); wid != "<none>" {
		t.Errorf("refused claim left a row for 802 (wid %q)", wid)
	}
}

func TestGithubInstall_NoStateNeverClaims(t *testing.T) {
	srv, h := newClaimServer(t, &fakeGitHub{sees: map[string][]int64{}})
	if got := githubReturn(t, h, "/github/setup?installation_id=900&setup_action=update", nil); !strings.Contains(got, "reason=unclaimed") {
		t.Errorf("no state, unclaimed: %s, want reason=unclaimed", got)
	}
	inst, err := srv.gh.store.GetInstallation(t.Context(), 900)
	if err != nil || inst.WorkspaceID != "" || inst.AccountLogin != "org-900" {
		t.Fatalf("mirror after a stateless redirect: %+v %v", inst, err)
	}
	if err := srv.gh.store.ClaimInstallation(t.Context(), githubapp.Installation{InstallationID: 900, WorkspaceID: "ws-x"}); err != nil {
		t.Fatal(err)
	}
	if got := githubReturn(t, h, "/github/setup?installation_id=900&setup_action=update", nil); !strings.Contains(got, "github=connected") {
		t.Errorf("no state, claimed: %s", got)
	}
	if wid := installationWorkspace(t, srv, 900); wid != "ws-x" {
		t.Errorf("a stateless redirect moved the claim to %q", wid)
	}
}

func TestGithubInstall_ReassignIsOperatorOnly(t *testing.T) {
	srv, h := newClaimServer(t, &fakeGitHub{sees: map[string][]int64{}})
	tokenA, wsA := mintWorkspaceUser(t, srv, "move-a@test.com", "MoveA")
	if err := srv.gh.store.ClaimInstallation(t.Context(), githubapp.Installation{InstallationID: 950, WorkspaceID: "ws-b"}); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"workspace_id": wsA}
	if w := putJSON(t, h, "/github/installations/950/workspace", body, tokenA); w.Code != http.StatusForbidden {
		t.Errorf("tenant admin reassign = %d, want 403: %s", w.Code, w.Body.String())
	}
	if wid := installationWorkspace(t, srv, 950); wid != "ws-b" {
		t.Fatalf("refused reassign moved it to %q", wid)
	}
	if w := putJSON(t, h, "/github/installations/950/workspace", map[string]any{"workspace_id": "ws-typo"}, ""); w.Code != http.StatusNotFound {
		t.Errorf("reassign to an unknown workspace = %d, want 404: %s", w.Code, w.Body.String())
	}
	if wid := installationWorkspace(t, srv, 950); wid != "ws-b" {
		t.Fatalf("reassign to an unknown workspace moved it to %q", wid)
	}
	w := putJSON(t, h, "/github/installations/950/workspace", body, "")
	if w.Code != http.StatusOK {
		t.Fatalf("operator reassign = %d: %s", w.Code, w.Body.String())
	}
	if wid := installationWorkspace(t, srv, 950); wid != wsA {
		t.Errorf("after reassign wid = %q, want %q", wid, wsA)
	}
	if w := putJSON(t, h, "/github/installations/404/workspace", body, ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown installation = %d, want 404", w.Code)
	}
}

func TestGithubInstall_LinkExistingProjectChecksTheRepository(t *testing.T) {
	srv, h := newClaimServer(t, &fakeGitHub{sees: map[string][]int64{"carol": {700}}})
	token, ws := mintWorkspaceUser(t, srv, "link-a@test.com", "LinkA")
	state, binder := installLink(t, h, token)
	if got := githubReturn(t, h, callbackPath(700, state, "code-carol"), binder); !strings.Contains(got, "github=connected") {
		t.Fatalf("claim: %s", got)
	}
	if wid := installationWorkspace(t, srv, 700); wid != ws {
		t.Fatalf("installation wid = %q, want %q", wid, ws)
	}
	pid := createProject(t, h, token, "Linkable")

	w := patchJSONAuth(t, h, "/projects/"+pid, token, map[string]any{"github_installation_id": 700})
	if w.Code != http.StatusBadRequest {
		t.Errorf("link a non-GitHub project = %d %s, want 400", w.Code, w.Body.String())
	}
	got, err := srv.projectStore.Get(t.Context(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if got.GithubInstallationID != 0 {
		t.Errorf("a refused link was saved: installation %d", got.GithubInstallationID)
	}
}
