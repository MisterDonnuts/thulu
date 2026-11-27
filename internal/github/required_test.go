package github

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestEvaluateRequired(t *testing.T) {
	run := func(name, status, conclusion string, app int64) CheckResult {
		return CheckResult{Name: name, Status: status, Conclusion: conclusion, AppID: app}
	}
	req := func(strict bool, names ...string) Requirement {
		r := Requirement{Strict: strict}
		for _, n := range names {
			r.Contexts = append(r.Contexts, RequiredContext{Context: n})
		}
		return r
	}
	cases := []struct {
		name      string
		req       Requirement
		checks    []CheckResult
		statuses  []CommitStatus
		mergeable string
		want      RequiredStatus
	}{
		{name: "nothing required", req: Requirement{}, want: RequiredStatus{State: RequiredNone}},
		{
			name:   "all green, neutral and skipped count",
			req:    req(false, "build", "lint", "docs"),
			checks: []CheckResult{run("build", "completed", "success", 1), run("lint", "completed", "neutral", 1), run("docs", "completed", "skipped", 1)},
			want:   RequiredStatus{State: RequiredPassed, Required: []string{"build", "lint", "docs"}},
		},
		{
			name:   "a failing and a running one",
			req:    req(false, "build", "lint", "e2e"),
			checks: []CheckResult{run("build", "completed", "failure", 1), run("lint", "in_progress", "", 1), run("e2e", "completed", "success", 1)},
			want:   RequiredStatus{State: RequiredFailing, Required: []string{"build", "lint", "e2e"}, Failing: []string{"build"}, Pending: []string{"lint"}},
		},
		{
			name:   "not reported yet is pending and missing",
			req:    req(false, "build", "deploy-preview"),
			checks: []CheckResult{run("build", "completed", "success", 1)},
			want:   RequiredStatus{State: RequiredPending, Required: []string{"build", "deploy-preview"}, Pending: []string{"deploy-preview"}, Missing: []string{"deploy-preview"}},
		},
		{
			name:   "a failing run beats a passing one of the same name",
			req:    req(false, "build"),
			checks: []CheckResult{run("build", "completed", "success", 1), run("build", "completed", "timed_out", 1)},
			want:   RequiredStatus{State: RequiredFailing, Required: []string{"build"}, Failing: []string{"build"}},
		},
		{
			name:     "a commit status answers a context no check run has",
			req:      req(false, "ci/circleci", "legacy"),
			statuses: []CommitStatus{{Context: "ci/circleci", State: "success"}, {Context: "legacy", State: "error"}},
			want:     RequiredStatus{State: RequiredFailing, Required: []string{"ci/circleci", "legacy"}, Failing: []string{"legacy"}},
		},
		{
			name:   "a run from another app does not satisfy an app-pinned check",
			req:    Requirement{Contexts: []RequiredContext{{Context: "build", AppID: 15368}}},
			checks: []CheckResult{run("build", "completed", "success", 999)},
			want:   RequiredStatus{State: RequiredPending, Required: []string{"build"}, Pending: []string{"build"}, Missing: []string{"build"}},
		},
		{
			name:      "green but behind a strict base",
			req:       req(true, "build"),
			checks:    []CheckResult{run("build", "completed", "success", 1)},
			mergeable: MergeableBehind,
			want:      RequiredStatus{State: RequiredPassed, Required: []string{"build"}, Behind: true},
		},
	}
	for _, c := range cases {
		got := EvaluateRequired(c.req, c.checks, c.statuses, c.mergeable)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: EvaluateRequired = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestRequiredStatusHolds(t *testing.T) {
	for state, holds := range map[string]bool{
		RequiredNone: false, RequiredPassed: false, RequiredPending: true, RequiredFailing: true, RequiredUnknown: true,
	} {
		st := RequiredStatus{State: state}
		if st.Holds() != holds {
			t.Errorf("%s: Holds = %v, want %v", state, st.Holds(), holds)
		}
	}
	behind := RequiredStatus{State: RequiredPassed, Behind: true}
	if !behind.Holds() {
		t.Error("a branch behind a strict base did not hold")
	}
}

func TestBranchRequirement_ProtectionAndRulesets(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/branches/main":
			fmt.Fprint(w, `{"name":"main","protected":true,"protection":{"enabled":true,
				"required_status_checks":{"enforcement_level":"non_admins","contexts":["build","lint"],
				"checks":[{"context":"build","app_id":15368},{"context":"lint","app_id":null}]}}}`)
		case "/repos/acme/widgets/rules/branches/main":
			fmt.Fprint(w, `[{"type":"pull_request","parameters":{}},
				{"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":true,
				"required_status_checks":[{"context":"e2e","integration_id":42},{"context":"build","integration_id":15368}]}}]`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	got, err := BranchRequirement(context.Background(), "tok", "acme", "widgets", "main")
	if err != nil {
		t.Fatal(err)
	}
	want := Requirement{Strict: true, Contexts: []RequiredContext{{"build", 15368}, {"lint", 0}, {"e2e", 42}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BranchRequirement = %+v, want %+v", got, want)
	}
}

func TestBranchRequirement_UnreadableSourcesAskNothing(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/rules/") {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Branch not found"}`)
	})
	got, err := BranchRequirement(context.Background(), "tok", "acme", "widgets", "main")
	if err != nil || len(got.Contexts) != 0 || got.Strict {
		t.Errorf("BranchRequirement = %+v, %v; want nothing required", got, err)
	}
}

func TestBranchRequirement_ServerErrorIsUnknown(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	if _, err := BranchRequirement(context.Background(), "tok", "acme", "widgets", "main"); err == nil {
		t.Fatal("a 502 read as a branch without requirements")
	}
}

// The whole read: the PR, its check runs, the base's requirement and the commit statuses for the
// context no run answered.
func TestFetchPRStatus_RequiredChecks(t *testing.T) {
	var asked []string
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch p := r.URL.Path; p {
		case "/repos/acme/widgets/pulls/7":
			fmt.Fprint(w, `{"number":7,"state":"open","mergeable_state":"blocked",
				"head":{"ref":"octopus/t1","sha":"abc123"},"base":{"ref":"main"}}`)
		case "/repos/acme/widgets/pulls/7/reviews":
			fmt.Fprint(w, `[]`)
		case "/repos/acme/widgets/commits/abc123/check-runs":
			fmt.Fprint(w, `{"total_count":2,"check_runs":[
				{"name":"build","status":"completed","conclusion":"failure","app":{"id":15368}},
				{"name":"lint","status":"completed","conclusion":"success","app":{"id":15368}}]}`)
		case "/repos/acme/widgets/branches/main":
			fmt.Fprint(w, `{"protection":{"enabled":true,"required_status_checks":{"contexts":["build","lint","ci/legacy"],"checks":[]}}}`)
		case "/repos/acme/widgets/rules/branches/main":
			fmt.Fprint(w, `[]`)
		case "/repos/acme/widgets/commits/abc123/status":
			fmt.Fprint(w, `{"state":"pending","statuses":[{"context":"ci/legacy","state":"pending"}]}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	st, err := FetchPRStatus(context.Background(), "tok", "acme", "widgets", 7, true)
	if err != nil {
		t.Fatal(err)
	}
	want := RequiredStatus{
		State: RequiredFailing, Required: []string{"build", "lint", "ci/legacy"},
		Failing: []string{"build"}, Pending: []string{"ci/legacy"},
	}
	if !reflect.DeepEqual(st.Required, want) {
		t.Errorf("Required = %+v, want %+v (asked %v)", st.Required, want, asked)
	}
}

func TestFetchPRStatus_MergedAsksNoRequirement(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/7":
			fmt.Fprint(w, `{"number":7,"state":"closed","merged":true,"head":{"sha":"abc123"},"base":{"ref":"main"}}`)
		case "/repos/acme/widgets/pulls/7/reviews":
			fmt.Fprint(w, `[]`)
		case "/repos/acme/widgets/commits/abc123/check-runs":
			fmt.Fprint(w, `{"total_count":0,"check_runs":[]}`)
		default:
			t.Errorf("a merged PR read %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	st, err := FetchPRStatus(context.Background(), "tok", "acme", "widgets", 7, true)
	if err != nil || st.Required.State != RequiredNone {
		t.Errorf("Required = %+v, %v; want none", st.Required, err)
	}
}
