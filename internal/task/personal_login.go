package task

import (
	"context"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type triggerKey struct{}

func WithTriggeredBy(ctx context.Context, userID string) context.Context {
	if userID = strings.TrimSpace(userID); userID == "" {
		return ctx
	}
	return context.WithValue(ctx, triggerKey{}, userID)
}

func TriggeredBy(ctx context.Context) string {
	if id, ok := ctx.Value(triggerKey{}).(string); ok {
		return id
	}
	return ""
}

type PersonalLogins interface {
	Volume(ctx context.Context, userID, engineName string) (volume string, ok bool)
}

func (d *Dispatcher) WithPersonalLogins(p PersonalLogins) *Dispatcher {
	d.personalLogins = p
	return d
}

// The Docker launcher mounts this volume at the engine's ConfigDir and drops the var; the
// Kubernetes launcher refuses runs carrying it.
const EnvPersonalLoginVolume = "OCTOPUS_PERSONAL_LOGIN_VOLUME"

type personalRun struct {
	user   string
	engine string
	volume string
}

func (d *Dispatcher) personalLoginFor(ctx context.Context, workspaceID string, agentType *types.AgentType, agentName string) (personalRun, bool) {
	user := TriggeredBy(ctx)
	if d.personalLogins == nil || user == "" {
		return personalRun{}, false
	}
	engineName := d.runEngine(ctx, workspaceID, agentType, agentName)
	if engine.PersonalLoginOf(engineName) == nil {
		return personalRun{}, false
	}
	vol, ok := d.personalLogins.Volume(ctx, user, engineName)
	if !ok {
		return personalRun{}, false
	}
	return personalRun{user: user, engine: engineName, volume: vol}, true
}

func (d *Dispatcher) runEngine(ctx context.Context, workspaceID string, agentType *types.AgentType, agentName string) string {
	label := resolveEngineName(agentType)
	if d.llmResolver == nil || workspaceID == "" {
		return label
	}
	resolved, err := d.llmResolver(ctx, workspaceID, agentName, label)
	if err != nil || strings.TrimSpace(resolved.Engine) == "" {
		return label
	}
	return strings.TrimSpace(resolved.Engine)
}

// Every credential, endpoint and gateway var must be cleared: an env credential outranks the stored
// login and would bill someone else.
func applyPersonalLogin(env map[string]string, p personalRun) error {
	eng, err := engine.Get(p.engine)
	if err != nil {
		return err
	}
	caps := eng.Capabilities()
	if caps.PersonalLogin == nil {
		return apperr.Internal("task.errors.engine_without_personal_login", "engine", p.engine)
	}
	if env["ENGINE"] != p.engine {
		clearEngineKeyEnv(env, env["ENGINE"])
		applyEngineEnvForName(env, p.engine, env["AGENT_NAME"])
	}
	for _, v := range engine.PersonalLoginClears(caps) {
		delete(env, v)
	}
	env[caps.PersonalLogin.ConfigDirEnv] = caps.PersonalLogin.ConfigDir
	env[engine.LLMSourceEnv] = engine.LLMSourcePersonal
	env[EnvPersonalLoginVolume] = p.volume
	return nil
}

func (d *Dispatcher) applyRunLLM(ctx context.Context, env map[string]string, t *Task,
	agentType *types.AgentType, agentName string, p personalRun, isPersonal bool,
) error {
	if !isPersonal || d.taskRunsOnRunner(ctx, t) {
		return d.applyLLMAuth(ctx, env, t.WorkspaceID, agentType, agentName, t)
	}
	err := applyPersonalLogin(env, p)
	d.applyAgentEgressEnv(env)
	return err
}

// NODE_EXTRA_CA_CERTS is dropped because the login containers do not mount the corporate CA.
func (d *Dispatcher) LoginEgressEnv(engineName string) map[string]string {
	env := agentEgressEnv(os.Getenv, engineName, d.agentBaseURL, d.LLMGatewayURL())
	delete(env, "NODE_EXTRA_CA_CERTS")
	return env
}
