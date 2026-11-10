package launchercontract

import (
	"context"
	"fmt"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type FakeLauncher struct {
	mu         sync.Mutex
	agents     map[string]*types.AgentType
	containers map[string]*fakeContainer
}

type fakeContainer struct {
	instanceID string
	agentType  string
	outputDir  string
	alive      bool
	exited     bool
	reaped     bool
	exitCode   int
	onExit     func(int, string, string)
}

var _ task.AgentLauncher = (*FakeLauncher)(nil)

func NewFake() (*FakeLauncher, Driver) {
	at := &types.AgentType{
		Name:        "contract-agent",
		Type:        "developer",
		ValidStages: []types.Stage{types.StageInProgress, types.StageMention},
	}
	f := &FakeLauncher{
		agents:     map[string]*types.AgentType{at.Name: at},
		containers: map[string]*fakeContainer{},
	}
	return f, fakeDriver{f: f, at: at}
}

func (f *FakeLauncher) FindAgentType(_, name string) *types.AgentType {
	f.mu.Lock()
	defer f.mu.Unlock()
	if at := f.agents[name]; at != nil {
		cp := *at
		return &cp
	}
	return nil
}

func (f *FakeLauncher) LaunchForStage(
	_ context.Context,
	_ *task.Task,
	_ task.Stage,
	at *types.AgentType,
	env map[string]string,
	_ map[string]string,
	_ map[string]string,
	_ bool,
) (containerID, instanceID, outputDir string, err error) {
	instanceID = env["INSTANCE_ID"]
	if instanceID == "" {
		return "", "", "", fmt.Errorf("launchercontract fake: env missing INSTANCE_ID") //i18n:ignore test fake, never reaches a user
	}
	containerID = "fake-" + instanceID
	outputDir = "/fake/runs/" + instanceID
	f.mu.Lock()
	f.containers[containerID] = &fakeContainer{
		instanceID: instanceID,
		agentType:  at.Name,
		outputDir:  outputDir,
		alive:      true,
	}
	f.mu.Unlock()
	return containerID, instanceID, outputDir, nil
}

func (f *FakeLauncher) OnExit(_ context.Context, containerID string, cb func(exitCode int, outputDir, worktreePath string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.containers[containerID]; c != nil {
		c.onExit = cb
	}
}

func (f *FakeLauncher) ContainerAlive(containerID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[containerID]
	return c != nil && c.alive
}

func (f *FakeLauncher) ScanExitedTaskAgents(_ context.Context) ([]task.ExitedTaskAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []task.ExitedTaskAgent
	for cid, c := range f.containers {
		if !c.exited || c.reaped {
			continue
		}
		out = append(out, task.ExitedTaskAgent{
			ContainerID: cid,
			InstanceID:  c.instanceID,
			AgentType:   c.agentType,
			OutputDir:   c.outputDir,
			ExitCode:    c.exitCode,
		})
	}
	return out, nil
}

func (f *FakeLauncher) forceExit(containerID string, code int) {
	f.mu.Lock()
	c := f.containers[containerID]
	if c == nil || c.exited {
		f.mu.Unlock()
		return
	}
	c.alive = false
	c.exited = true
	c.exitCode = code
	cb, outDir := c.onExit, c.outputDir
	if cb != nil {
		c.reaped = true
	}
	f.mu.Unlock()

	if cb != nil {
		cb(code, outDir, "")
	}
}

type fakeDriver struct {
	f  *FakeLauncher
	at *types.AgentType
}

func (d fakeDriver) AgentType() *types.AgentType         { return d.at }
func (d fakeDriver) ForceExit(containerID string, c int) { d.f.forceExit(containerID, c) }
