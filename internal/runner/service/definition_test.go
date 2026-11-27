package service

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func specWithEnv() Spec {
	s := testSpec()
	s.Env = map[string]string{"HTTPS_PROXY": "http://proxy:3128", "NODE_EXTRA_CA_CERTS": "/etc/ssl/corp & co.pem"}
	return s
}

func TestPlistRoundTripsBinaryAndEnv(t *testing.T) {
	var buf bytes.Buffer
	data := struct {
		Spec
		Label string
	}{Spec: escaped(specWithEnv()), Label: Label}
	if err := launchdPlist.Execute(&buf, data); err != nil {
		t.Fatal(err)
	}
	def, err := parsePlist(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"PATH": "/usr/bin:/bin", "HTTPS_PROXY": "http://proxy:3128", "NODE_EXTRA_CA_CERTS": "/etc/ssl/corp & co.pem"}
	if def.Binary != testSpec().Binary || !reflect.DeepEqual(def.Env, want) {
		t.Fatalf("read back %+v", def)
	}
}

func TestUnitRoundTripsBinaryAndEnv(t *testing.T) {
	s := specWithEnv()
	s.Binary = `/opt/50% "off"/octopus`
	s.Env["PRICE"] = "100% $5"
	var buf bytes.Buffer
	if err := systemdUnit.Execute(&buf, unitEscaped(s)); err != nil {
		t.Fatal(err)
	}
	def := parseUnit(buf.String())
	if def.Binary != s.Binary {
		t.Fatalf("binary = %q\n%s", def.Binary, buf.String())
	}
	if def.Env["PRICE"] != "100% $5" || def.Env["PATH"] != "/usr/bin:/bin" || def.Env["HTTPS_PROXY"] != "http://proxy:3128" {
		t.Fatalf("env = %v\n%s", def.Env, buf.String())
	}
}

// A plist a person edited by hand: an extra variable, a PATH with their own directories, keys in
// another order and a nested dict that is not the environment.
func TestParsePlistReadsAHandEditedFile(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key><false/></dict>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>/opt/homebrew/bin:/Users/x/.volta/bin:/usr/bin</string>
    <key>ANTHROPIC_BASE_URL</key><string>https://gateway.corp</string>
  </dict>
  <key>ProgramArguments</key>
  <array><string>/Users/x/.local/bin/thulu</string><string>runner</string><string>run</string></array>
</dict>
</plist>`
	def, err := parsePlist([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if def.Binary != "/Users/x/.local/bin/thulu" || def.Env["ANTHROPIC_BASE_URL"] != "https://gateway.corp" ||
		def.Env["PATH"] != "/opt/homebrew/bin:/Users/x/.volta/bin:/usr/bin" || len(def.Env) != 2 {
		t.Fatalf("read %+v", def)
	}
}

func TestMergeEnvKeepsTheUsersEdits(t *testing.T) {
	old := map[string]string{
		"PATH":               "/Users/x/.volta/bin:/usr/bin",
		"ANTHROPIC_BASE_URL": "https://gateway.corp",
		"HTTPS_PROXY":        "http://old-proxy",
	}
	m := MergeEnv(old, "/opt/homebrew/bin:/usr/bin:/bin", map[string]string{"HTTPS_PROXY": "http://new-proxy"})
	if m.Path != "/Users/x/.volta/bin:/usr/bin:/opt/homebrew/bin:/bin" {
		t.Fatalf("PATH = %s: the old entries first, in their order, then the shell's missing ones", m.Path)
	}
	if m.Env["ANTHROPIC_BASE_URL"] != "https://gateway.corp" || m.Env["HTTPS_PROXY"] != "http://new-proxy" {
		t.Fatalf("env = %v", m.Env)
	}
	if _, ok := m.Env["PATH"]; ok {
		t.Fatal("PATH travels in Path, not in Env")
	}
	if want := []string{"ANTHROPIC_BASE_URL", "PATH"}; !slices.Equal(m.Kept, want) {
		t.Fatalf("kept = %v, want %v (service_env's own keys are not 'kept')", m.Kept, want)
	}

	set := MergeEnv(old, "/usr/bin", map[string]string{"PATH": "/only/this"})
	if set.Path != "/only/this" {
		t.Fatalf("service_env.PATH replaces the PATH: %s", set.Path)
	}
	fresh := MergeEnv(nil, "/usr/bin:/bin", nil)
	if fresh.Path != "/usr/bin:/bin" || len(fresh.Env) != 0 || len(fresh.Kept) != 0 {
		t.Fatalf("a first install takes the shell's PATH: %+v", fresh)
	}
}

func TestInstalledReadsTheLaunchdAgent(t *testing.T) {
	useFake(t, "darwin")
	if _, ok, err := Installed(t.Context()); ok || err != nil {
		t.Fatalf("nothing installed: ok=%v err=%v", ok, err)
	}
	path, err := plistPath()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	data := struct {
		Spec
		Label string
	}{Spec: escaped(specWithEnv()), Label: Label}
	if err := launchdPlist.Execute(&buf, data); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	def, ok, err := Installed(t.Context())
	if err != nil || !ok || def.File != path || def.Env["HTTPS_PROXY"] != "http://proxy:3128" {
		t.Fatalf("def = %+v ok=%v err=%v", def, ok, err)
	}
}

func TestInstalledReadsTheSystemdUnit(t *testing.T) {
	useFake(t, "linux")
	path, err := unitPath()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := systemdUnit.Execute(&buf, unitEscaped(specWithEnv())); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	def, ok, err := Installed(t.Context())
	if err != nil || !ok || def.Binary != testSpec().Binary || def.Env["NODE_EXTRA_CA_CERTS"] != "/etc/ssl/corp & co.pem" {
		t.Fatalf("def = %+v ok=%v err=%v", def, ok, err)
	}
}

func TestRestartKicksTheService(t *testing.T) {
	f := useFake(t, "darwin")
	if err := Restart(t.Context()); err != nil || !f.ran("launchctl kickstart -k") {
		t.Fatalf("calls %v, err %v", f.calls, err)
	}
	f = useFake(t, "linux")
	if err := Restart(t.Context()); err != nil || !f.ran("systemctl --user restart") {
		t.Fatalf("calls %v, err %v", f.calls, err)
	}
}
