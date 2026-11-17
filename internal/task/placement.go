package task

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// RunnerLookup reads what the daemon knows of a registered machine. An empty workspace skips the
// workspace check.
type RunnerLookup interface {
	Get(ctx context.Context, workspaceID, id string) (*runner.Runner, error)
}

// Without it every run that needs a machine is refused: runners are off on this daemon.
func (d *Dispatcher) WithRunners(r RunnerLookup) *Dispatcher {
	d.runners = r
	return d
}

// placement is everything that decides where a run goes, gathered so decide stays pure.
type placement struct {
	// The machines the task's projects live on, distinct and in task order.
	projectRunners []string
	// A project whose code lives on a machine the daemon no longer knows: it must not run in a
	// container with no code.
	lostProject   string
	profileRunner string
	profileName   string
	// What the daemon knows of each machine above; nil when runners are off, a missing entry when
	// the machine is unknown.
	runners map[string]*runner.Runner
}

// decide returns the machine a run goes to, or "" for a container. The task's projects choose
// first; a machine profile chooses when they don't. The machine must be able to take the run now:
// nothing is queued for a machine that would only let it expire.
func (p placement) decide() (string, error) {
	if p.lostProject != "" {
		return "", apperr.Precondition("task.errors.project_runner_lost", "project", p.lostProject)
	}
	if len(p.projectRunners) > 1 {
		return "", apperr.Precondition("task.errors.task_mixes_runners", "runners", p.names(p.projectRunners))
	}
	chosen := p.profileRunner
	if len(p.projectRunners) == 1 {
		chosen = p.projectRunners[0]
		if p.profileRunner != "" && p.profileRunner != chosen {
			return "", apperr.Precondition("task.errors.placement_conflict", "project_runner", p.name(chosen),
				"profile", p.profileName, "profile_runner", p.name(p.profileRunner))
		}
	}
	if chosen == "" {
		return "", nil
	}
	return chosen, p.ready(chosen)
}

func (p placement) ready(id string) error {
	if p.runners == nil {
		return apperr.Precondition("task.errors.runners_unavailable", "runner", id)
	}
	r := p.runners[id]
	switch {
	case r == nil:
		return apperr.Precondition("task.errors.runner_unknown", "runner", id)
	case r.RevokedAt != nil:
		return apperr.Precondition("task.errors.runner_revoked", "runner", r.Name)
	case !r.Online:
		return apperr.Precondition("task.errors.runner_offline", "runner", r.Name)
	}
	return nil
}

func (p placement) name(id string) string {
	if r := p.runners[id]; r != nil && r.Name != "" {
		return r.Name
	}
	return id
}

func (p placement) names(ids []string) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, p.name(id))
	}
	return strings.Join(out, ", ")
}

// placementFor gathers what places a run of t under resolved. A nil t (a chat without a task, a
// readiness check) is placed by the profile alone.
func (d *Dispatcher) placementFor(ctx context.Context, workspaceID string, t *Task, resolved secrets.ResolvedLLM) (placement, error) {
	p := placement{profileRunner: resolved.RunnerID, profileName: resolved.ProfileName}
	p.projectRunners, p.lostProject = d.taskRunners(ctx, t)
	if d.runners != nil {
		p.runners = map[string]*runner.Runner{}
		for _, id := range append(slices.Clone(p.projectRunners), p.profileRunner) {
			if id == "" || p.runners[id] != nil {
				continue
			}
			r, err := d.runners.Get(ctx, workspaceID, id)
			if apperr.KindOf(err) == apperr.KindNotFound {
				continue
			}
			if err != nil {
				return p, err
			}
			p.runners[id] = r
		}
	}
	return p, nil
}

func (d *Dispatcher) taskRunners(ctx context.Context, t *Task) (runners []string, lost string) {
	if t == nil {
		return nil, ""
	}
	for _, id := range t.ProjectIDs {
		p := d.runnerProject(ctx, id)
		switch {
		case p == nil || !p.RunsOnRunner():
		case p.RunnerID == "":
			lost = p.Name
		case !slices.Contains(runners, p.RunnerID):
			runners = append(runners, p.RunnerID)
		}
	}
	return runners, lost
}

// applyMachineRun points a run at its machine. The machine's CLI login pays, so no credential,
// endpoint or profile var is left in env. A machine profile also names the engine and model, as a
// key profile for another engine does; a key profile on a run its project put on a machine only
// lends its model.
func applyMachineRun(env map[string]string, runnerID string, resolved secrets.ResolvedLLM, agentType *types.AgentType, agentName string) {
	if resolved.RunnerID != "" {
		engineName := strings.TrimSpace(resolved.Engine)
		if engineName == "" {
			engineName = resolveEngineName(agentType)
		}
		if engineName != env["ENGINE"] {
			clearEngineKeyEnv(env, env["ENGINE"])
			applyEngineEnvForName(env, engineName, agentName)
		}
		applyResolvedModel(env, resolved.ForAgent(agentModelEffort(agentType)), engineName)
	} else {
		applyRunnerModel(env, resolved, agentType)
	}
	ClearMachineCredentials(env)
	env[runner.RunnerIDEnv] = runnerID
	env[engine.LLMSourceEnv] = engine.LLMSourceRunnerLogin
}

// ClearMachineCredentials removes from env every credential, endpoint and profile var of its engine:
// the machine's own login pays, and a key must never travel to a machine.
func ClearMachineCredentials(env map[string]string) {
	eng, err := engine.Get(env["ENGINE"])
	if err != nil {
		return
	}
	for _, v := range engine.MachineLoginClears(eng.Capabilities()) {
		delete(env, v)
	}
}

// applyRunnerModel gives a run its project put on a machine the model and effort a key profile
// would: a profile bound to the agent first, else the agent's own, else the default profile's. A
// profile for another engine or provider names models the login can't run; those keep the agent's.
func applyRunnerModel(env map[string]string, resolved secrets.ResolvedLLM, agentType *types.AgentType) {
	engineName := cmp.Or(env["ENGINE"], engine.DefaultName)
	if resolved.Engine != "" && resolved.Engine != engineName {
		return
	}
	if resolved.Provider != "" {
		eng, err := engine.Get(engineName)
		if err != nil || resolved.Provider != eng.Capabilities().Provider {
			return
		}
	}
	applyResolvedModel(env, resolved.ForAgent(agentModelEffort(agentType)), engineName)
}

func applyResolvedModel(env map[string]string, resolved secrets.ResolvedLLM, engineName string) {
	if resolved.Model != "" {
		env["MODEL"] = resolved.Model
	}
	if resolved.Effort != "" {
		env["EFFORT"] = resolved.Effort
		if eng, err := engine.Get(engineName); err == nil && !eng.Capabilities().EffortFlag {
			delete(env, "EFFORT")
		}
	}
}

func onMachine(env map[string]string) bool { return env[runner.RunnerIDEnv] != "" }
