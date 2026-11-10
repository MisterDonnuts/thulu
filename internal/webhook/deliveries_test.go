package webhook_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/webhook"
)

func TestDeliveries_ListAndRetry(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ids = append(ids, r.Header.Get("X-Octopus-Delivery"))
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store, err := webhook.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ep, _ := store.CreateEndpoint(t.Context(), "ws-1", srv.URL+"/hook", []string{"*"})
	disp := webhook.NewDispatcher(t.Context(), store)
	if _, err := disp.DeliverTest(context.Background(), ep.ID, "ws-1", nil); err != nil {
		t.Fatal(err)
	}

	list, err := store.ListDeliveries(t.Context(), ep.ID, "ws-1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].StatusCode != http.StatusBadGateway || list[0].Delivered || list[0].Event != "test" {
		t.Fatalf("deliveries = %+v, want one failed 502", list)
	}
	if other, _ := store.ListDeliveries(t.Context(), ep.ID, "ws-2", 50); len(other) != 0 {
		t.Errorf("another workspace read %d deliveries", len(other))
	}

	if err := disp.Retry(t.Context(), list[0].ID, ep.ID, "ws-2"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("retry from another workspace: %v, want sql.ErrNoRows", err)
	}
	if err := disp.Retry(t.Context(), list[0].ID, ep.ID, "ws-1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := disp.ProcessDue(t.Context()); err != nil {
			t.Fatal(err)
		}
		list, _ = store.ListDeliveries(t.Context(), ep.ID, "ws-1", 50)
		if len(list) >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(list) != 2 || !list[0].Delivered || list[0].StatusCode != http.StatusOK {
		t.Fatalf("after retry: %+v, want a delivered attempt on top", list)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Errorf("delivery ids %v: a retry should carry the same payload id", ids)
	}
}
