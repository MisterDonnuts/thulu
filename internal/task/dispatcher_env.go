package task

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func resolveEngineName(agentType *types.AgentType) string {
	if agentType != nil && agentType.Engine != "" {
		return agentType.Engine
	}
	return engine.DefaultName
}

type LLMKeyResolver func(ctx context.Context, workspaceID, engine string) (key string, ok bool)

type LLMResolver func(ctx context.Context, workspaceID, agentName, fallbackEngine string) (secrets.ResolvedLLM, error)

type LLMProfileTouch func(ctx context.Context, workspaceID, profileID string)

type LLMProfileRefusal func(ctx context.Context, workspaceID, profileID string, status int)

// Implementations must record only metadata (profile id, source, engine), never the credential.
type LLMResolveAudit func(t *Task, agentName string, resolved secrets.ResolvedLLM)

// Call last in env construction so gated keys (EFFORT, MCP_TOOLS) are present when the gate
// inspects them.
func applyEngineEnv(env map[string]string, agentType *types.AgentType, agentName string) {
	applyEngineEnvForName(env, resolveEngineName(agentType), agentName)
}

func clearEngineKeyEnv(env map[string]string, name string) {
	if name == "" {
		return
	}
	eng, err := engine.Get(name)
	if err != nil {
		return
	}
	caps := eng.Capabilities()
	for _, k := range caps.KeyKinds {
		for _, v := range k.EnvVars {
			delete(env, v)
		}
		for _, v := range k.Clears {
			delete(env, v)
		}
	}
	for _, v := range []string{caps.BaseURLEnv, caps.OrgIDEnv, caps.ProviderEnv} {
		if v != "" {
			delete(env, v)
		}
	}
}

func applyEngineEnvForName(env map[string]string, name, agentName string) {
	if name == "" {
		name = engine.DefaultName
	}
	env["ENGINE"] = name
	if _, ok := env["AUDITOR_ENGINE"]; ok {
		env["AUDITOR_ENGINE"] = name
	}

	eng, err := engine.Get(name)
	if err != nil {
		// Unregistered in Go: don't gate, the shell side may still know how to run it.
		log.Printf("[dispatcher] %s", i18n.Log("task.log.engine_not_registered", "agent", agentName, "engine", name))
		return
	}
	caps := eng.Capabilities()

	if v := env["EFFORT"]; v != "" && !caps.EffortFlag {
		log.Printf("[dispatcher] %s", i18n.Log("task.log.engine_drops_effort", "agent", agentName, "engine", name, "value", v))
		delete(env, "EFFORT")
	}
	if v := env["MCP_TOOLS"]; v != "" && !caps.MCP {
		log.Printf("[dispatcher] %s", i18n.Log("task.log.engine_drops_mcp", "agent", agentName, "engine", name, "value", v))
		delete(env, "MCP_TOOLS")
	}
	env["SKILLS_MODE"] = resolveSkillsMode(env["SKILLS_MODE"], caps.Skills)
	if env["SKILLS_MODE"] == types.SkillsModeNative && !caps.Skills {
		log.Printf("[dispatcher] %s", i18n.Log("task.log.engine_native_skills_unsupported", "agent", agentName, "engine", name))
	}
}

func resolveSkillsMode(declared string, engineHasSkills bool) string {
	switch declared {
	case types.SkillsModeInline, types.SkillsModeNative:
		return declared
	default:
		if engineHasSkills {
			return types.SkillsModeNative
		}
		return types.SkillsModeInline
	}
}

// Never copy the operator's master OCTOPUS_API_KEY into containers: agents run untrusted
// LLM-generated code and one prompt injection would get daemon admin.
// Run keys cap at developer role, are pinned to their task, and their scope is how the daemon later
// learns which agent and stage the run is.
func (d *Dispatcher) applyRunAPIKey(ctx context.Context, env map[string]string, t *Task, agentName string, stage Stage) (keyID string) {
	return d.mintRunKey(ctx, env, t, auth.RunScope{TaskID: t.ID, Agent: agentName, Stage: string(stage)})
}

