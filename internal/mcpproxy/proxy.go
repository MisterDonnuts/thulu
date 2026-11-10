package mcpproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/mcp"
)

type RenewFunc func(ctx context.Context, rejected http.Header) (http.Header, error)

type Target struct {
	Name      string
	URL       string
	Transport string
	Header    http.Header
	Entry     mcp.ToolEntry
	Renew     RenewFunc
}

// Screenshot tool results run to a few MB; an unbounded line is memory an upstream could take.
const maxEventBytes = 64 << 20

type Proxy struct {
	client *http.Client

	mu       sync.Mutex
	sessions map[string]*sseSession
}

// The client must not set a Timeout: streams stay open as long as the agent listens.
func New(client *http.Client) *Proxy {
	return &Proxy{client: client, sessions: map[string]*sseSession{}}
}

// Allowlist: the client's X-API-Key, Authorization and cookies authenticate to the daemon and must
// not reach the upstream.
var passRequestHeaders = []string{
	"Accept", "Content-Type", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-Id", "User-Agent", //i18n:ignore HTTP header names
}

var passResponseHeaders = []string{"Content-Type", "Mcp-Session-Id", "Cache-Control"}

func (p *Proxy) newRequest(ctx context.Context, method, target string, body io.Reader, in *http.Request, t Target) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		for _, h := range passRequestHeaders {
			if v := in.Header.Values(h); len(v) > 0 {
				req.Header[http.CanonicalHeaderKey(h)] = append([]string(nil), v...)
			}
		}
	}
	if body != http.NoBody {
		// Force UTF-8 JSON: a client charset (utf-7, utf-16) would make the upstream decode text
		// the allowlist check never read.
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range t.Header {
		req.Header[k] = append([]string(nil), vs...)
	}
	return req, nil
}

func (p *Proxy) send(ctx context.Context, t *Target, build func(Target) (*http.Request, error)) (*http.Response, error) {
	req, err := build(*t)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || t.Renew == nil {
		return resp, err
	}
	h, err := t.Renew(ctx, t.Header)
	if errors.Is(err, ErrNotRenewable) {
		return resp, nil
	}
	resp.Body.Close()
	if err != nil {
		return nil, &renewError{err: err}
	}
	t.Header = h
	if req, err = build(*t); err != nil {
		return nil, err
	}
	return p.client.Do(req)
}

type renewError struct{ err error }

func (e *renewError) Error() string { return e.err.Error() }
func (e *renewError) Unwrap() error { return e.err }

func writeSendError(w http.ResponseWriter, r *http.Request, t Target, err error) {
	var re *renewError
	if !errors.As(err, &re) {
		writeError(w, r, http.StatusBadGateway, apperr.Wrap(err, "mcpproxy.errors.upstream", "server", t.Name))
		return
	}
	var refusal *Refusal
	if errors.As(re.err, &refusal) {
		writeError(w, r, http.StatusForbidden, apperr.Forbidden("mcpproxy.errors.credential_refused",
			"server", t.Name, "reason", string(refusal.Decision.Reason), "detail", refusal.Decision.Detail))
		return
	}
	writeError(w, r, http.StatusBadGateway, apperr.Wrap(re.err, "mcpproxy.errors.renew_failed", "server", t.Name))
}

func (p *Proxy) Streamable(w http.ResponseWriter, r *http.Request, t Target) {
	var raw []byte
	if r.Method == http.MethodPost {
		var err error
		raw, err = io.ReadAll(r.Body)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, apperr.Invalid("mcpproxy.errors.read_body"))
			return
		}
		if denied := screen(r.Context(), raw, t.Entry); denied != nil {
			writeScreened(w, denied)
			return
		}
	}
	resp, err := p.send(r.Context(), &t, func(t Target) (*http.Request, error) {
		// New reader per attempt: a renewed credential resends the same body.
		var body io.Reader = http.NoBody
		if raw != nil {
			body = bytes.NewReader(raw)
		}
		return p.newRequest(r.Context(), streamableMethod(r.Method), t.URL, body, r, t)
	})
	if err != nil {
		writeSendError(w, r, t, err)
		return
	}
	defer resp.Body.Close()
	if refusedUpstream(w, r, resp, t) {
		return
	}
	copyResponseHeaders(w, resp)
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		w.WriteHeader(resp.StatusCode)
		p.relayEvents(r.Context(), w, resp.Body, t, nil)
	case strings.HasPrefix(ct, "application/json") && t.Entry.Restricted():
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxEventBytes))
		if err != nil {
			writeError(w, r, http.StatusBadGateway, apperr.Wrap(err, "mcpproxy.errors.upstream", "server", t.Name))
			return
		}
		out := filterMessage(raw, t.Entry)
		w.Header().Del("Content-Length")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
	default:
		w.WriteHeader(resp.StatusCode)
		copyBody(w, resp.Body)
	}
}

