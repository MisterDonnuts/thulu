package secretbox_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/secretbox"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("OCTOPUS_SECRETBOX_KEY", "")
	const info = "octopus-test-v1"
	plain := "sk-ant-supersecret-key-value"

	enc, err := secretbox.Encrypt(info, plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(enc, "v1:") {
		t.Error("ciphertext should carry the v1 prefix")
	}
	if strings.Contains(enc, plain) {
		t.Error("ciphertext must not contain the plaintext")
	}
	got, err := secretbox.Decrypt(info, enc)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plain {
		t.Errorf("round-trip = %q, want %q", got, plain)
	}
}

func TestDecrypt_WrongInfoFails(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	enc, _ := secretbox.Encrypt("info-a", "secret")
	if _, err := secretbox.Decrypt("info-b", enc); err == nil {
		t.Error("decrypt with a different info label must fail")
	}
}

func TestDecrypt_LegacyPlaintextNeedsOptIn(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT", "")
	if _, err := secretbox.Decrypt("info", "legacy-plaintext"); !errors.Is(err, secretbox.ErrPlaintext) {
		t.Errorf("plaintext read without opt-in: err=%v, want ErrPlaintext", err)
	}
	t.Setenv("OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT", "1")
	got, err := secretbox.Decrypt("info", "legacy-plaintext")
	if err != nil || got != "legacy-plaintext" {
		t.Errorf("legacy passthrough with opt-in = %q err=%v", got, err)
	}
	t.Setenv("OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT", "")
	if got, err := secretbox.Decrypt("info", ""); got != "" || err != nil {
		t.Errorf("empty = %q, %v", got, err)
	}
}

func TestEncrypt_Empty(t *testing.T) {
	t.Setenv("OCTOPUS_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	enc, err := secretbox.Encrypt("info", "")
	if err != nil || enc != "" {
		t.Errorf("empty plaintext should encrypt to empty, got %q err=%v", enc, err)
	}
}

func TestMask(t *testing.T) {
	if m := secretbox.Mask("sk-ant-abcd1234"); m != "…1234" {
		t.Errorf("Mask = %q, want …1234", m)
	}
	if m := secretbox.Mask("abc"); m != "…" {
		t.Errorf("short secret should be fully masked, got %q", m)
	}
}
