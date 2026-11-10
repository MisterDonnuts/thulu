package tokenopt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const KindRead = "read"

// The delegations table keeps old write rows; a run that still asks for one gets ErrWriteRemoved.
const kindWrite = "write"

const (
	ProviderAnthropic        = "anthropic"
	ProviderOpenAI           = "openai"
	ProviderOpenAICompatible = "openai-compatible"
)

func SupportedProvider(provider string) bool {
	switch provider {
	case ProviderAnthropic, ProviderOpenAI, ProviderOpenAICompatible:
		return true
	}
	return false
}

const (
	MaxRequestBytes  = 1 << 20
	MaxFiles         = 200
	Timeout          = 120 * time.Second
	maxResponseBytes = 8 << 20
	MaxOutputTokens  = 8192
	temperature      = 0.2
	anthropicVersion = "2023-06-01"
)

var (
	ErrDisabled            error = apperr.Forbidden("tokenopt.errors.disabled")
	ErrNoWorker            error = apperr.Precondition("tokenopt.errors.no_worker")
	ErrProviderUnsupported error = apperr.Precondition("tokenopt.errors.provider_unsupported")
	ErrProfileNotUsable    error = apperr.Precondition("tokenopt.errors.profile_not_usable")
	ErrTimeout             error = apperr.Timeout("tokenopt.errors.timeout")
	ErrWorkerInPod         error = apperr.Precondition("tokenopt.errors.worker_in_pod")
	ErrWorkerNotInPod      error = apperr.Precondition("tokenopt.errors.worker_not_in_pod")
	ErrWriteRemoved        error = apperr.Invalid("tokenopt.errors.write_removed")
	// Error code the run's pod reports to finish; the pod also sends ErrTimeout,
	// tokenopt.errors.upstream_response and tokenopt.errors.empty_answer.
	ErrCLIFailed error = apperr.Unavailable("tokenopt.errors.cli_failed")
)

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Request struct {
	Kind     string `json:"kind"`
	Question string `json:"question,omitempty"`
	Files    []File `json:"files,omitempty"`
}

func (req Request) Validate() error {
	if err := ValidKind(req.Kind); err != nil {
		return err
	}
	if strings.TrimSpace(req.Question) == "" {
		return apperr.Invalid("tokenopt.errors.question_required")
	}
	if len(req.Files) == 0 {
		return apperr.Invalid("tokenopt.errors.files_required")
	}
	if len(req.Files) > MaxFiles {
		return apperr.Invalid("tokenopt.errors.too_many_files", "max", MaxFiles)
	}
	for _, f := range req.Files {
		if strings.TrimSpace(f.Path) == "" {
			return apperr.Invalid("tokenopt.errors.path_required")
		}
	}
	return nil
}

func ValidKind(kind string) error {
	switch kind {
	case KindRead:
		return nil
	case kindWrite:
		return ErrWriteRemoved
	}
	return apperr.Invalid("tokenopt.errors.invalid_kind", "kind", kind)
}

// Built by the API layer with the LLM gateway's rules (upstream, credential header, internal
// hosts); keep them in step.
type Target struct {
	Provider   string
	BaseURL    string
	Credential string
	Bearer     bool
	Headers    map[string]string
	// Lets the upstream resolve to an internal address; only for upstreams the operator vouched
	// for.
	AllowPrivate bool
}

// The daemon's Sender must go through the LLM gateway's guarded transport, which refuses internal
// addresses unless allowPrivate.
type Sender func(ctx context.Context, req *http.Request, allowPrivate bool) (*http.Response, error)

type Answer struct {
	Text          string
	Model         string
	InputTokens   int64
	OutputTokens  int64
	CostUSD       float64
	RequestBytes  int64
	ResponseBytes int64
}

type Worker struct {
	Send    Sender
	Timeout time.Duration
}

