package eventhub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"runtime/debug"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

var (
	ErrUnknownTopic  error = apperr.Invalid("eventhub.errors.unknown_topic")
	ErrUnknownStream error = apperr.NotFound("eventhub.errors.unknown_stream")
	ErrForbidden     error = apperr.Forbidden("eventhub.errors.forbidden")
	ErrTooManyTopics error = apperr.RateLimited("eventhub.errors.too_many_topics")
	ErrTooManyStream error = apperr.RateLimited("eventhub.errors.too_many_streams")
)

const (
	// Overflow drops frames and reports a lag frame, so a slow browser never blocks a turn
	// goroutine or a log tail.
	maxQueue = 16384

	defaultMaxStreams = 1024
	defaultMaxTopics  = 64
	// Per-owner cap: without it one user can exhaust the global stream cap and lock every other
	// tenant out of live updates.
	defaultMaxPerOwner = 32
)

type Frame struct {
	Topic string          `json:"topic"`
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data,omitempty"`
}

const (
	TypeLag       = "lag"
	TypeTruncated = "truncated"
	TypeEnd       = "end"
	TypeError     = "error"
)

type Scope struct {
	WorkspaceID  string
	Unrestricted bool
}

func (s Scope) Key() string {
	if s.Unrestricted {
		return "*"
	}
	return s.WorkspaceID
}

type Emit func(typ string, payload any)

type Source struct {
	// Must fold in anything that changes frame content (e.g. the caller's scope), or one tenant's
	// stream is served another's data.
	Key string

	Replay int

	ReplayBytes int

	// Runs on its own goroutine. Lossy upstreams (buses) must attach in the Resolver and let Run
	// drain the buffer, or frames published before Run's first receive are lost.
	Run func(ctx context.Context, emit Emit)

	// Called exactly once: when the source stops, or immediately if an equivalent source is already
	// running. Otherwise each subscriber leaks an upstream subscription.
	Close func()
}

type Resolver func(ctx context.Context, topic string, sc Scope) (*Source, error)

type pending struct {
	topic string
	src   *Source
}

type Hub struct {
	ctx     context.Context
	resolve Resolver

	maxStreams  int
	maxTopics   int
	maxPerOwner int

	mu      sync.Mutex
	streams map[string]*Stream
	sources map[string]*source
}

type source struct {
	key    string
	topic  string
	cancel context.CancelFunc
	subs   map[*Stream]struct{}

	replay      int
	replayBytes int
	buf         []Frame
	bufBytes    int
	truncated   bool
	ended       bool
}

type Stream struct {
	ID    string
	Scope Scope
	Owner string

	hub *Hub
	sig chan struct{}

	// Guarded by hub.mu.
	topics  map[string]*source
	queue   []Frame
	dropped int
	closed  bool
}

func New(ctx context.Context, resolve Resolver) *Hub {
	return &Hub{
		ctx:         ctx,
		resolve:     resolve,
		maxStreams:  defaultMaxStreams,
		maxTopics:   defaultMaxTopics,
		maxPerOwner: defaultMaxPerOwner,
		streams:     make(map[string]*Stream),
		sources:     make(map[string]*source),
	}
}

func (h *Hub) Open(sc Scope, owner string) (*Stream, error) {
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	st := &Stream{
		ID:     id,
		Scope:  sc,
		Owner:  owner,
		hub:    h,
		sig:    make(chan struct{}, 1),
		topics: make(map[string]*source),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.streams) >= h.maxStreams {
		return nil, ErrTooManyStream
	}
	mine := 0
	for _, other := range h.streams {
		if other.Owner == owner {
			mine++
		}
	}
	if mine >= h.maxPerOwner {
		return nil, ErrTooManyStream
	}
	h.streams[id] = st
	return st, nil
}

func (h *Hub) Get(id, owner string) (*Stream, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.streams[id]
	if !ok || st.closed {
		return nil, ErrUnknownStream
	}
	if st.Owner != owner {
		// Same answer as a missing stream so a caller cannot probe for someone else's stream.
		return nil, ErrUnknownStream
	}
	return st, nil
}

func (st *Stream) Signal() <-chan struct{} { return st.sig }

func (st *Stream) Drain() (frames []Frame, dropped int) {
	st.hub.mu.Lock()
	defer st.hub.mu.Unlock()
	frames, dropped = st.queue, st.dropped
	st.queue, st.dropped = nil, 0
	return frames, dropped
}

func (st *Stream) Topics() []string {
	st.hub.mu.Lock()
	defer st.hub.mu.Unlock()
	out := make([]string, 0, len(st.topics))
	for t := range st.topics {
		out = append(out, t)
	}
	return out
}