func writeScreened(w http.ResponseWriter, s *screened) {
	switch {
	case s.invalid:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
	case len(s.answer) == 0:
		w.WriteHeader(http.StatusAccepted)
		return
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}
	_, _ = w.Write(s.answer)
}

// Never forward a client-chosen method; anything else becomes a GET that cannot change state.
func streamableMethod(m string) string {
	switch m {
	case http.MethodPost:
		return http.MethodPost
	case http.MethodDelete:
		return http.MethodDelete
	default:
		return http.MethodGet
	}
}

type sseSession struct {
	owner  string // Run key id: only this run may post to the session.
	name   string
	mu     sync.Mutex
	post   string
	inject chan []byte
	done   chan struct{}
}

func (s *sseSession) postURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.post
}

func (p *Proxy) OpenSSE(w http.ResponseWriter, r *http.Request, t Target, owner string, messagesPath func(id string) string) {
	resp, err := p.send(r.Context(), &t, func(t Target) (*http.Request, error) {
		req, err := p.newRequest(r.Context(), http.MethodGet, t.URL, http.NoBody, nil, t)
		if err == nil {
			req.Header.Set("Accept", "text/event-stream")
		}
		return req, err
	})
	if err != nil {
		writeSendError(w, r, t, err)
		return
	}
	defer resp.Body.Close()
	if refusedUpstream(w, r, resp, t) {
		return
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		writeError(w, r, http.StatusBadGateway, apperr.New("mcpproxy.errors.upstream_not_sse", "server", t.Name, "status", resp.StatusCode))
		return
	}

	id := newSessionID()
	sess := &sseSession{owner: owner, name: t.Name, inject: make(chan []byte, 16), done: make(chan struct{})}
	p.mu.Lock()
	p.sessions[id] = sess
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.sessions, id)
		p.mu.Unlock()
		close(sess.done)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if flush(w) != nil {
		return
	}

	endpoint := func(ev *event) bool {
		target, err := sameOriginEndpoint(t.URL, ev.data)
		if err != nil {
			return false
		}
		sess.mu.Lock()
		sess.post = target
		sess.mu.Unlock()
		ev.data = messagesPath(id)
		return true
	}
	p.relayEvents(r.Context(), w, resp.Body, t, &sseHooks{endpoint: endpoint, inject: sess.inject})
}

func (p *Proxy) PostSSE(w http.ResponseWriter, r *http.Request, t Target, owner, sessionID string) {
	p.mu.Lock()
	sess := p.sessions[sessionID]
	p.mu.Unlock()
	// Another run's session answers exactly like a missing one.
	if sess == nil || sess.owner != owner || sess.name != t.Name || sess.postURL() == "" {
		writeError(w, r, http.StatusNotFound, apperr.NotFound("mcpproxy.errors.unknown_session"))
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.Invalid("mcpproxy.errors.read_body"))
		return
	}
	if denied := screen(r.Context(), raw, t.Entry); denied != nil {
		if denied.invalid {
			writeScreened(w, denied)
			return
		}
		if len(denied.answer) > 0 {
			select {
			case sess.inject <- denied.answer:
			case <-sess.done:
				writeError(w, r, http.StatusNotFound, apperr.NotFound("mcpproxy.errors.unknown_session"))
				return
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("Accepted")) //i18n:ignore the MCP SSE transport's acknowledgement body
		return
	}
	resp, err := p.send(r.Context(), &t, func(t Target) (*http.Request, error) {
		return p.newRequest(r.Context(), http.MethodPost, sess.postURL(), bytes.NewReader(raw), r, t)
	})
	if err != nil {
		writeSendError(w, r, t, err)
		return
	}
	defer resp.Body.Close()
	if refusedUpstream(w, r, resp, t) {
		return
	}
	copyResponseHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	copyBody(w, io.LimitReader(resp.Body, maxEventBytes))
}

// The proxy posts there with the run's credential, so the upstream must not point it at another
// origin.
func sameOriginEndpoint(sseURL, endpoint string) (string, error) {
	base, err := url.Parse(sseURL)
	if err != nil {
		return "", apperr.WrapKind(apperr.KindInvalid, err, "mcpproxy.errors.endpoint_invalid", "endpoint", sseURL)
	}
	ref, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", apperr.WrapKind(apperr.KindInvalid, err, "mcpproxy.errors.endpoint_invalid", "endpoint", endpoint)
	}
	abs := base.ResolveReference(ref)
	if abs.Scheme != base.Scheme || abs.Host != base.Host {
		return "", apperr.Invalid("mcpproxy.errors.endpoint_other_origin", "endpoint", endpoint)
	}
	return abs.String(), nil
}

