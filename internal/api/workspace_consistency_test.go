package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func requestAs(workspaceID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	if workspaceID == "" {
		return asDevOperator(r)
	}
	return r.WithContext(auth.WithClaims(r.Context(), &auth.Claims{WorkspaceID: workspaceID}))
}

func writeWorkspace(s *Server, r *http.Request) string {
	wid, _ := s.requestWorkspace(r)
	return wid
}

func TestWorkspace_RestrictedCallerWithNoWorkspaceIsRefused(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	owner, _ := mintWorkspaceUser(t, srv, "solewh@test.com", "Sole")
	if w := postJSONAuth(t, h, "/webhooks", owner, map[string]any{
		"url": "https://example.com/hook", "events": []string{"task.created"},
	}); w.Code != http.StatusCreated {
		t.Fatalf("POST /webhooks = %d: %s", w.Code, w.Body.String())
	}

	nowhere := httptest.NewRequest(http.MethodGet, "/webhooks", nil)
	nowhere = nowhere.WithContext(auth.WithClaims(nowhere.Context(),
		&auth.Claims{WorkspaceID: "", Role: auth.RoleOwner}))
	if wid, ok := srv.requestWorkspace(nowhere); ok || wid != "" {
		t.Errorf("requestWorkspace = (%q, %v), want (\"\", false)", wid, ok)
	}

	for name, handler := range map[string]http.HandlerFunc{
		"GET /webhooks":     srv.handleListWebhooks,
		"GET /llm-profiles": srv.handleListLLMProfiles,
		"GET /engines":      srv.handleListEngines,
	} {
		w := httptest.NewRecorder()
		handler(w, nowhere)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403: %s", name, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "example.com") {
			t.Errorf("%s leaked the sole workspace's rows: %s", name, w.Body.String())
		}
	}

	w := httptest.NewRecorder()
	if _, ok := srv.targetWorkspace(w, nowhere); ok || w.Code != http.StatusForbidden {
		t.Errorf("targetWorkspace ok=%v status=%d, want refused with 403", ok, w.Code)
	}

	if wid, ok := srv.requestWorkspace(requestAs("")); !ok || wid == "" {
		t.Errorf("unrestricted caller: requestWorkspace = (%q, %v), want a workspace", wid, ok)
	}
}

func TestWorkspace_IdentifiedCallerReadsAndWritesTheSameWorkspace(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)

	for _, wid := range []string{"acme", "default", "w-123"} {
		r := requestAs(wid)
		read, unrestricted := srv.callerWorkspace(r)
		if unrestricted {
			t.Errorf("%s: a caller presenting claims must be restricted to them", wid)
		}
		if read != wid {
			t.Errorf("callerWorkspace = %q, want %q", read, wid)
		}
		if write := writeWorkspace(srv, r); write != read {
			t.Errorf("%s: writes go to %q but visibility is computed against %q", wid, write, read)
		}
	}
}

func TestWorkspace_UnrestrictedCallerWritesToTheDefaultWorkspace(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	r := requestAs("")

	read, unrestricted := srv.callerWorkspace(r)
	if !unrestricted {
		t.Skip("this build does not grant unrestricted access without credentials")
	}
	if read != "" {
		t.Errorf("callerWorkspace = %q, want empty so every resource is visible", read)
	}

	write := writeWorkspace(srv, r)
	if write == "" {
		t.Fatal("writes would land in the empty workspace, which no store can scope")
	}
	if write != defaultWorkspaceID {
		t.Errorf("write workspace = %q, want %q", write, defaultWorkspaceID)
	}
	if again := writeWorkspace(srv, requestAs("")); again != write {
		t.Errorf("same caller, two answers: %q then %q", write, again)
	}
}

func TestWorkspace_WhatYouWriteYouCanSee(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)

	for _, wid := range []string{"acme", "", "default"} {
		r := requestAs(wid)
		write := writeWorkspace(srv, r)
		read, unrestricted := srv.callerWorkspace(r)
		if !workspaceVisible(write, read, unrestricted) {
			t.Errorf("caller %q writes into workspace %q and then cannot see it", wid, write)
		}
	}
}
