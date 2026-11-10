package secrets

import "testing"

func TestLLMProfile_KeyCheckAndRefusal(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", testJWTSecret)
	s := newTestStore(t)
	ctx := t.Context()

	prof, err := s.CreateLLMProfile(ctx, "ws-1", LLMProfileInput{
		Name: "main", Engine: "claude-code", Provider: "anthropic", RawKey: "sk-ant-api-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prof.KeyState != "" || prof.RefusedStatus != 0 {
		t.Fatalf("fresh profile = %+v", prof)
	}

	if err := s.SetLLMProfileKeyCheck(ctx, "ws-1", prof.ID,
		KeyCheck{State: KeyInvalid, Reason: KeyRejected, Status: 401}); err != nil {
		t.Fatal(err)
	}
	changed, err := s.SetLLMProfileRefusal(ctx, "ws-1", prof.ID, 403)
	if err != nil || !changed {
		t.Fatalf("set refusal: changed=%v err=%v", changed, err)
	}
	if changed, err := s.SetLLMProfileRefusal(ctx, "ws-1", prof.ID, 403); err != nil || changed {
		t.Errorf("repeat: changed=%v err=%v", changed, err)
	}
	got, err := s.GetLLMProfile(ctx, "ws-1", prof.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyState != KeyInvalid || got.KeyReason != KeyRejected || got.KeyStatus != 401 ||
		got.KeyCheckedAt.IsZero() || got.RefusedStatus != 403 || got.RefusedAt.IsZero() {
		t.Fatalf("after failures = %+v", got)
	}

	if changed, err := s.SetLLMProfileRefusal(ctx, "ws-2", prof.ID, 0); err != nil || changed {
		t.Errorf("cross-workspace clear: changed=%v err=%v", changed, err)
	}

	if err := s.SetLLMProfileKeyCheck(ctx, "ws-1", prof.ID,
		KeyCheck{State: KeyUnreachable, Reason: KeyNoAnswer, Host: "api.anthropic.com"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetLLMProfile(ctx, "ws-1", prof.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyReason != KeyNoAnswer || got.KeyHost != "api.anthropic.com" || got.KeyStatus != 0 ||
		got.RefusedStatus != 403 {
		t.Fatalf("after an unreachable check = %+v", got)
	}

	if err := s.SetLLMProfileKeyCheck(ctx, "ws-1", prof.ID, KeyCheck{State: KeyValid}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetLLMProfile(ctx, "ws-1", prof.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyState != KeyValid || got.KeyReason != "" || got.KeyHost != "" ||
		got.RefusedStatus != 0 || !got.RefusedAt.IsZero() {
		t.Fatalf("after a valid check = %+v", got)
	}
}
