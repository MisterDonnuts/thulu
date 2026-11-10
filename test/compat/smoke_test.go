//go:build compat

package compat

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/semver"
)

// Test order matters: with -run 'TestVerify|TestWrite', TestVerify must read earlier versions' rows
// before TestWrite adds new ones.

func TestSeed(t *testing.T) {
	s := loadState(t)
	if len(s.Steps) > 0 {
		t.Fatalf("%s already holds %d step(s); TestSeed runs on a fresh database", statePath(t), len(s.Steps))
	}
	bs := loadBootstrap(t)
	s.WorkspaceID = bs.WorkspaceID
	writeStep(t, s, stepName(t, "seed"))
}

func TestVerify(t *testing.T) {
	s := loadState(t)
	if len(s.Steps) == 0 {
		t.Fatal("no steps recorded; TestSeed has to run first")
	}
	admin := keyClient(t, loadBootstrap(t).Key)
	dev := devClient(t)
	have := featuresOf(admin)

	for _, st := range s.Steps {
		t.Run(st.Name, func(t *testing.T) {
			admin.t, dev.t = t, t
			verifyStep(t, admin, st, have)
		})
	}
	admin.t, dev.t = t, t

	if s.Setting != nil {
		var got any
		dev.must(http.MethodGet, "/settings/"+s.Setting.Key, nil, &got)
		if v := settingValue(got); v != fmt.Sprint(s.Setting.Value) {
			t.Errorf("setting %s = %v, want %d", s.Setting.Key, got, s.Setting.Value)
		}
	}

	var entries any
	admin.must(http.MethodGet, "/audit?limit=200", nil, &entries)
	if have.auditChain {
		var rep map[string]any
		dev.must(http.MethodGet, "/audit/verify", nil, &rep)
		switch {
		case !downgradedPastChain(s.Steps):
			if rep["ok"] != true {
				t.Errorf("audit chain does not verify: %v", rep)
			}
		// After a downgrade past the chain (0.1.0 ⇄ 0.2.0), the old release writes unhashed rows,
		// so verify must report exactly that break.
		case rep["ok"] != false || !strings.HasPrefix(str(rep, "reason"), "row without a hash after the chain started"):
			t.Errorf("after a downgrade past the audit chain, verify = %v; want the unhashed-row break", rep)
		}
	}
}

func downgradedPastChain(steps []step) bool {
	chained := false
	for _, st := range steps {
		if st.AuditChain {
			chained = true
		} else if chained {
			return true
		}
	}
	return false
}

// Features are decided by version, not by answer shape, so a regression that drops a field cannot
// pass as the old release.
type features struct {
	threads    bool
	auditChain bool
}

func featuresOf(c *client) features {
	c.t.Helper()
	var info map[string]any
	c.must(http.MethodGet, "/info", nil, &info)
	v := strings.TrimPrefix(str(info, "version"), "v")
	if v == "" {
		c.t.Fatalf("GET /info reports no version: %v", info)
	}
	legacy := !semver.Newer(v, "0.1.0")
	return features{threads: !legacy, auditChain: !legacy}
}

func TestWrite(t *testing.T) {
	s := loadState(t)
	if len(s.Steps) == 0 {
		t.Fatal("no steps recorded; TestSeed has to run first")
	}
	writeStep(t, s, stepName(t, fmt.Sprintf("step%d", len(s.Steps)+1)))
}

var stepNameRe = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)

// The step name becomes part of board and stage ids, which only take [a-z0-9_].
func stepName(t *testing.T, fallback string) string {
	t.Helper()
	name := strings.TrimSpace(os.Getenv("COMPAT_STEP"))
	if name == "" {
		name = fallback
	}
	if !stepNameRe.MatchString(name) {
		t.Fatalf("COMPAT_STEP %q: want [a-z][a-z0-9]{0,15}", name)
	}
	return name
}

