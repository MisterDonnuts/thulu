package hostrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// withPolicyFixture points the policy reader at a temp managed-settings directory, plists and user
// settings for one test.
func withPolicyFixture(t *testing.T, plists []string, plist func(context.Context, []byte) ([]byte, error)) (dir, user string) {
	t.Helper()
	dir = t.TempDir()
	user = filepath.Join(t.TempDir(), "settings.json")
	oldDir, oldPlists, oldRead, oldUser := managedSettingsDir, managedPlists, readPlist, userSettingsPath
	t.Cleanup(func() {
		managedSettingsDir, managedPlists, readPlist, userSettingsPath = oldDir, oldPlists, oldRead, oldUser
	})
	managedSettingsDir = func() string { return dir }
	managedPlists = func() []string { return plists }
	userSettingsPath = func() string { return user }
	if plist != nil {
		readPlist = plist
	}
	return dir, user
}

func TestReadPolicyFindsDisableBypassInManagedSettings(t *testing.T) {
	dir, _ := withPolicyFixture(t, nil, nil)
	if p := ReadPolicy(t.Context(), false); p.BypassDisabled || len(p.Sources) != 0 {
		t.Fatalf("no managed settings, no policy: %+v", p)
	}
	writeJSON(t, filepath.Join(dir, "managed-settings.json"), map[string]any{
		"permissions":      map[string]any{"disableBypassPermissionsMode": "disable"},
		"deniedMcpServers": []any{map[string]any{"serverName": "filesystem"}, map[string]any{"serverUrl": "https://x/*"}},
	})
	p := ReadPolicy(t.Context(), false)
	if !p.BypassDisabled || p.BypassSource != filepath.Join(dir, "managed-settings.json") {
		t.Fatalf("policy = %+v", p)
	}
	if !slices.Equal(p.DeniedMCP, []string{"filesystem"}) || !p.Blocks("filesystem") || p.Blocks("github") {
		t.Fatalf("denied = %v", p.DeniedMCP)
	}
}

func TestReadPolicyReadsDropInsAndAllowlists(t *testing.T) {
	dir, _ := withPolicyFixture(t, nil, nil)
	writeJSON(t, filepath.Join(dir, "managed-settings.d", "10-mcp.json"), map[string]any{
		"allowedMcpServers": []any{map[string]any{"serverName": "github"}},
	})
	writeJSON(t, filepath.Join(dir, "managed-settings.d", "20-perms.json"), map[string]any{
		"permissions": map[string]any{"disableBypassPermissionsMode": "disable"},
	})
	p := ReadPolicy(t.Context(), false)
	if !p.BypassDisabled || p.BypassSource != filepath.Join(dir, "managed-settings.d", "20-perms.json") {
		t.Fatalf("policy = %+v", p)
	}
	if !p.AllowList || p.Blocks("github") || !p.Blocks("slack") {
		t.Fatalf("an allowlist keeps out what it does not name: %+v", p)
	}
}

func TestReadPolicyCountsAnUnreadableSourceAsRestrictive(t *testing.T) {
	dir, _ := withPolicyFixture(t, nil, nil)
	path := filepath.Join(dir, "managed-settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := ReadPolicy(t.Context(), false); !p.BypassDisabled || p.BypassSource != path {
		t.Fatalf("a broken managed file turns bypass off, as claude does: %+v", p)
	}
}

func TestReadPolicyReadsTheMDMProfile(t *testing.T) {
	plist := filepath.Join(t.TempDir(), "com.anthropic.claudecode.plist")
	if err := os.WriteFile(plist, []byte("bplist00…"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []byte
	withPolicyFixture(t, []string{plist, filepath.Join(t.TempDir(), "missing.plist")}, func(_ context.Context, body []byte) ([]byte, error) {
		got = body
		return []byte(`{"permissions":{"disableBypassPermissionsMode":"disable"}}`), nil
	})
	p := ReadPolicy(t.Context(), false)
	if string(got) != "bplist00…" || !p.BypassDisabled || p.BypassSource != plist {
		t.Fatalf("policy = %+v, plutil got %q", p, got)
	}

	withPolicyFixture(t, []string{plist}, func(context.Context, []byte) ([]byte, error) {
		return nil, errors.New("plutil failed")
	})
	if p := ReadPolicy(t.Context(), false); !p.BypassDisabled {
		t.Fatalf("a profile plutil cannot read counts as restrictive: %+v", p)
	}
}

func TestReadPolicyReadsUserSettingsOnlyWhenRunsGetThem(t *testing.T) {
	_, user := withPolicyFixture(t, nil, nil)
	writeJSON(t, user, map[string]any{"permissions": map[string]any{"disableBypassPermissionsMode": "disable"}})
	if p := ReadPolicy(t.Context(), false); p.BypassDisabled {
		t.Fatalf("without user_settings the run never reads ~/.claude/settings.json: %+v", p)
	}
	if p := ReadPolicy(t.Context(), true); !p.BypassDisabled || p.BypassSource != user {
		t.Fatalf("with user_settings it does: %+v", p)
	}
}
