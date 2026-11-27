package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNormalizeGatesProfile(t *testing.T) {
	ok := []struct{ in, want string }{
		{``, ``},
		{`null`, ``},
		{` {} `, ``},
		{`{"coverage_min": 60, "timeouts": {"tests": 2400, "test_stall": 0}}`, `{"coverage_min":60,"timeouts":{"tests":2400,"test_stall":0}}`},
		{`{"commands": {"tests": "npx ng test --include src/app/auth"}}`, `{"commands":{"tests":"npx ng test --include src/app/auth"}}`},
		{`{"accept_unverified": {"tests": "Karma disconnects on base", "coverage_new": "depends on tests"}}`, `{"accept_unverified":{"tests":"Karma disconnects on base","coverage_new":"depends on tests"}}`},
	}
	for _, c := range ok {
		got, err := NormalizeGatesProfile(json.RawMessage(c.in))
		if err != nil || got != c.want {
			t.Errorf("NormalizeGatesProfile(%s) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	bad := map[string]string{
		"array":           `[1]`,
		"string":          `"x"`,
		"unknown key":     `{"coverage_minimum": 50}`,
		"unknown check":   `{"accept_unverified": {"karma": "hangs"}}`,
		"empty reason":    `{"accept_unverified": {"tests": "  "}}`,
		"reason not text": `{"accept_unverified": {"tests": true}}`,
		"long reason":     `{"accept_unverified": {"tests": "` + strings.Repeat("x", maxAcceptReasonRunes+1) + `"}}`,
		"negative":        `{"coverage_min": -1}`,
		"number as text":  `{"dup_max": "3"}`,
		"timeout text":    `{"timeouts": {"tests": "long"}}`,
		"negative timer":  `{"timeouts": {"tests": -5}}`,
		"command number":  `{"commands": {"tests": 1}}`,
		"too big":         `{"commands": {"tests": "` + strings.Repeat("y", maxGatesProfileBytes) + `"}}`,
	}
	for name, in := range bad {
		if got, err := NormalizeGatesProfile(json.RawMessage(in)); err == nil {
			t.Errorf("%s: accepted as %q", name, got)
		}
	}
}

// The Go lists mirror thulu-gates: a key or check id added there must be accepted here, and one
// removed there must not be accepted here.
func TestGatesProfileKeysMatchThuluGates(t *testing.T) {
	root := filepath.Join("..", "..", "images", "runtime", "thulu-gates", "lib")
	cfg, err := os.ReadFile(filepath.Join(root, "config.js"))
	if err != nil {
		t.Fatalf("read config.js: %v", err)
	}
	body := string(cfg)
	start := strings.Index(body, "export function defaults()")
	end := strings.Index(body[start:], "\n}\n")
	if start < 0 || end < 0 {
		t.Fatal("defaults() not found in config.js")
	}
	var jsKeys []string
	for _, m := range regexp.MustCompile(`(?m)^    ([a-z_]+):`).FindAllStringSubmatch(body[start:start+end], -1) {
		jsKeys = append(jsKeys, m[1])
	}
	jsKeys = append(jsKeys, "accept_unverified")
	slices.Sort(jsKeys)
	goKeys := slices.Sorted(slices.Values(gatesProfileKeys))
	if !slices.Equal(jsKeys, goKeys) {
		t.Errorf("profile keys differ:\n thulu-gates %v\n Go          %v", jsKeys, goKeys)
	}

	idx, err := os.ReadFile(filepath.Join(root, "checks", "index.js"))
	if err != nil {
		t.Fatalf("read checks/index.js: %v", err)
	}
	list := regexp.MustCompile(`export const CHECKS = \[([^\]]+)\]`).FindStringSubmatch(string(idx))
	if list == nil {
		t.Fatal("CHECKS not found in checks/index.js")
	}
	var jsIDs []string
	for _, mod := range strings.Split(list[1], ",") {
		mod = strings.TrimSpace(mod)
		file := regexp.MustCompile(`import \* as ` + mod + ` from "\./([a-z]+)\.js"`).FindStringSubmatch(string(idx))
		if file == nil {
			t.Fatalf("no import for %s", mod)
		}
		src, err := os.ReadFile(filepath.Join(root, "checks", file[1]+".js"))
		if err != nil {
			t.Fatalf("read %s: %v", file[1], err)
		}
		id := regexp.MustCompile(`export const id = "([a-z_]+)"`).FindStringSubmatch(string(src))
		if id == nil {
			t.Fatalf("no id in %s.js", file[1])
		}
		jsIDs = append(jsIDs, id[1])
	}
	if !slices.Equal(jsIDs, gatesCheckIDs) {
		t.Errorf("check ids differ:\n thulu-gates %v\n Go          %v", jsIDs, gatesCheckIDs)
	}
}

func TestGatesProfileRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "p1", testWS)
	if got, _ := s.Get(ctx, "p1"); len(got.GatesProfile) != 0 {
		t.Fatalf("fresh project profile = %s, want none", got.GatesProfile)
	}
	prof := json.RawMessage(`{"accept_unverified": {"tests": "Karma hangs on base"}, "timeouts": {"tests": 2400}}`)
	if _, err := s.Update(ctx, "p1", Patch{GatesProfile: &prof}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Get(ctx, "p1")
	if string(got.GatesProfile) != `{"accept_unverified":{"tests":"Karma hangs on base"},"timeouts":{"tests":2400}}` {
		t.Errorf("stored profile = %s", got.GatesProfile)
	}

	// A patch that leaves the profile out keeps it; a bad one changes nothing.
	desc := "d"
	if _, err := s.Update(ctx, "p1", Patch{Description: &desc}); err != nil {
		t.Fatalf("Update description: %v", err)
	}
	bad := json.RawMessage(`{"accept_unverified": {"tests": ""}}`)
	if _, err := s.Update(ctx, "p1", Patch{GatesProfile: &bad}); err == nil {
		t.Error("a profile accepting tests without a reason was saved")
	}
	if got, _ = s.Get(ctx, "p1"); len(got.GatesProfile) == 0 {
		t.Error("the profile was lost")
	}

	empty := json.RawMessage(`{}`)
	if _, err := s.Update(ctx, "p1", Patch{GatesProfile: &empty}); err != nil {
		t.Fatalf("Update clear: %v", err)
	}
	if got, _ = s.Get(ctx, "p1"); len(got.GatesProfile) != 0 {
		t.Errorf("after {} profile = %s, want none", got.GatesProfile)
	}
}

func TestImportProjectKeepsGatesProfile(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	p := Project{ID: "p2", Name: "p2", Path: "/data/projects/p2", RepoURL: "https://example.com/p2.git",
		DefaultBranch: "main", WorkspaceID: testWS, CreatedAt: time.Now().UTC(),
		GatesProfile: json.RawMessage(`{"coverage_min": 70}`)}
	if err := s.ImportProject(ctx, p); err != nil {
		t.Fatalf("ImportProject: %v", err)
	}
	if got, _ := s.Get(ctx, "p2"); string(got.GatesProfile) != `{"coverage_min":70}` {
		t.Errorf("imported profile = %s", got.GatesProfile)
	}
	p.ID, p.Name, p.GatesProfile = "p3", "p3", json.RawMessage(`{"nope": 1}`)
	if err := s.ImportProject(ctx, p); err == nil {
		t.Error("ImportProject accepted an unknown profile key")
	}
}
