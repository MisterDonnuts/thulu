package webhook_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

func tempDB(t *testing.T) *sql.DB {
	t.Helper()
	return testpg.New(t)
}

func TestStore_CreateAndList(t *testing.T) {
	store, err := webhook.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}

	ep, err := store.CreateEndpoint(t.Context(), "ws-1", "https://example.com/hook", []string{"*"})
	if err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	if ep.ID == "" {
		t.Fatal("empty endpoint ID")
	}
	if ep.Secret == "" {
		t.Fatal("expected one-time secret to be set")
	}

	eps, err := store.ListEndpoints(t.Context(), "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(eps))
	}
	if eps[0].URL != "https://example.com/hook" {
		t.Errorf("URL mismatch: %q", eps[0].URL)
	}

	other, _ := store.ListEndpoints(t.Context(), "ws-2")
	if len(other) != 0 {
		t.Errorf("workspace isolation broken: got %d endpoints for ws-2", len(other))
	}
}

func TestStore_DeleteEndpoint(t *testing.T) {
	store, err := webhook.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}

	ep, _ := store.CreateEndpoint(t.Context(), "ws-1", "https://example.com/hook", []string{"*"})
	if err := store.DeleteEndpoint(t.Context(), ep.ID, "ws-1"); err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}

	eps, _ := store.ListEndpoints(t.Context(), "ws-1")
	if len(eps) != 0 {
		t.Errorf("expected 0 endpoints after deletion, got %d", len(eps))
	}

	ep2, _ := store.CreateEndpoint(t.Context(), "ws-1", "https://b.com/", []string{"*"})
	if err := store.DeleteEndpoint(t.Context(), ep2.ID, "ws-WRONG"); err != nil {
		t.Errorf("cross-workspace delete should not error, got: %v", err)
	}
	eps2, _ := store.ListEndpoints(t.Context(), "ws-1")
	if len(eps2) != 1 {
		t.Errorf("ep2 should not have been deleted, got %d endpoints", len(eps2))
	}
}

func TestDispatcher_DeliverTest_Success(t *testing.T) {
	var receivedPayload map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedPayload)
		if r.Header.Get("X-Octopus-Event") != "test" {
			t.Errorf("expected X-Octopus-Event: test, got %q", r.Header.Get("X-Octopus-Event"))
		}
		if r.Header.Get("X-Octopus-Delivery") == "" {
			t.Error("missing X-Octopus-Delivery header")
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
	result, err := disp.DeliverTest(context.Background(), ep.ID, "ws-1", map[string]string{"hello": "world"})
	if err != nil {
		t.Fatalf("DeliverTest: %v", err)
	}
	if result["success"] != true {
		t.Errorf("expected success=true, got %v", result["success"])
	}
	if result["status_code"] != 200 {
		t.Errorf("expected status_code=200, got %v", result["status_code"])
	}
	if receivedPayload["event"] != "test" {
		t.Errorf("expected payload event=test, got %v", receivedPayload["event"])
	}
}

func TestDispatcher_DeliverTest_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store, err := webhook.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}

	ep, _ := store.CreateEndpoint(t.Context(), "ws-1", srv.URL+"/hook", []string{"*"})
	disp := webhook.NewDispatcher(t.Context(), store)

	result, err := disp.DeliverTest(context.Background(), ep.ID, "ws-1", nil)
	if err != nil {
		t.Fatalf("DeliverTest: %v", err)
	}
	if result["success"] != false {
		t.Errorf("expected success=false for 500 response, got %v", result["success"])
	}
	if result["status_code"] != 500 {
		t.Errorf("expected status_code=500, got %v", result["status_code"])
	}
}

func TestDispatcher_DeliverTest_WrongWorkspace(t *testing.T) {
	store, err := webhook.NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}

	ep, _ := store.CreateEndpoint(t.Context(), "ws-1", "https://example.com/hook", []string{"*"})
	disp := webhook.NewDispatcher(t.Context(), store)

	_, err = disp.DeliverTest(context.Background(), ep.ID, "ws-WRONG", nil)
	if err == nil {
		t.Error("expected error when workspace does not own the endpoint")
	}
}

