package claudecode

import (
	"context"
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

func TestName(t *testing.T) {
	if got := New(Config{}).Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
}

func TestAuthEnv_ZeroConfig_ReturnsNil(t *testing.T) {
	got := New(Config{}).AuthEnv(context.Background())
	if got != nil {
		t.Errorf("AuthEnv = %v, want nil for zero config", got)
	}
}

func TestCapabilities(t *testing.T) {
	c := New(Config{}).Capabilities()
	want := engine.Capabilities{
		ResumableSessions: true,
		StreamJSON:        true,
		EffortFlag:        true,
		MCP:               true,
		Models:            []string{"sonnet", "opus", "haiku"},
	}
	if c.ResumableSessions != want.ResumableSessions ||
		c.StreamJSON != want.StreamJSON ||
		c.EffortFlag != want.EffortFlag ||
		c.MCP != want.MCP {
		t.Errorf("flags = %+v, want %+v", c, want)
	}
	if len(c.Models) != len(want.Models) {
		t.Fatalf("Models len = %d, want %d", len(c.Models), len(want.Models))
	}
	for i, m := range want.Models {
		if c.Models[i] != m {
			t.Errorf("Models[%d] = %q, want %q", i, c.Models[i], m)
		}
	}
}

func TestCapabilities_KeyKinds(t *testing.T) {
	kinds := New(Config{}).Capabilities().KeyKinds
	if len(kinds) != 3 {
		t.Fatalf("KeyKinds len = %d, want 3", len(kinds))
	}
	byID := map[string]engine.KeyKind{}
	for _, k := range kinds {
		byID[k.ID] = k
	}
	api, ok := byID["api"]
	if !ok || api.Prefix != "sk-ant-api" || len(api.EnvVars) != 1 || api.EnvVars[0] != "ANTHROPIC_API_KEY" {
		t.Errorf("api kind = %+v, want prefix sk-ant-api + ANTHROPIC_API_KEY", api)
	}
	if len(api.Clears) != 2 || api.Clears[0] != "CLAUDE_CODE_OAUTH_TOKEN" || api.Clears[1] != "ANTHROPIC_AUTH_TOKEN" {
		t.Errorf("api kind clears = %v, want [CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_AUTH_TOKEN]", api.Clears)
	}
	oauth, ok := byID["oauth"]
	if !ok || oauth.Prefix != "sk-ant-oat" || len(oauth.EnvVars) != 1 || oauth.EnvVars[0] != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("oauth kind = %+v, want prefix sk-ant-oat + CLAUDE_CODE_OAUTH_TOKEN", oauth)
	}
	if len(oauth.Clears) != 2 || oauth.Clears[0] != "ANTHROPIC_API_KEY" || oauth.Clears[1] != "ANTHROPIC_AUTH_TOKEN" {
		t.Errorf("oauth kind clears = %v, want [ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN]", oauth.Clears)
	}
	if !oauth.NoGateway || api.NoGateway {
		t.Errorf("only the OAuth kind stays out of the gateway: api=%v oauth=%v", api.NoGateway, oauth.NoGateway)
	}
	if !oauth.OwnerOnly || api.OwnerOnly {
		t.Errorf("only the OAuth kind is the installation owner's: api=%v oauth=%v", api.OwnerOnly, oauth.OwnerOnly)
	}
	fed, ok := byID["federation"]
	if !ok || !fed.Federated || fed.Prefix != "" {
		t.Fatalf("federation kind = %+v, want Federated with no prefix", fed)
	}
	for _, v := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"} {
		if !slices.Contains(fed.Clears, v) {
			t.Errorf("federation kind must clear %s, which outranks it: %v", v, fed.Clears)
		}
	}
}

