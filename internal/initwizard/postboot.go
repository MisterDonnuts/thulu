package initwizard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type APIClient struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

func NewAPIClient(baseURL, key string) *APIClient {
	return &APIClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Key:     key,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *APIClient) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apperr.Wrap(err, "initwizard.errors.encode_request")
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, payload)
	if err != nil {
		return apperr.Wrap(err, "initwizard.errors.build_request")
	}
	//i18n:ignore HTTP auth scheme
	req.Header.Set("Authorization", "Bearer "+c.Key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		detail, readErr := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if readErr != nil {
			return apperr.Wrap(readErr, "initwizard.errors.response_unreadable", "method", method, "path", path, "status", resp.Status)
		}
		return fmt.Errorf("%s %s → %s: %s", method, path, resp.Status, strings.TrimSpace(string(detail)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return apperr.Wrap(err, "initwizard.errors.decode_response")
	}
	return nil
}

type CredentialResult struct {
	ID              string   `json:"id"`
	State           string   `json:"state"`
	Identity        string   `json:"identity"`
	Hint            string   `json:"hint"`
	Capabilities    []string `json:"capabilities"`
	IntrospectError string   `json:"introspect_error"`
}

func (c *APIClient) CreateGitCredential(ctx context.Context, name, token string) (*CredentialResult, error) {
	body := map[string]any{
		"name":     name,
		"provider": "github",
		"kind":     "github_pat",
		"payload":  map[string]string{"token": token},
		"grants":   []map[string]string{{"subject_type": "workspace"}},
	}
	var created CredentialResult
	if err := c.do(ctx, http.MethodPost, "/credentials", body, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func (c *APIClient) CreateLLMProfile(ctx context.Context, name string, cred EngineCredential) error {
	body := map[string]any{
		"name":       name,
		"engine":     cred.Engine,
		"key":        cred.Key,
		"is_default": true,
	}
	if cred.Kind != "" {
		body["kind"] = cred.Kind
	}
	if cred.BaseURL != "" {
		body["base_url"] = cred.BaseURL
	}
	if cred.Model != "" {
		body["model"] = cred.Model
	}
	return c.do(ctx, http.MethodPost, "/llm-profiles", body, nil)
}

func (c *APIClient) CreateRunnerToken(ctx context.Context, name string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := c.do(ctx, http.MethodPost, "/runners/tokens", map[string]string{"name": name}, &out); err != nil {
		return "", err
	}
	return out.Token, nil
}

func (c *APIClient) CreateRunnerProject(ctx context.Context, runnerID, name string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/runners/"+runnerID+"/projects", map[string]string{"name": name}, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *APIClient) Reachable(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/projects", nil, &json.RawMessage{})
}
