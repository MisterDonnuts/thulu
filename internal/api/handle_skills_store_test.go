package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestListSkillsReadsTheStore(t *testing.T) {
	t.Parallel()
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	put := func(owner, path, body string) {
		t.Helper()
		if _, err := store.PutFile(ctx, defaultWorkspaceID,
			agentstore.KindSkill, owner, path, []byte(body), "test"); err != nil {
			t.Fatalf("put %s/%s: %v", owner, path, err)
		}
	}
	put("worktree-protocol", "prompt.md", "never commit to main")
	put("worktree-protocol", "skill.yaml", "name: worktree-protocol\ndescription: git hygiene\n")
	put("visual-verification", "prompt.md", "look at it")
	put("visual-verification", "scripts/shot.sh", "#!/bin/sh\n")
	put("_base", "prompt.md", "shared rules")
	put("halfbaked", "skill.yaml", "name: halfbaked\n")
	if _, err := store.PutFile(ctx, "ws-other", agentstore.KindSkill, "theirs", "prompt.md",
		[]byte("not yours"), "test"); err != nil {
		t.Fatalf("put other: %v", err)
	}
	if _, err := store.PutSystemSkill(ctx, "octopus-gh", map[string][]byte{
		"prompt.md":  []byte("system"),
		"skill.yaml": []byte("name: octopus-gh\nversion: 3\nmcp: thulu-github\n"),
	}, "test"); err != nil {
		t.Fatalf("put system skill: %v", err)
	}

	srv := &Server{agentsDir: t.TempDir()}
	srv.SetAgentStore(store)

	rec := httptest.NewRecorder()
	srv.handleListSkills(rec, asDevOperator(httptest.NewRequest("GET", "/skills", nil)))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got []SkillDef
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}

	byName := map[string]SkillDef{}
	for _, d := range got {
		byName[d.Name] = d
	}
	if _, leaked := byName["_base"]; leaked {
		t.Fatal("_base is offered as a grant; it is loaded unconditionally")
	}
	if _, bad := byName["halfbaked"]; bad {
		t.Fatal("a skill with no prompt.md was listed")
	}
	if _, leaked := byName["theirs"]; leaked {
		t.Fatal("another workspace's skill was listed")
	}
	if len(got) != 3 {
		t.Fatalf("listed %d skills, want 3: %+v", len(got), got)
	}
	if d := byName["octopus-gh"]; !d.System || d.MCP != "thulu-github" {
		t.Fatalf("system skill not flagged: %+v", d)
	}
	if d := byName["worktree-protocol"]; d.Description != "git hygiene" || d.Type != "knowledge" {
		t.Fatalf("skill.yaml not applied: %+v", d)
	}
	if d := byName["visual-verification"]; !d.HasScripts || d.Type != "tool" {
		t.Fatalf("a scripts/ path must mark a tool skill: %+v", d)
	}
}
