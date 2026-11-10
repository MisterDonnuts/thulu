package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	dtypes "github.com/docker/docker/api/types"
	dcontainer "github.com/docker/docker/api/types/container"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

func TestTrimSlash(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{[]string{"/my-container"}, "my-container"},
		{[]string{"my-container"}, "my-container"},
		{[]string{"/my-container", "/other"}, "my-container"},
		{[]string{}, ""},
	}
	for _, tt := range tests {
		got := trimSlash(tt.names)
		if got != tt.want {
			t.Errorf("trimSlash(%v) = %q, want %q", tt.names, got, tt.want)
		}
	}
}

func TestTrimName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"/my-container", "my-container"},
		{"my-container", "my-container"},
		{"/", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := trimName(tt.name)
		if got != tt.want {
			t.Errorf("trimName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestContainerToAgent(t *testing.T) {
	c := dtypes.Container{
		ID:      "abc123def456789012345",
		Names:   []string{"/my-agent"},
		Image:   "octopus-docker:latest",
		State:   "running",
		Created: 1712800000,
		Labels: map[string]string{
			"thulu.type":     "planner",
			"thulu.project":  "default",
			"thulu.instance": "capture",
		},
	}

	a := containerToAgent(c)

	if a.ID != "abc123def456" {
		t.Errorf("ID = %q, want %q", a.ID, "abc123def456")
	}
	if a.Name != "my-agent" {
		t.Errorf("Name = %q, want %q", a.Name, "my-agent")
	}
	if a.Type != "planner" {
		t.Errorf("Type = %q, want %q", a.Type, "planner")
	}
	if a.Project != "default" {
		t.Errorf("Project = %q, want %q", a.Project, "default")
	}
	if a.Instance != "capture" {
		t.Errorf("Instance = %q, want %q", a.Instance, "capture")
	}
	if string(a.Status) != "running" {
		t.Errorf("Status = %q, want 'running'", a.Status)
	}
}

func TestContainerToAgentExited(t *testing.T) {
	c := dtypes.Container{
		ID:    "abc123def456789012345",
		Names: []string{"/exited-agent"},
		State: "exited",
		Labels: map[string]string{
			"thulu.type": "test",
		},
	}

	a := containerToAgent(c)
	if string(a.Status) != "exited" {
		t.Errorf("Status = %q, want 'exited'", a.Status)
	}
}

func TestContainerToAgentUnknownState(t *testing.T) {
	c := dtypes.Container{
		ID:     "abc123def456789012345",
		Names:  []string{"/unknown"},
		State:  "paused",
		Labels: map[string]string{},
	}

	a := containerToAgent(c)
	if string(a.Status) != "unknown" {
		t.Errorf("Status = %q, want 'unknown'", a.Status)
	}
}

func TestInspectToAgent(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	info := dtypes.ContainerJSON{
		ContainerJSONBase: &dtypes.ContainerJSONBase{
			ID:   "abc123def456789012345",
			Name: "/my-agent",
			State: &dtypes.ContainerState{
				StartedAt: ts,
			},
			Image: "octopus-docker:latest",
		},
		Config: &dcontainer.Config{
			Labels: map[string]string{
				"thulu.type":     "sonnet",
				"thulu.project":  "myproject",
				"thulu.instance": "run-1",
			},
			Image: "octopus-docker:latest",
		},
	}

	a := inspectToAgent(info)
	if a.ID != "abc123def456" {
		t.Errorf("ID = %q, want abc123def456", a.ID)
	}
	if a.Name != "my-agent" {
		t.Errorf("Name = %q, want my-agent", a.Name)
	}
	if a.Type != "sonnet" {
		t.Errorf("Type = %q, want sonnet", a.Type)
	}
	if a.Project != "myproject" {
		t.Errorf("Project = %q, want myproject", a.Project)
	}
	if a.Instance != "run-1" {
		t.Errorf("Instance = %q, want run-1", a.Instance)
	}
	if string(a.Status) != "running" {
		t.Errorf("Status = %q, want running", a.Status)
	}
}

func TestEnsureObsidianVault(t *testing.T) {
	tmp := t.TempDir()
	agentRoot := filepath.Join(tmp, "sonnet")
	runsDir := filepath.Join(agentRoot, "runs")
	outputDir := filepath.Join(runsDir, "instance-123")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}

	w := &Watcher{}
	w.ensureObsidianVault(outputDir)

	obsDir := filepath.Join(agentRoot, ".obsidian")
	if _, err := os.Stat(obsDir); err != nil {
		t.Errorf(".obsidian dir not created: %v", err)
	}
	for _, f := range []string{"app.json", "core-plugins.json"} {
		p := filepath.Join(obsDir, f)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s not created: %v", f, err)
		}
	}

	w.ensureObsidianVault(outputDir)
}

