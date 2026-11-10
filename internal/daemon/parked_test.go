package daemon

import (
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/task"
)

func TestSandboxReleasable(t *testing.T) {
	now := time.Now()
	failedTTL, grace := 6*time.Hour, 30*time.Minute
	at := func(s task.RunStatus, ago time.Duration) *task.Task {
		return &task.Task{Status: s, UpdatedAt: now.Add(-ago)}
	}
	cases := []struct {
		name   string
		t      *task.Task
		docker bool
		want   bool
	}{
		{"parked past grace, docker", at(task.StatusAwaitingInput, time.Hour), true, true},
		{"parked within grace", at(task.StatusAwaitingInput, 10*time.Minute), true, false},
		{"parked past grace, kubernetes", at(task.StatusAwaitingInput, 24*time.Hour), false, false},
		{"awaiting approval past grace, docker", at(task.StatusAwaitingApproval, time.Hour), true, true},
		{"awaiting approval within grace", at(task.StatusAwaitingApproval, 10*time.Minute), true, false},
		{"failed past ttl", at(task.StatusFailed, 7*time.Hour), false, true},
		{"failed within ttl", at(task.StatusFailed, time.Hour), true, false},
		{"running", at(task.StatusRunning, 48*time.Hour), true, false},
	}
	for _, c := range cases {
		if got := sandboxReleasable(c.t, now, failedTTL, grace, c.docker); got != c.want {
			t.Errorf("%s: releasable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestQuestionsSandboxGrace(t *testing.T) {
	t.Setenv(envQuestionsSandboxGrace, "")
	if got := questionsSandboxGrace(); got != defaultQuestionsSandboxGrace {
		t.Errorf("unset = %v, want %v", got, defaultQuestionsSandboxGrace)
	}
	t.Setenv(envQuestionsSandboxGrace, "2h")
	if got := questionsSandboxGrace(); got != 2*time.Hour {
		t.Errorf("2h = %v", got)
	}
	t.Setenv(envQuestionsSandboxGrace, "soon")
	if got := questionsSandboxGrace(); got != defaultQuestionsSandboxGrace {
		t.Errorf("garbage = %v, want the default", got)
	}
}

func TestStuckWorktreeRootsIncludeParkedTasks(t *testing.T) {
	old := time.Now().Add(-30 * 24 * time.Hour)
	cutoff := time.Now().Add(-14 * 24 * time.Hour)
	all := []task.Task{
		{ID: "parked", Status: task.StatusAwaitingInput, UpdatedAt: old, WorktreePaths: map[string]string{"p": "/w"}},
		{ID: "running", Status: task.StatusRunning, UpdatedAt: old, WorktreePaths: map[string]string{"p": "/w2"}},
		{ID: "approval", Status: task.StatusAwaitingApproval, UpdatedAt: old, WorktreePaths: map[string]string{"p": "/w3"}},
	}
	roots := stuckWorktreeRoots(all, cutoff)
	if len(roots) != 2 || roots[0] != "parked" || roots[1] != "approval" {
		t.Errorf("roots = %v, want [parked approval]", roots)
	}
}
