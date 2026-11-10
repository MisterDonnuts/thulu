package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/netguard"
)

// Must match the protocol version internal/sandbox's MCP server speaks.
const ProtocolVersion = "2024-11-05"

type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

const probeTimeout = 6 * time.Second

const (
	pingTimeout      = 3 * time.Second
	pingCacheTTL     = 10 * time.Second
	probeCacheTTL    = 2 * time.Minute
	probeFailureTTL  = 15 * time.Second
	probeMaxSSEBytes = 1 << 20
)

type probeEntry struct {
	tools   []ToolInfo
	err     error
	expires time.Time
}

type Prober struct {
	client *http.Client
	now    func() time.Time

	mu       sync.Mutex
	cache    map[string]probeEntry
	inflight map[string]chan struct{}
}

// No ambient proxy: these are in-cluster URLs. The internal policy admits private addresses but
// refuses link-local/metadata,
// since both the admin-edited URL and the server's own endpoint event pick the destination.
func NewProber() *Prober {
	tr := netguard.NewInternal().Transport()
	tr.DisableKeepAlives = true
	return &Prober{
		client:   &http.Client{Transport: tr},
		now:      time.Now,
		cache:    make(map[string]probeEntry),
		inflight: make(map[string]chan struct{}),
	}
}

func (p *Prober) Tools(ctx context.Context, sseURL string) ([]ToolInfo, error) {
	if strings.TrimSpace(sseURL) == "" {
		return nil, apperr.Invalid("mcp.errors.probe_empty_url")
	}
	return p.cached(ctx, sseURL, probeCacheTTL, probeFailureTTL, func() ([]ToolInfo, error) {
		return p.probe(ctx, sseURL)
	})
}

func (p *Prober) Ping(ctx context.Context, sseURL string) error {
	if strings.TrimSpace(sseURL) == "" {
		return apperr.Invalid("mcp.errors.ping_empty_url")
	}
	// NUL prefix keeps ping entries apart from Tools entries, which are keyed by the bare URL.
	_, err := p.cached(ctx, "\x00ping\x00"+sseURL, pingCacheTTL, pingCacheTTL, func() ([]ToolInfo, error) {
		return nil, p.ping(ctx, sseURL)
	})
	return err
}

func (p *Prober) cached(ctx context.Context, key string, okTTL, failTTL time.Duration, fn func() ([]ToolInfo, error)) ([]ToolInfo, error) {
	for {
		p.mu.Lock()
		if e, ok := p.cache[key]; ok && p.now().Before(e.expires) {
			p.mu.Unlock()
			return e.tools, e.err
		}
		wait, busy := p.inflight[key]
		if !busy {
			done := make(chan struct{})
			p.inflight[key] = done
			p.mu.Unlock()

			tools, err := fn()

			p.mu.Lock()
			ttl := okTTL
			if err != nil {
				ttl = failTTL
			}
			p.cache[key] = probeEntry{tools: tools, err: err, expires: p.now().Add(ttl)}
			delete(p.inflight, key)
			p.mu.Unlock()
			close(done)
			return tools, err
		}
		p.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (p *Prober) ping(ctx context.Context, sseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pingURL(sseURL), http.NoBody)
	if err != nil {
		return apperr.Wrap(err, "mcp.errors.ping_build_request")
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := p.client.Do(req)
	if err != nil {
		return apperr.Wrap(err, "mcp.errors.ping_connect")
	}
	// Close, never drain: an SSE stream never ends.
	defer func() { _ = resp.Body.Close() }()
	// 401/403 means up: the ping has no run key to offer, and a server that checks credentials
	// answered.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return apperr.New("mcp.errors.ping_sse_status", "status", resp.StatusCode)
	}
	if _, err := newSSEReader(io.LimitReader(resp.Body, probeMaxSSEBytes)).awaitEndpoint(); err != nil {
		return err
	}
	return nil
}

// MCP SSE transport (as internal/sandbox/mcp_server.go): responses arrive on the GET stream; the
// POST only acknowledges.
func (p *Prober) probe(ctx context.Context, sseURL string) ([]ToolInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, probeURL(sseURL), http.NoBody)
	if reqErr != nil {
		return nil, apperr.Wrap(reqErr, "mcp.errors.probe_build_request")
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, doErr := p.client.Do(req)
	if doErr != nil {
		return nil, apperr.Wrap(doErr, "mcp.errors.probe_connect")
	}
	// Close, never drain: an SSE stream never ends, so draining would make every successful probe
	// wait out the timeout.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, apperr.New("mcp.errors.probe_sse_status", "status", resp.StatusCode)
	}

	stream := newSSEReader(io.LimitReader(resp.Body, probeMaxSSEBytes))

	endpoint, epErr := stream.awaitEndpoint()
	if epErr != nil {
		return nil, epErr
	}
	postURL, urlErr := resolveEndpoint(sseURL, endpoint)
	if urlErr != nil {
		return nil, urlErr
	}

	if err := p.post(ctx, postURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "octopus-daemon", "version": "1"},
		},
	}); err != nil {
		return nil, err
	}
	if _, err := stream.awaitResponse(1); err != nil {
		return nil, err
	}

	// Servers refuse tools/list until notifications/initialized is sent.
	if err := p.post(ctx, postURL, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}); err != nil {
		return nil, err
	}

	if err := p.post(ctx, postURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	}); err != nil {
		return nil, err
	}
	result, listErr := stream.awaitResponse(2)
	if listErr != nil {
		return nil, listErr
	}

	var listed struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(result, &listed); err != nil {
		return nil, apperr.Wrap(err, "mcp.errors.probe_decode_tools")
	}
	return listed.Tools, nil
}

