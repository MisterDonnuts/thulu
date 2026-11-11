package agentscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testSID = "3f1c2e1a-0000-4000-8000-000000000001"

func snippetsDir(t *testing.T) string {
	t.Helper()
	return filepath.Dir(projectStageScript(t))
}

func needBash4(t *testing.T) {
	t.Helper()
	out, err := exec.Command(bashOrSkip(t), "-c", "echo ${BASH_VERSINFO[0]}").Output()
	if err != nil {
		t.Skip("bash version unknown")
	}
	if v, _ := strconv.Atoi(strings.TrimSpace(string(out))); v < 4 {
		t.Skip("bash < 4")
	}
}

const fakeClaude = `#!/bin/sh
n=$(cat "$FAKE_LOG/calls" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$FAKE_LOG/calls"
printf '%s\n' "$@" >"$FAKE_LOG/args.$n"
cat >"$FAKE_LOG/stdin.$n"
case " $* " in
*" --resume "*)
  if [ "${FAKE_RESUME:-}" = fail ]; then
    echo "No conversation found with session ID: x" >&2
    echo '{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":0,"session_id":"x"}'
    exit 1
  fi ;;
esac
echo '{"type":"system","subtype":"init","session_id":"S"}'
echo '{"type":"result","subtype":"success","is_error":false,"num_turns":1}'
`

const fakeCodex = `#!/bin/sh
n=$(cat "$FAKE_LOG/calls" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$FAKE_LOG/calls"
printf '%s\n' "$@" >"$FAKE_LOG/args.$n"
cat >"$FAKE_LOG/stdin.$n"
if [ "$2" = resume ] && [ "${FAKE_RESUME:-}" = fail ]; then
  echo "Error: thread/resume: no rollout found for thread id $3" >&2
  exit 1
fi
echo '{"type":"thread.started","thread_id":"T"}'
echo '{"type":"turn.completed","usage":{}}'
`

type adapterRun struct {
	home, wd, log string
	env           []string
}

func newAdapterRun(t *testing.T, cli, script string) *adapterRun {
	t.Helper()
	tmp := t.TempDir()
	r := &adapterRun{home: filepath.Join(tmp, "home"), wd: filepath.Join(tmp, "wd"), log: filepath.Join(tmp, "log")}
	bin := filepath.Join(tmp, "bin")
	for _, d := range []string{r.home, r.wd, r.log, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, cli), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r.env = append(os.Environ(),
		"OCTOPUS_LOCALE=en",
		"HOME="+r.home,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_LOG="+r.log,
		"SESSION_ID=", "SESSION_STATE_DIR=", "CONVERSATION_FILE=", "RESUME_BASE_PROMPT=", "RESUME_MESSAGE=",
		"STEER_ENABLED=0", "CODEX_HOME=", "MODEL=", "OPENAI_API_KEY=",
	)
	return r
}

func (r *adapterRun) run(t *testing.T, adapter, fn, message string, env ...string) string {
	t.Helper()
	dir := snippetsDir(t)
	wrapper := "set -eo pipefail; source " + filepath.Join(dir, "i18n.sh") +
		"; source " + filepath.Join(dir, "engines", adapter) + "; " + fn
	cmd := exec.Command(bashOrSkip(t), "-c", wrapper)
	cmd.Dir = r.wd
	cmd.Env = append(append([]string{}, r.env...), env...)
	cmd.Stdin = strings.NewReader(message)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\nstdout:\n%s\nstderr:\n%s", fn, err, out, stderr.String())
	}
	return string(out)
}

func (r *adapterRun) calls(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.log, "calls"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func (r *adapterRun) call(t *testing.T, n int) (args []string, stdin string) {
	t.Helper()
	a, err := os.ReadFile(filepath.Join(r.log, "args."+strconv.Itoa(n)))
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.ReadFile(filepath.Join(r.log, "stdin."+strconv.Itoa(n)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(a)), "\n"), string(s)
}

func (r *adapterRun) claudeProjects(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(r.wd)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(r.home, ".claude", "projects", regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(real, "-"))
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sessionLines(out string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, `"octopus.session"`) {
			got = append(got, l)
		}
	}
	return got
}

func firstLine(out string) string {
	l, _, _ := strings.Cut(out, "\n")
	return l
}

func hasArg(args []string, pair ...string) bool {
	for i := range args {
		if i+len(pair) <= len(args) && strings.Join(args[i:i+len(pair)], "\x00") == strings.Join(pair, "\x00") {
			return true
		}
	}
	return false
}

