package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const MCPProtocolVersion = "2024-11-05"

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ownerID is the parent for a subtask, so subtasks share one container; callers must key the
// manager off ownerID, not taskID.
type WorkspaceResolver interface {
	ResolveWorkspace(ctx context.Context, taskID string) (ownerID, workspacePath, workspaceID string, projectsPaths map[string]string, err error)
}

type WorkspaceResolverFunc func(ctx context.Context, taskID string) (string, string, string, map[string]string, error)

func (f WorkspaceResolverFunc) ResolveWorkspace(ctx context.Context, taskID string) (string, string, string, map[string]string, error) {
	return f(ctx, taskID)
}

type AttachFunc func(ctx context.Context, taskID, filename, mimeType string, data []byte) (any, error)

// exec runs arbitrary commands, so the task a session drives can't be trusted from the query
// string.
// Principal pins every later POST to the caller that opened the session, so a leaked session id is
// useless to others.
type SessionAuthorizer interface {
	Authorize(r *http.Request, taskID string) (principal string, status int, err error)
	Principal(r *http.Request) string
}

type MCPHandler struct {
	mgr         *Manager
	resolver    WorkspaceResolver
	attach      AttachFunc
	authz       SessionAuthorizer
	initScripts InitScripts
	previews    PreviewRecorder

	mu       sync.Mutex
	sessions map[string]*mcpSession
}

func (h *MCPHandler) WithAttacher(fn AttachFunc) *MCPHandler {
	h.attach = fn
	return h
}

// Without an authorizer any caller may open a session on any task; only acceptable in tests.
func (h *MCPHandler) WithAuthorizer(a SessionAuthorizer) *MCPHandler {
	h.authz = a
	return h
}

type PreviewRecorder func(ctx context.Context, taskID string, info PreviewInfo)

func (h *MCPHandler) WithPreviewRecorder(fn PreviewRecorder) *MCPHandler {
	h.previews = fn
	return h
}

type mcpSession struct {
	id        string
	taskID    string
	stage     string
	previewed map[string]bool
	principal string
	writer    http.ResponseWriter
	flusher   http.Flusher
	// dispatch uses this, not the POST's request context, which is canceled right after the 202 ack
	// and would abort exec work.
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
}

func NewMCPHandler(mgr *Manager, resolver WorkspaceResolver) *MCPHandler {
	if resolver == nil {
		resolver = WorkspaceResolverFunc(func(_ context.Context, taskID string) (string, string, string, map[string]string, error) {
			return taskID, "", "", nil, nil
		})
	}
	return &MCPHandler{
		mgr:      mgr,
		resolver: resolver,
		sessions: map[string]*mcpSession{},
	}
}

