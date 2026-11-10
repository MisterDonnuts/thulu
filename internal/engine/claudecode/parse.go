package claudecode

import (
	"bufio"
	"encoding/json"
	"io"
	"iter"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

type streamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Model     string `json:"model"`
	SessionID string `json:"session_id"`

	Mode string `json:"mode"`

	Result     string  `json:"result"`
	IsError    bool    `json:"is_error"`
	DurationMs int64   `json:"duration_ms"`
	NumTurns   int     `json:"num_turns"`
	CostUSD    float64 `json:"total_cost_usd"`
	StopReason string  `json:"stop_reason"`
	// Older CLIs omit both and only write "API Error: 529 …" in result.
	// Kept raw because the status may be a string or null, which would drop the whole line.
	TerminalReason string          `json:"terminal_reason"`
	APIErrorStatus json.RawMessage `json:"api_error_status"`

	Usage *rawUsage `json:"usage"`

	// Claude Code puts usage both here and at top level depending on the event.
	// Content stays raw: a user message carries a string or an array of blocks, and a fixed shape
	// would drop the line.
	Message *struct {
		ID    string          `json:"id"`
		Model string          `json:"model"`
		Usage *rawUsage       `json:"usage"`
		Cont  json.RawMessage `json:"content"`
	} `json:"message"`

	ParentToolUseID string `json:"parent_tool_use_id"`
	IsSynthetic     bool   `json:"isSynthetic"`

	ModelUsage map[string]rawModelUsage `json:"modelUsage"`

	// Some Claude Code versions emit tool_use as a top-level event instead of inside
	// message.content.
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`

	RateLimitInfo *rawRateLimit `json:"rate_limit_info"`
}

type rawContent struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Data      string          `json:"data"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   *bool           `json:"is_error"`
}

