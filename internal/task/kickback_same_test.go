package task

import (
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/types"
)

// A launcher that knows one agent type, so entering a stage launches it.
type knownAgentLauncher struct {
	*recordingLauncher
	agent *types.AgentType
}

func (l *knownAgentLauncher) FindAgentType(_, name string) *types.AgentType {
	if name == l.agent.Name {
		return l.agent
	}
	return nil
}

func hasCommentKey(t *testing.T, store *Store, taskID, key string) bool {
	t.Helper()
	full, err := store.Get(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range full.Comments {
		if c.Key == key {
			return true
		}
	}
	return false
}

// The auditor finds a defect the stage's own agent can fix: kick_back to its own stage runs the
// stage again, with the notes in the discussion, and counts against max_kick_backs.
func TestAuditorKickBackToItsOwnStageRedoesItAndCounts(t *testing.T) {
	d, store := newKickDispatcher(t, 1)
	l := &knownAgentLauncher{recordingLauncher: newRecordingLauncher(),
		agent: &types.AgentType{Name: "dev", ValidStages: []types.Stage{"code"}}}
	d.launcher = l
	tk := &Task{WorkspaceID: testWS, Title: "x", Stage: "code", StageAssignments: map[Stage]string{"code": "dev"}}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	tk, _ = store.Get(t.Context(), tk.ID)

	after := auditorKickBack(t, d, store, tk, "code")
	if after.Stage != "code" || after.Status == StatusBlocked {
		t.Fatalf("own-stage kick_back: %q/%q, want code and not blocked", after.Stage, after.Status)
	}
	moved := eventsOf(t, store, tk.ID, EventStageEntered)
	if len(moved) != 1 || moved[0]["from"] != "code" || moved[0]["to"] != "code" || moved[0]["via"] != ViaKickBack {
		t.Errorf("stage_entered = %v, want code → code via kick_back", moved)
	}
	if !hasCommentKey(t, store, tk.ID, "task.comments.auditor_kick_back_same") {
		t.Error("no comment says the stage is redone")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(l.launchedTasks()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := l.launchedTasks(); len(got) != 1 || got[0] != tk.ID {
		t.Fatalf("launches after the own-stage kick_back = %v, want one for the task", got)
	}

	// It is a return like any other: the second one over a cap of 1 blocks.
	tk, _ = store.Get(t.Context(), tk.ID)
	again := auditorKickBack(t, d, store, tk, "code")
	if again.Stage != "code" || again.Status != StatusBlocked {
		t.Errorf("second own-stage kick_back over a cap of 1: %q/%q, want code/blocked", again.Stage, again.Status)
	}
}

// A change to CI or gate config goes to a person under its own reason, not under "other".
func TestStageVerdictBlockKeepsTheGateConfigReason(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "verify", "")
	dir := writeVerdict(t, "---\nverdict: block\nreason: gate_config_changed\n---\n.github/workflows/ci.yml drops the lint job.\n")
	after := runVerdict(t, d, store, tk, auditedRun{Agent: "qa", Instance: "qa-task-" + tk.ID + "-verify-1", OutputDir: dir})
	if after.Stage != "verify" || after.Status != StatusBlocked {
		t.Fatalf("block verdict: %q/%q", after.Stage, after.Status)
	}
	verdicts := eventsOf(t, store, tk.ID, EventStageVerdict)
	if len(verdicts) != 1 || verdicts[0]["reason"] != "gate_config_changed" {
		t.Errorf("stage_verdict events = %v, want reason gate_config_changed", verdicts)
	}
}

// The stage agent's own verdict never redoes its stage: there kick_back is for earlier stages.
func TestAgentVerdictToItsOwnStageGoesBack(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "verify", "")
	v := stageVerdict{Verdict: verdictKickBack, KickBackTo: "verify", Source: verdictFromAgent, Agent: "qa"}
	if !d.applyVerdict(t.Context(), tk, tk.ID, "verify", v) {
		t.Fatal("a kick_back verdict must end the post-run flow")
	}
	if after, _ := store.Get(t.Context(), tk.ID); after.Stage != "code" {
		t.Errorf("agent verdict to its own stage landed in %q, want code", after.Stage)
	}
}