func TestClaudeAdapter_FreshRun(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaude)
	out := r.run(t, "claude-code.sh", "engine_run", "do the task")

	if got := firstLine(out); got != `{"type":"octopus.session","mode":"fresh"}` {
		t.Errorf("first line = %q", got)
	}
	args, stdin := r.call(t, 1)
	if !hasArg(args, "--model", "sonnet") || hasArg(args, "--resume") {
		t.Errorf("args = %q", args)
	}
	if !hasArg(args, "--disallowedTools", "AskUserQuestion") {
		t.Errorf("AskUserQuestion is not disallowed: %q", args)
	}
	if stdin != "do the task" {
		t.Errorf("stdin = %q, want the message untouched", stdin)
	}
}

func TestClaudeAdapter_ResumesNativelyFromSnapshot(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaude)
	state := filepath.Join(t.TempDir(), "engine-session")
	writeFile(t, filepath.Join(state, "state", testSID+".jsonl"), `{"type":"user"}`+"\n")
	writeFile(t, filepath.Join(state, "state", testSID, "subagents", "a.jsonl"), "{}\n")

	out := r.run(t, "claude-code.sh", "engine_run", "continue",
		"SESSION_ID="+testSID, "SESSION_STATE_DIR="+state)

	if got := firstLine(out); got != `{"type":"octopus.session","mode":"native"}` {
		t.Errorf("first line = %q", got)
	}
	if !strings.Contains(out, `"subtype":"init"`) {
		t.Errorf("the engine's output did not pass through:\n%s", out)
	}
	proj := r.claudeProjects(t)
	for _, f := range []string{testSID + ".jsonl", filepath.Join(testSID, "subagents", "a.jsonl")} {
		if _, err := os.Stat(filepath.Join(proj, f)); err != nil {
			t.Errorf("not restored: %v", err)
		}
	}
	if n := r.calls(t); n != 1 {
		t.Fatalf("claude ran %d times, want 1", n)
	}
	args, stdin := r.call(t, 1)
	if !hasArg(args, "--resume", testSID) || stdin != "continue" {
		t.Errorf("args = %q stdin = %q", args, stdin)
	}
}

func TestClaudeAdapter_FailedResumeStartsOverFromTranscript(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaude)
	tmp := t.TempDir()
	state := filepath.Join(tmp, "engine-session")
	writeFile(t, filepath.Join(state, "state", testSID+".jsonl"), "{}\n")
	transcript := filepath.Join(tmp, "conversation.md")
	writeFile(t, transcript, "**assistant:** drafted the plan")
	base := filepath.Join(tmp, "prompt.md")
	writeFile(t, base, "# You are the developer")

	out := r.run(t, "claude-code.sh", "engine_run", "pick B",
		"SESSION_ID="+testSID, "SESSION_STATE_DIR="+state, "CONVERSATION_FILE="+transcript,
		"RESUME_BASE_PROMPT="+base, "FAKE_RESUME=fail")

	if got := sessionLines(out); len(got) != 1 || got[0] != `{"type":"octopus.session","mode":"rehydrated"}` {
		t.Errorf("session lines = %q, want one rehydrated", got)
	}
	if strings.Contains(out, "error_during_execution") {
		t.Errorf("the failed resume's output leaked:\n%s", out)
	}
	if n := r.calls(t); n != 2 {
		t.Fatalf("claude ran %d times, want the resume and the new session", n)
	}
	args, stdin := r.call(t, 2)
	if hasArg(args, "--resume") || !hasArg(args, "--model", "sonnet") {
		t.Errorf("fallback args = %q", args)
	}
	iBase, iTr, iMsg := strings.Index(stdin, "# You are the developer"), strings.Index(stdin, "drafted the plan"), strings.Index(stdin, "pick B")
	if iBase != 0 || iTr < iBase || iMsg < iTr {
		t.Errorf("fallback prompt is not base, transcript, message:\n%s", stdin)
	}
}

