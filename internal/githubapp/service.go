package githubapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

//nolint:gosec // G101: false positives — these are environment variable NAMES, not credential values
const (
	envAppID         = "OCTOPUS_GITHUB_APP_ID"
	envSlug          = "OCTOPUS_GITHUB_APP_SLUG"
	envClientID      = "OCTOPUS_GITHUB_APP_CLIENT_ID"
	envClientSecret  = "OCTOPUS_GITHUB_APP_CLIENT_SECRET"
	envPrivateKey    = "OCTOPUS_GITHUB_APP_PRIVATE_KEY"
	envPrivateKeyFin = "OCTOPUS_GITHUB_APP_PRIVATE_KEY_FILE"
	envWebhookSecret = "OCTOPUS_GITHUB_APP_WEBHOOK_SECRET"
)

// Refresh early so an in-flight git push never straddles GitHub's expiry.
const tokenExpiryMargin = 5 * time.Minute

type Service struct {
	store *Store
	httpc *http.Client

	mu    sync.RWMutex
	cache map[string]cachedToken
	// The PUT/DELETE credential handlers must call InvalidateConfig; env-sourced config only
	// changes on restart.
	cfg       *AppConfig
	cfgSource string

	noChecks map[int64]time.Time

	apiBase   string
	oauthBase string
	now       func() time.Time
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

func NewService(store *Store) *Service {
	return &Service{
		store:     store,
		httpc:     &http.Client{Timeout: 15 * time.Second},
		cache:     map[string]cachedToken{},
		noChecks:  map[int64]time.Time{},
		apiBase:   githubcfg.APIURL(),
		oauthBase: githubcfg.WebURL(),
		now:       time.Now,
	}
}

func (s *Service) config(ctx context.Context) (*AppConfig, string, error) {
	s.mu.RLock()
	if s.cfg != nil {
		cfg, src := s.cfg, s.cfgSource
		s.mu.RUnlock()
		return cfg, src, nil
	}
	s.mu.RUnlock()

	cfg, src, err := s.loadConfig(ctx)
	if err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	s.cfg, s.cfgSource = cfg, src
	s.mu.Unlock()
	return cfg, src, nil
}

func (s *Service) loadConfig(ctx context.Context) (*AppConfig, string, error) {
	if cfg := configFromEnv(); cfg != nil {
		return cfg, "env", nil
	}
	if s.store == nil {
		return nil, "", ErrNotConfigured
	}
	cfg, err := s.store.GetConfig(ctx)
	if err != nil {
		return nil, "", err
	}
	return cfg, "db", nil
}

// Tokens minted under removed credentials stay valid on GitHub for up to an hour, so the token
// cache is cleared too.
func (s *Service) InvalidateConfig() {
	s.mu.Lock()
	s.cfg, s.cfgSource = nil, ""
	clear(s.cache)
	s.mu.Unlock()
}

func configFromEnv() *AppConfig {
	idStr := strings.TrimSpace(os.Getenv(envAppID))
	if idStr == "" {
		return nil
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		logx.Warn(apperr.Invalid("githubapp.errors.unparseable_env", "name", envAppID, "value", idStr), "githubapp.log.env_config")
		return nil
	}
	key := os.Getenv(envPrivateKey)
	if key == "" {
		if path := strings.TrimSpace(os.Getenv(envPrivateKeyFin)); path != "" {
			raw, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied config path, same trust as the env var itself
			if err != nil {
				logx.Warn(err, "githubapp.log.read_key_file_failed", "name", envPrivateKeyFin)
				return nil
			}
			key = string(raw)
		}
	}
	if strings.TrimSpace(key) == "" {
		logx.Warn(apperr.Invalid("githubapp.errors.env_missing_key"), "githubapp.log.env_config")
		return nil
	}
	return &AppConfig{
		AppID:         id,
		Slug:          strings.TrimSpace(os.Getenv(envSlug)),
		ClientID:      strings.TrimSpace(os.Getenv(envClientID)),
		ClientSecret:  strings.TrimSpace(os.Getenv(envClientSecret)),
		PrivateKey:    key,
		WebhookSecret: strings.TrimSpace(os.Getenv(envWebhookSecret)),
	}
}

func (s *Service) Configured(ctx context.Context) bool {
	_, _, err := s.config(ctx)
	return err == nil
}

type AppStatus struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
	AppID      int64  `json:"app_id,omitempty"`
	Slug       string `json:"slug,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	InstallURL string `json:"install_url,omitempty"`
}

func (s *Service) Status(ctx context.Context) AppStatus {
	cfg, source, err := s.config(ctx)
	if err != nil {
		return AppStatus{Configured: false}
	}
	st := AppStatus{
		Configured: true,
		Source:     source,
		AppID:      cfg.AppID,
		Slug:       cfg.Slug,
		ClientID:   cfg.ClientID,
	}
	if cfg.Slug != "" {
		st.InstallURL = githubcfg.AppInstallURL(cfg.Slug)
	}
	return st
}

func (s *Service) BotLogin(ctx context.Context) string {
	cfg, _, err := s.config(ctx)
	if err != nil || cfg.Slug == "" {
		return "github-app[bot]"
	}
	return cfg.Slug + "[bot]"
}

// A token never carries more than its caller asked for: agents can fetch their project's token from
// the daemon.
type Permissions map[string]string

var (
	PermsMetadata     = Permissions{"metadata": "read"}
	PermsClone        = Permissions{"metadata": "read", "contents": "read"}
	PermsPush         = Permissions{"metadata": "read", "contents": "write", "pull_requests": "write"}
	PermsAgentTools   = Permissions{"metadata": "read", "contents": "write", "pull_requests": "write", "actions": "read"}
	PermsActionsWrite = Permissions{"metadata": "read", "contents": "write", "pull_requests": "write", "actions": "write"}
	// GitHub files merging under "Contents: write"; without it the chat's token cannot merge around
	// the person's confirmation.
	PermsDelegatedChat    = Permissions{"metadata": "read", "contents": "read", "pull_requests": "write", "actions": "read"}
	PermsMerge            = Permissions{"metadata": "read", "contents": "write", "pull_requests": "write"}
	PermsDeploymentReview = Permissions{"metadata": "read", "actions": "read", "deployments": "write"}
	PermsPRStatus         = Permissions{"metadata": "read", "pull_requests": "read", "checks": "read", "actions": "read"}
	PermsPRStatusActions  = Permissions{"metadata": "read", "pull_requests": "read", "actions": "read"}
)

var (
	PermDeploymentsWrite = Permissions{"deployments": "write"}
	PermWorkflowsWrite   = Permissions{"workflows": "write"}
	// An installation without "Checks: read" refuses the whole mint (422), so only the tool that
	// reads checks asks for it.
	PermChecksRead = Permissions{"checks": "read"}
)

const EnvAllowWorkflowWrites = "OCTOPUS_GITHUB_ALLOW_WORKFLOW_WRITES"

// Off by default: a pushed workflow runs with the repository's secrets, so allowing it lets whoever
// steers the agent read them.
func WorkflowWritesAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvAllowWorkflowWrites))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (p Permissions) With(extra Permissions) Permissions {
	out := make(Permissions, len(p)+len(extra))
	for k, v := range p {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (p Permissions) String() string { return p.key() }

func (p Permissions) key() string {
	parts := make([]string, 0, len(p))
	for k, v := range p {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (s *Service) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	return s.MintToken(ctx, installationID, "", PermsMetadata)
}

func (s *Service) InstallationTokenForRepo(ctx context.Context, installationID int64, repo string) (string, error) {
	return s.MintToken(ctx, installationID, repo, PermsPush)
}

// Errors are returned, never answered with a wider token.
func (s *Service) MintToken(ctx context.Context, installationID int64, repo string, perms Permissions) (string, error) {
	if len(perms) == 0 {
		return "", apperr.Invalid("githubapp.errors.mint_no_permissions")
	}
	return s.installationToken(ctx, installationID, repo, perms)
}

func (s *Service) installationToken(ctx context.Context, installationID int64, repo string, perms Permissions) (string, error) {
	key := strconv.FormatInt(installationID, 10) + "/" + repo + "/" + perms.key()
	s.mu.RLock()
	if c, ok := s.cache[key]; ok && s.now().Before(c.expiresAt.Add(-tokenExpiryMargin)) {
		s.mu.RUnlock()
		return c.token, nil
	}
	s.mu.RUnlock()

	cfg, _, err := s.config(ctx)
	if err != nil {
		return "", err
	}
	jwt, err := appJWT(cfg.AppID, cfg.PrivateKey, s.now())
	if err != nil {
		return "", err
	}
	req := map[string]any{"permissions": perms}
	if repo != "" {
		req["repositories"] = []string{repo}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	body := strings.NewReader(string(payload))
	target := fmt.Sprintf("%s/app/installations/%d/access_tokens", s.apiBase, installationID)
	var resp struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	//i18n:ignore HTTP auth scheme
	if err := s.doJSON(ctx, http.MethodPost, target, "Bearer "+jwt, body, http.StatusCreated, &resp); err != nil {
		return "", apperr.Wrap(err, "githubapp.errors.mint_failed", "installation", installationID)
	}
	s.mu.Lock()
	for k, c := range s.cache {
		if !s.now().Before(c.expiresAt) {
			delete(s.cache, k)
		}
	}
	s.cache[key] = cachedToken{token: resp.Token, expiresAt: resp.ExpiresAt}
	s.mu.Unlock()
	return resp.Token, nil
}

func (s *Service) lacksChecks(installationID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	until, ok := s.noChecks[installationID]
	return ok && s.now().Before(until)
}

func (s *Service) markLacksChecks(installationID int64, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noChecks == nil {
		s.noChecks = map[int64]time.Time{}
	}
	now := s.now()
	for id, until := range s.noChecks {
		if !now.Before(until) {
			delete(s.noChecks, id)
		}
	}
	s.noChecks[installationID] = now.Add(ttl)
}

type AppMeta struct {
	ID          int64             `json:"id"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Permissions map[string]string `json:"permissions,omitempty"`
}

