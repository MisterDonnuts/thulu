package stage

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

// web/src/lib/stage-skips.ts mirrors this table.
func TestSkippableReason(t *testing.T) {
	cases := []struct {
		name string
		info types.StageInfo
		want string
	}{
		{"plain column", types.StageInfo{ID: "dev"}, ""},
		{"lock_tests", types.StageInfo{ID: "design", LockTests: true, PushBranch: true}, ""},
		{"agent_verdict", types.StageInfo{ID: "verify", AgentVerdict: true}, ""},
		{"skip_unless", types.StageInfo{ID: "qa", SkipUnless: []string{"ui"}}, ""},
		{"initial", types.StageInfo{ID: "backlog", Initial: true}, SkipWhyInitial},
		{"terminal", types.StageInfo{ID: "done", Terminal: true}, SkipWhyTerminal},
		{"virtual", types.StageInfo{ID: "mention", Virtual: true}, SkipWhyVirtual},
		{"no_skip", types.StageInfo{ID: "review", NoSkip: true}, SkipWhyNoSkip},
		{"ingest_flags", types.StageInfo{ID: "refinement", IngestFlags: true}, SkipWhyRefinement},
		{"approvals", types.StageInfo{ID: "gate", RequiredApprovals: []types.ApprovalRequirement{{Key: "qa"}}}, SkipWhyApprovals},
		{"open_pr", types.StageInfo{ID: "pr", OpenPR: true}, SkipWhyPullRequest},
		{"pr_draft", types.StageInfo{ID: "verification", PRDraft: true}, SkipWhyPullRequest},
		{"pr_ready", types.StageInfo{ID: "review", PRReady: true}, SkipWhyPullRequest},
		{"merge_target", types.StageInfo{ID: "release", MergeTarget: true}, SkipWhyPullRequest},
		{"release_resources", types.StageInfo{ID: "ship", ReleaseResources: true}, SkipWhyPullRequest},
	}
	for _, c := range cases {
		if got := SkippableReason(c.info); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// FlowHash marshals whole columns: a board that never set no_skip must hash as it did before the
// flag existed, or every installed flow would read as edited.
func TestNoSkipLeavesUnsetColumnsJSONAlone(t *testing.T) {
	raw, err := json.Marshal(types.StageInfo{ID: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "no_skip") {
		t.Errorf("a column without no_skip marshals it: %s", raw)
	}
}