func (d *Dispatcher) mintRunKey(ctx context.Context, env map[string]string, t *Task, scope auth.RunScope) (keyID string) {
	if d.keys == nil {
		if k := strings.TrimSpace(os.Getenv("OCTOPUS_API_KEY")); k != "" {
			env["OCTOPUS_API_KEY"] = k
		}
		return ""
	}
	raw, id, err := d.keys.MintRunKey(ctx, scope, t.WorkspaceID, runKeyTTL)
	if err != nil {
		log.Printf("[dispatcher] %s", i18n.Log("task.log.mint_run_key_failed", "task", t.ID, "err", err))
		return ""
	}
	env["OCTOPUS_API_KEY"] = raw
	return id
}

func (d *Dispatcher) revokeRunKey(ctx context.Context, keyID string) {
	if d.keys == nil || keyID == "" {
		return
	}
	logx.Warn(d.keys.RevokeAPIKey(ctx, keyID), "task.log.revoke_run_key", "key", keyID)
}

func (d *Dispatcher) applyBYOKKey(ctx context.Context, env map[string]string, workspaceID string, agentType *types.AgentType) bool {
	if d.llmKeys == nil || workspaceID == "" {
		return false
	}
	engineName := resolveEngineName(agentType)
	key, ok := d.llmKeys(ctx, workspaceID, engineName)
	if !ok || key == "" {
		return false
	}
	eng, err := engine.Get(engineName)
	if err != nil {
		env["LLM_API_KEY"] = key
		return true
	}
	caps := eng.Capabilities()
	kind, ok := engine.MatchKeyKind(caps.KeyKinds, key)
	if !ok {
		env["LLM_API_KEY"] = key
		return true
	}
	for _, v := range kind.EnvVars {
		env[v] = key
	}
	for _, v := range kind.Clears {
		delete(env, v)
	}
	return true
}

// Call after the run's OCTOPUS_API_KEY is in env: with the LLM gateway on, that key is what the CLI
// presents.
func (d *Dispatcher) ApplyLLMAuth(
	ctx context.Context, env map[string]string, workspaceID string, agentType *types.AgentType, agentName string,
) error {
	return d.applyLLMAuth(ctx, env, workspaceID, agentType, agentName, nil)
}

// A non-nil error means the run must not start: falling back to the operator's key would bill one
// tenant's work to another.
func (d *Dispatcher) applyLLMAuth(
	ctx context.Context, env map[string]string, workspaceID string,
	agentType *types.AgentType, agentName string, auditTask *Task,
) error {
	if d.taskRunsOnRunner(ctx, auditTask) {
		applyRunnerLogin(env)
		return nil
	}
	err := d.applyLLMCredential(ctx, env, workspaceID, agentType, agentName, auditTask)
	d.applyAgentEgressEnv(env)
	return err
}

func (d *Dispatcher) applyLLMCredential(
	ctx context.Context, env map[string]string, workspaceID string,
	agentType *types.AgentType, agentName string, auditTask *Task,
) error {
	return d.resolveLLMCredential(ctx, env, workspaceID, agentType, agentName, auditTask, true)
}

func (d *Dispatcher) LLMCredentialReady(ctx context.Context, workspaceID string, agentType *types.AgentType, agentName string) error {
	env := map[string]string{"ENGINE": resolveEngineName(agentType)}
	return d.resolveLLMCredential(ctx, env, workspaceID, agentType, agentName, nil, false)
}

func (d *Dispatcher) resolveLLMCredential(
	ctx context.Context, env map[string]string, workspaceID string,
	agentType *types.AgentType, agentName string, auditTask *Task, touch bool,
) error {
	if err := d.resolveLLMCredentialFrom(ctx, env, workspaceID, agentType, agentName, auditTask, touch); err != nil {
		return err
	}
	return refuseUnusableKinds(env)
}

