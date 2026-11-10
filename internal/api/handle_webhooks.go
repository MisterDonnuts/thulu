package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/netguard"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	endpoints, err := s.webhookStore.ListEndpoints(r.Context(), wsID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	// The dashboard does whs.length on the answer; null throws and leaves the panel on "Loading…".
	endpoints = nonNil(endpoints)
	writeJSON(w, http.StatusOK, endpoints)
}

func (s *Server) handleListWebhookEvents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, webhook.Events())
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}

	var body struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.URL == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.webhooks.errors.url_required")
		return
	}
	// The delivery client refuses loopback, private and metadata addresses on every connect; this
	// check only surfaces it at save time.
	// An unresolved host is not a policy verdict, so it passes and the connect-time check decides.
	if err := netguard.CheckURL(r.Context(), body.URL); err != nil && !errors.Is(err, netguard.ErrUnresolved) {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.webhooks.errors.invalid_url"))
		return
	}
	if len(body.Events) == 0 {
		body.Events = []string{"*"}
	}
	if unknown := webhook.UnknownEventPatterns(body.Events); len(unknown) > 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.webhooks.errors.unknown_event",
			"events", strings.Join(unknown, ", "))
		return
	}

	ep, err := s.webhookStore.CreateEndpoint(r.Context(), wsID, body.URL, body.Events)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, ep)
}

func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.webhooks.errors.id_required")
		return
	}

	if s.webhookDisp == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.webhooks.errors.dispatcher_unavailable")
		return
	}

	testPayload := map[string]any{
		"type":         "test",
		"workspace_id": wsID,
		"message":      i18n.Text("api.webhooks.text.test_delivery"),
		"endpoint_id":  id,
	}
	delivered, err := s.webhookDisp.DeliverTest(r.Context(), id, wsID, testPayload)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	delivered["endpoint_id"] = id
	if msg, ok := delivered["error"].(string); ok {
		delivered["error"] = webhook.FailureText(msg, i18n.RequestLocale(r))
	}
	writeJSON(w, http.StatusOK, delivered)
}

// Rotation is also how an endpoint with legacy_signing moves to HMAC keyed with the secret itself.
func (s *Server) handleRotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	ep, err := s.webhookStore.RotateSecret(r.Context(), id, wsID)
	if errors.Is(err, sql.ErrNoRows) {
		writeErrKey(w, r, http.StatusNotFound, "api.webhooks.errors.not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.emit(r, audit.KindWebhookRotated, "", ep.ID, "webhook",
		i18n.Text("api.webhooks.audit.secret_rotated", "url", ep.URL), nil)
	writeJSON(w, http.StatusOK, ep)
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.webhooks.errors.id_required")
		return
	}
	if err := s.webhookStore.DeleteEndpoint(r.Context(), id, wsID); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const deliveriesListed = 50

func (s *Server) handleListWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := s.webhookStore.GetEndpoint(r.Context(), id, wsID); errors.Is(err, sql.ErrNoRows) {
		writeErrKey(w, r, http.StatusNotFound, "api.webhooks.errors.not_found")
		return
	} else if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	list, err := s.webhookStore.ListDeliveries(r.Context(), id, wsID, deliveriesListed)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	locale := i18n.RequestLocale(r)
	for i := range list {
		list[i].Error = webhook.FailureText(list[i].Error, locale)
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRetryWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	if s.webhookDisp == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.webhooks.errors.dispatcher_unavailable")
		return
	}
	id, deliveryID := r.PathValue("id"), r.PathValue("deliveryId")
	err := s.webhookDisp.Retry(r.Context(), deliveryID, id, wsID)
	if errors.Is(err, sql.ErrNoRows) {
		writeErrKey(w, r, http.StatusNotFound, "api.webhooks.errors.delivery_not_found")
		return
	}
	if errors.Is(err, webhook.ErrNotRetryable) {
		writeErrKey(w, r, http.StatusConflict, "api.webhooks.errors.delivery_not_retryable")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}
