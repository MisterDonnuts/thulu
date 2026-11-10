package types

import "testing"

func TestLaunchLabelsCarryTheAttribution(t *testing.T) {
	labels := buildLaunchLabels(&ComposeServiceSpec{
		Labels: map[string]string{"thulu.agent": "true", "thulu.type": "architect"},
	}, LaunchParams{
		Env: map[string]string{
			"INSTANCE_ID":  "architect-task-0000000000000c01-refinement-1789852266148",
			"TASK_ID":      "0000000000000c01",
			"PROJECT_ID":   "0000000000000b01",
			"STAGE":        "refinement",
			"WORKSPACE_ID": "0000000000000a01",
		},
	})

	want := map[string]string{
		"thulu.agent":        "true",
		"thulu.type":         "architect",
		"thulu.instance":     "architect-task-0000000000000c01-refinement-1789852266148",
		"thulu.task":         "0000000000000c01",
		"thulu.project_id":   "0000000000000b01",
		"thulu.stage":        "refinement",
		"thulu.workspace_id": "0000000000000a01",
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, labels[k], v)
		}
	}
}

func TestLaunchLabelsSkipWhatTheRunDoesNotHave(t *testing.T) {
	labels := buildLaunchLabels(&ComposeServiceSpec{}, LaunchParams{
		Env: map[string]string{"INSTANCE_ID": "architect-adhoc-1"},
	})
	if labels["thulu.instance"] != "architect-adhoc-1" {
		t.Errorf("thulu.instance = %q", labels["thulu.instance"])
	}
	if _, ok := labels["thulu.task"]; ok {
		t.Errorf("a run with no TASK_ID must carry no thulu.task label, got %q", labels["thulu.task"])
	}
}

func TestLaunchLabelsNameTheEngine(t *testing.T) {
	labels := buildLaunchLabels(&ComposeServiceSpec{}, LaunchParams{Env: map[string]string{"ENGINE": "codex"}})
	if labels["thulu.engine"] != "codex" {
		t.Fatalf("thulu.engine = %q, want codex: usage parses the run's log with this engine", labels["thulu.engine"])
	}
}
