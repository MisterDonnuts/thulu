package daemon

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/api"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	"github.com/MisterDonnuts/thulu/internal/engine/codex"
	"github.com/MisterDonnuts/thulu/internal/engine/opencode"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/llmgateway"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type llmAuthSetup struct {
	allowHost bool
	gateway   gatewayConfig
	err       error
}

// OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL is off by default: on a shared daemon the host key would bill
// every tenant's runs to the operator.
func setupLLMAuth(agentsDir string) llmAuthSetup {
	gw, err := gatewayConfigFromEnv(os.Getenv)
	s := llmAuthSetup{allowHost: os.Getenv("OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL") == "1", gateway: gw, err: err}
	envFile := ""
	if s.allowHost {
		envFile = filepath.Join(agentsDir, ".env")
		log.Printf("[daemon] %s", i18n.Log("daemon.log.host_llm_credential_enabled", "file", envFile))
	}
	engine.Replace(claudecode.New(claudecode.Config{EnvFile: envFile}))
	engine.Replace(codex.New(codex.Config{EnvFile: envFile}))
	engine.Replace(opencode.New(opencode.Config{EnvFile: envFile}))
	return s
}

// Must run before srv.Start.
func (s llmAuthSetup) wire(ctx context.Context, srv *api.Server) error {
	if s.err != nil {
		return s.err
	}
	srv.Dispatcher().WithHostLLMCredential(s.allowHost)
	// Set even when the gateway is off: profiles are validated against these upstreams.
	srv.SetLLMUpstreams(s.gateway.upstreams)
	return startLLMGateway(ctx, srv, s.gateway)
}

type gatewayConfig struct {
	enabled   bool
	addr      string
	agentURL  string
	upstreams map[string]string
}

// auto enables the gateway when OCTOPUS_LLM_GATEWAY_ADDR or _URL is set; the Helm chart sets ADDR
// in kubernetes mode.
func gatewayConfigFromEnv(getenv func(string) string) (gatewayConfig, error) {
	cfg := gatewayConfig{
		addr:     strings.TrimSpace(getenv("OCTOPUS_LLM_GATEWAY_ADDR")),
		agentURL: strings.TrimSpace(getenv("OCTOPUS_LLM_GATEWAY_URL")),
		upstreams: map[string]string{
			llmgateway.ProviderAnthropic: "https://api.anthropic.com",
			llmgateway.ProviderOpenAI:    "https://api.openai.com/v1",
		},
	}
	switch mode := strings.ToLower(strings.TrimSpace(getenv("OCTOPUS_LLM_GATEWAY"))); mode {
	case "", "auto":
		cfg.enabled = cfg.addr != "" || cfg.agentURL != ""
	case "on", "1", "true":
		cfg.enabled = true
	case "off", "0", "false":
		cfg.enabled = false
	default:
		// A typo in a security control must stop the boot, not quietly pick one of the two
		// behaviors.
		return cfg, apperr.Invalid("daemon.errors.gateway_mode_invalid", "mode", mode)
	}
	for provider, env := range map[string]string{
		llmgateway.ProviderAnthropic: "OCTOPUS_LLM_UPSTREAM",
		llmgateway.ProviderOpenAI:    "OCTOPUS_LLM_OPENAI_UPSTREAM",
	} {
		v := strings.TrimSpace(getenv(env))
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return cfg, apperr.Invalid("daemon.errors.upstream_not_url", "env", env, "value", v)
		}
		cfg.upstreams[provider] = strings.TrimRight(v, "/")
	}
	if cfg.agentURL != "" {
		if u, err := url.Parse(cfg.agentURL); err != nil || u.Host == "" {
			return cfg, apperr.Invalid("daemon.errors.gateway_url_not_absolute", "value", cfg.agentURL)
		}
	}
	return cfg, nil
}

// The provider key never enters an agent container: runs hold a scoped token the gateway swaps at
// the edge.
// Mounted at llmgateway.MountPath on the API listener because the kubernetes egress policy already
// allows it. Must run before srv.Start.
func startLLMGateway(ctx context.Context, srv *api.Server, cfg gatewayConfig) error {
	if !cfg.enabled {
		log.Printf("[llm-gateway] %s", i18n.Log("daemon.log.llm_gateway_off"))
		return nil
	}
	gw, err := llmgateway.New(srv.LLMGatewayResolver(cfg.upstreams))
	if err != nil {
		return err
	}
	gw.OnOutcome(srv.LLMGatewayOutcome)
	if err := srv.MountLLMGateway(gw); err != nil {
		return err
	}
	srv.Dispatcher().WithLLMGateway(cfg.agentURL)
	log.Printf("[llm-gateway] %s", i18n.Log("daemon.log.llm_gateway_on", "url", srv.Dispatcher().LLMGatewayURL()))

	if cfg.addr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle(llmgateway.MountPath+"/", http.StripPrefix(llmgateway.MountPath, gw))
	mux.Handle("/", gw)
	server := &http.Server{Addr: cfg.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("[llm-gateway] %s", i18n.Log("daemon.log.llm_gateway_listening", "addr", cfg.addr))
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("[llm-gateway] %s", i18n.Log("daemon.log.llm_gateway_serve_failed", "err", serveErr))
		}
	}()
	//nolint:gosec // G118: the shutdown goroutine intentionally uses a fresh context; the request ctx is already canceled here.
	go func() {
		<-ctx.Done()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		//nolint:contextcheck // shutCtx is fresh; the request ctx is already canceled (mirrors the daemon's own shutdown).
		logx.Warn(server.Shutdown(shutCtx), "daemon.log.llm_gateway_shutdown")
	}()
	return nil
}
