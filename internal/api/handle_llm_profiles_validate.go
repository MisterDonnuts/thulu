package api

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

func (d engineDescriptor) supportsProvider(p string) bool {
	for _, have := range d.Providers {
		if have == p {
			return true
		}
	}
	return false
}

type profileSecretFields struct {
	Key                   string `json:"key"`
	AWSAccessKeyID        string `json:"aws_access_key_id"`
	AWSSecretAccessKey    string `json:"aws_secret_access_key"`
	AWSSessionToken       string `json:"aws_session_token"`
	GCPServiceAccountJSON string `json:"gcp_service_account_json"`
}

func (f profileSecretFields) trimmed() profileSecretFields {
	return profileSecretFields{
		Key:                   strings.TrimSpace(f.Key),
		AWSAccessKeyID:        strings.TrimSpace(f.AWSAccessKeyID),
		AWSSecretAccessKey:    strings.TrimSpace(f.AWSSecretAccessKey),
		AWSSessionToken:       strings.TrimSpace(f.AWSSessionToken),
		GCPServiceAccountJSON: strings.TrimSpace(f.GCPServiceAccountJSON),
	}
}

func (f profileSecretFields) hasCloudFields() bool {
	return f.AWSAccessKeyID != "" || f.AWSSecretAccessKey != "" || f.AWSSessionToken != "" || f.GCPServiceAccountJSON != ""
}

type profileShape struct {
	provider       string
	baseURL        string
	orgID          string
	authHeader     string
	region         string
	projectID      string
	smallFastModel string
}

func validateProfileShape(desc engineDescriptor, s profileShape) error {
	if !desc.supportsProvider(s.provider) {
		return apperr.Invalid("api.llm.errors.provider_not_supported",
			"engine", desc.Name, "provider", s.provider, "supported", strings.Join(desc.Providers, ", "))
	}
	if err := validateProfileBaseURL(s.baseURL); err != nil {
		return err
	}
	if engine.ProviderNeedsBaseURL(s.provider) && s.baseURL == "" {
		return apperr.Invalid("api.llm.errors.base_url_required", "provider", s.provider)
	}
	if !engine.ValidAuthHeader(s.authHeader) {
		return apperr.Invalid("api.llm.errors.auth_header_invalid", "api_key", engine.AuthHeaderAPIKey, "bearer", engine.AuthHeaderBearer)
	}
	cloud := engine.IsCloudProvider(s.provider)
	if s.authHeader != "" {
		switch {
		case cloud:
			return apperr.Invalid("api.llm.errors.auth_header_not_for_provider", "provider", s.provider)
		case !desc.AuthHeader:
			return apperr.Invalid("api.llm.errors.auth_header_fixed", "engine", desc.Label)
		case s.baseURL == "":
			// The provider's own API takes its key in its own header; a bearer key there is a 401
			// on every call.
			return apperr.Invalid("api.llm.errors.auth_header_needs_base_url")
		}
	}
	if !cloud {
		if s.region != "" || s.projectID != "" || s.smallFastModel != "" {
			return apperr.Invalid("api.llm.errors.cloud_fields_cloud_only")
		}
		return nil
	}
	if s.orgID != "" {
		return apperr.Invalid("api.llm.errors.org_id_not_for_provider", "provider", s.provider)
	}
	if s.region == "" {
		return apperr.Invalid("api.llm.errors.region_required", "provider", s.provider)
	}
	if s.provider == engine.ProviderVertex && s.projectID == "" {
		return apperr.Invalid("api.llm.errors.project_id_required")
	}
	return nil
}

type profileCredential struct {
	raw  string
	hint string
	set  bool
}

func buildProfileCredential(desc engineDescriptor, s profileShape, sec profileSecretFields, kind string) (profileCredential, error) {
	switch s.provider {
	case engine.ProviderBedrock:
		return bedrockCredential(sec)
	case engine.ProviderVertex:
		return vertexCredential(sec)
	}
	if sec.hasCloudFields() {
		return profileCredential{}, apperr.Invalid("api.llm.errors.cloud_credentials_cloud_only")
	}
	if sec.Key == "" {
		return profileCredential{}, nil
	}
	if s.baseURL == "" {
		if err := validateKeyAgainstEngineKinds(desc, kind, sec.Key); err != nil {
			return profileCredential{}, err
		}
	}
	return profileCredential{raw: sec.Key, hint: secretbox.Mask(sec.Key), set: true}, nil
}