func (s *Service) ValidateConfig(ctx context.Context, cfg AppConfig) (*AppMeta, error) {
	jwt, err := appJWT(cfg.AppID, cfg.PrivateKey, s.now())
	if err != nil {
		return nil, err
	}
	var meta AppMeta
	if err := s.doJSON(ctx, http.MethodGet, s.apiBase+"/app", "Bearer "+jwt, nil, http.StatusOK, &meta); err != nil { //i18n:ignore HTTP auth scheme
		return nil, err
	}
	if meta.ID != cfg.AppID {
		return nil, apperr.Invalid("githubapp.errors.key_wrong_app", "actual", meta.ID, "claimed", cfg.AppID)
	}
	return &meta, nil
}

type RemoteInstallation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	RepositorySelection string     `json:"repository_selection"`
	SuspendedAt         *time.Time `json:"suspended_at"`
}

// Redirect query params are attacker-controlled; never persist an installation not fetched with our
// own App JWT.
func (s *Service) FetchInstallation(ctx context.Context, installationID int64) (*RemoteInstallation, error) {
	cfg, _, err := s.config(ctx)
	if err != nil {
		return nil, err
	}
	jwt, err := appJWT(cfg.AppID, cfg.PrivateKey, s.now())
	if err != nil {
		return nil, err
	}
	var inst RemoteInstallation
	target := fmt.Sprintf("%s/app/installations/%d", s.apiBase, installationID)
	if err := s.doJSON(ctx, http.MethodGet, target, "Bearer "+jwt, nil, http.StatusOK, &inst); err != nil { //i18n:ignore HTTP auth scheme
		return nil, err
	}
	return &inst, nil
}