func writeStep(t *testing.T, s *state, name string) {
	for _, st := range s.Steps {
		if st.Name == name {
			t.Fatalf("step %q already ran against this database", name)
		}
	}
	bs := loadBootstrap(t)
	admin := keyClient(t, bs.Key)
	dev := devClient(t)

	st := step{Name: name, AuditChain: featuresOf(admin).auditChain}
	defer func() {
		s.Steps = append(s.Steps, st)
		saveState(t, s)
	}()

	// No column needs an agent, so nothing here starts a run.
	st.BoardID = "compat_" + name
	st.BoardLabel = "Compat " + name
	st.Stages = []string{name + "_todo", name + "_done"}
	admin.must(http.MethodPost, "/boards", map[string]any{
		"id":    st.BoardID,
		"label": st.BoardLabel,
		"stages": []map[string]any{
			{"id": st.Stages[0], "label": "To do", "color": "#8a8f98", "initial": true},
			{"id": st.Stages[1], "label": "Done", "color": "#2f9e44", "terminal": true},
		},
	}, nil)

	for i := 1; i <= 2; i++ {
		ts := taskState{
			Title: fmt.Sprintf("compat %s task %d", name, i),
			Body:  fmt.Sprintf("Written by step %s.\n\nSecond paragraph, with ünïcödé and `code`.", name),
		}
		var created map[string]any
		admin.must(http.MethodPost, "/tasks", map[string]any{
			"title":    ts.Title,
			"body":     ts.Body,
			"board_id": st.BoardID,
			"tags":     []string{"compat", name},
		}, &created)
		ts.ID = str(created, "id")
		if ts.ID == "" {
			t.Fatalf("POST /tasks answered without an id: %v", created)
		}

		var parent string
		for j := 1; j <= 2; j++ {
			c := commentState{Body: fmt.Sprintf("comment %d on %s by %s", j, ts.Title, name), ParentID: parent}
			body := map[string]any{"body": c.Body}
			if parent != "" {
				body["parent_id"] = parent
			}
			var out map[string]any
			admin.must(http.MethodPost, "/tasks/"+ts.ID+"/comments", body, &out)
			created := newComment(out)
			if c.ID = str(created, "id"); c.ID == "" {
				t.Fatalf("POST /tasks/%s/comments answered without an id: %v", ts.ID, out)
			}
			// 0.1.0 has no threads and drops parent_id; record what it stored.
			c.ParentID = str(created, "parent_id")
			if c.ParentID != parent && c.ParentID != "" {
				t.Errorf("comment %s stored parent %q, sent %q", c.ID, c.ParentID, parent)
			}
			parent = c.ID
			ts.Comments = append(ts.Comments, c)
		}
		st.Tasks = append(st.Tasks, ts)
	}

	// Settings are install-wide and belong to the operator, so they go through the dev session, not
	// a workspace key.
	setting := settingState{Key: "chat.turn_timeout_seconds", Value: 600 + len(s.Steps) + 1}
	dev.must(http.MethodPut, "/settings/"+setting.Key, setting.Value, nil)
	s.Setting = &setting

	// Placeholder key: the suite must never hold a real credential. 0.1.0 requires the sk-ant-
	// prefix; .invalid never resolves (RFC 2606).
	var engines struct {
		Engines []map[string]any `json:"engines"`
	}
	admin.must(http.MethodGet, "/engines", nil, &engines)
	if len(engines.Engines) == 0 {
		t.Fatal("GET /engines listed none")
	}
	st.ProfileName = "compat-" + name
	st.ProfileModel = "compat-model-" + name
	var prof map[string]any
	admin.must(http.MethodPost, "/llm-profiles", map[string]any{
		"name":     st.ProfileName,
		"engine":   str(engines.Engines[0], "name"),
		"model":    st.ProfileModel,
		"base_url": "https://llm.compat.invalid",
		"key":      "sk-ant-api-compat-placeholder-not-a-credential-" + name,
	}, &prof)
	if st.ProfileID = str(prof, "id"); st.ProfileID == "" {
		t.Fatalf("POST /llm-profiles answered without an id: %v", prof)
	}

	// Disabled and manual so the scheduler never runs it. 0.1.0 has no automations.
	var auto map[string]any
	if admin.optional(http.MethodPost, "/automations", map[string]any{
		"spec": "name: compat-" + name + "\n" +
			"trigger: {manual: true}\n" +
			"steps:\n  - id: ticket\n    task: {title: \"compat " + name + "\"}\n",
		"enabled": false,
	}, &auto) {
		st.AutomationName = "compat-" + name
		if st.AutomationID = str(auto, "id"); st.AutomationID == "" {
			t.Fatalf("POST /automations answered without an id: %v", auto)
		}
	}

	st.KeyName = "compat-" + name
	var key map[string]any
	admin.must(http.MethodPost, "/auth/api-keys", map[string]any{
		"name":            st.KeyName,
		"role":            "viewer",
		"permissions":     []string{"read-only"},
		"expires_in_days": 1,
	}, &key)
	st.KeyID, st.Key = str(key, "id"), str(key, "key")
	if st.KeyID == "" || st.Key == "" {
		t.Fatalf("POST /auth/api-keys answered without id or key")
	}
}

