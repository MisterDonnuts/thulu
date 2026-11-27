package api

import (
	"context"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// One report in the brief stays under this many characters; the Reports tab has the rest.
const maxBriefReport = 8000

// A stage whose agent gives a verdict reads what the earlier verdict stages reported: the reviewer
// reads Verification, the tester both, production readiness all of them. The latest agent report of
// each earlier agent_verdict column on the task's flow, oldest column first. Agents have no tool that
// reads reports, so without this a gate could only trust the one-paragraph comments.
func (s *Server) writeBriefEarlierReports(ctx context.Context, b *strings.Builder, t *task.Task) {
	boardID := t.BoardID
	if boardID == "" {
		boardID = s.boards.DefaultBoardIDFor(t.WorkspaceID)
	}
	reg := s.boards.BoardFor(boardID, t.TypeID)
	cur, ok := reg.Get(t.Stage)
	if !ok || !cur.AgentVerdict {
		return
	}
	reports, err := s.taskStore.ListReports(ctx, t.ID)
	if err != nil {
		logx.Warn(err, "api.tasks.log.brief_reports", "task", t.ID)
		return
	}
	latest := map[task.Stage]*task.Report{}
	for i := range reports {
		if r := &reports[i]; r.Kind == task.ReportKindAgentIndex && r.Source == task.SourceAgent && r.Stage != "" {
			latest[r.Stage] = r
		}
	}
	wrote := false
	stages := reg.List()
	for i := range stages {
		st := &stages[i]
		if !st.AgentVerdict || st.Position >= cur.Position {
			continue
		}
		r := latest[st.ID]
		if r == nil {
			continue
		}
		if !wrote {
			b.WriteString(i18n.Text("api.tasks.prompts.brief_reports_heading"))
			wrote = true
		}
		label := st.Label
		if label == "" {
			label = string(st.ID)
		}
		b.WriteString(i18n.Text("api.tasks.prompts.brief_report_item", "stage", label, "id", string(st.ID),
			"author", r.Author, "when", r.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")))
		body := strings.TrimSpace(r.Body)
		if runes := []rune(body); len(runes) > maxBriefReport {
			body = string(runes[:maxBriefReport])
			b.WriteString(body)
			b.WriteString(i18n.Text("api.tasks.prompts.brief_report_cut", "max", maxBriefReport))
			continue
		}
		b.WriteString(body)
		b.WriteString("\n\n")
	}
}
