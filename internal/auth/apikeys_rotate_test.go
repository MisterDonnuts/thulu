package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

type apiKeyFixture struct {
	h       *auth.Handler
	store   *auth.UserStore
	wrapped http.Handler
	token   string
	userID  string
}

func newAPIKeyFixture(t *testing.T) apiKeyFixture {
	t.Helper()
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	tokens := mustProvision(t, h, "owner@example.com", "Owner")
	return apiKeyFixture{
		h: h, store: store, wrapped: authedMux(mux),
		token: tokens["access_token"].(string), userID: tokens["user_id"].(string),
	}
}

func (f apiKeyFixture) mint(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	w := authedPost(t, f.wrapped, "/auth/api-keys", body, f.token)
	if w.Code != http.StatusCreated {
		t.Fatalf("mint: %d %s", w.Code, w.Body.String())
	}
	return parseJSON(t, w)
}

func (f apiKeyFixture) list(t *testing.T, path string) []map[string]any {
	t.Helper()
	w := authedGet(t, f.wrapped, path, f.token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	var out []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestListAPIKeys_ExpiryAndCreator(t *testing.T) {
	f := newAPIKeyFixture(t)
	f.mint(t, map[string]any{"name": "CI", "permissions": []string{"ci"}, "expires_in_days": 30})

	keys := f.list(t, "/auth/api-keys")
	if len(keys) != 1 {
		t.Fatalf("want 1 key, got %d", len(keys))
	}
	exp, _ := keys[0]["expires_at"].(string)
	at, err := time.Parse(time.RFC3339Nano, exp)
	if err != nil {
		t.Fatalf("expires_at = %q, want a timestamp", exp)
	}
	if d := time.Until(at); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("expires_at is %v away, want about 30 days", d)
	}
	if got := keys[0]["created_by_email"]; got != "owner@example.com" {
		t.Errorf("created_by_email = %v, want owner@example.com", got)
	}
}

func TestRotateAPIKey(t *testing.T) {
	f := newAPIKeyFixture(t)
	old := f.mint(t, map[string]any{
		"name": "Nightly export", "role": "viewer",
		"permissions": []string{"tasks:read", "runs:read"}, "expires_in_days": 10,
	})
	oldID := old["id"].(string)

	w := authedPost(t, f.wrapped, "/auth/api-keys/"+oldID+"/rotate", map[string]any{"grace_hours": 2}, f.token)
	if w.Code != http.StatusCreated {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}
	rotated := parseJSON(t, w)
	if rotated["key"] == "" || rotated["id"] == oldID {
		t.Fatalf("rotate did not mint a new key: %v", rotated)
	}
	if rotated["name"] != "Nightly export" || rotated["role"] != "viewer" {
		t.Errorf("new key = %v / %v, want the old name and role", rotated["name"], rotated["role"])
	}
	perms, _ := rotated["permissions"].([]any)
	if len(perms) != 2 {
		t.Errorf("permissions = %v, want the old two", perms)
	}
	newExp, _ := time.Parse(time.RFC3339Nano, rotated["expires_at"].(string))
	if d := time.Until(newExp); d < 9*24*time.Hour || d > 11*24*time.Hour {
		t.Errorf("new key lives %v, want the old lifetime of 10 days", d)
	}

	ctx := context.Background()
	ws := old["workspace_id"].(string)
	got, err := f.store.GetWorkspaceAPIKey(ctx, oldID, ws)
	if err != nil || got == nil {
		t.Fatalf("read old key: %v %v", got, err)
	}
	if got.RevokedAt != nil {
		t.Error("the old key was revoked at once; it should get the grace")
	}
	if got.ExpiresAt == nil || time.Until(*got.ExpiresAt) > 2*time.Hour+time.Minute || time.Until(*got.ExpiresAt) < 2*time.Hour-time.Minute {
		t.Errorf("old key expires at %v, want about two hours from now", got.ExpiresAt)
	}
	byID := map[string]map[string]any{}
	for _, k := range f.list(t, "/auth/api-keys") {
		byID[k["id"].(string)] = k
	}
	if byID[oldID]["replaced_by"] != rotated["id"] {
		t.Errorf("old key replaced_by = %v, want %v", byID[oldID]["replaced_by"], rotated["id"])
	}
	if _, set := byID[rotated["id"].(string)]["replaced_by"]; set {
		t.Errorf("the new key says it was replaced: %v", byID[rotated["id"].(string)])
	}
}

func TestRotateAPIKey_Refusals(t *testing.T) {
	f := newAPIKeyFixture(t)
	k := f.mint(t, map[string]any{"name": "CI", "permissions": []string{"ci"}})
	id := k["id"].(string)

	if w := authedPost(t, f.wrapped, "/auth/api-keys/"+id+"/rotate", map[string]any{"grace_hours": 1000}, f.token); w.Code != http.StatusBadRequest {
		t.Errorf("grace past a week: %d, want 400", w.Code)
	}
	if w := authedPost(t, f.wrapped, "/auth/api-keys/nope/rotate", map[string]any{}, f.token); w.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d, want 404", w.Code)
	}
	if w := authedDelete(t, f.wrapped, "/auth/api-keys/"+id, f.token); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w := authedPost(t, f.wrapped, "/auth/api-keys/"+id+"/rotate", map[string]any{}, f.token); w.Code != http.StatusConflict {
		t.Errorf("rotating a revoked key: %d, want 409", w.Code)
	}
}

