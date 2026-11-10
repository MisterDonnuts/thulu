package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/testdocker"
)

func requireRuntimeImage(t *testing.T, cli *client.Client) {
	t.Helper()
	testdocker.RequireImage(t, cli, DefaultRuntimeImage,
		"docker build -t octopus-runtime images/runtime/")
}

func withSandbox(t *testing.T, taskID string) (*Docker, string, map[int]int) {
	t.Helper()
	cli := testdocker.Client(t)
	requireRuntimeImage(t, cli)
	d := NewDocker(cli, DefaultRuntimeImage)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := d.Create(ctx, CreateOpts{TaskID: taskID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.Remove(ctx, res.ContainerID)
	})
	return d, res.ContainerID, res.HostPorts
}

func TestCreateProducesAliveContainerWithPorts(t *testing.T) {
	d, id, ports := withSandbox(t, "create-test-"+nowSlug())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if !d.Alive(ctx, id) {
		t.Errorf("container %s should be alive", id)
	}
	for _, p := range DefaultExposedPorts {
		hp, ok := ports[p]
		if !ok {
			t.Errorf("expected host port for container port %d", p)
			continue
		}
		if hp <= 0 || hp > 65535 {
			t.Errorf("invalid host port %d for container port %d", hp, p)
		}
	}
}

