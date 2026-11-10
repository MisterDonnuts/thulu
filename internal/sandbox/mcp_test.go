package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

type mcpRig struct {
	srv      *httptest.Server
	mgr      *Manager
	attaches *attachCapture
}

type attachCapture struct {
	mu    sync.Mutex
	items []capturedAttach
}

type capturedAttach struct {
	taskID   string
	filename string
	mimeType string
	data     []byte
}

func (a *attachCapture) fn(_ context.Context, taskID, filename, mimeType string, data []byte) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.items = append(a.items, capturedAttach{taskID, filename, mimeType, append([]byte(nil), data...)})
	return map[string]any{"id": fmt.Sprintf("att-%d", len(a.items)), "filename": filename, "size": len(data)}, nil
}

func (a *attachCapture) last() (capturedAttach, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.items) == 0 {
		return capturedAttach{}, false
	}
	return a.items[len(a.items)-1], true
}

func newMCPRig(t *testing.T) *mcpRig {
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
	_ = sett.SetSandboxMax(t.Context(), 3)
	_ = sett.PutJSON(t.Context(), settings.SandboxAcquireBlockMsKey, 200)

	dock := NewDocker(cli, DefaultRuntimeImage)
	mgr := NewManager(st, sett, dock, ManagerConfig{PublicHost: "localhost"})

	attaches := &attachCapture{}
	h := NewMCPHandler(mgr, nil).WithAttacher(attaches.fn)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sandbox/sse", h.HandleSSE)
	mux.HandleFunc("POST /sandbox/messages", h.HandleMessages)

	srv := httptest.NewServer(mux)

	t.Cleanup(func() {
		all, _ := st.ListAll(t.Context())
		for _, sb := range all {
			if sb.ContainerID != "" {
				_ = dock.Remove(context.Background(), sb.ContainerID)
			}
		}
		srv.Close()
	})

	return &mcpRig{srv: srv, mgr: mgr, attaches: attaches}
}

type mcpClient struct {
	srv         *httptest.Server
	taskID      string
	sessionPath string
	resp        *http.Response

	mu       sync.Mutex
	nextID   int
	pending  map[string]chan *jsonrpcResponse
	closed   bool
	endpoint chan string
}

func newMCPClient(t *testing.T, srv *httptest.Server, taskID, stage string) *mcpClient {
	t.Helper()
	url := srv.URL + "/sandbox/sse?task_id=" + taskID
	if stage != "" {
		url += "&stage=" + stage
	}
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("SSE handshake: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("SSE handshake status=%d body=%s", resp.StatusCode, body)
	}

	c := &mcpClient{
		srv:      srv,
		taskID:   taskID,
		resp:     resp,
		pending:  map[string]chan *jsonrpcResponse{},
		endpoint: make(chan string, 1),
	}

	go c.readLoop(t, resp.Body)

	select {
	case ep := <-c.endpoint:
		c.sessionPath = ep
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for endpoint event")
	}

	t.Cleanup(c.close)
	return c
}

func (c *mcpClient) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	if c.resp != nil {
		_ = c.resp.Body.Close()
	}
}

func (c *mcpClient) readLoop(t *testing.T, body io.Reader) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1<<20), 1<<22)
	var ev, data string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			c.handleEvent(ev, data)
			ev, data = "", ""
			continue
		}
		switch {
		case strings.HasPrefix(line, "event: "):
			ev = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
}

func (c *mcpClient) handleEvent(ev, data string) {
	switch ev {
	case "endpoint":
		select {
		case c.endpoint <- data:
		default:
		}
	case "message":
		var resp jsonrpcResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			return
		}
		idStr := string(resp.ID)
		c.mu.Lock()
		ch, ok := c.pending[idStr]
		if ok {
			delete(c.pending, idStr)
		}
		c.mu.Unlock()
		if ok {
			ch <- &resp
		}
	}
}

func (c *mcpClient) call(t *testing.T, method string, params any) *jsonrpcResponse {
	t.Helper()
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	idJSON, _ := json.Marshal(id)
	ch := make(chan *jsonrpcResponse, 1)
	c.pending[string(idJSON)] = ch
	c.mu.Unlock()

	paramsJSON, _ := json.Marshal(params)
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      idJSON,
		Method:  method,
		Params:  paramsJSON,
	}
	body, _ := json.Marshal(req)
	resp, err := c.srv.Client().Post(
		c.srv.URL+c.sessionPath,
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST %s status=%d", method, resp.StatusCode)
	}

	select {
	case got := <-ch:
		return got
	case <-time.After(30 * time.Second):
		t.Fatalf("timeout waiting for %s response", method)
		return nil
	}
}

