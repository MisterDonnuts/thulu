package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		raw          string
		owner, repo  string
		number       int
		ok           bool
		withGHESHost bool
	}{
		{raw: "https://github.com/acme/widgets/pull/53", owner: "acme", repo: "widgets", number: 53, ok: true},
		{raw: " https://github.com/acme/widgets/pull/7/ ", owner: "acme", repo: "widgets", number: 7, ok: true},
		{raw: "https://github.com/acme/widgets/issues/7"},
		{raw: "https://github.com/acme/widgets/pull/0"},
		{raw: "https://github.com/acme/widgets/pull/7?x=1"},
		{raw: "https://github.com/acme/widgets/pull/7/files"},
		{raw: "https://evil.example.com/acme/widgets/pull/7"},
		{raw: "https://ghe.example.com/acme/widgets/pull/9", owner: "acme", repo: "widgets", number: 9, ok: true, withGHESHost: true},
	}
	for _, c := range cases {
		if c.withGHESHost {
			t.Setenv("OCTOPUS_GITHUB_URL", "https://ghe.example.com")
		}
		owner, repo, n, ok := ParsePRURL(c.raw)
		if ok != c.ok || owner != c.owner || repo != c.repo || n != c.number {
			t.Errorf("ParsePRURL(%q) = %q %q %d %v, want %q %q %d %v", c.raw, owner, repo, n, ok, c.owner, c.repo, c.number, c.ok)
		}
	}
}

func TestReviewStateOf(t *testing.T) {
	rev := func(login, state string) Review {
		var r Review
		r.User.Login = login
		r.State = state
		return r
	}
	cases := []struct {
		name      string
		reviews   []Review
		requested bool
		want      string
	}{
		{"nothing", nil, false, ReviewNone},
		{"requested, nobody answered", nil, true, ReviewPending},
		{"only comments", []Review{rev("ana", "COMMENTED")}, true, ReviewPending},
		{"approved", []Review{rev("ana", "APPROVED")}, false, ReviewApproved},
		{"a comment after an approval keeps it", []Review{rev("ana", "APPROVED"), rev("ana", "COMMENTED")}, false, ReviewApproved},
		{"changes then approval", []Review{rev("ana", "CHANGES_REQUESTED"), rev("ana", "APPROVED")}, false, ReviewApproved},
		{"approval then changes", []Review{rev("ana", "APPROVED"), rev("ana", "CHANGES_REQUESTED")}, false, ReviewChangesRequested},
		{"one reviewer blocks", []Review{rev("ana", "APPROVED"), rev("bo", "CHANGES_REQUESTED")}, false, ReviewChangesRequested},
		{"dismissed withdraws the verdict", []Review{rev("ana", "CHANGES_REQUESTED"), rev("ana", "DISMISSED")}, true, ReviewPending},
		{"a draft review is nobody's verdict", []Review{rev("ana", "PENDING")}, false, ReviewNone},
		{"logins compare without case", []Review{rev("Ana", "CHANGES_REQUESTED"), rev("ana", "APPROVED")}, false, ReviewApproved},
		{"approval wins over an open request", []Review{rev("ana", "APPROVED")}, true, ReviewApproved},
	}
	for _, c := range cases {
		if got := ReviewStateOf(c.reviews, c.requested); got != c.want {
			t.Errorf("%s: ReviewStateOf = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMergeableAndStateOf(t *testing.T) {
	pr := func(state, mergeable string, draft, merged bool) *PullRequest {
		return &PullRequest{State: state, MergeableState: mergeable, Draft: draft, Merged: merged}
	}
	cases := []struct {
		pr               *PullRequest
		mergeable, state string
	}{
		{pr("open", "clean", false, false), MergeableReady, PRStateOpen},
		{pr("open", "unstable", false, false), MergeableReady, PRStateOpen},
		{pr("open", "has_hooks", false, false), MergeableReady, PRStateOpen},
		{pr("open", "blocked", false, false), MergeableBlocked, PRStateOpen},
		{pr("open", "behind", false, false), MergeableBehind, PRStateOpen},
		{pr("open", "dirty", false, false), MergeableConflicts, PRStateOpen},
		{pr("open", "unknown", false, false), MergeableUnknown, PRStateOpen},
		{pr("open", "", false, false), MergeableUnknown, PRStateOpen},
		{pr("open", "draft", true, false), MergeableBlocked, PRStateDraft},
		{pr("closed", "clean", false, true), MergeableUnknown, PRStateMerged},
		{pr("closed", "clean", false, false), MergeableUnknown, PRStateClosed},
	}
	for _, c := range cases {
		if got := MergeableOf(c.pr); got != c.mergeable {
			t.Errorf("MergeableOf(%+v) = %q, want %q", *c.pr, got, c.mergeable)
		}
		if got := StateOf(c.pr); got != c.state {
			t.Errorf("StateOf(%+v) = %q, want %q", *c.pr, got, c.state)
		}
	}
}

func TestCheckRunsCI_Counts(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/commits/abc123/check-runs" || r.URL.Query().Get("filter") != "latest" {
			t.Errorf("unexpected request %s", r.URL)
		}
		fmt.Fprint(w, `{"total_count":8,"check_runs":[
			{"status":"completed","conclusion":"success"},
			{"status":"completed","conclusion":"neutral"},
			{"status":"completed","conclusion":"skipped"},
			{"status":"completed","conclusion":"failure"},
			{"status":"completed","conclusion":"timed_out"},
			{"status":"in_progress","conclusion":null},
			{"status":"queued","conclusion":null},
			{"status":"completed","conclusion":"success"}]}`)
	})
	ci, err := CheckRunsCI(context.Background(), "tok", "acme", "widgets", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := CIStatus{Source: CISourceChecks, Total: 8, Passed: 4, Failed: 2, Running: 2, State: CIStateRunning}
	if ci != want {
		t.Errorf("CheckRunsCI = %+v, want %+v", ci, want)
	}
}

func TestCIStatusSettle(t *testing.T) {
	cases := []struct {
		in   CIStatus
		want string
	}{
		{CIStatus{}, CIStateNone},
		{CIStatus{Total: 2, Passed: 2}, CIStatePassed},
		{CIStatus{Total: 2, Passed: 1, Failed: 1}, CIStateFailed},
		{CIStatus{Total: 3, Passed: 1, Failed: 1, Running: 1}, CIStateRunning},
	}
	for _, c := range cases {
		c.in.settle()
		if c.in.State != c.want {
			t.Errorf("settle(%+v) = %q, want %q", c.in, c.in.State, c.want)
		}
	}
}

type fakePR struct {
	mu       sync.Mutex
	checks   bool
	requests []string
}

func (f *fakePR) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.Path)
	f.mu.Unlock()
	switch p := r.URL.Path; {
	case p == "/repos/acme/widgets/pulls/7":
		fmt.Fprint(w, `{"number":7,"title":"Fix login","state":"open","draft":false,"merged":false,
			"mergeable_state":"clean","html_url":"https://github.com/acme/widgets/pull/7",
			"head":{"ref":"octopus/t1","sha":"abc123"},"base":{"ref":"main"},
			"changed_files":5,"additions":120,"deletions":30,"commits":3,
			"updated_at":"2026-10-06T10:00:00Z","requested_reviewers":[{"login":"bo"}],"requested_teams":[]}`)
	case p == "/repos/acme/widgets/pulls/7/reviews":
		fmt.Fprint(w, `[{"user":{"login":"ana"},"state":"APPROVED"}]`)
	case strings.HasSuffix(p, "/check-runs"):
		if !f.checks {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
			return
		}
		fmt.Fprint(w, `{"total_count":1,"check_runs":[{"status":"completed","conclusion":"success"}]}`)
	case p == "/repos/acme/widgets/actions/runs":
		if r.URL.Query().Get("head_sha") != "abc123" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"workflow_runs":[{"id":11,"status":"in_progress"},{"id":12,"status":"queued"}]}`)
	case p == "/repos/acme/widgets/actions/runs/11/jobs":
		fmt.Fprint(w, `{"jobs":[{"status":"completed","conclusion":"success"},{"status":"in_progress","conclusion":null},{"status":"completed","conclusion":"cancelled"}]}`)
	case p == "/repos/acme/widgets/actions/runs/12/jobs":
		fmt.Fprint(w, `{"jobs":[]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}
}

