package task

import (
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func TestMentionLaneCoalescesWhileRunning(t *testing.T) {
	lane := newMentionLane()
	key := mentionKey("task1", "t1", "devops")

	claimed, _ := lane.begin(key, &Comment{ID: "c1", Body: "primera"})
	if !claimed {
		t.Fatal("first mention should claim the lane")
	}
	for _, c := range []*Comment{{ID: "c2", Body: "segunda"}, {ID: "c3", Body: "tercera"}} {
		if claimed, queued := lane.begin(key, c); claimed || queued == 0 {
			t.Fatalf("mention %s: claimed=%v queued=%d, want claimed=false and a queue", c.ID, claimed, queued)
		}
	}

	merged := lane.finish(key)
	if merged == nil {
		t.Fatal("finish should hand back the queued messages")
	}
	if merged.ID != "c3" {
		t.Errorf("merged identity = %q, want the last message c3", merged.ID)
	}
	for _, want := range []string{"segunda", "tercera"} {
		if !strings.Contains(merged.Body, want) {
			t.Errorf("merged body missing %q: %s", want, merged.Body)
		}
	}
	if strings.Contains(merged.Body, "primera") {
		t.Error("the message the finished run already answered must not be re-asked")
	}

	if claimed, _ := lane.begin(key, &Comment{ID: "c4", Body: "cuarta"}); claimed {
		t.Error("follow-up run must still hold the lane")
	}
}

func TestMentionLaneReleasesWhenQuiet(t *testing.T) {
	lane := newMentionLane()
	key := mentionKey("task1", "t1", "devops")

	lane.begin(key, &Comment{ID: "c1"})
	if merged := lane.finish(key); merged != nil {
		t.Fatalf("nothing was queued, want no follow-up run, got %+v", merged)
	}
	if claimed, _ := lane.begin(key, &Comment{ID: "c2"}); !claimed {
		t.Error("a released lane must be claimable again")
	}
}

func TestMentionLaneIsPerAgent(t *testing.T) {
	lane := newMentionLane()
	lane.begin(mentionKey("task1", "t1", "devops"), &Comment{ID: "c1"})
	if claimed, _ := lane.begin(mentionKey("task1", "t1", "reviewer"), &Comment{ID: "c2"}); !claimed {
		t.Error("a different agent on the same task answers at the same time")
	}
	if claimed, _ := lane.begin(mentionKey("task1", "t2", "devops"), &Comment{ID: "c3"}); !claimed {
		t.Error("the same agent in another thread answers at the same time")
	}
}

func TestMentionLaneAbandonReportsWhatItDrops(t *testing.T) {
	lane := newMentionLane()
	key := mentionKey("task1", "t1", "devops")
	lane.begin(key, &Comment{ID: "c1"})
	lane.begin(key, &Comment{ID: "c2"})
	if dropped := lane.abandon(key); dropped != 1 {
		t.Errorf("dropped = %d, want 1 — a launch that never happened owes the queue an explanation", dropped)
	}
	if claimed, _ := lane.begin(key, &Comment{ID: "c3"}); !claimed {
		t.Error("abandon must release the lane")
	}
}

func TestMentionLaneQueueIsBounded(t *testing.T) {
	lane := newMentionLane()
	key := mentionKey("task1", "t1", "devops")
	lane.begin(key, &Comment{ID: "c0", Body: "vieja"})
	for i := range maxPendingMentions + 3 {
		lane.begin(key, &Comment{ID: "c", Body: string(rune('a' + i))})
	}
	merged := lane.finish(key)
	if merged == nil {
		t.Fatal("want a follow-up")
	}
	if got := strings.Count(merged.Body, "---"); got > maxPendingMentions-1 {
		t.Errorf("merged %d separators, queue should cap at %d messages", got+1, maxPendingMentions)
	}
}

func TestRunFailureHintNamesTheBundleFailures(t *testing.T) {
	for _, tc := range []struct {
		exit int
		want string
	}{
		{90, "credential"},
		{91, "bundle"},
		{92, "bundle"},
		{93, "prompt"},
		{94, "NetworkPolicy"},
	} {
		if hint := apperr.Text(runFailureHint(tc.exit)); !strings.Contains(hint, tc.want) {
			t.Errorf("exit %d hint = %q, want it to mention %q", tc.exit, hint, tc.want)
		}
	}
	if hint := runFailureHint(1); hint != nil {
		t.Errorf("a plain non-zero exit is not a bundle failure, got %v", hint)
	}
}
