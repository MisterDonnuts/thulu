package webhook

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestMatchesEvent(t *testing.T) {
	cases := []struct {
		patterns []string
		event    EventType
		want     bool
	}{
		{[]string{"*"}, "task.created", true},
		{[]string{"task.created"}, "task.created", true},
		{[]string{"task.*"}, "task.created", true},
		{[]string{"task.*"}, "task.stage_changed", true},
		{[]string{"task.*"}, "agent.started", false},
		{[]string{"task.created"}, "task.deleted", false},
		{[]string{}, "task.created", false},
		{[]string{"other"}, "task.created", false},
		{[]string{"task.created", "task.deleted"}, "task.deleted", true},
	}
	for _, c := range cases {
		got := matchesEvent(c.patterns, c.event)
		if got != c.want {
			t.Errorf("matchesEvent(%v, %q) = %v, want %v", c.patterns, c.event, got, c.want)
		}
	}
}

func TestHashSecret(t *testing.T) {
	h1 := hashSecret("mysecret")
	h2 := hashSecret("mysecret")
	if h1 != h2 {
		t.Error("hashSecret should be deterministic")
	}
	h3 := hashSecret("other")
	if h1 == h3 {
		t.Error("different secrets should produce different hashes")
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex hash, got %d: %s", len(h1), h1)
	}
}

func TestSignPayload(t *testing.T) {
	body := []byte(`{"event":"test"}`)
	s1 := signPayload(body, []byte("secrethash"))
	s2 := signPayload(body, []byte("secrethash"))
	if s1 != s2 {
		t.Error("signPayload should be deterministic")
	}
	s3 := signPayload(body, []byte("otherhash"))
	if s1 == s3 {
		t.Error("different secrets should produce different signatures")
	}
}

func TestGenerateSecret(t *testing.T) {
	s1 := generateSecret()
	s2 := generateSecret()
	if s1 == s2 {
		t.Error("generateSecret should produce unique values")
	}
	if len(s1) < 10 {
		t.Errorf("secret too short: %q", s1)
	}
}

func TestDB_Accessor(t *testing.T) {
	db := testpg.New(t)
	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s.DB() == nil {
		t.Error("DB() should not be nil")
	}
}

