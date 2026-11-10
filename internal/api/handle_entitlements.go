package api

import (
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Entitlement is embedded so the limits stay top-level for clients that predate usage.
type entitlementView struct {
	entitlements.Entitlement
	Runs    entitlements.RunUsage `json:"runs"`
	Storage task.StorageUsage     `json:"storage"`
}

func (s *Server) entitlementViewFor(r *http.Request, workspaceID string) (entitlementView, error) {
	if s.entitlements == nil {
		ent := entitlements.DefaultFor(entitlements.PlanSelfHosted)
		ent.WorkspaceID = workspaceID
		storage, err := s.taskStore.WorkspaceStorage(r.Context(), workspaceID)
		return entitlementView{Entitlement: ent, Runs: entitlements.RunUsage{Remaining: -1}, Storage: storage}, err
	}
	ent, err := s.entitlements.Get(workspaceID)
	if err != nil {
		return entitlementView{}, err
	}
	runs, err := s.entitlements.RunUsage(r.Context(), workspaceID)
	if err != nil {
		return entitlementView{}, err
	}
	storage, err := s.taskStore.WorkspaceStorage(r.Context(), workspaceID)
	if err != nil {
		return entitlementView{}, err
	}
	return entitlementView{Entitlement: ent, Runs: runs, Storage: storage}, nil
}

func (s *Server) handleGetEntitlements(w http.ResponseWriter, r *http.Request) {
	wid, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	view, err := s.entitlementViewFor(r, wid)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// A limit of 0 means unlimited.
type setPlanReq struct {
	Plan              string `json:"plan"`
	SeatLimit         *int   `json:"seat_limit,omitempty"`
	ConcurrencyLimit  *int   `json:"concurrency_limit,omitempty"`
	RunQuotaMonthly   *int   `json:"run_quota_monthly,omitempty"`
	StorageQuotaBytes *int64 `json:"storage_quota_bytes,omitempty"`
}

func (req setPlanReq) hasOverrides() bool {
	return req.SeatLimit != nil || req.ConcurrencyLimit != nil ||
		req.RunQuotaMonthly != nil || req.StorageQuotaBytes != nil
}

func (req setPlanReq) entitlementFor(current entitlements.Entitlement) (entitlements.Entitlement, error) {
	plan := entitlements.Plan(req.Plan)
	if plan == "" && req.hasOverrides() {
		plan = current.Plan
	}
	switch plan {
	case entitlements.PlanFree, entitlements.PlanTeam, entitlements.PlanBusiness,
		entitlements.PlanEnterprise, entitlements.PlanSelfHosted:
	default:
		return entitlements.Entitlement{}, apperr.Invalid("api.billing.errors.unknown_plan")
	}
	ent := entitlements.DefaultFor(plan)
	ent.WorkspaceID = current.WorkspaceID
	for _, v := range []*int{req.SeatLimit, req.ConcurrencyLimit, req.RunQuotaMonthly} {
		if v != nil && *v < 0 {
			return entitlements.Entitlement{}, apperr.Invalid("api.billing.errors.negative_limit")
		}
	}
	if req.StorageQuotaBytes != nil && *req.StorageQuotaBytes < 0 {
		return entitlements.Entitlement{}, apperr.Invalid("api.billing.errors.negative_limit")
	}
	if req.SeatLimit != nil {
		ent.SeatLimit = *req.SeatLimit
	}
	if req.ConcurrencyLimit != nil {
		ent.ConcurrencyLimit = *req.ConcurrencyLimit
	}
	if req.RunQuotaMonthly != nil {
		ent.RunQuotaMonthly = *req.RunQuotaMonthly
	}
	if req.StorageQuotaBytes != nil {
		ent.StorageQuotaBytes = *req.StorageQuotaBytes
	}
	return ent, nil
}

// Operator-only: when any workspace admin could call it, a free tenant could upgrade itself to
// Enterprise.
func (s *Server) handleSetEntitlementPlan(w http.ResponseWriter, r *http.Request) {
	if s.entitlements == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.billing.errors.entitlements_disabled")
		return
	}
	if !s.requireOperator(w, r, "api.scope.operator_surfaces.plan_assignment", "api.scope.operator_verbs.change") {
		return
	}
	var req setPlanReq
	if !decodeBody(w, r, &req) {
		return
	}
	wsID := strings.TrimSpace(r.URL.Query().Get("workspace"))
	if wsID == "" {
		var ok bool
		if wsID, ok = s.workspaceIDFromRequest(w, r); !ok {
			return
		}
	}
	current, err := s.entitlements.Get(wsID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	want, err := req.entitlementFor(current)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err = s.entitlements.Set(want); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if s.auditStore != nil {
		actorID := ""
		if c := auth.ClaimsFromContext(r.Context()); c != nil {
			actorID = c.UserID
		}
		s.auditStore.LogRequest(r, "workspace.plan_changed", wsID, "workspace",
			i18n.Text("api.billing.audit.plan_set", "plan", string(want.Plan)), map[string]string{"actor": actorID})
	}
	view, err := s.entitlementViewFor(r, wsID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) seatLimitForWorkspace(workspaceID string) int {
	if s.entitlements == nil {
		return 0
	}
	ent, err := s.entitlements.Get(workspaceID)
	if err != nil {
		return 0 // fail open on read error: billing limits must not lock admins out
	}
	return ent.SeatLimit
}
