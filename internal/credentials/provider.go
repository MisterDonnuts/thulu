package credentials

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Introspect must never mutate anything at the provider and must never return the secret it was
// given.
type Provider interface {
	Spec() ProviderSpec

	// An error means the call could not be made; a credential the provider rejected returns
	// StateInvalid with a nil error.
	Introspect(ctx context.Context, kind string, payload map[string]string, want []string) (Introspection, error)
}

type localizedProvider interface {
	SpecIn(ctx context.Context) ProviderSpec
}

func specIn(ctx context.Context, p Provider) ProviderSpec {
	if lp, ok := p.(localizedProvider); ok {
		return lp.SpecIn(ctx)
	}
	return p.Spec()
}

type FieldSpec struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Secret      bool   `json:"secret,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
	Multiline   bool   `json:"multiline,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
}

type KindSpec struct {
	ID     string      `json:"id"`
	Label  string      `json:"label"`
	Help   string      `json:"help,omitempty"`
	Fields []FieldSpec `json:"fields"`
}

type ProviderSpec struct {
	ID      string     `json:"id"`
	Label   string     `json:"label"`
	Help    string     `json:"help,omitempty"`
	Connect bool       `json:"connect,omitempty"`
	Kinds   []KindSpec `json:"kinds"`
}

// Populated in NewRegistry and read-only afterwards, so no lock is needed.
type Registry struct {
	byID map[string]Provider
}

func NewRegistry() *Registry {
	r := &Registry{byID: map[string]Provider{}}
	r.Register(&githubProvider{})
	r.Register(&awsProvider{})
	r.Register(&gcpProvider{})
	r.Register(&headerProvider{})
	r.Register(&oauthProvider{})
	r.Register(&figmaProvider{})
	return r
}

func (r *Registry) Register(p Provider) { r.byID[p.Spec().ID] = p }

func (r *Registry) Get(id string) (Provider, bool) {
	p, ok := r.byID[id]
	return p, ok
}

func (r *Registry) Specs() []ProviderSpec { return r.SpecsIn(context.Background()) }

func (r *Registry) SpecsIn(ctx context.Context) []ProviderSpec {
	out := make([]ProviderSpec, 0, len(r.byID))
	for _, p := range r.byID {
		out = append(out, specIn(ctx, p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) Introspect(ctx context.Context, provider, kind string, payload map[string]string, want []string) (Introspection, error) {
	p, ok := r.Get(provider)
	if !ok {
		return Introspection{}, apperr.Invalid("credentials.errors.unknown_provider", "provider", provider)
	}
	return p.Introspect(ctx, kind, payload, want)
}

func (r *Registry) ValidatePayload(provider, kind string, payload map[string]string) error {
	p, ok := r.Get(provider)
	if !ok {
		return apperr.Invalid("credentials.errors.unknown_provider", "provider", provider)
	}
	spec := p.Spec()
	for _, k := range spec.Kinds {
		if k.ID != kind {
			continue
		}
		for _, f := range k.Fields {
			if f.Optional {
				continue
			}
			if strings.TrimSpace(payload[f.Name]) == "" {
				return apperr.Invalid("credentials.errors.field_required", "field", f.Label)
			}
		}
		return nil
	}
	return apperr.Invalid("credentials.errors.unknown_kind", "kind", kind, "provider", provider)
}

// Runs synchronously inside the operator's HTTP request.
const introspectTimeout = 15 * time.Second

// Every URL is a compile-time default or an operator env override; no part may come from user input
// (SSRF).
var httpClient = &http.Client{Timeout: introspectTimeout}

func wantForProvider(provider string, want []string) []string {
	prefix := provider + ":"
	out := make([]string, 0, len(want))
	seen := map[string]bool{}
	for _, w := range want {
		if !strings.HasPrefix(w, prefix) {
			continue
		}
		bare := strings.TrimPrefix(w, prefix)
		if bare == "" || seen[bare] {
			continue
		}
		seen[bare] = true
		out = append(out, bare)
	}
	sort.Strings(out)
	return out
}

func namespaced(provider string, bare []string) []string {
	out := make([]string, 0, len(bare))
	for _, b := range bare {
		out = append(out, provider+":"+b)
	}
	sort.Strings(out)
	return out
}
