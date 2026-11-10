package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func TestLocalizeEventSummaries(t *testing.T) {
	cases := []struct {
		stored, locale, want string
	}{
		{i18n.T("en", "api.tasks.timeline.depends_on", "task", "3fa1"), "es",
			i18n.T("es", "api.tasks.timeline.depends_on", "task", "3fa1")},
		{i18n.T("es", "api.tasks.timeline.depends_on", "task", "3fa1"), "en",
			i18n.T("en", "api.tasks.timeline.depends_on", "task", "3fa1")},
		{i18n.T("en", "task.comments.deps_resolved"), "es", i18n.T("es", "task.comments.deps_resolved")},
		{i18n.T("en", "task.comments.run_queued_event", "stage", "In progress", "agent", "dev"), "es",
			i18n.T("es", "task.comments.run_queued_event", "stage", "En curso", "agent", "dev")},
		{i18n.T("es", "task.comments.run_queued_event", "stage", "En curso", "agent", "dev"), "en",
			i18n.T("en", "task.comments.run_queued_event", "stage", "In progress", "agent", "dev")},
		{i18n.T("en", "task.comments.run_queued_event", "stage", "Triage", "agent", "dev"), "es",
			i18n.T("es", "task.comments.run_queued_event", "stage", "Triage", "agent", "dev")},
		{i18n.T("en", "api.tasks.timeline.forked_from", "task", "3fa1", "stage", "in_progress"), "es",
			i18n.T("es", "api.tasks.timeline.forked_from", "task", "3fa1", "stage", "En curso")},
		{"in_progress · developer", "es", "in_progress · developer"},
		{"https://github.com/acme/web/pull/7", "es", "https://github.com/acme/web/pull/7"},
	}
	for _, c := range cases {
		got := localizeEventSummaries([]task.WorkflowEvent{{Summary: c.stored}}, c.locale)[0].Summary
		if got != c.want {
			t.Errorf("%q in %s = %q, want %q", c.stored, c.locale, got, c.want)
		}
	}
}

func TestListEvents_SummaryInReaderLanguage(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	id := createWSTask(t, h, "Timeline language")
	stored := i18n.T("en", "api.tasks.timeline.depends_on", "task", "3fa1")
	if err := srv.taskStore.RecordEvent(t.Context(), id, task.EventDepAdded, "3fa1", "user", stored, nil); err != nil {
		t.Fatal(err)
	}
	read := func(headers map[string]string) string {
		w := requestWithHeaders(t, h, http.MethodGet, "/tasks/"+id+"/events", nil, headers)
		if w.Code != http.StatusOK {
			t.Fatalf("GET events = %d: %s", w.Code, w.Body.String())
		}
		var evs []task.WorkflowEvent
		if err := json.Unmarshal(w.Body.Bytes(), &evs); err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Kind == task.EventDepAdded {
				return e.Summary
			}
		}
		t.Fatalf("no dep_added event in %s", w.Body.String())
		return ""
	}
	if got, want := read(map[string]string{"Accept-Language": "es"}), i18n.T("es", "api.tasks.timeline.depends_on", "task", "3fa1"); got != want {
		t.Errorf("es reader: %q, want %q", got, want)
	}
	if got := read(nil); got != stored {
		t.Errorf("en reader: %q, want the stored %q", got, stored)
	}
}

func createWSTask(t *testing.T, h http.Handler, title string) string {
	t.Helper()
	w := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": title})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.ID == "" {
		t.Fatalf("create task: no id in %s", w.Body.String())
	}
	return resp.ID
}
