package llmgateway

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const MountPath = "/llm-gateway"

const (
	ProviderAnthropic        = "anthropic"
	ProviderOpenAI           = "openai"
	ProviderOpenAICompatible = "openai-compatible"
	ProviderBedrock          = "bedrock"
	ProviderVertex           = "vertex"
)

// The Anthropic API refuses requests over 32 MB; Bedrock and Vertex limits are lower.
const MaxSignedBody = 32 << 20

const RunKeyHeader = "X-Octopus-Gateway-Token"

const (
	AuthXAPIKey = "x-api-key"
	AuthBearer  = "bearer"
)

type Target struct {
	Upstream   *url.URL
	Credential string
	AuthStyle  string
	Headers    map[string]string
	// Only for upstreams the operator vouched for; otherwise internal addresses are refused (see
	// upstreamTransport).
	AllowPrivate bool
	Sign         func(ctx context.Context, out *http.Request, body []byte) error
	// A cloud credential can usually do more than call the model; the run may only call the model.
	Permit func(method, path string) error

	WorkspaceID string
	RunID       string
	Agent       string
	ProfileID   string
}

var (
	ErrUnauthorized error = apperr.Unauthenticated("llmgateway.errors.unknown_run_token")
	ErrForbidden    error = apperr.Forbidden("llmgateway.errors.route_forbidden")
)

// ErrUnauthorized/ErrForbidden error text reaches the client and must never carry a secret; other
// errors answer 503 and are only logged.
type Resolver func(ctx context.Context, token, provider string) (Target, error)

type Gateway struct {
	resolve Resolver
	proxy   *httputil.ReverseProxy
	logf    func(format string, args ...any)
	outcome func(ctx context.Context, t Target, status int)
}

// fn runs on the request goroutine after the response is written; it must be quick. Call before
// serving.
func (g *Gateway) OnOutcome(fn func(ctx context.Context, t Target, status int)) {
	g.outcome = fn
}

func New(resolve Resolver) (*Gateway, error) {
	if resolve == nil {
		return nil, apperr.Internal("llmgateway.errors.nil_resolver")
	}
	g := &Gateway{resolve: resolve, logf: log.Printf}
	g.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			t := targetFrom(pr.In.Context())
			pr.SetURL(t.Upstream)
			pr.Out.Host = t.Upstream.Host
			// Rewrite (unlike Director) adds no X-Forwarded-* headers, so the agent's in-cluster
			// address never reaches the provider.
		},
		Transport: newUpstreamTransport(),
		// Model responses stream over SSE; flush every write.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			t := targetFrom(r.Context())
			g.logf("[llm-gateway] %s", i18n.Log("llmgateway.log.upstream_failed", "host", t.Upstream.Host, "err", err))
			apperr.WriteJSON(w, r, http.StatusBadGateway, apperr.Unavailable("llmgateway.errors.upstream_unavailable"))
		},
	}
	return g, nil
}

type targetKey struct{}

func targetFrom(ctx context.Context) Target {
	if t, ok := ctx.Value(targetKey{}).(Target); ok {
		return t
	}
	return Target{Upstream: &url.URL{}}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	provider, rest, ok := splitProvider(r.URL.Path)
	if !ok {
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("llmgateway.errors.unknown_route"))
		return
	}
	token := extractToken(r)
	if token == "" {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("llmgateway.errors.missing_run_token"))
		return
	}
	target, err := g.resolve(r.Context(), token, provider)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnauthorized):
			apperr.WriteJSON(w, r, http.StatusUnauthorized, err)
		case errors.Is(err, ErrForbidden):
			apperr.WriteJSON(w, r, http.StatusForbidden, err)
		default:
			g.logf("[llm-gateway] %s", i18n.Log("llmgateway.log.resolve_failed", "err", err))
			apperr.WriteJSON(w, r, http.StatusServiceUnavailable, apperr.Unavailable("llmgateway.errors.resolve_failed"))
		}
		return
	}
	if target.Upstream == nil || target.Upstream.Host == "" || (target.Credential == "" && target.Sign == nil) {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("llmgateway.errors.no_usable_credential"))
		return
	}
	if target.Permit != nil {
		if err := target.Permit(r.Method, rest); err != nil {
			apperr.WriteJSON(w, r, http.StatusForbidden, err)
			return
		}
	}
	if target.Sign != nil && r.ContentLength > MaxSignedBody {
		apperr.WriteJSON(w, r, http.StatusRequestEntityTooLarge, apperr.TooLarge("llmgateway.errors.body_too_large"))
		return
	}

	// Delete every client credential header first, then set the real one. Signed routes get theirs
	// in the transport.
	for _, h := range scrubbedHeaders {
		r.Header.Del(h)
	}
	switch {
	case target.Sign != nil:
	case target.AuthStyle == AuthBearer:
		r.Header.Set("Authorization", "Bearer "+target.Credential) //i18n:ignore HTTP auth scheme
	default:
		r.Header.Set("x-api-key", target.Credential)
	}
	for k, v := range target.Headers {
		r.Header.Set(k, v)
	}

	// Keep the client's escaping: a Bedrock model id like anthropic.claude-v2:1 arrives as v2%3A1,
	// and SigV4 signs the path as sent.
	_, rawRest, _ := splitProvider(r.URL.EscapedPath())
	r.URL.Path = rest
	r.URL.RawPath = ""
	if rawRest != rest {
		r.URL.RawPath = rawRest
	}
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	g.proxy.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), targetKey{}, target)))
	// Never log the body: it is the user's prompt.
	g.logf("[llm-gateway] %s", i18n.Log("llmgateway.log.call",
		"workspace", target.WorkspaceID, "run", target.RunID, "agent", target.Agent, "profile", target.ProfileID,
		"method", r.Method, "host", target.Upstream.Host, "path", logx.Safe(rest),
		"status", rec.status, "elapsed", time.Since(start).Round(time.Millisecond)))
	if g.outcome != nil {
		g.outcome(r.Context(), target, rec.status)
	}
}

var scrubbedHeaders = []string{
	RunKeyHeader, "Authorization", "x-api-key", "OpenAI-Organization", //i18n:ignore HTTP header names
	"X-Amz-Security-Token", "X-Amz-Date", "X-Amz-Content-Sha256", "X-Goog-User-Project",
}

func splitProvider(p string) (provider, rest string, ok bool) {
	trimmed := strings.TrimPrefix(p, "/")
	provider, rest, _ = strings.Cut(trimmed, "/")
	switch provider {
	case ProviderAnthropic, ProviderOpenAI, ProviderOpenAICompatible, ProviderBedrock, ProviderVertex:
		return provider, "/" + rest, true
	default:
		return "", "", false
	}
}

func extractToken(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get(RunKeyHeader)); t != "" {
		return t
	}
	if t := strings.TrimSpace(r.Header.Get("x-api-key")); t != "" {
		return t
	}
	//i18n:ignore HTTP auth scheme
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(a, "Bearer ")) //i18n:ignore HTTP auth scheme
	}
	return ""
}

// Must pass Flush through or streaming responses buffer.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
