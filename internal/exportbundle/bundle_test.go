package exportbundle_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/exportbundle"
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const ws = "ws_test"

type kit struct {
	s              exportbundle.Stores
	stage          *stage.Store
	task           *task.Store
	metrics        *envmetrics.Store
	attachmentsDir string
}

func buildStores(t *testing.T, db *sql.DB, dataDir string) kit {
	t.Helper()
	agents, err := agentstore.New(db)
	must(t, err, "agent store")
	set, err := settings.NewStore(db)
	must(t, err, "settings")
	reg, err := mcp.NewRegistry(db)
	must(t, err, "mcp")
	boards, err := stage.NewStore(db)
	must(t, err, "stage")
	attachmentsDir := filepath.Join(dataDir, "task-attachments")
	tasks, err := task.NewStore(db, attachmentsDir)
	must(t, err, "task")
	tasks = tasks.WithBoards(boards)
	projects, err := project.NewStore(db, filepath.Join(dataDir, "projects"))
	must(t, err, "project")
	metrics, err := envmetrics.NewStore(db)
	must(t, err, "envmetrics")
	ing, err := ingest.NewStore(db)
	must(t, err, "ingest")
	creds, err := credentials.NewStore(db)
	must(t, err, "credentials")
	return kit{
		s: exportbundle.Stores{
			Agents: agents, Settings: set, MCP: reg, Boards: boards,
			Tasks: tasks, Projects: projects, Metrics: metrics, Ingest: ing, Creds: creds,
		},
		stage: boards, task: tasks, metrics: metrics, attachmentsDir: attachmentsDir,
	}
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	srcData := t.TempDir()
	src := buildStores(t, testpg.New(t), srcData)

	past := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)

	_, err := src.s.Agents.PutAgent(ctx, agentstore.GlobalTenant,
		agentstore.Agent{AgentType: types.AgentType{Name: "developer", Model: "opus"}}, "seed")
	must(t, err, "put agent")
	must(t, src.s.Settings.PutRaw(ctx, "chat.max", "7"), "put setting")
	must(t, src.s.Settings.PutRaw(ctx, settings.IngestPublicURLKey, `"https://old-host.example"`), "put ingest url")
	_, err = src.s.MCP.Register(ctx, mcp.Registration{
		Name: "octopus-api", URL: "http://mcp:9000", Description: "the api", CreatedBy: "seed", ForwardsCredential: true,
	})
	must(t, err, "register mcp")

	board := stage.BoardInfo{ID: "custom", Label: "Custom", WorkspaceID: ws, Position: 10, Default: true}
	columns := stage.Static().List()
	columns[0].RequiredApprovals = []types.ApprovalRequirement{{Key: "triage", Label: "Triage", ResetOnChange: true}}
	must(t, src.stage.CreateBoard(ctx, board, columns), "create board")

	must(t, src.s.Projects.ImportProject(ctx, project.Project{
		ID: "proj_a", WorkspaceID: ws, Name: "p", Path: "/x", RepoURL: "https://x", DefaultBranch: "main", CreatedAt: past,
	}), "seed project")
	must(t, src.s.Projects.ImportEnvironment(ctx, project.Environment{
		ID: "env_a", ProjectID: "proj_a", Name: "prod", Provider: "aws", IsProduction: true, CreatedAt: past, UpdatedAt: past,
	}), "seed env")
	_, err = src.s.Projects.SaveInitScript(ctx, "proj_a", project.InitScriptInput{Script: "old"},
		project.InitAuthor{Kind: project.InitAuthorUser, Label: "ana"})
	must(t, err, "seed init script 1")
	_, err = src.s.Projects.SaveInitScript(ctx, "proj_a", project.InitScriptInput{
		Script: "make up", Note: "make", Manifest: project.InitManifest{
			Ports: []int{8080},
			Setup: "npm ci",
			Tools: map[string]string{"node": "22"},
			Cache: project.InitCache{Key: []string{"package-lock.json"}, Paths: []string{"node_modules"}},
		},
	}, project.InitAuthor{Kind: project.InitAuthorAgent, Label: "agent developer · task t · stage s"})
	must(t, err, "seed init script 2")
	must(t, src.metrics.ImportCustomMetric(ctx, envmetrics.Metric{
		ID: "cm_a", EnvironmentID: "env_a", Service: "api", Name: "http_requests", Kind: "counter",
		Status: envmetrics.MetricApproved, CreatedAt: past, UpdatedAt: past,
	}), "seed metric")
	must(t, src.metrics.ImportDashboard(ctx, envmetrics.Dashboard{
		ID: "dash_a", EnvironmentID: "env_a", Name: "Overview", Status: envmetrics.DashActive, CreatedAt: past, UpdatedAt: past,
	}), "seed dashboard")

	taskA := &task.Task{ID: "task_a", WorkspaceID: ws, BoardID: "custom", Title: "A", Body: "body a"}
	must(t, src.task.Create(ctx, taskA), "create task a")
	must(t, setCreatedAt(src.task.DB(), "task_a", past), "backdate task a")
	taskB := &task.Task{ID: "task_b", WorkspaceID: ws, BoardID: "custom", Title: "B", Body: "body b"}
	must(t, src.task.Create(ctx, taskB), "create task b")
	comment, err := src.task.AddComment(ctx, "task_a", task.SourceSystem, "octopus", "hello")
	must(t, err, "add comment")
	_, err = src.task.AddReply(ctx, "task_a", comment.ID, task.SourceUser, "p@x", "reply")
	must(t, err, "add reply")
	must(t, src.task.AddDependency(ctx, "task_a", "task_b", task.DepHard), "add dep")

	srcApprovals, err := src.task.ListApprovals(ctx, "task_a")
	must(t, err, "list approvals")
	if len(srcApprovals) != 1 || srcApprovals[0].Key != "triage" {
		t.Fatalf("task a opened %+v, want triage", srcApprovals)
	}
	_, err = src.task.SetApprovalStatus(ctx, srcApprovals[0].ID, task.ApprovalApproved, "p@x", "", "proj_a@abc")
	must(t, err, "approve triage")
	asked := &task.Approval{TaskID: "task_a", Label: "Pick the DB", Source: task.ApprovalFromPerson, CreatedBy: "p@x"}
	must(t, src.task.AddApproval(ctx, asked), "ask approval")
	_, err = src.task.SetApprovalStatus(ctx, asked.ID, task.ApprovalChangesRequested, "q@x", "use Postgres", "")
	must(t, err, "request changes")

	attBytes := []byte("evidence-bytes-\x00\x01\x02")
	attPath := filepath.Join(srcData, "att-src.bin")
	must(t, os.WriteFile(attPath, attBytes, 0o640), "write att file")
	att, err := src.task.AddAttachment(ctx, task.NewAttachment{TaskID: "task_a", Filename: "evidence.bin", MimeType: "application/octet-stream", Path: attPath, Size: int64(len(attBytes))})
	must(t, err, "add attachment")

	report, err := src.task.AddReport(ctx, "task_a", task.SourceAgent, "qa", task.ReportKindEvidence, "❌ evidence plays", "| TC-1 | fail |", "")
	must(t, err, "add report")
	cases, err := src.task.AddTestCases(ctx, "task_a", "qa", []task.TestCase{{Title: "evidence plays"}})
	must(t, err, "add test case")
	srcRun, _, err := src.task.RecordTestCaseRun(ctx, "task_a", "TC-1", task.TestCaseRun{
		Verdict: task.TestCaseFail, VideoAttachmentID: att.ID, CommentID: comment.ID, ReportID: report.ID,
		CaptureAttachmentIDs: []string{att.ID}, Author: "qa",
	})
	must(t, err, "record run")

	bundle, attachments, err := exportbundle.Export(ctx, src.s, ws)
	must(t, err, "export")

	var buf bytes.Buffer
	must(t, exportbundle.Write(&buf, bundle, attachments), "write archive")

	got, gotAtt, err := exportbundle.Read(&buf)
	must(t, err, "read archive")
	if len(gotAtt) != 1 {
		t.Fatalf("expected 1 attachment in archive, got %d", len(gotAtt))
	}
	for _, b := range gotAtt {
		if !bytes.Equal(b, attBytes) {
			t.Fatalf("attachment bytes corrupted through the archive")
		}
	}

	dstData := t.TempDir()
	dst := buildStores(t, testpg.New(t), dstData)
	stats, err := exportbundle.Import(ctx, dst.s, got, gotAtt, dst.attachmentsDir)
	must(t, err, "import")

	if stats.InitScripts != 1 {
		t.Errorf("stats.InitScripts = %d, want 1", stats.InitScripts)
	}
	initScript, err := dst.s.Projects.InitScript(ctx, "proj_a")
	must(t, err, "init script after import")
	if initScript.Script != "make up" || initScript.Revision != 1 || initScript.AuthorKind != project.InitAuthorAgent ||
		initScript.Manifest.Ports[0] != 8080 || initScript.Manifest.Setup != "npm ci" ||
		initScript.Manifest.Tools["node"] != "22" || len(initScript.Manifest.Cache.Key) != 1 ||
		initScript.Manifest.Cache.Key[0] != "package-lock.json" || len(initScript.Manifest.Cache.Paths) != 1 ||
		initScript.Manifest.Cache.Paths[0] != "node_modules" {
		t.Errorf("imported init script = %+v", initScript)
	}

	foundBoard := false
	for _, b := range dst.stage.BoardList() {
		if b.ID == "custom" && b.WorkspaceID == ws {
			foundBoard = true
		}
	}
	if !foundBoard {
		t.Errorf("board 'custom' missing after import")
	}

	gotTask, err := dst.task.Get(ctx, "task_a")
	must(t, err, "get imported task")
	if !gotTask.CreatedAt.Equal(past) {
		t.Errorf("task created_at = %s, want preserved %s", gotTask.CreatedAt, past)
	}

	comments, err := dst.task.ListComments(ctx, "task_a")
	must(t, err, "list comments")
	if len(comments) != 2 || comments[0].Body != "hello" || comments[1].ParentID != comments[0].ID {
		t.Errorf("comment thread not restored: %+v", comments)
	}
	deps, err := dst.task.GetDependencies(ctx, "task_a")
	must(t, err, "list deps")
	if len(deps) != 1 || deps[0].TaskID != "task_b" {
		t.Errorf("dependency not restored: %+v", deps)
	}
	atts, err := dst.task.ListAttachments(ctx, "task_a")
	must(t, err, "list atts")
	if len(atts) != 1 {
		t.Fatalf("attachment row not restored: %+v", atts)
	}
	restored, err := os.ReadFile(atts[0].Path)
	must(t, err, "read restored attachment")
	if !bytes.Equal(restored, attBytes) {
		t.Errorf("restored attachment bytes differ from source")
	}

	gotCases, err := dst.task.ListTestCases(ctx, "task_a")
	must(t, err, "list test cases")
	if len(gotCases) != 1 || gotCases[0].ID != cases[0].ID || gotCases[0].Key != "TC-1" ||
		gotCases[0].Status != task.TestCaseFail || !gotCases[0].Covered {
		t.Fatalf("test case not restored: %+v", gotCases)
	}
	if runs := gotCases[0].Runs; len(runs) != 1 || runs[0].ID != srcRun.ID || runs[0].VideoAttachmentID != att.ID ||
		runs[0].CommentID != comment.ID || runs[0].ReportID != report.ID || len(runs[0].CaptureAttachmentIDs) != 1 {
		t.Errorf("test case run not restored: %+v", runs)
	}

	checkApprovals := func(when string) {
		t.Helper()
		gotApprovals, err := dst.task.ListApprovals(ctx, "task_a")
		must(t, err, "list approvals "+when)
		if len(gotApprovals) != 2 {
			t.Fatalf("%s: approvals = %+v, want 2", when, gotApprovals)
		}
		byID := map[string]task.Approval{}
		for _, a := range gotApprovals {
			byID[a.ID] = a
		}
		if a := byID[srcApprovals[0].ID]; a.Status != task.ApprovalApproved || a.ApprovedAtRef != "proj_a@abc" || a.Source != task.ApprovalFromStage {
			t.Errorf("%s: triage = %+v", when, a)
		}
		if a := byID[asked.ID]; a.Status != task.ApprovalChangesRequested || a.DecidedNote != "use Postgres" || a.DecidedBy != "q@x" {
			t.Errorf("%s: asked = %+v", when, a)
		}
	}
	checkApprovals("after import")

	reStats, err := exportbundle.Import(ctx, dst.s, got, gotAtt, dst.attachmentsDir)
	must(t, err, "re-import")
	if reStats.Tasks != 0 {
		t.Errorf("re-import counted %d tasks; every one already existed and none was written", reStats.Tasks)
	}
	if revs, _ := dst.s.Projects.ListInitScriptRevisions(ctx, "proj_a", 0); reStats.InitScripts != 0 || len(revs) != 1 {
		t.Errorf("re-import wrote the init script again: stats %d, %d revisions", reStats.InitScripts, len(revs))
	}
	checkApprovals("after re-import")
	again, err := dst.task.ListTestCases(ctx, "task_a")
	must(t, err, "list test cases after re-import")
	if len(again) != 1 || len(again[0].Runs) != 1 {
		t.Errorf("re-import duplicated test cases: %+v", again)
	}

	must(t, dst.task.DeleteTestCase(ctx, "task_a", "TC-1"), "delete imported case")
	local, err := dst.task.AddTestCases(ctx, "task_a", "qa", []task.TestCase{{Key: "TC-1", Title: "local case"}})
	must(t, err, "add local case")
	clash, err := exportbundle.Import(ctx, dst.s, got, gotAtt, dst.attachmentsDir)
	must(t, err, "import over a key collision")
	if !slices.ContainsFunc(clash.Warnings, func(w string) bool { return strings.Contains(w, "key is taken") }) {
		t.Errorf("expected a key collision warning, got %v", clash.Warnings)
	}
	kept, err := dst.task.ListTestCases(ctx, "task_a")
	must(t, err, "list test cases after collision")
	if len(kept) != 1 || kept[0].ID != local[0].ID || len(kept[0].Runs) != 0 {
		t.Errorf("collision should keep the local case without runs: %+v", kept)
	}

	dash, err := dst.metrics.GetDashboard(ctx, "env_a", "dash_a")
	must(t, err, "get dashboard")
	if dash.Status != envmetrics.DashActive {
		t.Errorf("dashboard status = %q, want active (status must survive restore)", dash.Status)
	}
	metricList, err := dst.metrics.ListCustomMetrics(ctx, "env_a")
	must(t, err, "list metrics")
	if len(metricList) != 1 || metricList[0].Status != envmetrics.MetricApproved {
		t.Errorf("custom metric approved status not preserved: %+v", metricList)
	}

	dstSettings, err := dst.s.Settings.List(ctx)
	must(t, err, "list dst settings")
	if dstSettings["chat.max"] != "7" {
		t.Errorf("chat.max = %q, want 7", dstSettings["chat.max"])
	}
	if dstSettings[settings.IngestPublicURLKey] == `"https://old-host.example"` {
		t.Errorf("ingest.public_url was imported from the old host; it must be skipped")
	}

	if stats.Tasks != 2 || stats.Boards != 1 || stats.Projects != 1 || stats.Attachments != 1 {
		t.Errorf("import stats look wrong: %+v", stats)
	}
}

func setCreatedAt(db *sql.DB, id string, at time.Time) error {
	_, err := db.Exec("UPDATE tasks SET created_at = $1, updated_at = $1 WHERE id = $2", at, id)
	return err
}

func must(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}
