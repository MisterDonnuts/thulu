package task

import (
	"context"
	"testing"
	"time"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/stage/stagetest"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func flowFixture(t *testing.T) *Store {
	t.Helper()
	return newTestStore(t).WithStages(stagepkg.NewSnapshot(stagetest.Flow()))
}

func TestSummarize(t *testing.T) {
	got := Summarize([]int64{10, 20, 30, 40, 100})
	if got.N != 5 {
		t.Errorf("N = %d, want 5", got.N)
	}
	if got.AvgMs != 40 {
		t.Errorf("AvgMs = %d, want 40", got.AvgMs)
	}
	if got.P50Ms != 30 {
		t.Errorf("P50Ms = %d, want 30", got.P50Ms)
	}
	if got.P90Ms != 100 {
		t.Errorf("P90Ms = %d, want 100", got.P90Ms)
	}
	if got.MaxMs != 100 {
		t.Errorf("MaxMs = %d, want 100", got.MaxMs)
	}
	if zero := Summarize(nil); zero != (Dist{}) {
		t.Errorf("Summarize(nil) = %+v, want zero", zero)
	}
}

func TestStageTransition(t *testing.T) {
	tests := []struct {
		name             string
		summary, payload string
		wantFrom, wantTo Stage
	}{
		{"payload wins", "whatever", `{"from":"review","to":"testing"}`,
			types.StageReview, types.StageTesting},
		{"summary fallback", "review → testing", "{}",
			types.StageReview, types.StageTesting},
		{"kick_back annotation stripped", "testing → in_progress (kick_back)", "",
			types.StageTesting, types.StageInProgress},
		{"unparseable", "nothing here", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			from, to := stageTransition(tc.summary, tc.payload)
			if from != tc.wantFrom || to != tc.wantTo {
				t.Errorf("got (%q,%q), want (%q,%q)", from, to, tc.wantFrom, tc.wantTo)
			}
		})
	}
}

func TestIsBackwards(t *testing.T) {
	s := flowFixture(t)
	reg := s.board("")
	if !isBackwards(reg, types.StageTesting, types.StageInProgress) {
		t.Error("testing → in_progress should be backwards")
	}
	if isBackwards(reg, types.StageInProgress, types.StageTesting) {
		t.Error("in_progress → testing should be forward")
	}
	if isBackwards(reg, types.StageTesting, types.StageTesting) {
		t.Error("a self-transition is not rework")
	}
	if isBackwards(reg, types.Stage("deleted_stage"), types.StageInProgress) {
		t.Error("unknown source stage should not count as backwards")
	}
}