// refuseUnusableKinds stops a run whose resolved credential is a kind this runtime does not allow
// (a Claude subscription token in Kubernetes without the operator's opt-in), wherever it came from:
// a stored key, a profile or the host's env.
func refuseUnusableKinds(env map[string]string) error {
	eng, err := engine.Get(env["ENGINE"])
	if err != nil {
		return nil
	}
	for _, k := range eng.Capabilities().KeyKinds {
		if k.Usable() {
			continue
		}
		for _, v := range k.EnvVars {
			if env[v] == "" {
				continue
			}
			for _, w := range k.EnvVars {
				delete(env, w)
			}
			return apperr.Precondition("task.errors.kind_needs_kube_opt_in", "kind", k.Label, "env", k.KubeOptIn)
		}
	}
	return nil
}

func (d *Dispatcher) resolveLLMCredentialFrom(
	ctx context.Context, env map[string]string, workspaceID string,
	agentType *types.AgentType, agentName string, auditTask *Task, touch bool,
) error {
	fallback := resolveEngineName(agentType)
	if d.llmResolver == nil {
		if d.applyBYOKKey(ctx, env, workspaceID, agentType) {
			return nil
		}
		return d.hostCredential(fallback, workspaceID, agentName)
	}
	if workspaceID == "" {
		return d.hostCredential(fallback, workspaceID, agentName)
	}
	resolved, err := d.llmResolver(ctx, workspaceID, llmBindingAgent(agentType, agentName), fallback)
	if err != nil {
		// Fail instead of falling back to host env: that would hand the operator's key to a
		// workspace whose own key merely failed to load.
		log.Printf("[dispatcher] %s", i18n.Log("task.log.resolve_llm_failed", "workspace", workspaceID, "agent", agentName, "err", err))
		return apperr.Wrap(err, "task.errors.resolve_llm_profile", "agent", agentName)
	}
	resolved = resolved.ForAgent(agentModelEffort(agentType))
	env[engine.LLMSourceEnv] = resolved.Source
	engineName := strings.TrimSpace(resolved.Engine)
	if engineName == "" {
		engineName = fallback
	}
	if engineName != env["ENGINE"] {
		// Clear the previous engine's key vars first, or a stale ANTHROPIC_API_KEY leaks into a run
		// routed to another engine.
		clearEngineKeyEnv(env, env["ENGINE"])
		applyEngineEnvForName(env, engineName, agentName)
	}
	if resolved.Model != "" {
		env["MODEL"] = resolved.Model
	}
	if resolved.Effort != "" {
		env["EFFORT"] = resolved.Effort
		if eng, gerr := engine.Get(engineName); gerr == nil && !eng.Capabilities().EffortFlag {
			delete(env, "EFFORT")
		}
	}
	if resolved.Source == "host" {
		return d.hostCredential(engineName, workspaceID, agentName)
	}
	if err := d.routeResolved(ctx, env, engineName, workspaceID, resolved, auditTask != nil, touch); err != nil {
		return err
	}
	if resolved.ProfileID != "" {
		env[engine.LLMProfileEnv] = resolved.ProfileID
	}
	if touch && d.llmProfTouch != nil && resolved.ProfileID != "" {
		d.llmProfTouch(ctx, workspaceID, resolved.ProfileID)
	}
	if d.llmAudit != nil && auditTask != nil {
		d.llmAudit(auditTask, agentName, resolved)
	}
	return nil
}

func llmBindingAgent(agentType *types.AgentType, agentName string) string {
	if agentType != nil && agentType.IgnoreLLMBinding {
		return ""
	}
	return agentName
}

func agentModelEffort(agentType *types.AgentType) (model, effort string) {
	if agentType == nil {
		return "", ""
	}
	return agentType.Model, agentType.Effort
}

