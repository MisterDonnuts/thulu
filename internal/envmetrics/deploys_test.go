package envmetrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRecentDeploysSendsTheAuthorizedTarget(t *testing.T) {
	var got deploysRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/deploys" {
			t.Errorf("hit %s, want /deploys", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"environment_id": got.EnvironmentID,
			"deploys":        []any{map[string]any{"at": "2026-11-18T10:00:00Z", "service": "api", "revision": "api-00042"}},
		})
	}))
	t.Cleanup(srv.Close)

	c := NewCollector(nil, fixedInterval(300), srv.URL)
	c.SetTargets(fakeTargets{})
	read, err := c.RecentDeploys(context.Background(), "env1", 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnvironmentID != "env1" || got.Environment.ID != "env1" || got.Credential.Payload["access_key_id"] != "AKIA" ||
		got.WindowHours != 48 {
		t.Fatalf("request = %+v", got)
	}
	if len(read.Deploys) != 1 || read.Deploys[0].Revision != "api-00042" {
		t.Fatalf("read = %+v", read)
	}
}

func TestRecentDeploysReportsTheCloudError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "permission denied on run.services.list"})
	}))
	t.Cleanup(srv.Close)
	c := NewCollector(nil, fixedInterval(300), srv.URL)
	c.SetTargets(fakeTargets{})
	if _, err := c.RecentDeploys(context.Background(), "env1", time.Hour); err == nil {
		t.Fatal("a 502 read as deploys")
	}
	if _, err := NewCollector(nil, fixedInterval(300), "").RecentDeploys(context.Background(), "env1", time.Hour); err == nil {
		t.Fatal("no cloud URL read as deploys")
	}
}