func (p *Prober) post(ctx context.Context, postURL string, payload map[string]any) error {
	return apperr.Wrap(postRPC(ctx, p.client, postURL, "", payload), "mcp.errors.probe_post")
}

// scope=write: scoped servers (thulu-github) filter tools/list and the picker needs the full
// catalog.
// catalog=1: lists tools no scope grants (octopus-api cross-board writes); servers widen listing
// only, never calls.
func probeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("scope", "write")
	q.Set("catalog", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

// Native servers open a session without a run key only for the list-only catalog=1 handshake.
func pingURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("catalog", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

func resolveEndpoint(sseURL, endpoint string) (string, error) {
	base, err := url.Parse(sseURL)
	if err != nil {
		return "", apperr.Wrap(err, "mcp.errors.probe_bad_sse_url", "url", sseURL)
	}
	ref, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", apperr.Wrap(err, "mcp.errors.probe_bad_endpoint", "endpoint", endpoint)
	}
	return base.ResolveReference(ref).String(), nil
}

type sseReader struct {
	sc *bufio.Scanner
}

func newSSEReader(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), probeMaxSSEBytes)
	return &sseReader{sc: sc}
}

func (s *sseReader) next() (event, data string, err error) {
	var (
		name string
		buf  []string
	)
	for s.sc.Scan() {
		line := s.sc.Text()
		if line == "" {
			if len(buf) == 0 && name == "" {
				continue
			}
			if name == "" {
				name = "message"
			}
			return name, strings.Join(buf, "\n"), nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			buf = append(buf, value)
		}
	}
	if err := s.sc.Err(); err != nil {
		return "", "", apperr.Wrap(err, "mcp.errors.probe_read_sse")
	}
	return "", "", io.EOF
}

func (s *sseReader) awaitEndpoint() (string, error) {
	for {
		event, data, err := s.next()
		if err != nil {
			return "", apperr.Wrap(err, "mcp.errors.probe_no_endpoint_event")
		}
		if event == "endpoint" {
			return data, nil
		}
	}
}

func (s *sseReader) awaitResponse(id int) (json.RawMessage, error) {
	for {
		_, data, err := s.next()
		if err != nil {
			return nil, apperr.Wrap(err, "mcp.errors.probe_no_response", "id", id)
		}
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			continue
		}
		if msg.ID == nil || *msg.ID != id {
			continue
		}
		if msg.Error != nil {
			return nil, apperr.New("mcp.errors.probe_server_error", "code", msg.Error.Code, "message", msg.Error.Message)
		}
		return msg.Result, nil
	}
}