func TestEmitBackground_NilDispatcher(t *testing.T) {
	webhook.EmitBackground(t.Context(), nil, "ws-1", webhook.EventTaskCreated, nil)
}

func TestDispatcher_Emit_NoEndpoints(t *testing.T) {
	store, _ := webhook.NewStore(tempDB(t))
	disp := webhook.NewDispatcher(t.Context(), store)
	disp.Emit(t.Context(), "ws-empty", webhook.EventTaskCreated, nil)
}

func TestDispatcher_DeliverTest_InactiveEndpoint(t *testing.T) {
	store, _ := webhook.NewStore(tempDB(t))

	ep, _ := store.CreateEndpoint(t.Context(), "ws-1", "https://example.com/hook", []string{"*"})
	if _, err := store.DB().Exec(`UPDATE webhook_endpoints SET active = 0 WHERE id = $1`, ep.ID); err != nil {
		t.Fatalf("mark inactive: %v", err)
	}

	disp := webhook.NewDispatcher(t.Context(), store)
	_, err := disp.DeliverTest(context.Background(), ep.ID, "ws-1", nil)
	if err == nil {
		t.Error("expected error for inactive endpoint")
	}
}

func TestEmit_EventMismatch(t *testing.T) {
	store, _ := webhook.NewStore(tempDB(t))

	ep, _ := store.CreateEndpoint(t.Context(), "ws-mismatch", "http://127.0.0.1:1", []string{"task.created"})
	if ep == nil {
		t.Fatal("expected endpoint")
	}

	d := webhook.NewDispatcher(t.Context(), store)
	d.Emit(t.Context(), "ws-mismatch", webhook.EventType("agent.started"), nil)
}

func TestEmitBackground_NonNilDispatcher(t *testing.T) {
	store, _ := webhook.NewStore(tempDB(t))

	disp := webhook.NewDispatcher(t.Context(), store)
	webhook.EmitBackground(t.Context(), disp, "ws-1", webhook.EventTaskCreated, nil)
}

func TestStore_CreateEndpoint_Defaults(t *testing.T) {
	store, _ := webhook.NewStore(tempDB(t))

	ep, err := store.CreateEndpoint(t.Context(), "ws-1", "https://example.com/hook", []string{"task.created", "agent.started"})
	if err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	if !ep.Active {
		t.Error("endpoint should be active by default")
	}
	if len(ep.Events) != 2 {
		t.Errorf("expected 2 events, got %d", len(ep.Events))
	}
	if ep.ID == "" {
		t.Error("endpoint ID should not be empty")
	}
}

func TestStore_CreateEndpoint_DBError(t *testing.T) {
	store, _ := webhook.NewStore(tempDB(t))
	store.DB().Close()
	_, err := store.CreateEndpoint(t.Context(), "ws-1", "https://example.com/hook", []string{"*"})
	if err == nil {
		t.Error("expected error from CreateEndpoint after DB close")
	}
}

func TestDispatcher_DeliverTest_InvalidURL(t *testing.T) {
	store, _ := webhook.NewStore(tempDB(t))

	if _, err := store.DB().Exec(
		`INSERT INTO webhook_endpoints (id, workspace_id, url, secret_hash, events, active, created_at)
		 VALUES ('ep-badurl', 'ws-bad', '://invalid-url', '', '["*"]', 1, NOW())`,
	); err != nil {
		t.Fatalf("insert bad endpoint: %v", err)
	}

	disp := webhook.NewDispatcher(t.Context(), store)
	_, err := disp.DeliverTest(context.Background(), "ep-badurl", "ws-bad", nil)
	if err == nil {
		t.Error("expected error for invalid URL in DeliverTest")
	}
}

