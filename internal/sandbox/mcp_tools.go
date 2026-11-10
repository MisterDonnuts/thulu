//nolint:lll // MCP tool description strings are inherently long
package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

var errSandboxQueued error = apperr.Unavailable("sandbox.errors.capacity_full")

type toolCallArgs struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func mcpToolDefs(ports []int) []map[string]any {
	return append(baseToolDefs(ports), initToolDefs()...)
}

func baseToolDefs(ports []int) []map[string]any {
	return []map[string]any{
		{
			"name":        "exec",
			"description": i18n.Text("sandbox.tools.exec_description"),
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":    map[string]any{"type": "string", "description": i18n.Text("sandbox.tools.exec_command")},
					"cwd":        map[string]any{"type": "string", "description": i18n.Text("sandbox.tools.exec_cwd")},
					"timeout_ms": map[string]any{"type": "integer", "description": i18n.Text("sandbox.tools.exec_timeout")},
					"env": map[string]any{
						"type":                 "object",
						"description":          i18n.Text("sandbox.tools.exec_env"),
						"additionalProperties": map[string]any{"type": "string"},
					},
				},
				"required": []string{"command"},
			},
		},
		{
			"name":        "preview_url",
			"description": i18n.Text("sandbox.tools.preview_description", "ports", portList(ports)),
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"port": map[string]any{"type": "integer", "description": i18n.Text("sandbox.tools.preview_port")},
				},
				"required": []string{"port"},
			},
		},
		{
			"name":        "attach_file",
			"description": i18n.Text("sandbox.tools.attach_description"),
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":      map[string]any{"type": "string", "description": i18n.Text("sandbox.tools.attach_path")},
					"filename":  map[string]any{"type": "string", "description": i18n.Text("sandbox.tools.attach_filename")},
					"mime_type": map[string]any{"type": "string", "description": i18n.Text("sandbox.tools.attach_mime_type")},
				},
				"required": []string{"path"},
			},
		},
	}
}

func toolErrorResult(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{
			{"type": "text", "text": msg},
		},
	}
}

func toolTextResult(blocks []string, isError bool) map[string]any {
	content := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		content = append(content, map[string]any{"type": "text", "text": b})
	}
	out := map[string]any{"content": content}
	if isError {
		out["isError"] = true
	}
	return out
}

type execArgs struct {
	Command   string            `json:"command"`
	Cwd       string            `json:"cwd"`
	TimeoutMs int               `json:"timeout_ms"`
	Env       map[string]string `json:"env"`
}

func (h *MCPHandler) toolExec(ctx context.Context, sess *mcpSession, raw json.RawMessage) any {
	var args execArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.invalid_args", "tool", "exec", "error", apperr.Text(err)))
	}
	if args.Command == "" {
		return toolErrorResult(i18n.Text("sandbox.tools.field_required", "field", "command"))
	}

	ownerID, wsPath, wsID, projects, err := h.resolver.ResolveWorkspace(ctx, sess.taskID)
	if err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.resolve_workspace_failed", "error", apperr.Text(err)))
	}
	if ownerID == "" {
		ownerID = sess.taskID
	}
	dockerOpts := AcquireDockerOpts{
		WorkspaceID:   wsID,
		WorkspacePath: wsPath,
		ProjectsPaths: projects,
	}
	execOpts := ExecOpts{
		Command:   args.Command,
		Cwd:       args.Cwd,
		TimeoutMs: args.TimeoutMs,
		Env:       args.Env,
	}
	res, ar, err := h.mgr.Exec(ctx, ownerID, sess.stage, dockerOpts, execOpts)
	if err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.exec_failed", "error", apperr.Text(err)))
	}
	if ar != nil && ar.Outcome == AcquireQueued {
		body := mustJSON(map[string]any{
			"queued":   true,
			"position": ar.QueuePosition,
			"message":  i18n.Text("sandbox.tools.queued_message"),
		})
		return toolTextResult([]string{body, i18n.Text("sandbox.tools.queued_position", "position", ar.QueuePosition)}, true)
	}

	body := mustJSON(res)
	summary := fmt.Sprintf("exit=%d duration=%dms", res.ExitCode, res.DurationMs)
	if res.TimedOut {
		summary = i18n.Text("sandbox.tools.timed_out", "duration", res.DurationMs, "limit", res.TimeoutMs)
	}
	return toolTextResult([]string{body, summary}, res.TimedOut || res.ExitCode != 0)
}

type previewArgs struct {
	Port int `json:"port"`
}

func (h *MCPHandler) toolPreviewURL(ctx context.Context, sess *mcpSession, raw json.RawMessage) any {
	var args previewArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.invalid_args", "tool", "preview_url", "error", apperr.Text(err)))
	}
	if args.Port < 1 || args.Port > 65535 {
		return toolErrorResult(i18n.Text("sandbox.tools.port_range"))
	}

	ownerID, _, _, _, err := h.resolver.ResolveWorkspace(ctx, sess.taskID)
	if err != nil || ownerID == "" {
		ownerID = sess.taskID
	}
	info, err := h.mgr.PreviewURL(ctx, ownerID, args.Port)
	if err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.preview_url_failed", "error", apperr.Text(err)))
	}
	h.recordPreview(ctx, sess, info)
	body := mustJSON(info)
	human := i18n.Text("sandbox.tools.preview_urls", "url", info.URL, "human_url", info.HumanURL)
	if info.HumanURL == "" {
		human = i18n.Text("sandbox.tools.preview_url_cluster", "url", info.URL, "note", info.Note)
	}
	return toolTextResult([]string{body, human}, false)
}

