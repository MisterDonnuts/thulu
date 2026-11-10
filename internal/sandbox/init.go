package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
)

type InitScripts interface {
	TaskProjects(ctx context.Context, taskID string) ([]InitProject, error)
	SaveInitScript(ctx context.Context, taskID, projectID string, in project.InitScriptInput) (*project.InitScript, error)
}

type InitProject struct {
	ID      string
	Name    string
	Dir     string
	Mounted bool
	Script  *project.InitScript
}

const (
	InitStateNever   = "never"
	InitStateRunning = "running"
	InitStateReady   = "ready"
	InitStateFailed  = "failed"
	InitStateTimeout = "timeout"
)

var (
	initRoot         = "/tmp/octopus-init"
	initProjectsRoot = "/workspace/projects"
)

const (
	initPollEveryDefault = 2 * time.Second
	initProbeTimeoutMs   = 30_000
	initStepTimeoutMs    = 60_000
	// Its base64 must stay far below the per-argument limit of a docker exec.
	initWriteChunk   = 32 << 10
	initLogTailLines = 60
)

var initPollEvery = initPollEveryDefault

type InitRun struct {
	State      string           `json:"state"`
	Revision   int              `json:"revision"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
	LogPath    string           `json:"log_path"`
	ExitCode   *int             `json:"exit_code,omitempty"`
	Message    string           `json:"message,omitempty"`
	Phase      string           `json:"phase,omitempty"`
	Setup      *InitSetupReport `json:"setup,omitempty"`

	container string
	deadline  time.Time
	timeout   time.Duration
	startSeen bool
	hasReady  bool
	workdir   string
	gen       uint64
	// Claimed under the registry's lock so the install log gets one row and the cache one
	// measurement per run (setup_log.go).
	setupLogged bool
	measured    bool
}

func (r *InitRun) observe(p initProbe, now time.Time) {
	if p.phase != "" {
		r.Phase = p.phase
	}
	if p.setup != nil {
		r.Setup = p.setup
	}
	if p.phase == initPhaseStart && !r.startSeen {
		r.startSeen = true
		if r.timeout > 0 {
			r.deadline = now.Add(r.timeout)
		}
	}
}

func (r *InitRun) finish(state, msg string) {
	now := time.Now().UTC()
	r.State = state
	r.FinishedAt = &now
	if msg != "" {
		r.Message = msg
	}
}

type initRegistry struct {
	mu   sync.Mutex
	gen  uint64
	runs map[string]map[string]*InitRun
}

func (r *initRegistry) get(owner, dir string) (InitRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[owner][dir]
	if !ok {
		return InitRun{}, false
	}
	return *run, true
}

// Later updates must present the returned generation, so a run replaced by a re-run can no longer
// write.
func (r *initRegistry) start(owner, dir string, run InitRun) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen++
	run.gen = r.gen
	if r.runs == nil {
		r.runs = map[string]map[string]*InitRun{}
	}
	if r.runs[owner] == nil {
		r.runs[owner] = map[string]*InitRun{}
	}
	r.runs[owner][dir] = &run
	return run.gen
}

func (r *initRegistry) update(owner, dir string, gen uint64, fn func(*InitRun)) (InitRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[owner][dir]
	if !ok || run.gen != gen {
		return InitRun{}, false
	}
	fn(run)
	return *run, true
}

func (r *initRegistry) adopt(owner, dir string, run InitRun) InitRun {
	r.mu.Lock()
	if cur, ok := r.runs[owner][dir]; ok && cur.container == run.container {
		out := *cur
		r.mu.Unlock()
		return out
	}
	r.mu.Unlock()
	run.gen = r.start(owner, dir, run)
	return run
}

func (r *initRegistry) drop(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, owner)
}

func (m *Manager) currentContainer(ctx context.Context, owner string) string {
	sb, err := m.store.Get(ctx, owner)
	if err != nil || sb.Status != StatusReady {
		return ""
	}
	return sb.ContainerID
}

func (m *Manager) lastInitRun(ctx context.Context, owner, dir string) (InitRun, bool) {
	if m == nil {
		return InitRun{}, false
	}
	run, ok := m.inits.get(owner, dir)
	if !ok || run.container != m.currentContainer(ctx, owner) {
		return InitRun{}, false
	}
	return run, true
}

// Uses m.docker.Exec, never Manager.Exec: a status question must not acquire or start a sandbox.
func (m *Manager) recoverInitRun(ctx context.Context, owner, dir string) (InitRun, bool) {
	if m == nil {
		return InitRun{}, false
	}
	mu := m.initMu.Get(owner + "\x00" + dir)
	mu.Lock()
	defer mu.Unlock()
	if run, ok := m.lastInitRun(ctx, owner, dir); ok {
		return run, true
	}
	container := m.currentContainer(ctx, owner)
	if container == "" {
		return InitRun{}, false
	}
	res, err := m.docker.Exec(ctx, container, ExecOpts{Command: initRecoverCmd(dir), TimeoutMs: initProbeTimeoutMs})
	if err != nil || res.TimedOut || res.ExitCode != 0 {
		return InitRun{}, false
	}
	run, ok := parseInitRecover(res.Stdout, time.Now())
	if !ok {
		return InitRun{}, false
	}
	if run.workdir == "" {
		run.workdir = "/"
	}
	run.LogPath = initLogPath(dir)
	run.Message = i18n.Text("sandbox.tools.init_recovered")
	run.container = container
	return m.inits.adopt(owner, dir, run), true
}

func initDir(dir string) string     { return initRoot + "/" + dir }
func initLogPath(dir string) string { return initRoot + "/" + dir + ".log" }

func initWorkdir(dir, workdir string) string {
	return path.Join(initProjectsRoot, dir, workdir)
}

// The sandbox's PID 1 (sleep, or the exec agent) reaps nothing, so a finished run stays a zombie
// and `kill -0` alone would call it alive forever.
//
//i18n:ignore shell function run in the sandbox
const initAliveFn = `alive() { s=$(sed -E 's/^.*\) (.).*$/\1/' "/proc/$1/stat" 2>/dev/null); [ -n "$s" ] && [ "$s" != Z ]; }`

const (
	initExitWorkdirMissing = 3
	initExitNoSetsid       = 4
)

func initStopCmd(dir string) string {
	d := shellSingleQuote(initDir(dir))
	//i18n:ignore shell command run in the sandbox
	return initAliveFn + "\n" + fmt.Sprintf(`d=%s
mkdir -p "$d"
if [ -s "$d/pid" ]; then
  old=$(cat "$d/pid")
  kill -TERM -- "-$old" 2>/dev/null
  i=0; while [ $i -lt 30 ] && alive "$old"; do sleep 0.1; i=$((i+1)); done
  kill -KILL -- "-$old" 2>/dev/null
fi
rm -f "$d/pid" "$d/exit" "$d/meta" "$d/phase" "$d/setup.result" "$d/start_at"
true`, d)
}

const (
	initPhasePrepare = "prepare"
	initPhaseStart   = "start"
)

// Writes its own pid first: setsid makes it the process group leader, so that pid is what a later
// stop signals.
func initWrapScript(dir string) string {
	d := shellSingleQuote(initDir(dir))
	//i18n:ignore shell script run in the sandbox
	return fmt.Sprintf(`echo %[2]s > %[1]s/phase
echo $$ > %[1]s/pid
. %[1]s/env
. %[1]s/prepare.sh
octopus_prepare
code=$?
if [ "$code" -ne 0 ]; then
  echo "$code" > %[1]s/exit
  exit "$code"
fi
date +%%s > %[1]s/start_at
"${OCTOPUS_MX[@]}" bash %[1]s/run.sh
code=$?
echo "$code" > %[1]s/exit
exit "$code"
`, d, initPhasePrepare)
}

func initLaunchCmd(dir, workdir string) string {
	d := shellSingleQuote(initDir(dir))
	//i18n:ignore shell command run in the sandbox
	return fmt.Sprintf(`d=%s
command -v setsid >/dev/null 2>&1 || exit %d
cd %s 2>/dev/null || exit %d
setsid nohup bash "$d/wrap.sh" > %s 2>&1 < /dev/null &
i=0; while [ $i -lt 50 ] && [ ! -s "$d/pid" ]; do sleep 0.1; i=$((i+1)); done
cat "$d/pid" 2>/dev/null`, d, initExitNoSetsid, shellSingleQuote(workdir), initExitWorkdirMissing,
		shellSingleQuote(initLogPath(dir)))
}

func initProbeCmd(dir, workdir string) string {
	//i18n:ignore shell command run in the sandbox
	return initAliveFn + "\n" + fmt.Sprintf("d=%s\nwd=%s\n", shellSingleQuote(initDir(dir)), shellSingleQuote(workdir)) +
		initProbeBody
}

// The ready command runs only once the start phase began; during the install it would judge the
// previous run's leftovers.
// A run without a phase file was started before phases existed and is probed as before.
//
//i18n:ignore shell script run in the sandbox
const initProbeBody = `pid=$(cat "$d/pid" 2>/dev/null)
a=0; if [ -n "$pid" ] && alive "$pid"; then a=1; fi
echo "alive=$a"
echo "exit=$(cat "$d/exit" 2>/dev/null)"
ph=$(cat "$d/phase" 2>/dev/null)
echo "phase=$ph"
echo "start_at=$(cat "$d/start_at" 2>/dev/null)"
cat "$d/setup.result" 2>/dev/null
r=-
if [ -f "$d/ready.sh" ] && { [ -z "$ph" ] || [ "$ph" = start ]; }; then
  if (cd "$wd" && . "$d/env" && bash "$d/ready.sh") >/dev/null 2>&1 < /dev/null; then r=0; else r=1; fi
fi
echo "ready=$r"`

// Read back by initRecoverCmd/parseInitMeta after a daemon restart; keep the keys in sync.
func initMetaFile(run InitRun) string {
	hasReady := 0
	if run.hasReady {
		hasReady = 1
	}
	//i18n:ignore file read back by initRecoverCmd
	return fmt.Sprintf("revision=%d\nstarted=%d\ndeadline=%d\ntimeout=%d\nhas_ready=%d\nworkdir=%s\n",
		run.Revision, run.StartedAt.Unix(), run.deadline.Unix(), int64(run.timeout/time.Second), hasReady, run.workdir)
}

func initRecoverCmd(dir string) string {
	//i18n:ignore shell command run in the sandbox
	return initAliveFn + "\n" + fmt.Sprintf("d=%s\n", shellSingleQuote(initDir(dir))) + initRecoverHead + initProbeBody
}

//i18n:ignore shell script run in the sandbox
const initRecoverHead = `if [ ! -s "$d/pid" ] && [ ! -f "$d/exit" ]; then echo found=0; exit 0; fi
echo found=1
cat "$d/meta" 2>/dev/null
wd=$(sed -n 's/^workdir=//p' "$d/meta" 2>/dev/null)
[ -n "$wd" ] || wd=/
`

func initEnvFile(vars map[string]string) string {
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString("export " + k + "=" + shellSingleQuote(vars[k]) + "\n")
	}
	return b.String()
}

const PreviewEnvPrefix = "OCTOPUS_PREVIEW_URL_"

func (m *Manager) previewEnv(ctx context.Context, owner string) map[string]string {
	sb, err := m.store.Get(ctx, owner)
	if err != nil {
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.init_preview_env_failed",
			"port", "*", "task", owner, "env", PreviewEnvPrefix+"*", "err", err))
		return nil
	}
	out := make(map[string]string, len(sb.HostPorts))
	for _, port := range slices.Sorted(maps.Keys(sb.HostPorts)) {
		name := PreviewEnvPrefix + strconv.Itoa(port)
		info, perr := m.PreviewURL(ctx, owner, port)
		if perr != nil {
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.init_preview_env_failed",
				"port", port, "task", owner, "env", name, "err", perr))
			continue
		}
		out[name] = info.URL
	}
	return out
}

type initProbe struct {
	alive    bool
	exited   bool
	exitCode int
	ready    bool
	phase    string
	startAt  int64
	setup    *InitSetupReport
	answered bool
}

func parseInitProbe(out string) initProbe {
	p := initProbe{}
	rep := InitSetupReport{}
	seen := false
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "alive":
			p.alive = v == "1"
			p.answered = true
		case "exit":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				p.exited, p.exitCode = true, n
			}
		case "ready":
			p.ready = v == "0"
		case "phase":
			p.phase = strings.TrimSpace(v)
		case "start_at":
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				p.startAt = n
			}
		default:
			if rep.parseSetupLine(k, strings.TrimSpace(v)) {
				seen = true
			}
		}
	}
	if rep.Status == "" && p.phase != "" && p.phase != initPhaseStart {
		rep.Status = SetupStateRunning
		seen = true
	}
	if seen && (rep.Status != SetupStateNone || rep.Tools != "") {
		p.setup = &rep
	}
	return p
}

func parseInitRecover(out string, now time.Time) (InitRun, bool) {
	run, found, deadline, timeout := parseInitMeta(out)
	if !found {
		return InitRun{}, false
	}
	p := parseInitProbe(out)
	if !p.answered {
		return InitRun{}, false
	}
	run.deadline = time.Unix(deadline, 0)
	if deadline == 0 {
		run.deadline = now.Add(project.InitMaxTimeoutSeconds * time.Second)
	}
	run.timeout = time.Duration(timeout) * time.Second
	run.Phase, run.Setup = p.phase, p.setup
	if p.phase == initPhaseStart {
		run.startSeen = true
		if p.startAt > 0 && timeout > 0 {
			run.deadline = time.Unix(p.startAt+timeout, 0)
		}
	}
	run.State = InitStateRunning
	state, code := p.judge(run.hasReady)
	if code != nil {
		c := *code
		run.ExitCode = &c
	}
	switch {
	case state == InitStateRunning && !now.Before(run.deadline):
		run.finish(InitStateTimeout, "")
	case state != InitStateRunning:
		run.finish(state, "")
	}
	return run, true
}

func parseInitMeta(out string) (run InitRun, found bool, deadline, timeout int64) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		n, nErr := strconv.ParseInt(v, 10, 64)
		switch {
		case k == "found":
			found = v == "1"
		case k == "revision" && nErr == nil:
			run.Revision = int(n)
		case k == "started" && nErr == nil:
			run.StartedAt = time.Unix(n, 0).UTC()
		case k == "deadline" && nErr == nil:
			deadline = n
		case k == "timeout" && nErr == nil:
			timeout = n
		case k == "has_ready":
			run.hasReady = v == "1"
		case k == "workdir":
			run.workdir = v
		}
	}
	return run, found, deadline, timeout
}

func (p initProbe) judge(hasReady bool) (state string, exitCode *int) {
	if !p.answered {
		return InitStateRunning, nil
	}
	if hasReady && p.ready {
		if p.exited {
			return InitStateReady, &p.exitCode
		}
		return InitStateReady, nil
	}
	if p.exited {
		code := p.exitCode
		switch {
		case code != 0:
			return InitStateFailed, &code
		case !hasReady:
			return InitStateReady, &code
		}
		// Exited 0 but not ready: the script may have started the server in the background and
		// returned.
		return InitStateRunning, &code
	}
	if !p.alive {
		return InitStateFailed, nil
	}
	return InitStateRunning, nil
}

func resolveInitParams(m project.InitManifest, given map[string]json.RawMessage) (map[string]string, error) {
	declared := map[string]project.InitParam{}
	for _, p := range m.Params {
		declared[p.Name] = p
	}
	var unknown []string
	values := map[string]string{}
	for name, raw := range given {
		if _, ok := declared[name]; !ok {
			unknown = append(unknown, name)
			continue
		}
		v, set, err := initParamValue(raw)
		if err != nil {
			return nil, apperr.Invalid("sandbox.errors.init_param_value", "name", name)
		}
		if set {
			values[name] = v
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		known := make([]string, 0, len(m.Params))
		for _, p := range m.Params {
			known = append(known, p.Name)
		}
		return nil, apperr.Invalid("sandbox.errors.init_param_unknown",
			"names", strings.Join(unknown, ", "), "known", strings.Join(known, ", "))
	}
	var missing []string
	for _, p := range m.Params {
		if v, ok := values[p.Name]; ok && v != "" {
			continue
		}
		if p.Default != "" {
			values[p.Name] = p.Default
			continue
		}
		if p.Required {
			missing = append(missing, p.Name)
		}
	}
	if len(missing) > 0 {
		return nil, apperr.Invalid("sandbox.errors.init_param_missing", "names", strings.Join(missing, ", "))
	}
	return values, nil
}

func initParamValue(raw json.RawMessage) (string, bool, error) {
	s := strings.TrimSpace(string(raw))
	switch {
	case s == "" || s == "null":
		return "", false, nil
	case strings.HasPrefix(s, `"`):
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", false, err
		}
		if strings.ContainsRune(v, 0) {
			return "", false, apperr.Invalid("sandbox.errors.init_param_nul")
		}
		return v, true, nil
	case s == "true" || s == "false":
		return s, true, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return "", false, err
	}
	return n.String(), true, nil
}

