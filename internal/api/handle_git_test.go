package api

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestGitAuthorize(t *testing.T) {
	lookup := func(_ context.Context, k string) (string, bool) {
		switch k {
		case "good-token":
			return "task42", true
		case "wrong-scope":
			return "task99", true
		}
		return "", false
	}
	cases := []struct {
		name     string
		repoPath string
		pass     string
		want     bool
	}{
		{"valid scoped token", "task42/app", "good-token", true},
		{"scope mismatch", "task42/app", "wrong-scope", false},
		{"invalid token", "task42/app", "nope", false},
		{"no credentials", "task42/app", "", false},
		{"empty repo path", "", "good-token", false},
		{"malformed: task only, no project", "task42", "good-token", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if c.pass != "" {
				r.SetBasicAuth("octopus", c.pass)
			}
			if got := gitAuthorize(lookup, r, c.repoPath); got != c.want {
				t.Errorf("gitAuthorize(%q, pass=%q) = %v, want %v", c.repoPath, c.pass, got, c.want)
			}
		})
	}
}

func TestGitResolveRepo(t *testing.T) {
	paths := func(_ context.Context, taskID string) []string {
		if taskID == "task42" {
			return []string{"/data/projects/webshop", "/data/projects/other"}
		}
		return nil
	}
	cases := []struct {
		name     string
		repoPath string
		wantDir  string
		wantOK   bool
	}{
		{"match", "task42/webshop", "/data/projects/webshop", true},
		{"second project", "task42/other", "/data/projects/other", true},
		{"unknown project", "task42/ghost", "", false},
		{"unknown task", "task99/webshop", "", false},
		{"no project segment", "task42", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, ok := gitResolveRepo(t.Context(), paths, c.repoPath)
			if ok != c.wantOK || dir != c.wantDir {
				t.Errorf("gitResolveRepo(%q) = (%q, %v), want (%q, %v)", c.repoPath, dir, ok, c.wantDir, c.wantOK)
			}
		})
	}
}
