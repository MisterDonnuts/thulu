package claudecode

import (
	"context"
	"encoding/base64"
	"io"
	"iter"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const Name = "claude-code"

type Config struct {
	// Re-read on every AuthEnv call so a token refresh reaches the next turn without restarting the
	// daemon or recreating containers; don't cache it.
	EnvFile string
}

func New(cfg Config) engine.Engine { return &claudeCode{cfg: cfg} }

type claudeCode struct {
	cfg Config
}

func (c *claudeCode) Name() string { return Name }

func (c *claudeCode) ParseStream(r io.Reader) iter.Seq[engine.Event] {
	return parseStream(r)
}

func (c *claudeCode) AuthEnv(_ context.Context) []string {
	if c.cfg.EnvFile == "" {
		return nil
	}
	return readAuthFromEnvFile(c.cfg.EnvFile)
}

func (c *claudeCode) Capabilities() engine.Capabilities {
	return c.CapabilitiesIn(context.Background())
}

func (c *claudeCode) CapabilitiesIn(ctx context.Context) engine.Capabilities {
	return engine.Capabilities{
		ResumableSessions: true,
		StreamJSON:        true,
		StreamingInput:    true,
		EffortFlag:        true,
		MCP:               true,

		Skills: true,

		Models: []string{"sonnet", "opus", "haiku"},
		// Must match claude-code.sh's `--model "${MODEL:-sonnet}"`.
		DefaultModel: "sonnet",
		TurnBudget:   true,
		Pricing:      pricing,

		// Each kind clears the others' env vars so the CLI can't fall back to the wrong credential.
		Label:            i18n.TCtx(ctx, "engine.labels.claude_code"),
		Provider:         "anthropic",
		BaseURLEnv:       "ANTHROPIC_BASE_URL",
		AuthorizationEnv: "ANTHROPIC_AUTH_TOKEN",
		KeyKinds: []engine.KeyKind{
			{
				ID:      "api",
				Label:   i18n.TCtx(ctx, "engine.labels.anthropic_api_key"),
				Prefix:  "sk-ant-api",
				EnvVars: []string{"ANTHROPIC_API_KEY"},
				Clears:  []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"},
			},
			{
				ID:      "oauth",
				Label:   i18n.TCtx(ctx, "engine.labels.claude_oauth_token"),
				Prefix:  "sk-ant-oat",
				EnvVars: []string{"CLAUDE_CODE_OAUTH_TOKEN"},
				Clears:  []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
				// The OAuth token is bound to the Claude Code client; the gateway cannot present
				// it.
				NoGateway: true,
				// It bills one person's subscription, so only the installation owner may use it.
				OwnerOnly: true,
				// Off in Kubernetes unless the operator turns it on: a shared cluster runs work for
				// many people on one person's subscription.
				KubeOptIn: KubeOAuthOptInEnv,
			},
			{
				ID:        "federation",
				Label:     i18n.TCtx(ctx, "engine.labels.anthropic_federation"),
				Federated: true,
				EnvVars:   federationVars,
				// These outrank federation in the CLI's credential order; a leftover one would win
				// silently.
				Clears:    []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"},
				NoGateway: true,
			},
		},
		FederationEnv:      federationEnv,
		FederationAudience: FederationAudience,
		CloudProviders:     []string{engine.ProviderBedrock, engine.ProviderVertex},
		CloudEnv:           cloudEnv,
		CloudGatewayEnv:    cloudGatewayEnv,
		NonessentialTrafficEnv: map[string]string{
			"DISABLE_AUTOUPDATER":                      "1",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			"DISABLE_TELEMETRY":                        "1",
			"DISABLE_ERROR_REPORTING":                  "1",
		},
		PersonalLogin: personalLogin,
	}
}

// KubeOAuthOptInEnv lets the installation owner store and use a Claude subscription token
// (sk-ant-oat) when agents run in Kubernetes.
const KubeOAuthOptInEnv = "OCTOPUS_KUBE_ALLOW_CLAUDE_OAUTH_TOKEN"

var pricing = []engine.ModelPrice{
	{Model: "claude-opus-4-7", InPerMTok: 15.0, OutPerMTok: 75.0, CacheReadPerMTok: 1.50, CacheCreatePerMTok: 18.75},
	{Model: "claude-sonnet-4-6", InPerMTok: 3.0, OutPerMTok: 15.0, CacheReadPerMTok: 0.30, CacheCreatePerMTok: 3.75},
	{Model: "claude-haiku-4-5-20251001", InPerMTok: 1.0, OutPerMTok: 5.0, CacheReadPerMTok: 0.10, CacheCreatePerMTok: 1.25},
	{Model: "opus", InPerMTok: 15.0, OutPerMTok: 75.0, CacheReadPerMTok: 1.50, CacheCreatePerMTok: 18.75, Family: true},
	{Model: "sonnet", InPerMTok: 3.0, OutPerMTok: 15.0, CacheReadPerMTok: 0.30, CacheCreatePerMTok: 3.75, Family: true},
	{Model: "haiku", InPerMTok: 1.0, OutPerMTok: 5.0, CacheReadPerMTok: 0.10, CacheCreatePerMTok: 1.25, Family: true},
}

const FederationAudience = "https://api.anthropic.com"

// The CLI reads the token from ANTHROPIC_IDENTITY_TOKEN_FILE;
// images/snippets/engines/claude-code.sh writes this var to disk, points that var at it and unsets
// this one.
const IdentityJWTEnv = "OCTOPUS_ANTHROPIC_IDENTITY_JWT"

var federationVars = []string{
	IdentityJWTEnv,
	"ANTHROPIC_FEDERATION_RULE_ID",
	"ANTHROPIC_ORGANIZATION_ID",
	"ANTHROPIC_SERVICE_ACCOUNT_ID",
	"ANTHROPIC_WORKSPACE_ID",
}

// Names per platform.claude.com/docs/en/manage-claude/wif-reference#environment-variables; the
// token file is written by the adapter.
func federationEnv(c engine.FederationConfig, identityToken string) map[string]string {
	env := map[string]string{}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			env[k] = v
		}
	}
	set(IdentityJWTEnv, identityToken)
	set("ANTHROPIC_FEDERATION_RULE_ID", c.RuleID)
	set("ANTHROPIC_ORGANIZATION_ID", c.OrganizationID)
	set("ANTHROPIC_SERVICE_ACCOUNT_ID", c.ServiceAccountID)
	set("ANTHROPIC_WORKSPACE_ID", c.WorkspaceID)
	return env
}

