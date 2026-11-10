package eventhub

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testSource struct {
	mu      sync.Mutex
	runs    map[string]int
	started chan string
	stopped chan string
	emits   map[string]Emit
}

func newTestSources() *testSource {
	return &testSource{
		runs:    map[string]int{},
		started: make(chan string, 64),
		stopped: make(chan string, 64),
		emits:   map[string]Emit{},
	}
}

func (ts *testSource) resolver(_ context.Context, topic string, sc Scope) (*Source, error) {
	switch topic {
	case "chat":
		return &Source{Key: "chat|" + sc.Key(), Run: ts.run("chat|" + sc.Key())}, nil
	case "bad":
		return nil, ErrUnknownTopic
	default:
		if len(topic) > 5 && topic[:5] == "logs:" {
			return &Source{Key: topic, Replay: 3, Run: ts.run(topic)}, nil
		}
		return nil, ErrUnknownTopic
	}
}

func (ts *testSource) run(key string) func(context.Context, Emit) {
	return func(ctx context.Context, emit Emit) {
		ts.mu.Lock()
		ts.runs[key]++
		ts.emits[key] = emit
		ts.mu.Unlock()
		ts.started <- key
		<-ctx.Done()
		ts.stopped <- key
	}
}

func (ts *testSource) emit(t *testing.T, key, typ string, payload any) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		ts.mu.Lock()
		e := ts.emits[key]
		ts.mu.Unlock()
		if e != nil {
			e(typ, payload)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("source %s never started", key)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (ts *testSource) runCount(key string) int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.runs[key]
}

