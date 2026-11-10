// Written against opencode-ai 1.18.35, the version images/kube-agent/Dockerfile pins; the stream
// parser follows that version, so bump both together.
package opencode

import (
	"context"
	"io"
	"iter"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const Name = "opencode"

const (
	KeyEnv      = "LLM_API_KEY"
	BaseURLEnv  = "OCTOPUS_LLM_BASE_URL"
	ProviderEnv = "OCTOPUS_LLM_PROVIDER"
)

// Must match opencode.sh's default; assumes the default openai-compatible provider points at
// DeepSeek.
const DefaultModel = "deepseek-chat"

type Config struct {
	EnvFile string
}

func New(cfg Config) engine.Engine { return &opencode{cfg: cfg} }

type opencode struct {
	cfg Config
}

func (o *opencode) Name() string { return Name }

func (o *opencode) ParseStream(r io.Reader) iter.Seq[engine.Event] {
	return parseStream(r)
}

func (o *opencode) AuthEnv(_ context.Context) []string {
	if o.cfg.EnvFile == "" {
		return nil
	}
	return engine.AuthFromEnvFile(o.cfg.EnvFile, []string{KeyEnv, BaseURLEnv, ProviderEnv}, nil)
}

func (o *opencode) Capabilities() engine.Capabilities { return o.CapabilitiesIn(context.Background()) }

func (o *opencode) CapabilitiesIn(ctx context.Context) engine.Capabilities {
	return engine.Capabilities{
		ResumableSessions: true,
		StreamJSON:        true,
		// --variant values differ per provider ("high", "max", "minimal"); low|medium|high doesn't
		// map onto every model.
		EffortFlag:   false,
		MCP:          true,
		Skills:       true,
		Models:       []string{"deepseek-chat", "deepseek-reasoner"},
		DefaultModel: DefaultModel,
		// A `steps` cap ends with a normal answer the stream can't distinguish, so MAX_TURNS is not
		// passed on.
		TurnBudget: false,
		Pricing:    pricing,

		Label:        i18n.TCtx(ctx, "engine.labels.opencode"),
		Provider:     engine.ProviderOpenAICompatible,
		APIProviders: []string{engine.ProviderAnthropic, engine.ProviderOpenAI},
		ProviderEnv:  ProviderEnv,
		KeyKinds: []engine.KeyKind{
			{
				ID:      "api",
				Label:   i18n.TCtx(ctx, "engine.labels.provider_api_key"),
				EnvVars: []string{KeyEnv},
			},
		},
		BaseURLEnv: BaseURLEnv,
		NonessentialTrafficEnv: map[string]string{
			"OPENCODE_DISABLE_AUTOUPDATE":   "1",
			"OPENCODE_DISABLE_MODELS_FETCH": "1",
			"OPENCODE_DISABLE_LSP_DOWNLOAD": "1",
			"OPENCODE_DISABLE_SHARE":        "1",
		},
	}
}

var pricing = []engine.ModelPrice{
	{Model: "deepseek-chat", InPerMTok: 0.28, OutPerMTok: 0.42, CacheReadPerMTok: 0.028},
	{Model: "deepseek-reasoner", InPerMTok: 0.28, OutPerMTok: 0.42, CacheReadPerMTok: 0.028},
}

func init() { engine.Register(New(Config{})) }