func (d *Dispatcher) routeProfileCredential(env map[string]string, engineName string, resolved secrets.ResolvedLLM, taskRun bool) error {
	eng, err := engine.Get(engineName)
	if err != nil {
		if resolved.Credential == "" {
			return apperr.Precondition("task.errors.llm_profile_no_credential", "profile", resolved.ProfileName)
		}
		env["LLM_API_KEY"] = resolved.Credential
		return nil
	}
	caps := eng.Capabilities()
	if engine.IsCloudProvider(resolved.Provider) {
		if err := checkAmbientCloudIdentity(resolved); err != nil {
			return err
		}
		if d.llm.gatewayOn && caps.GatewayCanPresent(resolved.Provider, resolved.Credential, resolved.BaseURL) {
			return d.applyCloudGatewayEnv(env, caps, resolved, taskRun)
		}
		return applyCloudProfile(env, engineName, caps, resolved)
	}
	if resolved.Credential == "" {
		return apperr.Precondition("task.errors.llm_profile_no_credential", "profile", resolved.ProfileName)
	}
	if d.llm.gatewayOn && caps.GatewayCanPresent(resolved.Provider, resolved.Credential, resolved.BaseURL) {
		return d.applyGatewayEnv(env, caps, resolved.Provider, taskRun)
	}
	applyDirectProfileEnv(env, caps, resolved)
	return nil
}

func applyDirectProfileEnv(env map[string]string, caps engine.Capabilities, resolved secrets.ResolvedLLM) {
	if caps.ProviderEnv != "" {
		env[caps.ProviderEnv] = caps.ProfileProvider(resolved.Provider)
	}
	if resolved.BaseURL != "" && caps.BaseURLEnv != "" {
		env[caps.BaseURLEnv] = resolved.BaseURL
	}
	if resolved.OrgID != "" && caps.OrgIDEnv != "" {
		env[caps.OrgIDEnv] = resolved.OrgID
	}
	kind, ok := engine.MatchKeyKind(caps.KeyKinds, resolved.Credential)
	if resolved.BaseURL != "" {
		// A custom endpoint's key prefix means nothing; the profile's auth_header decides the env
		// var.
		kind, ok = caps.CustomEndpointKind(resolved.AuthHeader)
	}
	if !ok {
		env["LLM_API_KEY"] = resolved.Credential
		return
	}
	for _, v := range kind.EnvVars {
		env[v] = resolved.Credential
	}
	for _, v := range kind.Clears {
		delete(env, v)
	}
}

func clearProviderAuthEnv(env map[string]string, caps engine.Capabilities) {
	for _, k := range caps.KeyKinds {
		for _, v := range k.EnvVars {
			delete(env, v)
		}
		for _, v := range k.Clears {
			delete(env, v)
		}
	}
	for _, v := range []string{caps.AuthorizationEnv, caps.BaseURLEnv, caps.OrgIDEnv, caps.ProviderEnv, "LLM_API_KEY"} {
		if v != "" {
			delete(env, v)
		}
	}
}

func checkAmbientCloudIdentity(resolved secrets.ResolvedLLM) error {
	secret, err := engine.DecodeCloudSecret(resolved.Credential)
	if err != nil {
		return apperr.Wrap(err, "task.errors.llm_profile", "profile", resolved.ProfileName)
	}
	if !secret.Empty() || engine.AmbientCloudIdentityAllowed(os.Getenv) {
		return nil
	}
	return apperr.Precondition("task.errors.llm_profile_ambient_identity",
		"profile", resolved.ProfileName, "provider", resolved.Provider, "env", engine.EnvAmbientCloudIdentity)
}

func (d *Dispatcher) applyCloudGatewayEnv(env map[string]string, caps engine.Capabilities, resolved secrets.ResolvedLLM, taskRun bool) error {
	token := env["OCTOPUS_API_KEY"]
	if taskRun && (d.keys == nil || token == "") {
		return apperr.Precondition("task.errors.gateway_no_run_key")
	}
	clearProviderAuthEnv(env, caps)
	for k, v := range caps.CloudGatewayEnv(engine.CloudConfig{
		Provider:       resolved.Provider,
		Region:         resolved.Region,
		ProjectID:      resolved.ProjectID,
		BaseURL:        d.LLMGatewayURL() + "/" + resolved.Provider,
		SmallFastModel: resolved.SmallFastModel,
	}, token) {
		env[k] = v
	}
	env[engine.LLMGatewayEnv] = "1"
	return nil
}

