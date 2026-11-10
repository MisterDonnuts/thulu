package secretbox_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/secretbox"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

const (
	jwtA  = "0123456789abcdef0123456789abcdef"
	keyK1 = "1111111111111111111111111111111111111111111111111111111111111111"
	keyK2 = "2222222222222222222222222222222222222222222222222222222222222222"
)

func setKeys(t *testing.T, current, previous string) {
	t.Helper()
	t.Setenv("OCTOPUS_JWT_SECRET", jwtA)
	t.Setenv("OCTOPUS_SECRETBOX_KEY", current)
	t.Setenv("OCTOPUS_SECRETBOX_KEY_FILE", "")
	t.Setenv("OCTOPUS_SECRETBOX_PREVIOUS_KEYS", previous)
	t.Setenv("OCTOPUS_SECRETBOX_ALLOW_PLAINTEXT", "")
}

func TestV2_DedicatedKeyIndependentOfJWTSecret(t *testing.T) {
	setKeys(t, keyK1, "")
	enc, err := secretbox.Encrypt("info", "sk-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "v2:"+secretbox.CurrentKeyID()+":") {
		t.Fatalf("value %q does not name its key", enc)
	}
	t.Setenv("OCTOPUS_JWT_SECRET", "ffffffffffffffffffffffffffffffff")
	if got, err := secretbox.Decrypt("info", enc); err != nil || got != "sk-secret" {
		t.Errorf("after JWT rotation: %q, %v", got, err)
	}
	if _, err := secretbox.Decrypt("other-info", enc); err == nil {
		t.Error("wrong info label decrypted")
	}
}

func TestV2_RotationWithPreviousKeys(t *testing.T) {
	setKeys(t, keyK1, "")
	old, _ := secretbox.Encrypt("info", "sk-secret")

	setKeys(t, keyK2, "")
	if _, err := secretbox.Decrypt("info", old); !errors.Is(err, secretbox.ErrUnknownKey) {
		t.Errorf("value from a dropped key: err=%v, want ErrUnknownKey", err)
	}
	setKeys(t, keyK2, keyK1)
	if got, err := secretbox.Decrypt("info", old); err != nil || got != "sk-secret" {
		t.Errorf("with the old key in PREVIOUS_KEYS: %q, %v", got, err)
	}
	if !secretbox.NeedsRewrap(old) {
		t.Error("a value under a retired key must need a rewrap")
	}
	fresh, _ := secretbox.Encrypt("info", "sk-secret")
	if secretbox.NeedsRewrap(fresh) {
		t.Error("a value under the current key needs no rewrap")
	}
}

func TestV1_StillReadableAfterAddingKey(t *testing.T) {
	setKeys(t, "", "")
	v1, _ := secretbox.Encrypt("info", "sk-secret")
	if !strings.HasPrefix(v1, "v1:") {
		t.Fatalf("no dedicated key should still write v1, got %q", v1)
	}
	if secretbox.NeedsRewrap(v1) {
		t.Error("v1 with no dedicated key has nowhere better to go")
	}
	setKeys(t, keyK1, "")
	if got, err := secretbox.Decrypt("info", v1); err != nil || got != "sk-secret" {
		t.Errorf("v1 after adding a key: %q, %v", got, err)
	}
	if !secretbox.NeedsRewrap(v1) {
		t.Error("v1 must move to v2 once a key exists")
	}
	v2, _ := secretbox.Encrypt("info", "sk-secret")
	setKeys(t, "", "")
	if secretbox.NeedsRewrap(v2) {
		t.Error("a v2 value would be rewritten under the JWT secret")
	}
}

func TestKeyConfig(t *testing.T) {
	setKeys(t, "too-short", "")
	if err := secretbox.ValidateConfig(); err == nil {
		t.Error("a short key was accepted")
	}
	if _, err := secretbox.Encrypt("info", "x"); err == nil {
		t.Error("Encrypt with a bad key config succeeded")
	}

	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(keyK1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setKeys(t, "", "")
	t.Setenv("OCTOPUS_SECRETBOX_KEY_FILE", path)
	fromFile, _ := secretbox.Encrypt("info", "x")
	t.Setenv("OCTOPUS_SECRETBOX_KEY_FILE", "")
	t.Setenv("OCTOPUS_SECRETBOX_KEY", keyK1)
	if got, err := secretbox.Decrypt("info", fromFile); err != nil || got != "x" {
		t.Errorf("KEY_FILE and KEY with the same material disagree: %q, %v", got, err)
	}
	t.Setenv("OCTOPUS_SECRETBOX_KEY_FILE", path)
	if err := secretbox.ValidateConfig(); err == nil {
		t.Error("KEY and KEY_FILE together were accepted")
	}
}

func TestMigrateAtStartup(t *testing.T) {
	db := testpg.New(t)
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE llm_keys (workspace_id TEXT, engine TEXT, key_encrypted TEXT NOT NULL);
		CREATE TABLE credentials (id TEXT PRIMARY KEY, provider TEXT NOT NULL, payload_encrypted TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	setKeys(t, "", "")
	v1, _ := secretbox.Encrypt("octopus-byok-v1", "sk-v1")
	setKeys(t, keyK1, "")
	oldKey, _ := secretbox.Encrypt("octopus-credential-github-v1", "ghp-old")
	setKeys(t, keyK2, keyK1)
	current, _ := secretbox.Encrypt("octopus-byok-v1", "sk-current")

	if _, err := db.ExecContext(ctx, `
		INSERT INTO llm_keys VALUES ('w1','claude',$1), ('w2','claude','sk-plain'), ('w3','claude',$2), ('w4','claude','v2:deadbeef:AAAA')`,
		v1, current); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO credentials VALUES ('c1','github',$1)`, oldKey); err != nil {
		t.Fatal(err)
	}

	rep, err := secretbox.MigrateAtStartup(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rewrapped != 3 || rep.Failed != 1 {
		t.Errorf("report = %+v, want 3 rewrapped (v1, plaintext, retired key) and 1 failed", rep)
	}

	want := map[string]string{"w1": "sk-v1", "w2": "sk-plain", "w3": "sk-current"}
	rows, err := db.QueryContext(ctx, `SELECT workspace_id, key_encrypted FROM llm_keys`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var ws, val string
		if err := rows.Scan(&ws, &val); err != nil {
			t.Fatal(err)
		}
		if ws == "w4" {
			if val != "v2:deadbeef:AAAA" {
				t.Errorf("undecryptable row was modified: %q", val)
			}
			continue
		}
		if secretbox.NeedsRewrap(val) {
			t.Errorf("%s still needs a rewrap: %q", ws, val)
		}
		if got, err := secretbox.Decrypt("octopus-byok-v1", val); err != nil || got != want[ws] {
			t.Errorf("%s = %q, %v; want %q", ws, got, err, want[ws])
		}
	}
	var cred string
	if err := db.QueryRowContext(ctx, `SELECT payload_encrypted FROM credentials WHERE id='c1'`).Scan(&cred); err != nil {
		t.Fatal(err)
	}
	setKeys(t, keyK2, "")
	if got, err := secretbox.Decrypt("octopus-credential-github-v1", cred); err != nil || got != "ghp-old" {
		t.Errorf("credential after rotation = %q, %v", got, err)
	}

	setKeys(t, keyK2, keyK1)
	if rep, err := secretbox.MigrateAtStartup(ctx, db); err != nil || rep.Rewrapped != 0 {
		t.Errorf("second run = %+v, %v; want nothing rewrapped", rep, err)
	}
}