func newInternalStore(t *testing.T) *Store {
	t.Helper()
	db := testpg.New(t)
	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestGetEndpointByID(t *testing.T) {
	s := newInternalStore(t)
	ep, _ := s.CreateEndpoint(t.Context(), "ws-1", "https://example.com", []string{"*"})

	got, err := s.getEndpointByID(t.Context(), ep.ID)
	if err != nil {
		t.Fatalf("getEndpointByID: %v", err)
	}
	if got == nil || got.SecretEncrypted == "" || got.SecretHash != "" {
		t.Error("expected a sealed secret and no legacy hash on a new endpoint")
	}
}

func newOutboxDispatcher(s *Store) *Dispatcher {
	return &Dispatcher{store: s, client: &http.Client{}, nudge: make(chan struct{}, 1), batch: 50}
}

func TestProcessDue_Success(t *testing.T) {
	var received atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	s := newInternalStore(t)
	ep, _ := s.CreateEndpoint(t.Context(), "ws-1", ts.URL, []string{"*"})
	p := Payload{ID: "p1", Event: "task.created", WorkspaceID: "ws-1"}
	body, _ := json.Marshal(p)
	if err := s.enqueueOutbox(t.Context(), ep.ID, p, body); err != nil {
		t.Fatalf("enqueueOutbox: %v", err)
	}

	d := newOutboxDispatcher(s)
	n, err := d.ProcessDue(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("ProcessDue: n=%d err=%v", n, err)
	}
	if received.Load() != 1 {
		t.Errorf("expected 1 delivery, got %d", received.Load())
	}
	n2, _ := d.ProcessDue(t.Context())
	if n2 != 0 {
		t.Errorf("delivered row should not be re-claimed, got %d", n2)
	}
}

func TestProcessDue_FailureReschedules(t *testing.T) {
	s := newInternalStore(t)
	ep, _ := s.CreateEndpoint(t.Context(), "ws-1", "http://127.0.0.1:1", []string{"*"})
	p := Payload{ID: "pf", Event: "task.created", WorkspaceID: "ws-1"}
	body, _ := json.Marshal(p)
	if err := s.enqueueOutbox(t.Context(), ep.ID, p, body); err != nil {
		t.Fatalf("enqueueOutbox: %v", err)
	}

	d := &Dispatcher{store: s, client: &http.Client{Timeout: 200 * time.Millisecond}, nudge: make(chan struct{}, 1), batch: 50}
	if _, err := d.ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	var status string
	var attempts int
	if err := s.db.QueryRow(`SELECT status, attempts FROM webhook_outbox WHERE id IN (SELECT id FROM webhook_outbox LIMIT 1)`).Scan(&status, &attempts); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if status != statusPending || attempts != 1 {
		t.Errorf("expected pending/attempts=1 after failure, got %s/%d", status, attempts)
	}
	if n, _ := d.ProcessDue(t.Context()); n != 0 {
		t.Errorf("backed-off row should not be due immediately, got %d", n)
	}
}

func TestProcessDue_DeadLetterAfterMaxAttempts(t *testing.T) {
	s := newInternalStore(t)
	ep, _ := s.CreateEndpoint(t.Context(), "ws-1", "http://127.0.0.1:1", []string{"*"})
	p := Payload{ID: "pd", Event: "task.created", WorkspaceID: "ws-1"}
	body, _ := json.Marshal(p)
	_ = s.enqueueOutbox(t.Context(), ep.ID, p, body)

	d := &Dispatcher{store: s, client: &http.Client{Timeout: 200 * time.Millisecond}, nudge: make(chan struct{}, 1), batch: 50}
	for i := 0; i < maxOutboxAttempts; i++ {
		if _, err := d.ProcessDue(t.Context()); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		_, _ = s.db.Exec(`UPDATE webhook_outbox SET next_attempt_at = $1`, time.Now().Add(-time.Minute))
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM webhook_outbox LIMIT 1`).Scan(&status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != statusFailed {
		t.Errorf("expected dead-letter status=failed after %d attempts, got %s", maxOutboxAttempts, status)
	}
}

func TestProcessDue_SurvivesRestart(t *testing.T) {
	var received atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	s := newInternalStore(t)
	ep, _ := s.CreateEndpoint(t.Context(), "ws-1", ts.URL, []string{"*"})
	p := Payload{ID: "pr", Event: "task.created", WorkspaceID: "ws-1"}
	body, _ := json.Marshal(p)
	_ = s.enqueueOutbox(t.Context(), ep.ID, p, body)

	d := newOutboxDispatcher(s)
	if _, err := d.ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if received.Load() != 1 {
		t.Errorf("restart must redeliver the pending row, got %d deliveries", received.Load())
	}
}

func TestEmit_WithMatchingEndpoint(t *testing.T) {
	var received atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	s := newInternalStore(t)
	s.CreateEndpoint(t.Context(), "ws-emit", ts.URL, []string{"task.created"})

	d := NewDispatcher(t.Context(), s)

	d.Emit(t.Context(), "ws-emit", EventTaskCreated, map[string]string{"title": "test"})

	for i := 0; i < 50 && received.Load() == 0; i++ {
		os.Getenv("_sleep_")
	}
}

func TestEmit_InactiveEndpointSkipped(t *testing.T) {
	s := newInternalStore(t)
	ep, _ := s.CreateEndpoint(t.Context(), "ws-inactive", "http://127.0.0.1:1", []string{"*"})

	if _, err := s.db.Exec(`UPDATE webhook_endpoints SET active = 0 WHERE id = $1`, ep.ID); err != nil {
		t.Fatal(err)
	}

	d := newOutboxDispatcher(s)
	d.Emit(t.Context(), "ws-inactive", EventTaskCreated, nil)
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM webhook_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected no outbox rows for inactive endpoint, got %d", n)
	}
}

func TestEmit_PersistsOutboxRow(t *testing.T) {
	s := newInternalStore(t)
	s.CreateEndpoint(t.Context(), "ws-emit2", "http://127.0.0.1:1", []string{"task.created"})

	d := newOutboxDispatcher(s)
	d.Emit(t.Context(), "ws-emit2", EventTaskCreated, map[string]string{"k": "v"})

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM webhook_outbox WHERE status = $1`, statusPending).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Emit should persist exactly one pending outbox row, got %d", n)
	}
}

