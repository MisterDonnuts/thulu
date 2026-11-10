package task

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

const (
	engineMessageMax = 300
	crashLogLines    = 3
)

func (d *Dispatcher) engineFailureNotice(workspaceID, agentName, runID, outputDir string) Note {
	ev, ok := d.lastEngineResult(workspaceID, agentName, runID, outputDir)
	if !ok {
		return Note{}
	}
	return engineFailureText(ev)
}

func (d *Dispatcher) lastEngineResult(workspaceID, agentName, runID, outputDir string) (engine.Event, bool) {
	preferred := d.runEngineName(workspaceID, agentName, runID)
	if ev, ok := engine.LastResult(readSessionLogTail(outputDir), preferred); ok {
		return ev, true
	}
	reader, canRead := d.launcher.(RunLogReader)
	if !canRead || runID == "" {
		return engine.Event{}, false
	}
	logs := reader.RunLogs(runID)
	if over := len(logs) - int(sessionLogTail); over > 0 {
		logs = logs[over:]
	}
	return engine.LastResult([]byte(logs), preferred)
}

// A run that failed before doing anything: it never started, or the provider refused it. Running out
// of turns or a crashing tool do not count, those leave work an auditor can judge.
func (d *Dispatcher) engineNeverWorked(workspaceID, agentName, runID, outputDir string, exitCode int) bool {
	if exitCode == 0 {
		return false
	}
	if runFailureHint(exitCode) != nil {
		return true
	}
	ev, ok := d.lastEngineResult(workspaceID, agentName, runID, outputDir)
	return ok && ev.IsError && ev.APIError
}

func keyRefusal(ev engine.Event) int {
	if ev.IsError && ev.APIError && (ev.APIErrorStatus == 401 || ev.APIErrorStatus == 403) {
		return ev.APIErrorStatus
	}
	return 0
}

func engineFailureText(ev engine.Event) Note {
	if !ev.IsError || !ev.APIError {
		return Note{}
	}
	msg := cleanEngineMessage(ev.Result)
	switch {
	case ev.APIErrorStatus == 401 || ev.APIErrorStatus == 403:
		return NoteOf("task.comments.run_engine_auth_failed", "status", ev.APIErrorStatus, "message", msg)
	case ev.APIErrorStatus != 0:
		return NoteOf("task.comments.run_engine_api_error", "status", ev.APIErrorStatus, "message", msg)
	default:
		return NoteOf("task.comments.run_engine_api_failed", "message", msg)
	}
}

var (
	longToken      = regexp.MustCompile(`[A-Za-z0-9_\-+/=.]{32,}`)
	apiErrorPrefix = regexp.MustCompile(`(?i)\bAPI Error:\s*\d{3}\s*`)
	// What reqwest appends to codex's errors: the gateway's address and request ids, of no use on a task.
	requestDetails = regexp.MustCompile(`(?i),\s*url:\s.*$`)
	// Provider messages can echo credentials back; redact them before the message lands on a task.
	secretShapes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bbearer\s+\S+`),
		regexp.MustCompile(`\b(sk|pk|rk)-[A-Za-z0-9_-]{6,}`),
		longToken,
	}
	whitespace = regexp.MustCompile(`\s+`)
)

// The part of a URL after "https:". Providers send a billing or docs link people need; every piece
// of a path too short to be a key stays.
func plainURLTail(tok string) bool {
	rest, ok := strings.CutPrefix(tok, "//")
	if !ok || strings.Contains(rest, "=") {
		return false
	}
	for _, piece := range strings.Split(rest, "/") {
		if len(piece) >= 32 {
			return false
		}
	}
	return true
}

func cleanEngineMessage(s string) string {
	s = apiErrorPrefix.ReplaceAllString(s, "")
	s = requestDetails.ReplaceAllString(s, "")
	if i := strings.IndexByte(s, '{'); i >= 0 {
		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(s[i:]), &body) == nil && body.Error.Message != "" {
			s = strings.TrimSpace(s[:i] + " " + body.Error.Message)
		}
	}
	for _, re := range secretShapes[:len(secretShapes)-1] {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	s = longToken.ReplaceAllStringFunc(s, func(tok string) string {
		if plainURLTail(tok) {
			return tok
		}
		return "[redacted]"
	})
	s = strings.TrimSpace(whitespace.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > engineMessageMax {
		s = string(r[:engineMessageMax]) + "…"
	}
	s = strings.TrimRight(s, ". ")
	if s == "" {
		return "?"
	}
	return s
}

// outputDir is written by the agent container; open through safepath (os.Root) so a planted symlink
// can't redirect the read.
func readSessionLogTail(outputDir string) []byte {
	if outputDir == "" {
		return nil
	}
	f, err := safepath.Open(outputDir, "session.log")
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	if off := st.Size() - sessionLogTail; off > 0 {
		if _, seekErr := f.Seek(off, io.SeekStart); seekErr != nil {
			return nil
		}
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	return buf
}

func (d *Dispatcher) runFailureNote(ctx context.Context, workspaceID, agentName, runID, outputDir string, exitCode int) Note {
	if exitCode == 0 {
		d.recordProfileOutcome(ctx, workspaceID, runID, 0)
		return Note{}
	}
	if hint := runFailureHint(exitCode); hint != nil {
		return NoteOf("task.comments.run_never_started", "hint", hint)
	}
	ev, ok := d.lastEngineResult(workspaceID, agentName, runID, outputDir)
	if !ok {
		return d.engineCrashNote(runID, outputDir)
	}
	if status := keyRefusal(ev); status != 0 {
		d.recordProfileOutcome(ctx, workspaceID, runID, status)
	}
	return engineFailureText(ev)
}

// The engine exited with an error and never reported a result: out of memory, a bad binary, a crash.
// The exit code alone says nothing, so the note quotes what the run printed last.
func (d *Dispatcher) engineCrashNote(runID, outputDir string) Note {
	logs := string(readSessionLogTail(outputDir))
	if reader, canRead := d.launcher.(RunLogReader); canRead && runID != "" {
		if out := reader.RunLogs(runID); out != "" {
			logs = out
		}
	}
	if tail := plainLogTail(logs); tail != "" {
		return NoteOf("task.comments.run_engine_crashed", "message", cleanEngineMessage(tail))
	}
	return NoteOf("task.comments.run_engine_crashed_silent")
}

// Stream events are JSON lines; what a crashing engine prints is the plain text around them.
func plainLogTail(logs string) string {
	var plain []string
	for _, line := range strings.Split(logs, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, `{"type":`) {
			continue
		}
		plain = append(plain, line)
	}
	if over := len(plain) - crashLogLines; over > 0 {
		plain = plain[over:]
	}
	return strings.Join(plain, " | ")
}

func (d *Dispatcher) recordProfileOutcome(ctx context.Context, workspaceID, runID string, status int) {
	if d.llmRefusal == nil || runID == "" {
		return
	}
	v, ok := d.runProfiles.Load(runID)
	if !ok {
		return
	}
	if profileID, isID := v.(string); isID && profileID != "" {
		d.llmRefusal(ctx, workspaceID, profileID, status)
	}
}
