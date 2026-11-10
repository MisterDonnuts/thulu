package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/eventhub"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/metrics"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Frames are SSE `msg` events with {"topic","type","data"}; `lag` is its own event name.
// The first event is `hello` (not `open`, which EventSource already dispatches) carrying the stream
// id.

const (
	topicChat    = "chat"
	topicTasks   = "tasks"
	topicLogsPfx = "logs:"
	typeWaiting  = "waiting"

	logsReplayMax = 20000
	logsReplayCap = 8 * 1024 * 1024
	maxTopicsPost = 64
)

func (s *Server) withHubStats(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.hub != nil {
			st := s.hub.Stats()
			metrics.RecordEventHub(st.Streams, st.Sources, st.Subscriptions)
		}
		next.ServeHTTP(w, r)
	}
}

func (s *Server) hubScope(r *http.Request) eventhub.Scope {
	wid, unrestricted := s.callerWorkspace(r)
	return eventhub.Scope{WorkspaceID: wid, Unrestricted: unrestricted}
}

// Each API key must be its own owner: keying by run scope made every workspace key, in every
// tenant, one owner.
func hubOwner(r *http.Request) string {
	return callerPrincipal(r)
}

func (s *Server) resolveTopic(ctx context.Context, topic string, sc eventhub.Scope) (*eventhub.Source, error) {
	switch {
	case topic == topicChat:
		if s.chatBus == nil {
			return nil, eventhub.ErrUnknownTopic
		}
		// The chat feed carries every conversation in the workspace; a run may only reach its own
		// chat, so it gets none.
		if auth.APIKeyRunScope(ctx).IsRun() {
			return nil, eventhub.ErrUnknownTopic
		}
		// The key must include scope, reader and locale: two tenants or two members must never
		// share a source, since each one's list holds only their own chats, and frames are
		// localized per reader.
		loc := i18n.FromContext(ctx)
		viewer := chatViewerOf(ctx)
		key := strings.Join([]string{topicChat, sc.Key(), viewer.Owner, viewer.Author, loc}, "|")
		return busSource(key, s.chatBus.Subscribe, sc,
			func(ev chat.Event) (string, string, any, bool) {
				ev.Message = ev.Message.InLocale(loc)
				return ev.WorkspaceID, string(ev.Type), ev, ev.Listed(viewer)
			}), nil

	case topic == topicTasks:
		if s.taskChanges == nil {
			return nil, eventhub.ErrUnknownTopic
		}
		return busSource(topicTasks+"|"+sc.Key(), s.taskChanges.Subscribe, sc,
			func(ch task.Change) (string, string, any, bool) {
				return ch.WorkspaceID, string(ch.Kind), ch, true
			}), nil

	case strings.HasPrefix(topic, topicLogsPfx):
		id := strings.TrimPrefix(topic, topicLogsPfx)
		if id == "" {
			return nil, eventhub.ErrUnknownTopic
		}
		// Same error as an unknown run id, so subscribing cannot reveal which runs exist.
		if !workspaceVisible(s.runWorkspace(ctx, id), sc.WorkspaceID, sc.Unrestricted) {
			return nil, eventhub.ErrUnknownTopic
		}
		if scope := auth.APIKeyRunScope(ctx); scope.IsRun() && !s.runMayReadRun(ctx, scope, id) {
			return nil, eventhub.ErrUnknownTopic
		}
		// Not scoped by caller on purpose: access is checked above and every viewer of a run shares
		// one upstream tail.
		return &eventhub.Source{
			Key:         topicLogsPfx + id,
			Replay:      logsReplayMax,
			ReplayBytes: logsReplayCap,
			Run:         s.runLogSource(id),
		}, nil
	}
	return nil, eventhub.ErrUnknownTopic
}

// Subscribe here, not inside Run: the bus drops undeliverable events, so a late subscribe loses
// events under load.
func busSource[T any](
	key string,
	subscribe func() (<-chan T, func()),
	sc eventhub.Scope,
	project func(T) (workspaceID, frameType string, payload any, keep bool),
) *eventhub.Source {
	events, cancel := subscribe()
	return &eventhub.Source{
		Key:   key,
		Close: cancel,
		Run: func(ctx context.Context, emit eventhub.Emit) {
			for {
				select {
				case <-ctx.Done():
					return
				case ev := <-events:
					wid, typ, payload, keep := project(ev)
					if !keep || !workspaceVisible(wid, sc.WorkspaceID, sc.Unrestricted) {
						continue
					}
					emit(typ, payload)
				}
			}
		},
	}
}

