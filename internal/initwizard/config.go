package initwizard

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
)

type Mode string

const (
	ModeDocker     Mode = "docker"
	ModeKubernetes Mode = "kubernetes"
)

type Identity string

const (
	IdentityNone   Identity = "none"
	IdentityOIDC   Identity = "oidc"
	IdentityGitHub Identity = "github"
)

type OIDCConfig struct {
	Issuer           string
	ClientID         string
	ClientSecret     string `json:"-"`
	DisplayName      string
	GroupsClaim      string
	GroupRoleMap     string
	DefaultWorkspace string
	// Empty lets in anyone the IdP authenticates; on a multi-tenant IdP (Google, Entra common) that
	// is anyone with an account.
	AllowedDomains string
}

type GitHubOAuth struct {
	ClientID     string
	ClientSecret string `json:"-"`
}

type Deferred struct {
	Step   string `json:"step"`
	Effect string `json:"effect"`
	Finish string `json:"finish"`
}

type Config struct {
	Mode     Mode     `json:"mode"`
	Identity Identity `json:"identity"`

	ComposeFile string `json:"compose_file,omitempty"`

	Image string `json:"image,omitempty"`

	ImageRegistry string `json:"image_registry,omitempty"`
	ImageTag      string `json:"image_tag,omitempty"`

	// nil means the step never ran (older answers file) and leaves existing config alone; empty
	// means "none".
	MCPServers []string `json:"mcp_servers"`

	Bind string `json:"bind,omitempty"`

	OIDC   OIDCConfig  `json:"oidc"`
	GitHub GitHubOAuth `json:"github"`

	GitHubURL    string `json:"github_url,omitempty"`
	GitHubAPIURL string `json:"github_api_url,omitempty"`

	JWTSecret string `json:"-"`

	// Generated once and kept: stored credentials encrypted under it are unreadable with any other
	// key.
	SecretboxKey string `json:"-"`

	DevToken string `json:"-"`

	// Kept across reruns: Postgres fixes it when the volume is initialized.
	PGPassword string `json:"-"`

	DatabaseURL string `json:"-"`
	// Differs from DatabaseURL in compose: pg:5432 inside the network, localhost:5433 from the
	// host, or the port in PGHostPort.
	BootstrapDSN string `json:"-"`

	// The loopback port of the bundled Postgres when 5433 was taken; empty means 5433.
	PGHostPort string `json:"-"`

	Domain         string `json:"domain"`
	PublicOrigin   string `json:"public_origin"`
	TrustedProxies string `json:"trusted_proxies"`
	LicenseKey     string `json:"-"`

	Namespace    string `json:"namespace,omitempty"`
	Release      string `json:"release,omitempty"`
	RuntimeClass string `json:"runtime_class,omitempty"`
	AgentImage   string `json:"agent_image,omitempty"`

	OperatorEmail string `json:"operator_email"`
	AdminEmail    string `json:"admin_email,omitempty"`
	WorkspaceName string `json:"workspace_name"`
	WorkspaceSlug string `json:"workspace_slug"`
	WorkspaceID   string `json:"workspace_id,omitempty"`

	Quick bool `json:"quick,omitempty"`

	Engines []string `json:"engines,omitempty"`

	Runner RunnerPlan `json:"runner"`

	GitToken         string             `json:"-"`
	GitProvider      string             `json:"-"`
	Credentials      []EngineCredential `json:"-"`
	MintedKey        string             `json:"-"`
	MintedKeyExpires *time.Time         `json:"-"`

	Deferred []Deferred `json:"deferred,omitempty"`
}

// Must match octopus.fullname in the Helm chart: "<release>-octopus" cut to 63 characters.
func (c *Config) DaemonDeployment() string {
	name := c.Release + "-octopus"
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimSuffix(name, "-")
}

func (c *Config) Defer(step, effect, finish string) {
	c.Deferred = append(c.Deferred, Deferred{Step: step, Effect: effect, Finish: finish})
}

func (c *Config) DevMode() bool { return c.Identity == IdentityNone }

