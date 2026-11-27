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
	"github.com/MisterDonnuts/thulu/internal/task"
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
	// Make the board a flow installs the workspace's default.
	FlowDefault bool `json:"flow_default,omitempty"`
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
	entries, err := workshop.List(r.Context(), st, s.flowBoards(st.Tenant), cat, i18n.RequestLocale(r))
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
	d, found, err := workshop.Describe(r.Context(), st, s.flowBoards(st.Tenant), cat, kind, name, i18n.RequestLocale(r))
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
	if err := s.applyWorkshopFlows(r, st.Tenant, set, plan, &res, req.FlowDefault); err != nil {
		// The agents and skills are already written: the log keeps them, and the flows that failed.
		s.auditWorkshop(r, st.Tenant, plan, res, err)
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.auditWorkshop(r, st.Tenant, plan, res, nil)
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
	env.Boards = s.flowBoards(st.Tenant)
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
	if s.sandbox.mgr != nil {
		known[mcp.SandboxServer] = true
	}
	return workshop.Env{
		KnownMCP:   known,
		StageKnown: func(id string) bool { return s.catalogHas(types.Stage(id)) },
		SaaS:       entitlements.SaaSMode(),
		Locale:     i18n.FromContext(ctx),
	}, nil
}

// Nil without a board store: flows then list as available and refuse to install.
func (s *Server) flowBoards(tenant string) workshop.BoardState {
	if s.stageAdmin == nil {
		return nil
	}
	return workshop.StageBoards{Store: s.stageAdmin, Tenant: tenant, Count: s.stageTaskCount, CountType: s.typeTaskCount}
}

func (s *Server) stageTaskCount(ctx context.Context, boardID, stage string) (int, error) {
	if s.taskStore == nil {
		return 0, nil
	}
	tasks, err := s.taskStore.ListInBoardStage(ctx, boardID, stage)
	return len(tasks), err
}

func (s *Server) typeTaskCount(ctx context.Context, boardID, typeID, stage string) (int, error) {
	if s.taskStore == nil {
		return 0, nil
	}
	var (
		tasks []task.Task
		err   error
	)
	if stage == "" {
		tasks, err = s.taskStore.ListByBoardType(ctx, boardID, typeID)
	} else {
		tasks, err = s.taskStore.ListInBoardTypeStage(ctx, boardID, typeID, stage)
	}
	return len(tasks), err
}

// Boards go after the agents and skills they name, so a board never points at an agent the same
// apply failed to write.
func (s *Server) applyWorkshopFlows(r *http.Request, tenant string, set *workshop.Set, plan *workshop.Plan,
	res *workshop.Result, makeDefault bool,
) error {
	if len(set.Flows) == 0 {
		return nil
	}
	var writer workshop.BoardWriter
	if s.stageAdmin != nil {
		writer = s.stageAdmin
	}
	if err := workshop.ApplyFlows(r.Context(), writer, tenant, set, plan, res); err != nil {
		return err
	}
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.Kind != workshop.KindFlow || it.Resolution == workshop.ResolveKeep || it.Resolution == workshop.ResolveSkip {
			continue
		}
		s.addStagePriorities(r.Context(), s.boards.Board(it.Target).List())
		if makeDefault {
			if err := s.stageAdmin.SetDefaultBoard(r.Context(), it.Target); err != nil {
				return err
			}
		}
		kind := audit.KindBoardCreated
		if it.Resolution == workshop.ResolveUpdate || it.Resolution == workshop.ResolveReplace {
			kind = audit.KindBoardUpdated
		}
		s.emitBoardsChanged(r, kind, it.Target,
			i18n.Text("api.workshop.audit.flow_board", "flow", it.Name, "board", it.Target, "resolution", string(it.Resolution)))
	}
	return nil
}

// auditWorkshop logs what an apply wrote. With flowErr set, a flow board failed after the agents
// and skills were written: the flows that did not land are listed as "failed" and the entry carries
// the error.
func (s *Server) auditWorkshop(r *http.Request, tenant string, plan *workshop.Plan, res workshop.Result, flowErr error) {
	if s.auditStore == nil {
		return
	}
	var written []string
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.Resolution == workshop.ResolveKeep {
			continue
		}
		outcome := string(it.Resolution)
		if flowErr != nil && it.Kind == workshop.KindFlow && it.Resolution != workshop.ResolveSkip && !res.Wrote(it.Kind, it.Target) {
			outcome = "failed"
		}
		written = append(written, fmt.Sprintf("%s/%s:%s", it.Kind, it.Target, outcome))
	}
	meta := map[string]string{"source": plan.Source, "items": strings.Join(written, ",")}
	if flowErr != nil {
		meta["error"] = flowErr.Error()
	}
	summaryKey := workshopAuditKey(plan.Source == workshop.SourceImport, flowErr != nil)
	count := len(res.Installed) + len(res.Updated) + len(res.Replaced) + len(res.Copied)
	s.auditStore.LogRequestIn(r, tenant, audit.KindWorkshopApplied, plan.Source, "workshop",
		i18n.Text(summaryKey, "count", count), meta)
}

// workshopAuditKey names every summary key literally so the i18n guard sees
// them in code.
func workshopAuditKey(imported, partial bool) string {
	switch {
	case imported && partial:
		return "api.workshop.audit.imported_partial"
	case imported:
		return "api.workshop.audit.imported"
	case partial:
		return "api.workshop.audit.installed_partial"
	default:
		return "api.workshop.audit.installed"
	}
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