type initTarget struct {
	owner, stage string
	opts         AcquireDockerOpts
	taskID       string
	proj         InitProject
	params       map[string]string
}

type InitRunResult struct {
	Project   string           `json:"project"`
	State     string           `json:"state"`
	Revision  int              `json:"revision"`
	ElapsedMs int64            `json:"elapsed_ms"`
	Ports     []int            `json:"ports"`
	LogPath   string           `json:"log_path"`
	LogTail   string           `json:"log_tail"`
	ExitCode  *int             `json:"exit_code,omitempty"`
	Message   string           `json:"message,omitempty"`
	Phase     string           `json:"phase,omitempty"`
	Setup     *InitSetupReport `json:"setup,omitempty"`
	Queued    bool             `json:"queued,omitempty"`
}

func setupFailedMessage(r *InitSetupReport) string {
	if r.Tools == "failed" {
		return i18n.Text("sandbox.tools.init_tools_failed")
	}
	code := "?"
	if r.ExitCode != nil {
		code = strconv.Itoa(*r.ExitCode)
	}
	return i18n.Text("sandbox.tools.init_setup_failed", "code", code)
}

var errInitQueued = errSandboxQueued

func (m *Manager) initExec(ctx context.Context, t *initTarget, cmd string, timeoutMs int) (*ExecResult, string, error) {
	res, ar, err := m.Exec(ctx, t.owner, t.stage, t.opts, ExecOpts{Command: cmd, TimeoutMs: timeoutMs})
	if err != nil {
		return nil, "", err
	}
	if ar != nil && ar.Outcome == AcquireQueued {
		return nil, "", errInitQueued
	}
	container := ""
	if ar != nil {
		container = ar.ContainerID
	}
	return res, container, nil
}

