package types

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAgent(t *testing.T, root, name, stages string) string {
	t.Helper()
	dir := filepath.Join(root, "agents", name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "docker-compose.yml")
	body := "services:\n  " + name + ":\n    labels:\n" +
		"      thulu.agent: \"true\"\n" +
		"      thulu.type: \"" + name + "\"\n" +
		"      thulu.stages: \"" + stages + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	return path
}

func TestCatalog_MatchesDiscover(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "alpha", "refinement,in_progress")
	writeAgent(t, root, "beta", "review")

	direct, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	cached, err := NewCatalog(root).All()
	if err != nil {
		t.Fatalf("Catalog.All: %v", err)
	}
	if len(cached) != len(direct) {
		t.Fatalf("catalog returned %d agents, Discover %d", len(cached), len(direct))
	}
	byName := map[string]AgentType{}
	for _, at := range cached {
		byName[at.Name] = at
	}
	for _, at := range direct {
		got, ok := byName[at.Name]
		if !ok {
			t.Fatalf("catalog is missing agent %q", at.Name)
		}
		if len(got.ValidStages) != len(at.ValidStages) {
			t.Errorf("%s: stages = %v, want %v", at.Name, got.ValidStages, at.ValidStages)
		}
	}
}

func TestCatalog_SecondCallDoesNotReRead(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "alpha", "refinement")

	c := NewCatalog(root)
	for range 5 {
		if _, err := c.All(); err != nil {
			t.Fatalf("All: %v", err)
		}
	}
	if got := c.reloads(); got != 1 {
		t.Errorf("re-read the agents directory %d times for 5 identical calls, want 1", got)
	}
}

func TestCatalog_PicksUpAnEditedCompose(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "alpha", "refinement")

	c := NewCatalog(root)
	first, err := c.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(first) != 1 || len(first[0].ValidStages) != 1 {
		t.Fatalf("unexpected first read: %+v", first)
	}

	// mtime resolution can be coarse; the sleep makes the edit visible to the catalog's change
	// check.
	time.Sleep(10 * time.Millisecond)
	writeAgent(t, root, "alpha", "refinement,in_progress,review")

	second, err := c.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(second) != 1 || len(second[0].ValidStages) != 3 {
		t.Errorf("stale after edit: %+v", second)
	}
	if c.reloads() < 2 {
		t.Errorf("reloads = %d, want at least 2 — the edit was not noticed", c.reloads())
	}
}

func TestCatalog_PicksUpANewAgent(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "alpha", "refinement")

	c := NewCatalog(root)
	if first, err := c.All(); err != nil || len(first) != 1 {
		t.Fatalf("first read: %+v (%v)", first, err)
	}

	time.Sleep(10 * time.Millisecond)
	writeAgent(t, root, "beta", "review")

	second, err := c.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(second) != 2 {
		t.Errorf("new agent not picked up: %+v", second)
	}
}

func TestCatalog_PicksUpARemovedAgent(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "alpha", "refinement")
	writeAgent(t, root, "beta", "review")

	c := NewCatalog(root)
	if first, err := c.All(); err != nil || len(first) != 2 {
		t.Fatalf("first read: %+v (%v)", first, err)
	}

	time.Sleep(10 * time.Millisecond)
	if err := os.RemoveAll(filepath.Join(root, "agents", "beta")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	second, err := c.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(second) != 1 || second[0].Name != "alpha" {
		t.Errorf("removed agent still listed: %+v", second)
	}
}

func TestCatalog_MissingDirectory(t *testing.T) {
	c := NewCatalog(filepath.Join(t.TempDir(), "nope"))
	got, err := c.All()
	if err == nil && len(got) != 0 {
		t.Errorf("expected no agents, got %+v", got)
	}
	if _, err2 := c.All(); (err == nil) != (err2 == nil) {
		t.Errorf("inconsistent error across calls: %v then %v", err, err2)
	}
}

func TestCatalog_CallersCannotMutateTheCache(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "alpha", "refinement")

	c := NewCatalog(root)
	first, err := c.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	first[0].Name = "tampered"
	first[0].ValidStages = nil

	second, err := c.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if second[0].Name != "alpha" || len(second[0].ValidStages) != 1 {
		t.Errorf("cache was mutated through a returned slice: %+v", second[0])
	}
}

func TestCatalog_ConcurrentReadsAreSafe(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "alpha", "refinement")
	c := NewCatalog(root)

	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 20 {
				if _, err := c.All(); err != nil {
					t.Errorf("All: %v", err)
					return
				}
			}
		}()
	}
	for range 8 {
		<-done
	}
}