func (c *mcpClient) callTool(t *testing.T, name string, args any) (texts []string, isError bool) {
	t.Helper()
	resp := c.call(t, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if resp.Error != nil {
		t.Fatalf("tools/call %s rpc error: %s", name, resp.Error.Message)
	}
	res, _ := resp.Result.(map[string]any)
	if v, ok := res["isError"].(bool); ok {
		isError = v
	}
	for _, c := range res["content"].([]any) {
		blk := c.(map[string]any)
		if blk["type"] == "text" {
			texts = append(texts, blk["text"].(string))
		}
	}
	return
}

func TestMCPSSEHandshakeEmitsEndpoint(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-1", "in_progress")
	if !strings.HasPrefix(c.sessionPath, "/sandbox/messages?sessionId=") {
		t.Errorf("sessionPath: %q", c.sessionPath)
	}
}

func TestMCPSSEMissingTaskIDReturns400(t *testing.T) {
	rig := newMCPRig(t)
	resp, err := rig.srv.Client().Get(rig.srv.URL + "/sandbox/sse")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestMCPInitializeReturnsProtocolVersion(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-init", "in_progress")
	resp := c.call(t, "initialize", map[string]any{})
	if resp.Error != nil {
		t.Fatalf("initialize error: %v", resp.Error)
	}
	res := resp.Result.(map[string]any)
	if res["protocolVersion"] != MCPProtocolVersion {
		t.Errorf("protocolVersion: %v", res["protocolVersion"])
	}
	server := res["serverInfo"].(map[string]any)
	if server["name"] != "thulu-sandbox" {
		t.Errorf("serverInfo.name: %v", server["name"])
	}
}

func TestMCPToolsListReturnsExecAndPreview(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-list", "in_progress")
	resp := c.call(t, "tools/list", map[string]any{})
	res := resp.Result.(map[string]any)
	tools := res["tools"].([]any)
	names := []string{}
	for _, ti := range tools {
		names = append(names, ti.(map[string]any)["name"].(string))
	}
	if !contains(names, "exec") || !contains(names, "preview_url") {
		t.Errorf("tools: %v", names)
	}
}

func TestMCPExecHappyPath(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-exec", "in_progress")
	defer rig.mgr.Release(context.Background(), "task-mcp-exec")

	texts, isError := c.callTool(t, "exec", map[string]any{
		"command": "echo from-mcp",
	})
	if isError {
		t.Errorf("isError=true: %v", texts)
	}
	if len(texts) == 0 {
		t.Fatal("no content")
	}
	var result ExecResult
	if err := json.Unmarshal([]byte(texts[0]), &result); err != nil {
		t.Fatalf("unmarshal: %v (text=%s)", err, texts[0])
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode: %d", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "from-mcp") {
		t.Errorf("Stdout: %q", result.Stdout)
	}
}

func TestMCPAttachFile(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-attach", "in_progress")
	defer rig.mgr.Release(context.Background(), "task-mcp-attach")

	const size = attachChunkBytes*2 + 123
	pattern := "0123456789abcdef"
	var want bytes.Buffer
	for want.Len() < size {
		want.WriteString(pattern)
	}
	wantBytes := want.Bytes()[:size]

	writeCmd := fmt.Sprintf("yes %s | tr -d '\\n' | head -c %d > /tmp/attach-test.bin", pattern, size)
	if _, isErr := c.callTool(t, "exec", map[string]any{"command": writeCmd}); isErr {
		t.Fatal("failed to write test file in sandbox")
	}

	texts, isError := c.callTool(t, "attach_file", map[string]any{
		"path":     "/tmp/attach-test.bin",
		"filename": "evidence.pdf",
	})
	if isError {
		t.Fatalf("attach_file isError=true: %v", texts)
	}

	got, ok := rig.attaches.last()
	if !ok {
		t.Fatal("attacher never received the file")
	}
	if got.taskID != "task-mcp-attach" {
		t.Errorf("taskID: got %q", got.taskID)
	}
	if got.filename != "evidence.pdf" {
		t.Errorf("filename: got %q", got.filename)
	}
	if got.mimeType != "application/pdf" {
		t.Errorf("mimeType: got %q, want application/pdf (inferred from .pdf)", got.mimeType)
	}
	if len(got.data) != size {
		t.Fatalf("size: got %d, want %d", len(got.data), size)
	}
	if !bytes.Equal(got.data, wantBytes) {
		t.Error("attached bytes do not match the file written in the sandbox (chunk reassembly corruption)")
	}
}

func TestMCPAttachFileMissingPath(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-attach-nopath", "in_progress")
	texts, isError := c.callTool(t, "attach_file", map[string]any{})
	if !isError {
		t.Error("expected isError=true on missing path")
	}
	if !strings.Contains(strings.Join(texts, " "), "required") {
		t.Errorf("expected 'required' in error: %v", texts)
	}
}

func TestMCPExecFailingCommandIsError(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-fail", "in_progress")
	defer rig.mgr.Release(context.Background(), "task-mcp-fail")

	texts, isError := c.callTool(t, "exec", map[string]any{
		"command": "exit 7",
	})
	if !isError {
		t.Errorf("expected isError=true (exit=7)")
	}
	var result ExecResult
	_ = json.Unmarshal([]byte(texts[0]), &result)
	if result.ExitCode != 7 {
		t.Errorf("ExitCode: %d", result.ExitCode)
	}
}

func TestMCPExecRequiresCommand(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-nocmd", "in_progress")
	texts, isError := c.callTool(t, "exec", map[string]any{})
	if !isError {
		t.Error("expected isError=true on missing command")
	}
	joined := strings.Join(texts, " ")
	if !strings.Contains(joined, "required") {
		t.Errorf("expected 'required' in error: %s", joined)
	}
}

func TestMCPPreviewURLReturnsValidURL(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-prev", "in_progress")
	defer rig.mgr.Release(context.Background(), "task-mcp-prev")

	_, _ = c.callTool(t, "exec", map[string]any{"command": "true"})

	texts, isError := c.callTool(t, "preview_url", map[string]any{"port": 3000})
	if isError {
		t.Errorf("isError: %v", texts)
	}
	if len(texts) < 1 {
		t.Fatal("no content")
	}
	var body struct {
		URL  string `json:"url"`
		Port int    `json:"port"`
	}
	if err := json.Unmarshal([]byte(texts[0]), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(body.URL, "http://octopus-sb-") || !strings.HasSuffix(body.URL, ":3000") {
		t.Errorf("URL: %s", body.URL)
	}
	if body.Port != 3000 {
		t.Errorf("Port: %d", body.Port)
	}
}

func TestMCPPreviewURLOnUnexposedPortIsError(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-prev-bad", "in_progress")
	defer rig.mgr.Release(context.Background(), "task-mcp-prev-bad")

	_, _ = c.callTool(t, "exec", map[string]any{"command": "true"})

	_, isError := c.callTool(t, "preview_url", map[string]any{"port": 9999})
	if !isError {
		t.Error("expected isError=true for unexposed port")
	}
}

func TestMCPUnknownMethodReturnsRPCError(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-bogus", "in_progress")
	resp := c.call(t, "totally/bogus", map[string]any{})
	if resp.Error == nil {
		t.Error("expected RPC error")
	}
	if resp.Error != nil && resp.Error.Code != -32601 {
		t.Errorf("code: %d", resp.Error.Code)
	}
}

func TestMCPUnknownToolReturnsToolError(t *testing.T) {
	rig := newMCPRig(t)
	c := newMCPClient(t, rig.srv, "task-mcp-utool", "in_progress")
	_, isError := c.callTool(t, "bogus_tool", map[string]any{})
	if !isError {
		t.Error("expected isError=true for unknown tool")
	}
}

func TestMCPSessionScopedToTask(t *testing.T) {
	rig := newMCPRig(t)
	cA := newMCPClient(t, rig.srv, "task-mcp-A", "in_progress")
	cB := newMCPClient(t, rig.srv, "task-mcp-B", "in_progress")
	defer rig.mgr.Release(context.Background(), "task-mcp-A")
	defer rig.mgr.Release(context.Background(), "task-mcp-B")

	_, _ = cA.callTool(t, "exec", map[string]any{
		"command": "echo I-am-A > /workspace/marker.txt",
	})
	_, _ = cB.callTool(t, "exec", map[string]any{
		"command": "echo I-am-B > /workspace/marker.txt",
	})

	textsA, _ := cA.callTool(t, "exec", map[string]any{
		"command": "cat /workspace/marker.txt",
	})
	var rA ExecResult
	_ = json.Unmarshal([]byte(textsA[0]), &rA)
	if !strings.Contains(rA.Stdout, "I-am-A") {
		t.Errorf("session A leaked: %q", rA.Stdout)
	}

	textsB, _ := cB.callTool(t, "exec", map[string]any{
		"command": "cat /workspace/marker.txt",
	})
	var rB ExecResult
	_ = json.Unmarshal([]byte(textsB[0]), &rB)
	if !strings.Contains(rB.Stdout, "I-am-B") {
		t.Errorf("session B leaked: %q", rB.Stdout)
	}
}

func TestMCPMessagesUnknownSessionReturns404(t *testing.T) {
	rig := newMCPRig(t)
	resp, err := rig.srv.Client().Post(
		rig.srv.URL+"/sandbox/messages?sessionId=does-not-exist",
		"application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

var _ = fmt.Sprintf