func (m *Manager) writeInitFile(ctx context.Context, t *initTarget, p, data string) error {
	q := shellSingleQuote(p)
	tmp := shellSingleQuote(p + ".b64")
	b := []byte(data)
	//i18n:ignore shell command run in the sandbox
	cmds := []string{fmt.Sprintf(`: > %s`, tmp)}
	for i := 0; i < len(b); i += initWriteChunk {
		end := min(i+initWriteChunk, len(b))
		//i18n:ignore shell command run in the sandbox
		cmds = append(cmds, fmt.Sprintf(`printf '%%s' '%s' >> %s`, base64.StdEncoding.EncodeToString(b[i:end]), tmp))
	}
	//i18n:ignore shell command run in the sandbox
	cmds = append(cmds, fmt.Sprintf(`base64 -d %s > %s && rm -f %s`, tmp, q, tmp))
	if len(b) <= initWriteChunk {
		cmds = []string{strings.Join(cmds, " && ")}
	}
	for _, c := range cmds {
		res, _, err := m.initExec(ctx, t, c, initStepTimeoutMs)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return apperr.Internal("sandbox.errors.init_write_failed", "path", p, "stderr", strings.TrimSpace(res.Stderr))
		}
	}
	return nil
}

// Holds the per-project lock so two init_run calls cannot interleave their stop and start.
func (m *Manager) launchInit(ctx context.Context, t *initTarget) (InitRun, error) {
	mu := m.initMu.Get(t.owner + "\x00" + t.proj.Dir)
	mu.Lock()
	defer mu.Unlock()

	sc := t.proj.Script
	dir := t.proj.Dir
	workdir := initWorkdir(dir, sc.Manifest.Workdir)

	res, _, err := m.initExec(ctx, t, initStopCmd(dir), initStepTimeoutMs)
	if err != nil {
		return InitRun{}, err
	}
	if res.ExitCode != 0 {
		return InitRun{}, apperr.Internal("sandbox.errors.init_stop_failed", "stderr", strings.TrimSpace(res.Stderr))
	}

	vars := map[string]string{
		"OCTOPUS_PROJECT": t.proj.Name, "OCTOPUS_TASK_ID": t.taskID,
		InitFilesEnv: initFilesPath(initDir(dir)),
	}
	maps.Copy(vars, m.previewEnv(ctx, t.owner))
	for k, v := range t.params {
		vars[k] = v
	}
	files := []struct{ name, data string }{
		{"run.sh", sc.Script},
		{"env", initEnvFile(vars)},
		{"wrap.sh", initWrapScript(dir)},
		{"prepare.sh", setupPrepareScript(setupSpec{
			dir:       initDir(dir),
			repo:      path.Join(initProjectsRoot, dir),
			projectID: t.proj.ID,
			manifest:  sc.Manifest,
		})},
		// Exactly the manifest's text: the setup key hashes this file.
		{"setup.sh", sc.Manifest.Setup},
	}
	if sc.Manifest.Ready.Command != "" {
		files = append(files, struct{ name, data string }{"ready.sh", sc.Manifest.Ready.Command + "\n"})
	} else {
		//i18n:ignore shell command run in the sandbox
		if _, _, rmErr := m.initExec(ctx, t, "rm -f "+shellSingleQuote(initDir(dir)+"/ready.sh"), initStepTimeoutMs); rmErr != nil {
			return InitRun{}, rmErr
		}
	}
	for _, f := range files {
		if werr := m.writeInitFile(ctx, t, initDir(dir)+"/"+f.name, f.data); werr != nil {
			return InitRun{}, werr
		}
	}
	if werr := m.writeInitSupportFiles(ctx, t, initDir(dir), sc.Manifest.Files); werr != nil {
		return InitRun{}, werr
	}

	res, container, err := m.initExec(ctx, t, initLaunchCmd(dir, workdir), initStepTimeoutMs)
	if err != nil {
		return InitRun{}, err
	}
	switch {
	case res.ExitCode == initExitWorkdirMissing:
		return InitRun{}, apperr.Invalid("sandbox.errors.init_workdir_missing", "workdir", workdir)
	case res.ExitCode == initExitNoSetsid:
		return InitRun{}, apperr.Unavailable("sandbox.errors.init_no_setsid")
	case res.ExitCode != 0 || strings.TrimSpace(res.Stdout) == "":
		return InitRun{}, apperr.Internal("sandbox.errors.init_start_failed", "stderr", strings.TrimSpace(res.Stderr))
	}

	timeout := time.Duration(sc.Manifest.Ready.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = project.InitDefaultTimeoutSeconds * time.Second
	}
	if maxWait := project.InitMaxTimeoutSeconds * time.Second; timeout > maxWait {
		timeout = maxWait
	}
	now := time.Now().UTC()
	run := InitRun{
		State:     InitStateRunning,
		Revision:  sc.Revision,
		StartedAt: now,
		LogPath:   initLogPath(dir),
		Phase:     initPhasePrepare,
		container: container,
		deadline:  now.Add(initSetupMaxSeconds*time.Second + timeout),
		timeout:   timeout,
		hasReady:  sc.Manifest.Ready.Command != "",
		workdir:   workdir,
	}
	if werr := m.writeInitFile(ctx, t, initDir(dir)+"/meta", initMetaFile(run)); werr != nil {
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.init_meta_failed", "task", t.owner, "dir", dir, "err", werr))
	}
	run.gen = m.inits.start(t.owner, dir, run)
	return run, nil
}

