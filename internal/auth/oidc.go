package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	oidclib "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const (
	ssoKindOIDC   = "oidc"
	ssoKindGitHub = "github"
)

type SSOProviderInfo struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type ssoProvider struct {
	info          SSOProviderInfo
	oauth2Config  oauth2.Config
	oidcProvider  *oidclib.Provider
	endSessionURL string
}

func LoadSSOProviders(ctx context.Context) (map[string]*ssoProvider, error) { //nolint:revive,lll // ssoProvider is internal to this package; callers use the map for dispatch only
	providers := make(map[string]*ssoProvider)

	if issuer := strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_ISSUER")); issuer != "" {
		clientID := strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_CLIENT_ID"))
		clientSecret := strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_CLIENT_SECRET"))
		name := strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_NAME"))
		if name == "" {
			name = "SSO"
		}
		if clientID == "" || clientSecret == "" {
			return nil, apperr.Precondition("auth.errors.oidc_client_required")
		}
		p, err := oidclib.NewProvider(ctx, issuer)
		if err != nil {
			return nil, apperr.Wrap(err, "auth.errors.oidc_discovery_failed", "issuer", issuer)
		}
		providers[ssoKindOIDC] = &ssoProvider{
			info:          SSOProviderInfo{Key: ssoKindOIDC, Name: name, Kind: ssoKindOIDC},
			oidcProvider:  p,
			endSessionURL: discoverEndSession(p),
			oauth2Config: oauth2.Config{
				ClientID:     clientID,
				ClientSecret: clientSecret,
				Endpoint:     p.Endpoint(),
				Scopes:       []string{oidclib.ScopeOpenID, "email", "profile"},
			},
		}
	}

	if ghID := strings.TrimSpace(os.Getenv("OCTOPUS_GITHUB_CLIENT_ID")); ghID != "" {
		ghSecret := strings.TrimSpace(os.Getenv("OCTOPUS_GITHUB_CLIENT_SECRET"))
		if ghSecret == "" {
			return nil, apperr.Precondition("auth.errors.github_secret_required")
		}
		providers[ssoKindGitHub] = &ssoProvider{
			info: SSOProviderInfo{Key: ssoKindGitHub, Name: "GitHub", Kind: ssoKindGitHub},
			oauth2Config: oauth2.Config{
				ClientID:     ghID,
				ClientSecret: ghSecret,
				Endpoint: oauth2.Endpoint{
					AuthURL:  githubcfg.OAuthAuthorizeURL(),
					TokenURL: githubcfg.OAuthTokenURL(),
				},
				Scopes: []string{"read:user", "user:email"}, //i18n:ignore OAuth scope names
			},
		}
	}

	return providers, nil
}

type oidcStatePayload struct {
	Nonce    string `json:"n"`
	Provider string `json:"p"`
	Origin   string `json:"o"`
	Next     string `json:"nx,omitempty"`
	BindHash string `json:"b"`
	Exp      int64  `json:"exp"`
}

