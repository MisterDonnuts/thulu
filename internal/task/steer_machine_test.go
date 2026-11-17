package task

import (
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// machineSteerer steers only the runs placed on a machine, as the routed launcher does on Kubernetes.
type machineSteerer struct{ *recordingLauncher }

func (machineSteerer) SteerRun(context.Context, string, []byte) error { return nil }

func (machineSteerer) CanSteer(env map[string]string) bool { return env[runner.RunnerIDEnv] != "" }

// Case 37: a Claude Code run on a machine is armed for mid-run messages even when containers could
// not take them; a container run on the same launcher is not.
func TestMachineRunIsArmedForSteering(t *testing.T) {
	f := newMachineFixture(t)
	l := machineSteerer{f.l}
	f.d.launcher = l
	at := &types.AgentType{Name: "dev", Type: "dev", ValidStages: []types.Stage{types.StageInProgress}}

	f.d.WithLLMResolver(resolverReturning(machineProfile(), nil), nil, nil)
	f.d.launchStage(t.Context(), f.task(t), types.StageInProgress, "dev", at, launchOpts{})
	f.d.WithLLMResolver(resolverReturning(secrets.ResolvedLLM{Source: "binding", ProfileID: "p2",
		Engine: engine.DefaultName, Credential: "sk-ant-api-x"}, nil), nil, nil)
	f.d.launchStage(t.Context(), f.task(t), types.StageInProgress, "dev", at, launchOpts{})

	launches := f.l.launches()
	if len(launches) != 2 {
		t.Fatalf("launches = %d, want 2", len(launches))
	}
	onMachine, inContainer := launches[0], launches[1]
	if onMachine[runner.RunnerIDEnv] != "a" || onMachine["ENGINE"] != engine.DefaultName || onMachine["STEER_ENABLED"] != "1" {
		t.Errorf("machine run: runner %q, engine %q, steer %q; want armed on a", onMachine[runner.RunnerIDEnv],
			onMachine["ENGINE"], onMachine["STEER_ENABLED"])
	}
	if _, armed := f.d.steerable.Load("ctr-a"); !armed {
		t.Error("the dispatcher did not remember the machine run as steerable")
	}
	if inContainer[runner.RunnerIDEnv] != "" || inContainer["STEER_ENABLED"] == "1" {
		t.Errorf("container run: runner %q, steer %q; want it unarmed", inContainer[runner.RunnerIDEnv], inContainer["STEER_ENABLED"])
	}
}