// 32 bytes is the length auth.ValidateStartupConfig requires.
func GenerateSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", apperr.Wrap(err, "initwizard.errors.generate_secret")
	}
	return hex.EncodeToString(buf), nil
}

func (c *Config) ApplyToEnv(f *EnvFile) {
	f.Set("OCTOPUS_JWT_SECRET", c.JWTSecret)
	// Never unset: stored credentials are unreadable under any other key.
	if c.SecretboxKey != "" {
		f.Set("OCTOPUS_SECRETBOX_KEY", c.SecretboxKey)
	}
	f.Set("OCTOPUS_DATABASE_URL", c.DatabaseURL)
	setOrUnset(f, "OCTOPUS_DOMAIN", c.Domain)
	setOrUnset(f, "OCTOPUS_PUBLIC_HOST", c.Domain)
	setOrUnset(f, "OCTOPUS_ALLOWED_ORIGINS", c.PublicOrigin)
	setOrUnset(f, "OCTOPUS_TRUSTED_PROXIES", c.TrustedProxies)
	setOrUnset(f, "OCTOPUS_LICENSE_KEY", c.LicenseKey)
	setOrUnset(f, "OCTOPUS_OPERATOR_WORKSPACE", c.WorkspaceID)
	setOrUnset(f, "OCTOPUS_BIND", c.Bind)
	// Never unset: `thulu update -version X` writes this key, and clearing it would downgrade a
	// release install to building from the checkout.
	if c.Image != "" {
		f.Set("OCTOPUS_IMAGE", c.Image)
	}
	// Never unset: `thulu update` moves this tag.
	if c.ImageTag != "" {
		f.Set(VersionEnvKey, c.ImageTag)
	}
	if c.ImageRegistry != "" {
		f.Set(ImageRegistryEnv, c.ImageRegistry)
	}
	if c.PGPassword != "" {
		f.Set("OCTOPUS_PG_PASSWORD", c.PGPassword)
	}
	setOrUnset(f, pgHostPortEnv, c.PGHostPort)
	c.applyMCPToEnv(f)

	if c.DevMode() {
		f.Set("OCTOPUS_DEV_MODE", "1")
		// Paired with the loopback-only compose override; see auth.DevModeContainerEnv for why both
		// are needed.
		f.Set("OCTOPUS_DEV_MODE_CONTAINER", "1")
		f.Set(auth.DevTokenEnv, c.DevToken)
	} else {
		f.Unset("OCTOPUS_DEV_MODE")
		f.Unset("OCTOPUS_DEV_MODE_CONTAINER")
		f.Unset(auth.DevTokenEnv)
	}

	setOrUnset(f, "OCTOPUS_OIDC_ISSUER", c.OIDC.Issuer)
	setOrUnset(f, "OCTOPUS_OIDC_CLIENT_ID", c.OIDC.ClientID)
	setOrUnset(f, "OCTOPUS_OIDC_CLIENT_SECRET", c.OIDC.ClientSecret)
	setOrUnset(f, "OCTOPUS_OIDC_NAME", c.OIDC.DisplayName)
	setOrUnset(f, "OCTOPUS_OIDC_GROUPS_CLAIM", c.OIDC.GroupsClaim)
	setOrUnset(f, "OCTOPUS_OIDC_GROUP_ROLE_MAP", c.OIDC.GroupRoleMap)
	setOrUnset(f, "OCTOPUS_SSO_DEFAULT_WORKSPACE", c.OIDC.DefaultWorkspace)
	setOrUnset(f, "OCTOPUS_SSO_ALLOWED_DOMAINS", c.OIDC.AllowedDomains)

	setOrUnset(f, "OCTOPUS_GITHUB_CLIENT_ID", c.GitHub.ClientID)
	setOrUnset(f, "OCTOPUS_GITHUB_CLIENT_SECRET", c.GitHub.ClientSecret)
	// The API URL is written explicitly because compose passes it to thulu-github by interpolation,
	// which cannot derive it.
	setOrUnset(f, "OCTOPUS_GITHUB_URL", c.GitHubURL)
	setOrUnset(f, "OCTOPUS_GITHUB_API_URL", c.GitHubAPIURL)
}