func issueOIDCState(provider, origin, next, bindToken string) (state, nonce string, err error) {
	nonceBytes := make([]byte, 16)
	if _, err = rand.Read(nonceBytes); err != nil {
		return "", "", err
	}
	nonce = hex.EncodeToString(nonceBytes)
	state, err = signEnvelope(oidcStatePayload{
		Nonce:    nonce,
		Provider: provider,
		Origin:   origin,
		Next:     next,
		BindHash: sha256Sum(bindToken),
		Exp:      time.Now().Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		return "", "", err
	}
	return state, nonce, nil
}

// Binds the SSO callback to the browser that started it (prevents OAuth login-CSRF) and carries the
// PKCE verifier, which must never appear in the authorization URL.
const ssoBindCookie = "octopus_sso_bind"

func setSSOBindCookie(w http.ResponseWriter, r *http.Request, bindToken, pkceVerifier string) {
	//nolint:gosec // G124: Secure is derived from the request scheme (see below), which the rule cannot model; same posture as SetAuthCookies
	http.SetCookie(w, &http.Cookie{
		Name:  ssoBindCookie,
		Value: bindToken + "." + pkceVerifier,
		Path:  "/auth/oidc",
		// Lax, not Strict: the IdP redirect back to /auth/oidc/callback is a cross-site navigation
		// that must carry the cookie.
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		// Secure follows the request scheme: forcing it on plain-http deployments makes the browser
		// drop the cookie and every callback fails.
		Secure: requestIsSecure(r),
		MaxAge: 600,
	})
}

func clearSSOBindCookie(w http.ResponseWriter, r *http.Request) {
	//nolint:gosec // G124: mirrors setSSOBindCookie — a deletion must match the original cookie's attributes to take effect
	http.SetCookie(w, &http.Cookie{
		Name:     ssoBindCookie,
		Path:     "/auth/oidc",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func validateSSOBind(r *http.Request, statePayload *oidcStatePayload) (pkceVerifier string, err error) {
	bindCk, err := r.Cookie(ssoBindCookie)
	if err != nil {
		return "", errSSOSessionMissing
	}
	bindToken, pkceVerifier, found := strings.Cut(bindCk.Value, ".")
	if !found || sha256Sum(bindToken) != statePayload.BindHash {
		return "", apperr.Unauthenticated("auth.errors.sso_session_mismatch")
	}
	return pkceVerifier, nil
}

func validateOIDCState(state string) (*oidcStatePayload, error) {
	var p oidcStatePayload
	if err := openEnvelope(state, &p); err != nil {
		return nil, err
	}
	if time.Now().Unix() > p.Exp {
		return nil, errSSOStateExpired
	}
	return &p, nil
}

func (h *Handler) handleOIDCAuthorize(w http.ResponseWriter, r *http.Request) {
	if len(h.ssoProviders) == 0 {
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.sso_not_configured"))
		return
	}
	providerKey := r.URL.Query().Get("provider")
	if providerKey == "" {
		for k := range h.ssoProviders {
			providerKey = k
			break
		}
	}
	p, ok := h.ssoProviders[providerKey]
	if !ok {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.sso_unknown_provider"))
		return
	}

	next := r.URL.Query().Get("next")
	if next == "" {
		next = "/"
	}

	origin := frontendOrigin(r)

	bindBytes := make([]byte, 24)
	if _, err := rand.Read(bindBytes); err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Wrap(err, "auth.errors.sso_state_generate_failed"))
		return
	}
	bindToken := hex.EncodeToString(bindBytes)
	pkceVerifier := oauth2.GenerateVerifier()

	state, nonce, err := issueOIDCState(providerKey, origin, next, bindToken)
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Wrap(err, "auth.errors.sso_state_generate_failed"))
		return
	}
	setSSOBindCookie(w, r, bindToken, pkceVerifier)

	cfg := p.oauth2Config
	cfg.RedirectURL = ssoCallbackURL(r)

	opts := []oauth2.AuthCodeOption{oauth2.AccessTypeOnline, oauth2.S256ChallengeOption(pkceVerifier)}
	if p.info.Kind == ssoKindOIDC {
		opts = append(opts, oidclib.Nonce(nonce))
	}

	authURL := cfg.AuthCodeURL(state, opts...)
	http.Redirect(w, r, authURL, http.StatusFound) //nolint:gosec // G710: authURL from OIDC config (OCTOPUS_OIDC_ISSUER), not user input
}

func (h *Handler) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		h.redirectToLogin(w, r, apperr.Unauthenticated("auth.errors.sso_provider_error", "error", errParam, "description", desc))
		return
	}

	stateStr := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if stateStr == "" || code == "" {
		h.redirectToLogin(w, r, apperr.Invalid("auth.errors.sso_missing_state_or_code"))
		return
	}

	statePayload, err := validateOIDCState(stateStr)
	if err != nil {
		reason := roundTripReason(err)
		if reason == "" {
			err = apperr.Wrap(err, "auth.errors.sso_state_invalid")
		}
		h.redirectToLoginReason(w, r, err, reason)
		return
	}

	pkceVerifier, bindErr := validateSSOBind(r, statePayload)
	if bindErr != nil {
		h.redirectToLoginReason(w, r, bindErr, roundTripReason(bindErr))
		return
	}
	clearSSOBindCookie(w, r)

	p, ok := h.ssoProviders[statePayload.Provider]
	if !ok {
		h.redirectToLogin(w, r, apperr.Invalid("auth.errors.sso_unknown_provider_in_state"))
		return
	}

	cfg := p.oauth2Config
	cfg.RedirectURL = ssoCallbackURL(r)

	ctx := r.Context()
	oauthToken, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		h.redirectToLogin(w, r, apperr.Unauthenticated("auth.errors.sso_token_exchange_failed"))
		return
	}

	id, err := extractSSOIdentity(ctx, p, oauthToken, statePayload.Nonce)
	if err != nil {
		h.redirectToLogin(w, r, apperr.Wrap(err, "auth.errors.sso_user_info_failed"))
		return
	}

	user, wsID, role, err := h.provisionSSOUser(r, p.info.Key, id)
	if errors.Is(err, ErrPendingAccess) {
		h.redirectPendingAccess(w, r, err, id)
		return
	}
	if errors.Is(err, ErrSSONotAllowed) || errors.Is(err, ErrSSOEmailUnverified) ||
		errors.Is(err, errSSOEmailUnverifiedLink) {
		h.redirectRefused(w, r, err, p.info.Name, id)
		return
	}
	if err != nil {
		h.redirectToLogin(w, r, apperr.Wrap(err, "auth.errors.sso_provisioning_failed"))
		return
	}

	h.completeSSOLogin(w, r, user, wsID, role, p.info.Name, statePayload.Origin, statePayload.Next)
}