func TestClaudeAdapter_ResumeWithoutSessionStartsFromTranscript(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaude)
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "conversation.md")
	writeFile(t, transcript, "**assistant:** asked which database to use")
	base := filepath.Join(tmp, "prompt.md")
	writeFile(t, base, "# You are the developer")

	out := r.run(t, "claude-code.sh", "engine_run", "use Postgres",
		"CONVERSATION_FILE="+transcript, "RESUME_BASE_PROMPT="+base, "RESUME_MESSAGE=use Postgres")

	if got := sessionLines(out); len(got) != 1 || got[0] != `{"type":"octopus.session","mode":"rehydrated"}` {
		t.Errorf("session lines = %q, want one rehydrated", got)
	}
	if n := r.calls(t); n != 1 {
		t.Fatalf("claude ran %d times, want only the new session", n)
	}
	args, stdin := r.call(t, 1)
	if hasArg(args, "--resume") {
		t.Errorf("args = %q, want no resume", args)
	}
	iBase, iTr, iMsg := strings.Index(stdin, "# You are the developer"), strings.Index(stdin, "asked which database"), strings.Index(stdin, "use Postgres")
	if iBase != 0 || iTr < iBase || iMsg < iTr {
		t.Errorf("prompt is not base, transcript, message:\n%s", stdin)
	}

	chat := newAdapterRun(t, "claude", fakeClaude)
	out = chat.run(t, "claude-code.sh", "engine_run", "hola", "RESUME_BASE_PROMPT="+base)
	if got := firstLine(out); got != `{"type":"octopus.session","mode":"fresh"}` {
		t.Errorf("chat first line = %q", got)
	}
	if _, stdin := chat.call(t, 1); stdin != "hola" {
		t.Errorf("chat stdin = %q, want the message untouched", stdin)
	}
}

func TestClaudeAdapter_NoStateNoTranscriptIsFresh(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaude)
	out := r.run(t, "claude-code.sh", "engine_run", "hola", "SESSION_ID="+testSID)
	if got := sessionLines(out); len(got) != 1 || !strings.Contains(got[0], `"fresh"`) {
		t.Errorf("session lines = %q", got)
	}
	if n := r.calls(t); n != 1 {
		t.Errorf("claude ran %d times: nothing to resume means no resume attempt", n)
	}
}

func TestClaudeAdapter_SessionSaveCopiesOnlyTheSession(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaude)
	claude := filepath.Join(r.home, ".claude")
	proj := filepath.Join(claude, "projects", "-workspace")
	writeFile(t, filepath.Join(claude, ".credentials.json"), "secret")
	writeFile(t, filepath.Join(claude, "settings.json"), "{}")
	writeFile(t, filepath.Join(claude, "skills", "x", "SKILL.md"), "x")
	writeFile(t, filepath.Join(proj, "memory", "MEMORY.md"), "m")
	writeFile(t, filepath.Join(proj, "11111111-0000-4000-8000-000000000000.jsonl"), "old")
	writeFile(t, filepath.Join(proj, testSID+".jsonl"), "new")
	writeFile(t, filepath.Join(proj, testSID, "subagents", "a.jsonl"), "sub")
	old := filepath.Join(proj, "11111111-0000-4000-8000-000000000000.jsonl")
	if err := os.Chtimes(old, timeAgo(), timeAgo()); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "out")
	r.run(t, "claude-code.sh", "engine_session_save "+dir, "")

	if id, _ := os.ReadFile(filepath.Join(dir, "id")); strings.TrimSpace(string(id)) != testSID {
		t.Errorf("id = %q, want the newest session", id)
	}
	if got, want := listFiles(t, filepath.Join(dir, "state")), []string{
		testSID + ".jsonl", testSID + "/subagents/a.jsonl",
	}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("state = %q, want %q", got, want)
	}

	empty := newAdapterRun(t, "claude", fakeClaude)
	dir2 := filepath.Join(t.TempDir(), "out")
	empty.run(t, "claude-code.sh", "mkdir -p "+dir2+"; engine_session_save "+dir2, "")
	if _, err := os.Stat(filepath.Join(dir2, "id")); err == nil {
		t.Error("no session, but an id was written")
	}
}

func TestClaudeAdapter_SkillsDir(t *testing.T) {
	r := newAdapterRun(t, "claude", fakeClaude)
	out := r.run(t, "claude-code.sh", "engine_skills_dir", "")
	if strings.TrimSpace(out) != filepath.Join(r.home, ".claude", "skills") {
		t.Errorf("engine_skills_dir = %q", out)
	}
}

func TestEngineSessionGuard_LooksPastSteerEcho(t *testing.T) {
	dir := snippetsDir(t)
	echo := `{"type":"user","octopus_injected":true,"message":{"content":"hola"}}`
	guard := func(input string) (string, bool) {
		marker := filepath.Join(t.TempDir(), "failed")
		cmd := exec.Command(bashOrSkip(t), "-c", "source "+filepath.Join(dir, "i18n.sh")+
			"; source "+filepath.Join(dir, "engines", "claude-code.sh")+
			"; engine_session_guard claude_resume_failed \"$MARKER\"")
		cmd.Env = append(os.Environ(), "MARKER="+marker)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(marker)
		return string(out), statErr == nil
	}

	out, failed := guard(echo + "\n" + `{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":0,"session_id":"x"}` + "\n")
	if !failed || out != "" {
		t.Errorf("failed resume: failed=%v out=%q", failed, out)
	}
	init := `{"type":"system","subtype":"init"}`
	out, failed = guard(echo + "\n" + init + "\nrest\n")
	want := `{"type":"octopus.session","mode":"native"}` + "\n" + echo + "\n" + init + "\nrest\n"
	if failed || out != want {
		t.Errorf("open resume: failed=%v out=%q, want %q", failed, out, want)
	}
	if _, failed = guard(""); !failed {
		t.Error("no output at all must count as a failed resume")
	}
}