func (m *Manager) probeInit(ctx context.Context, t *initTarget, run InitRun) (InitRun, bool, error) {
	res, _, err := m.initExec(ctx, t, initProbeCmd(t.proj.Dir, run.workdir), initProbeTimeoutMs)
	if err != nil {
		return run, true, err
	}
	p := initProbe{}
	if !res.TimedOut {
		p = parseInitProbe(res.Stdout)
	}
	state, code := p.judge(run.hasReady)
	updated, ok := m.inits.update(t.owner, t.proj.Dir, run.gen, func(r *InitRun) {
		if r.State != InitStateRunning {
			return
		}
		now := time.Now()
		if p.answered {
			r.observe(p, now)
		}
		if code != nil {
			c := *code
			r.ExitCode = &c
		}
		switch {
		case state == InitStateRunning && !now.Before(r.deadline):
			r.finish(InitStateTimeout, "")
		case state == InitStateFailed && r.Setup != nil && r.Setup.Status == SetupStateFailed:
			r.finish(state, setupFailedMessage(r.Setup))
		case state != InitStateRunning:
			r.finish(state, "")
		}
	})
	if ok {
		m.noteInitRun(ctx, t, updated)
	}
	return updated, ok, nil
}

func (m *Manager) waitInit(ctx context.Context, t *initTarget, run InitRun) (InitRun, error) {
	for {
		updated, ok, err := m.probeInit(ctx, t, run)
		if err != nil {
			return run, err
		}
		if !ok {
			run.finish(InitStateFailed, initSupersededMessage())
			return run, nil
		}
		run = updated
		if run.State != InitStateRunning {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return run, ctx.Err()
		case <-time.After(initPollEvery):
		}
	}
}

