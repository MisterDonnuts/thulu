package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const callTimeout = 60 * time.Second

type Caller struct {
	client *http.Client
}

func NewCaller() *Caller {
	return &Caller{client: &http.Client{Timeout: callTimeout + 5*time.Second}}
}

type ToolError struct {
	Tool    string
	Message string
}

func (e *ToolError) Error() string {
	return i18n.Log("mcp.errors.tool_failed", "tool", e.Tool, "message", e.Message)
}

// apiKey is the calling run's key, so a tool that writes back into Thulu acts with that run's
// workspace and limits.
func (c *Caller) Call(ctx context.Context, sseURL, tool string, args map[string]any, apiKey string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sseURL, http.NoBody)
	if err != nil {
		return nil, apperr.Wrap(err, "mcp.errors.call_build_request")
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, apperr.Wrap(err, "mcp.errors.call_connect")
	}
	// Close, never drain: an SSE stream has no end.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, apperr.New("mcp.errors.call_sse_status", "status", resp.StatusCode)
	}

	stream := newSSEReader(io.LimitReader(resp.Body, probeMaxSSEBytes))
	endpoint, err := stream.awaitEndpoint()
	if err != nil {
		return nil, err
	}
	postURL, err := resolveEndpoint(sseURL, endpoint)
	if err != nil {
		return nil, err
	}

	if hsErr := c.handshake(ctx, stream, postURL, apiKey); hsErr != nil {
		return nil, hsErr
	}

	if args == nil {
		args = map[string]any{}
	}
	if callErr := c.post(ctx, postURL, apiKey, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	}); callErr != nil {
		return nil, callErr
	}
	raw, err := stream.awaitResponse(2)
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, apperr.Wrap(err, "mcp.errors.call_decode_result")
	}
	// MCP reports tool failure inside a successful JSON-RPC response (isError).
	if failed, ok := result["isError"].(bool); ok && failed {
		return result, &ToolError{Tool: tool, Message: firstText(result)}
	}
	return result, nil
}

func firstText(result map[string]any) string {
	content, ok := result["content"].([]any)
	if !ok {
		return i18n.Log("mcp.errors.no_message")
	}
	for _, c := range content {
		block, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := block["text"].(string); ok && t != "" {
			return t
		}
	}
	return i18n.Log("mcp.errors.no_message")
}

func (c *Caller) post(ctx context.Context, postURL, apiKey string, payload map[string]any) error {
	return apperr.Wrap(postRPC(ctx, c.client, postURL, apiKey, payload), "mcp.errors.call_post")
}

func (c *Caller) handshake(ctx context.Context, stream *sseReader, postURL, apiKey string) error {
	if err := c.post(ctx, postURL, apiKey, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "octopus-automation", "version": "1"},
		},
	}); err != nil {
		return err
	}
	if _, err := stream.awaitResponse(1); err != nil {
		return err
	}
	return c.post(ctx, postURL, apiKey, map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})
}
