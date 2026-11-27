package exportbundle_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/exportbundle"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// What the default flow adds travels in the bundle: the board's flow flags, type agents and send-back
// cap, the project's release mode, and the task's merge commits, flow flags and locked tests. A
// second import of the same bundle leaves a locked test that moved on since alone.
func TestRoundTripFlowFields(t *testing.T) {
	ctx := context.Background()
	src := buildStores(t, testpg.New(t), t.TempDir())
	past := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)

	board := stage.BoardInfo{ID: "flow", Label: "Flow", WorkspaceID: ws, Default: true, MaxKickBacks: 2,
		Flow: &stage.BoardFlow{Name: "thulu", Revision: 1, Hash: "abc"}}
	must(t, src.stage.CreateBoard(ctx, board, []types.StageInfo{
		{ID: "f_backlog", Label: "Backlog", Initial: true},
		{ID: "f_dev", Label: "Dev", LockTests: true, AgentVerdict: true, DefaultAgent: "dev"},
		{ID: "f_qa", Label: "QA", SkipUnless: []string{"ui"}},
		{ID: "f_review", Label: "Review", PRDraft: true, PRReady: true},
		{ID: "f_release", Label: "Release", MergeTarget: true, ReleaseResources: true},
		{ID: "f_done", Label: "Done", Terminal: true},
	}), "create board")
	must(t, src.stage.SaveBoardTypes(ctx, "flow", []stage.TaskTypeInfo{
		{ID: "feature", Label: "Feature", Default: true},
		{ID: "release", Label: "Release", Skips: []types.Stage{"f_dev", "f_qa", "f_review"},
			Agents: map[types.Stage]string{"f_release": "releaser"}},
	}), "save types")
	must(t, src.s.Projects.ImportProject(ctx, project.Project{
		ID: "proj_f", WorkspaceID: ws, Name: "p", Path: "/x", RepoURL: "https://x", DefaultBranch: "main",
		CreatedAt: past, ReleaseMode: project.ReleaseBatch, ReleaseEnvironment: "production",
	}), "seed project")

	flags := []string{"ui"}
	tk := &task.Task{ID: "task_f", WorkspaceID: ws, BoardID: "flow", Title: "F", ProjectIDs: []string{"proj_f"},
		MergeSHAs: map[string]string{"proj_f": "1234567890123456789012345678901234567890"}, FlowFlags: flags}
	must(t, src.task.Create(ctx, tk), "create task")
	none := &task.Task{ID: "task_n", WorkspaceID: ws, BoardID: "flow", Title: "N", FlowFlags: []string{}}
	must(t, src.task.Create(ctx, none), "create task with no flags")
	locked := task.LockedFile{TaskID: "task_f", ProjectID: "proj_f", Path: "tests/a_test.go", BlobSHA: "111",
		Stage: "f_dev", LockedRef: "proj_f@aaa", LockedAt: past}
	must(t, src.task.PutLockedFile(ctx, locked), "lock test")

	bundle, attachments, err := exportbundle.Export(ctx, src.s, ws)
	must(t, err, "export")
	var buf bytes.Buffer
	must(t, exportbundle.Write(&buf, bundle, attachments), "write archive")
	got, gotAtt, err := exportbundle.Read(&buf)
	must(t, err, "read archive")

	dst := buildStores(t, testpg.New(t), t.TempDir())
	_, err = exportbundle.Import(ctx, dst.s, got, gotAtt, dst.attachmentsDir)
	must(t, err, "import")

	info, ok := stage.BoardInfoOf(dst.stage, "flow")
	if !ok || info.MaxKickBacks != 2 || info.Flow == nil || *info.Flow != *board.Flow {
		t.Errorf("board info = %+v (flow %+v), want the cap and the flow origin", info, info.Flow)
	}
	reg := dst.stage.Board("flow")
	dev, _ := reg.Get("f_dev")
	qa, _ := reg.Get("f_qa")
	review, _ := reg.Get("f_review")
	release, _ := reg.Get("f_release")
	if !dev.LockTests || !dev.AgentVerdict || dev.DefaultAgent != "dev" || !slices.Equal(qa.SkipUnless, []string{"ui"}) ||
		!review.PRDraft || !review.PRReady || !release.MergeTarget || !release.ReleaseResources {
		t.Errorf("stage flags lost: dev %+v qa %+v review %+v release %+v", dev, qa, review, release)
	}
	if agents := typeAgents(dst.stage.BoardTypes("flow"), "release"); agents["f_release"] != "releaser" {
		t.Errorf("release type agents = %v, want f_release: releaser", agents)
	}
	proj, err := dst.s.Projects.Get(ctx, "proj_f")
	must(t, err, "get project")
	if proj.ReleaseMode != project.ReleaseBatch || proj.ReleaseEnvironment != "production" {
		t.Errorf("project release = %q/%q, want batch/production", proj.ReleaseMode, proj.ReleaseEnvironment)
	}
	gotTask, err := dst.task.Get(ctx, "task_f")
	must(t, err, "get task")
	if gotTask.MergeSHAs["proj_f"] != tk.MergeSHAs["proj_f"] || !slices.Equal(gotTask.FlowFlags, flags) {
		t.Errorf("task merge_shas %v flow_flags %v", gotTask.MergeSHAs, gotTask.FlowFlags)
	}
	gotNone, err := dst.task.Get(ctx, "task_n")
	must(t, err, "get task with no flags")
	if gotNone.FlowFlags == nil || len(gotNone.FlowFlags) != 0 {
		t.Errorf("empty flow flags came back as %#v, want an empty list (nil runs every stage)", gotNone.FlowFlags)
	}
	files, err := dst.task.ListLockedFiles(ctx, "task_f")
	must(t, err, "list locked files")
	if len(files) != 1 || files[0].BlobSHA != "111" || files[0].LockedRef != "proj_f@aaa" || !files[0].LockedAt.Equal(past) {
		t.Fatalf("locked files = %+v", files)
	}

	// The task moves on here: a person approved a change to the locked test.
	moved := locked
	moved.BlobSHA, moved.LockedRef = "222", "proj_f@bbb"
	must(t, dst.task.PutLockedFile(ctx, moved), "relock")
	_, err = exportbundle.Import(ctx, dst.s, got, gotAtt, dst.attachmentsDir)
	must(t, err, "re-import")
	files, err = dst.task.ListLockedFiles(ctx, "task_f")
	must(t, err, "list locked files after re-import")
	if len(files) != 1 || files[0].BlobSHA != "222" {
		t.Errorf("re-import moved the locked baseline back: %+v", files)
	}
}

func typeAgents(tt []stage.TaskTypeInfo, id string) map[types.Stage]string {
	for i := range tt {
		if tt[i].ID == id {
			return tt[i].Agents
		}
	}
	return nil
}