func waitFrames(t *testing.T, st *Stream, n int) []Frame {
	t.Helper()
	var got []Frame
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < n && time.Now().Before(deadline) {
		select {
		case <-st.Signal():
			f, _ := st.Drain()
			got = append(got, f...)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if len(got) < n {
		t.Fatalf("wanted %d frames, got %d: %+v", n, len(got), got)
	}
	return got
}

func TestOpenAndOwnerCheck(t *testing.T) {
	h := New(t.Context(), newTestSources().resolver)
	st, err := h.Open(Scope{WorkspaceID: "w1"}, "user:alice")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.Get(st.ID, "user:alice"); err != nil {
		t.Fatalf("owner should resolve its own stream: %v", err)
	}
	if _, err := h.Get(st.ID, "user:mallory"); !errors.Is(err, ErrUnknownStream) {
		t.Fatalf("foreign owner got %v, want ErrUnknownStream", err)
	}
	if _, err := h.Get("nope", "user:alice"); !errors.Is(err, ErrUnknownStream) {
		t.Fatalf("unknown id got %v, want ErrUnknownStream", err)
	}

	st.Close()
	if _, err := h.Get(st.ID, "user:alice"); !errors.Is(err, ErrUnknownStream) {
		t.Fatalf("closed stream still resolvable: %v", err)
	}
}

func TestSharedSourceRunsOnce(t *testing.T) {
	ts := newTestSources()
	h := New(t.Context(), ts.resolver)

	a, _ := h.Open(Scope{WorkspaceID: "w1"}, "user:a")
	b, _ := h.Open(Scope{WorkspaceID: "w1"}, "user:b")
	for _, st := range []*Stream{a, b} {
		if err := st.Subscribe(t.Context(), []string{"logs:run1"}); err != nil {
			t.Fatal(err)
		}
	}
	<-ts.started
	if got := ts.runCount("logs:run1"); got != 1 {
		t.Fatalf("source started %d times, want 1", got)
	}

	ts.emit(t, "logs:run1", "line", "hello")
	for _, st := range []*Stream{a, b} {
		got := waitFrames(t, st, 1)
		if got[0].Topic != "logs:run1" || got[0].Type != "line" || string(got[0].Data) != `"hello"` {
			t.Fatalf("unexpected frame %+v", got[0])
		}
	}

	a.Unsubscribe([]string{"logs:run1"})
	select {
	case k := <-ts.stopped:
		t.Fatalf("source %s stopped while %d subscriber(s) remained", k, 1)
	case <-time.After(100 * time.Millisecond):
	}
	b.Close()
	select {
	case <-ts.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("source not stopped after last subscriber left")
	}
	a.Close()
	if s := h.Stats(); s.Sources != 0 || s.Streams != 0 {
		t.Fatalf("hub not drained: %+v", s)
	}
}

func TestScopedSourcesAreSeparate(t *testing.T) {
	ts := newTestSources()
	h := New(t.Context(), ts.resolver)

	a, _ := h.Open(Scope{WorkspaceID: "w1"}, "user:a")
	b, _ := h.Open(Scope{WorkspaceID: "w2"}, "user:b")
	if err := a.Subscribe(t.Context(), []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe(t.Context(), []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	<-ts.started
	<-ts.started
	if got := h.Stats().Sources; got != 2 {
		t.Fatalf("got %d sources for two workspaces, want 2", got)
	}

	ts.emit(t, "chat|w1", "message_added", map[string]string{"chat_id": "c1"})
	got := waitFrames(t, a, 1)
	if got[0].Type != "message_added" {
		t.Fatalf("unexpected frame %+v", got[0])
	}
	select {
	case <-b.Signal():
		f, _ := b.Drain()
		t.Fatalf("other workspace received %+v", f)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestReplayForLateSubscriber(t *testing.T) {
	ts := newTestSources()
	h := New(t.Context(), ts.resolver)

	first, _ := h.Open(Scope{Unrestricted: true}, "user:a")
	if err := first.Subscribe(t.Context(), []string{"logs:run2"}); err != nil {
		t.Fatal(err)
	}
	<-ts.started
	ts.emit(t, "logs:run2", "line", "one")
	ts.emit(t, "logs:run2", "line", "two")
	waitFrames(t, first, 2)

	late, _ := h.Open(Scope{Unrestricted: true}, "user:b")
	if err := late.Subscribe(t.Context(), []string{"logs:run2"}); err != nil {
		t.Fatal(err)
	}
	got := waitFrames(t, late, 2)
	if string(got[0].Data) != `"one"` || string(got[1].Data) != `"two"` {
		t.Fatalf("replay out of order or incomplete: %+v", got)
	}
}

func TestReplayTruncationIsAnnounced(t *testing.T) {
	ts := newTestSources()
	h := New(t.Context(), ts.resolver)

	first, _ := h.Open(Scope{Unrestricted: true}, "user:a")
	if err := first.Subscribe(t.Context(), []string{"logs:run3"}); err != nil {
		t.Fatal(err)
	}
	<-ts.started
	for i := 0; i < 5; i++ {
		ts.emit(t, "logs:run3", "line", strconv.Itoa(i))
	}
	waitFrames(t, first, 5)

	late, _ := h.Open(Scope{Unrestricted: true}, "user:b")
	if err := late.Subscribe(t.Context(), []string{"logs:run3"}); err != nil {
		t.Fatal(err)
	}
	got := waitFrames(t, late, 4)
	if got[0].Type != TypeTruncated {
		t.Fatalf("first replay frame %+v, want %s", got[0], TypeTruncated)
	}
	if string(got[1].Data) != `"2"` {
		t.Fatalf("replay kept the wrong window: %+v", got[1:])
	}
}

func TestNaturalEndEmitsEndFrame(t *testing.T) {
	done := make(chan struct{})
	h := New(t.Context(), func(_ context.Context, topic string, _ Scope) (*Source, error) {
		return &Source{Key: topic, Replay: 10, Run: func(_ context.Context, emit Emit) {
			emit("line", "only")
			<-done
		}}, nil
	})
	st, _ := h.Open(Scope{Unrestricted: true}, "user:a")
	if err := st.Subscribe(t.Context(), []string{"logs:short"}); err != nil {
		t.Fatal(err)
	}
	waitFrames(t, st, 1)
	close(done)

	got := waitFrames(t, st, 1)
	if got[0].Type != TypeEnd {
		t.Fatalf("got %+v, want %s", got[0], TypeEnd)
	}
	late, _ := h.Open(Scope{Unrestricted: true}, "user:b")
	if err := late.Subscribe(t.Context(), []string{"logs:short"}); err != nil {
		t.Fatal(err)
	}
	replay := waitFrames(t, late, 2)
	if replay[len(replay)-1].Type != TypeEnd {
		t.Fatalf("late subscriber never told the source ended: %+v", replay)
	}
	// Replay reconstructs the end frame, so buffering it too would deliver it twice.
	ends := 0
	for _, f := range replay {
		if f.Type == TypeEnd {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("late subscriber got %d end frames, want 1: %+v", ends, replay)
	}
}

func TestSubscribeIsAllOrNothing(t *testing.T) {
	ts := newTestSources()
	h := New(t.Context(), ts.resolver)
	st, _ := h.Open(Scope{Unrestricted: true}, "user:a")

	if err := st.Subscribe(t.Context(), []string{"logs:ok", "bad"}); !errors.Is(err, ErrUnknownTopic) {
		t.Fatalf("got %v, want ErrUnknownTopic", err)
	}
	if got := st.Topics(); len(got) != 0 {
		t.Fatalf("partial batch applied: %v", got)
	}
	if got := h.Stats().Sources; got != 0 {
		t.Fatalf("%d source(s) started for a rejected batch", got)
	}
}

func TestSubscribeIsIdempotent(t *testing.T) {
	ts := newTestSources()
	h := New(t.Context(), ts.resolver)
	st, _ := h.Open(Scope{Unrestricted: true}, "user:a")

	for i := 0; i < 3; i++ {
		if err := st.Subscribe(t.Context(), []string{"logs:run", "chat"}); err != nil {
			t.Fatal(err)
		}
	}
	<-ts.started
	<-ts.started
	if got := st.Topics(); len(got) != 2 {
		t.Fatalf("topics %v, want 2", got)
	}
	if got := ts.runCount("logs:run"); got != 1 {
		t.Fatalf("source restarted %d times on resubscribe", got)
	}
}

func TestTopicLimit(t *testing.T) {
	ts := newTestSources()
	h := New(t.Context(), ts.resolver)
	h.maxTopics = 2
	st, _ := h.Open(Scope{Unrestricted: true}, "user:a")

	if err := st.Subscribe(t.Context(), []string{"logs:a", "logs:b", "logs:c"}); !errors.Is(err, ErrTooManyTopics) {
		t.Fatalf("got %v, want ErrTooManyTopics", err)
	}
	if got := st.Topics(); len(got) != 0 {
		t.Fatalf("over-limit batch partially applied: %v", got)
	}
}

func TestStreamLimit(t *testing.T) {
	h := New(t.Context(), newTestSources().resolver)
	h.maxStreams = 1
	if _, err := h.Open(Scope{}, "user:a"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Open(Scope{}, "user:b"); !errors.Is(err, ErrTooManyStream) {
		t.Fatalf("got %v, want ErrTooManyStream", err)
	}
}

func TestSlowStreamDropsInsteadOfBlocking(t *testing.T) {
	ts := newTestSources()
	h := New(t.Context(), ts.resolver)
	st, _ := h.Open(Scope{Unrestricted: true}, "user:a")
	if err := st.Subscribe(t.Context(), []string{"logs:flood"}); err != nil {
		t.Fatal(err)
	}
	<-ts.started

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < maxQueue+500; i++ {
			ts.emit(t, "logs:flood", "line", "x")
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("producer blocked on a stream nobody drains")
	}

	frames, dropped := st.Drain()
	if len(frames) != maxQueue {
		t.Fatalf("queued %d frames, want the %d cap", len(frames), maxQueue)
	}
	if dropped != 500 {
		t.Fatalf("dropped %d, want 500", dropped)
	}
	if _, again := st.Drain(); again != 0 {
		t.Fatalf("drop counter not reset: %d", again)
	}
}

func TestHubShutdownStopsSources(t *testing.T) {
	ts := newTestSources()
	ctx, cancel := context.WithCancel(context.Background())
	h := New(ctx, ts.resolver)
	st, _ := h.Open(Scope{Unrestricted: true}, "user:a")
	if err := st.Subscribe(ctx, []string{"logs:x"}); err != nil {
		t.Fatal(err)
	}
	<-ts.started

	cancel()
	select {
	case <-ts.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("source survived hub shutdown")
	}
}

func TestConcurrentUse(t *testing.T) {
	var emitted atomic.Int64
	h := New(t.Context(), func(_ context.Context, topic string, _ Scope) (*Source, error) {
		return &Source{Key: topic, Replay: 8, Run: func(ctx context.Context, emit Emit) {
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				emit("line", "tick")
				emitted.Add(1)
				time.Sleep(time.Millisecond)
			}
		}}, nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, err := h.Open(Scope{WorkspaceID: "w" + strconv.Itoa(i%2)}, "user:"+strconv.Itoa(i))
			if err != nil {
				t.Error(err)
				return
			}
			defer st.Close()
			for j := 0; j < 20; j++ {
				topic := "logs:" + strconv.Itoa(j%3)
				if err := st.Subscribe(t.Context(), []string{topic}); err != nil {
					t.Error(err)
					return
				}
				select {
				case <-st.Signal():
					st.Drain()
				case <-time.After(5 * time.Millisecond):
				}
				st.Unsubscribe([]string{topic})
			}
		}(i)
	}
	wg.Wait()

	if s := h.Stats(); s.Streams != 0 || s.Sources != 0 {
		t.Fatalf("hub leaked state: %+v", s)
	}
}

// Bus-backed sources attach at resolve time; Subscribe must return only once attached,
// or events published right after it are lost (failed only on loaded CI).
func TestSubscribeAttachesBeforeItReturns(t *testing.T) {
	attached := make(chan struct{}, 8)
	released := make(chan string, 8)
	h := New(t.Context(), func(_ context.Context, topic string, _ Scope) (*Source, error) {
		ch := make(chan string, 8)
		attached <- struct{}{}
		return &Source{
			Key:   topic,
			Close: func() { released <- topic },
			Run: func(ctx context.Context, emit Emit) {
				for {
					select {
					case <-ctx.Done():
						return
					case v := <-ch:
						emit("line", v)
					}
				}
			},
		}, nil
	})

	st, _ := h.Open(Scope{Unrestricted: true}, "user:a")
	if err := st.Subscribe(t.Context(), []string{"logs:x"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-attached:
	default:
		t.Fatal("Subscribe returned before the source attached upstream")
	}
}

func TestDiscardedSpecIsReleased(t *testing.T) {
	released := make(chan string, 8)
	h := New(t.Context(), func(_ context.Context, topic string, _ Scope) (*Source, error) {
		if topic == "bad" {
			return nil, ErrUnknownTopic
		}
		return &Source{
			Key:   topic,
			Close: func() { released <- topic },
			Run:   func(ctx context.Context, _ Emit) { <-ctx.Done() },
		}, nil
	})

	a, _ := h.Open(Scope{Unrestricted: true}, "user:a")
	b, _ := h.Open(Scope{Unrestricted: true}, "user:b")
	if err := a.Subscribe(t.Context(), []string{"logs:shared"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe(t.Context(), []string{"logs:shared"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-released:
		if got != "logs:shared" {
			t.Fatalf("released %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the second subscriber's redundant attachment was never released")
	}

	if err := a.Subscribe(t.Context(), []string{"logs:other", "bad"}); !errors.Is(err, ErrUnknownTopic) {
		t.Fatalf("got %v, want ErrUnknownTopic", err)
	}
	select {
	case got := <-released:
		if got != "logs:other" {
			t.Fatalf("released %q, want logs:other", got)
		}
	case <-time.After(time.Second):
		t.Fatal("a rejected batch leaked the attachment of the topic that did resolve")
	}

	a.Close()
	b.Close()
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("stopping the source never released its attachment")
	}
}
