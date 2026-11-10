package engine

import (
	"encoding/json"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const (
	ProviderAnthropic        = "anthropic"
	ProviderOpenAI           = "openai"
	ProviderOpenAICompatible = "openai-compatible"
	ProviderBedrock          = "bedrock"
	ProviderVertex           = "vertex"
)

func ProviderNeedsBaseURL(p string) bool {
	return p == ProviderOpenAICompatible
}

func gatewayAPIProvider(p string) bool {
	return p == ProviderAnthropic || p == ProviderOpenAI || p == ProviderOpenAICompatible
}

func IsCloudProvider(p string) bool {
	return p == ProviderBedrock || p == ProviderVertex
}

// Off by default: the ambient identity is the operator's, and any workspace admin could bill their
// cloud account.
const EnvAmbientCloudIdentity = "OCTOPUS_LLM_AMBIENT_CLOUD_IDENTITY"

func AmbientCloudIdentityAllowed(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(EnvAmbientCloudIdentity))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

const (
	AuthHeaderAPIKey = "x-api-key"
	AuthHeaderBearer = "bearer"
)

func ValidAuthHeader(v string) bool {
	return v == "" || v == AuthHeaderAPIKey || v == AuthHeaderBearer
}

type CloudConfig struct {
	Provider       string
	Region         string
	ProjectID      string
	BaseURL        string
	SmallFastModel string
	Secret         CloudSecret
}

type CloudSecret struct {
	AWSAccessKeyID        string `json:"aws_access_key_id,omitempty"`
	AWSSecretAccessKey    string `json:"aws_secret_access_key,omitempty"`
	AWSSessionToken       string `json:"aws_session_token,omitempty"`
	BedrockAPIKey         string `json:"bedrock_api_key,omitempty"`
	GCPServiceAccountJSON string `json:"gcp_service_account_json,omitempty"`
}

func (s CloudSecret) Empty() bool {
	return s == CloudSecret{}
}

func (s CloudSecret) Encode() (string, error) {
	if s.Empty() {
		return "", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func DecodeCloudSecret(raw string) (CloudSecret, error) {
	var s CloudSecret
	if strings.TrimSpace(raw) == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return CloudSecret{}, apperr.Invalid("engine.errors.cloud_secret_invalid_json")
	}
	return s, nil
}

func (c Capabilities) SupportsProvider(p string) bool {
	if p == "" || p == c.Provider {
		return true
	}
	return contains(c.APIProviders, p) || contains(c.CloudProviders, p)
}

func (c Capabilities) Providers() []string {
	out := []string{c.Provider}
	out = append(out, c.APIProviders...)
	return append(out, c.CloudProviders...)
}

func (c Capabilities) ProfileProvider(p string) string {
	if p == "" {
		return c.Provider
	}
	return p
}

func (c Capabilities) APIKeyKind() (KeyKind, bool) {
	return KeyKindByID(c.KeyKinds, "api")
}

func (c Capabilities) CustomEndpointKind(authHeader string) (KeyKind, bool) {
	api, hasAPI := c.APIKeyKind()
	if authHeader == AuthHeaderBearer && c.AuthorizationEnv != "" {
		clears := []string{}
		for _, k := range c.KeyKinds {
			clears = append(clears, k.EnvVars...)
		}
		return KeyKind{ID: "bearer", EnvVars: []string{c.AuthorizationEnv}, Clears: clears}, true
	}
	if !hasAPI {
		return KeyKind{}, false
	}
	if c.AuthorizationEnv != "" && !contains(api.Clears, c.AuthorizationEnv) {
		api.Clears = append(append([]string{}, api.Clears...), c.AuthorizationEnv)
	}
	return api, true
}

// The dispatcher and the gateway both call this so they cannot disagree.
func (c Capabilities) GatewayCanPresent(provider, credential, baseURL string) bool {
	if IsCloudProvider(provider) {
		return c.CloudGatewayEnv != nil && c.SupportsProvider(provider)
	}
	p := c.ProfileProvider(provider)
	if !gatewayAPIProvider(p) || !c.SupportsProvider(p) || c.BaseURLEnv == "" {
		return false
	}
	if credential == "" {
		return false
	}
	if baseURL != "" {
		return true
	}
	k, ok := MatchKeyKind(c.KeyKinds, credential)
	return !ok || !k.NoGateway
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