type Repo struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
}

func (s *Service) ListInstallationRepos(ctx context.Context, installationID int64) ([]Repo, error) {
	token, err := s.InstallationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	var out []Repo
	for page := 1; page <= 10; page++ {
		var resp struct {
			TotalCount   int    `json:"total_count"`
			Repositories []Repo `json:"repositories"`
		}
		target := fmt.Sprintf("%s/installation/repositories?per_page=100&page=%d", s.apiBase, page) //i18n:ignore API path and query
		if err := s.doJSON(ctx, http.MethodGet, target, "token "+token, nil, http.StatusOK, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Repositories...)
		if len(out) >= resp.TotalCount || len(resp.Repositories) == 0 {
			break
		}
	}
	return out, nil
}

// The user token must never be stored.
func (s *Service) exchangeUserCode(ctx context.Context, code string) (string, error) {
	cfg, _, err := s.config(ctx)
	if err != nil {
		return "", err
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return "", apperr.Precondition("githubapp.errors.oauth_not_configured")
	}
	form := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {code},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.oauthBase+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	resp, err := s.httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return "", err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", apperr.Wrap(err, "githubapp.errors.decode_oauth_response")
	}
	if tok.AccessToken == "" {
		if tok.Error != "" {
			return "", apperr.Unauthenticated("githubapp.errors.oauth_exchange", "message", tok.Error)
		}
		return "", apperr.Unauthenticated("githubapp.errors.oauth_no_token")
	}
	return tok.AccessToken, nil
}

func (s *Service) VerifyWebhookSignature(ctx context.Context, payload []byte, header string) bool {
	cfg, _, err := s.config(ctx)
	if err != nil || cfg.WebhookSecret == "" {
		return false
	}
	sigHex, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(cfg.WebhookSecret))
	mac.Write(payload)
	return hmac.Equal(sig, mac.Sum(nil))
}

func (s *Service) InstallURLWithState(ctx context.Context, state string) (string, error) {
	cfg, _, err := s.config(ctx)
	if err != nil {
		return "", err
	}
	if cfg.Slug == "" {
		return "", apperr.Precondition("githubapp.errors.slug_not_configured")
	}
	return githubcfg.AppInstallURL(cfg.Slug) + "?state=" + url.QueryEscape(state), nil
}

func (s *Service) doJSON(ctx context.Context, method, target, authorization string, body io.Reader, wantStatus int, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return apperr.Wrap(err, "githubapp.errors.read_response")
	}
	if resp.StatusCode != wantStatus {
		var errResp struct {
			Message string `json:"message"`
		}
		logx.Warn(json.Unmarshal(raw, &errResp), "githubapp.log.parse_error_body_failed")
		msg := errResp.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		e := apperr.New("githubapp.errors.api", "message", msg)
		// A 422 on a mint is GitHub refusing a permission the installation lacks; the resolver's
		// checks fallback keys on KindUnprocessable.
		if resp.StatusCode == http.StatusUnprocessableEntity {
			return e.WithKind(apperr.KindUnprocessable)
		}
		return e
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