func TestDispatcher_GetEndpoint_NotFound(t *testing.T) {
	store, _ := webhook.NewStore(tempDB(t))

	disp := webhook.NewDispatcher(t.Context(), store)
	_, err := disp.DeliverTest(context.Background(), "nonexistent-id", "ws-1", nil)
	if err == nil {
		t.Error("expected error when endpoint not found")
	}
}

func TestStore_DeleteByWorkspace(t *testing.T) {
	db := tempDB(t)
	store, _ := webhook.NewStore(db)
	a, _ := store.CreateEndpoint(t.Context(), "ws-del", "https://a.example/h", []string{"*"})
	b, _ := store.CreateEndpoint(t.Context(), "ws-del", "https://b.example/h", []string{"*"})
	c, _ := store.CreateEndpoint(t.Context(), "ws-keep", "https://c.example/h", []string{"*"})
	for _, id := range []string{a.ID, b.ID, c.ID} {
		seedDeliveryRows(t, db, id)
	}

	if err := store.DeleteByWorkspace(t.Context(), "ws-del"); err != nil {
		t.Fatal(err)
	}
	del, _ := store.ListEndpoints(t.Context(), "ws-del")
	keep, _ := store.ListEndpoints(t.Context(), "ws-keep")
	if len(del) != 0 {
		t.Errorf("ws-del endpoints should be purged, got %d", len(del))
	}
	if len(keep) != 1 {
		t.Errorf("ws-keep endpoints must be untouched, got %d", len(keep))
	}
	for _, tbl := range []string{"webhook_deliveries", "webhook_outbox"} {
		if n := countEndpointRows(t, db, tbl, a.ID) + countEndpointRows(t, db, tbl, b.ID); n != 0 {
			t.Errorf("%s: %d rows of purged endpoints left", tbl, n)
		}
		if n := countEndpointRows(t, db, tbl, c.ID); n != 1 {
			t.Errorf("%s: ws-keep rows = %d, want 1", tbl, n)
		}
	}
}

func TestStore_DeleteEndpointTakesItsRows(t *testing.T) {
	db := tempDB(t)
	store, _ := webhook.NewStore(db)
	gone, _ := store.CreateEndpoint(t.Context(), "ws-1", "https://a.example/h", []string{"*"})
	kept, _ := store.CreateEndpoint(t.Context(), "ws-1", "https://b.example/h", []string{"*"})
	seedDeliveryRows(t, db, gone.ID)
	seedDeliveryRows(t, db, kept.ID)

	if err := store.DeleteEndpoint(t.Context(), gone.ID, "ws-WRONG"); err != nil {
		t.Fatal(err)
	}
	if n := countEndpointRows(t, db, "webhook_deliveries", gone.ID); n != 1 {
		t.Fatalf("cross-workspace delete removed the delivery log (%d left)", n)
	}

	if err := store.DeleteEndpoint(t.Context(), gone.ID, "ws-1"); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"webhook_deliveries", "webhook_outbox"} {
		if n := countEndpointRows(t, db, tbl, gone.ID); n != 0 {
			t.Errorf("%s: %d rows of the deleted endpoint left", tbl, n)
		}
		if n := countEndpointRows(t, db, tbl, kept.ID); n != 1 {
			t.Errorf("%s: sibling endpoint rows = %d, want 1", tbl, n)
		}
	}
}

func seedDeliveryRows(t *testing.T, db *sql.DB, endpointID string) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO webhook_deliveries (id, endpoint_id, event, payload, created_at)
		 VALUES ('d-' || $1, $1, 'task.created', '{}', NOW())`,
		`INSERT INTO webhook_outbox (id, endpoint_id, event, payload, next_attempt_at, created_at, updated_at)
		 VALUES ('o-' || $1, $1, 'task.created', '{}', NOW(), NOW(), NOW())`,
	} {
		if _, err := db.ExecContext(t.Context(), q, endpointID); err != nil {
			t.Fatalf("seed delivery rows: %v", err)
		}
	}
}

func countEndpointRows(t *testing.T, db *sql.DB, table, endpointID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM `+table+` WHERE endpoint_id = $1`, endpointID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
