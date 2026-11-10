package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// SSE is notification only: no replay or Last-Event-ID, the bus drops events for lagging
// subscribers, and clients refetch over REST on every (re)connect.
// The bus is in-memory, so events don't cross API replicas.
func (s *Server) handleChatEvents(w http.ResponseWriter, r *http.Request) {
	if s.chatBus == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.chats.errors.not_configured")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErrKey(w, r, http.StatusInternalServerError, "api.chats.errors.streaming_unsupported")
		return
	}
	visible := s.chatEventFilter(r)
	loc := i18n.RequestLocale(r)

	events, cancel := s.chatBus.Subscribe()
	defer cancel()

	setSSEHeaders(w)
	w.WriteHeader(http.StatusOK)
	// Write immediately so EventSource fires onopen and the client resyncs without waiting for an
	// event.
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil { //i18n:ignore SSE protocol
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	stillAllowed := s.streamCredentialCheck(r)
	recheck := time.NewTicker(s.streamRecheckEvery())
	defer recheck.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-recheck.C:
			if !stillAllowed(r.Context()) {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil { //i18n:ignore SSE protocol
				return
			}
			flusher.Flush()
		case ev := <-events:
			if !visible(ev) {
				continue
			}
			ev.Message = ev.Message.InLocale(loc)
			if !writeChatEvent(w, flusher, ev) {
				return
			}
		}
	}
}

func writeChatEvent(w http.ResponseWriter, flusher http.Flusher, ev chat.Event) bool {
	data, err := json.Marshal(ev)
	if err != nil {
		return true
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil { //i18n:ignore SSE protocol
		return false
	}
	flusher.Flush()
	return true
}

func (s *Server) chatEventFilter(r *http.Request) func(chat.Event) bool {
	wid, unrestricted := s.callerWorkspace(r)
	runScope := auth.APIKeyRunScope(r.Context())
	viewer := chatViewer(r)
	return func(ev chat.Event) bool {
		if !workspaceVisible(ev.WorkspaceID, wid, unrestricted) || !ev.Listed(viewer) {
			return false
		}
		return !runScope.IsRun() || ev.ChatID == runScope.ChatID
	}
}
