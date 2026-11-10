package task

import (
	"context"
	"log"
	"maps"
	"slices"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/tokenopt"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type TokenOptimizerWorker func(ctx context.Context, workspaceID, engine string) (tokenopt.WorkerInfo, string, error)

func (d *Dispatcher) WithTokenOptimizer(fn TokenOptimizerWorker) *Dispatcher {
	d.tokenOpt = fn
	return d
}

func (d *Dispatcher) ApplyTokenOptimizer(ctx context.Context, env map[string]string, workspaceID string, agentType *types.AgentType) {
	d.applyTokenOptimizer(ctx, env, workspaceID, agentType, string(types.StageChat))
}

// Call after the LLM credential is applied: the profile may switch the engine, and enforce needs
// one that runs hooks.
func (d *Dispatcher) applyTokenOptimizer(ctx context.Context, env map[string]string, workspaceID string, agentType *types.AgentType, stage string) {
	// The worker signs in with a workspace profile; runs on a runner's own login must get no
	// workspace credential.
	if onRunnerLogin(env) {
		return
	}
	if d.tokenOpt == nil || agentType == nil || !agentType.TokenOptimizer.On() {
		return
	}
	workerEngine := agentType.TokenOptimizer.Engine
	worker, oauth, err := d.tokenOpt(ctx, workspaceID, workerEngine)
	if err != nil {
		log.Printf("[dispatcher] %s", i18n.Log("tokenopt.log.worker_unreadable",
			"workspace", workspaceID, "engine", workerEngine, "err", err))
		return
	}
	res := tokenopt.Resolve(agentType, env["ENGINE"], worker)
	if !res.On() {
		return
	}
	maps.Copy(env, tokenopt.Env(res, oauth))
	// agentType is shared by every run of the agent; gate a copy.
	gated := *agentType
	gated.ActiveWhen = append(slices.Clone(agentType.ActiveWhen), types.SkillWhenTokenOptimizer)
	if skills := gated.DefaultSkills(stage); skills != "" {
		env["SKILLS"] = skills
	}
}