// The origin in the signed state came from the request Host at flow start, so Host-header injection
// can poison it.
// Check it against the allowlist before redirecting.
func (h *Handler) completeSSOLogin(w http.ResponseWriter, r *http.Request, user *User, wsID string, role Role,
	providerName, origin, next string) {
	tokens, err := h.issueTokenPair(r.Context(), user, wsID, role, clientDevice(r))
	if err != nil {
		h.redirectToLogin(w, r, apperr.Internal("auth.errors.sso_token_issuance_failed"))
		return
	}

	h.auditUser(r, "user.logged_in", user.ID, user.Email, wsID, i18n.Text("auth.audit.sso_login", "provider", providerName))
	h.countEvent("sso", "ok")

	if !isAllowedFrontendOrigin(r, origin) {
		h.redirectToLogin(w, r, apperr.Forbidden("auth.errors.sso_untrusted_origin"))
		return
	}

	if cookieErr := setSessionCookies(w, r, tokens); cookieErr != nil {
		h.redirectToLogin(w, r, apperr.Internal("auth.errors.sso_cookie_failed"))
		return
	}

	http.Redirect(w, r, origin+sanitizeNextPath(next), http.StatusFound)
}

// A leading "//" or a scheme would turn the post-login redirect into an open redirect.
func sanitizeNextPath(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

type ssoIdentity struct {
	Email         string
	Name          string
	Sub           string
	EmailVerified bool
	Groups        []string
}

func extractSSOIdentity(ctx context.Context, p *ssoProvider, oauthToken *oauth2.Token, wantNonce string) (ssoIdentity, error) {
	var (
		id  ssoIdentity
		err error
	)
	switch p.info.Kind {
	case ssoKindOIDC:
		id, err = extractOIDCClaims(ctx, p, oauthToken, wantNonce)
	case ssoKindGitHub:
		id, err = extractGitHubUser(ctx, oauthToken.AccessToken)
	default:
		err = apperr.Internal("auth.errors.sso_unknown_provider_kind")
	}
	if err != nil {
		return ssoIdentity{}, err
	}
	id.Email = strings.ToLower(strings.TrimSpace(id.Email))
	if id.Email == "" {
		return ssoIdentity{}, apperr.Unprocessable("auth.errors.sso_no_email")
	}
	return id, nil
}

func extractOIDCClaims(ctx context.Context, p *ssoProvider, oauthToken *oauth2.Token, wantNonce string) (ssoIdentity, error) {
	rawIDToken, ok := oauthToken.Extra("id_token").(string)
	if !ok {
		return ssoIdentity{}, apperr.Unauthenticated("auth.errors.oidc_no_id_token")
	}
	verifier := p.oidcProvider.Verifier(&oidclib.Config{ClientID: p.oauth2Config.ClientID})
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return ssoIdentity{}, apperr.WrapKind(apperr.KindUnauthenticated, err, "auth.errors.oidc_id_token_invalid")
	}
	if idToken.Nonce != wantNonce {
		return ssoIdentity{}, apperr.Unauthenticated("auth.errors.oidc_nonce_mismatch")
	}
	var raw map[string]json.RawMessage
	if err := idToken.Claims(&raw); err != nil {
		return ssoIdentity{}, apperr.Wrap(err, "auth.errors.oidc_claims_failed")
	}
	var id ssoIdentity
	id.Sub = idToken.Subject
	jsonStr(raw["email"], &id.Email)
	jsonStr(raw["name"], &id.Name)
	// A missing email_verified means unverified: multi-tenant Entra and Keycloak defaults issue
	// attacker-assertable emails, and account linking depends on verification.
	var verified *bool
	if v, ok := raw["email_verified"]; ok {
		_ = json.Unmarshal(v, &verified) //nolint:errcheck // absent/!bool → nil → treated as unverified
	}
	id.EmailVerified = verified != nil && *verified
	id.Groups = parseGroupsClaim(raw[oidcGroupsClaim()])
	return id, nil
}