func TestFederationEnv(t *testing.T) {
	caps := New(Config{}).Capabilities()
	kind, ok := caps.FederatedKeyKind()
	if !ok {
		t.Fatal("claude-code must federate")
	}
	if caps.FederationAudience != "https://api.anthropic.com" {
		t.Errorf("audience = %q", caps.FederationAudience)
	}
	env := caps.FederationEnv(engine.FederationConfig{
		RuleID: "fdrl_1", OrganizationID: "00000000-0000-0000-0000-000000000000",
		ServiceAccountID: "svac_1",
	}, "jwt.token.sig")
	want := map[string]string{
		IdentityJWTEnv:                 "jwt.token.sig",
		"ANTHROPIC_FEDERATION_RULE_ID": "fdrl_1",
		"ANTHROPIC_ORGANIZATION_ID":    "00000000-0000-0000-0000-000000000000",
		"ANTHROPIC_SERVICE_ACCOUNT_ID": "svac_1",
	}
	if len(env) != len(want) {
		t.Errorf("env = %v, want %v (an unset workspace must stay unset)", env, want)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	for k := range caps.FederationEnv(engine.FederationConfig{
		RuleID: "r", OrganizationID: "o", ServiceAccountID: "s", WorkspaceID: "w",
	}, "t") {
		if !slices.Contains(kind.EnvVars, k) {
			t.Errorf("FederationEnv sets %s, which the federation kind does not list", k)
		}
	}
}

func TestCloudEnv_Bedrock(t *testing.T) {
	caps := New(Config{}).Capabilities()
	if !caps.SupportsProvider(engine.ProviderBedrock) || !caps.SupportsProvider(engine.ProviderVertex) {
		t.Fatalf("claude-code must support bedrock and vertex: %v", caps.CloudProviders)
	}
	env := caps.CloudEnv(engine.CloudConfig{
		Provider: engine.ProviderBedrock, Region: "us-gov-west-1",
		BaseURL:        "https://vpce-1.bedrock-runtime.us-gov-west-1.vpce.amazonaws.com",
		SmallFastModel: "us-gov.anthropic.claude-haiku",
		Secret:         engine.CloudSecret{AWSAccessKeyID: "AKIA1", AWSSecretAccessKey: "s3cr3t"},
	})
	want := map[string]string{
		"CLAUDE_CODE_USE_BEDROCK":       "1",
		"AWS_REGION":                    "us-gov-west-1",
		"ANTHROPIC_BEDROCK_BASE_URL":    "https://vpce-1.bedrock-runtime.us-gov-west-1.vpce.amazonaws.com",
		"AWS_ACCESS_KEY_ID":             "AKIA1",
		"AWS_SECRET_ACCESS_KEY":         "s3cr3t",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "us-gov.anthropic.claude-haiku",
		"ANTHROPIC_SMALL_FAST_MODEL":    "us-gov.anthropic.claude-haiku",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["AWS_SESSION_TOKEN"]; ok {
		t.Error("an absent session token must not be set (it would shadow the SDK chain)")
	}
}

func TestCloudEnv_VertexAmbientAndKeyFile(t *testing.T) {
	caps := New(Config{}).Capabilities()
	ambient := caps.CloudEnv(engine.CloudConfig{Provider: engine.ProviderVertex, Region: "global", ProjectID: "p1"})
	if ambient["CLAUDE_CODE_USE_VERTEX"] != "1" || ambient["CLOUD_ML_REGION"] != "global" ||
		ambient["ANTHROPIC_VERTEX_PROJECT_ID"] != "p1" {
		t.Errorf("vertex env = %v", ambient)
	}
	if _, ok := ambient[VertexKeyFileEnv]; ok {
		t.Error("ambient identity must not carry a key file")
	}
	withKey := caps.CloudEnv(engine.CloudConfig{
		Provider: engine.ProviderVertex, Region: "us-east5", ProjectID: "p1",
		Secret: engine.CloudSecret{GCPServiceAccountJSON: `{"type":"service_account"}`},
	})
	if withKey[VertexKeyFileEnv] != "eyJ0eXBlIjoic2VydmljZV9hY2NvdW50In0=" {
		t.Errorf("key file env = %q", withKey[VertexKeyFileEnv])
	}
}

func TestNonessentialTrafficEnv(t *testing.T) {
	env := New(Config{}).Capabilities().NonessentialTrafficEnv
	for _, k := range []string{"DISABLE_AUTOUPDATER", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
		"DISABLE_TELEMETRY", "DISABLE_ERROR_REPORTING"} {
		if env[k] != "1" {
			t.Errorf("%s = %q, want 1", k, env[k])
		}
	}
}
