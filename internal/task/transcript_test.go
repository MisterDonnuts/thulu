package task

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func logRun(stage Stage, at time.Time, log string) TranscriptRun {
	return TranscriptRun{
		Stage: string(stage), StartedAt: at,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(log)), nil },
	}
}

const claudeRunLog = `{"type":"system","subtype":"init","session_id":"s1","model":"m"}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go   test\n ./..."}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"internal/x.go","old_string":"a"}}]}}
{"type":"result","subtype":"success","result":"Tests pass; I need to know which DB to use.","session_id":"s1"}
`

func TestBuildTranscriptMergesRunsAndMessagesOldestFirst(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	runs := []TranscriptRun{
		logRun(types.StageInProgress, t0, claudeRunLog),
		{Stage: "in_progress", StartedAt: t0, Open: func() (io.ReadCloser, error) { return nil, errors.New("gone") }},
	}
	msgs := []Message{
		{Role: RoleUser, Author: "ana", Body: "use Postgres", CreatedAt: t0.Add(time.Minute)},
		{Role: RoleUser, Agent: "reviewer", Author: "ana", Body: "esto es para otro", CreatedAt: t0.Add(2 * time.Minute)},
		{Role: RoleUser, Agent: "developer", Body: "and add the test", CreatedAt: t0.Add(3 * time.Minute)},
	}
	got := BuildTranscript("developer", runs, msgs, 0)

	for _, want := range []string{"developer", "`Bash`: go test ./...", "`Edit`: internal/x.go",
		"Tests pass; I need to know which DB to use.", "use Postgres", "and add the test"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "esto es para otro") {
		t.Errorf("a message for another agent leaked in:\n%s", got)
	}
	if strings.Index(got, "`Bash`") > strings.Index(got, "use Postgres") ||
		strings.Index(got, "use Postgres") > strings.Index(got, "and add the test") {
		t.Errorf("entries out of order:\n%s", got)
	}
	if BuildTranscript("developer", nil, nil, 0) != "" {
		t.Error("an empty transcript must be empty, for the 404")
	}
}

func TestBuildTranscriptKeepsTheMostRecentPart(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	msgs := []Message{{Role: RoleUser, Author: "ana", Body: "EL PRIMERO", CreatedAt: t0.Add(-time.Minute)}}
	for i := range 200 {
		msgs = append(msgs, Message{Role: RoleUser, Author: "ana",
			Body:      "mensaje ñandú número " + strings.Repeat("x", i%7) + string(rune('A'+i%26)),
			CreatedAt: t0.Add(time.Duration(i) * time.Minute)})
	}
	msgs = append(msgs, Message{Role: RoleUser, Author: "ana", Body: "EL ÚLTIMO", CreatedAt: t0.Add(time.Hour * 24)})
	const maxBytes = 2048
	got := BuildTranscript("developer", nil, msgs, maxBytes)

	if len(got) > maxBytes {
		t.Errorf("len = %d, want at most %d", len(got), maxBytes)
	}
	if !utf8.ValidString(got) {
		t.Error("the cut split a rune")
	}
	if !strings.Contains(got, "EL ÚLTIMO") {
		t.Error("the newest message was cut")
	}
	if strings.Contains(got, "EL PRIMERO") {
		t.Error("the oldest message survived the cut")
	}
	if !strings.HasPrefix(got, "# ") {
		t.Errorf("the title went with the cut: %.80q", got)
	}
}

func TestToolLineClipsAndFlattens(t *testing.T) {
	long := strings.Repeat("é", 400)
	line := toolLine("Bash", map[string]any{"command": "echo `x`\n" + long})
	if strings.Contains(line, "\n") || strings.Count(line, "`") != 2 {
		t.Errorf("line = %q, want one line with only the name quoted", line)
	}
	if utf8.RuneCountInString(line) > maxToolLine+20 {
		t.Errorf("line has %d runes, want it clipped", utf8.RuneCountInString(line))
	}
	if got := toolLine("", nil); got != "- `?`" {
		t.Errorf("nameless tool = %q", got)
	}
}

func TestBuildTranscriptStopsOpeningLogsAtTheCap(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	oldOpened := false
	old := TranscriptRun{Stage: "in_progress", StartedAt: t0, Open: func() (io.ReadCloser, error) {
		oldOpened = true
		return io.NopCloser(strings.NewReader(claudeRunLog)), nil
	}}
	recent := logRun(types.StageInProgress, t0.Add(time.Hour), claudeRunLog)
	got := BuildTranscript("developer", []TranscriptRun{old, recent}, nil, 64)
	if oldOpened {
		t.Error("the oldest log was parsed although the newest one already filled the cap")
	}
	if got == "" {
		t.Error("the capped transcript came out empty")
	}
	if all := BuildTranscript("developer", []TranscriptRun{old, recent}, nil, 0); !oldOpened || all == "" {
		t.Error("without a cap every run must be read")
	}
}
