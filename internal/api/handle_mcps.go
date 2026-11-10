package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/mcp"
)

const (
	mcpSourceEnv        = "env"
	mcpSourceRuntime    = "runtime"
	mcpSourceRegistered = "registered"
)

const (
	mcpStatusConfigured = "configured"
	mcpStatusExternal   = "external"
)

const mcpPingBudget = 4 * time.Second

func (s *Server) handleListMCPs(w http.ResponseWriter, r *http.Request) {
	runtime := []mcp.Service{}
	if s.mcp.mgr != nil {
		out, err := s.mcp.mgr.List(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		for i := range out {
			out[i].Source = mcpSourceRuntime
		}
		runtime = out
	}

	regs := []mcp.Registration{}
	if s.mcp.registry != nil {
		got, err := s.mcp.registry.List(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		regs = got
	}

	merged := mergeMCPList(envMCPServices(s.mcp.env, s.mcp.envForward), runtime, regs)
	if !s.callerIsOperator(r) {
		// Tenant admins must not see URLs, ports or images (the install's private network), nor
		// make the daemon dial every address per page load.
		for i := range merged {
			merged[i] = tenantMCPView(merged[i])
		}
		writeJSON(w, http.StatusOK, merged)
		return
	}
	s.pingMCPs(r.Context(), merged)
	writeJSON(w, http.StatusOK, merged)
}

func tenantMCPView(sv mcp.Service) mcp.Service {
	return mcp.Service{
		Name:               sv.Name,
		Source:             sv.Source,
		Status:             sv.Status,
		State:              sv.State,
		Health:             sv.Health,
		Description:        sv.Description,
		ForwardsCredential: sv.ForwardsCredential,
		ImageAvailable:     sv.ImageAvailable,
	}
}

func envMCPServices(env map[string]string, forward map[string]bool) []mcp.Service {
	out := make([]mcp.Service, 0, len(env))
	for key, u := range env {
		name := mcp.EnvKeyToName(key)
		if name == "" {
			continue
		}
		out = append(out, mcp.Service{
			Name:               name,
			Source:             mcpSourceEnv,
			URL:                u,
			ForwardsCredential: forward[name],
			Status:             mcpStatusConfigured,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Precedence env < runtime < registered mirrors the forwarding table (SetMCPRegistry seeds after
// env; writes go through SetMCPURL), so each row shows the URL agents get.
func mergeMCPList(env, runtime []mcp.Service, regs []mcp.Registration) []mcp.Service {
	byName := make(map[string]int, len(runtime)+len(env))
	out := make([]mcp.Service, 0, len(runtime)+len(env)+len(regs))
	for i := range runtime {
		byName[runtime[i].Name] = len(out)
		out = append(out, runtime[i])
	}
	for i := range env {
		e := env[i]
		if idx, ok := byName[e.Name]; ok {
			if out[idx].URL == "" {
				out[idx].URL = e.URL
			}
			out[idx].ForwardsCredential = e.ForwardsCredential
			continue
		}
		byName[e.Name] = len(out)
		out = append(out, e)
	}
	for i := range regs {
		reg := &regs[i]
		if idx, ok := byName[reg.Name]; ok && out[idx].Source == mcpSourceRuntime {
			out[idx].Source = mcpSourceRegistered
			out[idx].URL = reg.URL
			out[idx].Description = reg.Description
			out[idx].ForwardsCredential = reg.ForwardsCredential
			setRegistrationAuth(&out[idx], reg)
			continue
		}
		row := mcp.Service{
			Name:               reg.Name,
			Source:             mcpSourceRegistered,
			URL:                reg.URL,
			Description:        reg.Description,
			ForwardsCredential: reg.ForwardsCredential,
			Status:             mcpStatusExternal,
		}
		setRegistrationAuth(&row, reg)
		if idx, ok := byName[reg.Name]; ok {
			out[idx] = row
			continue
		}
		byName[reg.Name] = len(out)
		out = append(out, row)
	}
	return out
}

func setRegistrationAuth(sv *mcp.Service, reg *mcp.Registration) {
	sv.Transport, sv.Auth = reg.Transport, reg.Auth
	sv.CredentialRef, sv.HeaderTemplate = reg.CredentialRef, reg.HeaderTemplate
}

func (s *Server) pingMCPs(ctx context.Context, list []mcp.Service) {
	if s.mcp.prober == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, mcpPingBudget)
	defer cancel()
	var wg sync.WaitGroup
	for i := range list {
		sv := &list[i]
		if sv.URL == "" || (sv.Status != mcpStatusConfigured && sv.Status != mcpStatusExternal) {
			continue
		}
		// The prober opens an unauthenticated SSE session; Streamable HTTP or credentialed servers
		// would falsely read as down, so they stay unchecked.
		if sv.Transport == mcp.TransportHTTP || sv.Auth == mcp.AuthCredential {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.mcp.prober.Ping(ctx, sv.URL); err != nil {
				sv.Health = "unhealthy"
				sv.HealthDetail = err.Error()
				return
			}
			sv.Health = "healthy"
		}()
	}
	wg.Wait()
}

type registerMCPBody struct {
	Name               string `json:"name"`
	URL                string `json:"url"`
	Description        string `json:"description,omitempty"`
	ForwardsCredential bool   `json:"forwards_credential,omitempty"`
	Transport          string `json:"transport,omitempty"`
	Auth               string `json:"auth,omitempty"`
	CredentialRef      string `json:"credential_ref,omitempty"`
	HeaderTemplate     string `json:"header_template,omitempty"`
}

func sendsSecret(reg mcp.Registration) bool {
	return reg.ForwardsCredential || reg.Auth == mcp.AuthCredential
}

func (s *Server) forwardRegistration(reg *mcp.Registration) {
	if s.taskDispatcher == nil {
		return
	}
	s.taskDispatcher.SetMCPURL(reg.EnvKey(), reg.URL)
	s.taskDispatcher.SetMCPForwardsCredential(reg.Name, reg.ForwardsCredential)
	s.taskDispatcher.SetMCPTransport(reg.Name, reg.Transport)
}

// The registry is install-wide and its URLs (plus run keys with forwards_credential) go to every
// tenant's agents; only the operator may change it.
func (s *Server) requireMCPOperator(w http.ResponseWriter, r *http.Request) bool {
	return s.requireOperator(w, r, "api.scope.operator_surfaces.mcp_registry", "api.scope.operator_verbs.change")
}

// Forwarded keys travel in handshake headers: require https, or plain http only to
// loopback/private/link-local IPs, single-label names or cluster suffixes.
func credentialForwardingURLError(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return "api.mcps.errors.invalid_url"
	}
	if u.Scheme == "https" {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip, perr := netip.ParseAddr(host); perr == nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return ""
		}
		return "api.mcps.errors.forwarding_needs_private_url"
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return ""
	}
	for _, suffix := range []string{".svc", ".cluster.local", ".internal", ".local"} {
		if strings.HasSuffix(host, suffix) {
			return ""
		}
	}
	return "api.mcps.errors.forwarding_needs_private_url"
}

func (s *Server) handleRegisterMCP(w http.ResponseWriter, r *http.Request) {
	if s.mcp.registry == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.mcps.errors.registry_unavailable")
		return
	}
	if !s.requireMCPOperator(w, r) {
		return
	}
	var body registerMCPBody
	if !decodeBody(w, r, &body) {
		return
	}
	in := mcp.Registration{
		Name: body.Name, URL: body.URL, Description: body.Description,
		ForwardsCredential: body.ForwardsCredential, Transport: body.Transport,
		Auth: body.Auth, CredentialRef: body.CredentialRef, HeaderTemplate: body.HeaderTemplate,
	}
	if sendsSecret(in) {
		if msg := credentialForwardingURLError(body.URL); msg != "" {
			writeErrKey(w, r, http.StatusBadRequest, msg)
			return
		}
	}
	if c := auth.ClaimsFromContext(r.Context()); c != nil {
		in.CreatedBy = c.Email
	}
	reg, err := s.mcp.registry.Register(r.Context(), in)
	if err != nil {
		writeError(w, r, statusForMCPErr(err), err)
		return
	}
	s.forwardRegistration(&reg)
	writeJSON(w, http.StatusCreated, reg)
}

type updateMCPBody struct {
	URL                string  `json:"url,omitempty"`
	Description        string  `json:"description,omitempty"`
	ForwardsCredential *bool   `json:"forwards_credential,omitempty"`
	Transport          *string `json:"transport,omitempty"`
	Auth               *string `json:"auth,omitempty"`
	CredentialRef      *string `json:"credential_ref,omitempty"`
	HeaderTemplate     *string `json:"header_template,omitempty"`
}

func (s *Server) handleUpdateMCP(w http.ResponseWriter, r *http.Request) {
	if s.mcp.registry == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.mcps.errors.registry_unavailable")
		return
	}
	if !s.requireMCPOperator(w, r) {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.mcps.errors.missing_name")
		return
	}
	var body updateMCPBody
	if !decodeBody(w, r, &body) {
		return
	}
	patch := mcp.Patch{
		URL: body.URL, Description: body.Description, ForwardsCredential: body.ForwardsCredential,
		Transport: body.Transport, Auth: body.Auth, CredentialRef: body.CredentialRef, HeaderTemplate: body.HeaderTemplate,
	}
	// Validate the post-patch registration: a new URL on a secret-sending server, or a secret
	// enabled on the existing URL.
	if current, err := s.mcp.registry.Get(r.Context(), name); err == nil {
		if next, perr := patch.Apply(current); perr == nil && sendsSecret(next) {
			if msg := credentialForwardingURLError(next.URL); msg != "" {
				writeErrKey(w, r, http.StatusBadRequest, msg)
				return
			}
		}
	}
	reg, err := s.mcp.registry.Update(r.Context(), name, patch)
	if err != nil {
		writeError(w, r, statusForMCPErr(err), err)
		return
	}
	s.forwardRegistration(&reg)
	writeJSON(w, http.StatusOK, reg)
}

func (s *Server) handleDeleteMCP(w http.ResponseWriter, r *http.Request) {
	if s.mcp.registry == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.mcps.errors.registry_unavailable")
		return
	}
	if !s.requireMCPOperator(w, r) {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.mcps.errors.missing_name")
		return
	}
	if err := s.mcp.registry.Delete(r.Context(), name); err != nil {
		writeError(w, r, statusForMCPErr(err), err)
		return
	}
	s.dropRegistrationFromForwarding(name)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) dropRegistrationFromForwarding(name string) {
	if s.taskDispatcher == nil {
		return
	}
	key := mcp.NameToEnvKey(name)
	if u := s.mcp.env[key]; u != "" {
		s.taskDispatcher.SetMCPURL(key, u)
	} else {
		s.taskDispatcher.DeleteMCPURL(key)
	}
	s.taskDispatcher.SetMCPForwardsCredential(name, s.mcp.envForward[name])
	s.taskDispatcher.SetMCPTransport(name, mcp.TransportSSE)
}

func (s *Server) handleStartMCP(w http.ResponseWriter, r *http.Request) {
	s.runMCPAction(w, r, "start")
}

func (s *Server) handleStopMCP(w http.ResponseWriter, r *http.Request) {
	s.runMCPAction(w, r, "stop")
}

func (s *Server) handleRestartMCP(w http.ResponseWriter, r *http.Request) {
	s.runMCPAction(w, r, "restart")
}

func (s *Server) runMCPAction(w http.ResponseWriter, r *http.Request, action string) {
	if !s.requireMCPOperator(w, r) {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.mcps.errors.missing_name")
		return
	}
	var err error
	switch action {
	case "start":
		err = s.mcp.mgr.Start(r.Context(), name)
	case "stop":
		err = s.mcp.mgr.Stop(r.Context(), name)
	case "restart":
		err = s.mcp.mgr.Restart(r.Context(), name)
	default:
		writeErrKey(w, r, http.StatusBadRequest, "api.mcps.errors.unknown_action")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if refreshed := s.refreshMCPView(r.Context(), name); refreshed != nil {
		writeJSON(w, http.StatusOK, refreshed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) refreshMCPView(ctx context.Context, name string) *mcp.Service {
	if s.mcp.mgr == nil {
		return nil
	}
	out, err := s.mcp.mgr.List(ctx)
	if err != nil {
		return nil
	}
	for i := range out {
		if out[i].Name == name {
			out[i].Source = mcpSourceRuntime
			return &out[i]
		}
	}
	return nil
}

func statusForMCPErr(err error) int {
	switch {
	case errors.Is(err, mcp.ErrRegistrationNotFound):
		return http.StatusNotFound
	case errors.Is(err, mcp.ErrRegistrationExists):
		return http.StatusConflict
	case errors.Is(err, mcp.ErrInvalidName),
		errors.Is(err, mcp.ErrInvalidURL),
		errors.Is(err, mcp.ErrReservedName),
		errors.Is(err, mcp.ErrInvalidTransport),
		errors.Is(err, mcp.ErrInvalidAuth),
		errors.Is(err, mcp.ErrCredentialRefNeeded),
		apperr.KindOf(err) == apperr.KindInvalid:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

type probeMCPBody struct {
	URL string `json:"url"`
}

// Operator-only: it makes the daemon call a URL of the caller's choosing.
func (s *Server) handleProbeMCP(w http.ResponseWriter, r *http.Request) {
	if !s.requireMCPOperator(w, r) {
		return
	}
	var body probeMCPBody
	if !decodeBody(w, r, &body) {
		return
	}
	u, err := url.Parse(strings.TrimSpace(body.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.mcps.errors.invalid_url")
		return
	}
	d := s.mcp.detector
	if d == nil {
		d = mcp.NewDetector()
	}
	got, err := d.Detect(r.Context(), u.String())
	if err != nil {
		writeError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}
