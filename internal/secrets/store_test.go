package secrets

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db := testpg.New(t)
	s, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestDB_Accessor(t *testing.T) {
	s := newTestStore(t)
	if s.DB() == nil {
		t.Error("DB() should not be nil")
	}
}

func TestLLMKey_EncryptedRoundTrip(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	s := newTestStore(t)

	if err := s.SetLLMKey(t.Context(), "ws-1", "claude-code", "anthropic", "sk-ant-secret-12345"); err != nil {
		t.Fatalf("SetLLMKey: %v", err)
	}
	var enc string
	if err := s.DB().QueryRow(`SELECT key_encrypted FROM llm_profiles WHERE workspace_id=$1 AND is_default`, "ws-1").Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc == "" || enc == "sk-ant-secret-12345" || !startsWith(enc, "v1:") {
		t.Errorf("key must be stored encrypted, got %q", enc)
	}

	provider, raw, err := s.GetLLMKey(t.Context(), "ws-1", "claude-code")
	if err != nil {
		t.Fatalf("GetLLMKey: %v", err)
	}
	if provider != "anthropic" || raw != "sk-ant-secret-12345" {
		t.Errorf("decrypted = %s/%s", provider, raw)
	}
}

func TestLLMKey_StatusMasksKey(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	s := newTestStore(t)
	_ = s.SetLLMKey(t.Context(), "ws-2", "claude-code", "anthropic", "sk-ant-secret-9876")

	st, err := s.StatusLLMKey(t.Context(), "ws-2", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if !st.HasKey || st.Hint != "…9876" {
		t.Errorf("status should mask key, got has=%v hint=%q", st.HasKey, st.Hint)
	}
	miss, err := s.StatusLLMKey(t.Context(), "ws-none", "claude-code")
	if err != nil || miss.HasKey {
		t.Errorf("missing key should be HasKey=false, got %+v err=%v", miss, err)
	}
}

func TestLLMKey_Delete(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	s := newTestStore(t)
	_ = s.SetLLMKey(t.Context(), "ws-3", "claude-code", "anthropic", "sk-ant-xyz")
	if err := s.DeleteLLMKey(t.Context(), "ws-3", "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetLLMKey(t.Context(), "ws-3", "claude-code"); err == nil {
		t.Error("expected ErrNotFound after delete")
	}
}

func TestLLMKey_PerEngineIsolation(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	s := newTestStore(t)
	if err := s.SetLLMKey(t.Context(), "ws-m", "claude-code", "anthropic", "sk-ant-aaaa1111"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLLMKey(t.Context(), "ws-m", "future-engine", "other", "ok-bbbb2222"); err != nil {
		t.Fatal(err)
	}
	if _, raw, _ := s.GetLLMKey(t.Context(), "ws-m", "claude-code"); raw != "sk-ant-aaaa1111" {
		t.Errorf("claude-code key = %q", raw)
	}
	if _, raw, _ := s.GetLLMKey(t.Context(), "ws-m", "future-engine"); raw != "ok-bbbb2222" {
		t.Errorf("future-engine key = %q", raw)
	}
	all, err := s.ListLLMKeys(t.Context(), "ws-m")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all["claude-code"] == nil || all["future-engine"] == nil {
		t.Fatalf("expected 2 engine keys, got %v", all)
	}
	if err := s.DeleteLLMKey(t.Context(), "ws-m", "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetLLMKey(t.Context(), "ws-m", "claude-code"); err == nil {
		t.Error("claude-code key should be gone")
	}
	if _, raw, _ := s.GetLLMKey(t.Context(), "ws-m", "future-engine"); raw != "ok-bbbb2222" {
		t.Errorf("future-engine key should survive, got %q", raw)
	}
}

func startsWith(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

func TestDeleteByWorkspace(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	s := newTestStore(t)
	_ = s.SetLLMKey(t.Context(), "ws-x", "claude-code", "anthropic", "sk-ant-key")
	if err := s.DeleteByWorkspace(t.Context(), "ws-x"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetLLMKey(t.Context(), "ws-x", "claude-code"); err == nil {
		t.Error("llm key should be gone after erasure")
	}
}
