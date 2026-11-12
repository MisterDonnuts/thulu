package api

import (
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/MisterDonnuts/thulu/internal/edition"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Public route: Stripe sends no Thulu credentials, so the Stripe signature is the only
// authentication.
func (s *Server) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	provider := entitlements.BillingProvider
	if s.entitlements == nil || provider == nil || !edition.Has(edition.SaaS) {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.billing.errors.disabled")
		return
	}
	secret := os.Getenv("OCTOPUS_STRIPE_WEBHOOK_SECRET")
	if secret == "" {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.billing.errors.webhook_unconfigured")
		return
	}

	payload, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.billing.errors.read_body")
		return
	}
	if vErr := provider.VerifySignature(payload, r.Header.Get("Stripe-Signature"), secret, time.Now()); vErr != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.billing.errors.invalid_signature")
		return
	}

	change, ok, err := provider.ResolvePlanChange(payload)
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.billing.errors.bad_event")
		return
	}
	if !ok {
		// Ack signed events we ignore: Stripe retries any non-2xx.
		w.WriteHeader(http.StatusOK)
		return
	}

	setErr := s.entitlements.ApplyPlan(change.WorkspaceID, change.Plan, change.Agents, change.Nodes)
	if setErr == nil && change.Customer != "" {
		setErr = s.entitlements.SetBillingCustomer(change.WorkspaceID, change.Customer)
	}
	if setErr != nil {
		log.Printf("[billing] %s", i18n.Log("api.billing.log.apply_plan_failed",
			"plan", change.Plan, "workspace", logx.Safe(change.WorkspaceID), "err", setErr))
		writeErrKey(w, r, http.StatusInternalServerError, "api.billing.errors.apply_plan_failed")
		return
	}
	if s.auditStore != nil {
		s.auditStore.LogRequest(r, "workspace.plan_changed", change.WorkspaceID, "workspace",
			i18n.Text("api.billing.audit.plan_set_stripe", "plan", change.Plan), map[string]string{"source": "stripe"})
	}
	w.WriteHeader(http.StatusOK)
}
