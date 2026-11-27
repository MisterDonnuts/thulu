package api

import (
	"context"
	"slices"
	"testing"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/task"
)

type taskContainersFake struct {
	list    []dockertypes.Container
	removed []string
	filter  []string
}

func (f *taskContainersFake) ContainerList(_ context.Context, o container.ListOptions) ([]dockertypes.Container, error) {
	f.filter = o.Filters.Get("label")
	return f.list, nil
}

func (f *taskContainersFake) ContainerRemove(_ context.Context, id string, o container.RemoveOptions) error {
	if o.Force {
		f.removed = append(f.removed, id)
	}
	return nil
}

func TestRemoveTaskContainersStopsAgentsAndMentionsButNotChats(t *testing.T) {
	const task = "t-1"
	f := &taskContainersFake{list: []dockertypes.Container{
		{ID: "aaaaaaaaaaaaaaaa", Labels: map[string]string{agent.LabelTask: task, agent.LabelManaged: "true", agent.LabelInstance: "t-1-dev-1"}},
		{ID: "bbbbbbbbbbbbbbbb", Labels: map[string]string{agent.LabelTask: task, agent.LabelManaged: "true", agent.LabelInstance: "t-1-mention-2"}},
		{ID: "cccccccccccccccc", Labels: map[string]string{agent.LabelTask: task, agent.LabelChat: "true"}},
		{ID: "dddddddddddddddd", Labels: map[string]string{agent.LabelTask: task}},
	}}
	var stopped, instances []string
	got := removeTaskContainers(context.Background(), f, task, func(id, inst string) {
		stopped = append(stopped, id)
		instances = append(instances, inst)
	})

	if !slices.Equal(f.filter, []string{agent.LabelTask + "=" + task}) {
		t.Fatalf("listed by %v, want the task label", f.filter)
	}
	if got != 3 || !slices.Equal(f.removed, []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "dddddddddddddddd"}) {
		t.Fatalf("removed %d %v; the chat must stay, everything else of the task goes", got, f.removed)
	}
	if !slices.Equal(stopped, []string{shortContainerID("aaaaaaaaaaaaaaaa"), shortContainerID("bbbbbbbbbbbbbbbb")}) {
		t.Fatalf("marked stopped %v: want the two agent runs, so their exit callbacks know who killed them", stopped)
	}
	if !slices.Equal(instances, []string{"t-1-dev-1", "t-1-mention-2"}) {
		t.Fatalf("stopped instances %v: the subtask's teardown reaps the parent's sandbox by them", instances)
	}
	if removeTaskContainers(context.Background(), f, "", func(string, string) {}) != 0 {
		t.Fatal("an empty task id listed every container")
	}
}

type subtaskReaperFake struct{ calls []string }

func (f *subtaskReaperFake) ReapTask(_ context.Context, owner, taskID string) error {
	f.calls = append(f.calls, "task "+owner+" "+taskID)
	return nil
}

func (f *subtaskReaperFake) ReapRun(_ context.Context, owner, inst string) error {
	f.calls = append(f.calls, "run "+owner+" "+inst)
	return nil
}

// A subtask's runs work in its parent's sandbox, which outlives the subtask. Deleting the subtask
// kills there what its runs left, by the task marker and by each stopped run's instance.
func TestReapSubtaskInTargetsTheParentsSandbox(t *testing.T) {
	f := &subtaskReaperFake{}
	reapSubtaskIn(context.Background(), f, &task.Task{ID: "sub1", ParentID: "root1"}, []string{"dev-task-sub1-dev-1"})
	want := []string{"task root1 sub1", "run root1 dev-task-sub1-dev-1"}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
}
