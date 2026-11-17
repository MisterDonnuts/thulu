package hostrun

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// fakeCLI puts a `claude` on a PATH of its own that prints a version and the given status JSON,
// exiting with code.
func fakeCLI(t *testing.T, status string, code int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script CLI")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '2.1.7 (Claude Code)'; exit 0; fi\n" +
		"echo 'update available'\n" +
		"echo '" + status + "'\n" +
		"exit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func claudeState(t *testing.T) runner.EngineState {
	t.Helper()
	for _, st := range probeEngines(context.Background()) {
		if st.Engine == engine.DefaultName {
			return st
		}
	}
	t.Fatal("claude-code was not probed")
	return runner.EngineState{}
}

// Case 26: the runner reports which engines are installed and logged in.
func TestProbeReportsInstalledAndSignedInEngines(t *testing.T) {
	cases := []struct {
		name   string
		status string
		code   int
		want   runner.EngineState
	}{
		{
			name:   "signed in",
			status: `{"loggedIn":true,"authMethod":"claude.ai","email":"ada@example.com","orgName":"Ada"}`,
			want: runner.EngineState{Engine: engine.DefaultName, Installed: true, Version: "2.1.7 (Claude Code)",
				LoggedIn: true, Email: "ada@example.com", Org: "Ada", Method: "claude.ai"},
		},
		{
			name:   "signed out exits 1",
			status: `{"loggedIn":false,"authMethod":"none"}`,
			code:   1,
			want:   runner.EngineState{Engine: engine.DefaultName, Installed: true, Version: "2.1.7 (Claude Code)", Method: "none"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCLI(t, tc.status, tc.code)
			got := claudeState(t)
			if got.CheckedAt.IsZero() {
				t.Error("the state carries no check time")
			}
			got.CheckedAt = tc.want.CheckedAt
			if got != tc.want {
				t.Errorf("state = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestProbeReportsAMissingCLIAndAnUnreadableStatus(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if st := claudeState(t); st.Installed || st.LoggedIn {
		t.Errorf("no CLI on the PATH = %+v", st)
	}
	fakeCLI(t, "not json", 2)
	st := claudeState(t)
	if !st.Installed || st.LoggedIn || st.Error == "" {
		t.Errorf("unreadable status = %+v, want installed with an error", st)
	}
}

// Case 59 at the probe: a key exported in the shell does not make the machine look signed in, since
// the job runs without it.
func TestProbeIgnoresTheShellsCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script CLI")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '2.1.7'; exit 0; fi\n" +
		"if [ -n \"$ANTHROPIC_API_KEY$CLAUDE_CODE_OAUTH_TOKEN\" ]; then echo '{\"loggedIn\":true,\"authMethod\":\"api_key\"}'; exit 0; fi\n" +
		"echo '{\"loggedIn\":false,\"authMethod\":\"none\"}'; exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api-shell")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat-shell")
	if st := claudeState(t); !st.Installed || st.LoggedIn {
		t.Errorf("state = %+v, want installed and signed out", st)
	}
}

// Case 30: a job for an engine the machine does not have fails naming it.
func TestJobForAMissingEngineFailsNamingIt(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := checkJobEngine(engine.DefaultName)
	if apperr.KindOf(err) != apperr.KindPrecondition || !strings.Contains(apperr.Text(err), engine.DefaultName) {
		t.Errorf("missing engine err = %v", err)
	}
	if err := checkJobEngine("no-such-engine"); err == nil || !strings.Contains(apperr.Text(err), "no-such-engine") {
		t.Errorf("unknown engine err = %v", err)
	}
	fakeCLI(t, `{"loggedIn":true}`, 0)
	if err := checkJobEngine(""); err != nil {
		t.Errorf("installed default engine err = %v", err)
	}
}

// stubTools returns a directory holding executables named after the runner's required tools.
func stubTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"bash", "git", "curl", "node"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPreflightNeedsAnEngine(t *testing.T) {
	tools := stubTools(t)
	fakeCLI(t, `{"loggedIn":true}`, 0)
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+tools)
	if err := Preflight(&Config{}); err != nil {
		t.Errorf("preflight with tools and claude = %v", err)
	}

	t.Setenv("PATH", tools)
	err := Preflight(&Config{})
	if apperr.KindOf(err) != apperr.KindPrecondition || !strings.Contains(apperr.Text(err), "claude") {
		t.Errorf("preflight without an engine = %v, want a precondition naming claude", err)
	}

	t.Setenv("PATH", t.TempDir())
	if err := Preflight(&Config{}); err == nil || !strings.Contains(apperr.Text(err), "git") {
		t.Errorf("preflight without tools = %v, want the missing tools", err)
	}
}

func jobEnv(t *testing.T, spec map[string]string) map[string]string {
	t.Helper()
	j := &job{r: &Runner{cfg: &Config{DaemonURL: "http://daemon"}}, spec: &runner.JobSpec{Env: spec}, root: t.TempDir()}
	out := map[string]string{}
	for _, kv := range j.env() {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// Case 31: OCTOPUS_LLM_SOURCE comes from the job; a job that names none runs on the machine's login.
func TestJobEnvTakesTheLLMSourceFromTheJob(t *testing.T) {
	t.Setenv(engine.LLMSourceEnv, "host")
	if got := jobEnv(t, map[string]string{engine.LLMSourceEnv: "binding"})[engine.LLMSourceEnv]; got != "binding" {
		t.Errorf("source = %q, want the job's", got)
	}
	if got := jobEnv(t, map[string]string{})[engine.LLMSourceEnv]; got != runner.SourceRunnerLogin {
		t.Errorf("source = %q, want %q when the job names none", got, runner.SourceRunnerLogin)
	}
}

// Case 59: a key exported in the person's shell never reaches the engine.
func TestJobEnvDropsTheShellsCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api-shell")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat-shell")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("KEEP_ME", "yes")
	env := jobEnv(t, map[string]string{"ENGINE": engine.DefaultName})
	for _, v := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK"} {
		if _, ok := env[v]; ok {
			t.Errorf("%s from the shell reached the engine", v)
		}
	}
	if env["KEEP_ME"] != "yes" {
		t.Error("an unrelated shell variable was dropped")
	}
}