func TestRotateAPIKey_NoGrace(t *testing.T) {
	f := newAPIKeyFixture(t)
	k := f.mint(t, map[string]any{"name": "CI", "permissions": []string{"ci"}})
	w := authedPost(t, f.wrapped, "/auth/api-keys/"+k["id"].(string)+"/rotate", map[string]any{"grace_hours": 0}, f.token)
	if w.Code != http.StatusCreated {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}
	if f.store.LookupAPIKeyInfo(context.Background(), k["key"].(string)) != nil {
		t.Error("the old key still authenticates after a rotation without grace")
	}
}

func TestListRunKeys(t *testing.T) {
	f := newAPIKeyFixture(t)
	f.h.WithTaskTitles(func(_ context.Context, ids []string) map[string]string {
		out := map[string]string{}
		for _, id := range ids {
			out[id] = "Title of " + id
		}
		return out
	})
	f.h.WithStageLabels(func(_ context.Context, taskID, stage string) string {
		return "Label of " + stage + " on " + taskID
	})
	ws := f.mint(t, map[string]any{"name": "CI", "permissions": []string{"ci"}})["workspace_id"].(string)
	ctx := context.Background()
	if _, _, err := f.store.MintRunKey(ctx, auth.RunScope{TaskID: "t1", Agent: "coder", Stage: "dev"}, ws, time.Hour); err != nil {
		t.Fatal(err)
	}
	_, deadID, err := f.store.MintRunKey(ctx, auth.RunScope{TaskID: "t2", Agent: "coder"}, ws, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.RevokeAPIKey(ctx, deadID); err != nil {
		t.Fatal(err)
	}

	keys := f.list(t, "/auth/run-keys")
	if len(keys) != 1 {
		t.Fatalf("want the one live run key, got %d: %v", len(keys), keys)
	}
	if keys[0]["scope_task_id"] != "t1" || keys[0]["scope_agent"] != "coder" || keys[0]["task_title"] != "Title of t1" {
		t.Errorf("run key = %v", keys[0])
	}
	if keys[0]["stage_label"] != "Label of dev on t1" {
		t.Errorf("stage_label = %v, want the board's name for the stage", keys[0]["stage_label"])
	}
	if got := f.list(t, "/auth/api-keys"); len(got) != 1 || got[0]["name"] != "CI" {
		t.Errorf("api-keys list = %v, want only CI", got)
	}
}