func jsonStr(v json.RawMessage, dst *string) {
	if len(v) > 0 {
		_ = json.Unmarshal(v, dst) //nolint:errcheck // best-effort: leave dst empty on type mismatch
	}
}

func parseGroupsClaim(v json.RawMessage) []string {
	if len(v) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(v, &arr); err == nil {
		return arr
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
	}
	return nil
}

func extractGitHubUser(ctx context.Context, accessToken string) (ssoIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", githubcfg.APIURL()+"/user", http.NoBody)
	if err != nil {
		return ssoIdentity{}, apperr.Wrap(err, "auth.errors.github_user_request")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken) //i18n:ignore HTTP auth scheme
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ssoIdentity{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ssoIdentity{}, apperr.Wrap(err, "auth.errors.github_user_read")
	}
	var u struct {
		ID    int    `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if unmarshalErr := json.Unmarshal(body, &u); unmarshalErr != nil {
		return ssoIdentity{}, apperr.Wrap(unmarshalErr, "auth.errors.github_user_parse")
	}
	// The /user email has no verified flag and GitHub allows unverified addresses, so resolve it
	// through /user/emails.
	verifiedEmail, err := fetchGitHubVerifiedEmail(ctx, accessToken)
	if err != nil {
		return ssoIdentity{}, err
	}
	displayName := u.Name
	if displayName == "" {
		displayName = u.Login
	}
	return ssoIdentity{
		Email:         verifiedEmail,
		Name:          displayName,
		Sub:           fmt.Sprintf("github:%d", u.ID),
		EmailVerified: true,
	}, nil
}

// Never return an unverified address: extractGitHubUser marks the result EmailVerified.
func fetchGitHubVerifiedEmail(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", githubcfg.APIURL()+"/user/emails", http.NoBody)
	if err != nil {
		return "", apperr.Wrap(err, "auth.errors.github_emails_request")
	}
	req.Header.Set("Authorization", "Bearer "+token) //i18n:ignore HTTP auth scheme
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return "", err
	}
	if domains := csvEnv("OCTOPUS_SSO_ALLOWED_DOMAINS"); len(domains) > 0 {
		for _, e := range emails {
			if e.Verified && emailInDomains(e.Email, domains) {
				return e.Email, nil
			}
		}
	}
	firstVerified := ""
	for _, e := range emails {
		if !e.Verified {
			continue
		}
		if e.Primary {
			return e.Email, nil
		}
		if firstVerified == "" {
			firstVerified = e.Email
		}
	}
	if firstVerified != "" {
		return firstVerified, nil
	}
	return "", apperr.Unprocessable("auth.errors.github_no_verified_email")
}

func oidcGroupsClaim() string {
	if c := strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_GROUPS_CLAIM")); c != "" {
		return c
	}
	return "groups"
}

func mapGroupsToRole(groups []string) Role {
	raw := strings.TrimSpace(os.Getenv("OCTOPUS_OIDC_GROUP_ROLE_MAP"))
	if raw == "" || len(groups) == 0 {
		return ""
	}
	have := make(map[string]bool, len(groups))
	for _, g := range groups {
		have[strings.TrimSpace(g)] = true
	}
	var best Role
	for _, pair := range strings.Split(raw, ",") {
		grpRaw, roleRaw, ok := strings.Cut(strings.TrimSpace(pair), ":")
		if !ok {
			continue
		}
		grp := strings.TrimSpace(grpRaw)
		role := Role(strings.TrimSpace(roleRaw))
		if !have[grp] || !isKnownRole(role) {
			continue
		}
		if best == "" || roleAtLeast(role, best) {
			best = role
		}
	}
	return best
}

func isKnownRole(r Role) bool {
	switch r {
	case RoleViewer, RoleDeveloper, RoleAdmin, RoleOwner:
		return true
	}
	return false
}

func (h *Handler) provisionSSOUser(r *http.Request, provider string, id ssoIdentity) (*User, string, Role, error) {
	ctx := r.Context()
	linked, linkErr := h.store.GetUserByOIDCSub(ctx, provider, id.Sub)
	if linkErr != nil {
		linked = nil
	}
	err := ssoAdmit(id)
	if err == nil {
		err = h.signupAdmit(ctx, id, linked)
	}
	if err != nil {
		h.emitAudit(r, AuditEvent{
			Kind:         "auth.sso_denied",
			Actor:        Actor{Type: ActorUser, ID: "sso:" + provider + ":" + id.Sub, Label: id.Email, Email: id.Email},
			ResourceType: "user",
			Summary:      i18n.Text("auth.audit.sso_denied", "provider", provider, "reason", apperr.Text(err)),
		})
		return nil, "", "", err
	}

	// With OCTOPUS_OIDC_GROUP_ROLE_MAP set, the mapped role is applied on every login, so removing
	// someone from an IdP group downgrades them.
	groupRole := mapGroupsToRole(id.Groups)

	if linked != nil {
		return h.landExistingUser(r, linked, groupRole)
	}

	// Linking by email lets any trusted IdP take over an account: require
	// OCTOPUS_ALLOW_SSO_AUTOLINK=1 and a provider-verified email.
	// GetUserByEmail returns (nil, nil) for not found.
	user, err := h.store.GetUserByEmail(ctx, id.Email)
	if err == nil && user != nil {
		if os.Getenv("OCTOPUS_ALLOW_SSO_AUTOLINK") != "1" {
			return nil, "", "", apperr.Conflict("auth.errors.sso_email_exists")
		}
		if !id.EmailVerified {
			return nil, "", "", errSSOEmailUnverifiedLink
		}
		if linkErr := h.store.LinkOIDCIdentity(ctx, user.ID, provider, id.Sub, id.Email); linkErr != nil {
			return nil, "", "", apperr.Wrap(linkErr, "auth.errors.sso_link_identity")
		}
		h.auditUser(r, "user.sso_identity_linked", user.ID, user.Email, "",
			i18n.Text("auth.audit.sso_identity_linked", "provider", provider))
		return h.landExistingUser(r, user, groupRole)
	}

	// Only a provider-verified email may create an account: invitations and the signup allowlist
	// match on it, so an unproven address could squat on someone else's.
	if !id.EmailVerified {
		return nil, "", "", ErrSSOEmailUnverified
	}
	name := id.Name
	if name == "" {
		name = strings.Split(id.Email, "@")[0]
	}
	newUser := &User{
		ID:        genID(),
		Email:     id.Email,
		Name:      name,
		CreatedAt: time.Now(),
	}
	if createErr := h.store.CreateUserSSO(ctx, newUser); createErr != nil {
		return nil, "", "", apperr.Wrap(createErr, "auth.errors.sso_create_user")
	}
	if linkErr := h.store.LinkOIDCIdentity(ctx, newUser.ID, provider, id.Sub, id.Email); linkErr != nil {
		return nil, "", "", apperr.Wrap(linkErr, "auth.errors.sso_link_identity")
	}

	return h.assignWorkspace(r, newUser, groupRole)
}

// The IdP group role applies to the first membership, not the landing one. Otherwise switching
// workspaces before sign-out picks where the group promotes you, other tenants included.
func (h *Handler) landExistingUser(r *http.Request, user *User, groupRole Role) (*User, string, Role, error) {
	h.acceptInvitations(r, user)
	ws, role, wsErr := h.store.GetDefaultWorkspace(r.Context(), user.ID)
	if errors.Is(wsErr, sql.ErrNoRows) {
		return h.assignWorkspace(r, user, groupRole)
	}
	if wsErr != nil {
		return nil, "", "", apperr.Wrap(wsErr, "auth.errors.sso_no_workspace")
	}
	role = h.syncGroupRole(r, user.ID, ws, role, groupRole)
	last, lastRole, lastErr := h.store.LastWorkspace(r.Context(), user.ID)
	logx.Warn(lastErr, "auth.account.log.last_workspace", "user", user.ID)
	if lastErr == nil && last != "" && last != ws {
		return user, last, lastRole, nil
	}
	return user, ws, role, nil
}

// Never fall back to the oldest workspace: in a multi-tenant deployment that is the first
// tenant's.
func (h *Handler) assignWorkspace(r *http.Request, user *User, groupRole Role) (*User, string, Role, error) {
	if first, ok := h.acceptInvitations(r, user); ok {
		return user, first.WorkspaceID, first.Role, nil
	}
	ctx := i18n.WithLocale(r.Context(), i18n.RequestLocale(r))
	ws, role, err := h.store.ProvisionSSOWorkspace(ctx, user)
	if errors.Is(err, ErrPendingAccess) {
		h.auditUser(r, "user.pending_access", user.ID, user.Email, "",
			i18n.Text("auth.audit.pending_access", "email", user.Email))
		return nil, "", "", err
	}
	if err != nil {
		return nil, "", "", apperr.Wrap(err, "auth.errors.sso_assign_workspace")
	}
	role = h.syncGroupRole(r, user.ID, ws, role, groupRole)
	return user, ws, role, nil
}

func (h *Handler) syncGroupRole(r *http.Request, userID, ws string, current, groupRole Role) Role {
	if groupRole == "" || groupRole == current || ws == "" {
		return current
	}
	if err := h.store.UpdateMemberRole(r.Context(), ws, userID, groupRole); err != nil {
		logx.Warn(err, "auth.log.sso_sync_group_role", "user", userID)
		return current
	}
	h.auditUser(r, "user.role_synced", userID, "", ws,
		i18n.Text("auth.audit.role_synced", "role", groupRole))
	return groupRole
}

func (h *Handler) redirectToLogin(w http.ResponseWriter, r *http.Request, cause error) {
	h.redirectToLoginReason(w, r, cause, "")
}

func (h *Handler) redirectToLoginReason(w http.ResponseWriter, r *http.Request, cause error, reason string) {
	q := url.Values{}
	if reason != "" {
		q.Set("reason", reason)
	}
	h.redirectToLoginWith(w, r, cause, q)
}

func (h *Handler) redirectToLoginWith(w http.ResponseWriter, r *http.Request, cause error, q url.Values) {
	h.countEvent("sso", "fail")
	q.Set("error", apperr.Localize(cause, i18n.RequestLocale(r)))
	//i18n:ignore URL path
	http.Redirect(w, r, frontendOrigin(r)+"/login?"+q.Encode(), http.StatusFound)
}

func frontendOrigin(r *http.Request) string {
	if override := os.Getenv("OCTOPUS_FRONTEND_ORIGIN"); override != "" {
		return strings.TrimRight(override, "/")
	}
	return requestScheme(r) + "://" + r.Host
}

func RequestOrigin(r *http.Request) string { return requestScheme(r) + "://" + r.Host }

func requestScheme(r *http.Request) string {
	if requestIsSecure(r) {
		return "https"
	}
	return "http"
}

// When origins are configured, the request Host is ignored so a spoofed Host cannot redirect fresh
// sessions. Same-origin is only the no-config fallback.
func allowedFrontendOrigins(r *http.Request) map[string]bool {
	set := map[string]bool{}
	add := func(o string) {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o != "" {
			set[o] = true
		}
	}
	add(os.Getenv("OCTOPUS_FRONTEND_ORIGIN"))
	for _, o := range strings.Split(os.Getenv("OCTOPUS_ALLOWED_ORIGINS"), ",") {
		add(o)
	}
	if len(set) == 0 {
		add(requestScheme(r) + "://" + r.Host)
	}
	return set
}

func isAllowedFrontendOrigin(r *http.Request, origin string) bool {
	return allowedFrontendOrigins(r)[strings.TrimRight(origin, "/")]
}

// The scheme comes from requestIsSecure, not r.TLS: behind a TLS-terminating proxy, "http" would
// not match the registered redirect_uri.
// The proxy must be listed in OCTOPUS_TRUSTED_PROXIES.
func ssoCallbackURL(r *http.Request) string {
	if override := os.Getenv("OCTOPUS_OIDC_REDIRECT_URI"); override != "" {
		return override
	}
	return requestScheme(r) + "://" + r.Host + "/auth/oidc/callback"
}
