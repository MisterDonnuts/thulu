package api

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Stage agents hand the task on through verdict.md and the auditor's feedback.md, not through
// comments: the brief carries what the stage that sent the task back wrote, this stage's own last
// send-back or block, and the `delta` and `next` the earlier stages left.
func (s *Server) writeBriefHandoff(ctx context.Context, b *strings.Builder, t *task.Task) {
	reports, err := s.taskStore.ListReports(ctx, t.ID)
	if err != nil {
		logx.Warn(err, "api.tasks.log.brief_reports", "task", t.ID)
		return
	}
	events, err := s.taskStore.ListEvents(ctx, t.ID)
	if err != nil {
		logx.Warn(err, "api.tasks.log.brief_reports", "task", t.ID)
		return
	}
	boardID := t.BoardID
	if boardID == "" {
		boardID = s.boards.DefaultBoardIDFor(t.WorkspaceID)
	}
	reg := s.boards.BoardFor(boardID, t.TypeID)
	label := func(id task.Stage) string {
		if info, ok := reg.Get(id); ok && info.Label != "" {
			return info.Label
		}
		return string(id)
	}

	var out strings.Builder
	writeHandoffKickBack(&out, t, reports, events, label)
	if cur, ok := reg.Get(t.Stage); ok {
		if cur.AgentVerdict {
			writeHandoffPrevious(&out, t.Stage, reports)
		}
		writeHandoffEarlier(&out, reg.List(), cur.Position, reports, label)
	}
	if out.Len() == 0 {
		return
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_handoff_heading"))
	b.WriteString(out.String())
}

type stageEntry struct {
	From string `json:"from"`
	To   string `json:"to"`
	Via  string `json:"via"`
}

// The send-back that put the task in its stage, when the latest entry into that stage was one.
func writeHandoffKickBack(b *strings.Builder, t *task.Task, reports []task.Report, events []task.WorkflowEvent,
	label func(task.Stage) string,
) {
	var entry stageEntry
	var at *task.WorkflowEvent
	for i := len(events) - 1; i >= 0; i-- {
		e := &events[i]
		if e.Kind != task.EventStageEntered || len(e.Payload) == 0 {
			continue
		}
		var p stageEntry
		if json.Unmarshal(e.Payload, &p) != nil || task.Stage(p.To) != t.Stage {
			continue
		}
		entry, at = p, e
		break
	}
	if at == nil {
		return
	}
	from := task.Stage(entry.From)
	when := briefWhen(at.CreatedAt)
	switch entry.Via {
	case task.ViaVerdict, task.ViaKickBack:
		kind := task.ReportKindStageVerdict
		if entry.Via == task.ViaKickBack {
			kind = task.ReportKindAuditorFeedback
		}
		r := latestReport(reports, kind, from, at.CreatedAt)
		if r == nil {
			return
		}
		meta, body := verdictFrontMatter(r.Body)
		b.WriteString(i18n.Text("api.tasks.prompts.brief_handoff_kick_back",
			"stage", label(from), "id", string(from), "author", r.Author, "when", when))
		writeHandoffBody(b, body)
		writeHandoffFields(b, meta)
	case task.ViaChangesRequested:
		idx := slices.IndexFunc(t.Approvals, func(a task.Approval) bool { return a.ID == at.RefID })
		if idx < 0 {
			return
		}
		a := &t.Approvals[idx]
		b.WriteString(i18n.Text("api.tasks.prompts.brief_handoff_changes",
			"approval", a.Label, "stage", label(from), "id", string(from), "who", a.DecidedBy, "when", when))
		writeHandoffBody(b, a.DecidedNote)
	}
}

// Readiness compares a finding with its own last send-back; a relaunched block says why it stopped.
func writeHandoffPrevious(b *strings.Builder, stage task.Stage, reports []task.Report) {
	r := latestReport(reports, task.ReportKindStageVerdict, stage, time.Time{})
	if r == nil {
		return
	}
	meta, body := verdictFrontMatter(r.Body)
	verdict := strings.TrimSpace(fmt.Sprint(meta["verdict"]))
	if verdict != "kick_back" && verdict != "block" {
		return
	}
	b.WriteString(i18n.Text("api.tasks.prompts.brief_handoff_previous", "verdict", verdict, "when", briefWhen(r.CreatedAt)))
	writeHandoffBody(b, body)
	writeHandoffFields(b, meta)
}

func writeHandoffEarlier(b *strings.Builder, stages []types.StageInfo, before int, reports []task.Report,
	label func(task.Stage) string,
) {
	wrote := false
	for i := range stages {
		st := &stages[i]
		if st.Position >= before {
			continue
		}
		r := latestReport(reports, task.ReportKindStageVerdict, st.ID, time.Time{})
		if r == nil {
			continue
		}
		meta, _ := verdictFrontMatter(r.Body)
		if handoffText(meta["delta"]) == "" && handoffText(meta["next"]) == "" {
			continue
		}
		if !wrote {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_handoff_earlier_heading"))
			wrote = true
		}
		b.WriteString(i18n.Text("api.tasks.prompts.brief_handoff_earlier_item",
			"stage", label(st.ID), "id", string(st.ID), "author", r.Author, "when", briefWhen(r.CreatedAt)))
		writeHandoffFields(b, meta)
	}
}

// The latest agent report of kind from stage; a non-zero notAfter leaves out later ones.
func latestReport(reports []task.Report, kind string, stage task.Stage, notAfter time.Time) *task.Report {
	for i := len(reports) - 1; i >= 0; i-- {
		r := &reports[i]
		if r.Kind != kind || r.Stage != stage {
			continue
		}
		if !notAfter.IsZero() && r.CreatedAt.After(notAfter) {
			continue
		}
		return r
	}
	return nil
}

func briefWhen(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

func writeHandoffBody(b *strings.Builder, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	if runes := []rune(body); len(runes) > maxBriefReport {
		b.WriteString(string(runes[:maxBriefReport]))
		b.WriteString(i18n.Text("api.tasks.prompts.brief_report_cut", "max", maxBriefReport))
		return
	}
	b.WriteString(body)
	b.WriteString("\n\n")
}

func writeHandoffFields(b *strings.Builder, meta map[string]any) {
	for _, f := range []struct{ key, label string }{
		{"next", "api.tasks.prompts.brief_handoff_next"},
		{"delta", "api.tasks.prompts.brief_handoff_delta"},
	} {
		text := handoffText(meta[f.key])
		if text == "" {
			continue
		}
		sep := " "
		if strings.Contains(text, "\n") {
			sep = "\n"
		}
		fmt.Fprintf(b, "**%s:**%s%s\n\n", i18n.Text(f.label), sep, text)
	}
}

// verdict.md's front matter as a map, and the text after it. Read here rather than through the
// dispatcher's verdict reader: the brief only needs the optional handoff fields, and a body without
// front matter is passed on as it is.
func verdictFrontMatter(body string) (map[string]any, string) {
	s := strings.TrimSpace(body)
	if !strings.HasPrefix(s, "---") {
		return nil, s
	}
	parts := strings.SplitN(s, "---", 3)
	if len(parts) < 3 {
		return nil, s
	}
	var meta map[string]any
	if yaml.Unmarshal([]byte(parts[1]), &meta) != nil {
		return nil, strings.TrimSpace(parts[2])
	}
	return meta, strings.TrimSpace(parts[2])
}

// A string stays as written; a list becomes one bullet per item, a map one "key: value" line.
func handoffText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case []any:
		lines := make([]string, 0, len(x))
		for _, item := range x {
			if s := handoffInline(item); s != "" {
				lines = append(lines, "- "+s)
			}
		}
		return strings.Join(lines, "\n")
	case map[string]any:
		return handoffInline(x)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func handoffInline(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.Join(strings.Fields(x), " ")
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if s := handoffInline(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if s := handoffInline(x[k]); s != "" {
				parts = append(parts, k+": "+s)
			}
		}
		return strings.Join(parts, "; ")
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