func bedrockCredential(sec profileSecretFields) (profileCredential, error) {
	if sec.GCPServiceAccountJSON != "" {
		return profileCredential{}, apperr.Invalid("api.llm.errors.gcp_json_vertex_only")
	}
	pair := sec.AWSAccessKeyID != "" || sec.AWSSecretAccessKey != ""
	switch {
	case pair && (sec.AWSAccessKeyID == "" || sec.AWSSecretAccessKey == ""):
		return profileCredential{}, apperr.Invalid("api.llm.errors.aws_pair_incomplete")
	case sec.AWSSessionToken != "" && !pair:
		return profileCredential{}, apperr.Invalid("api.llm.errors.aws_session_token_needs_pair")
	case pair && sec.Key != "":
		return profileCredential{}, apperr.Invalid("api.llm.errors.bedrock_key_or_pair")
	}
	secret := engine.CloudSecret{
		AWSAccessKeyID:     sec.AWSAccessKeyID,
		AWSSecretAccessKey: sec.AWSSecretAccessKey,
		AWSSessionToken:    sec.AWSSessionToken,
		BedrockAPIKey:      sec.Key,
	}
	if secret.Empty() {
		return profileCredential{}, nil
	}
	raw, err := secret.Encode()
	if err != nil {
		return profileCredential{}, err
	}
	hint := secretbox.Mask(sec.Key)
	if pair {
		hint = secretbox.Mask(sec.AWSAccessKeyID)
	}
	return profileCredential{raw: raw, hint: hint, set: true}, nil
}

func vertexCredential(sec profileSecretFields) (profileCredential, error) {
	if sec.Key != "" || sec.AWSAccessKeyID != "" || sec.AWSSecretAccessKey != "" || sec.AWSSessionToken != "" {
		return profileCredential{}, apperr.Invalid("api.llm.errors.vertex_takes_gcp_json")
	}
	if sec.GCPServiceAccountJSON == "" {
		return profileCredential{}, nil
	}
	var sa struct {
		Type        string `json:"type"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	if err := json.Unmarshal([]byte(sec.GCPServiceAccountJSON), &sa); err != nil {
		return profileCredential{}, apperr.Invalid("api.llm.errors.gcp_json_invalid")
	}
	if sa.Type != "service_account" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return profileCredential{}, apperr.Invalid("api.llm.errors.gcp_json_not_service_account")
	}
	raw, err := engine.CloudSecret{GCPServiceAccountJSON: sec.GCPServiceAccountJSON}.Encode()
	if err != nil {
		return profileCredential{}, err
	}
	return profileCredential{raw: raw, hint: sa.ClientEmail, set: true}, nil
}

// URL credentials are refused: base_url is stored and returned in the clear.
func validateProfileBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return apperr.Invalid("api.llm.errors.base_url_not_absolute")
	}
	if u.User != nil {
		return apperr.Invalid("api.llm.errors.base_url_has_credentials")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) || llmHostListed(u.Hostname()) {
			return nil
		}
		return apperr.Invalid("api.llm.errors.base_url_http_not_allowed")
	default:
		return apperr.Invalid("api.llm.errors.base_url_https_required")
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Read per call, not cached: the gateway re-checks this env on every request and both must agree.
func llmHostListed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	for _, entry := range strings.Split(os.Getenv("OCTOPUS_LLM_ALLOW_HTTP_HOSTS"), ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		switch {
		case entry == "":
			continue
		case strings.HasPrefix(entry, "*."):
			if strings.HasSuffix(host, entry[1:]) {
				return true
			}
		case host == entry:
			return true
		}
	}
	return false
}

func (d engineDescriptor) federatedKind() (engine.KeyKind, bool) {
	for _, k := range d.KeyKinds {
		if k.Federated {
			return k, true
		}
	}
	return engine.KeyKind{}, false
}

func (s *Server) requestFederation(
	desc engineDescriptor, shape profileShape, sec profileSecretFields, kind string, fed *engine.FederationConfig,
) (engine.FederationConfig, bool, error) {
	fedKind, canFederate := desc.federatedKind()
	named := fed != nil && fed.Trimmed().Set()
	if !named && (!canFederate || kind != fedKind.ID) {
		return engine.FederationConfig{}, false, nil
	}
	if !canFederate {
		return engine.FederationConfig{}, false, apperr.Invalid("api.llm.errors.federation_not_supported", "engine", desc.Label)
	}
	if !named {
		return engine.FederationConfig{}, false, apperr.Invalid("api.llm.errors.federation_required", "kind", kind)
	}
	if shape.provider != desc.Provider || shape.baseURL != "" || shape.authHeader != "" {
		return engine.FederationConfig{}, false, apperr.Invalid("api.llm.errors.federation_own_provider")
	}
	if sec.Key != "" || sec.hasCloudFields() {
		return engine.FederationConfig{}, false, apperr.Invalid("api.llm.errors.federation_no_key")
	}
	cfg := fed.Trimmed()
	if err := cfg.Validate(); err != nil {
		return engine.FederationConfig{}, false, err
	}
	if s.workloadIdentity == nil {
		return engine.FederationConfig{}, false, apperr.Precondition("api.server.errors.secrets_store_unavailable")
	}
	if err := s.workloadIdentity.Ready(); err != nil {
		return engine.FederationConfig{}, false, err
	}
	return cfg, true, nil
}