func (h *MCPHandler) recordPreview(ctx context.Context, sess *mcpSession, info PreviewInfo) {
	if h.previews == nil || info.HumanURL == "" {
		return
	}
	sess.mu.Lock()
	seen := sess.previewed[info.HumanURL]
	if !seen {
		if sess.previewed == nil {
			sess.previewed = map[string]bool{}
		}
		sess.previewed[info.HumanURL] = true
	}
	sess.mu.Unlock()
	if !seen {
		h.previews(ctx, sess.taskID, info)
	}
}

func (h *MCPHandler) appPorts() []int {
	if h.mgr == nil {
		return DefaultExposedPorts
	}
	return h.mgr.AppPorts()
}

// Base64 of 64 KiB (~87KB) stays under the docker exec output cap (100KB) and the kube execagent
// cap (10MB).
const attachChunkBytes = 65536

// Must match the REST attachment upload endpoint's 50MB limit.
const attachMaxBytes = 50 << 20

type attachFileArgs struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
}

func (h *MCPHandler) toolAttachFile(ctx context.Context, sess *mcpSession, raw json.RawMessage) any {
	var args attachFileArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.invalid_args", "tool", "attach_file", "error", apperr.Text(err)))
	}
	if args.Path == "" {
		return toolErrorResult(i18n.Text("sandbox.tools.field_required", "field", "path"))
	}
	if h.attach == nil {
		return toolErrorResult(i18n.Text("sandbox.tools.attach_unavailable"))
	}

	ownerID, wsPath, wsID, projects, err := h.resolver.ResolveWorkspace(ctx, sess.taskID)
	if err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.resolve_workspace_failed", "error", apperr.Text(err)))
	}
	if ownerID == "" {
		ownerID = sess.taskID
	}
	dockerOpts := AcquireDockerOpts{WorkspaceID: wsID, WorkspacePath: wsPath, ProjectsPaths: projects}

	data, err := h.readSandboxFile(ctx, ownerID, sess.stage, dockerOpts, args.Path)
	if errors.Is(err, errSandboxQueued) {
		return toolTextResult([]string{`{"queued":true}`, i18n.Text("sandbox.tools.queued_retry")}, true)
	}
	if err != nil {
		return toolErrorResult(apperr.Text(err))
	}
	if len(data) == 0 {
		return toolErrorResult(i18n.Text("sandbox.tools.file_empty", "path", args.Path))
	}

	filename := strings.TrimSpace(args.Filename)
	if filename == "" {
		filename = path.Base(args.Path)
	}
	mimeType := resolveMimeType(args.MimeType, filename)

	att, err := h.attach(ctx, sess.taskID, filename, mimeType, data)
	if err != nil {
		return toolErrorResult(i18n.Text("sandbox.tools.attach_failed", "error", apperr.Text(err)))
	}
	summary := i18n.Text("sandbox.tools.attached", "filename", filename, "size", len(data), "mime_type", mimeType)
	return toolTextResult([]string{mustJSON(att), summary}, false)
}

func (h *MCPHandler) readSandboxFile(ctx context.Context, ownerID, stage string, dockerOpts AcquireDockerOpts, filePath string) ([]byte, error) {
	quoted := shellSingleQuote(filePath)
	var data []byte
	for i := 0; ; i++ {
		//i18n:ignore shell command run in the sandbox
		cmd := fmt.Sprintf("dd if=%s bs=%d skip=%d count=1 2>/dev/null | base64 -w0", quoted, attachChunkBytes, i)
		res, ar, err := h.mgr.Exec(ctx, ownerID, stage, dockerOpts, ExecOpts{Command: cmd})
		if err != nil {
			return nil, apperr.Wrap(err, "sandbox.errors.read_file")
		}
		if ar != nil && ar.Outcome == AcquireQueued {
			return nil, errSandboxQueued
		}
		chunkB64 := strings.TrimSpace(res.Stdout)
		if chunkB64 == "" {
			break
		}
		chunk, err := base64.StdEncoding.DecodeString(chunkB64)
		if err != nil {
			return nil, apperr.WrapKind(apperr.KindInvalid, err, "sandbox.errors.decode_block", "block", i, "path", filePath)
		}
		data = append(data, chunk...)
		if len(data) > attachMaxBytes {
			return nil, apperr.TooLarge("sandbox.errors.file_too_large", "mb", attachMaxBytes>>20)
		}
		if len(chunk) < attachChunkBytes {
			break
		}
	}
	return data, nil
}

func resolveMimeType(given, filename string) string {
	if m := strings.TrimSpace(given); m != "" {
		return m
	}
	if m := mime.TypeByExtension(strings.ToLower(path.Ext(filename))); m != "" {
		return m
	}
	return "application/octet-stream"
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ") //nolint:errcheck // marshaling any to JSON; error only on non-marshallable types which won't be passed here
	return string(b)
}
