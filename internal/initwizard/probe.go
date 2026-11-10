package initwizard

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/db"
)

const probeTimeout = 8 * time.Second

type OIDCDiscovery struct {
	Issuer        string   `json:"issuer"`
	AuthURL       string   `json:"authorization_endpoint"`
	TokenURL      string   `json:"token_endpoint"`
	JWKSURL       string   `json:"jwks_uri"`
	UserInfoURL   string   `json:"userinfo_endpoint"`
	ScopesSupport []string `json:"scopes_supported"`
}

func ProbeOIDC(ctx context.Context, issuer string) (*OIDCDiscovery, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return nil, apperr.Invalid("initwizard.errors.issuer_empty")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	url := issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, apperr.Wrap(err, "initwizard.errors.build_discovery_request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindUnavailable, err, "initwizard.errors.reach", "url", url)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apperr.Unavailable("initwizard.errors.discovery_status", "url", url, "status", resp.Status)
	}
	var doc OIDCDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, apperr.Wrap(err, "initwizard.errors.parse_discovery")
	}
	if doc.Issuer != "" && strings.TrimRight(doc.Issuer, "/") != issuer {
		return nil, apperr.Invalid("initwizard.errors.issuer_mismatch", "entered", issuer, "declared", doc.Issuer)
	}
	return &doc, nil
}

func ProbePostgres(ctx context.Context, dsn string) error {
	handle, err := db.Open(dsn)
	if err != nil {
		return apperr.Wrap(err, "initwizard.errors.open_database")
	}
	defer handle.Close()

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		return apperr.WrapKind(apperr.KindUnavailable, err, "initwizard.errors.ping_database")
	}
	return nil
}

func ProbeHTTP(ctx context.Context, url string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, apperr.Wrap(err, "initwizard.errors.build_request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, apperr.WrapKind(apperr.KindUnavailable, err, "initwizard.errors.reach", "url", url)
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func WaitForHTTP(ctx context.Context, url string, want int, timeout time.Duration, progress func(attempt int, status int, err error)) error {
	deadline := time.Now().Add(timeout)
	for attempt := 1; ; attempt++ {
		status, err := ProbeHTTP(ctx, url)
		if progress != nil {
			progress(attempt, status, err)
		}
		if err == nil && status == want {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return apperr.WrapKind(apperr.KindTimeout, err, "initwizard.errors.never_came_up", "url", url, "timeout", timeout)
			}
			return apperr.Timeout("initwizard.errors.wrong_status", "url", url, "status", status, "want", want, "timeout", timeout)
		}
		select {
		case <-ctx.Done():
			return apperr.Wrap(ctx.Err(), "initwizard.errors.waiting_for", "url", url)
		case <-time.After(2 * time.Second):
		}
	}
}

func DockerReachable(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return apperr.Unavailable("initwizard.errors.docker_unreachable", "output", strings.TrimSpace(string(out)))
	}
	return nil
}

// The Docker daemon's arch, not this process's: on a Mac the CLI talks to a Linux VM.
func DockerArch(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Arch}}").Output()
	if err != nil {
		return ""
	}
	switch arch := strings.TrimSpace(string(out)); arch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return arch
	}
}

func ImagePresent(ctx context.Context, ref string) bool {
	if strings.TrimSpace(ref) == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	//nolint:gosec // ref comes from the interview's own image step, not from a shell
	return exec.CommandContext(ctx, "docker", "image", "inspect", ref).Run() == nil
}

// The socket's group differs per host (991 Colima, 999 most Linux) and on macOS lives inside the
// VM,
// so only a container mounting it can read it; a wrong GID crash-loops the unprivileged daemon.
func DockerSocketGID(ctx context.Context, image string) string {
	if strings.TrimSpace(image) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	//nolint:gosec // image comes from the interview's own image step, not from a shell
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"--entrypoint", "stat", image, "-c", "%g", "/var/run/docker.sock").Output()
	if err != nil {
		return ""
	}
	gid := strings.TrimSpace(string(out))
	if _, convErr := strconv.Atoi(gid); convErr != nil {
		return ""
	}
	return gid
}

func KubeContext(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	// Stdout only: a credential plugin (gke-gcloud-auth-plugin with an expired login) writes its
	// complaint to stderr even when the context resolves, and that must stay out of the check's line.
	cmd := exec.CommandContext(ctx, "kubectl", "config", "current-context")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", apperr.Unavailable("initwizard.errors.no_kube_context", "output", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func RuntimeClassPresent(ctx context.Context, name string) (bool, error) {
	if name == "" {
		return true, nil
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	//nolint:gosec // fixed argv; `name` is one of the wizard's own menu options
	out, err := exec.CommandContext(ctx, "kubectl", "get", "runtimeclass", name, "-o", "name").CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "NotFound") {
			return false, nil
		}
		return false, apperr.Unavailable("initwizard.errors.check_runtimeclass", "name", name, "output", strings.TrimSpace(string(out)))
	}
	return true, nil
}

func WaitForPostgres(ctx context.Context, dsn string, timeout time.Duration, progress func()) error {
	deadline := time.Now().Add(timeout)
	for {
		err := ProbePostgres(ctx, dsn)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return apperr.WrapKind(apperr.KindTimeout, err, "initwizard.errors.postgres_never_ready", "timeout", timeout)
		}
		if progress != nil {
			progress()
		}
		select {
		case <-ctx.Done():
			return apperr.Wrap(ctx.Err(), "initwizard.errors.waiting_for_postgres")
		case <-time.After(2 * time.Second):
		}
	}
}
