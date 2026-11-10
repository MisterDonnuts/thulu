// Targets codex-cli 0.142.0, the @openai/codex version pinned in images/kube-agent/Dockerfile.
package codex

import (
	"context"
	"io"
	"iter"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const Name = "codex"

type Config struct {
	EnvFile string
}

func New(cfg Config) engine.Engine { return &codex{cfg: cfg} }

type codex struct {
	cfg Config
}

func (c *codex) Name() string { return Name }

func (c *codex) ParseStream(r io.Reader) iter.Seq[engine.Event] {
	return parseStream(r)
}

func (c *codex) AuthEnv(_ context.Context) []string {
	if c.cfg.EnvFile == "" {
		return nil
	}
	return readAuthFromEnvFile(c.cfg.EnvFile)
}

func (c *codex) Capabilities() engine.Capabilities { return c.CapabilitiesIn(context.Background()) }

func (c *codex) CapabilitiesIn(ctx context.Context) engine.Capabilities {
	return engine.Capabilities{
		ResumableSessions: true,
		StreamJSON:        true,

		// Off until codex.sh renders `-c model_reasoning_effort=…`; the dispatcher drops EFFORT
		// while false.
		EffortFlag: false,

		// codex.sh bridges the SSE MCP servers via mcp-remote (codex can't reach SSE); true makes
		// the dispatcher pass MCP_TOOLS through.
		MCP: true,

		// codex has no skill discovery; the entrypoint pastes skills into the prompt instead
		// (snippets/skills.sh).
		Skills: false,

		Models: []string{"gpt-5.4-mini", "gpt-5.4", "gpt-5.5", "gpt-5.3-codex"},
		// Must match codex.sh's `--model "${MODEL:-gpt-5.4-mini}"`.
		DefaultModel: "gpt-5.4-mini",
		TurnBudget:   false,
		Pricing:      pricing,

		Label:    i18n.TCtx(ctx, "engine.labels.openai_codex"),
		Provider: "openai",
		KeyKinds: []engine.KeyKind{
			{
				ID:      "api",
				Label:   i18n.TCtx(ctx, "engine.labels.openai_api_key"),
				Prefix:  "sk-",
				EnvVars: []string{"OPENAI_API_KEY"},
			},
		},

		BaseURLEnv: "OPENAI_BASE_URL",
		OrgIDEnv:   "OPENAI_ORG_ID",
	}
}

// OpenAI bills no cache writes, hence no CacheCreatePerMTok.
// No Family rows: short names like "o3" would match inside unrelated ids.
// The older rows stay to price past runs and agents that still name those models.
// gpt-5.5 and gpt-5.4 cost more past 272K of context; these are the rates under it.
var pricing = []engine.ModelPrice{
	{Model: "gpt-5.5", InPerMTok: 5.0, OutPerMTok: 30.0, CacheReadPerMTok: 0.50},
	{Model: "gpt-5.4", InPerMTok: 2.50, OutPerMTok: 15.0, CacheReadPerMTok: 0.25},
	{Model: "gpt-5.4-mini", InPerMTok: 0.75, OutPerMTok: 4.50, CacheReadPerMTok: 0.075},
	{Model: "gpt-5.3-codex", InPerMTok: 1.75, OutPerMTok: 14.0, CacheReadPerMTok: 0.175},
	{Model: "gpt-4o-mini", InPerMTok: 0.15, OutPerMTok: 0.60, CacheReadPerMTok: 0.075},
	{Model: "gpt-4o", InPerMTok: 2.50, OutPerMTok: 10.0, CacheReadPerMTok: 1.25},
	{Model: "o4-mini", InPerMTok: 1.10, OutPerMTok: 4.40, CacheReadPerMTok: 0.275},
	{Model: "o3", InPerMTok: 2.0, OutPerMTok: 8.0, CacheReadPerMTok: 0.50},
}

func init() { engine.Register(New(Config{})) }
