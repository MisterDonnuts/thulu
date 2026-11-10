package task

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type TranscriptRun struct {
	Stage     string
	StartedAt time.Time
	Engine    string
	Open      func() (io.ReadCloser, error)
}

// A session.log is usually a few MB; past 64 MiB it is a runaway and the capped transcript has no
// use for the rest.
const maxTranscriptRunLog = 64 << 20

const maxToolLine = 160

// Empty means nothing to tell: the caller answers 404, which the entrypoint reads as "no
// transcript".
func BuildTranscript(agent string, runs []TranscriptRun, msgs []Message, maxBytes int) string {
	type entry struct {
		at   time.Time
		text string
	}
	var entries []entry
	// Newest run first, stopping at the cap: otherwise a repeated request keeps the daemon parsing
	// logs the cut would drop.
	order := make([]int, len(runs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return runs[order[a]].StartedAt.After(runs[order[b]].StartedAt) })
	rendered := 0
	for _, i := range order {
		if maxBytes > 0 && rendered >= maxBytes {
			break
		}
		if text := renderTranscriptRun(&runs[i]); text != "" {
			entries = append(entries, entry{runs[i].StartedAt, text})
			rendered += len(text)
		}
	}
	for i := range msgs {
		m := &msgs[i]
		if m.Role != RoleUser || strings.TrimSpace(m.Body) == "" || (m.Agent != "" && m.Agent != agent) {
			continue
		}
		author := m.Author
		if author == "" {
			author = i18n.Text("task.prompts.someone")
		}
		entries = append(entries, entry{m.CreatedAt, i18n.Text("task.sessions.transcript.person_message",
			"author", author, "at", m.CreatedAt.UTC().Format(time.RFC3339), "body", strings.TrimSpace(m.Body))})
	}
	if len(entries) == 0 {
		return ""
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })

	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.text
	}
	title := i18n.Text("task.sessions.transcript.title", "agent", agent)
	return capTranscript(title, strings.Join(parts, "\n\n"), maxBytes)
}

func renderTranscriptRun(run *TranscriptRun) string {
	if run.Open == nil {
		return ""
	}
	rc, err := run.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	in := io.LimitReader(rc, maxTranscriptRunLog)
	eng, err := engine.Get(run.Engine)
	if err != nil {
		logx.Warn(err, "task.sessions.log.transcript_engine", "engine", run.Engine)
		return ""
	}
	var lines []string
	for ev := range eng.ParseStream(in) {
		switch ev.Kind {
		case engine.EventToolUse:
			lines = append(lines, toolLine(ev.ToolName, ev.ToolInput))
		case engine.EventResult:
			if res := strings.TrimSpace(ev.Result); res != "" {
				lines = append(lines, "", i18n.Text("task.sessions.transcript.result", "text", res))
			}
		case engine.EventUnknown, engine.EventInit, engine.EventAssistant, engine.EventRateLimit,
			engine.EventToolResult, engine.EventUserMessage:
		}
	}
	if len(lines) == 0 {
		return ""
	}
	head := i18n.Text("task.sessions.transcript.run_heading",
		"stage", run.Stage, "at", run.StartedAt.UTC().Format(time.RFC3339))
	return head + "\n\n" + strings.Join(lines, "\n")
}

func toolLine(name string, input map[string]any) string {
	if name == "" {
		name = "?"
	}
	hint := engine.ToolHint(input, maxToolLine)
	if hint == "" {
		return fmt.Sprintf("- `%s`", name)
	}
	return fmt.Sprintf("- `%s`: %s", name, strings.ReplaceAll(hint, "`", "'"))
}

func capTranscript(title, body string, maxBytes int) string {
	full := title + "\n\n" + body + "\n"
	if maxBytes <= 0 || len(full) <= maxBytes {
		return full
	}
	note := i18n.Text("task.sessions.transcript.truncated") + "\n\n"
	room := maxBytes - len(title) - len("\n\n") - len(note) - len("\n")
	if room <= 0 {
		return clipTail(full, maxBytes)
	}
	tail := body[len(body)-room:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	} else {
		tail = clipTail(tail, len(tail))
	}
	return title + "\n\n" + note + tail + "\n"
}

func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for s != "" && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}
