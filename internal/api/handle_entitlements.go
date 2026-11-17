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
	Hours   entitlements.HourUsage `json:"hours"`
	Storage task.StorageUsage      `json:"storage"`
}

func (s *Server) entitlementViewFor(r *http.Request, workspaceID string) (entitlementView, error) {
	if s.entitlements == nil {
		ent := entitlements.DefaultFor(entitlements.PlanSelfHosted)
		ent.WorkspaceID = workspaceID
		storage, err := s.taskStore.WorkspaceStorage(r.Context(), workspaceID)
		return entitlementView{Entitlement: ent, Storage: storage}, err
	}
	ent, err := s.entitlements.Get(workspaceID)
	if err != nil {
		return entitlementView{}, err
	}
	hours, err := s.entitlements.HourUsage(r.Context(), workspaceID)
	if err != nil {
		return entitlementView{}, err
	}
	// Clients read the resolved cap from hours.cap_usd; -1 ("the plan's default") would only confuse them.
	ent.ExtraHoursCapUSD = hours.CapUSD
	storage, err := s.taskStore.WorkspaceStorage(r.Context(), workspaceID)
	if err != nil {
		return entitlementView{}, err
	}
	return entitlementView{Entitlement: ent, Hours: hours, Storage: storage}, nil
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

// A limit of 0 means unlimited. Agents and nodes size the plan (hours and storage follow them); the
// other fields override single limits for a custom deal.
type setPlanReq struct {
	Plan              string   `json:"plan"`
	Agents            *int     `json:"agents,omitempty"`
	Nodes             *int     `json:"nodes,omitempty"`
	SeatLimit         *int     `json:"seat_limit,omitempty"`
	ConcurrencyLimit  *int     `json:"concurrency_limit,omitempty"`
	HourQuotaMonthly  *int     `json:"hour_quota_monthly,omitempty"`
	StorageQuotaBytes *int64   `json:"storage_quota_bytes,omitempty"`
	ExtraHoursCapUSD  *float64 `json:"extra_hours_cap_usd,omitempty"`
}

func (req setPlanReq) hasOverrides() bool {
	return req.Agents != nil || req.Nodes != nil || req.SeatLimit != nil || req.ConcurrencyLimit != nil ||
		req.HourQuotaMonthly != nil || req.StorageQuotaBytes != nil || req.ExtraHoursCapUSD != nil
}

func (req setPlanReq) entitlementFor(current entitlements.Entitlement) (entitlements.Entitlement, error) {
	plan := entitlements.Plan(req.Plan)
	if plan == "" && req.hasOverrides() {
		plan = current.Plan
	}
	if !entitlements.KnownPlan(plan) {
		return entitlements.Entitlement{}, apperr.Invalid("api.billing.errors.unknown_plan")
	}
	for _, v := range []*int{req.Agents, req.Nodes, req.SeatLimit, req.ConcurrencyLimit, req.HourQuotaMonthly} {
		if v != nil && *v < 0 {
			return entitlements.Entitlement{}, apperr.Invalid("api.billing.errors.negative_limit")
		}
	}
	if (req.StorageQuotaBytes != nil && *req.StorageQuotaBytes < 0) ||
		(req.ExtraHoursCapUSD != nil && *req.ExtraHoursCapUSD < 0) {
		return entitlements.Entitlement{}, apperr.Invalid("api.billing.errors.negative_limit")
	}
	deref := func(v *int) int {
		if v == nil {
			return 0
		}
		return *v
	}
	ent := entitlements.ForPlan(plan, deref(req.Agents), deref(req.Nodes))
	ent.WorkspaceID = current.WorkspaceID
	ent.TrialEndsAt = current.TrialEndsAt
	if current.ExtraHoursCapUSD >= 0 {
		ent.ExtraHoursCapUSD = current.ExtraHoursCapUSD
	}
	if req.SeatLimit != nil {
		ent.SeatLimit = *req.SeatLimit
	}
	if req.ConcurrencyLimit != nil {
		ent.ConcurrencyLimit = *req.ConcurrencyLimit
	}
	if req.HourQuotaMonthly != nil {
		ent.HourQuotaMonthly = *req.HourQuotaMonthly
	}
	if req.StorageQuotaBytes != nil {
		ent.StorageQuotaBytes = *req.StorageQuotaBytes
	}
	if req.ExtraHoursCapUSD != nil {
		ent.ExtraHoursCapUSD = *req.ExtraHoursCapUSD
	}
	return ent, nil
}

type extraCapReq struct {
	CapUSD *float64 `json:"cap_usd"`
}

// Workspace admins choose how much they accept to spend on extra hours: it is their money, and the
// operator never has to be asked to raise it.
func (s *Server) handleSetExtraHoursCap(w http.ResponseWriter, r *http.Request) {
	if s.entitlements == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.billing.errors.entitlements_disabled")
		return
	}
	wid, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	var req extraCapReq
	if !decodeBody(w, r, &req) {
		return
	}
	if req.CapUSD == nil || *req.CapUSD < 0 || *req.CapUSD > maxExtraCapUSD {
		writeErrKey(w, r, http.StatusBadRequest, "api.billing.errors.bad_extra_cap", "max", maxExtraCapUSD)
		return
	}
	if err := s.entitlements.SetExtraHoursCap(wid, *req.CapUSD); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if s.auditStore != nil {
		s.auditStore.LogRequest(r, "workspace.extra_hours_cap_changed", wid, "workspace",
			i18n.Text("api.billing.audit.extra_cap_set", "cap", *req.CapUSD), nil)
	}
	view, err := s.entitlementViewFor(r, wid)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// A typo of three extra zeros should not let a workspace run up a five-figure bill.
const maxExtraCapUSD = 10000

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

// seatLimitForWorkspace caps people per workspace in the SaaS only. Self-hosted never blocks anyone,
// on Docker or on Kubernetes: its license counts people and the contract settles the rest.
func (s *Server) seatLimitForWorkspace(workspaceID string) int {
	if s.entitlements == nil || !entitlements.SaaSMode() {
		return 0
	}
	ent, err := s.entitlements.Get(workspaceID)
	if err != nil {
		return 0 // fail open on read error: billing limits must not lock admins out
	}
	return ent.SeatLimit
}