func (h *MCPHandler) HandleSSE(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		apperr.WriteJSON(w, r, 0, apperr.Invalid("sandbox.errors.task_id_param_required"))
		return
	}
	stage := r.URL.Query().Get("stage")
	if stage == "" {
		stage = "in_progress"
	}

	principal := ""
	if h.authz != nil {
		p, status, err := h.authz.Authorize(r, taskID)
		if err != nil {
			apperr.WriteJSON(w, r, status, err)
			return
		}
		principal = p
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		apperr.WriteJSON(w, r, 0, apperr.Internal("sandbox.errors.streaming_unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sessionID := newSessionID()
	ctx, cancel := context.WithCancel(r.Context())
	sess := &mcpSession{
		id:        sessionID,
		taskID:    taskID,
		stage:     stage,
		principal: principal,
		writer:    w,
		flusher:   flusher,
		ctx:       ctx,
		cancel:    cancel,
	}
	h.mu.Lock()
	h.sessions[sessionID] = sess
	h.mu.Unlock()

	endpoint := fmt.Sprintf("/sandbox/messages?sessionId=%s", sessionID) //i18n:ignore URL path
	if err := sess.writeEvent("endpoint", endpoint); err != nil {
		log.Printf("[mcp-sandbox] %s", i18n.Log("sandbox.log.sse_endpoint_write_failed", "err", err))
		h.cleanupSession(sessionID)
		return
	}

	<-r.Context().Done()
	h.cleanupSession(sessionID)
}

func (h *MCPHandler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		apperr.WriteJSON(w, r, 0, apperr.Invalid("sandbox.errors.session_id_param_required"))
		return
	}
	h.mu.Lock()
	sess, ok := h.sessions[sessionID]
	h.mu.Unlock()
	// A principal mismatch must look exactly like a missing session, so a POST cannot probe for
	// live ids.
	if ok && h.authz != nil && h.authz.Principal(r) != sess.principal {
		ok = false
	}
	if !ok {
		apperr.WriteJSON(w, r, 0, apperr.NotFound("sandbox.errors.session_not_found"))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		apperr.WriteJSON(w, r, 0, apperr.WrapKind(apperr.KindInvalid, err, "sandbox.errors.read_body"))
		return
	}
	var req jsonrpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		apperr.WriteJSON(w, r, 0, apperr.WrapKind(apperr.KindInvalid, err, "sandbox.errors.json_parse"))
		return
	}

	w.WriteHeader(http.StatusAccepted)

	go h.dispatch(sess.ctx, sess, &req) //nolint:contextcheck // intentional: sess.ctx outlives this POST handler
}

func (h *MCPHandler) dispatch(ctx context.Context, sess *mcpSession, req *jsonrpcRequest) {
	resp := jsonrpcResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": MCPProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "thulu-sandbox",
				"version": "0.2.0",
			},
		}

	// MCP spec 2024-11-05 spells it "cancelled"; accept "canceled" too.
	case "notifications/initialized", "notifications/cancelled", "notifications/canceled":
		return

	case "tools/list":
		resp.Result = map[string]any{"tools": mcpToolDefs(h.appPorts())}

	case "tools/call":
		resp.Result = h.handleToolCall(ctx, sess, req.Params)

	case "ping":
		resp.Result = map[string]any{}

	default:
		resp.Error = &jsonrpcError{
			Code:    -32601,
			Message: i18n.Text("sandbox.tools.method_not_found", "method", req.Method),
		}
	}

	if err := sess.writeJSONRPC(&resp); err != nil {
		log.Printf("[mcp-sandbox] %s", i18n.Log("sandbox.log.write_response_failed", "err", err))
	}
}

func (h *MCPHandler) handleToolCall(ctx context.Context, sess *mcpSession, raw json.RawMessage) any {
	var args toolCallArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.invalid_call_params", "error", apperr.Text(err)))
	}
	switch args.Name {
	case "exec":
		return h.toolExec(ctx, sess, args.Arguments)
	case "preview_url":
		return h.toolPreviewURL(ctx, sess, args.Arguments)
	case "attach_file":
		return h.toolAttachFile(ctx, sess, args.Arguments)
	case "init_status":
		return h.toolInitStatus(ctx, sess, args.Arguments)
	case "init_run":
		return h.toolInitRun(ctx, sess, args.Arguments)
	case "init_script_get":
		return h.toolInitScriptGet(ctx, sess, args.Arguments)
	case "init_script_update":
		return h.toolInitScriptUpdate(ctx, sess, args.Arguments)
	default:
		return toolErrorResult(i18n.Text("sandbox.tools.unknown_tool", "name", args.Name))
	}
}

func (h *MCPHandler) cleanupSession(sessionID string) {
	h.mu.Lock()
	sess, ok := h.sessions[sessionID]
	delete(h.sessions, sessionID)
	h.mu.Unlock()
	if ok {
		sess.close()
	}
}

func (s *mcpSession) writeJSONRPC(resp *jsonrpcResponse) error {
	body, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return s.writeEvent("message", string(body))
}

func (s *mcpSession) writeEvent(event, data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return apperr.New("sandbox.errors.session_closed")
	}
	//i18n:ignore SSE wire format
	if _, err := fmt.Fprintf(s.writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *mcpSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
