package task

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// rearmLauncher reports live runs as a restarted daemon finds them, and records kills and canceled
// watches.
type rearmLauncher struct {
	*recordingLauncher
	live     []LiveTaskAgent
	km       sync.Mutex
	killed   []string
	canceled []string
}

func (l *rearmLauncher) ScanLiveTaskAgents(context.Context) ([]LiveTaskAgent, error) {
	return l.live, nil
}

func (l *rearmLauncher) KillRun(_ context.Context, id string) error {
	l.km.Lock()
	defer l.km.Unlock()
	l.killed = append(l.killed, id)
	return nil
}

func (l *rearmLauncher) CancelExitWatch(id string) {
	l.km.Lock()
	defer l.km.Unlock()
	l.canceled = append(l.canceled, id)
}

func (l *rearmLauncher) kills() []string {
	l.km.Lock()
	defer l.km.Unlock()
	return slices.Clone(l.killed)
}

func TestRearmLiveRunsGivesBackExitsAndDeadlines(t *testing.T) {
	_, store, _ := newMentionDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "restarted"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	run := func(ctr string, started time.Time) LiveTaskAgent {
		return LiveTaskAgent{
			ExitedTaskAgent: ExitedTaskAgent{ContainerID: ctr, InstanceID: "dev-task-" + tk.ID + "-in_progress-" + ctr[len(ctr)-1:], AgentType: "dev"},
			StartedAt:       started,
		}
	}
	base := newRecordingLauncher()
	l := &rearmLauncher{recordingLauncher: base, live: []LiveTaskAgent{
		run("ctr-1", time.Now().Add(-2*time.Hour)),                                        // past its hour: killed now
		run("ctr-2", time.Now().Add(-time.Minute)),                                        // within its hour: watched, not killed
		run("ctr-3", time.Now().Add(-time.Minute)),                                        // exited between scan and watch
		{ExitedTaskAgent: ExitedTaskAgent{ContainerID: "ctr-4", InstanceID: "not-a-run"}}, // not a task run
	}}
	base.alive["ctr-1"] = true
	base.alive["ctr-2"] = true
	d := NewDispatcher(store, l, nil, nil, nil).WithRunDeadline(func(string) time.Duration { return time.Hour })

	d.RearmLiveRuns(context.Background())

	waitFor(t, func() bool { return slices.Equal(l.kills(), []string{"ctr-1"}) })
	base.mu.Lock()
	_, watched1 := base.exits["ctr-1"]
	_, watched2 := base.exits["ctr-2"]
	_, watched4 := base.exits["ctr-4"]
	base.mu.Unlock()
	if !watched1 || !watched2 || watched4 {
		t.Fatalf("exit callbacks: ctr-1=%v ctr-2=%v ctr-4=%v; want the two live task runs only", watched1, watched2, watched4)
	}
	l.km.Lock()
	canceled := slices.Clone(l.canceled)
	l.km.Unlock()
	if !slices.Equal(canceled, []string{"ctr-3"}) {
		t.Fatalf("canceled watches %v: want the run that died before its watch", canceled)
	}
	time.Sleep(50 * time.Millisecond)
	if got := l.kills(); !slices.Equal(got, []string{"ctr-1"}) {
		t.Fatalf("kills %v: a run inside its deadline was killed", got)
	}
}
