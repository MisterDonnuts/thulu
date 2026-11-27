package api

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/eventhub"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// A chat turn's live feed and transcript, stopping it and its queue.

const topicChatTurnPfx = "chatturn:"

// A message mentions at most this many tasks the agent gets context for; past it the ids still
// reach the agent in the text.
const maxChatRefs = 10

const (
	maxRefBodyRunes  = 1200
	maxHandoffBytes  = 12 << 10
	handoffMessages  = 40
	handoffTextRunes = 2000
)

// chatTurnContext is what the agent reads before a person's message: the tasks it mentions and, on
// a chat that continues another one, that conversation. It also records both in meta.
func (s *Server) chatTurnContext(
	w http.ResponseWriter, r *http.Request, c *chat.Chat, refs []string, handoffFrom string, meta map[string]string,
) (string, bool) {
	ctx := r.Context()
	var b strings.Builder
	if handoffFrom != "" {
		from, ok := s.visibleChat(r, handoffFrom)
		if !ok || from.ID == c.ID {
			writeErrKey(w, r, http.StatusNotFound, "api.scope.errors.chat_not_found", "id", handoffFrom)
			return "", false
		}
		if text := s.handoffTranscript(ctx, from); text != "" {
			b.WriteString(i18n.TCtx(ctx, "api.chats.prompts.handoff_header", "agent", from.AgentType, "title", from.Title))
			b.WriteString(text)
			b.WriteString("\n")
		}
		meta[chat.MetaHandoffFrom] = from.ID
	}
	if kept := s.writeChatRefs(r, c, refs, &b); len(kept) > 0 {
		meta[chat.MetaRefs] = strings.Join(kept, ",")
	}
	if b.Len() == 0 {
		return "", true
	}
	return i18n.TCtx(ctx, "api.chats.prompts.context_start") + b.String() + i18n.TCtx(ctx, "api.chats.prompts.context_end"), true
}

// writeChatRefs writes the tasks a message names that the caller can see and that belong to the
// chat's workspace, and returns their ids. Unknown and foreign ids are dropped without a word.
func (s *Server) writeChatRefs(r *http.Request, c *chat.Chat, refs []string, b *strings.Builder) []string {
	ctx := r.Context()
	wid, unrestricted := s.callerWorkspace(r)
	var kept []string
	for _, id := range refs {
		id = strings.TrimSpace(id)
		if id == "" || len(kept) == maxChatRefs || containsStr(kept, id) {
			continue
		}
		t, err := s.taskStore.GetRow(ctx, id)
		if err != nil || t == nil {
			continue
		}
		if !workspaceVisible(t.WorkspaceID, wid, unrestricted) || (t.WorkspaceID != "" && t.WorkspaceID != c.WorkspaceID) {
			continue
		}
		if len(kept) == 0 {
			b.WriteString(i18n.TCtx(ctx, "api.chats.prompts.refs_header"))
		}
		kept = append(kept, t.ID)
		b.WriteString(i18n.TCtx(ctx, "api.chats.prompts.ref_line", "id", t.ID, "title", t.Title,
			"stage", s.stageName(ctx, t), "status", string(t.Status)))
		if body := strings.TrimSpace(t.Body); body != "" {
			b.WriteString(indentBlock(truncRunes(body, maxRefBodyRunes)))
		}
	}
	return kept
}

// visibleChat is chatForCaller without the error response.
func (s *Server) visibleChat(r *http.Request, id string) (*chat.Chat, bool) {
	ctx := r.Context()
	c, err := s.chatSvc.Store().GetChat(ctx, id)
	if err != nil {
		return nil, false
	}
	wid, unrestricted := s.callerWorkspace(r)
	if !workspaceVisible(c.WorkspaceID, wid, unrestricted) || !c.VisibleTo(chatViewerOf(ctx)) {
		return nil, false
	}
	if scope := auth.APIKeyRunScope(ctx); scope.IsRun() && scope.ChatID != c.ID {
		return nil, false
	}
	return c, true
}

// handoffTranscript is the latest of another chat, newest kept, as the agent reads it.
func (s *Server) handoffTranscript(ctx context.Context, c *chat.Chat) string {
	msgs, err := s.chatSvc.Store().ListMessages(ctx, c.ID, handoffMessages, "")
	if err != nil {
		return ""
	}
	loc := i18n.FromContext(ctx)
	var parts []string
	size := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i].InLocale(loc)
		if m.Metadata[chat.MetaError] == "true" || strings.TrimSpace(m.Text) == "" {
			continue
		}
		who := m.Author
		if m.Role == chat.RoleAssistant {
			who = c.AgentType
		}
		line := who + ": " + truncRunes(strings.TrimSpace(m.Text), handoffTextRunes) + "\n"
		if size+len(line) > maxHandoffBytes {
			break
		}
		size += len(line)
		parts = append(parts, line)
	}
	var b strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		b.WriteString(parts[i])
	}
	return b.String()
}

func indentBlock(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		b.WriteString("  ")
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Server) handleCancelChatTurn(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if c.Delegated(time.Now()) {
		if a, err := auth.AuthorFromContext(r.Context()); err != nil || a.Name != c.DelegatedBy {
			writeErrKey(w, r, http.StatusForbidden, "api.chats.errors.delegated_write_only", "person", c.DelegatedBy)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"canceled": s.chatSvc.CancelTurn(c.ID)})
}

func (s *Server) handleUnqueueChatMessage(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if !s.chatSvc.Unqueue(r.Context(), c.ID, r.PathValue("queueId")) {
		writeErrKey(w, r, http.StatusNotFound, "api.chats.errors.queued_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": true, "queue": s.chatSvc.Queue(c.ID)})
}

// handleChatTurnTranscript returns what the engine printed during one turn, one JSON line each, in
// the format of a run's logs.
func (s *Server) handleChatTurnTranscript(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	lines, err := s.chatSvc.Runtime().Transcript(c.ID, r.PathValue("turnId"))
	if errors.Is(err, fs.ErrNotExist) {
		writeErrKey(w, r, http.StatusNotFound, "api.chats.errors.no_transcript")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if lines == nil {
		lines = []string{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// chatTurnSource follows one turn of a chat the caller may read: live while it runs, from its
// transcript after.
func (s *Server) chatTurnSource(ctx context.Context, topic string, sc eventhub.Scope) (*eventhub.Source, error) {
	rest := strings.TrimPrefix(topic, topicChatTurnPfx)
	chatID, turnID, ok := strings.Cut(rest, ":")
	if !ok || chatID == "" || turnID == "" || s.chatSvc == nil {
		return nil, eventhub.ErrUnknownTopic
	}
	c, err := s.chatSvc.Store().GetChat(ctx, chatID)
	// Same error for a chat the caller may not read, so subscribing cannot reveal which exist.
	if err != nil || !workspaceVisible(c.WorkspaceID, sc.WorkspaceID, sc.Unrestricted) || !c.VisibleTo(chatViewerOf(ctx)) {
		return nil, eventhub.ErrUnknownTopic
	}
	if scope := auth.APIKeyRunScope(ctx); scope.IsRun() && scope.ChatID != c.ID {
		return nil, eventhub.ErrUnknownTopic
	}
	rt := s.chatSvc.Runtime()
	return &eventhub.Source{
		Key:         topic,
		Replay:      logsReplayMax,
		ReplayBytes: logsReplayCap,
		Run: func(ctx context.Context, emit eventhub.Emit) {
			rt.FollowTurn(ctx, chatID, turnID, func(line string) bool {
				emit("line", line)
				return ctx.Err() == nil
			})
		},
	}, nil
}