// The Google SDK reads a file; images/snippets/engines/claude-code.sh decodes this to disk and sets
// GOOGLE_APPLICATION_CREDENTIALS before the CLI starts.
const VertexKeyFileEnv = "OCTOPUS_VERTEX_CREDENTIALS_B64"

// Env names per code.claude.com/docs/en/amazon-bedrock and …/google-vertex-ai.
// Absent secrets stay unset so the SDK's default credential chain (instance role, IRSA, workload
// identity) supplies them.
func cloudEnv(c engine.CloudConfig) map[string]string {
	env := map[string]string{}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			env[k] = v
		}
	}
	switch c.Provider {
	case engine.ProviderBedrock:
		env["CLAUDE_CODE_USE_BEDROCK"] = "1"
		set("AWS_REGION", c.Region)
		set("ANTHROPIC_BEDROCK_BASE_URL", c.BaseURL)
		set("AWS_ACCESS_KEY_ID", c.Secret.AWSAccessKeyID)
		set("AWS_SECRET_ACCESS_KEY", c.Secret.AWSSecretAccessKey)
		set("AWS_SESSION_TOKEN", c.Secret.AWSSessionToken)
		set("AWS_BEARER_TOKEN_BEDROCK", c.Secret.BedrockAPIKey)
	case engine.ProviderVertex:
		env["CLAUDE_CODE_USE_VERTEX"] = "1"
		set("CLOUD_ML_REGION", c.Region)
		set("ANTHROPIC_VERTEX_PROJECT_ID", c.ProjectID)
		set("ANTHROPIC_VERTEX_BASE_URL", c.BaseURL)
		if sa := strings.TrimSpace(c.Secret.GCPServiceAccountJSON); sa != "" {
			env[VertexKeyFileEnv] = base64.StdEncoding.EncodeToString([]byte(sa))
		}
	default:
		return env
	}
	// ANTHROPIC_SMALL_FAST_MODEL is deprecated but older CLIs in pinned images still read it.
	set("ANTHROPIC_DEFAULT_HAIKU_MODEL", c.SmallFastModel)
	set("ANTHROPIC_SMALL_FAST_MODEL", c.SmallFastModel)
	return env
}

// c.Secret is cleared so no cloud secret enters the run; the CLI's own signing is skipped and the
// run token goes out as "Authorization: Bearer".
func cloudGatewayEnv(c engine.CloudConfig, runToken string) map[string]string {
	c.Secret = engine.CloudSecret{}
	env := cloudEnv(c)
	switch c.Provider {
	case engine.ProviderBedrock:
		env["CLAUDE_CODE_SKIP_BEDROCK_AUTH"] = "1"
	case engine.ProviderVertex:
		env["CLAUDE_CODE_SKIP_VERTEX_AUTH"] = "1"
	default:
		return env
	}
	if runToken != "" {
		env["ANTHROPIC_AUTH_TOKEN"] = runToken
	}
	return env
}

// Default-config instance; the daemon's main() swaps in the configured one via engine.Replace.
func init() { engine.Register(New(Config{})) }
