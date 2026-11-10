package api

// Only workspace admins install: every run of an agent using a skill is told its text.
// Apply recomputes the plan against the workspace as it is then, so a stale screen cannot overwrite
// anything not picked.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/builtin"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/workshop"
)

var workshopCatalog = sync.OnceValues(func() (*workshop.Catalog, error) {
	return workshop.LoadCatalog(builtin.Workshop())
})

type workshopRequest struct {
	Items       []workshop.Ref                 `json:"items"`
	Bundle      json.RawMessage                `json:"bundle"`
	Resolutions map[string]workshop.Resolution `json:"resolutions"`
	Names       map[string]string              `json:"names"`
}

type workshopExportRequest struct {
	Items []workshop.Ref `json:"items"`
}

type workshopApplyResponse struct {
	Plan   *workshop.Plan  `json:"plan"`
	Result workshop.Result `json:"result"`
}

func (s *Server) registerWorkshopRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /workshop", rr(auth.RoleViewer, s.handleWorkshopList))
	mux.HandleFunc("GET /workshop/{kind}/{name}", rr(auth.RoleViewer, s.handleWorkshopItem))
	mux.HandleFunc("POST /workshop/plan", rr(auth.RoleAdmin, s.handleWorkshopPlan))
	mux.HandleFunc("POST /workshop/apply", rr(auth.RoleAdmin, s.handleWorkshopApply))
	mux.HandleFunc("POST /workshop/export", rr(auth.RoleViewer, s.handleWorkshopExport))
	mux.HandleFunc("GET /types/{name}/export", rr(auth.RoleViewer, s.handleExportAgent))
	mux.HandleFunc("GET /skills/{name}/export", rr(auth.RoleViewer, s.handleExportSkill))
}

func (s *Server) workshopState(w http.ResponseWriter, r *http.Request) (workshop.StoreState, bool) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return workshop.StoreState{}, false
	}
	if store == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.workshop.errors.no_store")
		return workshop.StoreState{}, false
	}
	return workshop.StoreState{Store: store, Tenant: tenant}, true
}

