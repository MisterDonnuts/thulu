package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// On: only the chat's creator, signed in (no API key), with workspace policy allowing it.
// Off: the creator or a workspace admin, since turning it off only removes authority.
func (s *Server) handleChatDelegation(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	person, ok := requirePerson(w, r, "api.chats.labels.person_delegating")
	if !ok {
		return
	}
	if body.Enabled {
		s.startDelegation(w, r, c, person)
		return
	}
	role, _ := auth.PersonRole(r.Context())
	if person.Name != c.DelegatedBy && !role.AtLeast(auth.RoleAdmin) {
		writeErrKey(w, r, http.StatusForbidden, "api.chats.errors.end_delegation_forbidden")
		return
	}
	updated, err := s.chatSvc.EndDelegation(r.Context(), c.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.emit(r, audit.KindChatDelegationEnded, "", c.ID, "chat",
		i18n.Text("api.chats.audit.delegation_ended", "agent", c.AgentType, "person", orNobody(c.DelegatedBy)), nil)
	writeJSON(w, http.StatusOK, s.chatView(r.Context(), updated, false))
}

func (s *Server) startDelegation(w http.ResponseWriter, r *http.Request, c *chat.Chat, person auth.Author) {
	if c.CreatedBy != person.Name {
		writeErrKey(w, r, http.StatusForbidden, "api.chats.errors.start_delegation_forbidden")
		return
	}
	allowed, err := s.chatSvc.Store().DelegationAllowed(r.Context(), c.WorkspaceID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if !allowed {
		writeErrKey(w, r, http.StatusForbidden, "api.chats.errors.delegation_disabled")
		return
	}
	updated, err := s.chatSvc.Delegate(r.Context(), c.ID, person.Name)
	switch {
	case errors.Is(err, chatruntime.ErrTurnInFlight):
		writeErrKey(w, r, http.StatusConflict, "api.chats.errors.delegation_turn_running")
		return
	case errors.Is(err, chatruntime.ErrArchived):
		writeErrKey(w, r, http.StatusConflict, "api.chats.errors.archived")
		return
	case errors.Is(err, chatruntime.ErrNotDelegable):
		writeError(w, r, http.StatusConflict, err)
		return
	case err != nil:
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.emit(r, audit.KindChatDelegationStarted, "", c.ID, "chat",
		i18n.Text("api.chats.audit.delegation_started", "agent", c.AgentType, "person", person.Name,
			"until", updated.DelegatedUntil.UTC().Format(time.RFC3339)), nil)
	writeJSON(w, http.StatusOK, s.chatView(r.Context(), updated, false))
}

// API keys, operator keys included, are refused: this records a person's consent, not a script's.
func requirePerson(w http.ResponseWriter, r *http.Request, key string) (auth.Author, bool) {
	if _, ok := auth.PersonRole(r.Context()); !ok {
		writeErrKey(w, r, http.StatusForbidden, "api.chats.errors.person_required", "what", i18n.TCtx(r.Context(), key))
		return auth.Author{}, false
	}
	return requireAuthor(w, r)
}

func orNobody(s string) string {
	if s == "" {
		return i18n.Text("api.chats.labels.nobody")
	}
	return s
}

// Must wrap outside RequireRole: the held request later runs with the delegating person's role, not
// the key's.
// Requests the key may never make (personOnlyWrite, chats) are not held, so they get refused
// instead of approved by habit.
func (s *Server) holdForConfirmation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope := auth.APIKeyRunScope(r.Context())
		if !scope.Delegated() || !heldForConfirmation(r) {
			next(w, r)
			return
		}
		s.holdAction(w, r, scope)
	}
}

func heldForConfirmation(r *http.Request) bool {
	_, route, _ := strings.Cut(r.Pattern, " ")
	if heldGithubTool(route) != "" {
		return true
	}
	if r.Method != http.MethodDelete {
		return false
	}
	return personOnlyWrite(route) == "" &&
		auth.PermissionForRequest(r.Method, r.URL.Path) == auth.PermTasksWrite
}

const maxHeldBody = 64 << 10

func (s *Server) holdAction(w http.ResponseWriter, r *http.Request, scope auth.RunScope) {
	if s.chatSvc == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.chats.errors.not_configured")
		return
	}
	c, err := s.chatSvc.Store().GetChat(r.Context(), scope.ChatID)
	if err != nil || !c.Delegated(time.Now()) {
		writeErrKey(w, r, http.StatusForbidden, "api.chats.errors.delegation_over")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHeldBody))
	if err != nil {
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.chats.errors.held_body_too_large")
		return
	}
	summary, ok := s.describeHeld(w, r, body)
	if !ok {
		return
	}
	a := &chat.Action{
		ChatID: c.ID, WorkspaceID: c.WorkspaceID, Agent: scope.Agent,
		Method: r.Method, Path: r.URL.RequestURI(), Body: body, Summary: summary,
	}
	if err := s.chatSvc.Store().CreateAction(r.Context(), a); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.publishAction(chat.EventActionPending, c, a)
	s.emit(r, audit.KindChatActionHeld, "", a.ID, "chat_action",
		i18n.Text("api.chats.audit.action_held", "agent", scope.Agent, "person", c.DelegatedBy, "summary", summary), nil)
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":    "awaiting_confirmation",
		"action_id": a.ID,
		"message":   i18n.TCtx(r.Context(), "api.chats.hints.awaiting_confirmation", "person", c.DelegatedBy),
	})
}

