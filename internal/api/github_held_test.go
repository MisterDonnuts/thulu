package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
)

type githubRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (g *githubRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.calls = append(g.calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/merge"):
		fmt.Fprint(w, `{"merged":true,"sha":"abc123","message":"merged"}`)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pending_deployments"):
		fmt.Fprint(w, `[{"environment":{"id":5,"name":"production"},"current_user_can_approve":true},`+
			`{"environment":{"id":6,"name":"locked"},"current_user_can_approve":false}]`)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pending_deployments"):
		fmt.Fprint(w, `[{"id":1234}]`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}
}

func (g *githubRecorder) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

func newHeldGithubRig(t *testing.T, chatStage ...string) (*delegatedGithubRig, *githubRecorder) {
	t.Helper()
	gh := &githubRecorder{}
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	return newDelegatedGithubRig(t, chatStage...), gh
}

func (rig *delegatedGithubRig) held(t *testing.T, path string, body any) string {
	t.Helper()
	w := keyDo(t, rig.h, http.MethodPost, path, rig.key, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("delegated POST %s = %d, want 202: %s", path, w.Code, w.Body.String())
	}
	var held struct {
		Status   string `json:"status"`
		ActionID string `json:"action_id"`
	}
	json.NewDecoder(w.Body).Decode(&held)
	if held.Status != "awaiting_confirmation" || held.ActionID == "" {
		t.Fatalf("held answer = %+v", held)
	}
	return held.ActionID
}

func (rig *delegatedGithubRig) confirm(t *testing.T, actionID string) chat.Action {
	t.Helper()
	w := tokenDo(t, rig.h, http.MethodPost, fmt.Sprintf("/chats/%s/actions/%s/confirm", rig.chatID, actionID), rig.token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", w.Code, w.Body.String())
	}
	var a chat.Action
	json.NewDecoder(w.Body).Decode(&a)
	return a
}

func (rig *delegatedGithubRig) pendingActions(t *testing.T) []chat.Action {
	t.Helper()
	w := tokenDo(t, rig.h, http.MethodGet, "/chats/"+rig.chatID+"/actions?status=pending", rig.token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list actions = %d: %s", w.Code, w.Body.String())
	}
	var acts []chat.Action
	json.NewDecoder(w.Body).Decode(&acts)
	return acts
}

func TestDelegatedChatGithub_MergeWaitsForThePerson(t *testing.T) {
	rig, gh := newHeldGithubRig(t, "thulu-github@read+pr_merge")
	path := "/internal/github/acme/widgets/pulls/7/merge"

	id := rig.held(t, path, map[string]string{"merge_method": "squash"})
	if calls := gh.seen(); len(calls) != 0 {
		t.Fatalf("GitHub was called before anyone confirmed: %v", calls)
	}
	acts := rig.pendingActions(t)
	if len(acts) != 1 || !strings.Contains(acts[0].Summary, "#7") || !strings.Contains(acts[0].Summary, "acme/widgets") {
		t.Fatalf("pending actions = %+v, want one naming #7 in acme/widgets", acts)
	}

	for name, call := range map[string]func() *httptest.ResponseRecorder{
		"the person, directly": func() *httptest.ResponseRecorder {
			return tokenDo(t, rig.h, http.MethodPost, path, rig.token, map[string]string{})
		},
		"a task run": func() *httptest.ResponseRecorder {
			return keyDo(t, rig.h, http.MethodPost, path, rig.keyA, map[string]string{})
		},
		"a chat that acts for nobody": func() *httptest.ResponseRecorder {
			return keyDo(t, rig.h, http.MethodPost, path, rig.plainKey, map[string]string{})
		},
	} {
		if w := call(); w.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403: %s", name, w.Code, w.Body.String())
		}
	}
	if calls := gh.seen(); len(calls) != 0 {
		t.Fatalf("GitHub was called without a confirmation: %v", calls)
	}

	a := rig.confirm(t, id)
	if a.Status != chat.ActionConfirmed || a.ResultCode != http.StatusOK {
		t.Fatalf("confirmed merge = %+v", a)
	}
	calls := gh.seen()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "PUT /repos/acme/widgets/pulls/7/merge ") ||
		!strings.Contains(calls[0], `"merge_method":"squash"`) {
		t.Errorf("GitHub saw %v, want one squash merge of #7", calls)
	}
	if got := rig.tokens.last(); !samePerms(got, githubapp.PermsMerge) {
		t.Errorf("merge token minted with %v, want %v", got, githubapp.PermsMerge)
	}
}

func TestDelegatedChatGithub_DeploymentReviewWaitsForThePerson(t *testing.T) {
	rig, gh := newHeldGithubRig(t, "thulu-github@read+run_dispatch+deployment_approve")
	path := "/internal/github/acme/widgets/runs/99/deployment-review"

	id := rig.held(t, path, map[string]string{"state": "approved", "comment": "ship it"})
	if calls := gh.seen(); len(calls) != 0 {
		t.Fatalf("GitHub was called before anyone confirmed: %v", calls)
	}

	a := rig.confirm(t, id)
	if a.Status != chat.ActionConfirmed || a.ResultCode != http.StatusOK {
		t.Fatalf("confirmed review = %+v", a)
	}
	calls := gh.seen()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "GET /repos/acme/widgets/actions/runs/99/pending_deployments") ||
		!strings.HasPrefix(calls[1], "POST /repos/acme/widgets/actions/runs/99/pending_deployments ") ||
		!strings.Contains(calls[1], `"environment_ids":[5]`) || !strings.Contains(calls[1], `"state":"approved"`) {
		t.Errorf("GitHub saw %v, want the pending list and an approval of environment 5 only", calls)
	}
	if got := rig.tokens.last(); !samePerms(got, githubapp.PermsDeploymentReview) {
		t.Errorf("review token minted with %v, want %v", got, githubapp.PermsDeploymentReview)
	}

	w := keyDo(t, rig.h, http.MethodGet, "/internal/github-token?repo=acme/widgets&permissions=actions:write", rig.key, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("run_dispatch token = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := rig.tokens.last(); got["actions"] != "write" || got["contents"] != "read" {
		t.Errorf("run_dispatch token = %v, want actions:write without contents:write", got)
	}
	if n := len(rig.pendingActions(t)); n != 0 {
		t.Errorf("%d actions still pending; run_dispatch must not be held", n)
	}
}

func TestDelegatedChatGithub_RefusedBeforeHolding(t *testing.T) {
	rig, gh := newHeldGithubRig(t, "thulu-github@read+deployment_approve")

	cases := []struct {
		name, path string
		body       any
		want       int
	}{
		{"stage does not grant pr_merge", "/internal/github/acme/widgets/pulls/7/merge", nil, http.StatusForbidden},
		{"another workspace's repository", "/internal/github/acme/secret/runs/1/deployment-review", nil, http.StatusNotFound},
		{"bad state", "/internal/github/acme/widgets/runs/1/deployment-review", map[string]string{"state": "maybe"}, http.StatusBadRequest},
		{"bad run id", "/internal/github/acme/widgets/runs/x/deployment-review", nil, http.StatusBadRequest},
	}
	for _, c := range cases {
		if w := keyDo(t, rig.h, http.MethodPost, c.path, rig.key, c.body); w.Code != c.want {
			t.Errorf("%s = %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
	}
	if n := len(rig.pendingActions(t)); n != 0 {
		t.Errorf("%d refused requests were held anyway", n)
	}
	if calls := gh.seen(); len(calls) != 0 {
		t.Errorf("GitHub was called: %v", calls)
	}
}