func TestFetchPRStatus_WithChecks(t *testing.T) {
	f := &fakePR{checks: true}
	fakeAPI(t, f.ServeHTTP)
	st, err := FetchPRStatus(context.Background(), "tok", "acme", "widgets", 7, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Number != 7 || st.Title != "Fix login" || st.State != PRStateOpen || st.Head != "octopus/t1" ||
		st.Base != "main" || st.HeadSHA != "abc123" || st.ChangedFiles != 5 || st.Additions != 120 ||
		st.Deletions != 30 || st.Commits != 3 || st.Mergeable != MergeableReady || st.Review != ReviewApproved {
		t.Errorf("status = %+v", st)
	}
	want := CIStatus{Source: CISourceChecks, Total: 1, Passed: 1, State: CIStatePassed}
	if st.CI != want {
		t.Errorf("CI = %+v, want %+v", st.CI, want)
	}
}

func TestFetchPRStatus_FallsBackToActions(t *testing.T) {
	for _, checks := range []bool{true, false} {
		f := &fakePR{checks: false}
		fakeAPI(t, f.ServeHTTP)
		st, err := FetchPRStatus(context.Background(), "tok", "acme", "widgets", 7, checks)
		if err != nil {
			t.Fatal(err)
		}
		want := CIStatus{Source: CISourceActions, Total: 4, Passed: 1, Failed: 1, Running: 2, State: CIStateRunning}
		if st.CI != want {
			t.Errorf("checks=%v: CI = %+v, want %+v", checks, st.CI, want)
		}
		asked := false
		for _, p := range f.requests {
			asked = asked || strings.HasSuffix(p, "/check-runs")
		}
		if asked != checks {
			t.Errorf("checks=%v: asked for check runs = %v", checks, asked)
		}
	}
}

func TestFetchPRStatus_MissingPRFails(t *testing.T) {
	fakeAPI(t, (&fakePR{}).ServeHTTP)
	if _, err := FetchPRStatus(context.Background(), "tok", "acme", "widgets", 99, true); err == nil {
		t.Fatal("a pull request GitHub does not know came back without an error")
	}
}