// Resolve inside the goroutine so a run without logs yet is reported on its topic instead of
// failing the subscribe batch.
func (s *Server) runLogSource(id string) func(context.Context, eventhub.Emit) {
	return func(ctx context.Context, emit eventhub.Emit) {
		src, err := s.waitForSessionLogs(ctx, id, func() {
			emit(typeWaiting, i18n.TCtx(ctx, "api.events.hints.waiting_for_logs"))
		})
		if err != nil {
			emit(eventhub.TypeError, apperr.LocalizeCtx(ctx, err))
			return
		}
		src.Pump(ctx, func(line string) bool {
			if ctx.Err() != nil {
				return false
			}
			emit("line", line)
			return true
		})
	}
}

func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	stream, err := s.hub.Open(s.hubScope(r), hubOwner(r))
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, err)
		return
	}
	defer stream.Close()

	var initErr string
	if raw := r.URL.Query().Get("topics"); raw != "" {
		if serr := stream.Subscribe(r.Context(), splitTopics(raw)); serr != nil {
			initErr = apperr.LocalizeCtx(r.Context(), serr)
		}
	}

	conn := newSSEConn(w, r)
	if conn == nil {
		return
	}
	hello := map[string]any{"stream_id": stream.ID}
	if initErr != "" {
		hello["error"] = initErr
	}
	payload, err := json.Marshal(hello)
	if err != nil || !conn.event("hello", payload) {
		return
	}

	ctx := r.Context()
	stop := conn.heartbeat(ctx)
	defer stop()
	stillAllowed := s.streamCredentialCheck(r)
	recheck := time.NewTicker(s.streamRecheckEvery())
	defer recheck.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-recheck.C:
			if !stillAllowed(ctx) {
				return
			}
		case <-stream.Signal():
			if !s.drainStream(conn, stream) {
				return
			}
		}
	}
}

func (s *Server) drainStream(conn *sseConn, stream *eventhub.Stream) bool {
	frames, dropped := stream.Drain()
	batch := make([]sseFrame, 0, len(frames)+1)
	if dropped > 0 {
		if b, err := json.Marshal(map[string]int{"dropped": dropped}); err == nil {
			batch = append(batch, sseFrame{Type: eventhub.TypeLag, Data: b})
		}
	}
	for _, f := range frames {
		b, err := json.Marshal(f)
		if err != nil {
			continue
		}
		batch = append(batch, sseFrame{Type: "msg", Data: b})
	}
	return conn.events(batch)
}

type topicsRequest struct {
	Topics []string `json:"topics"`
}

func (s *Server) handleEventsSubscribe(w http.ResponseWriter, r *http.Request) {
	stream, topics, ok := s.streamAndTopics(w, r)
	if !ok {
		return
	}
	if err := stream.Subscribe(r.Context(), topics); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, eventhub.ErrTooManyTopics) {
			status = http.StatusTooManyRequests
		}
		writeError(w, r, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"topics": stream.Topics()})
}

func (s *Server) handleEventsUnsubscribe(w http.ResponseWriter, r *http.Request) {
	stream, topics, ok := s.streamAndTopics(w, r)
	if !ok {
		return
	}
	stream.Unsubscribe(topics)
	writeJSON(w, http.StatusOK, map[string]any{"topics": stream.Topics()})
}

func (s *Server) streamAndTopics(w http.ResponseWriter, r *http.Request) (*eventhub.Stream, []string, bool) {
	stream, err := s.hub.Get(r.PathValue("id"), hubOwner(r))
	if err != nil {
		writeErrKey(w, r, http.StatusNotFound, "api.events.errors.stream_not_found")
		return nil, nil, false
	}
	var req topicsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.events.errors.invalid_body")
		return nil, nil, false
	}
	if len(req.Topics) > maxTopicsPost {
		writeErrKey(w, r, http.StatusBadRequest, "api.events.errors.too_many_topics")
		return nil, nil, false
	}
	return stream, req.Topics, true
}

func splitTopics(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
