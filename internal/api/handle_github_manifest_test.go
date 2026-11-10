package api

import (
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/githubapp"
)

func TestManifestSubscribesToEveryEventTheDaemonReads(t *testing.T) {
	m, _, err := githubapp.BuildManifest(githubapp.ManifestOptions{BaseURL: "https://thulu.example.com"})
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	needsMore := map[string]bool{"issue_comment": true, "deployment_status": true}

	read := map[string]bool{}
	for ev := range prStatusEvents {
		read[ev] = true
	}
	for _, ev := range []string{
		"pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment",
		"push", "check_suite", "check_run", "workflow_run", "workflow_job", "deployment_status",
		"issues", "release", "create", "delete", "status", "installation", "installation_repositories",
	} {
		if githubEventKind(ev) != "" {
			read[ev] = true
		}
	}
	for ev := range read {
		if needsMore[ev] {
			continue
		}
		if !slices.Contains(m.DefaultEvents, ev) {
			t.Errorf("the daemon reads %q but the manifest does not subscribe to it (events %v)", ev, m.DefaultEvents)
		}
	}
}
