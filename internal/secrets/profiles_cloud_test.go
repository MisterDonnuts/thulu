package secrets

import "testing"

func TestLLMProfile_CloudFieldsAndAmbientIdentity(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)
	ctx := t.Context()

	if _, err := s.CreateLLMProfile(ctx, "ws-1", LLMProfileInput{
		Name: "no-key", Engine: "claude-code", Provider: "anthropic",
	}); err == nil {
		t.Fatal("a key-based profile without a key must be refused")
	}

	prof, err := s.CreateLLMProfile(ctx, "ws-1", LLMProfileInput{
		Name: "vertex", Engine: "claude-code", Provider: "vertex",
		Region: "global", ProjectID: "acme-prod", SmallFastModel: "claude-haiku-4-5",
		KeyOptional: true,
	})
	if err != nil {
		t.Fatalf("create ambient cloud profile: %v", err)
	}
	if prof.HasKey || prof.KeyHint != "" || prof.Region != "global" || prof.ProjectID != "acme-prod" {
		t.Fatalf("ambient profile view = %+v", prof)
	}
	if err := s.SetAgentBinding(ctx, "ws-1", "dev", prof.ID, ""); err != nil {
		t.Fatal(err)
	}
	res, err := s.ResolveLLMForAgent(ctx, "ws-1", "dev", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "binding" || res.Provider != "vertex" || res.Region != "global" ||
		res.ProjectID != "acme-prod" || res.SmallFastModel != "claude-haiku-4-5" || res.Credential != "" {
		t.Fatalf("resolved = %+v", res)
	}

	prof, err = s.UpdateLLMProfile(ctx, "ws-1", prof.ID, LLMProfileInput{
		Engine: "claude-code", Provider: "vertex", Region: "global", ProjectID: "acme-prod",
		RawKey: `{"gcp_service_account_json":"{}"}`, KeyHint: "sa@acme",
	})
	if err != nil || !prof.HasKey || prof.KeyHint != "sa@acme" {
		t.Fatalf("with secret = %+v, %v", prof, err)
	}
	prof, err = s.UpdateLLMProfile(ctx, "ws-1", prof.ID, LLMProfileInput{
		Engine: "claude-code", Provider: "vertex", Region: "global", ProjectID: "acme-prod", ClearKey: true,
	})
	if err != nil || prof.HasKey || prof.KeyHint != "" {
		t.Fatalf("cleared = %+v, %v", prof, err)
	}
}

func TestSetLLMKey_KeepsEndpointFields(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)
	ctx := t.Context()
	p, err := s.CreateLLMProfile(ctx, "ws-1", LLMProfileInput{
		Name: "default-claude-code", Engine: "claude-code", Provider: "anthropic",
		BaseURL: "https://llm.corp.example", AuthHeader: "bearer", RawKey: "tok-1", IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLLMKey(ctx, "ws-1", "claude-code", "anthropic", "tok-2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetLLMProfile(ctx, "ws-1", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AuthHeader != "bearer" || got.BaseURL != "https://llm.corp.example" || !got.HasKey {
		t.Errorf("rotation lost endpoint fields: %+v", got)
	}
}
