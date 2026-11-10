package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"

	"github.com/MisterDonnuts/thulu/internal/testdocker"
)

type e2eRig struct {
	srv *httptest.Server
	mgr *Manager
	st  *Store
	se  *settings.Store

	mu     sync.Mutex
	grants []string
}

func newE2ERig(t *testing.T, max int, blockMs int, idleSecs int) *e2eRig {
	t.Helper()
	cli := testdocker.Client(t)
	requireRuntimeImage(t, cli)

	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatalf("settings.NewStore: %v", err)
	}
	_ = sett.SetSandboxMax(t.Context(), max)
	_ = sett.PutJSON(t.Context(), settings.SandboxAcquireBlockMsKey, blockMs)
	_ = sett.PutJSON(t.Context(), settings.SandboxIdleEvictSecondsKey, idleSecs)

	dock := NewDocker(cli, DefaultRuntimeImage)
	rig := &e2eRig{st: st, se: sett}
	rig.mgr = NewManager(st, sett, dock, ManagerConfig{
		PublicHost: "localhost",
		OnSlotGranted: func(taskID, stage string) {
			rig.mu.Lock()
			rig.grants = append(rig.grants, taskID)
			rig.mu.Unlock()
		},
	})
	h := NewMCPHandler(rig.mgr, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sandbox/sse", h.HandleSSE)
	mux.HandleFunc("POST /sandbox/messages", h.HandleMessages)
	mux.HandleFunc("DELETE /tasks/{id}/sandbox", func(w http.ResponseWriter, r *http.Request) {
		taskID := r.PathValue("id")
		if err := rig.mgr.Release(r.Context(), taskID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /sandboxes", func(w http.ResponseWriter, r *http.Request) {
		rows, _ := st.ListLive(r.Context())
		w.Header().Set("Content-Type", "application/json")
		out := []map[string]any{}
		for _, sb := range rows {
			out = append(out, map[string]any{
				"task_id":      sb.TaskID,
				"container_id": sb.ContainerID,
				"status":       sb.Status,
				"host_ports":   sb.HostPorts,
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("PUT /settings/{key}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = sett.PutRaw(r.Context(), key, string(raw))
		w.WriteHeader(http.StatusNoContent)
	})

	rig.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		rig.srv.Close()
		all, _ := st.ListAll(context.Background())
		for _, sb := range all {
			if sb.ContainerID != "" {
				_ = dock.Remove(context.Background(), sb.ContainerID)
			}
		}
	})
	return rig
}

func (r *e2eRig) recordedGrants() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.grants))
	copy(out, r.grants)
	return out
}

func TestE2EAgentExecCreatesSandboxLazily(t *testing.T) {
	r := newE2ERig(t, 3, 200, 1800)

	resp, _ := r.srv.Client().Get(r.srv.URL + "/sandboxes")
	var before []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&before)
	resp.Body.Close()
	if len(before) != 0 {
		t.Fatalf("expected 0 sandboxes before any call, got %d", len(before))
	}

	c := newMCPClient(t, r.srv, "task-e2e-1", "in_progress")
	defer r.mgr.Release(context.Background(), "task-e2e-1")

	_, isErr := c.callTool(t, "exec", map[string]any{"command": "echo lazy-create"})
	if isErr {
		t.Fatal("exec failed")
	}

	resp, _ = r.srv.Client().Get(r.srv.URL + "/sandboxes")
	var after []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&after)
	resp.Body.Close()
	if len(after) != 1 {
		t.Fatalf("expected 1 sandbox after lazy create, got %d", len(after))
	}
	if after[0]["task_id"] != "task-e2e-1" {
		t.Errorf("task_id mismatch: %v", after[0])
	}
}

func TestE2EDeleteSandboxFreesSlotAndUnblocksQueue(t *testing.T) {
	r := newE2ERig(t, 1, 5000, 1800)

	cA := newMCPClient(t, r.srv, "task-e2e-A", "in_progress")
	_, _ = cA.callTool(t, "exec", map[string]any{"command": "true"})

	cB := newMCPClient(t, r.srv, "task-e2e-B", "handoff")
	type result struct {
		texts   []string
		isError bool
	}
	resCh := make(chan result, 1)
	go func() {
		texts, isErr := cB.callTool(t, "exec", map[string]any{"command": "echo from-B"})
		resCh <- result{texts: texts, isError: isErr}
	}()

	time.Sleep(150 * time.Millisecond)

	req, _ := http.NewRequest("DELETE", r.srv.URL+"/tasks/task-e2e-A/sandbox", nil)
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: status=%d", resp.StatusCode)
	}

	select {
	case got := <-resCh:
		if got.isError {
			t.Errorf("B exec errored: %v", got.texts)
		}
		if len(got.texts) > 0 && !strings.Contains(got.texts[0], "from-B") {
			t.Errorf("B stdout: %v", got.texts)
		}
		_ = r.mgr.Release(context.Background(), "task-e2e-B")
	case <-time.After(15 * time.Second):
		t.Fatal("B never unblocked after A was killed")
	}
}

