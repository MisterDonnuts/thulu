package api

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/cloudsign"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/llmgateway"
	"github.com/MisterDonnuts/thulu/internal/secrets"
)

var (
	bedrockModelPath = regexp.MustCompile(`^/model/([^/]+)/(invoke|invoke-with-response-stream|count-tokens)$`)
	vertexModelPath  = regexp.MustCompile(`^/projects/([^/]+)/locations/([^/]+)/publishers/anthropic/models/([^/:]+):(rawPredict|streamRawPredict)$`)
)

func resolveCloudGatewayTarget(
	res secrets.ResolvedLLM, provider string, caps engine.Capabilities, base llmgateway.Target,
) (llmgateway.Target, error) {
	if res.Provider != provider {
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.profile_wrong_provider",
			"profile_provider", firstNonEmpty(res.Provider, caps.Provider), "provider", provider)
	}
	if !caps.GatewayCanPresent(res.Provider, res.Credential, res.BaseURL) {
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.credential_not_served")
	}
	return cloudGatewayTarget(res, base)
}

func cloudGatewayTarget(res secrets.ResolvedLLM, base llmgateway.Target) (llmgateway.Target, error) {
	secret, err := engine.DecodeCloudSecret(res.Credential)
	if err != nil {
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.cloud_credential_unreadable")
	}
	if secret.Empty() && !engine.AmbientCloudIdentityAllowed(os.Getenv) {
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.no_cloud_credential")
	}
	region := strings.TrimSpace(res.Region)
	if !cloudsign.ValidRegion(region) {
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.invalid_region")
	}

	var def string
	switch res.Provider {
	case engine.ProviderBedrock:
		def = "https://bedrock-runtime." + region + ".amazonaws.com"
		base.Permit = permitBedrock
		base.Sign = bedrockSigner(secret, region)
	case engine.ProviderVertex:
		project := strings.TrimSpace(res.ProjectID)
		if project == "" {
			return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.vertex_no_project")
		}
		def = "https://" + region + "-aiplatform.googleapis.com/v1"
		if region == "global" {
			def = "https://aiplatform.googleapis.com/v1"
		}
		base.Permit = permitVertex(project, region)
		base.Sign = vertexSigner(secret)
	default:
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden,
			"api.gateway.errors.provider_not_signed", "provider", res.Provider)
	}
	upstream, allowPrivate, err := gatewayUpstream(res.BaseURL, def)
	if err != nil {
		return llmgateway.Target{}, err
	}
	// With PrivateLink or Private Service Connect the cloud's public name resolves to a private
	// address, which must stay reachable.
	base.Upstream, base.AllowPrivate = upstream, allowPrivate
	return base, nil
}

func permitBedrock(method, path string) error {
	m := bedrockModelPath.FindStringSubmatch(path)
	if method != http.MethodPost || m == nil || dotSegment(m[1]) {
		return apperr.Forbidden("api.gateway.errors.bedrock_invocations_only")
	}
	return nil
}

func permitVertex(project, region string) func(method, path string) error {
	return func(method, path string) error {
		m := vertexModelPath.FindStringSubmatch(path)
		if method != http.MethodPost || m == nil || dotSegment(m[3]) {
			return apperr.Forbidden("api.gateway.errors.vertex_anthropic_only")
		}
		if m[1] != project || m[2] != region {
			return apperr.Forbidden("api.gateway.errors.vertex_wrong_project", "project", project, "region", region)
		}
		return nil
	}
}

// An upstream would resolve "." and ".." to another path than the one vetted.
func dotSegment(s string) bool { return s == "." || s == ".." }

func bedrockSigner(secret engine.CloudSecret, region string) func(context.Context, *http.Request, []byte) error {
	if key := strings.TrimSpace(secret.BedrockAPIKey); key != "" {
		return func(_ context.Context, out *http.Request, _ []byte) error {
			out.Header.Set("Authorization", "Bearer "+key) //i18n:ignore HTTP auth scheme
			return nil
		}
	}
	static := cloudsign.AWSCreds{
		AccessKeyID:     strings.TrimSpace(secret.AWSAccessKeyID),
		SecretAccessKey: strings.TrimSpace(secret.AWSSecretAccessKey),
		SessionToken:    strings.TrimSpace(secret.AWSSessionToken),
	}
	return func(ctx context.Context, out *http.Request, body []byte) error {
		creds := static
		if creds.AccessKeyID == "" {
			ambient, err := cloudsign.AmbientAWS(ctx)
			if err != nil {
				return err
			}
			creds = ambient
		}
		return cloudsign.SignV4(out, body, creds, region, "bedrock", time.Now())
	}
}

func vertexSigner(secret engine.CloudSecret) func(context.Context, *http.Request, []byte) error {
	key := strings.TrimSpace(secret.GCPServiceAccountJSON)
	return func(ctx context.Context, out *http.Request, _ []byte) error {
		var (
			tok string
			err error
		)
		if key != "" {
			tok, err = cloudsign.GCPServiceAccountToken(ctx, key)
		} else {
			tok, err = cloudsign.AmbientGCPToken(ctx)
		}
		if err != nil {
			return err
		}
		out.Header.Set("Authorization", "Bearer "+tok) //i18n:ignore HTTP auth scheme
		return nil
	}
}
