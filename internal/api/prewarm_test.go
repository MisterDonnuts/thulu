package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestSetupPrewarm_StatusAndAuthz(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	admin, ws := mintWorkspaceUser(t, srv, "prewarm-a@test.com", "PrewarmA")
	other, _ := mintWorkspaceUser(t, srv, "prewarm-b@test.com", "PrewarmB")
	pid := createProject(t, h, admin, "Prewarm App")
	base := "/projects/" + pid + "/init-script"
	path := base + "/prewarm"

	status := func(token string) prewarmStatus {
		t.Helper()
		w := getJSON(t, h, path, token)
		if w.Code != http.StatusOK {
			t.Fatalf("GET prewarm = %d: %s", w.Code, w.Body.String())
		}
		var st prewarmStatus
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}

	if st := status(admin); st.Available || st.Eligible || st.Running || st.Prewarms == nil {
		t.Fatalf("before a script = %+v", st)
	}
	if w := sendJSON(t, h, http.MethodPut, base, admin, map[string]any{
		"script": "npm start",
		"manifest": map[string]any{
			"setup": "npm ci",
			"tools": map[string]string{"node": "22"},
			"cache": map[string]any{"key": []string{"package-lock.json"}, "paths": []string{"node_modules"}},
		},
	}); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}
	if st := status(admin); !st.Eligible {
		t.Fatalf("after a setup with cache paths = %+v", st)
	}
	if w := sendJSON(t, h, http.MethodPost, path, admin, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST without sandboxes = %d, want 503: %s", w.Code, w.Body.String())
	}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if w := sendJSON(t, h, method, path, other, nil); w.Code != http.StatusNotFound {
			t.Errorf("other workspace %s = %d, want 404", method, w.Code)
		}
	}
	viewer := mintKey(t, srv, ws, auth.RoleViewer, auth.PermAll)
	developer := mintKey(t, srv, ws, auth.RoleDeveloper, auth.PermAll)
	if w := keyDo(t, h, http.MethodGet, path, viewer, nil); w.Code != http.StatusOK {
		t.Fatalf("viewer GET = %d", w.Code)
	}
	for _, key := range []string{viewer, developer} {
		if w := keyDo(t, h, http.MethodPost, path, key, nil); w.Code != http.StatusForbidden {
			t.Fatalf("non-admin POST = %d, want 403", w.Code)
		}
	}

	if w := sendJSON(t, h, http.MethodPut, base, admin, map[string]any{
		"script": "npm start", "manifest": map[string]any{"setup": "npm ci"},
	}); w.Code != http.StatusOK {
		t.Fatalf("PUT 2 = %d: %s", w.Code, w.Body.String())
	}
	if st := status(admin); st.Eligible {
		t.Fatalf("setup without cache paths = %+v", st)
	}
}
