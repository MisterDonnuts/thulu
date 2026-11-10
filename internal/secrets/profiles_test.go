package secrets

import (
	"errors"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

// Low-entropy descriptive literal on purpose so gitleaks does not flag it as generic-api-key.
const testJWTSecret = "octopus-profiles-tests-jwt-secret-do-not-rotate"

func TestLLMProfile_CRUDAndEncryption(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)

	prof, err := s.CreateLLMProfile(t.Context(), "ws-1", LLMProfileInput{
		Name:     "team-anthropic",
		Engine:   "claude-code",
		Model:    "sonnet",
		Effort:   "medium",
		BaseURL:  "https://api.anthropic.com",
		Provider: "anthropic",
		RawKey:   "sk-ant-api-secret-7777",
	})
	if err != nil {
		t.Fatalf("CreateLLMProfile: %v", err)
	}
	if prof.ID == "" || prof.KeyHint == "" {
		t.Fatalf("profile missing id/hint: %+v", prof)
	}
	var enc string
	if err := s.DB().QueryRow(`SELECT key_encrypted FROM llm_profiles WHERE id=$1`, prof.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc == "" || enc == "sk-ant-api-secret-7777" || !startsWith(enc, "v1:") {
		t.Errorf("key must be stored encrypted, got %q", enc)
	}

	raw, err := s.GetLLMProfileCredential(t.Context(), "ws-1", prof.ID)
	if err != nil {
		t.Fatalf("GetLLMProfileCredential: %v", err)
	}
	if raw != "sk-ant-api-secret-7777" {
		t.Errorf("decrypted credential = %q", raw)
	}

	rotated, err := s.UpdateLLMProfile(t.Context(), "ws-1", prof.ID, LLMProfileInput{
		Name:   "team-anthropic",
		Engine: "claude-code",
		Model:  "opus",
	})
	if err != nil {
		t.Fatalf("UpdateLLMProfile no-key: %v", err)
	}
	if rotated.Model != "opus" {
		t.Errorf("model not rotated, got %q", rotated.Model)
	}
	if raw2, err := s.GetLLMProfileCredential(t.Context(), "ws-1", prof.ID); err != nil || raw2 != "sk-ant-api-secret-7777" {
		t.Errorf("credential should be unchanged when key is empty, got %q err=%v", raw2, err)
	}

	rotKey := "sk-ant-api-rotated-9999"
	if _, err := s.UpdateLLMProfile(t.Context(), "ws-1", prof.ID, LLMProfileInput{RawKey: rotKey}); err != nil {
		t.Fatalf("UpdateLLMProfile rotate: %v", err)
	}
	if raw3, err := s.GetLLMProfileCredential(t.Context(), "ws-1", prof.ID); err != nil || raw3 != rotKey {
		t.Errorf("credential should be rotated, got %q err=%v", raw3, err)
	}

	list, err := s.ListLLMProfiles(t.Context(), "ws-1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListLLMProfiles: got %d profiles err=%v", len(list), err)
	}

	if err := s.DeleteLLMProfile(t.Context(), "ws-1", prof.ID); err != nil {
		t.Fatalf("DeleteLLMProfile: %v", err)
	}
	if _, err := s.GetLLMProfile(t.Context(), "ws-1", prof.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestLLMProfile_UniqueNamePerWorkspace(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)
	in := LLMProfileInput{
		Name:   "dup",
		Engine: "claude-code",
		RawKey: "sk-ant-api-1",
	}
	if _, err := s.CreateLLMProfile(t.Context(), "ws-1", in); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := s.CreateLLMProfile(t.Context(), "ws-1", in); err == nil {
		t.Error("expected error on duplicate name within workspace")
	}
	if _, err := s.CreateLLMProfile(t.Context(), "ws-2", in); err != nil {
		t.Errorf("same name in another workspace must be allowed: %v", err)
	}
}

func TestAgentBinding_CRUD(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)
	prof, _ := s.CreateLLMProfile(t.Context(), "ws-1", LLMProfileInput{
		Name: "p1", Engine: "claude-code", RawKey: "sk-ant-api-x",
	})

	if _, err := s.GetAgentBinding(t.Context(), "ws-1", "reviewer"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	if err := s.SetAgentBinding(t.Context(), "ws-1", "reviewer", prof.ID, "alice@example.com"); err != nil {
		t.Fatalf("SetAgentBinding: %v", err)
	}
	b, err := s.GetAgentBinding(t.Context(), "ws-1", "reviewer")
	if err != nil {
		t.Fatalf("GetAgentBinding: %v", err)
	}
	if b.ProfileID != prof.ID || b.UpdatedBy != "alice@example.com" {
		t.Errorf("binding = %+v", b)
	}

	otherProf, _ := s.CreateLLMProfile(t.Context(), "ws-2", LLMProfileInput{
		Name: "p2", Engine: "claude-code", RawKey: "sk-ant-api-y",
	})
	if err := s.SetAgentBinding(t.Context(), "ws-1", "reviewer", otherProf.ID, "alice"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant bind should be ErrNotFound, got %v", err)
	}

	if err := s.DeleteLLMProfile(t.Context(), "ws-1", prof.ID); !errors.Is(err, ErrProfileInUse) {
		t.Errorf("expected ErrProfileInUse, got %v", err)
	}

	if err := s.DeleteAgentBinding(t.Context(), "ws-1", "reviewer"); err != nil {
		t.Fatalf("DeleteAgentBinding: %v", err)
	}
	if err := s.DeleteLLMProfile(t.Context(), "ws-1", prof.ID); err != nil {
		t.Errorf("delete after unbind: %v", err)
	}
}

func TestResolveLLMForAgent_PrecedenceChain(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)

	res, err := s.ResolveLLMForAgent(t.Context(), "ws-1", "reviewer", "claude-code")
	if err != nil {
		t.Fatalf("resolve empty: %v", err)
	}
	if res.Source != "host" || res.Credential != "" {
		t.Errorf("expected host, got %+v", res)
	}

	enc, err := secretbox.Encrypt(llmKeyInfo, "sk-ant-api-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO llm_keys(workspace_id, engine, provider, key_encrypted, key_hint, created_at, updated_at)
		VALUES($1,'claude-code','anthropic',$2,'x',now(),now())`, "ws-1", enc); err != nil {
		t.Fatal(err)
	}
	res, err = s.ResolveLLMForAgent(t.Context(), "ws-1", "reviewer", "claude-code")
	if err != nil {
		t.Fatalf("resolve legacy: %v", err)
	}
	if res.Source != "legacy" || res.Credential != "sk-ant-api-legacy" {
		t.Errorf("expected legacy/sk-ant-api-legacy, got %+v", res)
	}

	if err := s.SetLLMKey(t.Context(), "ws-1", "claude-code", "anthropic", "sk-ant-api-default"); err != nil {
		t.Fatalf("SetLLMKey: %v", err)
	}
	res, err = s.ResolveLLMForAgent(t.Context(), "ws-1", "reviewer", "claude-code")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if res.Source != "workspace_default" || res.Credential != "sk-ant-api-default" {
		t.Errorf("expected workspace_default, got %+v", res)
	}

	if _, err := s.CreateLLMProfile(t.Context(), "ws-1", LLMProfileInput{
		Name: "team-sonnet", Engine: "claude-code", Model: "sonnet",
		Provider: "anthropic", RawKey: "sk-ant-api-team", IsDefault: true,
	}); err != nil {
		t.Fatalf("create default profile: %v", err)
	}
	res, err = s.ResolveLLMForAgent(t.Context(), "ws-1", "reviewer", "claude-code")
	if err != nil {
		t.Fatalf("resolve new default: %v", err)
	}
	if res.Source != "workspace_default" || res.Credential != "sk-ant-api-team" || res.Model != "sonnet" {
		t.Errorf("expected the new default to win, got %+v", res)
	}

	specific, _ := s.CreateLLMProfile(t.Context(), "ws-1", LLMProfileInput{
		Name: "gpt5-research", Engine: "openai-codex", Model: "gpt-5",
		BaseURL: "https://litellm.internal", OrgID: "org-42",
		Provider: "openai", RawKey: "sk-proj-binding",
	})
	if err := s.SetAgentBinding(t.Context(), "ws-1", "reviewer", specific.ID, "alice"); err != nil {
		t.Fatalf("SetAgentBinding: %v", err)
	}
	res, err = s.ResolveLLMForAgent(t.Context(), "ws-1", "reviewer", "claude-code")
	if err != nil {
		t.Fatalf("resolve binding: %v", err)
	}
	if res.Source != "binding" {
		t.Errorf("expected binding source, got %+v", res)
	}
	if res.Engine != "openai-codex" || res.Model != "gpt-5" {
		t.Errorf("engine/model override failed: %+v", res)
	}
	if res.BaseURL != "https://litellm.internal" || res.OrgID != "org-42" {
		t.Errorf("base_url/org_id override failed: %+v", res)
	}
	if res.Credential != "sk-proj-binding" {
		t.Errorf("credential override failed: %q", res.Credential)
	}

	res2, err := s.ResolveLLMForAgent(t.Context(), "ws-1", "other-agent", "claude-code")
	if err != nil {
		t.Fatalf("resolve other-agent: %v", err)
	}
	if res2.Source != "workspace_default" {
		t.Errorf("other-agent should hit workspace_default, got %+v", res2)
	}
}

func TestMigration8_LegacyKeysBecomeDefaultProfiles(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)
	db := s.DB()
	mustEnc := func(raw string) string {
		enc, err := secretbox.Encrypt(llmKeyInfo, raw)
		if err != nil {
			t.Fatal(err)
		}
		return enc
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	exec(`INSERT INTO llm_profiles(id, workspace_id, name, engine, provider, key_encrypted, key_hint, created_at, updated_at)
		VALUES('legacy-ws-a','ws-a','default-claude-code','claude-code','anthropic',$1,'…old1',now()-interval '2 days',now()-interval '2 days')`,
		mustEnc("sk-ant-api-old"))
	exec(`INSERT INTO llm_keys(workspace_id, engine, provider, key_encrypted, key_hint, created_at, updated_at)
		VALUES('ws-a','claude-code','anthropic',$1,'…new2',now()-interval '3 days',now())`, mustEnc("sk-ant-api-new"))
	exec(`INSERT INTO llm_keys(workspace_id, engine, provider, key_encrypted, key_hint, created_at, updated_at)
		VALUES('ws-b','codex','openai',$1,'…b',now(),now())`, mustEnc("sk-proj-b"))
	exec(`INSERT INTO llm_profiles(id, workspace_id, name, engine, provider, key_encrypted, key_hint, created_at, updated_at)
		VALUES('legacy-ws-c','ws-c','default-claude-code','claude-code','anthropic',$1,'…c1',now(),now())`,
		mustEnc("sk-ant-api-c-profile"))
	exec(`INSERT INTO llm_keys(workspace_id, engine, provider, key_encrypted, key_hint, created_at, updated_at)
		VALUES('ws-c','claude-code','anthropic',$1,'…c2',now()-interval '1 day',now()-interval '1 day')`,
		mustEnc("sk-ant-api-c-legacy"))

	exec(migration8DefaultProfiles)

	for _, tc := range []struct{ ws, engine, want string }{
		{"ws-a", "claude-code", "sk-ant-api-new"},
		{"ws-b", "codex", "sk-proj-b"},
		{"ws-c", "claude-code", "sk-ant-api-c-profile"},
	} {
		res, err := s.ResolveLLMForAgent(t.Context(), tc.ws, "any-agent", tc.engine)
		if err != nil {
			t.Fatalf("%s: %v", tc.ws, err)
		}
		if res.Source != "workspace_default" || res.Credential != tc.want {
			t.Errorf("%s: want workspace_default/%s, got %s/%s", tc.ws, tc.want, res.Source, res.Credential)
		}
	}
	var legacyLeft int
	if err := db.QueryRow(`SELECT COUNT(*) FROM llm_keys`).Scan(&legacyLeft); err != nil {
		t.Fatal(err)
	}
	if legacyLeft != 0 {
		t.Errorf("carried legacy rows must be removed, %d left", legacyLeft)
	}
}

func TestLLMProfile_OneDefaultPerEngine(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)
	a, err := s.CreateLLMProfile(t.Context(), "ws-1", LLMProfileInput{
		Name: "a", Engine: "claude-code", RawKey: "sk-ant-api-a", IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateLLMProfile(t.Context(), "ws-1", LLMProfileInput{Name: "b", Engine: "claude-code", RawKey: "sk-ant-api-b"})
	if err != nil {
		t.Fatal(err)
	}
	cx, err := s.CreateLLMProfile(t.Context(), "ws-1", LLMProfileInput{
		Name: "cx", Engine: "codex", RawKey: "sk-proj-cx", IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetLLMProfileDefault(t.Context(), "ws-1", b.ID, true); err != nil {
		t.Fatalf("SetLLMProfileDefault: %v", err)
	}
	got, err := s.DefaultLLMProfile(t.Context(), "ws-1", "claude-code")
	if err != nil || got.ID != b.ID {
		t.Fatalf("default for claude-code = %+v err=%v, want b", got, err)
	}
	if again, _ := s.GetLLMProfile(t.Context(), "ws-1", a.ID); again.IsDefault {
		t.Error("a must have been demoted")
	}
	if got, err := s.DefaultLLMProfile(t.Context(), "ws-1", "codex"); err != nil || got.ID != cx.ID {
		t.Errorf("codex default changed: %+v err=%v", got, err)
	}
	if _, err := s.SetLLMProfileDefault(t.Context(), "ws-1", b.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DefaultLLMProfile(t.Context(), "ws-1", "claude-code"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected no default, got err=%v", err)
	}
}

func TestResolvedLLM_ForAgent(t *testing.T) {
	cases := []struct {
		name                  string
		in                    ResolvedLLM
		agentModel, agentEff  string
		wantModel, wantEffort string
	}{
		{"binding decides", ResolvedLLM{Source: "binding", Model: "opus", Effort: "high"}, "haiku", "low", "opus", "high"},
		{"binding fills from agent", ResolvedLLM{Source: "binding"}, "haiku", "low", "haiku", "low"},
		{"default yields to agent", ResolvedLLM{Source: "workspace_default", Model: "opus", Effort: "high"}, "haiku", "low", "haiku", "low"},
		{"default fills the gaps", ResolvedLLM{Source: "workspace_default", Model: "opus", Effort: "high"}, "", "", "opus", "high"},
		{"host keeps agent", ResolvedLLM{Source: "host"}, "sonnet", "", "sonnet", ""},
	}
	for _, tc := range cases {
		got := tc.in.ForAgent(tc.agentModel, tc.agentEff)
		if got.Model != tc.wantModel || got.Effort != tc.wantEffort {
			t.Errorf("%s: got %s/%s, want %s/%s", tc.name, got.Model, got.Effort, tc.wantModel, tc.wantEffort)
		}
	}
}

func TestLLMProfile_DeleteByWorkspaceCascades(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)
	p, _ := s.CreateLLMProfile(t.Context(), "ws-1", LLMProfileInput{
		Name: "p", Engine: "claude-code", RawKey: "sk-ant-api-x",
	})
	_ = s.SetAgentBinding(t.Context(), "ws-1", "reviewer", p.ID, "alice")

	if err := s.DeleteByWorkspace(t.Context(), "ws-1"); err != nil {
		t.Fatalf("DeleteByWorkspace: %v", err)
	}
	if list, _ := s.ListLLMProfiles(t.Context(), "ws-1"); len(list) != 0 {
		t.Errorf("profiles should be gone, got %d", len(list))
	}
	if list, _ := s.ListAgentBindings(t.Context(), "ws-1"); len(list) != 0 {
		t.Errorf("bindings should be gone, got %d", len(list))
	}
}