func newSessionID() string { return rand.Text() }

type event struct {
	comments []string
	name     string
	id       string
	data     string
	hasData  bool
	retry    string
}

func (e *event) write(w io.Writer) error {
	var b strings.Builder
	for _, c := range e.comments {
		b.WriteString(":" + c + "\n")
	}
	if e.name != "" {
		b.WriteString("event: " + e.name + "\n")
	}
	if e.id != "" {
		b.WriteString("id: " + e.id + "\n")
	}
	if e.retry != "" {
		b.WriteString("retry: " + e.retry + "\n")
	}
	if e.hasData {
		for _, line := range strings.Split(e.data, "\n") {
			b.WriteString("data: " + line + "\n")
		}
	}
	b.WriteString("\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func readEvents(ctx context.Context, r io.Reader, out chan<- *event) {
	defer close(out)
	br := bufio.NewReaderSize(r, 64<<10)
	ev := &event{}
	pending := false
	for {
		line, err := readLine(br)
		if err != nil {
			return
		}
		if line == "" {
			if pending {
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
			ev, pending = &event{}, false
			continue
		}
		pending = true
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "":
			ev.comments = append(ev.comments, strings.TrimPrefix(line, ":"))
		case "event":
			ev.name = value
		case "id":
			ev.id = value
		case "retry":
			ev.retry = value
		case "data":
			if ev.hasData {
				ev.data += "\n"
			}
			ev.data += value
			ev.hasData = true
		}
	}
}

func readLine(br *bufio.Reader) (string, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return "", err
		}
		buf = append(buf, chunk...)
		if len(buf) > maxEventBytes {
			return "", errors.New("mcpproxy: event line too long") //i18n:ignore internal sentinel, never shown
		}
		if !isPrefix {
			return string(buf), nil
		}
	}
}

type sseHooks struct {
	endpoint func(*event) bool
	inject   <-chan []byte
}

func (p *Proxy) relayEvents(ctx context.Context, w http.ResponseWriter, body io.Reader, t Target, hooks *sseHooks) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan *event)
	go readEvents(ctx, body, events)
	var inject <-chan []byte
	if hooks != nil {
		inject = hooks.inject
	}
	for {
		var ev *event
		select {
		case <-ctx.Done():
			return
		case msg := <-inject:
			ev = &event{name: "message", data: string(msg), hasData: true}
		case upstream, ok := <-events:
			if !ok || !rewriteEvent(upstream, t, hooks) {
				return
			}
			ev = upstream
		}
		if ev.write(w) != nil || flush(w) != nil {
			return
		}
	}
}

func rewriteEvent(ev *event, t Target, hooks *sseHooks) bool {
	if hooks != nil && ev.name == "endpoint" {
		return hooks.endpoint(ev)
	}
	if ev.hasData && t.Entry.Restricted() {
		ev.data = string(filterMessage([]byte(ev.data), t.Entry))
	}
	return true
}

func flush(w http.ResponseWriter) error {
	return http.NewResponseController(w).Flush()
}

func copyBody(w io.Writer, r io.Reader) {
	if _, err := io.Copy(w, r); err != nil {
		return
	}
}

type rpcMessage struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

// -32602 is the code the spec gives an unknown tool, which a refused tool is to the run.
const errNotAllowed = -32602

const errInvalidRequest = -32600

type screened struct {
	invalid bool
	answer  []byte
}

// Fails closed: an unparseable body is refused because the upstream (MCP SDK, JSON.parse) may read
// a call we did not see.
// A batch with one refused call is refused whole, so one request never splits into two answers.
func screen(ctx context.Context, raw []byte, e mcp.ToolEntry) *screened {
	if !e.Restricted() {
		return nil
	}
	msgs, batch, ok := parseRPC(raw)
	if !ok {
		return &screened{invalid: true, answer: marshalAnswer(rpcError{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: rpcErrorBody{Code: errInvalidRequest, Message: i18n.TCtx(ctx, "mcpproxy.errors.invalid_message", "server", e.Server)}})}
	}
	refused, found := "", false
	for _, m := range msgs {
		if m.Method != "tools/call" {
			continue
		}
		name, ok := callName(m.Params)
		if !ok || !e.Allows(name) {
			refused, found = name, true
			break
		}
	}
	if !found {
		return nil
	}
	msg := i18n.TCtx(ctx, "mcpproxy.errors.tool_not_allowed", "tool", refused, "server", e.Server)
	answers := []rpcError{}
	for _, m := range msgs {
		if len(m.ID) == 0 {
			continue
		}
		answers = append(answers, rpcError{JSONRPC: "2.0", ID: m.ID,
			Error: rpcErrorBody{Code: errNotAllowed, Message: msg}})
	}
	if batch {
		return &screened{answer: marshalAnswer(answers)}
	}
	if len(answers) == 0 {
		return &screened{answer: []byte{}}
	}
	return &screened{answer: marshalAnswer(answers[0])}
}