func TestEnsureObsidianVault_NoRunsDir(t *testing.T) {
	tmp := t.TempDir()
	outputDir := filepath.Join(tmp, "other", "instance-123")
	os.MkdirAll(outputDir, 0755)
	w := &Watcher{}
	w.ensureObsidianVault(outputDir)

	obsDir := filepath.Join(tmp, ".obsidian")
	if _, err := os.Stat(obsDir); err == nil {
		t.Error("obsidian should not be created when parent is not 'runs'")
	}
}

func TestEnsureObsidianVault_Empty(t *testing.T) {
	w := &Watcher{}
	w.ensureObsidianVault("")
}

func TestResolveLabels(t *testing.T) {
	tests := []struct {
		name          string
		labels        map[string]string
		wantEngine    string
		wantProjectID string
	}{
		{
			name:          "both canonical labels present",
			labels:        map[string]string{"thulu.engine": "opencode", "thulu.project_id": "proj-42"},
			wantEngine:    "opencode",
			wantProjectID: "proj-42",
		},
		{
			name:          "engine empty → default",
			labels:        map[string]string{"thulu.project_id": "proj-42"},
			wantEngine:    engine.DefaultName,
			wantProjectID: "proj-42",
		},
		{
			name:          "project_id missing → fall back to thulu.project",
			labels:        map[string]string{"thulu.engine": "claude-code", "thulu.project": "task-abc"},
			wantEngine:    "claude-code",
			wantProjectID: "task-abc",
		},
		{
			name:          "neither id label → empty (caller has nothing to fall back to)",
			labels:        map[string]string{"thulu.engine": "claude-code"},
			wantEngine:    "claude-code",
			wantProjectID: "",
		},
		{
			name:          "thulu.project_id takes precedence over thulu.project",
			labels:        map[string]string{"thulu.project_id": "stable-id", "thulu.project": "display-name"},
			wantEngine:    engine.DefaultName,
			wantProjectID: "stable-id",
		},
		{
			name:          "whitespace-only treated as empty",
			labels:        map[string]string{"thulu.engine": "   ", "thulu.project_id": "\t", "thulu.project": "fallback"},
			wantEngine:    engine.DefaultName,
			wantProjectID: "fallback",
		},
		{
			name:          "no labels at all",
			labels:        map[string]string{},
			wantEngine:    engine.DefaultName,
			wantProjectID: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEng, gotPID := resolveLabels(tt.labels)
			if gotEng != tt.wantEngine {
				t.Errorf("engine = %q, want %q", gotEng, tt.wantEngine)
			}
			if gotPID != tt.wantProjectID {
				t.Errorf("projectID = %q, want %q", gotPID, tt.wantProjectID)
			}
		})
	}
}

func TestContainerToAgent_LabelFallback(t *testing.T) {
	c := dtypes.Container{
		ID:    "abc123def456789012345",
		Names: []string{"/legacy"},
		State: "exited",
		Labels: map[string]string{
			"thulu.type":    "developer",
			"thulu.project": "task-bbbb000000000004",
		},
	}
	a := containerToAgent(c)
	if a.Engine != engine.DefaultName {
		t.Errorf("Engine = %q, want %q (registered default)", a.Engine, engine.DefaultName)
	}
	if a.ProjectID != "task-bbbb000000000004" {
		t.Errorf("ProjectID = %q, want %q (fell back to thulu.project)", a.ProjectID, "task-bbbb000000000004")
	}
	if a.Project != "task-bbbb000000000004" {
		t.Errorf("Project (display) = %q, want unchanged", a.Project)
	}
}

func TestInspectToAgent_LabelFallback(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	info := dtypes.ContainerJSON{
		ContainerJSONBase: &dtypes.ContainerJSONBase{
			ID:    "abc123def456789012345",
			Name:  "/legacy",
			State: &dtypes.ContainerState{StartedAt: ts},
		},
		Config: &dcontainer.Config{
			Labels: map[string]string{
				"thulu.type":    "developer",
				"thulu.project": "task-xyz",
			},
		},
	}
	a := inspectToAgent(info)
	if a.Engine != engine.DefaultName {
		t.Errorf("Engine = %q, want %q", a.Engine, engine.DefaultName)
	}
	if a.ProjectID != "task-xyz" {
		t.Errorf("ProjectID = %q, want %q", a.ProjectID, "task-xyz")
	}
}
