package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func TestGetPR_MergeCommitSHA(t *testing.T) {
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/7":
			fmt.Fprint(w, `{"number":7,"state":"closed","merged":true,"merge_commit_sha":"abc1234def",
				"merged_at":"2026-11-17T10:00:00Z","merged_by":{"login":"ana"},"base":{"ref":"main"}}`)
		case "/repos/acme/widgets/pulls/8":
			// An open PR carries GitHub's test merge commit; it is not a merge.
			fmt.Fprint(w, `{"number":8,"state":"open","merged":false,"merge_commit_sha":"feedbeef"}`)
		default:
			http.NotFound(w, r)
		}
	})
	pr, err := GetPR(context.Background(), "tok", "acme", "widgets", 7)
	if err != nil {
		t.Fatal(err)
	}
	if MergeSHAOf(pr) != "abc1234def" || pr.MergedByLogin() != "ana" || pr.MergedAt == nil || pr.Base.Ref != "main" {
		t.Errorf("merged PR = sha %q by %q at %v base %q", MergeSHAOf(pr), pr.MergedByLogin(), pr.MergedAt, pr.Base.Ref)
	}
	open, err := GetPR(context.Background(), "tok", "acme", "widgets", 8)
	if err != nil {
		t.Fatal(err)
	}
	if MergeSHAOf(open) != "" || open.MergedByLogin() != "" {
		t.Errorf("open PR merge sha = %q, want empty", MergeSHAOf(open))
	}
}

func TestCompare(t *testing.T) {
	var paths []string
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		base, head, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/repos/acme/widgets/compare/"), "...")
		switch {
		case base == "aaaaaaa" && head == "bbbbbbb":
			fmt.Fprint(w, `{"status":"ahead","total_commits":1,"commits":[{"sha":"b1","commit":{"message":"feat"}}]}`)
		case base == "bbbbbbb" && head == "aaaaaaa":
			fmt.Fprint(w, `{"status":"behind","total_commits":0,"commits":[]}`)
		case base == "ccccccc":
			fmt.Fprint(w, `{"status":"identical","total_commits":0,"commits":[]}`)
		case base == "ddddddd":
			fmt.Fprint(w, `{"status":"diverged","total_commits":2,"commits":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"No common ancestor"}`)
		}
	})
	ctx := context.Background()
	for _, c := range []struct {
		base, head string
		ancestor   bool
	}{
		{"aaaaaaa", "bbbbbbb", true},
		{"bbbbbbb", "aaaaaaa", false},
		{"ccccccc", "ccccccc", true},
		{"ddddddd", "bbbbbbb", false},
	} {
		got, err := Compare(ctx, "tok", "acme", "widgets", c.base, c.head, 1)
		if err != nil {
			t.Fatalf("%s...%s: %v", c.base, c.head, err)
		}
		if got.BaseIsAncestor() != c.ancestor {
			t.Errorf("%s...%s: status %q ancestor = %v, want %v", c.base, c.head, got.Status, got.BaseIsAncestor(), c.ancestor)
		}
	}
	got, _ := Compare(ctx, "tok", "acme", "widgets", "aaaaaaa", "bbbbbbb", 1)
	if len(got.Commits) != 1 || got.Commits[0].Message != "feat" || !got.Complete {
		t.Errorf("commits = %+v complete %v", got.Commits, got.Complete)
	}
	if _, err := Compare(ctx, "tok", "acme", "widgets", "eeeeeee", "bbbbbbb", 1); apperr.KindOf(err) != apperr.KindNotFound {
		t.Errorf("unknown commit: err = %v, want not found", err)
	}
	before := len(paths)
	if _, err := Compare(ctx, "tok", "acme", "widgets", "main", "bbbbbbb", 1); err == nil {
		t.Error("a branch name went through as a commit")
	}
	if _, err := Compare(ctx, "tok", "acme", "widgets", "aaaaaaa", "../../x", 1); err == nil {
		t.Error("a path went through as a commit")
	}
	if len(paths) != before {
		t.Errorf("refused refs still called GitHub: %v", paths[before:])
	}
}

func TestComparePages(t *testing.T) {
	const total = 250
	var pages int
	fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		page := r.URL.Query().Get("page")
		var n int
		switch page {
		case "1", "2":
			n = 100
		case "3":
			n = 50
		}
		var b strings.Builder
		for i := range n {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"sha":"p%s-%d","commit":{"message":"m"}}`, page, i)
		}
		fmt.Fprintf(w, `{"status":"ahead","total_commits":%d,"commits":[%s]}`, total, b.String())
	})
	got, err := Compare(context.Background(), "tok", "acme", "widgets", "aaaaaaa", "bbbbbbb", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commits) != 200 || got.Complete || pages != 2 {
		t.Errorf("2 pages: %d commits, complete %v, %d calls; want 200, false, 2", len(got.Commits), got.Complete, pages)
	}
	pages = 0
	got, _ = Compare(context.Background(), "tok", "acme", "widgets", "aaaaaaa", "bbbbbbb", 5)
	if len(got.Commits) != total || !got.Complete || pages != 3 {
		t.Errorf("5 pages: %d commits, complete %v, %d calls; want %d, true, 3", len(got.Commits), got.Complete, pages, total)
	}
}