func marshalAnswer(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		return []byte{}
	}
	return out
}

// encoding/json matches fields case-insensitively and keeps the last duplicate, so
// {"method":"tools/call","METHOD":"x"}
// would read as "x" here and tools/call in JavaScript. Hence exact keys, no case-folded duplicates,
// no BOM, valid UTF-8.
func parseRPC(raw []byte) (msgs []rpcMessage, batch, ok bool) {
	if !utf8.Valid(raw) || bytes.HasPrefix(raw, []byte("\xEF\xBB\xBF")) {
		return nil, false, false
	}
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(trimmed, &items) != nil || len(items) == 0 {
			return nil, true, false
		}
		for _, item := range items {
			m, read := parseMessage(item)
			if !read {
				return nil, true, false
			}
			msgs = append(msgs, m)
		}
		return msgs, true, true
	}
	m, ok := parseMessage(trimmed)
	return []rpcMessage{m}, false, ok
}

func parseMessage(raw []byte) (rpcMessage, bool) {
	obj, ok := strictObject(raw)
	if !ok {
		return rpcMessage{}, false
	}
	var version string
	if json.Unmarshal(obj["jsonrpc"], &version) != nil || version != "2.0" {
		return rpcMessage{}, false
	}
	m := rpcMessage{ID: obj["id"], Params: obj["params"]}
	if method, ok := obj["method"]; ok {
		if json.Unmarshal(method, &m.Method) != nil || m.Method == "" {
			return rpcMessage{}, false
		}
	}
	return m, true
}

func strictObject(raw []byte) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	obj := map[string]json.RawMessage{}
	folded := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, isKey := tok.(string)
		if err != nil || !isKey || folded[strings.ToLower(key)] {
			return nil, false
		}
		folded[strings.ToLower(key)] = true
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return nil, false
		}
		obj[key] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return obj, true
}

type rpcError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   rpcErrorBody    `json:"error"`
}

type rpcErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func callName(params json.RawMessage) (string, bool) {
	p, ok := strictObject(params)
	if !ok {
		return "", false
	}
	var name string
	if json.Unmarshal(p["name"], &name) != nil || name == "" {
		return "", false
	}
	return name, true
}

// Not a security boundary: unreadable messages pass unchanged; the tools/call check is what stops
// calls.
func filterMessage(raw []byte, e mcp.ToolEntry) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw
	}
	if trimmed[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal(trimmed, &batch) != nil {
			return raw
		}
		changed := false
		for i := range batch {
			if out, ok := filterOne(batch[i], e); ok {
				batch[i], changed = out, true
			}
		}
		if !changed {
			return raw
		}
		out, err := json.Marshal(batch)
		if err != nil {
			return raw
		}
		return out
	}
	if out, ok := filterOne(trimmed, e); ok {
		return out
	}
	return raw
}

func filterOne(raw json.RawMessage, e mcp.ToolEntry) (json.RawMessage, bool) {
	var msg map[string]json.RawMessage
	if json.Unmarshal(raw, &msg) != nil {
		return nil, false
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(msg["result"], &result) != nil {
		return nil, false
	}
	var tools []map[string]json.RawMessage
	if json.Unmarshal(result["tools"], &tools) != nil || tools == nil {
		return nil, false
	}
	kept := tools[:0]
	for _, tool := range tools {
		var name string
		if json.Unmarshal(tool["name"], &name) == nil && e.Allows(name) {
			kept = append(kept, tool)
		}
	}
	keptRaw, err := json.Marshal(kept)
	if err != nil {
		return nil, false
	}
	result["tools"] = keptRaw
	resultRaw, err := json.Marshal(result)
	if err != nil {
		return nil, false
	}
	msg["result"] = resultRaw
	out, err := json.Marshal(msg)
	if err != nil {
		return nil, false
	}
	return out, true
}

func copyResponseHeaders(w http.ResponseWriter, resp *http.Response) {
	for _, h := range passResponseHeaders {
		if v := resp.Header.Values(h); len(v) > 0 {
			w.Header()[http.CanonicalHeaderKey(h)] = append([]string(nil), v...)
		}
	}
}

// Passing a 401 through would send the agent's MCP client into OAuth discovery against the daemon;
// answer 502 instead.
func refusedUpstream(w http.ResponseWriter, r *http.Request, resp *http.Response, t Target) bool {
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return false
	}
	writeError(w, r, http.StatusBadGateway, apperr.New("mcpproxy.errors.upstream_refused", "server", t.Name, "status", resp.StatusCode))
	return true
}

func writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	apperr.WriteJSON(w, r, status, err)
}
