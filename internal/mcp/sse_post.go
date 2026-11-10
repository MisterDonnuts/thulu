package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func postRPC(ctx context.Context, client *http.Client, postURL, apiKey string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return apperr.Wrap(err, "mcp.errors.post_encode", "method", payload["method"])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(body))
	if err != nil {
		return apperr.Wrap(err, "mcp.errors.post_build")
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return apperr.Wrap(err, "mcp.errors.post_send", "method", payload["method"])
	}
	defer func() {
		if _, cerr := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)); cerr != nil {
			_ = cerr
		}
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 300 {
		return apperr.New("mcp.errors.post_status", "method", payload["method"], "status", resp.StatusCode)
	}
	return nil
}