func TestE2EPriorityChangeViaSettingsAffectsQueueOrdering(t *testing.T) {
	r := newE2ERig(t, 1, 80, 1800)

	cHold := newMCPClient(t, r.srv, "task-hold", "in_progress")
	_, _ = cHold.callTool(t, "exec", map[string]any{"command": "true"})
	defer r.mgr.Release(context.Background(), "task-hold")

	custom := map[string]int{"refinement": 999, "handoff": 1}
	body, _ := json.Marshal(custom)
	req, _ := http.NewRequest("PUT", r.srv.URL+"/settings/sandbox.priorities",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT settings: status=%d", resp.StatusCode)
	}

	cHand := newMCPClient(t, r.srv, "task-hand", "handoff")
	cHand.callTool(t, "exec", map[string]any{"command": "true"})

	cRef := newMCPClient(t, r.srv, "task-ref", "refinement")
	cRef.callTool(t, "exec", map[string]any{"command": "true"})

	if err := r.mgr.Release(context.Background(), "task-hold"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g := r.recordedGrants(); len(g) > 0 {
			if g[0] != "task-ref" {
				t.Errorf("expected task-ref to win with custom priorities, got %v", g)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("no grant fired: %v", r.recordedGrants())
}

func TestE2EDevServerVisibleViaPreviewURL(t *testing.T) {
	r := newE2ERig(t, 3, 200, 1800)
	c := newMCPClient(t, r.srv, "task-preview", "in_progress")
	defer r.mgr.Release(context.Background(), "task-preview")

	docroot := "/tmp/thulu-e2e-preview"
	bootstrap := strings.Join([]string{
		"mkdir -p " + docroot,
		"echo '<h1>thulu-sandbox e2e ok</h1>' > " + docroot + "/index.html",
		"(setsid nohup python3 -m http.server 8000 --directory " + docroot +
			" > /tmp/preview.log 2>&1 < /dev/null &)",
		"sleep 0.5",
		"echo started",
	}, " && ")
	texts, isErr := c.callTool(t, "exec", map[string]any{
		"command":    bootstrap,
		"timeout_ms": 5000,
	})
	if isErr {
		t.Fatalf("bootstrap exec failed: %v", texts)
	}

	urlTexts, isErr := c.callTool(t, "preview_url", map[string]any{"port": 8000})
	if isErr {
		t.Fatalf("preview_url errored: %v", urlTexts)
	}
	var pv struct {
		URL      string `json:"url"`
		HumanURL string `json:"human_url"`
		Port     int    `json:"port"`
	}
	_ = json.Unmarshal([]byte(urlTexts[0]), &pv)
	if !strings.HasPrefix(pv.HumanURL, "http://localhost:") {
		t.Fatalf("preview human_url: %s", pv.HumanURL)
	}
	if pv.URL != "http://octopus-sb-task-preview:8000" {
		t.Fatalf("preview url: %s", pv.URL)
	}

	var status int
	for i := 0; i < 10; i++ {
		client := &http.Client{Timeout: 1 * time.Second}
		resp, err := client.Get(pv.HumanURL)
		if err == nil {
			status = resp.StatusCode
			body := make([]byte, 200)
			n, _ := resp.Body.Read(body)
			resp.Body.Close()
			if status == 200 && strings.Contains(string(body[:n]), "thulu-sandbox e2e ok") {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("preview URL %s never returned 200 with expected body (last status=%d)",
		pv.URL, status)
}

func TestE2EReleaseRespectsActiveContainerOnReboot(t *testing.T) {
	r := newE2ERig(t, 1, 200, 1800)

	c := newMCPClient(t, r.srv, "task-reboot", "in_progress")
	_, _ = c.callTool(t, "exec", map[string]any{"command": "true"})

	sb, err := r.st.Get(t.Context(), "task-reboot")
	if err != nil {
		t.Fatal(err)
	}
	cli := testdocker.Client(t)
	dock := NewDocker(cli, DefaultRuntimeImage)
	if err := dock.Remove(context.Background(), sb.ContainerID); err != nil {
		t.Fatal(err)
	}

	texts, isErr := c.callTool(t, "exec", map[string]any{"command": "echo recovered"})
	if isErr {
		t.Fatalf("recovery exec failed: %v", texts)
	}
	if len(texts) == 0 || !strings.Contains(texts[0], "recovered") {
		t.Errorf("recovery stdout: %v", texts)
	}
	_ = r.mgr.Release(context.Background(), "task-reboot")
}