func (st *Stream) Subscribe(ctx context.Context, topics []string) error {
	h := st.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if st.closed {
		return ErrUnknownStream
	}

	var todo []pending
	for _, t := range topics {
		if t == "" || st.topics[t] != nil {
			continue
		}
		src, err := h.resolve(ctx, t, st.Scope)
		if err != nil {
			releaseAll(todo)
			return err
		}
		todo = append(todo, pending{topic: t, src: src})
	}
	if len(st.topics)+len(todo) > h.maxTopics {
		releaseAll(todo)
		return ErrTooManyTopics
	}

	for _, p := range todo {
		s := h.sources[p.src.Key]
		if s == nil {
			s = h.startSource(p.topic, p.src)
		} else if p.src.Close != nil {
			p.src.Close()
		}
		s.subs[st] = struct{}{}
		st.topics[p.topic] = s
		st.replayLocked(s)
	}
	return nil
}

// Caller holds h.mu.
func (h *Hub) startSource(topic string, spec *Source) *source {
	ctx, cancel := context.WithCancel(h.ctx)
	s := &source{
		key:         spec.Key,
		topic:       topic,
		cancel:      cancel,
		subs:        make(map[*Stream]struct{}),
		replay:      spec.Replay,
		replayBytes: spec.ReplayBytes,
	}
	h.sources[spec.Key] = s

	emit := func(typ string, payload any) { h.publish(s, typ, payload) }
	go func() {
		defer cancel()
		if spec.Close != nil {
			defer spec.Close()
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[eventhub] %s", i18n.Log("eventhub.log.source_panicked", "source", s.key, "panic", r, "stack", string(debug.Stack())))
					h.publish(s, TypeError, i18n.Text("eventhub.errors.stream_read_failed"))
				}
			}()
			spec.Run(ctx, emit)
		}()
		if ctx.Err() != nil {
			return
		}
		h.mu.Lock()
		s.ended = true
		h.mu.Unlock()
		h.publish(s, TypeEnd, nil)
	}()
	return s
}

// Caller holds h.mu, so no live frame can interleave with the replay.
func (st *Stream) replayLocked(s *source) {
	if s.truncated {
		st.pushLocked(Frame{Topic: s.topic, Type: TypeTruncated})
	}
	for _, f := range s.buf {
		st.pushLocked(f)
	}
	if s.ended {
		st.pushLocked(Frame{Topic: s.topic, Type: TypeEnd})
	}
}

func (st *Stream) Unsubscribe(topics []string) {
	h := st.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range topics {
		h.detachLocked(st, t)
	}
}

func (st *Stream) Close() {
	h := st.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if st.closed {
		return
	}
	st.closed = true
	for t := range st.topics {
		h.detachLocked(st, t)
	}
	st.queue = nil
	delete(h.streams, st.ID)
}

func (h *Hub) detachLocked(st *Stream, topic string) {
	s := st.topics[topic]
	if s == nil {
		return
	}
	delete(st.topics, topic)
	delete(s.subs, st)
	if len(s.subs) > 0 {
		return
	}
	// A later subscriber restarts the source from scratch, so sources must be replayable (log tails
	// re-read from the beginning).
	delete(h.sources, s.key)
	s.cancel()
	s.buf, s.bufBytes = nil, 0
}

func (h *Hub) publish(s *source, typ string, payload any) {
	var data json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			logx.Warn(err, "eventhub.log.marshal_frame", "type", typ, "topic", s.topic)
			return
		}
		data = b
	}
	f := Frame{Topic: s.topic, Type: typ, Data: data}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sources[s.key] != s {
		return
	}
	s.retainLocked(f)
	for st := range s.subs {
		st.pushLocked(f)
	}
}

func (s *source) retainLocked(f Frame) {
	if s.replay <= 0 {
		return
	}
	// TypeEnd is rebuilt from s.ended at replay time; buffering it would send a late subscriber
	// two.
	if f.Type == TypeEnd {
		return
	}
	s.buf = append(s.buf, f)
	s.bufBytes += len(f.Data)
	for len(s.buf) > s.replay || (s.replayBytes > 0 && s.bufBytes > s.replayBytes && len(s.buf) > 1) {
		s.bufBytes -= len(s.buf[0].Data)
		s.buf = s.buf[1:]
		s.truncated = true
	}
}

func (st *Stream) pushLocked(f Frame) {
	if st.closed {
		return
	}
	if len(st.queue) >= maxQueue {
		st.dropped++
		return
	}
	st.queue = append(st.queue, f)
	select {
	case st.sig <- struct{}{}:
	default:
	}
}

type Stats struct {
	Streams       int `json:"streams"`
	Sources       int `json:"sources"`
	Subscriptions int `json:"subscriptions"`
}

func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := Stats{Streams: len(h.streams), Sources: len(h.sources)}
	for _, s := range h.sources {
		st.Subscriptions += len(s.subs)
	}
	return st
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func releaseAll(specs []pending) {
	for _, p := range specs {
		if p.src.Close != nil {
			p.src.Close()
		}
	}
}
