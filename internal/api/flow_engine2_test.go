package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func flowRawDo(t *testing.T, h http.Handler, method, path, ctype, key, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", ctype)
	if key != "" {
		r.Header.Set("X-API-Key", key)
	} else {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func flowFlagsOf(t *testing.T, rig *runScopeRig, id string) []string {
	t.Helper()
	got, err := rig.srv.taskStore.GetRow(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got.FlowFlags
}

// The surface decides which stages run, so only the stage that writes the spec may send flags.yaml.
func TestFlagsImport_OnlyAnIngestFlagsStageSetsTheSurface(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	const body = "surface: [UI, api]\nrisk: high\nsize: M\n"
	path := "/tasks/" + rig.a + "/flags/import"

	if w := flowRawDo(t, rig.h, http.MethodPost, path, "application/x-yaml", rig.keyA, "", body); w.Code != http.StatusForbidden {
		t.Fatalf("in_progress (no ingest_flags) = %d, want 403: %s", w.Code, w.Body.String())
	}
	if got := flowFlagsOf(t, rig, rig.a); got != nil {
		t.Fatalf("flow_flags = %v after a refused import, want NULL", got)
	}

	if w := patchJSONAuth(t, rig.h, "/stages/in_progress", "", map[string]any{"ingest_flags": true}); w.Code != http.StatusOK {
		t.Fatalf("PATCH ingest_flags = %d: %s", w.Code, w.Body.String())
	}
	auditor := rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: auth.AuditorAgent, Stage: "in_progress"})
	if w := flowRawDo(t, rig.h, http.MethodPost, path, "application/x-yaml", auditor, "", body); w.Code != http.StatusForbidden {
		t.Errorf("the auditor = %d, want 403: %s", w.Code, w.Body.String())
	}
	w := flowRawDo(t, rig.h, http.MethodPost, path, "application/x-yaml", rig.keyA, "", body)
	if w.Code != http.StatusOK {
		t.Fatalf("ingest_flags stage = %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		FlowFlags []string `json:"flow_flags"`
		Risk      string   `json:"risk"`
		Size      string   `json:"size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.FlowFlags, []string{"ui", "api"}) || res.Risk != "high" || res.Size != "M" {
		t.Errorf("response = %+v", res)
	}
	if got := flowFlagsOf(t, rig, rig.a); !reflect.DeepEqual(got, []string{"ui", "api"}) {
		t.Errorf("stored flow_flags = %v", got)
	}
}

func TestTaskPatch_FlowFlagsArePersonOnly(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	path := "/tasks/" + rig.a

	if w := keyDo(t, rig.h, http.MethodPatch, path, rig.keyA, map[string]any{"flow_flags": []string{"ui"}}); w.Code != http.StatusForbidden {
		t.Errorf("agent PATCH flow_flags = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := patchJSONAuth(t, rig.h, path, rig.token, map[string]any{"flow_flags": []string{"Not Valid"}}); w.Code != http.StatusBadRequest {
		t.Errorf("invalid flow_flags = %d, want 400: %s", w.Code, w.Body.String())
	}
	if w := patchJSONAuth(t, rig.h, path, rig.token, map[string]any{"flow_flags": []string{"perf"}}); w.Code != http.StatusOK {
		t.Fatalf("person PATCH flow_flags = %d: %s", w.Code, w.Body.String())
	}
	if got := flowFlagsOf(t, rig, rig.a); !reflect.DeepEqual(got, []string{"perf"}) {
		t.Errorf("flow_flags = %v, want [perf]", got)
	}
	// An empty list is a set surface that skips every conditional stage, not NULL.
	if w := patchJSONAuth(t, rig.h, path, rig.token, map[string]any{"flow_flags": []string{}}); w.Code != http.StatusOK {
		t.Fatalf("person PATCH [] = %d: %s", w.Code, w.Body.String())
	}
	if got := flowFlagsOf(t, rig, rig.a); got == nil || len(got) != 0 {
		t.Errorf("flow_flags = %#v, want []", got)
	}
}

func TestLockedTests_UploadAndRead(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	own := "developer-task-" + rig.a + "-in_progress-3"
	path := "/tasks/" + rig.a + "/locked-tests?instance="
	const list = "internal/x/contract_test.go\n"

	auditor := rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: auth.AuditorAgent, Stage: "in_progress"})
	if w := flowRawDo(t, rig.h, http.MethodPost, path+own, "text/plain", auditor, "", list); w.Code != http.StatusForbidden {
		t.Errorf("the auditor = %d, want 403", w.Code)
	}
	if w := flowRawDo(t, rig.h, http.MethodPost, path+"developer-task-"+rig.a+"-review-3", "text/plain", rig.keyA, "", list); w.Code != http.StatusBadRequest {
		t.Errorf("another stage's instance = %d, want 400", w.Code)
	}
	if w := flowRawDo(t, rig.h, http.MethodPost, path+own, "text/plain", rig.keyA, "", list); w.Code != http.StatusCreated {
		t.Fatalf("own run = %d: %s", w.Code, w.Body.String())
	}
	reports, err := rig.srv.taskStore.ListReports(t.Context(), rig.a)
	if err != nil {
		t.Fatal(err)
	}
	filed := 0
	for _, r := range reports {
		if r.Kind == task.ReportKindLockedTests && r.InstanceID == own && r.Body == list {
			filed++
		}
	}
	if filed != 1 {
		t.Errorf("%d locked_tests reports for the run, want 1", filed)
	}

	if err := rig.srv.taskStore.PutLockedFile(t.Context(), task.LockedFile{
		TaskID: rig.a, ProjectID: "p1", Path: "a_test.go", BlobSHA: "abc1234", Stage: "test_design",
	}); err != nil {
		t.Fatal(err)
	}
	w := getJSON(t, rig.h, "/tasks/"+rig.a+"/locked-files", rig.token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET locked-files = %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Files []task.LockedFile `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "a_test.go" || got.Files[0].BlobSHA != "abc1234" || got.Files[0].Stage != "test_design" {
		t.Errorf("locked files = %+v", got.Files)
	}
}

// The brief tells a stage what its flow flags mean before the run, not after: the surface only on a
// board with conditional stages, the lock in a lock_tests stage, and the locked files elsewhere.
func TestBrief_SaysWhatTheFlowExpects(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	h, id := rig.h, rig.a
	if w := moveTask(t, h, rig.token, id, "review"); w.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", w.Code, w.Body.String())
	}
	brief := func() string {
		t.Helper()
		w := getJSON(t, h, "/tasks/"+id+"/brief", rig.token)
		if w.Code != http.StatusOK {
			t.Fatalf("brief = %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	patch := func(body map[string]any) {
		t.Helper()
		if w := patchJSONAuth(t, h, "/stages/review", "", body); w.Code != http.StatusOK {
			t.Fatalf("PATCH %v = %d: %s", body, w.Code, w.Body.String())
		}
	}

	patch(map[string]any{"ingest_flags": true})
	if strings.Contains(brief(), "surface:") {
		t.Error("a board without conditional stages asks for the surface")
	}
	patch(map[string]any{"skip_unless": []string{"ui"}})
	if !strings.Contains(brief(), "surface:") {
		t.Error("an ingest_flags stage on a board with conditional stages is not told about the surface")
	}
	patch(map[string]any{"skip_unless": []string{}, "ingest_flags": false})
	if b := brief(); strings.Contains(b, "locked-tests.txt") || strings.Contains(b, "surface:") {
		t.Errorf("a plain stage brief mentions the flow files:\n%s", b)
	}
	patch(map[string]any{"lock_tests": true})
	if !strings.Contains(brief(), "locked-tests.txt") {
		t.Error("a lock_tests stage is not told it locks its tests")
	}

	for _, f := range []task.LockedFile{
		{TaskID: id, ProjectID: "p1", Path: "pkg/a_test.go", BlobSHA: "abc1234", Stage: "test_design"},
		{TaskID: id, ProjectID: "p1", Path: "pkg/gone_test.go", Stage: "test_design"},
		{TaskID: id, ProjectID: "p1", Path: "pkg/own_test.go", BlobSHA: "def5678", Stage: "review"},
	} {
		if err := rig.srv.taskStore.PutLockedFile(t.Context(), f); err != nil {
			t.Fatal(err)
		}
	}
	b := brief()
	if !strings.Contains(b, "`pkg/a_test.go`") {
		t.Errorf("the brief does not list the locked test:\n%s", b)
	}
	for _, p := range []string{"pkg/gone_test.go", "pkg/own_test.go"} {
		if strings.Contains(b, p) {
			t.Errorf("the brief lists %s (a locked deletion, or a file this stage locks itself)", p)
		}
	}
}
