package runner

import (
	"context"
	"testing"
	"time"
)

func TestWithin(t *testing.T) {
	roots := []string{"/Users/a/code", "/srv/repo"}
	cases := map[string]bool{
		"/Users/a/code":            true,
		"/Users/a/code/shop":       true,
		"/Users/a/code/shop/../x":  true,
		"/Users/a/codebase":        false,
		"/Users/a":                 false,
		"/srv/repo":                true,
		"/Users/a/code/../secrets": false,
		"relative/code":            false,
	}
	for path, want := range cases {
		if got := Within(roots, path); got != want {
			t.Errorf("Within(%q) = %v, want %v", path, got, want)
		}
	}
	if Within(nil, "/Users/a/code") {
		t.Error("no roots allowed a folder")
	}
}

func TestSelfContained(t *testing.T) {
	if (JobSpec{}).SelfContained() {
		t.Error("a job without projects counted as self-contained")
	}
	mixed := JobSpec{Projects: []JobProject{{ID: "a", Path: "/x"}, {ID: "b"}}}
	if mixed.SelfContained() {
		t.Error("a job with a project without folder counted as self-contained")
	}
	full := JobSpec{Projects: []JobProject{{ID: "a", Path: "/x"}, {ID: "b", Path: "/y"}}}
	if !full.SelfContained() {
		t.Error("a job whose projects all carry a folder did not count as self-contained")
	}
	if full.MinProtocol() != ProtocolFolders {
		t.Errorf("a folders job needs protocol %d, want %d", full.MinProtocol(), ProtocolFolders)
	}
	daemon := JobSpec{Projects: []JobProject{{ID: "a", Path: "/x"}, {ID: "b", Source: SourceDaemon}}}
	if !daemon.SelfContained() || daemon.MinProtocol() != ProtocolDaemonCode {
		t.Errorf("a job with a project from the daemon: self-contained %v, protocol %d; want true, %d",
			daemon.SelfContained(), daemon.MinProtocol(), ProtocolDaemonCode)
	}
}

// A job whose projects carry their folder reaches the runner even when its config maps none of them:
// the folder lives in the dashboard now, and the runner checks it against its allowed folders.
func TestClaimHandsSelfContainedJobsWithoutMapping(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")
	j := &Job{RunnerID: rn.RunnerID, WorkspaceID: "ws1", TaskID: "task1", ProjectID: "projA",
		InstanceID: "dev-1", AgentName: "dev", Stage: "in_progress",
		Spec: JobSpec{InstanceID: "dev-1", TaskID: "task1",
			Projects: []JobProject{{ID: "projA", Path: "/Users/a/code/shop"}}}}
	if err := s.Enqueue(ctx, j); err != nil {
		t.Fatal(err)
	}
	spec, err := s.Claim(ctx, rn.RunnerID, ProtocolVersion, nil, time.Minute)
	if err != nil || spec == nil || spec.ID != j.ID {
		t.Fatalf("Claim = %+v, %v; want the self-contained job", spec, err)
	}
	if spec.Projects[0].Path != "/Users/a/code/shop" {
		t.Errorf("claimed spec lost the folder: %+v", spec.Projects)
	}
}

func TestInventoryRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")

	inv, err := s.Inventory(ctx, "ws1", rn.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.ScannedAt.IsZero() || inv.Roots == nil || inv.Repos == nil {
		t.Fatalf("unreported inventory = %+v, want zero time and empty lists", inv)
	}
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	want := Inventory{Roots: []string{"/Users/a/code"},
		Repos:     []InventoryRepo{{Path: "/Users/a/code/shop", Name: "shop", Branch: "main"}},
		Mapped:    map[string]string{"projA": "/Users/a/code/shop"},
		ScannedAt: at}
	if err := s.SaveInventory(ctx, rn.RunnerID, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Inventory(ctx, "ws1", rn.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ScannedAt.Equal(at) || len(got.Repos) != 1 || got.Repos[0].Branch != "main" || got.Mapped["projA"] == "" {
		t.Fatalf("Inventory = %+v, want %+v", got, want)
	}
	if _, err := s.Inventory(ctx, "ws2", rn.RunnerID); err == nil {
		t.Error("another workspace read the runner's folders")
	}
}

func TestUnnamedTokenTakesTheHostname(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tok, _, err := s.CreateRegistration(ctx, "ws1", "  ", "admin@test")
	if err != nil {
		t.Fatalf("an unnamed token was refused: %v", err)
	}
	resp, err := s.Register(ctx, RegisterRequest{Token: tok, Hostname: "juans-mac.local"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Name != "juans-mac" {
		t.Errorf("name = %q, want the hostname without .local", resp.Name)
	}
	if registeredName("") != "runner" {
		t.Error("a machine without hostname got no name")
	}
}