func applyCloudProfile(env map[string]string, engineName string, caps engine.Capabilities, resolved secrets.ResolvedLLM) error {
	if caps.CloudEnv == nil || !caps.SupportsProvider(resolved.Provider) {
		return apperr.Invalid("task.errors.engine_cannot_run_profile",
			"engine", engineName, "provider", resolved.Provider, "profile", resolved.ProfileName)
	}
	secret, err := engine.DecodeCloudSecret(resolved.Credential)
	if err != nil {
		return apperr.Wrap(err, "task.errors.llm_profile", "profile", resolved.ProfileName)
	}
	clearProviderAuthEnv(env, caps)
	for k, v := range caps.CloudEnv(engine.CloudConfig{
		Provider:       resolved.Provider,
		Region:         resolved.Region,
		ProjectID:      resolved.ProjectID,
		BaseURL:        resolved.BaseURL,
		SmallFastModel: resolved.SmallFastModel,
		Secret:         secret,
	}) {
		env[k] = v
	}
	return nil
}

func sandboxMCPURL(baseURL, taskID string, stage Stage) string {
	return fmt.Sprintf("%s/sandbox/sse?task_id=%s&stage=%s", baseURL, taskID, stage) //i18n:ignore URL template
}

func buildStageEnv(t *Task, stage Stage, agentName, instanceID, baseURL string, agentType *types.AgentType, turnsOverride string) map[string]string { //nolint:lll // signature
	env := map[string]string{
		"TASK_ID":               t.ID,
		"OCTOPUS_API":           baseURL,
		"AGENT_NAME":            agentName,
		"INSTANCE_ID":           instanceID,
		"PROJECT_NAME":          fmt.Sprintf("task-%s", t.ID),
		"STAGE":                 string(stage),
		"MCP_THULU_SANDBOX_URL": sandboxMCPURL(baseURL, t.ID, stage),
		"OCTOPUS_LOCALE":        i18n.DaemonLocale(),
	}
	if t.BoardID != "" {
		env["BOARD_ID"] = t.BoardID
	}
	if t.WorkspaceID != "" {
		env["WORKSPACE_ID"] = t.WorkspaceID
	}
	// Multi-project tasks get no PROJECT_ID so usage is not misattributed to the first project.
	if len(t.ProjectIDs) == 1 {
		env["PROJECT_ID"] = t.ProjectIDs[0]
	}
	stageStr := string(stage)
	env["MAX_TURNS"] = agentType.TurnsForStage(stageStr)
	if turnsOverride != "" {
		env["MAX_TURNS"] = turnsOverride
	}
	if skills := agentType.DefaultSkills(stageStr); skills != "" {
		env["SKILLS"] = skills
	}
	env["SKILLS_MODE"] = agentType.SkillsMode
	if tools := agentType.DefaultMCP(stageStr); tools != "" {
		env["MCP_TOOLS"] = tools
	}
	if t.BranchName != "" {
		env["BRANCH_NAME"] = t.BranchName
	}
	if agentType.Model != "" {
		env["MODEL"] = agentType.Model
	}
	if agentType.Effort != "" {
		env["EFFORT"] = agentType.Effort
	}
	applyEngineEnv(env, agentType, agentName)
	return env
}

func applyAuditorEnv(env map[string]string, auditor *types.AuditorSpec, stageStr string) {
	if !auditor.RunsForStage(stageStr) {
		return
	}
	env["AUDITOR_ENGINE"] = env["ENGINE"]
	if v := auditor.TurnsForStage(stageStr); v != "" {
		env["AUDITOR_TURNS"] = v
	}
	if v := auditor.ModelForStage(stageStr); v != "" {
		env["AUDITOR_MODEL"] = v
	}
	if v := auditor.SkillsForStage(stageStr); v != "" {
		env["AUDITOR_SKILLS"] = v
	}
}
