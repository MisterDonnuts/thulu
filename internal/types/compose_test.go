package types

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandEnv(t *testing.T) {
	lookup := func(key string) (string, bool) {
		switch key {
		case "FOO":
			return "bar", true
		case "EMPTY":
			return "", true
		default:
			return "", false
		}
	}

	tests := []struct {
		input string
		want  string
	}{
		{"${FOO}", "bar"},
		{"${FOO:-fallback}", "bar"},
		{"${MISSING:-fallback}", "fallback"},
		{"${EMPTY:-fallback}", "fallback"},
		{"$FOO", "bar"},
		{"$MISSING", ""},
		{"hello-${FOO}-world", "hello-bar-world"},
		{"plain-text", "plain-text"},
		{"${FOO}-${MISSING:-def}", "bar-def"},
		{"${MISSING:-}", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ExpandEnv(tt.input, lookup)
			if got != tt.want {
				t.Errorf("ExpandEnv(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseVolume(t *testing.T) {
	baseDir := "/home/user/agents/planner"

	tests := []struct {
		input   string
		wantSrc string
		wantTgt string
		wantRO  bool
	}{
		{
			"./runs:/workspace/runs",
			"/home/user/agents/planner/runs",
			"/workspace/runs",
			false,
		},
		{
			"../images/entrypoint-base.sh:/workspace/entrypoint.sh:ro",
			"/home/user/agents/images/entrypoint-base.sh",
			"/workspace/entrypoint.sh",
			true,
		},
		{
			"/var/run/docker.sock:/var/run/docker.sock",
			"/var/run/docker.sock",
			"/var/run/docker.sock",
			false,
		},
		{
			"./prompts:/workspace/instructions/prompts:ro",
			"/home/user/agents/planner/prompts",
			"/workspace/instructions/prompts",
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			src, tgt, ro := ParseVolume(tt.input, baseDir)
			if src != tt.wantSrc {
				t.Errorf("source = %q, want %q", src, tt.wantSrc)
			}
			if tgt != tt.wantTgt {
				t.Errorf("target = %q, want %q", tgt, tt.wantTgt)
			}
			if ro != tt.wantRO {
				t.Errorf("readOnly = %v, want %v", ro, tt.wantRO)
			}
		})
	}
}

func TestParseEnvFile(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")

	content := `# Comment
FOO=bar
BAZ=qux

# Another comment
EMPTY=
KEY_WITH_EQUALS=val=ue
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	entries, err := ParseEnvFile(envFile)
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{"FOO=bar", "BAZ=qux", "EMPTY=", "KEY_WITH_EQUALS=val=ue"}
	if len(entries) != len(expected) {
		t.Fatalf("got %d entries, want %d: %v", len(entries), len(expected), entries)
	}
	for i, e := range entries {
		if e != expected[i] {
			t.Errorf("entry[%d] = %q, want %q", i, e, expected[i])
		}
	}
}

func TestParseComposeSpec(t *testing.T) {
	dir := t.TempDir()
	compose := `services:
  myagent:
    build: .
    container_name: test-container
    env_file:
      - ../.env
    environment:
      - HOME=/home/node
      - AGENT_NAME=test
      - MAX_TURNS=20
    labels:
      thulu.agent: "true"
      thulu.type: "test"
    volumes:
      - ./runs:/workspace/runs
      - ./prompt.md:/workspace/prompt.md:ro
    network_mode: host
    shm_size: '2gb'
    stop_grace_period: 5m
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}

	spec, svcName, err := ParseComposeSpec(dir)
	if err != nil {
		t.Fatal(err)
	}

	if svcName != "myagent" {
		t.Errorf("service name = %q, want %q", svcName, "myagent")
	}

	if spec.Build.Context != "." {
		t.Errorf("build.context = %q, want %q", spec.Build.Context, ".")
	}

	if len(spec.EnvFile) != 1 || spec.EnvFile[0] != "../.env" {
		t.Errorf("env_file = %v, want [../.env]", spec.EnvFile)
	}

	if len(spec.Environment) != 3 {
		t.Errorf("environment has %d entries, want 3", len(spec.Environment))
	}

	if spec.Labels["thulu.agent"] != "true" {
		t.Errorf("labels[thulu.agent] = %q, want %q", spec.Labels["thulu.agent"], "true")
	}

	if len(spec.Volumes) != 2 {
		t.Errorf("volumes has %d entries, want 2", len(spec.Volumes))
	}

	if spec.NetworkMode != "host" {
		t.Errorf("network_mode = %q, want %q", spec.NetworkMode, "host")
	}

	if spec.ShmSize != "2gb" {
		t.Errorf("shm_size = %q, want %q", spec.ShmSize, "2gb")
	}

	if spec.StopGrace != "5m" {
		t.Errorf("stop_grace_period = %q, want %q", spec.StopGrace, "5m")
	}
}

func TestParseComposeSpec_BuildObject(t *testing.T) {
	dir := t.TempDir()
	compose := `services:
  test:
    build:
      context: ../template-agent
      dockerfile: Dockerfile.custom
    environment:
      - HOME=/home/node
    labels:
      thulu.agent: "true"
    volumes:
      - ./runs:/workspace/runs
    network_mode: host
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}

	spec, _, err := ParseComposeSpec(dir)
	if err != nil {
		t.Fatal(err)
	}

	if spec.Build.Context != "../template-agent" {
		t.Errorf("build.context = %q, want %q", spec.Build.Context, "../template-agent")
	}
	if spec.Build.Dockerfile != "Dockerfile.custom" {
		t.Errorf("build.dockerfile = %q, want %q", spec.Build.Dockerfile, "Dockerfile.custom")
	}
}

func TestParseComposeSpec_EnvFileString(t *testing.T) {
	dir := t.TempDir()
	compose := `services:
  test:
    build: .
    env_file: ../.env
    environment:
      - HOME=/home/node
    labels:
      thulu.agent: "true"
    volumes:
      - ./runs:/workspace/runs
    network_mode: host
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}

	spec, _, err := ParseComposeSpec(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(spec.EnvFile) != 1 || spec.EnvFile[0] != "../.env" {
		t.Errorf("env_file = %v, want [../.env]", spec.EnvFile)
	}
}

func TestResolveEnvironment(t *testing.T) {
	dir := t.TempDir()

	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte("SECRET=from_file\nOVERRIDE_ME=file_val\n"), 0644)

	spec := &ComposeServiceSpec{
		EnvFile:     composeEnvFile{".env"},
		Environment: []string{"HOME=/home/node", "AGENT_NAME=${AGENT_NAME:-default}", "OVERRIDE_ME=compose_val"},
	}

	overrides := map[string]string{
		"AGENT_NAME":  "custom",
		"OVERRIDE_ME": "override_val",
	}

	env, err := ResolveEnvironment(spec, dir, overrides)
	if err != nil {
		t.Fatal(err)
	}

	envMap := make(map[string]string)
	for _, e := range env {
		k, v, _ := cut(e, "=")
		envMap[k] = v
	}

	if envMap["SECRET"] != "from_file" {
		t.Errorf("SECRET = %q, want %q", envMap["SECRET"], "from_file")
	}

	if envMap["HOME"] != "/home/node" {
		t.Errorf("HOME = %q, want %q", envMap["HOME"], "/home/node")
	}

	if envMap["AGENT_NAME"] != "custom" {
		t.Errorf("AGENT_NAME = %q, want %q", envMap["AGENT_NAME"], "custom")
	}

	if envMap["OVERRIDE_ME"] != "override_val" {
		t.Errorf("OVERRIDE_ME = %q, want %q", envMap["OVERRIDE_ME"], "override_val")
	}
}

func TestParseGracePeriod(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"5m", 300},
		{"30s", 30},
		{"1m", 60},
		{"unknown", 30},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseGracePeriod(tt.input)
			if got != tt.want {
				t.Errorf("parseGracePeriod(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeContainerName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"with spaces", "with-spaces"},
		{"olla guacho-123", "olla-guacho-123"},
		{"my_agent.v2", "my_agent.v2"},
		{"---leading", "leading"},
		{"  spaces  ", "spaces"},
		{"café-latte", "caf--latte"},
		{"", ""},
		{"valid-name_1.0", "valid-name_1.0"},
		{"a/b:c", "a-b-c"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeContainerName(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeContainerName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseComposeSpec_CommandString(t *testing.T) {
	dir := t.TempDir()
	compose := `services:
  myagent:
    image: octopus-docker
    command: "/bin/sh /entrypoint.sh"
    labels:
      thulu.agent: "true"
      thulu.type: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	spec, _, err := ParseComposeSpec(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Command) != 3 || spec.Command[0] != "/bin/sh" || spec.Command[1] != "-c" {
		t.Errorf("command string form = %v, want [/bin/sh -c <cmd>]", spec.Command)
	}
}

func TestParseComposeSpec_CommandList(t *testing.T) {
	dir := t.TempDir()
	compose := `services:
  myagent:
    image: octopus-docker
    command: ["/bin/sh", "/entrypoint.sh"]
    labels:
      thulu.agent: "true"
      thulu.type: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	spec, _, err := ParseComposeSpec(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Command) != 2 || spec.Command[0] != "/bin/sh" || spec.Command[1] != "/entrypoint.sh" {
		t.Errorf("command list form = %v, want [/bin/sh /entrypoint.sh]", spec.Command)
	}
}

func TestParseComposeSpec_FileNotFound(t *testing.T) {
	_, _, err := ParseComposeSpec(t.TempDir())
	if err == nil {
		t.Error("expected error when docker-compose.yml missing")
	}
}

func TestParseComposeSpec_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("{invalid yaml:::"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ParseComposeSpec(dir)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestParseComposeSpec_NoServices(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ParseComposeSpec(dir)
	if err == nil {
		t.Error("expected error when no services defined")
	}
}

func TestParseEnvFile_NotFound(t *testing.T) {
	_, err := ParseEnvFile("/tmp/does-not-exist-xyz.env")
	if err == nil {
		t.Error("expected error for missing env file")
	}
}

func TestParseComposeSpec_CommandEmpty(t *testing.T) {
	dir := t.TempDir()
	compose := `services:
  myagent:
    image: octopus-docker
    command: ""
    labels:
      thulu.agent: "true"
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	spec, _, err := ParseComposeSpec(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Command) != 0 {
		t.Errorf("empty command string: want nil/empty, got %v", spec.Command)
	}
}

func TestParseVolume_NoColon(t *testing.T) {
	src, tgt, ro := ParseVolume("nodestination", "/base")
	if src != "nodestination" || tgt != "nodestination" || ro {
		t.Errorf("ParseVolume(no colon) = (%q, %q, %v), want same string twice, false", src, tgt, ro)
	}
}

func TestExpandEnv_MissingNoDefault(t *testing.T) {
	lookup := func(key string) (string, bool) { return "", false }
	got := ExpandEnv("${MISSING}", lookup)
	if got != "" {
		t.Errorf("ExpandEnv(${MISSING}) = %q, want empty string", got)
	}
}

func TestResolveEnvironment_EnvEntryNoEquals(t *testing.T) {
	spec := &ComposeServiceSpec{
		Environment: []string{"NOEQUALS_ENTRY"},
	}
	env, err := ResolveEnvironment(spec, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range env {
		if e == "NOEQUALS_ENTRY" {
			t.Error("env entry without '=' should not appear in result")
		}
	}
}

func TestResolveEnvironment_MissingEnvFile(t *testing.T) {
	spec := &ComposeServiceSpec{
		EnvFile: composeEnvFile{"does-not-exist.env"},
	}
	_, err := ResolveEnvironment(spec, t.TempDir(), nil)
	if err == nil {
		t.Error("expected error when env_file does not exist")
	}
}

func TestParseComposeSpec_CommandMapError(t *testing.T) {
	dir := t.TempDir()
	compose := `services:
  myagent:
    image: octopus-docker
    command:
      key: value
    labels:
      thulu.agent: "true"
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ParseComposeSpec(dir)
	if err == nil {
		t.Error("expected error when command is a map (neither string nor list)")
	}
}

func TestParseComposeSpec_EnvFileMapError(t *testing.T) {
	dir := t.TempDir()
	compose := `services:
  myagent:
    image: octopus-docker
    env_file:
      key: value
    labels:
      thulu.agent: "true"
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ParseComposeSpec(dir)
	if err == nil {
		t.Error("expected error when env_file is a map (neither string nor list)")
	}
}

func cut(s, sep string) (before, after string, found bool) {
	i := indexOf(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