// codex-cli 0.142 does not read OPENAI_BASE_URL, so without the config override a gateway or a custom
// profile is skipped and the run goes to OpenAI's own host.
func TestCodexAdapter_BaseURLReachesTheConfig(t *testing.T) {
	needBash4(t)
	r := newAdapterRun(t, "codex", fakeCodex)
	r.run(t, "codex.sh", "engine_run", "go", "OPENAI_BASE_URL=https://gw.example/llm-gateway/openai")
	args, _ := r.call(t, 1)
	if !hasArg(args, "-c", `openai_base_url="https://gw.example/llm-gateway/openai"`) {
		t.Errorf("args = %q, want the base URL as a config override", args)
	}

	plain := newAdapterRun(t, "codex", fakeCodex)
	plain.run(t, "codex.sh", "engine_run", "go", "OPENAI_BASE_URL=")
	args, _ = plain.call(t, 1)
	if strings.Contains(strings.Join(args, " "), "openai_base_url") {
		t.Errorf("args = %q, want no override without a base URL", args)
	}
}

func TestCodexAdapter_SessionRoundTrip(t *testing.T) {
	needBash4(t)
	r := newAdapterRun(t, "codex", fakeCodex)
	sessions := filepath.Join(r.home, ".codex", "sessions")
	writeFile(t, filepath.Join(r.home, ".codex", "auth.json"), "secret")
	writeFile(t, filepath.Join(r.home, ".codex", "config.toml"), "")
	rollout := filepath.Join("2026", "10", "01", "rollout-2026-10-01T10-00-00-"+testSID+".jsonl")
	writeFile(t, filepath.Join(sessions, rollout), "{}\n")

	dir := filepath.Join(t.TempDir(), "out")
	r.run(t, "codex.sh", "engine_session_save "+dir, "")
	if id, _ := os.ReadFile(filepath.Join(dir, "id")); strings.TrimSpace(string(id)) != testSID {
		t.Errorf("id = %q, want the rollout's thread id", id)
	}
	if got := listFiles(t, filepath.Join(dir, "state")); len(got) != 1 || got[0] != filepath.ToSlash(filepath.Join("sessions", rollout)) {
		t.Errorf("state = %q", got)
	}

	next := newAdapterRun(t, "codex", fakeCodex)
	out := next.run(t, "codex.sh", "engine_run", "continue", "SESSION_ID="+testSID, "SESSION_STATE_DIR="+dir)
	if got := firstLine(out); got != `{"type":"octopus.session","mode":"native","model":"gpt-5.4-mini"}` {
		t.Errorf("first line = %q", got)
	}
	if _, err := os.Stat(filepath.Join(next.home, ".codex", "sessions", rollout)); err != nil {
		t.Errorf("rollout not restored: %v", err)
	}
	args, _ := next.call(t, 1)
	if !hasArg(args, "exec", "resume", testSID) {
		t.Errorf("args = %q", args)
	}
}

func TestCodexAdapter_FailedResumeStartsOver(t *testing.T) {
	needBash4(t)
	r := newAdapterRun(t, "codex", fakeCodex)
	tmp := t.TempDir()
	state := filepath.Join(tmp, "engine-session")
	writeFile(t, filepath.Join(state, "state", "sessions", "2026", "10", "01", "rollout-x-"+testSID+".jsonl"), "{}\n")
	transcript := filepath.Join(tmp, "conversation.md")
	writeFile(t, transcript, "**assistant:** listo")

	out := r.run(t, "codex.sh", "engine_run", "dale", "SESSION_ID="+testSID, "SESSION_STATE_DIR="+state,
		"CONVERSATION_FILE="+transcript, "FAKE_RESUME=fail")
	if got := sessionLines(out); len(got) != 1 || !strings.Contains(got[0], `"rehydrated"`) {
		t.Errorf("session lines = %q", got)
	}
	args, stdin := r.call(t, 2)
	if hasArg(args, "resume") || !hasArg(args, "--model", "gpt-5.4-mini") || !strings.Contains(stdin, "listo") {
		t.Errorf("fallback args = %q stdin = %q", args, stdin)
	}
}

func timeAgo() time.Time { return time.Now().Add(-time.Hour) }

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var got []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		got = append(got, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	return got
}