func newComment(out map[string]any) map[string]any {
	if str(out, "id") != "" && out["comments"] == nil {
		return out
	}
	for _, k := range []string{"comment", "Comment"} {
		if m, ok := out[k].(map[string]any); ok {
			return m
		}
	}
	if list, ok := out["comments"].([]any); ok && len(list) > 0 {
		if m, ok := list[len(list)-1].(map[string]any); ok {
			return m
		}
	}
	return nil
}

func verifyStep(t *testing.T, admin *client, st step, have features) {
	var boards any
	admin.must(http.MethodGet, "/boards", nil, &boards)
	found := false
	for _, b := range items(boards, "boards") {
		if str(b, "id") == st.BoardID {
			found = true
			if got := str(b, "label"); got != st.BoardLabel {
				t.Errorf("board %s label = %q, want %q", st.BoardID, got, st.BoardLabel)
			}
		}
	}
	if !found {
		t.Errorf("board %s missing from GET /boards", st.BoardID)
	}
	var stages any
	admin.must(http.MethodGet, "/boards/"+st.BoardID+"/stages", nil, &stages)
	for _, want := range st.Stages {
		ok := false
		for _, s := range items(stages, "stages") {
			ok = ok || str(s, "id") == want
		}
		if !ok {
			t.Errorf("board %s lost stage %s: %v", st.BoardID, want, stages)
		}
	}

	var list any
	admin.must(http.MethodGet, "/tasks?board_id="+url.QueryEscape(st.BoardID), nil, &list)
	for _, ts := range st.Tasks {
		var got map[string]any
		admin.must(http.MethodGet, "/tasks/"+ts.ID, nil, &got)
		if str(got, "title") != ts.Title || str(got, "body") != ts.Body {
			t.Errorf("task %s = %q / %q, want %q / %q", ts.ID, str(got, "title"), str(got, "body"), ts.Title, ts.Body)
		}
		if b := str(got, "board_id"); b != "" && b != st.BoardID {
			t.Errorf("task %s board = %q, want %q", ts.ID, b, st.BoardID)
		}
		comments, _ := got["comments"].([]any)
		for _, want := range ts.Comments {
			ok := false
			for _, c := range comments {
				m, _ := c.(map[string]any)
				if str(m, "id") != want.ID {
					continue
				}
				ok = str(m, "body") == want.Body
				if have.threads && want.ParentID != "" && str(m, "parent_id") != want.ParentID {
					t.Errorf("comment %s parent = %q, want %q", want.ID, str(m, "parent_id"), want.ParentID)
				}
			}
			if !ok {
				t.Errorf("task %s: comment %s (%q) missing or changed", ts.ID, want.ID, want.Body)
			}
		}
	}

	if st.ProfileID != "" {
		var prof map[string]any
		admin.must(http.MethodGet, "/llm-profiles/"+st.ProfileID, nil, &prof)
		if str(prof, "name") != st.ProfileName || str(prof, "model") != st.ProfileModel {
			t.Errorf("profile %s = %q/%q, want %q/%q", st.ProfileID, str(prof, "name"), str(prof, "model"), st.ProfileName, st.ProfileModel)
		}
		if strings.Contains(fmt.Sprint(prof), "compat-placeholder-not-a-credential") {
			t.Errorf("GET /llm-profiles/%s returned the raw key", st.ProfileID)
		}
	}

	var a map[string]any
	if st.AutomationID != "" && admin.optional(http.MethodGet, "/automations/"+st.AutomationID, nil, &a) {
		if str(a, "name") != st.AutomationName {
			t.Errorf("automation %s name = %q, want %q", st.AutomationID, str(a, "name"), st.AutomationName)
		}
		if a["enabled"] == true {
			t.Errorf("automation %s came back enabled", st.AutomationID)
		}
	}

	if st.KeyID != "" {
		var keys any
		admin.must(http.MethodGet, "/auth/api-keys", nil, &keys)
		ok := false
		for _, k := range items(keys, "keys") {
			ok = ok || str(k, "id") == st.KeyID
		}
		if !ok {
			t.Errorf("API key %s missing from GET /auth/api-keys", st.KeyID)
		}
		if len(st.Tasks) > 0 {
			viewer := keyClient(t, st.Key)
			viewer.must(http.MethodGet, "/tasks/"+st.Tasks[0].ID, nil, nil)
			if code, _ := viewer.do(http.MethodPost, "/tasks/"+st.Tasks[0].ID+"/comments",
				map[string]any{"body": "must be refused"}, nil); code < 400 {
				t.Errorf("read-only key %s could comment (status %d)", st.KeyID, code)
			}
		}
	}
}