func (s *Server) handleWorkshopList(w http.ResponseWriter, r *http.Request) {
	st, ok := s.workshopState(w, r)
	if !ok {
		return
	}
	cat, err := workshopCatalog()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	entries, err := workshop.List(r.Context(), st, cat, i18n.RequestLocale(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleWorkshopItem(w http.ResponseWriter, r *http.Request) {
	st, ok := s.workshopState(w, r)
	if !ok {
		return
	}
	cat, err := workshopCatalog()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	kind, name := r.PathValue("kind"), r.PathValue("name")
	d, found, err := workshop.Describe(r.Context(), st, cat, kind, name, i18n.RequestLocale(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	if !found {
		writeError(w, r, http.StatusNotFound, workshop.NoSuchItem(kind, name))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleWorkshopPlan(w http.ResponseWriter, r *http.Request) {
	st, ok := s.workshopState(w, r)
	if !ok {
		return
	}
	var req workshopRequest
	if !decodeBody(w, r, &req) {
		return
	}
	_, plan, err := s.workshopPlan(r.Context(), st, req)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleWorkshopApply(w http.ResponseWriter, r *http.Request) {
	st, ok := s.workshopState(w, r)
	if !ok {
		return
	}
	var req workshopRequest
	if !decodeBody(w, r, &req) {
		return
	}
	set, plan, err := s.workshopPlan(r.Context(), st, req)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	res, err := workshop.Apply(r.Context(), st.Store, st.Tenant, set, plan, actorID(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.invalidateCatalog(st.Tenant)
	s.auditWorkshop(r, st.Tenant, plan, res)
	writeJSON(w, http.StatusOK, workshopApplyResponse{Plan: plan, Result: res})
}

func (s *Server) workshopPlan(ctx context.Context, st workshop.StoreState, req workshopRequest) (*workshop.Set, *workshop.Plan, error) {
	var (
		set    *workshop.Set
		source string
		err    error
	)
	switch {
	case len(req.Bundle) > 0 && len(req.Items) > 0:
		return nil, nil, apperr.Invalid("api.workshop.errors.items_or_bundle")
	case len(req.Bundle) > 0:
		set, err = workshop.Parse(req.Bundle)
		source = workshop.SourceImport
	case len(req.Items) > 0:
		var cat *workshop.Catalog
		if cat, err = workshopCatalog(); err == nil {
			set, err = cat.Select(req.Items)
		}
		source = workshop.SourceWorkshop
	default:
		return nil, nil, apperr.Invalid("api.workshop.errors.items_or_bundle")
	}
	if err != nil {
		return nil, nil, err
	}
	env, err := s.workshopEnv(ctx)
	if err != nil {
		return nil, nil, err
	}
	plan, err := workshop.MakePlan(ctx, st, set, source,
		workshop.Choices{Resolutions: req.Resolutions, Names: req.Names}, env)
	if err != nil {
		return nil, nil, err
	}
	workshop.SortItems(plan.Items)
	return set, plan, nil
}

func (s *Server) workshopEnv(ctx context.Context) (workshop.Env, error) {
	runtime := []mcp.Service{}
	if s.mcp.mgr != nil {
		got, err := s.mcp.mgr.List(ctx)
		if err != nil {
			return workshop.Env{}, err
		}
		runtime = got
	}
	regs := []mcp.Registration{}
	if s.mcp.registry != nil {
		got, err := s.mcp.registry.List(ctx)
		if err != nil {
			return workshop.Env{}, err
		}
		regs = got
	}
	known := map[string]bool{}
	merged := mergeMCPList(envMCPServices(s.mcp.env, s.mcp.envForward), runtime, regs)
	for i := range merged {
		sv := &merged[i]
		known[sv.Name] = true
	}
	return workshop.Env{
		KnownMCP:   known,
		StageKnown: func(id string) bool { return s.catalogHas(types.Stage(id)) },
		SaaS:       entitlements.SaaSMode(),
		Locale:     i18n.FromContext(ctx),
	}, nil
}

func (s *Server) auditWorkshop(r *http.Request, tenant string, plan *workshop.Plan, res workshop.Result) {
	if s.auditStore == nil {
		return
	}
	var written []string
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.Resolution != workshop.ResolveKeep {
			written = append(written, fmt.Sprintf("%s/%s:%s", it.Kind, it.Target, it.Resolution))
		}
	}
	summaryKey := "api.workshop.audit.installed"
	if plan.Source == workshop.SourceImport {
		summaryKey = "api.workshop.audit.imported"
	}
	count := len(res.Installed) + len(res.Updated) + len(res.Replaced) + len(res.Copied)
	s.auditStore.LogRequestIn(r, tenant, audit.KindWorkshopApplied, plan.Source, "workshop",
		i18n.Text(summaryKey, "count", count),
		map[string]string{"source": plan.Source, "items": strings.Join(written, ",")})
}

func (s *Server) handleWorkshopExport(w http.ResponseWriter, r *http.Request) {
	st, ok := s.workshopState(w, r)
	if !ok {
		return
	}
	var req workshopExportRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Items) == 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.workshop.errors.export_nothing")
		return
	}
	set, err := workshop.Export(r.Context(), st, req.Items)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeBundle(w, "workspace", set)
}

func (s *Server) handleExportAgent(w http.ResponseWriter, r *http.Request) {
	st, ok := s.workshopState(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	set, err := workshop.ExportAgent(r.Context(), st, name)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeBundle(w, "agent-"+name, set)
}

func (s *Server) handleExportSkill(w http.ResponseWriter, r *http.Request) {
	st, ok := s.workshopState(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	set, err := workshop.ExportSkill(r.Context(), st, name)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeBundle(w, "skill-"+name, set)
}

// Names already passed the store's name rules, so they are safe inside the header.
func writeBundle(w http.ResponseWriter, stem string, set *workshop.Set) {
	//i18n:ignore HTTP header value, not a message
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="octopus-%s.json"`, stem))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(workshop.Encode(set, time.Now()))
}