func (w Worker) Call(ctx context.Context, t Target, model string, req Request) (Answer, error) {
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, url, err := buildBody(t, model, req)
	if err != nil {
		return Answer{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Answer{}, apperr.WrapKind(apperr.KindUnavailable, err, "tokenopt.errors.upstream_unreachable")
	}
	setHeaders(httpReq, t)
	resp, err := w.Send(ctx, httpReq, t.AllowPrivate)
	if err != nil {
		return Answer{}, sendError(ctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Answer{}, sendError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Answer{}, apperr.Unavailable("tokenopt.errors.upstream_status",
			"status", resp.StatusCode, "message", providerMessage(raw))
	}
	ans, err := parseAnswer(t.Provider, raw)
	if err != nil {
		return Answer{}, err
	}
	if ans.Model == "" {
		ans.Model = model
	}
	ans.RequestBytes = int64(len(body))
	ans.ResponseBytes = int64(len(raw))
	return ans, nil
}

func sendError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return apperr.WrapKind(apperr.KindUnavailable, err, "tokenopt.errors.upstream_unreachable")
}

func buildBody(t Target, model string, req Request) ([]byte, string, error) {
	maxOut := MaxOutputTokens
	system, user := Instructions(), UserMessage(req)
	base := strings.TrimRight(t.BaseURL, "/")
	var payload map[string]any
	var url string
	switch t.Provider {
	case ProviderAnthropic:
		url = base + "/v1/messages"
		payload = map[string]any{
			"model":       model,
			"max_tokens":  maxOut,
			"temperature": temperature,
			"system":      system,
			"messages":    []map[string]string{{"role": "user", "content": user}},
		}
	case ProviderOpenAI, ProviderOpenAICompatible:
		url = base + "/chat/completions"
		payload = map[string]any{
			"model": model,
			"messages": []map[string]string{
				{"role": "system", "content": system},
				{"role": "user", "content": user},
			},
		}
		if t.Provider == ProviderOpenAI {
			// OpenAI's newer models refuse max_tokens and need max_completion_tokens; its reasoning
			// models refuse any temperature but 1.
			payload["max_completion_tokens"] = maxOut
			if !openAIReasoningModel(model) {
				payload["temperature"] = temperature
			}
		} else {
			// Chat Completions clones (vLLM, Ollama, DeepSeek, OpenRouter) read max_tokens.
			payload["max_tokens"] = maxOut
			payload["temperature"] = temperature
		}
	default:
		return nil, "", apperr.Wrap(ErrProviderUnsupported, "tokenopt.errors.provider_named", "provider", t.Provider)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", apperr.Wrap(err, "tokenopt.errors.encode_request")
	}
	return body, url, nil
}

func openAIReasoningModel(model string) bool {
	m := strings.ToLower(model)
	for _, p := range []string{"o1", "o3", "o4", "gpt-5"} {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

func setHeaders(r *http.Request, t Target) {
	r.Header.Set("Content-Type", "application/json")
	switch {
	case t.Provider == ProviderAnthropic && !t.Bearer:
		r.Header.Set("x-api-key", t.Credential)
	default:
		r.Header.Set("Authorization", "Bearer "+t.Credential) //i18n:ignore HTTP auth scheme
	}
	if t.Provider == ProviderAnthropic {
		r.Header.Set("anthropic-version", anthropicVersion)
	}
	for k, v := range t.Headers {
		r.Header.Set(k, v)
	}
}

func parseAnswer(provider string, raw []byte) (Answer, error) {
	var ans Answer
	switch provider {
	case ProviderAnthropic:
		var m struct {
			Model   string `json:"model"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				InputTokens              int64 `json:"input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return Answer{}, apperr.Unavailable("tokenopt.errors.upstream_response")
		}
		var b strings.Builder
		for _, c := range m.Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		ans.Text, ans.Model = b.String(), m.Model
		ans.InputTokens = m.Usage.InputTokens + m.Usage.CacheReadInputTokens + m.Usage.CacheCreationInputTokens
		ans.OutputTokens = m.Usage.OutputTokens
	default:
		var m struct {
			Model   string `json:"model"`
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int64   `json:"prompt_tokens"`
				CompletionTokens int64   `json:"completion_tokens"`
				Cost             float64 `json:"cost"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return Answer{}, apperr.Unavailable("tokenopt.errors.upstream_response")
		}
		if len(m.Choices) > 0 {
			ans.Text = m.Choices[0].Message.Content
		}
		ans.Model = m.Model
		ans.InputTokens, ans.OutputTokens = m.Usage.PromptTokens, m.Usage.CompletionTokens
		ans.CostUSD = m.Usage.Cost
	}
	if strings.TrimSpace(ans.Text) == "" {
		return Answer{}, apperr.Unavailable("tokenopt.errors.empty_answer")
	}
	return ans, nil
}

// Safe to surface: the provider's error.message describes the request, never echoes it.
func providerMessage(raw []byte) string {
	var m struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Error.Message == "" {
		return ""
	}
	msg := m.Error.Message
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}
