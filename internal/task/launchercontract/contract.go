package launchercontract

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type Driver interface {
	AgentType() *types.AgentType

	ForceExit(containerID string, code int)
}

const (
	contractStage = types.StageInProgress
	contractTask  = "c0ntract7"
)

const callbackTimeout = 10 * time.Second

func RunSuite(t *testing.T, l task.AgentLauncher, d Driver) {
	t.Helper()
	at := d.AgentType()
	if at == nil || at.Name == "" {
		t.Fatalf("Driver.AgentType returned an unusable agent: %+v", at) //i18n:ignore test assertion
	}
	t.Run("FindAgentType", func(t *testing.T) { testFindAgentType(t, l, at) })
	t.Run("LaunchThenOnExitFiresOnce", func(t *testing.T) { testOnExitOnce(t.Context(), t, l, d, at) })
	t.Run("ScanExitedReportsReconcilableRun", func(t *testing.T) { testScanReconcile(t, l, d, at) })
}

const Workspace = "ws-contract"

func testFindAgentType(t *testing.T, l task.AgentLauncher, at *types.AgentType) {
	if got := l.FindAgentType(Workspace, at.Name); got == nil {
		t.Fatalf("FindAgentType(%q) = nil, want the driver's agent", at.Name) //i18n:ignore test assertion
	}
	if got := l.FindAgentType(Workspace, "no-such-agent-xyz"); got != nil {
		t.Errorf("FindAgentType(unknown) = %+v, want nil", got) //i18n:ignore test assertion
	}
}

func testOnExitOnce(ctx context.Context, t *testing.T, l task.AgentLauncher, d Driver, at *types.AgentType) {
	cid, instanceID := launch(t, l, at)

	if !l.ContainerAlive(cid) {
		t.Errorf("ContainerAlive(%q) = false right after launch, want true", cid) //i18n:ignore test assertion
	}

	exits := make(chan int, 4)
	var gotOutDir string
	l.OnExit(ctx, cid, func(code int, outputDir, _ string) {
		gotOutDir = outputDir
		exits <- code
	})
	d.ForceExit(cid, 7)

	select {
	case code := <-exits:
		if code != 7 {
			t.Errorf("OnExit code = %d, want 7", code) //i18n:ignore test assertion
		}
	case <-time.After(callbackTimeout):
		t.Fatalf("OnExit did not fire within %s (instance %q)", callbackTimeout, instanceID) //i18n:ignore test assertion
	}
	if gotOutDir == "" {
		t.Errorf("OnExit outputDir empty, want non-empty") //i18n:ignore test assertion
	}

	select {
	case code := <-exits:
		t.Errorf("OnExit fired more than once (second code=%d)", code) //i18n:ignore test assertion
	case <-time.After(500 * time.Millisecond):
	}

	if l.ContainerAlive(cid) {
		t.Errorf("ContainerAlive(%q) = true after exit, want false", cid) //i18n:ignore test assertion
	}
}

func testScanReconcile(t *testing.T, l task.AgentLauncher, d Driver, at *types.AgentType) {
	// No OnExit here: a run whose OnExit fired is reaped and never reported by
	// ScanExitedTaskAgents.
	cid, instanceID := launch(t, l, at)
	d.ForceExit(cid, 5)

	exited := eventuallyScan(t, l, instanceID)
	if exited.ExitCode != 5 {
		t.Errorf("ExitedTaskAgent.ExitCode = %d, want 5", exited.ExitCode) //i18n:ignore test assertion
	}
	if exited.OutputDir == "" {
		t.Errorf("ExitedTaskAgent.OutputDir empty, want non-empty") //i18n:ignore test assertion
	}
	if exited.AgentType == "" {
		t.Errorf("ExitedTaskAgent.AgentType empty, want the launched agent") //i18n:ignore test assertion
	}
}

func launch(t *testing.T, l task.AgentLauncher, at *types.AgentType) (containerID, instanceID string) {
	t.Helper()
	instanceID = fmt.Sprintf("%s-task-%s-%s-%d", at.Name, contractTask, contractStage, time.Now().UnixMilli())
	env := map[string]string{"INSTANCE_ID": instanceID}
	tk := &task.Task{ID: contractTask, Stage: contractStage, WorkspaceID: Workspace}

	cid, gotInstance, outDir, err := l.LaunchForStage(t.Context(), tk, contractStage, at, env, nil, nil, false)
	if err != nil {
		t.Fatalf("LaunchForStage: %v", err) //i18n:ignore test assertion
	}
	if cid == "" || gotInstance == "" || outDir == "" {
		//i18n:ignore test assertion
		t.Fatalf("LaunchForStage returned an empty id: containerID=%q instanceID=%q outputDir=%q", cid, gotInstance, outDir)
	}
	if gotInstance != instanceID {
		t.Errorf("returned instanceID = %q, want the minted %q", gotInstance, instanceID) //i18n:ignore test assertion
	}
	return cid, instanceID
}

func eventuallyScan(t *testing.T, l task.AgentLauncher, instanceID string) task.ExitedTaskAgent {
	t.Helper()
	deadline := time.Now().Add(callbackTimeout)
	for {
		exited, err := l.ScanExitedTaskAgents(t.Context())
		if err != nil {
			t.Fatalf("ScanExitedTaskAgents: %v", err) //i18n:ignore test assertion
		}
		for _, e := range exited {
			if e.InstanceID == instanceID {
				return e
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("ScanExitedTaskAgents never reported instance %q (waited %s); got %+v", //i18n:ignore test assertion
				instanceID, callbackTimeout, exited)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
