package auth

import (
	"context"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/middleware"
)

const (
	ActorUser   = "user"
	ActorAPIKey = "api_key"
	ActorAgent  = "agent"
	ActorDev    = "dev_mode"
	ActorSystem = "system"
	ActorRunner = "runner"
)

type Actor struct {
	Type  string
	ID    string
	Label string
	Email string
	// Empty for dev mode and for per-run keys minted before keys recorded a workspace; callers
	// resolve those from the task.
	WorkspaceID string
}

func ActorFromRequest(r *http.Request) Actor {
	if r == nil {
		return Actor{Type: ActorSystem, ID: "system"}
	}
	return ActorFromContext(r.Context())
}

func ActorFromContext(ctx context.Context) Actor {
	if IsAPIKeyAuthed(ctx) {
		a := Actor{
			Type:        ActorAPIKey,
			ID:          "key:" + APIKeyID(ctx),
			Label:       APIKeyName(ctx),
			WorkspaceID: APIKeyWorkspace(ctx),
		}
		if scope := APIKeyRunScope(ctx); scope.IsRun() {
			a.Type = ActorAgent
			if a.Label == "" {
				a.Label = runKeyName(scope)
			}
		}
		return a
	}
	if c := ClaimsFromContext(ctx); c != nil {
		return Actor{Type: ActorUser, ID: c.UserID, Label: c.Email, Email: c.Email, WorkspaceID: c.WorkspaceID}
	}
	if DevSession(ctx) {
		return Actor{Type: ActorDev, ID: "dev-mode", Label: i18n.Text("auth.labels.dev_actor")}
	}
	return Actor{Type: ActorSystem, ID: "system"}
}

type AuditEvent struct {
	Kind         string
	WorkspaceID  string
	Actor        Actor
	ResourceID   string
	ResourceType string
	ClientIP     string
	UserAgent    string
	Summary      string
}

type AuditFn func(AuditEvent)

func (h *Handler) auditRequest(r *http.Request, kind, wsID, resourceID, resourceType, summary string) {
	actor := ActorFromRequest(r)
	if wsID == "" {
		wsID = actor.WorkspaceID
	}
	h.emitAudit(r, AuditEvent{
		Kind: kind, WorkspaceID: wsID, Actor: actor,
		ResourceID: resourceID, ResourceType: resourceType, Summary: summary,
	})
}

func (h *Handler) auditUser(r *http.Request, kind, userID, email, wsID, summary string) {
	h.emitAudit(r, AuditEvent{
		Kind:         kind,
		WorkspaceID:  wsID,
		Actor:        Actor{Type: ActorUser, ID: userID, Label: email, Email: email, WorkspaceID: wsID},
		ResourceID:   userID,
		ResourceType: "user",
		Summary:      summary,
	})
}

func (h *Handler) emitAudit(r *http.Request, e AuditEvent) {
	if h.auditFn == nil {
		return
	}
	if r != nil {
		e.ClientIP = middleware.ClientIP(r)
		e.UserAgent = r.UserAgent()
	}
	h.auditFn(e)
}
