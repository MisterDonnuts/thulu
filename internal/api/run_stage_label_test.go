package api

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func TestRunStageLabel(t *testing.T) {
	t.Parallel()
	s, _ := newIntegrationServer(t)
	const id = "t-label"
	if err := s.taskStore.Create(t.Context(), &task.Task{ID: id, WorkspaceID: "ws-test", Title: "label me", Stage: "backlog"}); err != nil {
		t.Fatal(err)
	}

	es := i18n.WithLocale(t.Context(), "es")
	if got := s.runStageLabel(es, id, "in_progress"); got != "En curso" {
		t.Errorf("in_progress in es = %q, want En curso", got)
	}
	en := i18n.WithLocale(t.Context(), "en")
	if got := s.runStageLabel(en, id, "in_progress"); got != "In progress" {
		t.Errorf("in_progress in en = %q, want In progress", got)
	}
	if got := s.runStageLabel(es, "no-such-task", "in_progress"); got != "En curso" {
		t.Errorf("unknown task = %q, want the default board's label", got)
	}
	if got := s.runStageLabel(es, id, "nowhere"); got != "" {
		t.Errorf("unknown stage = %q, want empty", got)
	}
}
