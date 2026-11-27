package project

import (
	"testing"
	"time"
)

func TestReleaseSettingsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "p1", testWS)

	got, err := s.Get(ctx, "p1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ReleaseMode != "" || got.ReleaseEnvironment != "" {
		t.Fatalf("fresh project release = %q/%q, want empty", got.ReleaseMode, got.ReleaseEnvironment)
	}

	mode, env := ReleaseBatch, "production"
	if _, err := s.Update(ctx, "p1", Patch{ReleaseMode: &mode, ReleaseEnvironment: &env}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _ = s.Get(ctx, "p1"); got.ReleaseMode != ReleaseBatch || got.ReleaseEnvironment != "production" {
		t.Errorf("after Update release = %q/%q", got.ReleaseMode, got.ReleaseEnvironment)
	}

	// A patch that names only the mode keeps the environment.
	mode = ReleaseAuto
	if _, err := s.Update(ctx, "p1", Patch{ReleaseMode: &mode}); err != nil {
		t.Fatalf("Update mode: %v", err)
	}
	if got, _ = s.Get(ctx, "p1"); got.ReleaseMode != ReleaseAuto || got.ReleaseEnvironment != "production" {
		t.Errorf("after mode-only Update release = %q/%q", got.ReleaseMode, got.ReleaseEnvironment)
	}
}

func TestReleaseSettingsRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "p1", testWS)
	bad := "nightly"
	if _, err := s.Update(ctx, "p1", Patch{ReleaseMode: &bad}); err == nil {
		t.Error("release_mode nightly accepted")
	}
	env := "prod; rm -rf /"
	if _, err := s.Update(ctx, "p1", Patch{ReleaseEnvironment: &env}); err == nil {
		t.Error("release_environment with a semicolon accepted")
	}
	if got, _ := s.Get(ctx, "p1"); got.ReleaseMode != "" || got.ReleaseEnvironment != "" {
		t.Errorf("a rejected patch changed the row: %q/%q", got.ReleaseMode, got.ReleaseEnvironment)
	}
}

func TestImportProjectKeepsReleaseSettings(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	p := Project{ID: "p2", Name: "p2", Path: "/data/projects/p2", RepoURL: "https://example.com/p2.git",
		DefaultBranch: "main", WorkspaceID: testWS, CreatedAt: time.Now().UTC(),
		ReleaseMode: ReleaseAuto, ReleaseEnvironment: "prod"}
	if err := s.ImportProject(ctx, p); err != nil {
		t.Fatalf("ImportProject: %v", err)
	}
	got, err := s.Get(ctx, "p2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ReleaseMode != ReleaseAuto || got.ReleaseEnvironment != "prod" {
		t.Errorf("imported release = %q/%q", got.ReleaseMode, got.ReleaseEnvironment)
	}
	p.ID, p.Name, p.ReleaseMode = "p3", "p3", "weekly"
	if err := s.ImportProject(ctx, p); err == nil {
		t.Error("ImportProject accepted release_mode weekly")
	}
}
