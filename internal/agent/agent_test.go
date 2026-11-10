package agent

import (
	"testing"
	"time"
)

func TestUptimeRunning(t *testing.T) {
	a := &Agent{
		Status:    StatusRunning,
		StartedAt: time.Now().Add(-5 * time.Minute),
	}
	uptime := a.Uptime()
	if uptime < 4*time.Minute || uptime > 6*time.Minute {
		t.Fatalf("expected ~5m uptime, got %v", uptime)
	}
}

func TestUptimeExited(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	finish := start.Add(3 * time.Minute)
	a := &Agent{
		Status:     StatusExited,
		StartedAt:  start,
		FinishedAt: &finish,
	}
	uptime := a.Uptime()
	if uptime != 3*time.Minute {
		t.Fatalf("expected 3m uptime, got %v", uptime)
	}
}

func TestUptimeExitedNoFinish(t *testing.T) {
	a := &Agent{
		Status:    StatusExited,
		StartedAt: time.Now().Add(-10 * time.Minute),
	}
	if a.Uptime() != 0 {
		t.Fatalf("expected 0 uptime when exited with no FinishedAt, got %v", a.Uptime())
	}
}

func TestUptimeStopped(t *testing.T) {
	a := &Agent{
		Status:    StatusStopped,
		StartedAt: time.Now().Add(-10 * time.Minute),
	}
	if a.Uptime() != 0 {
		t.Fatalf("expected 0 uptime for stopped with no finish, got %v", a.Uptime())
	}
}

func TestLabelConstants(t *testing.T) {
	if LabelManaged != "thulu.agent" {
		t.Errorf("LabelManaged = %q, want %q", LabelManaged, "thulu.agent")
	}
	if LabelType != "thulu.type" {
		t.Errorf("LabelType = %q, want %q", LabelType, "thulu.type")
	}
	if LabelProject != "thulu.project" {
		t.Errorf("LabelProject = %q, want %q", LabelProject, "thulu.project")
	}
	if LabelInstance != "thulu.instance" {
		t.Errorf("LabelInstance = %q, want %q", LabelInstance, "thulu.instance")
	}
}