func TestComputeFlowDwellAndRework(t *testing.T) {
	s := flowFixture(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	from, to := base.Add(-time.Hour), base.Add(100*time.Hour)

	tasks := []flowTask{{
		id: "t1", stage: types.StageTesting, boardID: "", status: StatusIdle, created: base,
	}}
	events := map[string][]flowEvent{"t1": {
		{taskID: "t1", kind: EventStageEntered, from: types.StageBacklog, to: types.StageInProgress, at: base.Add(time.Hour)},
		{taskID: "t1", kind: EventStageEntered, from: types.StageInProgress, to: types.StageTesting, at: base.Add(4 * time.Hour)},
		{taskID: "t1", kind: EventStageEntered, from: types.StageTesting, to: types.StageInProgress, at: base.Add(6 * time.Hour)},
		{taskID: "t1", kind: EventAgentRunStarted, at: base.Add(2 * time.Hour)},
		{taskID: "t1", kind: EventAgentRunStarted, at: base.Add(7 * time.Hour)},
		{taskID: "t1", kind: EventPROpened, at: base.Add(5 * time.Hour)},
		{taskID: "t1", kind: EventFlagRaised, at: base.Add(5 * time.Hour)},
	}}

	m := s.computeFlow(tasks, events, from, to)

	if m.TasksTotal != 1 || m.TasksOpen != 1 || m.TasksDone != 0 {
		t.Errorf("census = total %d open %d done %d, want 1/1/0",
			m.TasksTotal, m.TasksOpen, m.TasksDone)
	}
	if m.Kickbacks != 1 || m.ReworkedTasks != 1 {
		t.Errorf("kickbacks %d reworked %d, want 1/1", m.Kickbacks, m.ReworkedTasks)
	}
	if m.ReworkRate != 1 {
		t.Errorf("ReworkRate = %v, want 1", m.ReworkRate)
	}
	if m.AgentRuns != 2 || m.RunsPerTask != 2 {
		t.Errorf("AgentRuns %d RunsPerTask %v, want 2/2", m.AgentRuns, m.RunsPerTask)
	}
	if m.PRsOpened != 1 || m.FlagsRaised != 1 {
		t.Errorf("PRsOpened %d FlagsRaised %d, want 1/1", m.PRsOpened, m.FlagsRaised)
	}

	dwell := map[string]Dist{}
	for _, d := range m.StageDwell {
		dwell[d.Stage] = d.Dist
	}
	if got := dwell["backlog"].AvgMs; got != time.Hour.Milliseconds() {
		t.Errorf("backlog dwell = %d, want %d", got, time.Hour.Milliseconds())
	}
	if got := dwell["in_progress"].AvgMs; got != (3 * time.Hour).Milliseconds() {
		t.Errorf("in_progress dwell = %d, want %d", got, (3 * time.Hour).Milliseconds())
	}
	if got := dwell["testing"].AvgMs; got != (2 * time.Hour).Milliseconds() {
		t.Errorf("testing dwell = %d, want %d", got, (2 * time.Hour).Milliseconds())
	}

	if m.LeadTime.N != 0 {
		t.Errorf("LeadTime.N = %d, want 0 (task not finished)", m.LeadTime.N)
	}
	wantAge := to.Sub(base.Add(6 * time.Hour)).Milliseconds()
	if m.WIPAge.N != 1 || m.WIPAge.MaxMs != wantAge {
		t.Errorf("WIPAge = %+v, want n=1 max=%d", m.WIPAge, wantAge)
	}
}

func TestComputeFlowLeadTime(t *testing.T) {
	s := flowFixture(t)
	created := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	doneAt := created.Add(30 * time.Hour)
	from, to := created.Add(-24*time.Hour), doneAt.Add(24*time.Hour)

	tasks := []flowTask{{id: "t1", stage: types.StageDone, created: created}}
	events := map[string][]flowEvent{"t1": {
		{taskID: "t1", kind: EventStageEntered, from: types.StageBacklog, to: types.StageInProgress, at: created.Add(time.Hour)},
		{taskID: "t1", kind: EventStageEntered, from: types.StageInProgress, to: types.StageDone, at: doneAt},
	}}

	m := s.computeFlow(tasks, events, from, to)

	if m.TasksDone != 1 || m.TasksOpen != 0 {
		t.Errorf("census = done %d open %d, want 1/0", m.TasksDone, m.TasksOpen)
	}
	if m.TasksCompleted != 1 {
		t.Errorf("TasksCompleted = %d, want 1", m.TasksCompleted)
	}
	if want := (30 * time.Hour).Milliseconds(); m.LeadTime.N != 1 || m.LeadTime.P50Ms != want {
		t.Errorf("LeadTime = %+v, want n=1 p50=%d", m.LeadTime, want)
	}
	if m.WIPAge.N != 0 {
		t.Errorf("WIPAge.N = %d, want 0 (nothing open)", m.WIPAge.N)
	}

	var completedOn string
	for _, p := range m.Throughput {
		if p.Completed > 0 {
			completedOn = p.Bucket
		}
	}
	if completedOn != "2026-07-11" {
		t.Errorf("completion bucketed on %q, want 2026-07-11", completedOn)
	}
}

func TestComputeFlowWindowExcludesOldEvents(t *testing.T) {
	s := flowFixture(t)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(48 * time.Hour)

	tasks := []flowTask{{id: "t1", stage: types.StageDone, created: old}}
	events := map[string][]flowEvent{"t1": {
		{taskID: "t1", kind: EventStageEntered, from: types.StageBacklog, to: types.StageDone, at: old.Add(time.Hour)},
		{taskID: "t1", kind: EventAgentRunStarted, at: old.Add(time.Hour)},
	}}

	m := s.computeFlow(tasks, events, from, to)

	if m.TasksTotal != 1 || m.TasksDone != 1 {
		t.Errorf("census should still see the task: %+v", m)
	}
	if m.TasksCompleted != 0 || m.TasksCreated != 0 || m.AgentRuns != 0 {
		t.Errorf("pre-window activity leaked: completed %d created %d runs %d",
			m.TasksCompleted, m.TasksCreated, m.AgentRuns)
	}
	if m.LeadTime.N != 0 {
		t.Errorf("LeadTime.N = %d, want 0", m.LeadTime.N)
	}
}

func TestProjectFlowEndToEnd(t *testing.T) {
	s := flowFixture(t)
	ctx := context.Background()

	mine := &Task{WorkspaceID: testWS, ID: "task-mine", ProjectIDs: []string{"proj-a"}, Title: "mine", Stage: types.StageBacklog}
	other := &Task{WorkspaceID: testWS, ID: "task-other", ProjectIDs: []string{"proj-b"}, Title: "other", Stage: types.StageBacklog}
	shared := &Task{WorkspaceID: testWS, ID: "task-shared", ProjectIDs: []string{"proj-b", "proj-a"}, Title: "shared", Stage: types.StageDone}
	for _, tk := range []*Task{mine, other, shared} {
		if err := s.Create(ctx, tk); err != nil {
			t.Fatalf("Create(%s): %v", tk.ID, err)
		}
	}
	if err := s.RecordEvent(ctx, "task-mine", EventAgentRunStarted, "", "octopus", "run", nil); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	m, err := s.ProjectFlow(ctx, "proj-a", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ProjectFlow: %v", err)
	}
	if m.TasksTotal != 2 {
		t.Errorf("TasksTotal = %d, want 2 (mine + shared, not other)", m.TasksTotal)
	}
	if m.TasksDone != 1 || m.TasksOpen != 1 {
		t.Errorf("done %d open %d, want 1/1", m.TasksDone, m.TasksOpen)
	}
	if m.AgentRuns != 1 {
		t.Errorf("AgentRuns = %d, want 1", m.AgentRuns)
	}

	empty, err := s.ProjectFlow(ctx, "proj-nothing", time.Now().Add(-48*time.Hour), time.Now())
	if err != nil {
		t.Fatalf("ProjectFlow(empty): %v", err)
	}
	if empty.TasksTotal != 0 {
		t.Errorf("TasksTotal = %d, want 0", empty.TasksTotal)
	}
	if len(empty.Throughput) == 0 {
		t.Error("empty project should still get zero-filled day buckets")
	}
}

func TestFirstPassIgnoresWindowForRework(t *testing.T) {
	s := flowFixture(t)
	old := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(48 * time.Hour)
	doneAt := from.Add(time.Hour)

	tasks := []flowTask{
		{id: "clean", stage: types.StageDone, created: old},
		{id: "kicked", stage: types.StageDone, created: old},
	}
	events := map[string][]flowEvent{
		"clean": {
			{taskID: "clean", kind: EventStageEntered, from: types.StageBacklog, to: types.StageInProgress, at: old},
			{taskID: "clean", kind: EventStageEntered, from: types.StageInProgress, to: types.StageDone, at: doneAt},
		},
		"kicked": {
			{taskID: "kicked", kind: EventStageEntered, from: types.StageTesting, to: types.StageInProgress, at: old},
			{taskID: "kicked", kind: EventStageEntered, from: types.StageInProgress, to: types.StageDone, at: doneAt},
		},
	}

	m := s.computeFlow(tasks, events, from, to)

	if m.TasksCompleted != 2 {
		t.Fatalf("TasksCompleted = %d, want 2", m.TasksCompleted)
	}
	if m.FirstPassTasks != 1 {
		t.Errorf("FirstPassTasks = %d, want 1 (only the clean one)", m.FirstPassTasks)
	}
	if m.Kickbacks != 0 {
		t.Errorf("Kickbacks = %d, want 0 (the kickback predates the window)", m.Kickbacks)
	}
}

func TestProjectFlowHumanIntervention(t *testing.T) {
	s := flowFixture(t)
	ctx := context.Background()

	tk := &Task{WorkspaceID: testWS, ID: "hi-1", ProjectIDs: []string{"proj-hi"}, Title: "t", Stage: types.StageBacklog}
	if err := s.Create(ctx, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.AddComment(ctx, "hi-1", SourceUser, "me", "please fix the naming"); err != nil {
		t.Fatalf("AddComment(user): %v", err)
	}
	if _, err := s.AddComment(ctx, "hi-1", SourceAgent, "developer", "done"); err != nil {
		t.Fatalf("AddComment(agent): %v", err)
	}
	if _, err := s.AddComment(ctx, "hi-1", SourceSystem, "octopus", "stage moved"); err != nil {
		t.Fatalf("AddComment(system): %v", err)
	}

	m, err := s.ProjectFlow(ctx, "proj-hi", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ProjectFlow: %v", err)
	}
	if m.HumanComments != 1 {
		t.Errorf("HumanComments = %d, want 1 (agent/system comments must not count)", m.HumanComments)
	}
	if m.TasksTouched != 1 {
		t.Errorf("TasksTouched = %d, want 1", m.TasksTouched)
	}
	if m.HumanInterventionRate != 1 {
		t.Errorf("HumanInterventionRate = %v, want 1", m.HumanInterventionRate)
	}
}

func TestFirstPassRateZeroWithoutCompletions(t *testing.T) {
	s := flowFixture(t)
	ctx := context.Background()
	tk := &Task{WorkspaceID: testWS, ID: "fp-open", ProjectIDs: []string{"proj-fp"}, Title: "t", Stage: types.StageBacklog}
	if err := s.Create(ctx, tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, err := s.ProjectFlow(ctx, "proj-fp", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ProjectFlow: %v", err)
	}
	if m.FirstPassRate != 0 || m.FirstPassTasks != 0 {
		t.Errorf("first pass = %d/%v, want 0/0", m.FirstPassTasks, m.FirstPassRate)
	}
}
