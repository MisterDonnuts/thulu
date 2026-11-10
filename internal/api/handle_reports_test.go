package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestAddReport(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	ownRun := "developer-task-" + rig.a + "-in_progress-17"
	report := func(kind, body, instance string) map[string]any {
		return map[string]any{"kind": kind, "title": "✅ SHOP-142", "body": body, "instance_id": instance}
	}

	cases := []struct {
		name, key string
		body      map[string]any
		want      int
	}{
		{"evidence from the run", rig.keyA, report(task.ReportKindEvidence, "## checks", ""), http.StatusCreated},
		{"run index from the run", rig.keyA, report(task.ReportKindAgentIndex, "## Plan", ownRun), http.StatusCreated},
		{"auditor feedback has its own route", rig.keyA, report(task.ReportKindAuditorFeedback, "x", ""), http.StatusBadRequest},
		{"unknown kind", rig.keyA, report("note", "x", ""), http.StatusBadRequest},
		{"empty body", rig.keyA, report(task.ReportKindEvidence, "  ", ""), http.StatusBadRequest},
		{"body over the limit", rig.keyA, report(task.ReportKindEvidence, strings.Repeat("x", maxReportBody+1), ""), http.StatusRequestEntityTooLarge},
		{"title over the limit", rig.keyA, map[string]any{"kind": task.ReportKindEvidence, "title": strings.Repeat("t", maxReportTitle+1), "body": "x"}, http.StatusBadRequest},
		{"instance of another task", rig.keyA, report(task.ReportKindAgentIndex, "x", "developer-task-"+rig.b+"-in_progress-17"), http.StatusBadRequest},
		{"another task's run", rig.keyB1, report(task.ReportKindEvidence, "x", ""), http.StatusForbidden},
	}
	for _, c := range cases {
		w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/reports", c.key, c.body)
		if w.Code != c.want {
			t.Errorf("%s = %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
	}

	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/reports", rig.token, report(task.ReportKindAgentIndex, "x", "")); w.Code != http.StatusForbidden {
		t.Errorf("person filing a run index = %d, want 403: %s", w.Code, w.Body.String())
	}
	w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/reports", rig.token, report(task.ReportKindEvidence, "by hand", ""))
	if w.Code != http.StatusCreated {
		t.Fatalf("person filing evidence = %d, want 201: %s", w.Code, w.Body.String())
	}
	var byHand task.Report
	if err := json.Unmarshal(w.Body.Bytes(), &byHand); err != nil {
		t.Fatal(err)
	}
	if byHand.Source != task.SourceUser || byHand.Title != "✅ SHOP-142" || byHand.Kind != task.ReportKindEvidence {
		t.Errorf("report = %+v, want a user's evidence titled ✅ SHOP-142", byHand)
	}

	reports, err := rig.srv.taskStore.ListReports(t.Context(), rig.a)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 3 {
		t.Fatalf("reports = %d, want 3 (evidence, run index, by hand)", len(reports))
	}
	if reports[0].Source != task.SourceAgent || reports[0].Author != "developer" {
		t.Errorf("run's report signed %s/%s, want agent/developer", reports[0].Source, reports[0].Author)
	}
	if reports[1].InstanceID != ownRun {
		t.Errorf("run index instance = %q, want %q", reports[1].InstanceID, ownRun)
	}
	comments, err := rig.srv.taskStore.ListComments(t.Context(), rig.a)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 0 {
		t.Errorf("filing reports left %d comments, want none", len(comments))
	}
}

func TestAddReport_StageFromTheRunKey(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	mention := rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: "qa", Stage: string(types.StageMention)})
	evidence := map[string]any{"kind": task.ReportKindEvidence, "title": "TC-1", "body": "## checks"}

	w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/reports", mention, evidence)
	if w.Code != http.StatusCreated {
		t.Fatalf("mention run's evidence = %d: %s", w.Code, w.Body.String())
	}
	var fromRun task.Report
	if err := json.Unmarshal(w.Body.Bytes(), &fromRun); err != nil {
		t.Fatal(err)
	}
	if fromRun.Stage != types.StageMention {
		t.Errorf("mention run's report stage = %q, want %q", fromRun.Stage, types.StageMention)
	}

	w = postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/reports", rig.token, evidence)
	if w.Code != http.StatusCreated {
		t.Fatalf("person's evidence = %d: %s", w.Code, w.Body.String())
	}
	var byHand task.Report
	if err := json.Unmarshal(w.Body.Bytes(), &byHand); err != nil {
		t.Fatal(err)
	}
	if byHand.Stage == "" || byHand.Stage == types.StageMention {
		t.Errorf("person's report stage = %q, want the task's column", byHand.Stage)
	}
}

func TestRecordTestCaseRun_ReportID(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	addCases(t, rig.h, rig.token, rig.a, map[string]any{"title": "neto"})
	file := func(taskID string) string {
		rep, err := rig.srv.taskStore.AddReport(t.Context(), taskID, task.SourceAgent, "developer", task.ReportKindEvidence, "", "x", "")
		if err != nil {
			t.Fatal(err)
		}
		return rep.ID
	}
	own, foreign := file(rig.a), file(rig.b)

	if w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/test-cases/TC-1/runs", rig.keyA,
		map[string]any{"verdict": "pass", "report_id": foreign}); w.Code != http.StatusNotFound {
		t.Errorf("another task's report = %d, want 404: %s", w.Code, w.Body.String())
	}
	w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/test-cases/TC-1/runs", rig.keyA,
		map[string]any{"verdict": "pass", "report_id": own})
	if w.Code != http.StatusCreated {
		t.Fatalf("own report = %d, want 201: %s", w.Code, w.Body.String())
	}
	cases := listCases(t, rig.h, rig.token, rig.a)
	if len(cases[0].Runs) != 1 || cases[0].Runs[0].ReportID != own {
		t.Errorf("runs = %+v, want one run pointing at report %s", cases[0].Runs, own)
	}
}
