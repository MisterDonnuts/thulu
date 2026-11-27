package hostrun

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/runner"

	// The hostcli binary links no daemon wiring; the runner registers the engines it can run.
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
)

// A CLI that hangs (an update prompt, a keychain dialog) must not stall the inventory.
const engineProbeTimeout = 10 * time.Second

// machineEngines lists the engines whose script can run on a runner, in a stable order.
func machineEngines() []string {
	var out []string
	for _, name := range engine.Known() {
		if engine.MachineLoginOf(name) != nil {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func probeEngines(ctx context.Context) []runner.EngineState {
	names := machineEngines()
	out := make([]runner.EngineState, 0, len(names))
	for _, name := range names {
		out = append(out, ProbeEngine(ctx, name))
	}
	return out
}

// ProbeEngine reports one engine's CLI on this machine as a job would find it. An engine with no
// machine login reads as not installed.
func ProbeEngine(ctx context.Context, name string) runner.EngineState {
	ml := engine.MachineLoginOf(name)
	if ml == nil {
		return runner.EngineState{Engine: name, CheckedAt: time.Now().UTC()}
	}
	return probeEngine(ctx, name, ml)
}

func probeEngine(ctx context.Context, name string, ml *engine.MachineLogin) runner.EngineState {
	st := runner.EngineState{Engine: name, CheckedAt: time.Now().UTC()}
	if _, err := exec.LookPath(ml.Binary); err != nil {
		return st
	}
	st.Installed = true
	// The job runs without the shell's login overrides; a status read with them would vouch for a
	// login the job never sees.
	env := envList(loginEnv(name))
	if out, err := runCLI(ctx, env, ml.Version); err == nil {
		st.Version, _, _ = strings.Cut(strings.TrimSpace(string(out)), "\n")
	}
	out, runErr := runCLI(ctx, env, ml.Status)
	ls, err := ml.ParseStatus(out)
	if err != nil {
		if runErr != nil {
			err = apperr.Wrap(runErr, "hostrun.errors.engine_status", "binary", ml.Binary)
		}
		st.Error = apperr.Text(err)
		return st
	}
	st.LoggedIn, st.Email, st.Org, st.Method = ls.LoggedIn, ls.Email, ls.Org, ls.Method
	return st
}

// runCLI returns stdout even when the CLI exits non-zero: a status command signed out says so on
// stdout with exit 1.
func runCLI(ctx context.Context, env, argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, engineProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: argv comes from the engine registry
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	err := runGrouped(cmd)
	return out.Bytes(), err
}

// How long Wait waits for a probe's child that keeps its output pipe once the probe is done.
const probeWaitDelay = 2 * time.Second

// runGrouped runs cmd in a process group of its own: a timeout kills the group, not the CLI alone, so
// what the CLI started (an MCP server, a helper) does not stay behind under launchd. The group is
// killed from Cancel, before Wait reaps the CLI, while its pid cannot belong to anyone else.
func runGrouped(cmd *exec.Cmd) error {
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		kill(cmd)
		return nil
	}
	cmd.WaitDelay = probeWaitDelay
	return apperr.Wrap(cmd.Run(), "hostrun.errors.run_cli", "command", filepath.Base(cmd.Path))
}

// installedEngines names the engines whose CLI this machine has on its PATH.
func installedEngines() []string {
	var out []string
	for _, name := range machineEngines() {
		if _, err := exec.LookPath(engine.MachineLoginOf(name).Binary); err == nil {
			out = append(out, name)
		}
	}
	return out
}

// checkJobEngine refuses a job whose engine this machine cannot run, before any checkout is touched.
func checkJobEngine(name string) error {
	if name == "" {
		name = engine.DefaultName
	}
	ml := engine.MachineLoginOf(name)
	if ml == nil {
		return apperr.Precondition("hostrun.errors.engine_unsupported", "engine", name)
	}
	if _, err := exec.LookPath(ml.Binary); err != nil {
		return apperr.Precondition("hostrun.errors.engine_missing", "engine", name, "binary", ml.Binary)
	}
	return nil
}

// loginEnv is the person's shell as a job of engineName sees it.
func loginEnv(engineName string) map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	withoutLoginOverrides(m, engineName)
	return m
}

func envList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// withoutLoginOverrides drops from the person's shell what would outrank the machine's login for
// the job's engine: a key exported there would bill someone else.
func withoutLoginOverrides(env map[string]string, engineName string) {
	if engineName == "" {
		engineName = engine.DefaultName
	}
	eng, err := engine.Get(engineName)
	if err != nil {
		return
	}
	for _, v := range engine.MachineLoginClears(eng.Capabilities()) {
		delete(env, v)
	}
}
