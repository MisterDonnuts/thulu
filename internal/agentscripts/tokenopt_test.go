package agentscripts

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func claudeArgs(t *testing.T, r *adapterRun) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.log, "args"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

type hookSettings struct {
	Hooks struct {
		PreToolUse []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"PreToolUse"`
	} `json:"hooks"`
}

func TestClaudeAdapter_TokenOptimizerEnforceWiresHooks(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaudeEnv)
	state := filepath.Join(t.TempDir(), "tokopt")
	r.run(t, "claude-code.sh", "engine_run", "hello",
		"ANTHROPIC_API_KEY=k", "OCTOPUS_TOKEN_OPTIMIZER=enforce",
		"OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR="+state)

	args := claudeArgs(t, r)
	i := slices.Index(args, "--settings")
	if i == -1 || i+1 >= len(args) {
		t.Fatalf("claude args lack --settings <file>:\n%s", strings.Join(args, "\n"))
	}
	if !slices.Contains(args, "--strict-mcp-config") || !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("the other flags must stay:\n%s", strings.Join(args, "\n"))
	}
	raw, err := os.ReadFile(args[i+1])
	if err != nil {
		t.Fatal(err)
	}
	var s hookSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("settings are not JSON: %v\n%s", err, raw)
	}
	cmds := map[string]string{}
	for _, m := range s.Hooks.PreToolUse {
		if len(m.Hooks) != 1 || m.Hooks[0].Type != "command" || m.Hooks[0].Timeout <= 0 {
			t.Fatalf("matcher %s: %+v", m.Matcher, m.Hooks)
		}
		cmds[m.Matcher] = m.Hooks[0].Command
	}
	hook := filepath.Join(snippetsDir(t), "token-optimizer-hook.js")
	for matcher, mode := range map[string]string{"Read": "read", "Bash": "bash"} {
		if want := "node '" + hook + "' " + mode; cmds[matcher] != want {
			t.Errorf("%s hook = %q, want %q", matcher, cmds[matcher], want)
		}
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	big := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 40000)), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bashOrSkip(t), "-c", cmds["Read"])
	cmd.Env = append(os.Environ(), "OCTOPUS_TOKEN_OPTIMIZER=enforce", "OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR="+state)
	cmd.Stdin = strings.NewReader(`{"tool_name":"Read","tool_input":{"file_path":"` + big + `"}}`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	if !strings.Contains(string(out), `"permissionDecision":"deny"`) {
		t.Errorf("hook output = %s, want a deny", out)
	}
}

func TestClaudeAdapter_TokenOptimizerSuggestOrOffHasNoHooks(t *testing.T) {
	for _, level := range []string{"suggest", ""} {
		r := newAdapterRun(t, "claude", fakeClaudeEnv)
		r.run(t, "claude-code.sh", "engine_run", "hello",
			"ANTHROPIC_API_KEY=k", "OCTOPUS_TOKEN_OPTIMIZER="+level,
			"OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR="+filepath.Join(t.TempDir(), "tokopt"))
		if args := claudeArgs(t, r); slices.Contains(args, "--settings") {
			t.Errorf("level %q: claude got --settings:\n%s", level, strings.Join(args, "\n"))
		}
	}
}

func engineRunCopy(t *testing.T) string {
	t.Helper()
	src := snippetsDir(t)
	dir := filepath.Join(t.TempDir(), "snippets")
	for _, d := range []string{"engines", "bin"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyFile := func(name string, mode os.FileMode, edit func(string) string) {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(edit(string(b))), mode); err != nil {
			t.Fatal(err)
		}
	}
	same := func(s string) string { return s }
	copyFile("i18n.sh", 0o644, same)
	copyFile(filepath.Join("bin", "octopus-delegate"), 0o755, same)
	copyFile("octopus-delegate.js", 0o644, same)
	copyFile("token-optimizer-worker-host.js", 0o644, same)
	copyFile("engine-run.sh", 0o755, same)
	writeFile(t, filepath.Join(dir, "engines", "fake.sh"), `engine_run() {
  echo '{"type":"octopus.session","mode":"fresh"}'
  echo "level=${OCTOPUS_TOKEN_OPTIMIZER-unset} bytes=${OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES-unset} worker_token=${OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN-unset}"
  echo "delegate=$(command -v octopus-delegate || echo none)"
  echo "socket=${OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET-unset} host=${_TOKOPT_HOST_PID:-none}"
  if [ -n "${OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET:-}" ]; then
    echo "dir=$(ls -ld "$(dirname "$OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET")" | cut -c1-10) sock=$(ls -l "$OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET" | cut -c1-10)"
  fi
}
`)
	return dir
}

func TestEngineRun_TokenOptimizerLine(t *testing.T) {
	dir := engineRunCopy(t)
	state := filepath.Join(t.TempDir(), "tokopt")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(env ...string) []string {
		t.Helper()
		writeFile(t, filepath.Join(state, "failures"), "2\n")
		cmd := exec.Command(bashOrSkip(t), filepath.Join(dir, "engine-run.sh"))
		cmd.Env = append(os.Environ(), append([]string{
			"ENGINE=fake", "OCTOPUS_LOCALE=en", "OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR=" + state,
			"OCTOPUS_ROOT=" + filepath.Dir(dir),
			"PATH=" + os.Getenv("PATH"),
		}, env...)...)
		cmd.Stdin = strings.NewReader("prompt")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("engine-run.sh: %v\n%s", err, out)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}

	lines := run("OCTOPUS_TOKEN_OPTIMIZER=enforce", "OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES=30000",
		"OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN=worker-tok")
	if lines[0] != `{"type":"octopus.token_optimizer","level":"enforce","threshold_bytes":30000}` {
		t.Errorf("first line = %q", lines[0])
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Errorf("not JSON: %v", err)
	}
	if lines[2] != "level=enforce bytes=30000 worker_token=unset" {
		t.Errorf("engine env = %q", lines[2])
	}
	if want := "delegate=" + filepath.Join(dir, "bin", "octopus-delegate"); lines[3] != want {
		t.Errorf("%s, want %s", lines[3], want)
	}
	if _, err := os.Stat(filepath.Join(state, "failures")); !os.IsNotExist(err) {
		t.Errorf("an earlier run's failure counter survived: %v", err)
	}

	for _, bad := range []string{"", "abc", "0", "-5"} {
		lines = run("OCTOPUS_TOKEN_OPTIMIZER=suggest", "OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES="+bad)
		if lines[0] != `{"type":"octopus.token_optimizer","level":"suggest","threshold_bytes":24576}` {
			t.Errorf("threshold %q: first line = %q", bad, lines[0])
		}
	}
	lines = run("OCTOPUS_TOKEN_OPTIMIZER=suggest", "OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES=0042")
	if lines[0] != `{"type":"octopus.token_optimizer","level":"suggest","threshold_bytes":42}` {
		t.Errorf("leading zeros: first line = %q", lines[0])
	}

	for _, level := range []string{"", "off", "bogus"} {
		lines = run("OCTOPUS_TOKEN_OPTIMIZER="+level, "OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN=worker-tok")
		if lines[0] != `{"type":"octopus.session","mode":"fresh"}` {
			t.Errorf("level %q: first line = %q", level, lines[0])
		}
		if lines[1] != "level=unset bytes=unset worker_token=unset" || lines[2] != "delegate=none" {
			t.Errorf("level %q: engine saw %q / %q", level, lines[1], lines[2])
		}
	}
}

func TestEngineRun_WorkerHostOnlyForCLIWorkers(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	dir := engineRunCopy(t)
	// A unix socket path must fit in ~104 bytes; t.TempDir() can be longer.
	tmp, err := os.MkdirTemp("/tmp", "eh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	run := func(env ...string) (stdout []string, stderr string) {
		t.Helper()
		cmd := exec.Command(bashOrSkip(t), filepath.Join(dir, "engine-run.sh"))
		cmd.Env = append(os.Environ(), append([]string{
			"ENGINE=fake", "OCTOPUS_LOCALE=en", "TMPDIR=" + tmp, "PATH=" + os.Getenv("PATH"),
			"OCTOPUS_ROOT=" + filepath.Dir(dir),
			"OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR=" + filepath.Join(tmp, "state"),
			"OCTOPUS_TOKEN_OPTIMIZER_READ_VIA=", "OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN=",
		}, env...)...)
		cmd.Stdin = strings.NewReader("prompt")
		var errb strings.Builder
		cmd.Stderr = &errb
		out, _ := cmd.Output() // error ignored on purpose: the failing engine below exits non-zero
		return strings.Split(strings.TrimSpace(string(out)), "\n"), errb.String()
	}
	field := func(lines []string, prefix string) string {
		for _, l := range lines {
			if strings.HasPrefix(l, prefix) {
				return l
			}
		}
		return ""
	}
	const token = "OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN=worker-tok"

	for _, env := range [][]string{
		{"OCTOPUS_TOKEN_OPTIMIZER=enforce", token},
		{"OCTOPUS_TOKEN_OPTIMIZER=enforce", "OCTOPUS_TOKEN_OPTIMIZER_READ_VIA=api", token},
		{"OCTOPUS_TOKEN_OPTIMIZER=", "OCTOPUS_TOKEN_OPTIMIZER_READ_VIA=cli", token},
		{"OCTOPUS_TOKEN_OPTIMIZER=enforce", "OCTOPUS_TOKEN_OPTIMIZER_WRITE_VIA=cli", token},
	} {
		lines, _ := run(env...)
		if got := field(lines, "socket="); got != "socket=unset host=none" {
			t.Errorf("%v: %q, want no host", env, got)
		}
	}

	lines, stderr := run("OCTOPUS_TOKEN_OPTIMIZER=suggest", "OCTOPUS_TOKEN_OPTIMIZER_READ_VIA=cli")
	if got := field(lines, "socket="); got != "socket=none host=none" {
		t.Errorf("cli worker without a token: %q, want socket=none and no host", got)
	}
	if !strings.Contains(stderr, "WARN: the token optimizer's in-pod worker has no token") {
		t.Errorf("cli worker without a token: no warning on stderr:\n%s", stderr)
	}

	writeFile(t, filepath.Join(dir, "engines", "codex.sh"), "engine_run() { echo \"socket=${OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET-unset} host=${_TOKOPT_HOST_PID:-none}\"; }\n")
	for _, engine := range []string{"fake", "codex"} {
		lines, _ := run("ENGINE="+engine, "OCTOPUS_TOKEN_OPTIMIZER=suggest", "OCTOPUS_TOKEN_OPTIMIZER_READ_VIA=cli", token)
		if engine == "fake" && lines[0] != `{"type":"octopus.token_optimizer","level":"suggest","threshold_bytes":24576}` {
			t.Errorf("stdout must stay the engine's stream, first line = %q", lines[0])
		}
		sockLine := field(lines, "socket=")
		var sock string
		var pid int
		if _, err := fmt.Sscanf(sockLine, "socket=%s host=%d", &sock, &pid); err != nil || pid <= 0 {
			t.Fatalf("%s: %q (%v)", engine, sockLine, err)
		}
		if engine == "fake" {
			if got := field(lines, "dir="); got != "dir=drwx------ sock=srw-------" {
				t.Errorf("permissions %q", got)
			}
			if got := field(lines, "level="); !strings.HasSuffix(got, " worker_token=unset") {
				t.Errorf("the engine sees the worker's token: %q", got)
			}
		}
		if _, err := os.Stat(filepath.Dir(sock)); !os.IsNotExist(err) {
			t.Errorf("%s: the host's directory survived the run: %v", engine, err)
		}
		if syscall.Kill(pid, 0) == nil {
			t.Errorf("%s: the host (pid %d) survived the run", engine, pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}

	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(bin, "claude"), "#!/bin/sh\nenv > \"$FAKE_DIR/claude.env\"\ncat >/dev/null\necho '{\"type\":\"result\",\"result\":\"ok\"}'\n")
	if err := os.Chmod(filepath.Join(bin, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "engines", "asker.sh"), `engine_run() {
  echo "engine: token=${OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN-unset} key=${ANTHROPIC_API_KEY-unset}"
  node -e '
const c = require("net").createConnection(process.env.OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET, () =>
  c.end(JSON.stringify({ model: "haiku", system_prompt: "s", message: "m", timeout_ms: 5000 })));
let r = "";
c.on("data", (d) => (r += d));
c.on("end", () => console.log("host: exit=" + JSON.parse(r).exit_code));
'
}
`)
	fakeDir := filepath.Join(tmp, "fake")
	if err := os.MkdirAll(fakeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines, stderr = run("ENGINE=asker", "OCTOPUS_TOKEN_OPTIMIZER=enforce", "OCTOPUS_TOKEN_OPTIMIZER_READ_VIA=cli", token,
		"PATH="+bin+":"+os.Getenv("PATH"), "FAKE_DIR="+fakeDir,
		"ANTHROPIC_API_KEY=run-key", "CLAUDE_CODE_OAUTH_TOKEN=run-oauth", "ANTHROPIC_BASE_URL=https://gateway.example")
	if got := field(lines, "engine:"); got != "engine: token=unset key=run-key" {
		t.Errorf("engine env: %q", got)
	}
	if got := field(lines, "host:"); got != "host: exit=0" {
		t.Fatalf("host round trip: %q\nstderr:\n%s", got, stderr)
	}
	b, err := os.ReadFile(filepath.Join(fakeDir, "claude.env"))
	if err != nil {
		t.Fatal(err)
	}
	cenv := string(b)
	if !strings.Contains(cenv, "\nCLAUDE_CODE_OAUTH_TOKEN=worker-tok\n") && !strings.HasPrefix(cenv, "CLAUDE_CODE_OAUTH_TOKEN=worker-tok\n") {
		t.Errorf("the host's claude does not log in with the worker's token:\n%s", cenv)
	}
	for _, leak := range []string{"run-key", "run-oauth", "gateway.example", "OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN"} {
		if strings.Contains(cenv, leak) {
			t.Errorf("the host's claude got %q from the run", leak)
		}
	}

	writeFile(t, filepath.Join(dir, "engines", "boom.sh"), `engine_run() {
  echo "host=$_TOKOPT_HOST_PID dir=$(dirname "$OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET")"
  return 7
}
`)
	lines, _ = run("ENGINE=boom", "OCTOPUS_TOKEN_OPTIMIZER=enforce", "OCTOPUS_TOKEN_OPTIMIZER_READ_VIA=cli", token)
	var pid int
	var hostDir string
	if _, err := fmt.Sscanf(field(lines, "host="), "host=%d dir=%s", &pid, &hostDir); err != nil {
		t.Fatalf("%v: %v", lines, err)
	}
	if _, err := os.Stat(hostDir); !os.IsNotExist(err) || syscall.Kill(pid, 0) == nil {
		t.Errorf("after a failed engine the host is still there (dir %v)", err)
	}
}
