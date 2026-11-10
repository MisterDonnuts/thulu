package opencode

import (
	"bufio"
	"encoding/json"
	"io"
	"iter"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

// Lines without a sessionID are ignored so this parser never claims another engine's stream
// (engine.LastResult tries every parser).
// OpenCode's output excludes reasoning tokens, which are billed as output, so stepUsage adds them
// back; its input already excludes the cache.

type ocEvent struct {
	Type      string  `json:"type"`
	Timestamp int64   `json:"timestamp"`
	SessionID string  `json:"sessionID"`
	Part      *ocPart `json:"part"`
	Error     *ocErr  `json:"error"`
	Mode      string  `json:"mode"`
	Model     string  `json:"model"`
}

type ocPart struct {
	Text   string    `json:"text"`
	Tool   string    `json:"tool"`
	State  *ocState  `json:"state"`
	Reason string    `json:"reason"`
	Tokens *ocTokens `json:"tokens"`
	Cost   float64   `json:"cost"`
}

type ocState struct {
	Status string         `json:"status"`
	Input  map[string]any `json:"input"`
}

type ocTokens struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	Reasoning int `json:"reasoning"`
	Cache     struct {
		Read  int `json:"read"`
		Write int `json:"write"`
	} `json:"cache"`
}

type ocErr struct {
	Name string `json:"name"`
	Data struct {
		Message    string `json:"message"`
		StatusCode int    `json:"statusCode"`
	} `json:"data"`
}

type ocParser struct {
	sessionID string
	mode      string
	model     string
	inited    bool

	stepText string
	lastText string
	reason   string
	steps    int
	stepOpen bool
	total    engine.Usage
	cost     float64

	failed     bool
	errText    string
	errStatus  int
	apiError   bool
	start, end int64
}

func parseStream(r io.Reader) iter.Seq[engine.Event] {
	return func(yield func(engine.Event) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		p := &ocParser{}
		for sc.Scan() {
			ev, ok := decodeLine(sc.Text())
			if !ok {
				continue
			}
			if !p.handle(&ev, yield) {
				return
			}
		}
		if p.inited {
			yield(p.result())
		}
	}
}

func decodeLine(line string) (ocEvent, bool) {
	idx := strings.IndexByte(line, '{')
	if idx < 0 {
		return ocEvent{}, false
	}
	var ev ocEvent
	if err := json.Unmarshal([]byte(line[idx:]), &ev); err != nil {
		return ocEvent{}, false
	}
	return ev, true
}

func (p *ocParser) handle(ev *ocEvent, yield func(engine.Event) bool) bool {
	if ev.Type == engine.SessionLineType {
		p.mode, p.model = ev.Mode, ev.Model
		return true
	}
	if ev.SessionID == "" {
		return true
	}
	if !p.inited {
		p.inited = true
		p.sessionID = ev.SessionID
		p.start = ev.Timestamp
		if !yield(engine.Event{Kind: engine.EventInit, Model: p.model, SessionID: p.sessionID, SessionMode: p.mode}) {
			return false
		}
	}
	if ev.Timestamp > 0 {
		p.end = ev.Timestamp
	}
	switch ev.Type {
	case "step_start":
		p.stepOpen = true
		p.stepText = ""
	case "text":
		if ev.Part != nil && strings.TrimSpace(ev.Part.Text) != "" {
			if p.stepText != "" {
				p.stepText += "\n\n"
			}
			p.stepText += ev.Part.Text
		}
	case "step_finish":
		return p.finishStep(ev.Part, yield)
	case "tool_use":
		if ev.Part == nil || ev.Part.Tool == "" {
			return true
		}
		var input map[string]any
		if ev.Part.State != nil {
			input = ev.Part.State.Input
		}
		return yield(engine.Event{Kind: engine.EventToolUse, ToolName: ev.Part.Tool, ToolInput: input})
	case "error":
		p.fail(ev.Error)
	}
	return true
}

func (p *ocParser) finishStep(part *ocPart, yield func(engine.Event) bool) bool {
	p.stepOpen = false
	p.steps++
	p.failed = false
	if p.stepText != "" {
		p.lastText = p.stepText
	}
	if part == nil {
		return true
	}
	p.reason = part.Reason
	p.cost += part.Cost
	u := stepUsage(part.Tokens)
	if u == nil {
		return yield(engine.Event{Kind: engine.EventAssistant})
	}
	p.total.InputTokens += u.InputTokens
	p.total.OutputTokens += u.OutputTokens
	p.total.CacheRead += u.CacheRead
	p.total.CacheCreation += u.CacheCreation
	return yield(engine.Event{Kind: engine.EventAssistant, Usage: u})
}

func (p *ocParser) fail(e *ocErr) {
	p.failed = true
	if e == nil {
		return
	}
	switch {
	case e.Data.Message != "":
		p.errText = e.Data.Message
	case e.Name != "":
		p.errText = e.Name
	}
	p.errStatus = e.Data.StatusCode
	p.apiError = e.Name == "APIError" || e.Data.StatusCode > 0
}

func (p *ocParser) result() engine.Event {
	usage := p.total
	ev := engine.Event{
		Kind:       engine.EventResult,
		SessionID:  p.sessionID,
		Result:     p.lastText,
		Outcome:    engine.OutcomeOK,
		Usage:      &usage,
		CostUSD:    p.cost,
		NumTurns:   p.steps,
		StopReason: p.reason,
	}
	if p.end > p.start && p.start > 0 {
		ev.DurationMs = p.end - p.start
	}
	if p.failed || p.stepOpen {
		ev.Outcome = engine.OutcomeError
		ev.IsError = true
		if p.errText != "" {
			ev.Result = p.errText
		}
		ev.APIError, ev.APIErrorStatus = p.apiError, p.errStatus
	}
	return ev
}

func stepUsage(t *ocTokens) *engine.Usage {
	if t == nil {
		return nil
	}
	return &engine.Usage{
		InputTokens:   t.Input,
		OutputTokens:  t.Output + t.Reasoning,
		CacheRead:     t.Cache.Read,
		CacheCreation: t.Cache.Write,
	}
}
