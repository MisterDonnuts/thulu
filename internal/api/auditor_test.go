package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
)

const auditVerdict = "---\nverdict: block\n---\n# Notes\nfalta el test\n"

func postFeedback(t *testing.T, h http.Handler, taskID, instance, key, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost,
		"/tasks/"+taskID+"/auditor-feedback?instance="+instance, strings.NewReader(auditVerdict))
	r.Header.Set("Content-Type", "text/markdown")
	if key != "" {
		r.Header.Set("X-API-Key", key)
	} else {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAuditorFeedback_OnlyTheAuditorsKeyFilesIt(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	auditor := rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: auth.AuditorAgent, Stage: "in_progress"})
	own := "developer-audit-" + rig.a + "-in_progress-17"

	cases := []struct {
		name, key, task, instance string
		want                      int
	}{
		{"the audited agent's key", rig.keyA, rig.a, own, http.StatusForbidden},
		{"a person", "", rig.a, own, http.StatusForbidden},
		{"the auditor, another stage", auditor, rig.a, "developer-audit-" + rig.a + "-review-17", http.StatusBadRequest},
		{"the auditor, the agent's instance", auditor, rig.a, "developer-task-" + rig.a + "-in_progress-17", http.StatusBadRequest},
		{"the auditor, another task", auditor, rig.b, "developer-audit-" + rig.b + "-in_progress-17", http.StatusForbidden},
		{"the auditor, its own run", auditor, rig.a, own, http.StatusCreated},
	}
	for _, c := range cases {
		w := postFeedback(t, rig.h, c.task, c.instance, c.key, rig.token)
		if w.Code != c.want {
			t.Errorf("%s = %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
	}

	reports, err := rig.srv.taskStore.ListReports(t.Context(), rig.a)
	if err != nil {
		t.Fatal(err)
	}
	filed := 0
	for _, r := range reports {
		if r.Kind == task.ReportKindAuditorFeedback {
			filed++
			if r.Source != task.SourceAuditor || r.Author != auth.AuditorAgent || r.InstanceID != own {
				t.Errorf("feedback filed as %s/%s on %s", r.Source, r.Author, r.InstanceID)
			}
		}
	}
	if filed != 1 {
		t.Errorf("%d feedback reports, want only the auditor's", filed)
	}
}

func TestBrief_ShowsTheRunUnderAudit(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	run := "developer-task-" + rig.a + "-in_progress-17"
	ctx := t.Context()
	if _, err := rig.srv.taskStore.AddReport(ctx, rig.a, task.SourceAgent, "developer",
		task.ReportKindAgentIndex, "", "changed the login handler", run); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.srv.taskStore.AddReport(ctx, rig.a, task.SourceAgent, "developer",
		task.ReportKindAgentIndex, "", "otra corrida", "developer-task-"+rig.a+"-in_progress-9"); err != nil {
		t.Fatal(err)
	}
	auditor := rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: auth.AuditorAgent, Stage: "in_progress"})

	w := keyDo(t, rig.h, http.MethodGet, "/tasks/"+rig.a+"/brief?audit="+run, auditor, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("brief = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Run under audit: `"+run+"`") || !strings.Contains(body, "changed the login handler") {
		t.Errorf("brief is missing the audited run:\n%s", body)
	}
	if strings.Contains(body, "otra corrida") {
		t.Errorf("brief carries another run's report:\n%s", body)
	}
}
