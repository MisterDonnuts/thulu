package credentials

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

type headerProvider struct{}

const HeaderProviderID = "http_header"

const (
	HeaderKindBearer = "bearer"
	HeaderKindBasic  = "basic"
	HeaderKindCustom = "custom"
)

func (p *headerProvider) Spec() ProviderSpec { return p.SpecIn(context.Background()) }

func (p *headerProvider) SpecIn(ctx context.Context) ProviderSpec {
	token := FieldSpec{
		Name:   "token",
		Label:  i18n.TCtx(ctx, "credentials.labels.field_token"),
		Secret: true,
	}
	return ProviderSpec{
		ID:    HeaderProviderID,
		Label: i18n.TCtx(ctx, "credentials.labels.provider_http_header"),
		Help:  i18n.TCtx(ctx, "credentials.hints.provider_http_header"),
		Kinds: []KindSpec{
			{
				ID:     HeaderKindBearer,
				Label:  i18n.TCtx(ctx, "credentials.labels.kind_header_bearer"),
				Help:   i18n.TCtx(ctx, "credentials.hints.kind_header_bearer"),
				Fields: []FieldSpec{token},
			},
			{
				ID:    HeaderKindBasic,
				Label: i18n.TCtx(ctx, "credentials.labels.kind_header_basic"),
				Help:  i18n.TCtx(ctx, "credentials.hints.kind_header_basic"),
				Fields: []FieldSpec{
					{
						Name:  "username",
						Label: i18n.TCtx(ctx, "credentials.labels.field_username"),
						Help:  i18n.TCtx(ctx, "credentials.hints.field_username"),
					},
					token,
				},
			},
			{
				ID:    HeaderKindCustom,
				Label: i18n.TCtx(ctx, "credentials.labels.kind_header_custom"),
				Help:  i18n.TCtx(ctx, "credentials.hints.kind_header_custom"),
				Fields: []FieldSpec{
					{
						Name:        "header_name",
						Label:       i18n.TCtx(ctx, "credentials.labels.field_header_name"),
						Placeholder: "X-Api-Key", //i18n:ignore a sample header name, not a message
					},
					{
						Name:   "header_value",
						Label:  i18n.TCtx(ctx, "credentials.labels.field_header_value"),
						Secret: true,
					},
				},
			},
		},
	}
}

func (p *headerProvider) Introspect(_ context.Context, kind string, payload map[string]string, _ []string) (Introspection, error) {
	if _, err := StaticHeaders(kind, payload); err != nil {
		return Introspection{}, err
	}
	out := Introspection{State: StateUnknown}
	switch kind {
	case HeaderKindBasic:
		out.Identity = strings.TrimSpace(payload["username"])
		out.Hint = secretbox.Mask(strings.TrimSpace(payload["token"]))
	case HeaderKindCustom:
		out.Identity = http.CanonicalHeaderKey(strings.TrimSpace(payload["header_name"]))
		out.Hint = secretbox.Mask(strings.TrimSpace(payload["header_value"]))
	default:
		out.Hint = secretbox.Mask(strings.TrimSpace(payload["token"]))
	}
	return out, nil
}

// Values with CR or LF are refused, not cleaned: they would inject an extra header into the
// upstream request.
func StaticHeaders(kind string, payload map[string]string) (http.Header, error) {
	get := func(k string) string { return strings.TrimSpace(payload[k]) }
	h := http.Header{}
	switch kind {
	case HeaderKindBearer:
		if get("token") == "" {
			return nil, apperr.Invalid("credentials.errors.token_required")
		}
		h.Set("Authorization", "Bearer "+get("token")) //i18n:ignore HTTP auth scheme
	case HeaderKindBasic:
		if get("username") == "" || get("token") == "" {
			return nil, apperr.Invalid("credentials.errors.basic_fields_required")
		}
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(get("username")+":"+get("token")))) //i18n:ignore HTTP auth scheme
	case HeaderKindCustom:
		name := get("header_name")
		if !ValidHeaderName(name) || get("header_value") == "" {
			return nil, apperr.Invalid("credentials.errors.header_fields_required")
		}
		h.Set(name, get("header_value"))
	default:
		return nil, apperr.Invalid("credentials.errors.unknown_kind", "kind", kind, "provider", HeaderProviderID)
	}
	for _, vs := range h {
		for _, v := range vs {
			if strings.ContainsAny(v, "\r\n") {
				return nil, apperr.Invalid("credentials.errors.header_value_newline")
			}
		}
	}
	return h, nil
}

// Only checks RFC 9110 token syntax; callers must refuse names the proxy owns itself (e.g. Host).
func ValidHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