// A task the key cannot see is answered 404 here instead of being held, matching the handler.
func (s *Server) describeHeld(w http.ResponseWriter, r *http.Request, body []byte) (string, bool) {
	_, route, _ := strings.Cut(r.Pattern, " ")
	if tool := heldGithubTool(route); tool != "" {
		summary, err := s.describeHeldGithub(r, tool, body)
		if err != nil {
			writeError(w, r, 0, err)
			return "", false
		}
		return summary, true
	}
	if !strings.HasPrefix(route, "/tasks/{id}") {
		return r.Method + " " + r.URL.Path, true
	}
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return "", false
	}
	if route == "/tasks/{id}" {
		return i18n.Text("api.chats.labels.held_delete_task", "title", t.Title), true
	}
	return i18n.Text("api.chats.labels.held_delete_on_task",
		"route", strings.TrimPrefix(route, "/tasks/{id}"), "title", t.Title), true
}

func (s *Server) publishAction(kind chat.EventType, c *chat.Chat, a *chat.Action) {
	s.chatBus.Publish(chat.Event{Type: kind, Action: a}.For(c))
}

func (s *Server) handleListChatActions(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	acts, err := s.chatSvc.Store().ListActions(r.Context(), c.ID, chat.ActionStatus(r.URL.Query().Get("status")))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, acts)
}

