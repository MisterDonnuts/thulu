package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func commentsOf(t *testing.T, store *Store, taskID string) []Comment {
	t.Helper()
	comments, err := store.ListComments(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return comments
}

// A launch is the agent_run_started event; the discussion stays for people.
func TestLaunchLeavesOnlyTheEvent(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	d.launcher = newRecordingLauncher()
	tk := gatedTask(t, store)
	d.launchStage(t.Context(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})

	if got := commentsOf(t, store, tk.ID); len(got) != 0 {
		t.Errorf("a launch posted comments: %+v", got)
	}
	if len(eventsOf(t, store, tk.ID, EventAgentRunStarted)) != 1 {
		t.Error("the launch recorded no agent_run_started event")
	}
}

func stageRunWith(t *testing.T, exit int, files map[string]string) (*Store, string) {
	t.Helper()
	d, store, _ := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	instanceID := "developer-task-" + tk.ID + "-in_progress-1727500000000"
	outDir := filepath.Join(t.TempDir(), "runs", instanceID)
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d.postStageRun(context.Background(), tk.ID, types.StageInProgress, "developer", "ctr-a", instanceID, outDir, exit,
		postRunOpts{NoAdvance: true})
	return store, tk.ID
}

// A clean stage run hands off through its report and verdict: comment.md is not posted and the run
// end is the agent_run_finished event alone.
func TestCleanStageRunPostsNoComment(t *testing.T) {
	store, id := stageRunWith(t, 0, map[string]string{
		"comment.md": "Done: 7 locked tests pass.",
		"_index.md":  "## Report",
	})
	if got := commentsOf(t, store, id); len(got) != 0 {
		t.Errorf("a clean stage run posted comments: %+v", got)
	}
	if len(eventsOf(t, store, id, EventAgentRunFinished)) != 1 {
		t.Error("the run end recorded no agent_run_finished event")
	}
}

// A person has to act on a run that failed, so it still says so in the discussion.
func TestFailedStageRunStillComments(t *testing.T) {
	store, id := stageRunWith(t, 2, map[string]string{"comment.md": "half done"})
	var keys []string
	for _, c := range commentsOf(t, store, id) {
		if c.Source == SourceAgent {
			t.Errorf("a failed stage run posted its comment.md: %q", c.Body)
		}
		keys = append(keys, c.Key)
	}
	if !strings.Contains(strings.Join(keys, ","), "task.comments.run_finished") {
		t.Errorf("a failed run left no run note; comment keys = %v", keys)
	}
	if len(eventsOf(t, store, id, EventAgentRunFinished)) != 1 {
		t.Error("the failed run recorded no agent_run_finished event")
	}
}

func TestRunnerWarningIsAnEvent(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	tk := gatedTask(t, store)
	d.RecordRunnerReport(t.Context(), tk.ID, RunnerReport{
		Runner: "mac-mini", RunID: "runner-42", Warnings: []string{"video.webm skipped: over the upload budget"},
	})
	if got := commentsOf(t, store, tk.ID); len(got) != 0 {
		t.Errorf("a runner warning posted comments: %+v", got)
	}
	evs := eventsOf(t, store, tk.ID, EventRunnerWarning)
	if len(evs) != 1 {
		t.Fatalf("runner_warning events = %d, want 1", len(evs))
	}
	if evs[0]["run_id"] != "runner-42" || evs[0]["message"] != "video.webm skipped: over the upload budget" {
		t.Errorf("payload = %+v", evs[0])
	}
}

func TestUncheckedLockedTestsAreAnEvent(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	tk := gatedTask(t, store)
	d.warnUnchecked(t.Context(), tk, []string{"proj-1"})
	if got := commentsOf(t, store, tk.ID); len(got) != 0 {
		t.Errorf("unchecked locked tests posted comments: %+v", got)
	}
	all, err := store.ListEvents(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range all {
		found = found || (e.Kind == EventLockedTestsUnchecked && strings.Contains(e.Summary, "runs on a runner"))
	}
	if !found {
		t.Errorf("no readable locked_tests_unchecked event in %+v", all)
	}
}
