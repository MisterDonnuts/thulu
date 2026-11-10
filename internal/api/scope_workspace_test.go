package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestLLMKey_UnscopedCallerTargetsNamedWorkspace(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	const secret = "sk-ant-api-goes-to-the-named-workspace"
	const target = "ws-owned-by-a-human"

	body, _ := json.Marshal(map[string]string{"provider": "anthropic", "key": secret})
	put := httptest.NewRequest(http.MethodPut, "/llm-key?workspace="+target, strings.NewReader(string(body)))
	put.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, put)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /llm-key?workspace= = %d: %s", w.Code, w.Body.String())
	}
	var st map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["workspace_id"] != target {
		t.Fatalf("PUT echoed workspace_id=%v, want %q", st["workspace_id"], target)
	}

	if !hasKeyIn(t, h, "?workspace="+target) {
		t.Error("key not found in the named workspace")
	}
	if hasKeyIn(t, h, "") {
		t.Error("key also landed in the default workspace — the write was not targeted")
	}
}

func hasKeyIn(t *testing.T, h http.Handler, query string) bool {
	t.Helper()
	g := httptest.NewRecorder()
	h.ServeHTTP(g, httptest.NewRequest(http.MethodGet, "/llm-key"+query, nil))
	if g.Code != http.StatusOK {
		t.Fatalf("GET /llm-key%s = %d: %s", query, g.Code, g.Body.String())
	}
	var st map[string]any
	if err := json.Unmarshal(g.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st["has_key"] == true
}

func TestTargetWorkspace_ScopedCallerCannotLeaveItsWorkspace(t *testing.T) {
	s := &Server{}
	const mine = "ws-mine"

	for _, tc := range []struct {
		name   string
		query  string
		want   string
		wantOK bool
	}{
		{name: "no parameter", query: "", want: mine, wantOK: true},
		{name: "names its own", query: "?workspace=" + mine, want: mine, wantOK: true},
		{name: "names another", query: "?workspace=ws-someone-else", want: "", wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, "/llm-key"+tc.query, nil)
			r = r.WithContext(auth.WithClaims(r.Context(), &auth.Claims{WorkspaceID: mine}))
			w := httptest.NewRecorder()

			got, ok := s.targetWorkspace(w, r)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (body: %s)", ok, tc.wantOK, w.Body.String())
			}
			if got != tc.want {
				t.Errorf("workspace = %q, want %q", got, tc.want)
			}
			if !tc.wantOK && w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
		})
	}
}