func TestExecHappyPath(t *testing.T) {
	d, id, _ := withSandbox(t, "exec-happy-"+nowSlug())
	ctx := context.Background()

	r, err := d.Exec(ctx, id, ExecOpts{Command: "echo hello"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if r.ExitCode != 0 {
		t.Errorf("ExitCode: %d (stderr=%q)", r.ExitCode, r.Stderr)
	}
	if !strings.Contains(r.Stdout, "hello") {
		t.Errorf("Stdout: %q", r.Stdout)
	}
	if r.TimedOut {
		t.Errorf("TimedOut should be false")
	}
}

func TestExecCwdHonored(t *testing.T) {
	d, id, _ := withSandbox(t, "exec-cwd-"+nowSlug())
	ctx := context.Background()

	r, _ := d.Exec(ctx, id, ExecOpts{Command: "pwd", Cwd: "/tmp"})
	if got := strings.TrimSpace(r.Stdout); got != "/tmp" {
		t.Errorf("expected /tmp, got %q", got)
	}
}

func TestExecDefaultCwdIsWorkspace(t *testing.T) {
	d, id, _ := withSandbox(t, "exec-default-cwd-"+nowSlug())
	r, _ := d.Exec(context.Background(), id, ExecOpts{Command: "pwd"})
	if got := strings.TrimSpace(r.Stdout); got != "/workspace" {
		t.Errorf("expected /workspace, got %q", got)
	}
}

func TestExecEnvMerged(t *testing.T) {
	d, id, _ := withSandbox(t, "exec-env-"+nowSlug())
	r, _ := d.Exec(context.Background(), id, ExecOpts{
		Command: "echo \"x=$OCTOPUS_TEST_X\"",
		Env:     map[string]string{"OCTOPUS_TEST_X": "ping"},
	})
	if !strings.Contains(r.Stdout, "x=ping") {
		t.Errorf("env not merged: %q", r.Stdout)
	}
}

func TestExecStderrSeparate(t *testing.T) {
	d, id, _ := withSandbox(t, "exec-stderr-"+nowSlug())
	r, _ := d.Exec(context.Background(), id, ExecOpts{
		Command: "echo OUT; echo ERR >&2",
	})
	if !strings.Contains(r.Stdout, "OUT") {
		t.Errorf("Stdout missing OUT: %q", r.Stdout)
	}
	if !strings.Contains(r.Stderr, "ERR") {
		t.Errorf("Stderr missing ERR: %q", r.Stderr)
	}
	if strings.Contains(r.Stdout, "ERR") {
		t.Errorf("Stdout leaked ERR: %q", r.Stdout)
	}
}

func TestExecFailingCommand(t *testing.T) {
	d, id, _ := withSandbox(t, "exec-fail-"+nowSlug())
	r, _ := d.Exec(context.Background(), id, ExecOpts{Command: "exit 42"})
	if r.ExitCode != 42 {
		t.Errorf("ExitCode: %d", r.ExitCode)
	}
}

func TestExecTimeoutKillsCommand(t *testing.T) {
	d, id, _ := withSandbox(t, "exec-timeout-"+nowSlug())
	start := time.Now()
	r, err := d.Exec(context.Background(), id, ExecOpts{
		Command:   "sleep 30",
		TimeoutMs: 500,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !r.TimedOut {
		t.Errorf("TimedOut should be true (exit=%d)", r.ExitCode)
	}
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Errorf("timeout did not enforce: took %v", elapsed)
	}
}

func TestExecLargeStdoutTruncated(t *testing.T) {
	d, id, _ := withSandbox(t, "exec-trunc-"+nowSlug())
	r, _ := d.Exec(context.Background(), id, ExecOpts{
		Command: "yes 'x' | head -c 250000",
	})
	if r.ExitCode != 0 {
		t.Errorf("ExitCode: %d (stderr=%q)", r.ExitCode, r.Stderr)
	}
	if !strings.Contains(r.Stdout, "[…truncated") {
		t.Errorf("expected truncation marker, got %d bytes", len(r.Stdout))
	}
	if len(r.Stdout) > 200_000 {
		t.Errorf("stdout should be capped well below 250KB, got %d", len(r.Stdout))
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	cli := testdocker.Client(t)
	requireRuntimeImage(t, cli)
	d := NewDocker(cli, DefaultRuntimeImage)

	if err := d.Remove(context.Background(), "container-that-does-not-exist"); err != nil {
		t.Errorf("Remove on missing container should be nil, got %v", err)
	}
}

func TestPortMappingHonorsCustomList(t *testing.T) {
	cli := testdocker.Client(t)
	requireRuntimeImage(t, cli)
	d := NewDocker(cli, DefaultRuntimeImage)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := d.Create(ctx, CreateOpts{
		TaskID:      "ports-custom-" + nowSlug(),
		ExposePorts: []int{4242, 9999},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = d.Remove(context.Background(), res.ContainerID) })

	if _, ok := res.HostPorts[4242]; !ok {
		t.Errorf("missing 4242 mapping in %v", res.HostPorts)
	}
	if _, ok := res.HostPorts[9999]; !ok {
		t.Errorf("missing 9999 mapping in %v", res.HostPorts)
	}
	if _, ok := res.HostPorts[3000]; ok {
		t.Errorf("3000 should NOT be mapped when custom list excludes it")
	}
}

func nowSlug() string {
	return time.Now().UTC().Format("150405.000")
}

func TestShellQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hello", "'hello'"},
		{"it's a test", "'it'\\''s a test'"},
		{"no special", "'no special'"},
		{"", "''"},
	}
	for _, c := range cases {
		got := shellQuote(c.in)
		if got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeTaskID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"valid-task_id.1", "valid-task_id.1"},
		{"task with spaces", "task-with-spaces"},
		{"task@special!", "task-special-"},
		{"", "unknown"},
		{"abc123", "abc123"},
	}
	for _, c := range cases {
		got := sanitizeTaskID(c.in)
		if got != c.want {
			t.Errorf("sanitizeTaskID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("abcdefghijklmnop"); got != "abcdefghijkl" {
		t.Errorf("shortID long: got %q, want abcdefghijkl", got)
	}
	if got := shortID("short"); got != "short" {
		t.Errorf("shortID short: got %q, want short", got)
	}
}

func TestWithSandboxDefaults(t *testing.T) {
	containsKV := func(envs []string, want string) bool {
		for _, e := range envs {
			if e == want {
				return true
			}
		}
		return false
	}
	countKey := func(envs []string, key string) int {
		n := 0
		for _, e := range envs {
			if strings.HasPrefix(e, key+"=") {
				n++
			}
		}
		return n
	}

	got := withSandboxDefaults(nil)
	if !containsKV(got, "NODE_OPTIONS=--max-old-space-size=4096") {
		t.Errorf("defaults not applied to empty extra: %v", got)
	}

	got = withSandboxDefaults([]string{"NODE_OPTIONS=--max-old-space-size=8192"})
	if countKey(got, "NODE_OPTIONS") != 1 {
		t.Errorf("expected exactly one NODE_OPTIONS entry, got %v", got)
	}
	if !containsKV(got, "NODE_OPTIONS=--max-old-space-size=8192") {
		t.Errorf("caller override not preserved: %v", got)
	}

	got = withSandboxDefaults([]string{"FOO=bar", "BAZ=qux"})
	if !containsKV(got, "FOO=bar") || !containsKV(got, "BAZ=qux") {
		t.Errorf("extras lost: %v", got)
	}
	if !containsKV(got, "NODE_OPTIONS=--max-old-space-size=4096") {
		t.Errorf("default missing alongside extras: %v", got)
	}
}