type rawUsage struct {
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	ServiceTier              string `json:"service_tier"`
	CacheCreation            *struct {
		Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

func (u *rawUsage) normalize() *engine.Usage {
	if u == nil {
		return nil
	}
	out := &engine.Usage{
		InputTokens:   u.InputTokens,
		OutputTokens:  u.OutputTokens,
		CacheRead:     u.CacheReadInputTokens,
		CacheCreation: u.CacheCreationInputTokens,
		ServiceTier:   u.ServiceTier,
	}
	if u.CacheCreation != nil {
		out.CacheCreation1h = u.CacheCreation.Ephemeral1h
	}
	return out
}

type rawModelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

func modelUsage(m map[string]rawModelUsage) []engine.ModelUsage {
	if len(m) == 0 {
		return nil
	}
	out := make([]engine.ModelUsage, 0, len(m))
	for name, u := range m {
		out = append(out, engine.ModelUsage{
			Model:         name,
			InputTokens:   u.InputTokens,
			OutputTokens:  u.OutputTokens,
			CacheRead:     u.CacheReadInputTokens,
			CacheCreation: u.CacheCreationInputTokens,
			CostUSD:       u.CostUSD,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

type rawRateLimit struct {
	Status        string  `json:"status"`
	ResetsAt      int64   `json:"resetsAt"`
	RateLimitType string  `json:"rateLimitType"`
	Utilization   float64 `json:"utilization"`
	IsOverage     bool    `json:"isUsingOverage"`
}

func (r *rawRateLimit) normalize() *engine.RateLimit {
	if r == nil {
		return nil
	}
	return &engine.RateLimit{
		Status:        r.Status,
		ResetsAt:      r.ResetsAt,
		RateLimitType: r.RateLimitType,
		Utilization:   r.Utilization,
		IsOverage:     r.IsOverage,
	}
}

// 16 MiB: a single result line can carry embedded artifacts.
func parseStream(r io.Reader) iter.Seq[engine.Event] {
	return func(yield func(engine.Event) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		mode := ""
		spent := map[string]float64{}
		for sc.Scan() {
			line := sc.Text()
			// Strip Docker `--timestamps` prefixes so the same parser works on raw stdout and
			// persisted logs.
			idx := strings.IndexByte(line, '{')
			if idx < 0 {
				continue
			}
			ts := engine.LineTimestamp(line[:idx])
			line = line[idx:]
			var ev streamEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			if ev.Type == engine.SessionLineType {
				mode = ev.Mode
				continue
			}
			if ev.Type == "result" {
				ev.CostUSD = costSince(spent, ev.SessionID, ev.CostUSD)
			}
			stamped := func(e engine.Event) bool {
				e.TS = ts
				return yield(e)
			}
			if !yieldEvents(&ev, mode, stamped) {
				return
			}
		}
	}
}

// total_cost_usd is the process's running total, repeated on each result (one per answered message,
// again when a background subagent finishes).
// Return only the delta; a total below the last one is a new process resuming the session.
func costSince(spent map[string]float64, sessionID string, total float64) float64 {
	last, seen := spent[sessionID]
	spent[sessionID] = total
	if !seen || total < last {
		return total
	}
	return total - last
}

func yieldEvents(ev *streamEvent, mode string, yield func(engine.Event) bool) bool {
	switch {
	case ev.Type == "system" && ev.Subtype == "init" && ev.Model != "":
		return yield(engine.Event{Kind: engine.EventInit, Model: ev.Model, SessionID: ev.SessionID, SessionMode: mode})

	case ev.Type == "assistant" && ev.Message != nil:
		return yieldAssistantEvents(ev, yield)

	case ev.Type == "user" && ev.Message != nil:
		return yieldUserEvents(ev, yield)

	case ev.Type == "tool_use" && ev.Name != "":
		return yield(engine.Event{Kind: engine.EventToolUse, ToolName: ev.Name, ToolInput: ev.Input})

	case ev.Type == "rate_limit_event" && ev.RateLimitInfo != nil:
		return yield(engine.Event{Kind: engine.EventRateLimit, RateLimit: ev.RateLimitInfo.normalize()})

	case ev.Type == "result":
		apiErr, status := resultAPIError(ev)
		return yield(engine.Event{
			Kind:           engine.EventResult,
			Result:         ev.Result,
			IsError:        ev.IsError,
			Outcome:        resultOutcome(ev),
			Usage:          ev.Usage.normalize(),
			DurationMs:     ev.DurationMs,
			NumTurns:       ev.NumTurns,
			CostUSD:        ev.CostUSD,
			StopReason:     ev.StopReason,
			SessionID:      ev.SessionID,
			APIError:       apiErr,
			APIErrorStatus: status,
			ModelUsage:     modelUsage(ev.ModelUsage),
		})
	}
	return true
}

//i18n:ignore the CLI's wire text, matched, not shown
var apiErrorInText = regexp.MustCompile(`API Error: (\d{3})\b`)

func resultAPIError(ev *streamEvent) (bool, int) {
	if !ev.IsError {
		return false, 0
	}
	status := 0
	raw := strings.Trim(strings.TrimSpace(string(ev.APIErrorStatus)), `"`)
	if n, err := strconv.Atoi(raw); err == nil && n >= 100 && n <= 599 {
		status = n
	}
	if m := apiErrorInText.FindStringSubmatch(ev.Result); status == 0 && m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			status = n
		}
	}
	return ev.TerminalReason == "api_error" || status != 0, status
}

// is_error flags an API failure even under subtype "success", so it counts as an error regardless
// of subtype.
func resultOutcome(ev *streamEvent) string {
	switch {
	case ev.Subtype == "error_max_turns":
		return engine.OutcomeMaxTurns
	case ev.IsError, strings.HasPrefix(ev.Subtype, "error"):
		return engine.OutcomeError
	default:
		return engine.OutcomeOK
	}
}

// Claude Code prints one line per content block and repeats the message's usage on each; consumers
// summing usage must dedupe on MessageID.
func yieldAssistantEvents(ev *streamEvent, yield func(engine.Event) bool) bool {
	blocks := contentBlocks(ev.Message.Cont)
	out := 0
	for i := range blocks {
		out += outputBytes(&blocks[i])
	}
	if ev.Message.Usage != nil {
		if !yield(engine.Event{
			Kind:            engine.EventAssistant,
			Usage:           ev.Message.Usage.normalize(),
			MessageID:       ev.Message.ID,
			Model:           ev.Message.Model,
			ParentToolUseID: ev.ParentToolUseID,
			OutputBytes:     out,
		}) {
			return false
		}
	}
	// Tool uses must follow the assistant usage so the collector can attribute that turn's cost to
	// them.
	for i := range blocks {
		c := &blocks[i]
		if c.Type != "tool_use" || c.Name == "" {
			continue
		}
		if !yield(engine.Event{
			Kind:            engine.EventToolUse,
			ToolName:        c.Name,
			ToolInput:       c.Input,
			ToolUseID:       c.ID,
			MessageID:       ev.Message.ID,
			ParentToolUseID: ev.ParentToolUseID,
		}) {
			return false
		}
	}
	return true
}

func yieldUserEvents(ev *streamEvent, yield func(engine.Event) bool) bool {
	var text string
	if json.Unmarshal(ev.Message.Cont, &text) == nil {
		if text == "" {
			return true
		}
		return yield(engine.Event{
			Kind: engine.EventUserMessage, ResultBytes: len(text),
			ParentToolUseID: ev.ParentToolUseID, Synthetic: ev.IsSynthetic,
		})
	}
	texts := 0
	blocks := contentBlocks(ev.Message.Cont)
	for i := range blocks {
		c := &blocks[i]
		switch c.Type {
		case "tool_result":
			size, tail := resultText(c.Content)
			if !yield(engine.Event{
				Kind:            engine.EventToolResult,
				ToolUseID:       c.ToolUseID,
				ParentToolUseID: ev.ParentToolUseID,
				ResultBytes:     size,
				ResultTail:      tail,
				IsError:         c.IsError != nil && *c.IsError,
			}) {
				return false
			}
		case "text":
			texts += len(c.Text)
		}
	}
	if texts > 0 {
		return yield(engine.Event{Kind: engine.EventUserMessage, ResultBytes: texts, ParentToolUseID: ev.ParentToolUseID, Synthetic: ev.IsSynthetic})
	}
	return true
}

func contentBlocks(raw json.RawMessage) []rawContent {
	var blocks []rawContent
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	return blocks
}

// Signatures are excluded: the API reads them back but does not bill them as prompt text.
func outputBytes(c *rawContent) int {
	switch c.Type {
	case "text":
		return len(c.Text)
	case "thinking":
		return len(c.Thinking)
	case "redacted_thinking":
		return len(c.Data)
	case "tool_use":
		return len(c.Name) + inputBytes(c.Input)
	}
	return 0
}

func inputBytes(in map[string]any) int {
	if len(in) == 0 {
		return 0
	}
	b, err := json.Marshal(in)
	if err != nil {
		return 0
	}
	return len(b)
}

// Images are billed by pixels, not base64 size: a screenshot is ~1,500 tokens, ~6,000 bytes at four
// bytes a token.
const imageBytes = 6000

func resultText(raw json.RawMessage) (size int, tail string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return len(s), lastBytes(s, engine.ResultTailBytes)
	}
	var b strings.Builder
	blocks := contentBlocks(raw)
	for i := range blocks {
		c := &blocks[i]
		switch c.Type {
		case "text":
			size += len(c.Text)
			b.WriteString(c.Text)
		case "image":
			size += imageBytes
		}
	}
	return size, lastBytes(b.String(), engine.ResultTailBytes)
}

func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for i := 0; i < len(s) && i < 4; i++ {
		if utf8.RuneStart(s[i]) {
			return s[i:]
		}
	}
	return s
}
