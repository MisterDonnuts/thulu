package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type callServer struct {
	mu       sync.Mutex
	sessions map[string]chan string
	seq      atomic.Int32

	lastArgs   map[string]any
	lastAPIKey atomic.Value
	reply      string
	failTool   bool
}

func newCallServer(reply string) *callServer {
	return &callServer{sessions: map[string]chan string{}, reply: reply}
}

func (f *callServer) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", f.serveSSE)
	mux.HandleFunc("/messages", f.serveMessages)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/sse"
}

func (f *callServer) serveSSE(w http.ResponseWriter, r *http.Request) {
	f.lastAPIKey.Store(r.Header.Get("X-API-Key"))
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	id := fmt.Sprintf("s%d", f.seq.Add(1))
	out := make(chan string, 8)
	f.mu.Lock()
	f.sessions[id] = out
	f.mu.Unlock()

	fmt.Fprintf(w, "event: endpoint\ndata: /messages?sessionId=%s\n\n", id)
	flusher.Flush()
	for {
		select {
		case msg := <-out:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (f *callServer) serveMessages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     *int   `json:"id"`
		Method string `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	f.mu.Lock()
	out := f.sessions[r.URL.Query().Get("sessionId")]
	if req.Method == "tools/call" {
		f.lastArgs = req.Params.Arguments
	}
	f.mu.Unlock()
	if out == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	if req.ID == nil {
		return
	}
	switch req.Method {
	case "initialize":
		out <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":%q}}`, *req.ID, ProtocolVersion)
	case "tools/call":
		if f.failTool {
			out <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"isError":true,"content":[{"type":"text","text":"repo not found"}]}}`, *req.ID)
			return
		}
		out <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, *req.ID, f.reply)
	default:
		out <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, *req.ID)
	}
}

func TestCaller_CallsToolAndReturnsResult(t *testing.T) {
	srv := newCallServer(`{"content":[{"type":"text","text":"{\"groups\":[{\"service\":\"api\"}]}"}]}`)
	url := srv.start(t)

	result, err := NewCaller().Call(context.Background(), url, "query_errors",
		map[string]any{"env": "prod", "window": "15m"}, "run-key-123")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("result = %+v", result)
	}
	srv.mu.Lock()
	args := srv.lastArgs
	srv.mu.Unlock()
	if args["env"] != "prod" || args["window"] != "15m" {
		t.Errorf("arguments = %+v", args)
	}
}

func TestCaller_ForwardsTheAPIKey(t *testing.T) {
	srv := newCallServer(`{"content":[]}`)
	url := srv.start(t)

	if _, err := NewCaller().Call(context.Background(), url, "noop", nil, "run-key-abc"); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got, _ := srv.lastAPIKey.Load().(string); got != "run-key-abc" {
		t.Errorf("X-API-Key = %q, want the run key", got)
	}
}

func TestCaller_ToolErrorSurfaces(t *testing.T) {
	srv := newCallServer(`{}`)
	srv.failTool = true
	url := srv.start(t)

	_, err := NewCaller().Call(context.Background(), url, "pr_list", nil, "")
	if err == nil {
		t.Fatal("isError:true came back as success")
	}
	if !strings.Contains(err.Error(), "repo not found") {
		t.Errorf("error = %q, want the server's own message", err)
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("error type = %T, want *ToolError", err)
	}
}

func TestCaller_UnreachableServer(t *testing.T) {
	_, err := NewCaller().Call(context.Background(), "http://127.0.0.1:1/sse", "x", nil, "")
	if err == nil {
		t.Fatal("a dead server answered")
	}
}
