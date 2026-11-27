package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

// queueBlockedAndFresh leaves TC-1 queued after a blocked run and TC-2 queued without any run.
func queueBlockedAndFresh(t *testing.T, s *Store, taskID string) {
	t.Helper()
	if _, _, err := s.RecordTestCaseRun(t.Context(), taskID, "TC-1", TestCaseRun{Verdict: TestCaseBlocked}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueTestCases(t.Context(), taskID, []string{"TC-1", "TC-2"}, "alice"); err != nil {
		t.Fatal(err)
	}
}

func wantStatuses(t *testing.T, s *Store, taskID string, want map[string]string) {
	t.Helper()
	for key, status := range want {
		if c, err := s.GetTestCase(t.Context(), taskID, key); err != nil || c.Status != status {
			t.Errorf("%s = %v %v, want %s", key, c, err, status)
		}
	}
}

// A queued case goes back to the verdict of its newest run, pending when it never ran, once the task
// leaves the stage the access was given in. Redoing the same column keeps the queue.
func TestReleaseQueuedTestCases(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "release")
	addCases(t, s, tk.ID, TestCase{Title: "a"}, TestCase{Title: "b"})
	queueBlockedAndFresh(t, s, tk.ID)

	if n := s.ReleaseQueuedTestCases(t.Context(), tk.ID, "octopus"); n != 2 {
		t.Errorf("released %d, want 2", n)
	}
	wantStatuses(t, s, tk.ID, map[string]string{"TC-1": TestCaseBlocked, "TC-2": TestCasePending})

	if _, _, err := s.QueueTestCases(t.Context(), tk.ID, []string{"TC-1"}, "alice"); err != nil {
		t.Fatal(err)
	}
	move := func(from, to string) {
		t.Helper()
		if err := s.RecordEvent(t.Context(), tk.ID, EventStageEntered, "", "user", "", map[string]string{"from": from, "to": to}); err != nil {
			t.Fatal(err)
		}
	}
	move("testing", "testing")
	wantStatuses(t, s, tk.ID, map[string]string{"TC-1": TestCaseQueued})
	move("testing", "code")
	wantStatuses(t, s, tk.ID, map[string]string{"TC-1": TestCaseBlocked})
}

// The run a person's access relaunched ends without running a queued case, or never starts: the case
// leaves the queue instead of reading "Running again" forever.
func TestQueuedCasesLeaveTheQueueWhenTheRunEnds(t *testing.T) {
	t.Run("the run ended", func(t *testing.T) {
		d, store, _ := newConvDispatcher(t)
		tk := &Task{WorkspaceID: testWS, Title: "ended", Stage: types.StageInProgress}
		if err := store.Create(t.Context(), tk); err != nil {
			t.Fatal(err)
		}
		addCases(t, store, tk.ID, TestCase{Title: "a"}, TestCase{Title: "b"})
		queueBlockedAndFresh(t, store, tk.ID)
		instanceID := "developer-task-" + tk.ID + "-in_progress-1727500000000"
		outDir := filepath.Join(t.TempDir(), "runs", instanceID)
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			t.Fatal(err)
		}
		d.postStageRun(context.Background(), tk.ID, types.StageInProgress, "developer", "ctr-a", instanceID, outDir, 0,
			postRunOpts{NoAdvance: true, NoAudit: true})
		wantStatuses(t, store, tk.ID, map[string]string{"TC-1": TestCaseBlocked, "TC-2": TestCasePending})
	})
	t.Run("the run never started", func(t *testing.T) {
		d, store, _ := newConvDispatcher(t)
		tk := &Task{WorkspaceID: testWS, Title: "not started", Stage: types.StageInProgress}
		if err := store.Create(t.Context(), tk); err != nil {
			t.Fatal(err)
		}
		addCases(t, store, tk.ID, TestCase{Title: "a"}, TestCase{Title: "b"})
		queueBlockedAndFresh(t, store, tk.ID)
		err := d.runNotStarted(context.Background(), tk, tk.Stage, "developer", launchOpts{Kind: LaunchStage}, CauseRuntime,
			Note{}, errors.New("no runtime"))
		var ns *NotStartedError
		if !errors.As(err, &ns) {
			t.Fatalf("runNotStarted = %v", err)
		}
		wantStatuses(t, store, tk.ID, map[string]string{"TC-1": TestCaseBlocked, "TC-2": TestCasePending})
	})
}
