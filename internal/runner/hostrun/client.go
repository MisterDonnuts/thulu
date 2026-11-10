package hostrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

type Client struct {
	base       string
	credential string
	version    string
	http       *http.Client
}

func NewClient(base, credential, version string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"), credential: credential, version: version,
		// No overall timeout: the claim long-polls up to runner.ClaimWait and an upload goes at the
		// link's speed. Each call carries its own context.
		http: &http.Client{},
	}
}

type callError struct {
	Status int
	Body   string
}

func (e *callError) Error() string { return http.StatusText(e.Status) + ": " + e.Body }

func isNotHeld(err error) bool {
	var ce *callError
	return asCallError(err, &ce) && (ce.Status == http.StatusConflict || ce.Status == http.StatusUnauthorized)
}

func asCallError(err error, target **callError) bool {
	ce, ok := err.(*callError) //nolint:errorlint // do returns it unwrapped
	if ok {
		*target = ce
	}
	return ok
}

func (c *Client) do(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/runner/v1/"+path, body)
	if err != nil {
		return apperr.Wrap(err, "hostrun.errors.call", "path", path)
	}
	req.Header.Set(runner.ProtocolHeader, runner.ProtocolVersion)
	req.Header.Set("User-Agent", "octopus-runner/"+c.version) //i18n:ignore HTTP user agent
	if c.credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.credential) //i18n:ignore HTTP auth scheme
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return apperr.Wrap(err, "hostrun.errors.call", "path", path)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		logx.Warn(rerr, "hostrun.log.read_answer_failed", "path", path)
		return &callError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return apperr.Wrap(err, "hostrun.errors.call", "path", path)
	}
	return nil
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return apperr.Wrap(err, "hostrun.errors.call", "path", path)
	}
	return c.do(ctx, http.MethodPost, path, "application/json", bytes.NewReader(b), out)
}

func (c *Client) Register(ctx context.Context, req runner.RegisterRequest) (*runner.RegisterResponse, error) {
	var out runner.RegisterResponse
	if err := c.postJSON(ctx, "register", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Claim(ctx context.Context, projects []string) (*runner.ClaimResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, runner.ClaimWait+30*time.Second)
	defer cancel()
	var out runner.ClaimResponse
	err := c.postJSON(ctx, "claim", runner.ClaimRequest{Projects: projects, Version: c.version}, &out)
	return &out, err
}

func (c *Client) Heartbeat(ctx context.Context, jobID string) (*runner.HeartbeatResponse, error) {
	var out runner.HeartbeatResponse
	err := c.postJSON(ctx, "jobs/"+jobID+"/heartbeat", struct{}{}, &out)
	return &out, err
}

func (c *Client) Log(ctx context.Context, jobID string, chunk []byte) error {
	return c.do(ctx, http.MethodPost, "jobs/"+jobID+"/log", "text/plain", bytes.NewReader(chunk), nil)
}

func (c *Client) Outputs(ctx context.Context, jobID, dir string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(runner.PackOutputs(dir, pw)) }()
	err := c.do(ctx, http.MethodPut, "jobs/"+jobID+"/outputs", "application/gzip", pr, nil)
	logx.Close(pr, "outputs upload") //i18n:ignore resource name in a log line
	return err
}

func (c *Client) Finish(ctx context.Context, jobID string, res runner.FinishRequest) error {
	return c.postJSON(ctx, "jobs/"+jobID+"/finish", res, nil)
}
