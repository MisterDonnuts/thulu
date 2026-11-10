package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

// Figma's /v1/me reports no scopes and no expiry, so capabilities stay nil (unrestricted, like a
// fine-grained GitHub PAT).
type figmaProvider struct{}

var figmaAPI = "https://api.figma.com"

func (p *figmaProvider) Spec() ProviderSpec { return p.SpecIn(context.Background()) }

func (p *figmaProvider) SpecIn(ctx context.Context) ProviderSpec {
	return ProviderSpec{
		ID:    "figma",
		Label: i18n.TCtx(ctx, "credentials.labels.provider_figma"),
		Help:  i18n.TCtx(ctx, "credentials.hints.provider_figma"),
		Kinds: []KindSpec{{
			ID:    "figma_pat",
			Label: i18n.TCtx(ctx, "credentials.labels.kind_figma_pat"),
			Fields: []FieldSpec{{
				Name:        "token",
				Label:       i18n.TCtx(ctx, "credentials.labels.field_token"),
				Secret:      true,
				Placeholder: i18n.TCtx(ctx, "credentials.hints.figma_token_placeholder"),
				Help:        i18n.TCtx(ctx, "credentials.hints.figma_token"),
			}},
		}},
	}
}

func (p *figmaProvider) Introspect(ctx context.Context, _ string, payload map[string]string, _ []string) (Introspection, error) {
	token := strings.TrimSpace(payload["token"])
	if token == "" {
		return Introspection{}, apperr.Invalid("credentials.errors.token_required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, figmaAPI+"/v1/me", http.NoBody)
	if err != nil {
		return Introspection{}, err
	}
	req.Header.Set("X-Figma-Token", token)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Introspection{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return Introspection{}, apperr.Wrap(err, "credentials.errors.read_response", "service", "/v1/me")
	}

	out := Introspection{Hint: secretbox.Mask(token)}
	if resp.StatusCode != http.StatusOK {
		// Figma returns 403 for both revoked tokens and tokens without current_user:read; its
		// message says which.
		out.State = StateInvalid
		out.Err = figmaErrorMessage(body, resp.StatusCode)
		return out, nil
	}

	var me struct {
		Handle string `json:"handle"`
		Email  string `json:"email"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return Introspection{}, apperr.Wrap(err, "credentials.errors.decode_response", "service", "/v1/me")
	}
	out.State = StateValid
	out.Identity = me.Email
	if out.Identity == "" {
		out.Identity = me.Handle
	}
	return out, nil
}

// Figma uses two error shapes: {"status":403,"err":"..."} and {"message":"..."}.
func figmaErrorMessage(body []byte, status int) string {
	var errResp struct {
		Err     string `json:"err"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil {
		if errResp.Err != "" {
			return errResp.Err
		}
		if errResp.Message != "" {
			return errResp.Message
		}
	}
	return fmt.Sprintf("HTTP %d", status)
}
