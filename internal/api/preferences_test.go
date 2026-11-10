package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func readTasksView(t *testing.T, h http.Handler, token string) (map[string]any, bool) {
	t.Helper()
	w := tokenDo(t, h, http.MethodGet, "/me/preferences", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /me/preferences = %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Preferences map[string]map[string]any `json:"preferences"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	v, ok := body.Preferences["tasks.view"]
	return v, ok
}

func putRaw(t *testing.T, h http.Handler, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPreferences_FollowThePersonAndOnlyThem(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	ana, ws := mintWorkspaceUserAs(t, srv, "ana-prefs@test.com", "Ana", auth.RoleViewer)
	u, err := srv.authStore.CreateUser(t.Context(), "bob-prefs@test.com", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.authStore.AddMember(t.Context(), ws, u.ID, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	bob, err := auth.IssueJWT(auth.Claims{UserID: u.ID, Email: u.Email, WorkspaceID: ws, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := readTasksView(t, h, ana); ok {
		t.Fatal("a person who never chose has a tasks.view")
	}
	w := tokenDo(t, h, http.MethodPut, "/me/preferences/tasks.view", ana, map[string]string{"layout": "list", "sort": "priority", "dir": "desc"})
	if w.Code != http.StatusOK {
		t.Fatalf("viewer PUT = %d: %s", w.Code, w.Body.String())
	}
	got, ok := readTasksView(t, h, ana)
	want := map[string]any{"layout": "list", "group": "none", "sort": "priority", "dir": "desc"}
	if !ok || !mapsEqual(got, want) {
		t.Errorf("ana reads %v, want %v (group filled in)", got, want)
	}
	if v, ok := readTasksView(t, h, bob); ok {
		t.Errorf("another member of the workspace reads ana's view: %v", v)
	}

	w = tokenDo(t, h, http.MethodPut, "/me/preferences/tasks.view", ana, map[string]string{"layout": "board"})
	if w.Code != http.StatusOK {
		t.Fatalf("second PUT = %d: %s", w.Code, w.Body.String())
	}
	if got, _ := readTasksView(t, h, ana); got["layout"] != "board" || got["sort"] != "" {
		t.Errorf("a PUT replaces the whole value: got %v", got)
	}
}

func TestPreferences_DevSessionKeepsItsOwn(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := putRaw(t, h, "/me/preferences/tasks.view", "", `{"layout":"list"}`); w.Code != http.StatusOK {
		t.Fatalf("dev session PUT = %d: %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/me/preferences", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"layout":"list"`) {
		t.Errorf("dev session reads %s", w.Body.String())
	}
}

func TestPreferences_RefuseWhatTheUICannotRead(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	ana, ws := mintWorkspaceUserAs(t, srv, "ana-bad-prefs@test.com", "Ana", auth.RoleDeveloper)

	for name, c := range map[string]struct {
		path, body string
		want       int
	}{
		"unknown layout":    {"/me/preferences/tasks.view", `{"layout":"kanban"}`, http.StatusBadRequest},
		"missing layout":    {"/me/preferences/tasks.view", `{"sort":"title"}`, http.StatusBadRequest},
		"unknown sort":      {"/me/preferences/tasks.view", `{"layout":"list","sort":"cost"}`, http.StatusBadRequest},
		"unknown field":     {"/me/preferences/tasks.view", `{"layout":"list","color":"red"}`, http.StatusBadRequest},
		"not json":          {"/me/preferences/tasks.view", `list`, http.StatusBadRequest},
		"two values":        {"/me/preferences/tasks.view", `{"layout":"list"}{"layout":"board"}`, http.StatusBadRequest},
		"unknown key":       {"/me/preferences/theme", `{"layout":"list"}`, http.StatusNotFound},
		"larger than 4 KiB": {"/me/preferences/tasks.view", `{"layout":"list","sort":"` + strings.Repeat("x", 5000) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		if w := putRaw(t, h, c.path, ana, c.body); w.Code != c.want {
			t.Errorf("%s: PUT = %d, want %d: %s", name, w.Code, c.want, w.Body.String())
		}
	}
	if _, ok := readTasksView(t, h, ana); ok {
		t.Error("a refused PUT stored something")
	}

	key := mintKey(t, srv, ws, auth.RoleAdmin, auth.PermTasksRead)
	r := httptest.NewRequest(http.MethodPut, "/me/preferences/tasks.view", bytes.NewReader([]byte(`{"layout":"list"}`)))
	r.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("an API key PUT = %d, want 403: a key has no person to remember it for", w.Code)
	}
}

func TestPreferences_AStaleStoredValueFallsBackToTheDefault(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	ana, _ := mintWorkspaceUserAs(t, srv, "ana-stale-prefs@test.com", "Ana", auth.RoleDeveloper)
	claims, err := auth.ValidateJWT(ana)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"tasks.view": `{"layout":"timeline"}`,
		"retired":    `{"x":1}`,
	} {
		if err := srv.authStore.SetPreference(t.Context(), claims.UserID, key, value); err != nil {
			t.Fatal(err)
		}
	}
	w := tokenDo(t, h, http.MethodGet, "/me/preferences", ana, nil)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"preferences":{}}` {
		t.Errorf("GET = %d %s, want an empty set", w.Code, w.Body.String())
	}
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
