package secrets

import (
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Case 6: a machine profile may be the engine's default, and resolution carries its machine both
// through a binding and as the default.
func TestMachineProfileResolvesWithItsRunner(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)
	ctx := t.Context()
	prof, err := s.CreateLLMProfile(ctx, "ws-1", LLMProfileInput{
		Name: "my-mac", Engine: "claude-code", Model: "opus", RunnerID: "rn1", KeyOptional: true, IsDefault: true,
	})
	if err != nil {
		t.Fatalf("CreateLLMProfile: %v", err)
	}
	if prof.RunnerID != "rn1" || prof.HasKey || !prof.IsDefault {
		t.Fatalf("machine profile = %+v", prof)
	}

	byDefault, err := s.ResolveLLMForAgent(ctx, "ws-1", "dev", "claude-code")
	if err != nil || byDefault.Source != "workspace_default" || byDefault.RunnerID != "rn1" || byDefault.Credential != "" {
		t.Fatalf("default resolution = %+v, %v", byDefault, err)
	}
	if err := s.SetAgentBinding(ctx, "ws-1", "dev", prof.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	byBinding, err := s.ResolveLLMForAgent(ctx, "ws-1", "dev", "claude-code")
	if err != nil || byBinding.Source != "binding" || byBinding.RunnerID != "rn1" {
		t.Fatalf("binding resolution = %+v, %v", byBinding, err)
	}

	moved, err := s.UpdateLLMProfile(ctx, "ws-1", prof.ID, LLMProfileInput{Engine: "claude-code", Model: "opus", RunnerID: "rn2"})
	if err != nil || moved.RunnerID != "rn2" {
		t.Fatalf("moving the profile to another machine = %+v, %v", moved, err)
	}
}

// Case 5 through the legacy key endpoint: a key set for an engine whose default is a machine
// profile is refused, and the profile stays a machine one. The key status says no key.
func TestLegacyKeyDoesNotTurnAMachineDefaultIntoAKeyProfile(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)
	ctx := t.Context()
	prof, err := s.CreateLLMProfile(ctx, "ws-1", LLMProfileInput{
		Name: "my-mac", Engine: "claude-code", RunnerID: "rn1", KeyOptional: true, IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.SetLLMKey(ctx, "ws-1", "claude-code", "anthropic", "sk-ant-api-legacy")
	if apperr.KindOf(err) != apperr.KindConflict || !strings.Contains(apperr.Text(err), "my-mac") {
		t.Fatalf("SetLLMKey over a machine default = %v, want a conflict naming the profile", err)
	}
	got, err := s.GetLLMProfile(ctx, "ws-1", prof.ID)
	if err != nil || got.RunnerID != "rn1" || got.HasKey {
		t.Fatalf("machine profile after the refused key = %+v, %v", got, err)
	}
	st, err := s.StatusLLMKey(ctx, "ws-1", "claude-code")
	if err != nil || st.HasKey || st.RunnerID != "rn1" {
		t.Errorf("key status = %+v, %v, want no key and the machine", st, err)
	}
}