// Replays with the confirming person's credentials, so confirming grants nothing they lacked.
// DecideAction claims the action first so a double click or two tabs run it once.
func (s *Server) handleConfirmChatAction(w http.ResponseWriter, r *http.Request) {
	c, a, person, ok := s.decidableAction(w, r)
	if !ok {
		return
	}
	if !c.Delegated(time.Now()) {
		writeErrKey(w, r, http.StatusConflict, "api.chats.errors.delegation_over_for_you")
		return
	}
	a, err := s.chatSvc.Store().DecideAction(r.Context(), c.ID, a.ID, chat.ActionConfirmed, person.Name)
	if !s.decided(w, r, err) {
		return
	}
	code, errText := s.replayAsCaller(r, a)
	logx.Warn(s.chatSvc.Store().RecordActionResult(r.Context(), a, code, errText), "api.chats.log.record_action_result_failed", "action", a.ID)
	s.publishAction(chat.EventActionDecided, c, a)
	s.emit(r, audit.KindChatActionConfirmed, "", a.ID, "chat_action",
		i18n.Text("api.chats.audit.action_confirmed", "person", person.Name, "agent", a.Agent, "summary", a.Summary, "code", code), nil)
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleCancelChatAction(w http.ResponseWriter, r *http.Request) {
	c, a, person, ok := s.decidableAction(w, r)
	if !ok {
		return
	}
	a, err := s.chatSvc.Store().DecideAction(r.Context(), c.ID, a.ID, chat.ActionCanceled, person.Name)
	if !s.decided(w, r, err) {
		return
	}
	s.publishAction(chat.EventActionDecided, c, a)
	s.emit(r, audit.KindChatActionCanceled, "", a.ID, "chat_action",
		i18n.Text("api.chats.audit.action_canceled", "person", person.Name, "agent", a.Agent, "summary", a.Summary), nil)
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) decidableAction(w http.ResponseWriter, r *http.Request) (*chat.Chat, *chat.Action, auth.Author, bool) {
	if !s.chatRequired(w, r) {
		return nil, nil, auth.Author{}, false
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return nil, nil, auth.Author{}, false
	}
	person, ok := requirePerson(w, r, "api.chats.labels.person_deciding")
	if !ok {
		return nil, nil, auth.Author{}, false
	}
	if c.DelegatedBy == "" || person.Name != c.DelegatedBy {
		writeErrKey(w, r, http.StatusForbidden, "api.chats.errors.decide_forbidden")
		return nil, nil, auth.Author{}, false
	}
	a, err := s.chatSvc.Store().GetAction(r.Context(), c.ID, r.PathValue("actionId"))
	if errors.Is(err, chat.ErrNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.chats.errors.action_not_found")
		return nil, nil, auth.Author{}, false
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return nil, nil, auth.Author{}, false
	}
	return c, a, person, true
}

func (s *Server) decided(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, chat.ErrActionDecided):
		writeErrKey(w, r, http.StatusConflict, "api.chats.errors.already_decided")
	case errors.Is(err, chat.ErrNotFound):
		writeErrKey(w, r, http.StatusNotFound, "api.chats.errors.action_not_found")
	default:
		writeError(w, r, http.StatusInternalServerError, err)
	}
	return false
}

// Middleware outside the mux (auth, CSRF, rate limits) already ran for r and is not re-run.
// The confirmed action in the context is what lets the github_held.go routes through.
func (s *Server) replayAsCaller(r *http.Request, a *chat.Action) (int, string) {
	req, err := http.NewRequestWithContext(withConfirmedAction(r.Context(), a), a.Method, a.Path, bytes.NewReader(a.Body))
	if err != nil {
		return http.StatusInternalServerError, apperr.Text(err)
	}
	req.Header = r.Header.Clone()
	req.Header.Del("Content-Length")
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = r.RemoteAddr
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code < 400 {
		return rec.Code, ""
	}
	var refusal struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &refusal) == nil && refusal.Error != "" {
		return rec.Code, refusal.Error
	}
	text := strings.TrimSpace(rec.Body.String())
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return rec.Code, text
}

func (s *Server) handleGetDelegationPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	// targetWorkspace, not newResourceWorkspace: the latter 400s a dev-mode dashboard that names no
	// workspace,
	// and the drawer then assumes delegation is allowed.
	wid, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	allowed, err := s.chatSvc.Store().DelegationAllowed(r.Context(), wid)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": allowed, "ttl_hours": int(chat.DelegationTTL.Hours())})
}

func (s *Server) handlePutDelegationPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if _, ok := requirePerson(w, r, "api.chats.labels.person_policy"); !ok {
		return
	}
	wid, ok := s.newResourceWorkspace(w, r, "policy")
	if !ok {
		return
	}
	if err := s.chatSvc.Store().SetDelegationAllowed(r.Context(), wid, body.Enabled); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if !body.Enabled {
		if err := s.chatSvc.EndWorkspaceDelegations(r.Context(), wid); err != nil {
			writeError(w, r, http.StatusInternalServerError, apperr.Wrap(err, "api.chats.errors.policy_off_partial"))
			return
		}
	}
	summary := i18n.Text("api.chats.audit.policy_off")
	if body.Enabled {
		summary = i18n.Text("api.chats.audit.policy_on")
	}
	s.emit(r, audit.KindChatDelegationPolicy, "", wid, "workspace", summary, nil)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": body.Enabled, "ttl_hours": int(chat.DelegationTTL.Hours())})
}
