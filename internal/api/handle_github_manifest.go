package api

import (
	"net/http"
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Distinct from githubInstallBinder: both flows can be open at once in one browser.
const githubManifestBinder = "octopus_gh_manifest"

type githubManifestResp struct {
	PostURL  string `json:"post_url"`
	Manifest string `json:"manifest"`
}

func (s *Server) handleGithubAppManifest(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppReady(w, r) || !s.requireOperator(w, r,
		i18n.TCtx(r.Context(), "api.github.labels.operator_subject"),
		i18n.TCtx(r.Context(), "api.github.labels.operator_verb_configure")) {
		return
	}
	if !s.requireDBConfigurableApp(w, r) {
		return
	}
	// A second App would replace the first and orphan its installations.
	if s.gh.app.Configured(r.Context()) {
		writeErrKey(w, r, http.StatusConflict, "api.github.errors.app_already_configured")
		return
	}
	q := r.URL.Query()
	manifest, target, err := githubapp.BuildManifest(githubapp.ManifestOptions{
		BaseURL: q.Get("base_url"),
		Org:     q.Get("org"),
		Public:  q.Get("public") == "1",
		Deploy:  q.Get("deploy") == "1",
		Name:    q.Get("name"),
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	body, err := githubapp.MarshalManifest(manifest)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	wid, userID := "", ""
	if c := auth.ClaimsFromContext(r.Context()); c != nil {
		wid, userID = c.WorkspaceID, c.UserID
	}
	state, binder, err := s.gh.app.IssueManifestState(r.Context(), wid, userID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	auth.SetFlowBindCookie(w, r, githubManifestBinder, githubInstallBinderPath, binder,
		int(githubapp.ManifestStateTTL.Seconds()))
	writeJSON(w, http.StatusOK, githubManifestResp{
		PostURL:  target + "?state=" + state,
		Manifest: body,
	})
}

func githubManifestRedirect(w http.ResponseWriter, r *http.Request, query string) {
	http.Redirect(w, r, "/credentials/?"+query, http.StatusFound)
}

func (s *Server) handleGithubManifestCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(reason string) { githubManifestRedirect(w, r, "ghapp=error&reason="+reason) }
	if s.gh.app == nil || s.gh.store == nil {
		fail("unavailable")
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	binder := ""
	if c, err := r.Cookie(githubManifestBinder); err == nil {
		binder = c.Value
	}
	auth.ClearFlowBindCookie(w, r, githubManifestBinder, githubInstallBinderPath)
	flow, err := s.gh.app.ConsumeManifestState(ctx, q.Get("state"), binder)
	if err != nil {
		logx.Warn(err, "api.github.log.manifest_state_failed")
		fail("state")
		return
	}
	if st := s.gh.app.Status(ctx); st.Configured {
		fail("configured")
		return
	}
	conv, err := s.gh.app.ConvertManifest(ctx, q.Get("code"))
	if err != nil {
		logx.Warn(err, "api.github.log.manifest_convert_failed")
		fail("convert")
		return
	}
	cfg := conv.Config()
	meta, err := s.gh.app.ValidateConfig(ctx, cfg)
	if err != nil {
		logx.Warn(err, "api.github.log.manifest_validate_failed", "app_id", cfg.AppID)
		fail("validate")
		return
	}
	if meta.Slug != "" {
		cfg.Slug = meta.Slug
	}
	if err := s.gh.store.SetConfig(ctx, cfg); err != nil {
		logx.Warn(err, "api.github.log.manifest_store_failed", "app_id", cfg.AppID)
		fail("store")
		return
	}
	s.gh.app.InvalidateConfig()
	s.auditGithubClaim(flow, "github.app.configured", strconv.FormatInt(cfg.AppID, 10),
		i18n.Text("api.github.audit.app_registered", "slug", cfg.Slug, "app_id", cfg.AppID))
	githubManifestRedirect(w, r, "ghapp=created")
}

func appPermissionGaps(meta *githubapp.AppMeta) []githubapp.PermissionGap {
	if meta == nil {
		return nil
	}
	return githubapp.MissingPermissions(meta.Permissions)
}

type githubAppConnectResp struct {
	githubapp.AppStatus
	MissingPermissions []githubapp.PermissionGap `json:"missing_permissions,omitempty"`
}
