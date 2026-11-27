package deploycontents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestMain(m *testing.M) {
	if err := testgit.IsolateHostConfig(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// history is a repository with:
//
//	init ── A (task-a) ── B (task-b) ── R (reverts A) ── C
//	   └── X (task-x, on a branch never merged)
type history struct {
	dir                 string
	init, a, b, r, c, x string
}

func newHistory(t *testing.T) history {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(file, msg string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(msg+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", ".")
		run("commit", "-q", "-m", msg)
		return run("rev-parse", "HEAD")
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	h := history{dir: dir}
	h.init = commit("README.md", "init")
	run("checkout", "-q", "-b", "side")
	h.x = commit("x.txt", "task x")
	run("checkout", "-q", "main")
	h.a = commit("a.txt", "task a")
	h.b = commit("b.txt", "task b")
	run("revert", "--no-edit", h.a)
	h.r = run("rev-parse", "HEAD")
	h.c = commit("c.txt", "task c")
	return h
}

func (h history) cands() []Candidate {
	return []Candidate{
		{TaskID: "task-a", MergeSHA: h.a},
		{TaskID: "task-b", MergeSHA: h.b[:10]},
		{TaskID: "task-x", MergeSHA: h.x},
		{TaskID: "task-none"},
		{TaskID: "task-ghost", MergeSHA: strings.Repeat("e", 40)},
	}
}

func byTask(rep Report) map[string]TaskResult {
	out := map[string]TaskResult{}
	for _, r := range rep.Tasks {
		out[r.TaskID] = r
	}
	return out
}

// Both sources read the same repository, so they must agree on every answer.
func sources(t *testing.T, h history) map[string]Repo {
	t.Helper()
	srv := httptest.NewServer(githubFromGit(t, h.dir))
	t.Cleanup(srv.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", srv.URL)
	return map[string]Repo{
		"git":    GitRepo{History: project.OpenHistory(h.dir)},
		"github": GitHubRepo{Token: "tok", Owner: "acme", Name: "widgets"},
	}
}

func TestBuild_IncludedAndNot(t *testing.T) {
	h := newHistory(t)
	for name, repo := range sources(t, h) {
		t.Run(name, func(t *testing.T) {
			rep, err := Build(t.Context(), repo, h.b, "", h.cands())
			if err != nil {
				t.Fatal(err)
			}
			got := byTask(rep)
			if got["task-a"].Included != IncludedYes || got["task-a"].Reverted {
				t.Errorf("task-a = %+v, want included and not reverted (the revert comes after B)", got["task-a"])
			}
			if got["task-b"].Included != IncludedYes {
				t.Errorf("task-b (abbreviated merge sha) = %+v, want included", got["task-b"])
			}
			if got["task-x"].Included != IncludedNo {
				t.Errorf("task-x (never merged into main) = %+v, want not included", got["task-x"])
			}
			if r := got["task-none"]; r.Included != IncludedUnknown || r.Reason != ReasonNoMergeSHA {
				t.Errorf("task-none = %+v, want unknown/no_merge_sha", r)
			}
			if r := got["task-ghost"]; r.Included != IncludedUnknown || r.Reason != ReasonCompareFailed || r.Detail == "" {
				t.Errorf("task-ghost = %+v, want unknown/compare_failed with the error", r)
			}
			if len(rep.Reverts) != 0 || rep.PreviousSHA != "" {
				t.Errorf("without a previous deploy: reverts %v prev %q, want none", rep.Reverts, rep.PreviousSHA)
			}
		})
	}
}

func TestBuild_RevertAndPrevious(t *testing.T) {
	h := newHistory(t)
	for name, repo := range sources(t, h) {
		t.Run(name, func(t *testing.T) {
			rep, err := Build(t.Context(), repo, h.c, h.a, h.cands())
			if err != nil {
				t.Fatal(err)
			}
			got := byTask(rep)
			a := got["task-a"]
			if a.Included != IncludedYes || !a.Reverted || len(a.RevertBy) != 1 || a.RevertBy[0] != h.r {
				t.Errorf("task-a = %+v, want included, reverted by %s", a, h.r)
			}
			if !a.AlreadyDeployed {
				t.Errorf("task-a was the previous deploy itself: already_deployed = false")
			}
			if b := got["task-b"]; b.AlreadyDeployed || b.Reverted || b.Included != IncludedYes {
				t.Errorf("task-b = %+v, want newly deployed", b)
			}
			if len(rep.Reverts) != 1 || rep.Reverts[0].SHA != h.r || rep.Reverts[0].TaskID != "task-a" ||
				rep.Reverts[0].Reverts != h.a || !strings.HasPrefix(rep.Reverts[0].Subject, `Revert "task a"`) {
				t.Errorf("range reverts = %+v, want the revert of task-a", rep.Reverts)
			}
			if rep.RangeNotLinear || rep.RangePartial || rep.RangeError != "" {
				t.Errorf("range flags = %+v", rep)
			}
		})
	}
}

// Reverting the revert ("Reapply") puts the change back: the task is live again. A third revert takes
// it out once more.
func TestBuild_RevertOfRevert(t *testing.T) {
	h := newHistory(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = h.dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("revert", "--no-edit", h.r)
	reapply := git("rev-parse", "HEAD")
	git("revert", "--no-edit", reapply)
	again := git("rev-parse", "HEAD")
	for name, repo := range sources(t, h) {
		t.Run(name, func(t *testing.T) {
			rep, err := Build(t.Context(), repo, reapply, "", h.cands())
			if err != nil {
				t.Fatal(err)
			}
			if a := byTask(rep)["task-a"]; a.Included != IncludedYes || a.Reverted || len(a.RevertBy) != 0 {
				t.Errorf("task-a after the reapply = %+v, want included and not reverted", a)
			}
			rep, err = Build(t.Context(), repo, again, "", h.cands())
			if err != nil {
				t.Fatal(err)
			}
			if a := byTask(rep)["task-a"]; !a.Reverted || len(a.RevertBy) != 1 || a.RevertBy[0] != h.r {
				t.Errorf("task-a after reverting the reapply = %+v, want reverted by %s", a, h.r)
			}
		})
	}
}

func TestBuild_PreviousNotAncestor(t *testing.T) {
	h := newHistory(t)
	for name, repo := range sources(t, h) {
		t.Run(name, func(t *testing.T) {
			// Deploying B after C is a rollback: C is not in B's history.
			rep, err := Build(t.Context(), repo, h.b, h.c, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !rep.RangeNotLinear {
				t.Errorf("rollback range: range_not_linear = false")
			}
		})
	}
}

func TestBuild_RequiresDeployedSHA(t *testing.T) {
	if _, err := Build(context.Background(), fakeRepo{}, "", "", nil); err == nil {
		t.Fatal("an empty deployed sha went through")
	}
}

type fakeRepo struct {
	compare func(base, head string, commits bool) (Comparison, error)
}

func (f fakeRepo) Compare(_ context.Context, base, head string, commits bool) (Comparison, error) {
	return f.compare(base, head, commits)
}

func TestBuild_PartialHistory(t *testing.T) {
	repo := fakeRepo{compare: func(base, head string, commits bool) (Comparison, error) {
		if base == "range00" {
			return Comparison{}, errors.New("boom")
		}
		return Comparison{Ancestor: true, Complete: false, Commits: []Commit{{SHA: "c1", Message: "unrelated"}}}, nil
	}}
	rep, err := Build(context.Background(), repo, "deploy0", "range00", []Candidate{{TaskID: "t", MergeSHA: "aaaaaaa"}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.RangeError != "boom" {
		t.Errorf("range error = %q, want boom", rep.RangeError)
	}
	if r := rep.Tasks[0]; r.Included != IncludedYes || !r.RevertsPartial {
		t.Errorf("task = %+v, want included with reverts_partial", r)
	}
}

func TestIncludedJSON(t *testing.T) {
	b, err := json.Marshal([]Included{IncludedYes, IncludedNo, IncludedUnknown})
	if err != nil || string(b) != `[true,false,"unknown"]` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	var back []Included
	if err := json.Unmarshal(b, &back); err != nil || back[0] != IncludedYes || back[1] != IncludedNo || back[2] != IncludedUnknown {
		t.Fatalf("unmarshal = %v, %v", back, err)
	}
}

func TestRevertedSHA(t *testing.T) {
	for msg, want := range map[string]string{
		"Revert \"x\"\n\nThis reverts commit 0123456789ABCDEF0123456789abcdef01234567.": "0123456789abcdef0123456789abcdef01234567",
		"Revert x\n\nThis reverts commit abc1234 by hand":                               "abc1234",
		"fix: mention reverts commit abc1234 in passing":                                "",
		"This reverts commit abc12":                                                     "",
	} {
		if got := RevertedSHA(msg); got != want {
			t.Errorf("RevertedSHA(%q) = %q, want %q", msg, got, want)
		}
	}
}

// githubFromGit answers GitHub's compare endpoint from a real repository, so the GitHub source is
// checked against the same history as the git one.
func githubFromGit(t *testing.T, dir string) http.Handler {
	t.Helper()
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		basehead, ok := strings.CutPrefix(r.URL.Path, "/repos/acme/widgets/compare/")
		base, head, cut := strings.Cut(basehead, "...")
		if !ok || !cut {
			http.NotFound(w, r)
			return
		}
		fullBase, err1 := git("rev-parse", "--verify", base+"^{commit}")
		fullHead, err2 := git("rev-parse", "--verify", head+"^{commit}")
		if err1 != nil || err2 != nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		_, ancErr := git("merge-base", "--is-ancestor", fullBase, fullHead)
		_, backErr := git("merge-base", "--is-ancestor", fullHead, fullBase)
		status := "diverged"
		switch {
		case fullBase == fullHead:
			status = "identical"
		case ancErr == nil:
			status = "ahead"
		case backErr == nil:
			status = "behind"
		}
		out, _ := git("log", "--reverse", "--format=%H%x1f%B%x1e", fullBase+".."+fullHead)
		type commit struct {
			SHA    string            `json:"sha"`
			Commit map[string]string `json:"commit"`
		}
		var commits []commit
		for _, rec := range strings.Split(out, "\x1e") {
			sha, msg, ok := strings.Cut(strings.TrimLeft(rec, "\n"), "\x1f")
			if ok {
				commits = append(commits, commit{SHA: sha, Commit: map[string]string{"message": strings.TrimSpace(msg)}})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "total_commits": len(commits), "commits": commits})
	})
}
