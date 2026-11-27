package project

import (
	"errors"
	"testing"
	"time"
)

func TestValidateDeployWatchMenu(t *testing.T) {
	ok := []struct {
		in         DeployWatchInput
		wantTarget string
		wantEvery  time.Duration
	}{
		{DeployWatchInput{Source: WatchGitHubWorkflow, Target: "deploy.yml"}, ".github/workflows/deploy.yml", 0},
		{DeployWatchInput{Source: WatchGitHubWorkflow, Target: ".github/workflows/prod.yaml"}, ".github/workflows/prod.yaml", 0},
		{DeployWatchInput{Source: WatchGitHubWorkflow, Target: " Deploy to prod "}, "Deploy to prod", 0},
		{DeployWatchInput{Source: WatchGitHubDeployment, Target: "production"}, "production", 0},
		{DeployWatchInput{Source: WatchCloud, Target: "env_1"}, "env_1", DefaultWatchInterval},
		{DeployWatchInput{Source: WatchHTTP, Target: "https://shop.example.com/version", Interval: 5 * time.Minute},
			"https://shop.example.com/version", 5 * time.Minute},
		{DeployWatchInput{Source: WatchHTTP, Target: "http://shop.example.com/health", Interval: 24 * time.Hour},
			"http://shop.example.com/health", 24 * time.Hour},
	}
	for _, c := range ok {
		got, err := ValidateDeployWatch(c.in)
		if err != nil {
			t.Errorf("%+v: %v", c.in, err)
			continue
		}
		if got.Target != c.wantTarget || got.Interval != c.wantEvery {
			t.Errorf("%+v → target %q every %v, want %q every %v", c.in, got.Target, got.Interval, c.wantTarget, c.wantEvery)
		}
	}

	bad := []DeployWatchInput{
		{Source: "jenkins", Target: "x"},
		{Source: WatchGitHubWorkflow, Target: `deploy" || true`},
		{Source: WatchGitHubWorkflow, Target: "scripts/deploy.yml"},
		{Source: WatchGitHubWorkflow, Target: "{{ trigger }}"},
		{Source: WatchGitHubWorkflow, Target: "deploy.yml", Interval: 10 * time.Minute},
		{Source: WatchGitHubDeployment, Target: "prod; rm"},
		{Source: WatchCloud, Target: "../env"},
		{Source: WatchHTTP, Target: "ftp://shop.example.com"},
		{Source: WatchHTTP, Target: "https://user:pw@shop.example.com/version"},
		{Source: WatchHTTP, Target: "https://shop.example.com/{{x}}"},
		{Source: WatchHTTP, Target: "https://shop.example.com", Interval: 4 * time.Minute},
		{Source: WatchHTTP, Target: "https://shop.example.com", Interval: 25 * time.Hour},
		{Source: WatchHTTP, Target: "https://shop.example.com", Interval: 90 * time.Second},
	}
	for _, in := range bad {
		if _, err := ValidateDeployWatch(in); err == nil {
			t.Errorf("%+v accepted", in)
		}
	}
}

// One watch per project, source and target: a second set updates it and keeps what the watch already
// observed and the automation it owns.
func TestDeployWatchUpsertAndObservation(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "p1", testWS)

	w := &DeployWatch{WorkspaceID: testWS, ProjectID: "p1", Source: WatchHTTP, Target: "https://shop.example.com/version",
		IntervalSeconds: 900, BoardID: "thulu", TypeID: "release", CreatedBy: "thulu-release", CreatedByTask: "t1"}
	first, created, err := s.UpsertDeployWatch(ctx, w)
	if err != nil || !created {
		t.Fatalf("first upsert: created=%v err=%v", created, err)
	}
	if got, err := s.SwapDeployWatchAutomation(ctx, first.ID, "", "auto1"); err != nil || got != "auto1" {
		t.Fatalf("swap onto an unarmed watch = %q, %v", got, err)
	}
	// A second writer that read the watch unarmed loses: the watch keeps the first automation.
	if got, err := s.SwapDeployWatchAutomation(ctx, first.ID, "", "auto2"); err != nil || got != "auto1" {
		t.Fatalf("losing swap = %q, %v, want auto1", got, err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.RecordDeployObservation(ctx, first.ID, DeployObservation{State: WatchStateSuccess, Version: "v1", At: at}, false); err != nil {
		t.Fatal(err)
	}

	w.IntervalSeconds = 300
	second, created, err := s.UpsertDeployWatch(ctx, w)
	if err != nil || created {
		t.Fatalf("second upsert: created=%v err=%v", created, err)
	}
	if second.ID != first.ID || second.IntervalSeconds != 300 || second.AutomationID != "auto1" ||
		second.LastVersion != "v1" || second.LastState != WatchStateSuccess {
		t.Fatalf("second = %+v", second)
	}
	if second.LastChangedAt != nil {
		t.Errorf("a baseline observation set last_changed_at")
	}

	// A failed read keeps the last state and version.
	if err := s.RecordDeployObservation(ctx, first.ID, DeployObservation{Error: "timeout", At: at}, false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetDeployWatch(ctx, first.ID)
	if got.LastVersion != "v1" || got.LastError != "timeout" {
		t.Fatalf("after error = %+v", got)
	}
	if err := s.RecordDeployObservation(ctx, first.ID, DeployObservation{State: WatchStateFailure, Version: "v1", At: at}, true); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetDeployWatch(ctx, first.ID)
	if got.LastState != WatchStateFailure || got.LastError != "" || got.LastChangedAt == nil {
		t.Fatalf("after change = %+v", got)
	}

	other := *w
	other.Source, other.Target, other.IntervalSeconds = WatchGitHubDeployment, "production", 0
	if _, created, err := s.UpsertDeployWatch(ctx, &other); err != nil || !created {
		t.Fatalf("second watch: created=%v err=%v", created, err)
	}
	list, err := s.ListDeployWatches(ctx, "p1")
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d (%v)", len(list), err)
	}

	if err := s.DeleteDeployWatch(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDeployWatch(ctx, first.ID); !errors.Is(err, ErrDeployWatchNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if _, err := s.GetDeployWatch(ctx, first.ID); !errors.Is(err, ErrDeployWatchNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
}

func TestDeleteProjectDropsItsWatches(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "p1", testWS)
	if _, _, err := s.UpsertDeployWatch(ctx, &DeployWatch{WorkspaceID: testWS, ProjectID: "p1",
		Source: WatchGitHubDeployment, Target: "production", BoardID: "thulu"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListDeployWatches(ctx, "p1"); len(list) != 0 {
		t.Fatalf("watches outlived their project: %+v", list)
	}
}