func setOrUnset(f *EnvFile, key, value string) {
	if strings.TrimSpace(value) == "" {
		f.Unset(key)
		return
	}
	f.Set(key, value)
}

// Emit only what the interview decided, so chart default changes still reach wizard installs on
// upgrade.
func (c *Config) HelmValues() map[string]any {
	secrets := map[string]any{"jwtSecret": c.JWTSecret}
	if c.SecretboxKey != "" {
		secrets["secretboxKey"] = c.SecretboxKey
	}
	if c.LicenseKey != "" {
		secrets["licenseKey"] = c.LicenseKey
	}
	if c.Identity == IdentityOIDC {
		secrets["oidc"] = map[string]any{
			"issuer":       c.OIDC.Issuer,
			"clientId":     c.OIDC.ClientID,
			"clientSecret": c.OIDC.ClientSecret,
			"name":         c.OIDC.DisplayName,
			"groupRoleMap": c.OIDC.GroupRoleMap,
		}
	}
	if c.Identity == IdentityGitHub {
		secrets["github"] = map[string]any{
			"clientId":     c.GitHub.ClientID,
			"clientSecret": c.GitHub.ClientSecret,
		}
	}

	extraEnv := map[string]any{"OCTOPUS_DATABASE_URL": c.DatabaseURL}
	if c.WorkspaceID != "" {
		extraEnv["OCTOPUS_OPERATOR_WORKSPACE"] = c.WorkspaceID
	}
	if c.OIDC.DefaultWorkspace != "" {
		extraEnv["OCTOPUS_SSO_DEFAULT_WORKSPACE"] = c.OIDC.DefaultWorkspace
	}
	if c.OIDC.GroupsClaim != "" {
		extraEnv["OCTOPUS_OIDC_GROUPS_CLAIM"] = c.OIDC.GroupsClaim
	}

	agentRuntime := map[string]any{
		"mode":         "kubernetes",
		"runtimeClass": c.RuntimeClass,
	}
	if c.RuntimeClass == "" {
		// Both are required: the daemon refuses to start without a RuntimeClass unless told,
		// and the admission policy rejects pods outside a non-empty RuntimeClass list.
		extraEnv["OCTOPUS_KUBE_ALLOW_NO_RUNTIMECLASS"] = "1"
		agentRuntime["admissionPolicy"] = map[string]any{"allowedRuntimeClasses": []any{}}
	}
	if c.AgentImage != "" {
		agentRuntime["agentImage"] = c.AgentImage
	}

	config := map[string]any{
		"domain":         c.Domain,
		"allowedOrigins": c.PublicOrigin,
		"trustedProxies": c.TrustedProxies,
		"devMode":        false,
	}
	if c.OIDC.AllowedDomains != "" {
		config["sso"] = map[string]any{"allowedDomains": c.OIDC.AllowedDomains}
	}

	values := map[string]any{
		"config":       config,
		"secrets":      secrets,
		"extraEnv":     extraEnv,
		"agentRuntime": agentRuntime,
		// Agents are pods here; mounting the node's Docker socket would give the daemon a
		// root-equivalent escape it never uses.
		"dockerSocket": map[string]any{"enabled": false},
		"ingress": map[string]any{
			"enabled": true,
			"hosts": []any{map[string]any{
				"host": c.Domain,
				//i18n:ignore Kubernetes Ingress pathType value
				"paths": []any{map[string]any{"path": "/", "pathType": "Prefix"}},
			}},
			"tls": []any{map[string]any{
				"secretName": "octopus-tls",
				"hosts":      []any{c.Domain},
			}},
		},
		"mcp": c.mcpHelmValues(),
	}
	if c.GitHubURL != "" {
		values["github"] = map[string]any{"url": c.GitHubURL, "apiUrl": c.GitHubAPIURL}
	}
	if reg := imageRegistryOverride(); reg != "" {
		values["image"] = map[string]any{"repository": reg + "/octopus"}
	}
	return values
}
