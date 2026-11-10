package gitremote

import (
	"context"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: Httpoxy (CVE-2016-5386) is mitigated in Go >=1.6.3 (cgi strips the Proxy-header proxy env); built on Go 1.26.
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Authorizer func(r *http.Request, repo string) bool

type Resolver func(ctx context.Context, repoPath string) (repoDir string, ok bool)

type Handler struct {
	root      string
	backend   string
	authorize Authorizer
	resolver  Resolver
}

func New(root string, authorize Authorizer) (*Handler, error) {
	if authorize == nil {
		return nil, apperr.Internal("gitremote.errors.authorize_nil")
	}
	backend, err := backendPath()
	if err != nil {
		return nil, err
	}
	return &Handler{root: root, backend: backend, authorize: authorize}, nil
}

func (h *Handler) WithResolver(fn Resolver) *Handler {
	h.resolver = fn
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	repoPath, suffix := splitGitPath(r.URL.Path)
	if repoPath == "" {
		http.NotFound(w, r)
		return
	}
	if !h.authorize(r, repoPath) {
		// git only sends the credentials embedded in the clone URL after a Basic challenge.
		//i18n:ignore HTTP auth challenge header value, not a message
		w.Header().Set("WWW-Authenticate", `Basic realm="octopus-git"`)
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("gitremote.errors.unauthorized"))
		return
	}

	projectRoot := h.root
	if h.resolver != nil {
		dir, ok := h.resolver(r.Context(), repoPath)
		if !ok {
			http.NotFound(w, r)
			return
		}
		projectRoot = filepath.Dir(dir)
		r = r.Clone(r.Context())
		r.URL.Path = "/" + filepath.Base(dir) + suffix
	}

	cgih := &cgi.Handler{
		Path: h.backend,
		Env: []string{
			"GIT_PROJECT_ROOT=" + projectRoot,
			// Safe to export every repo: authorize gates the request and PATH_INFO pins the single
			// resolved repo.
			"GIT_HTTP_EXPORT_ALL=1",
			// git-http-backend refuses receive-pack (push) when REMOTE_USER is empty.
			"REMOTE_USER=octopus-agent",
		},
	}
	cgih.ServeHTTP(w, r)
}

var gitServiceMarkers = []string{"/info/refs", "/git-upload-pack", "/git-receive-pack", "/HEAD", "/objects/"}

func splitGitPath(p string) (repoPath, suffix string) {
	for _, m := range gitServiceMarkers {
		if i := strings.Index(p, m); i >= 0 {
			return strings.Trim(p[:i], "/"), p[i:]
		}
	}
	return strings.Trim(p, "/"), ""
}

// Alpine ships git-http-backend in the separate git-daemon package. Fail here so the daemon
// disables the transport at startup instead of 500ing on the first agent clone.
func backendPath() (string, error) {
	var candidates []string
	if out, err := exec.CommandContext(context.Background(), "git", "--exec-path").Output(); err == nil {
		if execPath := strings.TrimSpace(string(out)); execPath != "" {
			candidates = append(candidates, filepath.Join(execPath, "git-http-backend"))
		}
	}
	candidates = append(candidates,
		"/usr/libexec/git-core/git-http-backend",
		"/usr/lib/git-core/git-http-backend",
	)
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", apperr.Unavailable("gitremote.errors.backend_not_found", "candidates", candidates)
}
