package api

import (
	"context"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/middleware"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

func (s *Server) emit(
	r *http.Request,
	auditKind audit.EventKind,
	whEvent webhook.EventType,
	resourceID, resourceType, summary string,
	data any,
) {
	actor := auth.ActorFromRequest(r)
	wsID, unrestricted := s.callerWorkspace(r)
	if wsID == "" && unrestricted {
		wsID = "default"
	}
	s.record(r.Context(), audit.Entry{
		Kind:         auditKind,
		WorkspaceID:  wsID,
		ActorID:      actor.ID,
		ActorType:    actor.Type,
		ActorLabel:   actor.Label,
		ActorEmail:   actor.Email,
		ResourceID:   resourceID,
		ResourceType: resourceType,
		Summary:      summary,
		ClientIP:     middleware.ClientIP(r),
		UserAgent:    r.Header.Get("User-Agent"),
	}, whEvent, data)
}

func (s *Server) emitSystem(
	ctx context.Context,
	wsID string,
	auditKind audit.EventKind,
	whEvent webhook.EventType,
	resourceID, resourceType, summary string,
	data any,
) {
	s.record(ctx, audit.Entry{
		Kind:         auditKind,
		WorkspaceID:  wsID,
		ActorID:      "system",
		ActorType:    auth.ActorSystem,
		ResourceID:   resourceID,
		ResourceType: resourceType,
		Summary:      summary,
	}, whEvent, data)
}

func (s *Server) record(ctx context.Context, e audit.Entry, whEvent webhook.EventType, data any) {
	if s.auditStore != nil {
		s.auditStore.Log(e)
	}
	if whEvent != "" {
		webhook.EmitBackground(ctx, s.webhookDisp, e.WorkspaceID, whEvent, data)
	}
}
