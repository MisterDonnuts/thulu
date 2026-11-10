package tokenopt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func plainSender(ctx context.Context, req *http.Request, _ bool) (*http.Response, error) {
	return http.DefaultClient.Do(req.WithContext(ctx))
}

type captured struct {
	path    string
	headers http.Header
	body    map[string]any
}

func fakeProvider(t *testing.T, status int, answer string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.headers = r.URL.Path, r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

var readReq = Request{
	Kind:     KindRead,
	Question: "Which functions return errors?",
	Files:    []File{{Path: `a/"x".go`, Content: "package a\nfunc F() error { return nil }"}},
}

func TestWorkerAnthropic(t *testing.T) {
	srv, got := fakeProvider(t, 200, `{"model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"- F (func, a.go:2) returns error"}],
		"usage":{"input_tokens":120,"cache_read_input_tokens":5,"output_tokens":17}}`)
	ans, err := Worker{Send: plainSender}.Call(t.Context(),
		Target{Provider: ProviderAnthropic, BaseURL: srv.URL + "/", Credential: "sk-ant-test"}, "claude-haiku-4-5", readReq)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/messages" || got.headers.Get("x-api-key") != "sk-ant-test" ||
		got.headers.Get("anthropic-version") == "" || got.headers.Get("Authorization") != "" {
		t.Fatalf("request: path %s headers %v", got.path, got.headers)
	}
	if got.body["model"] != "claude-haiku-4-5" || got.body["temperature"] != 0.2 || got.body["max_tokens"] != float64(MaxOutputTokens) {
		t.Fatalf("body = %v", got.body)
	}
	if sys, _ := got.body["system"].(string); !strings.Contains(sys, "precise code analyst") {
		t.Fatalf("system prompt = %q", sys)
	}
	msgs, _ := got.body["messages"].([]any)
	user, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(user, `<file path="a/&quot;x&quot;.go">`) || !strings.HasSuffix(user, "Question: Which functions return errors?") {
		t.Fatalf("user message = %q", user)
	}
	if ans.Text != "- F (func, a.go:2) returns error" || ans.Model != "claude-haiku-4-5-20251001" ||
		ans.InputTokens != 125 || ans.OutputTokens != 17 || ans.RequestBytes == 0 || ans.ResponseBytes == 0 {
		t.Fatalf("answer = %+v", ans)
	}
}

func TestWorkerAnthropicBearer(t *testing.T) {
	srv, got := fakeProvider(t, 200, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	ans, err := Worker{Send: plainSender}.Call(t.Context(),
		Target{Provider: ProviderAnthropic, BaseURL: srv.URL, Credential: "tok", Bearer: true}, "m", readReq)
	if err != nil {
		t.Fatal(err)
	}
	if got.headers.Get("Authorization") != "Bearer tok" || got.headers.Get("x-api-key") != "" {
		t.Fatalf("headers = %v", got.headers)
	}
	if ans.Model != "m" {
		t.Fatalf("a response with no model keeps the asked one, got %q", ans.Model)
	}
}

func TestWorkerOpenAI(t *testing.T) {
	srv, got := fakeProvider(t, 200, `{"model":"gpt-5-mini","choices":[{"message":{"content":"- F returns error"}}],`+
		`"usage":{"prompt_tokens":300,"completion_tokens":40,"cost":0.0012}}`)
	ans, err := Worker{Send: plainSender}.Call(t.Context(),
		Target{Provider: ProviderOpenAI, BaseURL: srv.URL + "/v1", Credential: "sk-proj", Headers: map[string]string{"OpenAI-Organization": "org1"}},
		"gpt-5-mini", readReq)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/chat/completions" || got.headers.Get("Authorization") != "Bearer sk-proj" || got.headers.Get("OpenAI-Organization") != "org1" {
		t.Fatalf("request: %s %v", got.path, got.headers)
	}
	if _, ok := got.body["temperature"]; ok {
		t.Fatalf("a reasoning model gets no temperature: %v", got.body)
	}
	if got.body["max_completion_tokens"] != float64(MaxOutputTokens) {
		t.Fatalf("body = %v", got.body)
	}
	msgs, _ := got.body["messages"].([]any)
	user, _ := msgs[1].(map[string]any)["content"].(string)
	if !strings.HasSuffix(user, "Question: Which functions return errors?") {
		t.Fatalf("user message = %q", user)
	}
	if ans.Text != "- F returns error" || ans.InputTokens != 300 || ans.OutputTokens != 40 || ans.CostUSD != 0.0012 {
		t.Fatalf("answer = %+v", ans)
	}
}

func TestWorkerOpenAICompatible(t *testing.T) {
	srv, got := fakeProvider(t, 200, `{"choices":[{"message":{"content":"- x"}}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`)
	if _, err := (Worker{Send: plainSender}).Call(t.Context(),
		Target{Provider: ProviderOpenAICompatible, BaseURL: srv.URL + "/api", Credential: "k"}, "deepseek-chat", readReq); err != nil {
		t.Fatal(err)
	}
	if got.path != "/api/chat/completions" || got.body["max_tokens"] != float64(MaxOutputTokens) || got.body["temperature"] != 0.2 {
		t.Fatalf("request: %s %v", got.path, got.body)
	}
}

func TestWorkerErrors(t *testing.T) {
	srv, _ := fakeProvider(t, 429, `{"error":{"type":"rate_limit_error","message":"slow down"}}`)
	_, err := Worker{Send: plainSender}.Call(t.Context(), Target{Provider: ProviderAnthropic, BaseURL: srv.URL}, "m", readReq)
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "slow down") {
		t.Fatalf("status error = %v", err)
	}

	empty, _ := fakeProvider(t, 200, `{"content":[]}`)
	if _, err := (Worker{Send: plainSender}).Call(t.Context(), Target{Provider: ProviderAnthropic, BaseURL: empty.URL}, "m", readReq); err == nil {
		t.Fatal("an empty answer must fail")
	}

	if _, err := (Worker{Send: plainSender}).Call(t.Context(), Target{Provider: "bedrock"}, "m", readReq); !errors.Is(err, ErrProviderUnsupported) {
		t.Fatalf("bedrock = %v, want ErrProviderUnsupported", err)
	}

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	_, err = Worker{Send: plainSender, Timeout: 50 * time.Millisecond}.Call(t.Context(),
		Target{Provider: ProviderOpenAI, BaseURL: slow.URL}, "m", readReq)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("slow provider = %v, want ErrTimeout", err)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	_, err = Worker{Send: plainSender}.Call(t.Context(), Target{Provider: ProviderOpenAI, BaseURL: closed.URL}, "m", readReq)
	if err == nil || errors.Is(err, ErrTimeout) {
		t.Fatalf("unreachable provider = %v", err)
	}
}

func TestRequestValidate(t *testing.T) {
	if err := readReq.Validate(); err != nil {
		t.Errorf("%+v: %v", readReq, err)
	}
	bad := []Request{
		{Kind: "summarize"},
		{Kind: KindRead, Files: readReq.Files},
		{Kind: KindRead, Question: "q"},
		{Kind: KindRead, Question: "q", Files: []File{{Content: "x"}}},
		{Kind: KindRead, Question: "q", Files: make([]File, MaxFiles+1)},
	}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%+v: want an error", r.Kind)
		}
	}
	write := Request{Kind: "write", Question: "q", Files: readReq.Files}
	if err := write.Validate(); !errors.Is(err, ErrWriteRemoved) {
		t.Fatalf("write = %v, want ErrWriteRemoved", err)
	}
}