func (m *Manager) initLogTail(ctx context.Context, t *initTarget) string {
	//i18n:ignore shell command run in the sandbox
	cmd := fmt.Sprintf("tail -n %d %s 2>/dev/null", initLogTailLines, shellSingleQuote(initLogPath(t.proj.Dir)))
	res, _, err := m.initExec(ctx, t, cmd, initProbeTimeoutMs)
	if err != nil {
		return ""
	}
	return res.Stdout
}

func (m *Manager) runInit(ctx context.Context, t *initTarget, wait bool) (InitRunResult, error) {
	started := time.Now()
	out := InitRunResult{
		Project:  t.proj.Name,
		Revision: t.proj.Script.Revision,
		Ports:    t.proj.Script.Manifest.Ports,
		LogPath:  initLogPath(t.proj.Dir),
	}
	run, err := m.launchInit(ctx, t)
	if err != nil {
		return out, err
	}
	if wait {
		run, err = m.waitInit(ctx, t, run)
		if err != nil {
			return out, err
		}
	}
	out.State = run.State
	out.ExitCode = run.ExitCode
	out.Message = run.Message
	out.Phase = run.Phase
	out.Setup = run.Setup
	out.ElapsedMs = time.Since(started).Milliseconds()
	out.LogTail = m.initLogTail(ctx, t)
	return out, nil
}

func (m *Manager) refreshInit(ctx context.Context, t *initTarget, run InitRun) InitRun {
	if run.State != InitStateRunning {
		m.noteInitRun(ctx, t, run)
		return run
	}
	updated, ok, err := m.probeInit(ctx, t, run)
	if err != nil || !ok {
		return run
	}
	return updated
}
