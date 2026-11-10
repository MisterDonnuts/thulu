package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"iter"
	"regexp"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

// codex prints no result frame; one EventResult is synthesized at EOF.
// An item of type "error" is often a non-fatal warning (unknown model metadata) and does not fail
// the run.

type codexEvent struct {
	Type     string      `json:"type"`
	ThreadID string      `json:"thread_id"`
	Model    string      `json:"model"`
	Item     *codexItem  `json:"item"`
	Usage    *codexUsage `json:"usage"`
	// A string on older frames, {"message":…} on turn.failed; a fixed type would drop one of them.
	Error   json.RawMessage `json:"error"`
	Message string          `json:"message"`
	Mode    string          `json:"mode"`
}

func (ev *codexEvent) errorText() string {
	if ev.Message != "" {
		return ev.Message
	}
	if len(ev.Error) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(ev.Error, &s) == nil {
		return s
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(ev.Error, &obj) == nil {
		return obj.Message
	}
	return ""
}

type codexItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

type codexUsage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
}

type codexParser struct {
	sessionID string
	mode      string
	model     string
	lastText  string
	errText   string
	total     engine.Usage
	// A later turn.completed clears failed: codex reports recoverable trouble (a reconnected
	// stream) as an error, then finishes the turn.
	failed   bool
	turnOpen bool
	saw      bool
}

func parseStream(r io.Reader) iter.Seq[engine.Event] {
	return func(yield func(engine.Event) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		p := &codexParser{}

		for sc.Scan() {
			ev, ok := decodeLine(sc.Text())
			if !ok {
				continue
			}
			if !p.handle(&ev, yield) {
				return
			}
		}
		if p.saw {
			yield(p.result())
		}
	}
}

func (p *codexParser) result() engine.Event {
	usage := p.total
	outcome := engine.OutcomeOK
	text := p.lastText
	if p.failed || p.turnOpen {
		outcome = engine.OutcomeError
		if p.errText != "" {
			text = p.errText
		}
	}
	ev := engine.Event{
		Kind:      engine.EventResult,
		Result:    text,
		IsError:   outcome == engine.OutcomeError,
		Outcome:   outcome,
		Usage:     &usage,
		SessionID: p.sessionID,
	}
	// codex names the provider's refusal in errText, with or without a status ("You have no credits
	// remaining" has none); a turn killed mid-way has no text and is not the provider's doing.
	if ev.IsError && p.failed && p.errText != "" {
		ev.APIError = true
		if m := statusInError.FindStringSubmatch(text); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				ev.APIErrorStatus = n
			}
		}
	}
	return ev
}

//i18n:ignore codex's wire text, matched, not shown
var statusInError = regexp.MustCompile(`unexpected status (\d{3})\b`)

func decodeLine(line string) (codexEvent, bool) {
	idx := strings.IndexByte(line, '{')
	if idx < 0 {
		return codexEvent{}, false
	}
	var ev codexEvent
	if err := json.Unmarshal([]byte(line[idx:]), &ev); err != nil {
		return codexEvent{}, false
	}
	return ev, true
}

func (p *codexParser) handle(ev *codexEvent, yield func(engine.Event) bool) bool {
	switch ev.Type {
	case engine.SessionLineType:
		p.mode = ev.Mode
		p.model = ev.Model

	case "thread.started", "session.created":
		p.saw = true
		if ev.ThreadID != "" {
			p.sessionID = ev.ThreadID
		}
		model := ev.Model
		if model == "" {
			model = p.model
		}
		return yield(engine.Event{Kind: engine.EventInit, Model: model, SessionID: p.sessionID, SessionMode: p.mode})

	case "turn.started":
		p.saw = true
		p.turnOpen = true

	case "turn.completed", "turn.complete":
		p.saw = true
		p.turnOpen = false
		p.failed = false
		u := turnUsage(ev.Usage)
		if u != nil {
			p.total.InputTokens += u.InputTokens
			p.total.OutputTokens += u.OutputTokens
			p.total.CacheRead += u.CacheRead
		}
		return yield(engine.Event{Kind: engine.EventAssistant, Usage: u})

	case "turn.failed":
		p.saw = true
		p.turnOpen = false
		p.fail(ev.errorText())

	case "item.completed", "item.complete":
		return p.handleItem(ev.Item, yield)

	case "error":
		p.saw = true
		p.fail(ev.errorText())
	}
	return true
}

func (p *codexParser) fail(msg string) {
	p.failed = true
	if msg != "" {
		p.errText = msg
	}
}

func (p *codexParser) handleItem(item *codexItem, yield func(engine.Event) bool) bool {
	if item == nil {
		return true
	}
	p.saw = true
	switch item.Type {
	case "assistant_message", "agent_message", "message":
		if item.Text != "" {
			p.lastText = item.Text
		}
	case "command_execution", "local_shell_call", "function_call", "tool_call":
		name := item.Name
		if name == "" {
			name = "shell"
		}
		return yield(engine.Event{Kind: engine.EventToolUse, ToolName: name})
	}
	return true
}

// OpenAI counts cached tokens inside input_tokens; engine.Usage excludes them, so subtract to avoid
// double-counting in PromptSize and pricing.
func turnUsage(u *codexUsage) *engine.Usage {
	if u == nil {
		return nil
	}
	return &engine.Usage{
		InputTokens:  max(u.InputTokens-u.CachedInputTokens, 0),
		OutputTokens: u.OutputTokens,
		CacheRead:    u.CachedInputTokens,
	}
}
