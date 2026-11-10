package tokenopt

import (
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const (
	LevelOff     = types.TokenOptimizerOff
	LevelSuggest = types.TokenOptimizerSuggest
	LevelEnforce = types.TokenOptimizerEnforce
)

// 24 KiB, about 6k tokens of code. Fixed on purpose; no setting changes it.
const DefaultThresholdBytes = 24576

const (
	ReasonNoWorker           = "no_worker"
	ReasonEngineWithoutHooks = "engine_without_hooks"
)

// Only this engine's CLI runs the hooks "enforce" relies on; other engines get "suggest".
const HooksEngine = engine.DefaultName

const (
	// The daemon calls the provider with the profile's key; the key never reaches the pod.
	ViaAPI = "api"
	// The pod runs "claude -p" with the profile's subscription token: that token may only go to the
	// Claude Code client.
	ViaCLI = "cli"
)

// A cli worker runs claude in the run's own pod and only Claude Code's agent image ships that CLI,
// so a cli worker serves only agents on this engine.
const CLIEngine = engine.DefaultName

const ProviderClaudeCode = "claude-code"

const (
	WorkerNoDefaultProfile = "no_default_profile"
	WorkerUnsupported      = "unsupported_credential"
)

type WorkerInfo struct {
	Engine      string `json:"engine"`
	Available   bool   `json:"available"`
	Via         string `json:"via"`
	Provider    string `json:"provider"`
	ProfileName string `json:"profile_name"`
	Reason      string `json:"reason"`
	ProfileID   string `json:"-"`
}

type Resolved struct {
	Level          string `json:"level"`
	ThresholdBytes int    `json:"threshold_bytes"`
	Reason         string `json:"reason"`
	ReadEngine     string `json:"read_engine"`
	ReadVia        string `json:"read_via"`
	ReadModel      string `json:"read_model"`
}

func (r Resolved) On() bool { return r.Level != LevelOff }

func Resolve(at *types.AgentType, agentEngine string, worker WorkerInfo) Resolved {
	off := Resolved{Level: LevelOff, ThresholdBytes: DefaultThresholdBytes}
	if at == nil {
		return off
	}
	policy := at.TokenOptimizer.Normalize()
	if !policy.On() {
		return off
	}
	if policy.Engine == "" || policy.Model == "" || !worker.Available || worker.Engine != policy.Engine ||
		(worker.Via != ViaAPI && worker.Via != ViaCLI) {
		off.Reason = ReasonNoWorker
		return off
	}
	if agentEngine == "" {
		agentEngine = engine.DefaultName
	}
	if worker.Via == ViaCLI && agentEngine != CLIEngine {
		off.Reason = ReasonNoWorker
		return off
	}
	out := Resolved{
		Level: policy.Level, ThresholdBytes: DefaultThresholdBytes,
		ReadEngine: policy.Engine, ReadVia: worker.Via, ReadModel: policy.Model,
	}
	if out.Level == LevelEnforce && agentEngine != HooksEngine {
		out.Level = LevelSuggest
		out.Reason = ReasonEngineWithoutHooks
	}
	return out
}

// An api worker never gets a credential: the daemon calls it.
func Env(r Resolved, workerOAuth string) map[string]string {
	if !r.On() {
		return nil
	}
	env := map[string]string{
		EnvLevel:          r.Level,
		EnvThresholdBytes: strconv.Itoa(r.ThresholdBytes),
		EnvReadVia:        r.ReadVia,
	}
	if r.ReadVia == ViaCLI {
		env[EnvReadModel] = r.ReadModel
		if workerOAuth != "" {
			env[EnvWorkerOAuth] = workerOAuth
		}
	}
	return env
}

const (
	EnvLevel          = "OCTOPUS_TOKEN_OPTIMIZER"
	EnvThresholdBytes = "OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES"
	EnvReadVia        = "OCTOPUS_TOKEN_OPTIMIZER_READ_VIA"
	EnvReadModel      = "OCTOPUS_TOKEN_OPTIMIZER_READ_MODEL"
	// A credential: the pod must get it in its env Secret, like the engine's own.
	EnvWorkerOAuth = "OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN"
)

// Must list every variable Env sets: callers clear these before applying a new resolution.
var EnvKeys = []string{EnvLevel, EnvThresholdBytes, EnvReadVia, EnvReadModel, EnvWorkerOAuth}
