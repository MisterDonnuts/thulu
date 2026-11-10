package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

const testWebhookSecret = "tenancy-hook-secret"

func configureTestGithubApp(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	t.Setenv("OCTOPUS_GITHUB_APP_ID", "123456")
	t.Setenv("OCTOPUS_GITHUB_APP_SLUG", "test-app")
	t.Setenv("OCTOPUS_GITHUB_APP_PRIVATE_KEY", pemStr)
	t.Setenv("OCTOPUS_GITHUB_APP_CLIENT_ID", "Iv23liTEST")
	t.Setenv("OCTOPUS_GITHUB_APP_CLIENT_SECRET", "client-secret")
	t.Setenv("OCTOPUS_GITHUB_APP_WEBHOOK_SECRET", testWebhookSecret)
}

func deliverWebhook(t *testing.T, h http.Handler, event string, payload any) int {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(testWebhookSecret))
	mac.Write(body)
	req := httptest.NewRequest("POST", "/github/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func queuedEvents(t *testing.T, store *automation.Store) []automation.Event {
	t.Helper()
	rows, err := store.DB().Query(`SELECT workspace_id, kind, payload FROM automation_events ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []automation.Event
	for rows.Next() {
		var e automation.Event
		var raw string
		if err := rows.Scan(&e.WorkspaceID, &e.Kind, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &e.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGithubWebhookEventsFollowTheInstallation(t *testing.T) {
	t.Setenv("OCTOPUS_SAAS_MODE", "1")
	configureTestGithubApp(t)
	srv, h := newIntegrationServer(t)
	autoStore, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := automation.NewEngine(autoStore, automation.Deps{})
	srv.SetAutomations(autoStore, engine, automation.NewScheduler(autoStore, engine))

	ctx := t.Context()
	for _, inst := range []githubapp.Installation{
		{InstallationID: 100, WorkspaceID: "ws-a", AccountLogin: "org-a"},
		{InstallationID: 200, AccountLogin: "org-unclaimed"},
	} {
		if err := srv.gh.store.UpsertInstallation(ctx, inst); err != nil {
			t.Fatal(err)
		}
	}
	const prA, prB = "https://github.com/org-a/app/pull/1", "https://github.com/org-b/app/pull/2"
	taskA := &task.Task{WorkspaceID: "ws-a", Title: "a"}
	taskB := &task.Task{WorkspaceID: "ws-b", Title: "b"}
	for tk, url := range map[*task.Task]string{taskA: prA, taskB: prB} {
		if err := srv.taskStore.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if _, err := srv.taskStore.Update(ctx, tk.ID, task.Patch{PRURLs: map[string]string{"p": url}}); err != nil {
			t.Fatal(err)
		}
	}

	pr := func(installation int64, url string) map[string]any {
		return map[string]any{
			"action":       "opened",
			"installation": map[string]any{"id": installation},
			"repository":   map[string]any{"full_name": "org/app"},
			"pull_request": map[string]any{"number": 1, "html_url": url},
		}
	}
	for _, payload := range []map[string]any{pr(200, prA), {"ref": "refs/heads/main"}, pr(999, prA)} {
		if code := deliverWebhook(t, h, "pull_request", payload); code != http.StatusNoContent {
			t.Fatalf("delivery = %d, want 204 (acknowledged even when dropped)", code)
		}
	}
	if got := queuedEvents(t, autoStore); len(got) != 0 {
		t.Fatalf("unattributable deliveries queued %d events, want 0: %+v", len(got), got)
	}

	deliverWebhook(t, h, "pull_request", pr(100, prB))
	deliverWebhook(t, h, "pull_request", pr(100, prA))
	got := queuedEvents(t, autoStore)
	if len(got) != 2 {
		t.Fatalf("queued %d events, want 2: %+v", len(got), got)
	}
	for _, e := range got {
		if e.WorkspaceID != "ws-a" {
			t.Errorf("event attributed to %q, want ws-a (the installation's workspace)", e.WorkspaceID)
		}
	}
	if id, ok := got[0].Payload["task_id"]; ok {
		t.Errorf("ws-b's task %v was attached to ws-a's event", id)
	}
	if got[1].Payload["task_id"] != taskA.ID {
		t.Errorf("task_id = %v, want ws-a's own task %s", got[1].Payload["task_id"], taskA.ID)
	}
}

func TestGithubWebhookUnclaimedInstallationSelfHosted(t *testing.T) {
	t.Setenv("OCTOPUS_SAAS_MODE", "")
	configureTestGithubApp(t)
	srv, h := newIntegrationServer(t)
	autoStore, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := automation.NewEngine(autoStore, automation.Deps{})
	srv.SetAutomations(autoStore, engine, automation.NewScheduler(autoStore, engine))
	if sole := srv.soleWorkspaceID(t.Context()); sole != "" {
		t.Fatalf("the integration server starts with workspace %q; this test needs none", sole)
	}
	if err := srv.gh.store.UpsertInstallation(t.Context(),
		githubapp.Installation{InstallationID: 300, AccountLogin: "org-self"}); err != nil {
		t.Fatal(err)
	}
	push := map[string]any{
		"installation": map[string]any{"id": 300},
		"repository":   map[string]any{"full_name": "org-self/app"},
		"ref":          "refs/heads/main",
	}

	_, wsOnly := mintWorkspaceUser(t, srv, "self-only@test.com", "SelfOnly")
	deliverWebhook(t, h, "push", push)
	got := queuedEvents(t, autoStore)
	if len(got) != 1 || got[0].WorkspaceID != wsOnly {
		t.Fatalf("one workspace: queued %+v, want one event for %s", got, wsOnly)
	}

	mintWorkspaceUser(t, srv, "self-second@test.com", "SelfSecond")
	deliverWebhook(t, h, "push", push)
	if got := queuedEvents(t, autoStore); len(got) != 1 {
		t.Fatalf("two workspaces: queued %d events, want the unclaimed delivery dropped", len(got))
	}
}