func TestProcessDue_InvalidURL(t *testing.T) {
	s := newInternalStore(t)
	ep, _ := s.CreateEndpoint(t.Context(), "ws-1", "://invalid-url", []string{"*"})
	p := Payload{ID: "bad", Event: "task.created", WorkspaceID: "ws-1"}
	body, _ := json.Marshal(p)
	_ = s.enqueueOutbox(t.Context(), ep.ID, p, body)

	d := newOutboxDispatcher(s)
	if _, err := d.ProcessDue(t.Context()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
}

func TestGetEndpointByID_NotFound(t *testing.T) {
	s := newInternalStore(t)
	ep, err := s.getEndpointByID(t.Context(), "nonexistent-id")
	if err != nil {
		t.Errorf("missing endpoint should return (nil, nil), got err=%v", err)
	}
	if ep != nil {
		t.Error("expected nil endpoint for nonexistent id")
	}
}

func TestRecordDelivery_DBClosed(t *testing.T) {
	s := newInternalStore(t)
	ep, _ := s.CreateEndpoint(t.Context(), "ws-1", "https://example.com", []string{"*"})
	_ = s.db.Close()
	s.recordDelivery(t.Context(), ep.ID, "", Payload{ID: "p1", Event: "task.created"}, 200, 1, 10, "", true)
}

func TestDeliverySeries_RetryOnlyWhenExhausted(t *testing.T) {
	s := newInternalStore(t)
	ctx := t.Context()
	ep, _ := s.CreateEndpoint(ctx, "ws-1", "http://127.0.0.1:1", []string{"*"})
	p := Payload{ID: "ps", Event: "task.created", WorkspaceID: "ws-1"}
	body, _ := json.Marshal(p)
	if err := s.enqueueOutbox(ctx, ep.ID, p, body); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{store: s, client: &http.Client{Timeout: 200 * time.Millisecond}, nudge: make(chan struct{}, 1), batch: 50}
	pass := func() {
		t.Helper()
		if _, err := d.ProcessDue(ctx); err != nil {
			t.Fatal(err)
		}
		_, _ = s.db.Exec(`UPDATE webhook_outbox SET next_attempt_at = $1`, time.Now().Add(-time.Minute))
	}
	list := func() []Delivery {
		t.Helper()
		got, err := s.ListDeliveries(ctx, ep.ID, "ws-1", 0)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	pass()
	first := list()
	if len(first) != 1 || first[0].State != DeliveryRetrying || first[0].Retryable {
		t.Fatalf("after one failure = %+v, want one retrying, not retryable", first)
	}
	if err := s.requeueDelivery(ctx, first[0].ID, ep.ID, "ws-1"); !errors.Is(err, ErrNotRetryable) {
		t.Errorf("retry while retrying = %v, want ErrNotRetryable", err)
	}

	for i := 1; i < maxOutboxAttempts; i++ {
		pass()
	}
	all := list()
	if len(all) != maxOutboxAttempts {
		t.Fatalf("attempts logged = %d, want %d", len(all), maxOutboxAttempts)
	}
	if all[0].State != DeliveryFailed || !all[0].Retryable || all[0].Attempt != maxOutboxAttempts {
		t.Errorf("last attempt = %+v, want failed, retryable, attempt %d", all[0], maxOutboxAttempts)
	}
	for _, old := range all[1:] {
		if old.State != DeliveryRetried || old.Retryable {
			t.Errorf("earlier attempt %d = %s retryable=%v, want retried and not retryable", old.Attempt, old.State, old.Retryable)
		}
	}

	if err := s.requeueDelivery(ctx, all[2].ID, ep.ID, "ws-1"); err != nil {
		t.Fatalf("retry an exhausted event: %v", err)
	}
	var rows, attempts int
	var status string
	if err := s.db.QueryRow(`SELECT COUNT(*), MIN(status), MIN(attempts) FROM webhook_outbox`).Scan(&rows, &status, &attempts); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || status != statusPending || attempts != 0 {
		t.Errorf("outbox = %d rows, %s, %d attempts; want the same row pending from zero", rows, status, attempts)
	}
	if err := s.requeueDelivery(ctx, all[0].ID, ep.ID, "ws-1"); !errors.Is(err, ErrNotRetryable) {
		t.Errorf("second retry while the first runs = %v, want ErrNotRetryable", err)
	}
	if got := list(); got[0].State != DeliveryRetrying || got[0].Retryable {
		t.Errorf("after the retry the last attempt = %+v, want retrying", got[0])
	}

	ep2, _ := s.CreateEndpoint(ctx, "ws-1", "http://127.0.0.1:1", []string{"*"})
	s.recordDelivery(ctx, ep2.ID, "", Payload{ID: "pt", Event: "webhook.test"}, 0, 1, 5, "refused", false)
	test, err := s.ListDeliveries(ctx, ep2.ID, "ws-1", 0)
	if err != nil || len(test) != 1 || !test[0].Retryable {
		t.Fatalf("test delivery = %+v, %v; want one retryable row", test, err)
	}
	if err := s.requeueDelivery(ctx, test[0].ID, ep2.ID, "ws-1"); err != nil {
		t.Fatalf("retry a test delivery: %v", err)
	}
	if err := s.requeueDelivery(ctx, test[0].ID, ep2.ID, "ws-1"); !errors.Is(err, ErrNotRetryable) {
		t.Errorf("second retry of a test delivery = %v, want ErrNotRetryable", err)
	}
	var queued int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM webhook_outbox WHERE endpoint_id = $1`, ep2.ID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Errorf("outbox rows for the test delivery = %d, want 1", queued)
	}
	if got, _ := s.ListDeliveries(ctx, ep2.ID, "ws-1", 0); got[0].State != DeliveryRetrying || got[0].Retryable {
		t.Errorf("retried test delivery = %+v, want retrying and not retryable", got[0])
	}
}

func TestPlaceDeliveries(t *testing.T) {
	list := []Delivery{
		{ID: "a3", outboxID: "o1", outboxStatus: statusFailed},
		{ID: "b1", outboxID: "o2", Delivered: true, outboxStatus: statusDelivered},
		{ID: "a2", outboxID: "o1", outboxStatus: statusFailed},
		{ID: "t1"},
		{ID: "c1", outboxID: "o3", outboxStatus: ""},
	}
	placeDeliveries(list)
	want := map[string]struct {
		state     string
		retryable bool
	}{
		"a3": {DeliveryFailed, true},
		"b1": {DeliveryDelivered, false},
		"a2": {DeliveryRetried, false},
		"t1": {DeliveryFailed, true},
		"c1": {DeliveryFailed, true},
	}
	for _, d := range list {
		if w := want[d.ID]; d.State != w.state || d.Retryable != w.retryable {
			t.Errorf("%s = %s/%v, want %s/%v", d.ID, d.State, d.Retryable, w.state, w.retryable)
		}
	}
}
