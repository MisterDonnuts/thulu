package task

import "testing"

func TestInstanceOfTask(t *testing.T) {
	const id = "0000000000000c02"
	for _, c := range []struct {
		instance string
		want     bool
	}{
		{"developer-task-" + id + "-in_progress-17", true},
		{"developer-audit-" + id + "-review-17", true},
		{"developer-mention-" + id + "-abcd1234-17", true},
		{"developer-task-ffff000011112222-in_progress-17", false},
		{"developer-mention-ffff000011112222-abcd1234-17", false},
		{id, false},
		{"", false},
	} {
		if got := InstanceOfTask(c.instance, id); got != c.want {
			t.Errorf("InstanceOfTask(%q) = %v, want %v", c.instance, got, c.want)
		}
	}
}

func TestInstanceStage(t *testing.T) {
	const id = "0000000000000c02"
	for _, c := range []struct {
		instance string
		want     Stage
		ok       bool
	}{
		{"developer-task-" + id + "-in_progress-17", "in_progress", true},
		{"developer-audit-" + id + "-review-17", "review", true},
		{"qa-mention-" + id + "-abcd1234-17", "mention", true},
		{"", "", false},
		{"whatever", "", false},
	} {
		got, ok := InstanceStage(c.instance)
		if got != c.want || ok != c.ok {
			t.Errorf("InstanceStage(%q) = %q, %v; want %q, %v", c.instance, got, ok, c.want, c.ok)
		}
	}
}
