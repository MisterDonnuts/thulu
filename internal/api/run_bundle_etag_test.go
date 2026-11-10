package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func etagFixture(t *testing.T) (*Server, types.AgentType) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("agents/developer/docker-compose.yml",
		"services:\n  agent:\n    labels:\n"+
			"      thulu.agent: \"true\"\n"+
			"      thulu.stages: \"in_progress\"\n"+
			"      thulu.skills.default.in_progress: \"worktree-protocol\"\n")
	write("agents/developer/prompts/main.md", "you are the developer")
	write("skills/_base/prompt.md", "shared rules")
	write("skills/worktree-protocol/prompt.md", "never commit to main")

	discovered, err := types.Discover(root)
	if err != nil || len(discovered) != 1 {
		t.Fatalf("discover: %v (%d agents)", err, len(discovered))
	}
	return &Server{agentCatalog: types.NewCatalog(root), agentsDir: root}, discovered[0]
}

func TestBundleETagIsStableUntilContentChanges(t *testing.T) {
	srv, at := etagFixture(t)
	ctx := context.Background()

	first, err := bundleETag(ctx, srv.content(ctx, ""), at, "in_progress")
	if err != nil {
		t.Fatalf("etag: %v", err)
	}
	if first == "" {
		t.Fatal("empty etag")
	}
	again, err := bundleETag(ctx, srv.content(ctx, ""), at, "in_progress")
	if err != nil {
		t.Fatalf("etag again: %v", err)
	}
	if first != again {
		t.Fatalf("etag moved with no change: %s then %s", first, again)
	}

	if err := os.WriteFile(filepath.Join(srv.agentsDir, "skills", "worktree-protocol", "prompt.md"),
		[]byte("never commit to main, ever"), 0o600); err != nil {
		t.Fatalf("edit skill: %v", err)
	}
	changed, err := bundleETag(ctx, srv.content(ctx, ""), at, "in_progress")
	if err != nil {
		t.Fatalf("etag after edit: %v", err)
	}
	if changed == first {
		t.Fatal("etag did not move after a granted skill changed")
	}
}

func TestRunBundleServes304OnlyForTheMatchingTag(t *testing.T) {
	t.Parallel()
	f := newBundleFixture(t)
	_, key := f.task(t, "developer")

	first := f.fetchBundle(t, key, "?stage=in_progress")
	if first.Code != 200 {
		t.Fatalf("first fetch = %d", first.Code)
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag on the bundle response")
	}

	req := httptest.NewRequest("GET", "/run/bundle?stage=in_progress", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("If-None-Match", tag)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != 304 {
		t.Fatalf("matching If-None-Match = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 carried %d bytes of body", rec.Body.Len())
	}

	req2 := httptest.NewRequest("GET", "/run/bundle?stage=in_progress", nil)
	req2.Header.Set("Authorization", "Bearer "+key)
	req2.Header.Set("If-None-Match", `"stale"`)
	rec2 := httptest.NewRecorder()
	f.h.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("stale If-None-Match = %d, want 200", rec2.Code)
	}
	if rec2.Body.Len() == 0 {
		t.Fatal("200 carried no bundle")
	}
}

func TestETagMatchesHandlesListsAndWeakValidators(t *testing.T) {
	const tag = `"abc123"`
	for _, header := range []string{tag, `"other", "abc123"`, `W/"abc123"`, "*"} {
		if !etagMatches(header, tag) {
			t.Fatalf("If-None-Match %q should match %s", header, tag)
		}
	}
	for _, header := range []string{`"nope"`, `"ab", "cd"`, ""} {
		if etagMatches(header, tag) {
			t.Fatalf("If-None-Match %q should not match %s", header, tag)
		}
	}
}
