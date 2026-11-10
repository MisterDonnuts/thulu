package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func rawParams(t *testing.T, js string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestResolveInitParams(t *testing.T) {
	m := project.InitManifest{Params: []project.InitParam{
		{Name: "PORT", Default: "3000"},
		{Name: "DB_URL", Required: true},
		{Name: "SEED"},
		{Name: "DEBUG"},
	}}

	got, err := resolveInitParams(m, rawParams(t, `{"DB_URL":"postgres://x","SEED":7,"DEBUG":true}`))
	if err != nil {
		t.Fatalf("valid params refused: %v", err)
	}
	want := map[string]string{"PORT": "3000", "DB_URL": "postgres://x", "SEED": "7", "DEBUG": "true"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("params = %v, want %v", got, want)
	}

	if got, _ := resolveInitParams(m, rawParams(t, `{"DB_URL":"x","PORT":"8080"}`)); got["PORT"] != "8080" {
		t.Fatalf("a given value must win over the default: %v", got)
	}

	cases := map[string]string{
		"unknown name":           `{"DB_URL":"x","NOPE":"1"}`,
		"required missing":       `{}`,
		"required empty":         `{"DB_URL":""}`,
		"required null":          `{"DB_URL":null}`,
		"object value":           `{"DB_URL":{"a":1}}`,
		"array value":            `{"DB_URL":["a"]}`,
		"lowercase unknown name": `{"DB_URL":"x","port":"1"}`,
	}
	for name, js := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveInitParams(m, rawParams(t, js)); err == nil {
				t.Fatalf("%s accepted", js)
			}
		})
	}

	_, err = resolveInitParams(m, rawParams(t, `{"DB_URL":"x","ZED":"1","ALPHA":"2"}`))
	if err == nil || !strings.Contains(err.Error(), "ALPHA") {
		t.Fatalf("unknown-param error should name them: %v", err)
	}
}

func TestInitProbeJudge(t *testing.T) {
	code := func(c *int) string {
		if c == nil {
			return "nil"
		}
		return strconv.Itoa(*c)
	}
	cases := []struct {
		out       string
		hasReady  bool
		wantState string
		wantCode  string
	}{
		{"alive=1\nexit=\nready=1\n", true, InitStateRunning, "nil"},
		{"alive=1\nexit=\nready=0\n", true, InitStateReady, "nil"},
		{"alive=0\nexit=0\nready=0\n", true, InitStateReady, "0"},
		{"alive=0\nexit=0\nready=1\n", true, InitStateRunning, "0"},
		{"alive=0\nexit=0\nready=-\n", false, InitStateReady, "0"},
		{"alive=0\nexit=2\nready=1\n", true, InitStateFailed, "2"},
		{"alive=0\nexit=2\nready=-\n", false, InitStateFailed, "2"},
		{"alive=0\nexit=\nready=1\n", true, InitStateFailed, "nil"},
		{"alive=1\nexit=\nready=-\n", false, InitStateRunning, "nil"},
		{"", true, InitStateRunning, "nil"},
	}
	for _, c := range cases {
		state, ec := parseInitProbe(c.out).judge(c.hasReady)
		if state != c.wantState || code(ec) != c.wantCode {
			t.Errorf("%q ready=%v → %s/%s, want %s/%s", c.out, c.hasReady, state, code(ec), c.wantState, c.wantCode)
		}
	}
}

func TestInitEnvFileQuotes(t *testing.T) {
	got := initEnvFile(map[string]string{"B": "it's", "A": "$(rm -rf /)"})
	want := "export A='$(rm -rf /)'\nexport B='it'\\''s'\n"
	if got != want {
		t.Fatalf("env file = %q, want %q", got, want)
	}
}

type localRuntime struct {
	mu    sync.Mutex
	n     int
	execs []string
}

func (l *localRuntime) Image() string { return "local" }

func (l *localRuntime) Create(context.Context, CreateOpts) (*CreateResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n++
	return &CreateResult{ContainerID: fmt.Sprintf("local-%d", l.n), HostPorts: map[int]int{}}, nil
}

func (l *localRuntime) Exec(ctx context.Context, _ string, opts ExecOpts) (*ExecResult, error) {
	l.mu.Lock()
	l.execs = append(l.execs, opts.Command)
	l.mu.Unlock()
	if opts.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(opts.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	start := time.Now()
	cmd := exec.CommandContext(ctx, "bash", "-c", opts.Command)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}
	err := cmd.Run()
	res := &ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), DurationMs: time.Since(start).Milliseconds()}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut, res.ExitCode = true, -1
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
	} else if err != nil {
		return nil, err
	}
	return res, nil
}

func (l *localRuntime) Alive(context.Context, string) bool { return true }
func (l *localRuntime) Remove(context.Context, string) error {
	return nil
}

type fakeInitScripts struct {
	mu       sync.Mutex
	projects []InitProject
	saved    []project.InitScriptInput
	savedFor []string
}

func (f *fakeInitScripts) TaskProjects(context.Context, string) ([]InitProject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]InitProject(nil), f.projects...), nil
}

func (f *fakeInitScripts) SaveInitScript(_ context.Context, taskID, projectID string, in project.InitScriptInput) (*project.InitScript, error) {
	in, err := project.ValidateInitScript(in)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, in)
	f.savedFor = append(f.savedFor, taskID+"/"+projectID)
	return &project.InitScript{ProjectID: projectID, Revision: len(f.saved) + 1, Script: in.Script, Manifest: in.Manifest,
		AuthorKind: project.InitAuthorAgent, AuthorLabel: "agent tester"}, nil
}

type initRig struct {
	h       *MCPHandler
	sess    *mcpSession
	scripts *fakeInitScripts
	rt      *localRuntime
	work    string
}

func newInitRig(t *testing.T, sc *project.InitScript) *initRig {
	t.Helper()
	return newInitRigOn(t, sc, func(l *localRuntime) Runtime { return l })
}

func newInitRigOn(t *testing.T, sc *project.InitScript, wrap func(*localRuntime) Runtime) *initRig {
	t.Helper()
	for _, bin := range []string{"bash", "setsid", "base64", "sed"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	root := t.TempDir()
	oldRoot, oldProjects, oldPoll := initRoot, initProjectsRoot, initPollEvery
	initRoot, initProjectsRoot, initPollEvery = filepath.Join(root, "init"), filepath.Join(root, "projects"), 100*time.Millisecond
	t.Cleanup(func() { initRoot, initProjectsRoot, initPollEvery = oldRoot, oldProjects, oldPoll })
	work := filepath.Join(initProjectsRoot, "app")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if pid, err := os.ReadFile(filepath.Join(initRoot, "app", "pid")); err == nil {
			_ = exec.Command("bash", "-c", "kill -KILL -- -"+strings.TrimSpace(string(pid))).Run()
		}
	})

	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	rt := &localRuntime{}
	m := NewManager(st, sett, wrap(rt), ManagerConfig{})
	scripts := &fakeInitScripts{projects: []InitProject{
		{ID: "p-app", Name: "App", Dir: "app", Mounted: true, Script: sc},
		{ID: "p-docs", Name: "Docs", Dir: "docs", Mounted: false},
	}}
	resolver := WorkspaceResolverFunc(func(context.Context, string) (string, string, string, map[string]string, error) {
		return "task-1", "", "ws-1", map[string]string{"app": work}, nil
	})
	h := NewMCPHandler(m, resolver).WithInitScripts(scripts)
	return &initRig{
		h:       h,
		sess:    &mcpSession{taskID: "task-1", stage: "in_progress"},
		scripts: scripts,
		rt:      rt,
		work:    work,
	}
}

func (r *initRig) call(t *testing.T, tool, args string, out any) (string, bool) {
	t.Helper()
	raw := json.RawMessage(fmt.Sprintf(`{"name":%q,"arguments":%s}`, tool, args))
	res, ok := r.h.handleToolCall(t.Context(), r.sess, raw).(map[string]any)
	if !ok {
		t.Fatalf("%s returned a %T", tool, res)
	}
	content, _ := res["content"].([]map[string]any)
	if len(content) == 0 {
		t.Fatalf("%s returned no content: %v", tool, res)
	}
	text, _ := content[0]["text"].(string)
	isErr, _ := res["isError"].(bool)
	if out != nil && !isErr {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			t.Fatalf("%s: decode %q: %v", tool, text, err)
		}
	}
	return text, isErr
}

func initScript(script string, m project.InitManifest) *project.InitScript {
	if m.Workdir == "" {
		m.Workdir = "."
	}
	if m.Ready.TimeoutSeconds == 0 {
		m.Ready.TimeoutSeconds = 30
	}
	return &project.InitScript{ProjectID: "p-app", Revision: 4, Script: script, Manifest: m}
}

func TestInitRun_ReadyWithParamsAndEnv(t *testing.T) {
	r := newInitRig(t, initScript(
		`echo "hello $GREETING from $OCTOPUS_PROJECT in $OCTOPUS_TASK_ID at $(pwd)"
(sleep 0.4; touch up.flag) &
sleep 60`,
		project.InitManifest{
			Params: []project.InitParam{{Name: "GREETING", Default: "hi"}, {Name: "NAME", Required: true}},
			Ports:  []int{3000},
			Ready:  project.InitReady{Command: "test -f up.flag"},
		}))

	var res InitRunResult
	text, isErr := r.call(t, "init_run", `{"project":"App","params":{"NAME":"x","GREETING":"hola"}}`, &res)
	if isErr {
		t.Fatalf("init_run failed: %s", text)
	}
	if res.State != InitStateReady {
		t.Fatalf("state = %s (%s), want ready", res.State, text)
	}
	want := "hello hola from App in task-1 at " + r.work
	if !strings.Contains(res.LogTail, want) {
		t.Fatalf("log tail %q lacks %q", res.LogTail, want)
	}
	if len(res.Ports) != 1 || res.Ports[0] != 3000 || res.Revision != 4 {
		t.Fatalf("result = %+v", res)
	}

	var st struct {
		Projects []initStatusEntry `json:"projects"`
	}
	if text, isErr := r.call(t, "init_status", `{}`, &st); isErr {
		t.Fatalf("init_status: %s", text)
	}
	if len(st.Projects) != 2 {
		t.Fatalf("status = %+v", st)
	}
	app, docs := st.Projects[0], st.Projects[1]
	if !app.HasScript || app.Run.State != InitStateReady || app.Run.StartedAt.IsZero() || app.Run.FinishedAt == nil {
		t.Fatalf("app status = %+v", app)
	}
	if docs.HasScript || docs.Run.State != InitStateNever {
		t.Fatalf("docs status = %+v", docs)
	}
}

func TestInitRun_RejectsBadParamsBeforeRunning(t *testing.T) {
	r := newInitRig(t, initScript("true", project.InitManifest{
		Params: []project.InitParam{{Name: "NAME", Required: true}},
	}))
	for _, args := range []string{`{"project":"app"}`, `{"project":"app","params":{"NAME":"x","OTHER":"y"}}`} {
		if text, isErr := r.call(t, "init_run", args, nil); !isErr {
			t.Fatalf("%s accepted: %s", args, text)
		}
	}
	if len(r.rt.execs) != 0 {
		t.Fatalf("a refused run reached the sandbox: %v", r.rt.execs)
	}
}

func TestInitRun_FailedScriptReportsExitCode(t *testing.T) {
	r := newInitRig(t, initScript("echo boom >&2; exit 7", project.InitManifest{}))
	var res InitRunResult
	text, isErr := r.call(t, "init_run", `{"project":"app"}`, nil)
	if !isErr {
		t.Fatalf("a failed run is not an error result: %s", text)
	}
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatal(err)
	}
	if res.State != InitStateFailed || res.ExitCode == nil || *res.ExitCode != 7 || !strings.Contains(res.LogTail, "boom") {
		t.Fatalf("result = %+v", res)
	}
}

func TestInitRun_NoReadyCommandIsReadyOnExitZero(t *testing.T) {
	r := newInitRig(t, initScript("echo built", project.InitManifest{}))
	var res InitRunResult
	if text, isErr := r.call(t, "init_run", `{"project":"p-app"}`, &res); isErr || res.State != InitStateReady {
		t.Fatalf("init_run = %s", text)
	}
}

func TestInitRun_TimesOutWhenNeverReady(t *testing.T) {
	r := newInitRig(t, initScript("sleep 60", project.InitManifest{
		Ready: project.InitReady{Command: "false", TimeoutSeconds: 1},
	}))
	start := time.Now()
	text, isErr := r.call(t, "init_run", `{"project":"app"}`, nil)
	var res InitRunResult
	_ = json.Unmarshal([]byte(text), &res)
	if !isErr || res.State != InitStateTimeout {
		t.Fatalf("init_run = %s", text)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("a 1s ready timeout took %s", d)
	}
}

func TestInitRun_RerunKillsThePreviousRun(t *testing.T) {
	r := newInitRig(t, initScript(`sleep 300 & echo $! > child.pid; wait`, project.InitManifest{}))

	var first InitRunResult
	if text, isErr := r.call(t, "init_run", `{"project":"app","wait":false}`, &first); isErr || first.State != InitStateRunning {
		t.Fatalf("first run = %s", text)
	}
	child := readPidFile(t, filepath.Join(r.work, "child.pid"))
	leader := readPidFile(t, filepath.Join(initRoot, "app", "pid"))
	if !processRunning(child) {
		t.Fatalf("background child %d not running after start", child)
	}

	if text, isErr := r.call(t, "init_run", `{"project":"app","wait":false}`, nil); isErr {
		t.Fatalf("second run = %s", text)
	}
	deadline := time.Now().Add(5 * time.Second)
	for processRunning(child) || processRunning(leader) {
		if time.Now().After(deadline) {
			t.Fatalf("previous run still alive: child %d=%v leader %d=%v",
				child, processRunning(child), leader, processRunning(leader))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if now := readPidFile(t, filepath.Join(initRoot, "app", "pid")); now == leader {
		t.Fatalf("pid file still names the old run %d", leader)
	}
}

func readPidFile(t *testing.T, p string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(p)
		if n, convErr := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && convErr == nil && n > 0 {
			return n
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid in %s", p)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func processRunning(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndex(s, ") ")
	return i >= 0 && i+2 < len(s) && s[i+2] != 'Z'
}

func TestInitTools_ProjectOutsideTheTask(t *testing.T) {
	r := newInitRig(t, initScript("true", project.InitManifest{}))
	for _, tool := range []string{"init_run", "init_script_get", "init_status"} {
		if text, isErr := r.call(t, tool, `{"project":"someone-elses"}`, nil); !isErr {
			t.Fatalf("%s on a project outside the task = %s", tool, text)
		}
	}
	text, isErr := r.call(t, "init_script_update", `{"project":"someone-elses","script":"x","note":"n"}`, nil)
	if !isErr {
		t.Fatalf("init_script_update outside the task = %s", text)
	}
	if len(r.scripts.saved) != 0 {
		t.Fatal("a save outside the task reached the store")
	}
	if text, isErr := r.call(t, "init_run", `{}`, nil); !isErr || !strings.Contains(text, "App, Docs") {
		t.Fatalf("init_run without a project = %s", text)
	}
}

func TestInitRun_RefusesProjectsItCannotRun(t *testing.T) {
	r := newInitRig(t, initScript("true", project.InitManifest{}))
	if text, isErr := r.call(t, "init_run", `{"project":"Docs"}`, nil); !isErr {
		t.Fatalf("init_run on a project with no script and no worktree = %s", text)
	}
}

func TestInitScriptUpdate_SavesForTheTasksProject(t *testing.T) {
	r := newInitRig(t, initScript("true", project.InitManifest{}))
	args := `{"project":"App","script":"make up","note":"use make","author_kind":"user","author_label":"admin",
		"manifest":{"ports":[8080],"ready":{"command":"curl -fs localhost:8080"}}}`
	var out map[string]any
	if text, isErr := r.call(t, "init_script_update", args, &out); isErr {
		t.Fatalf("update = %s", text)
	}
	if len(r.scripts.saved) != 1 || r.scripts.savedFor[0] != "task-1/p-app" {
		t.Fatalf("saved = %+v for %v", r.scripts.saved, r.scripts.savedFor)
	}
	in := r.scripts.saved[0]
	if in.Script != "make up" || in.Note != "use make" || in.Manifest.Ports[0] != 8080 || in.Manifest.Workdir != "." {
		t.Fatalf("saved input = %+v", in)
	}
	if out["author_label"] != "agent tester" {
		t.Fatalf("author from the request body leaked through: %v", out)
	}

	if text, isErr := r.call(t, "init_script_update", `{"project":"App","script":"x","note":"n","manifest":{"params":[{"name":"bad-name"}]}}`, nil); !isErr {
		t.Fatalf("invalid manifest accepted: %s", text)
	}
}

func TestInitTools_UnavailableWithoutBackend(t *testing.T) {
	h := NewMCPHandler(nil, nil)
	res, _ := h.handleToolCall(t.Context(), &mcpSession{taskID: "t"}, json.RawMessage(`{"name":"init_status","arguments":{}}`)).(map[string]any)
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("init_status without a backend = %v", res)
	}
}

func TestToolDescriptorMatchesServedTools(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "tools", "thulu-sandbox", "tool.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var desc struct {
		DefaultScope string `yaml:"default_scope"`
		Tools        []struct {
			Name  string `yaml:"name"`
			Scope string `yaml:"scope"`
		} `yaml:"tools"`
	}
	if err := yaml.Unmarshal(b, &desc); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, tool := range desc.Tools {
		listed[tool.Name] = true
		if tool.Scope != "" {
			t.Errorf("tool.yaml gives %s a scope; thulu-sandbox has none", tool.Name)
		}
	}
	if desc.DefaultScope != "" {
		t.Errorf("tool.yaml has default_scope %q; thulu-sandbox has no scopes", desc.DefaultScope)
	}
	served := map[string]bool{}
	for _, d := range mcpToolDefs(nil) {
		name, _ := d["name"].(string)
		served[name] = true
		if !listed[name] {
			t.Errorf("served tool %s missing from tool.yaml", name)
		}
	}
	for name := range listed {
		if !served[name] {
			t.Errorf("tool.yaml lists %s, which the server does not serve", name)
		}
	}
}

func (r *initRig) initStatusRun(t *testing.T) InitRun {
	t.Helper()
	var out struct {
		Projects []initStatusEntry `json:"projects"`
	}
	if text, isErr := r.call(t, "init_status", `{"project":"app"}`, &out); isErr || len(out.Projects) != 1 {
		t.Fatalf("init_status = %s", text)
	}
	return out.Projects[0].Run
}

func TestInitStatus_AfterDaemonRestart(t *testing.T) {
	cases := []struct {
		name, script, args string
		ready              string
		want               string
		exit               *int
	}{
		{name: "running", script: "sleep 300", args: `{"project":"app","wait":false}`, want: InitStateRunning},
		{name: "ready", script: "echo up", args: `{"project":"app"}`, want: InitStateReady, exit: new(0)},
		{name: "ready by command", script: "sleep 300", ready: "true", args: `{"project":"app"}`, want: InitStateReady},
		{name: "failed", script: "exit 5", args: `{"project":"app"}`, want: InitStateFailed, exit: new(5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newInitRig(t, initScript(tc.script, project.InitManifest{Ready: project.InitReady{Command: tc.ready}}))
			if text, _ := r.call(t, "init_run", tc.args, nil); text == "" {
				t.Fatal("init_run answered nothing")
			}
			r.h.mgr.inits.drop("task-1")
			run := r.initStatusRun(t)
			if run.State != tc.want || run.Revision != 4 || run.Message == "" {
				t.Fatalf("recovered run = %+v, want state %s, revision 4 and a message", run, tc.want)
			}
			if tc.exit != nil && (run.ExitCode == nil || *run.ExitCode != *tc.exit) {
				t.Fatalf("exit code = %v, want %d", run.ExitCode, *tc.exit)
			}
			if run.StartedAt.IsZero() {
				t.Fatal("started_at not read back")
			}
		})
	}
}

func TestInitStatus_NeverStartsASandbox(t *testing.T) {
	r := newInitRig(t, initScript("echo up", project.InitManifest{}))
	if run := r.initStatusRun(t); run.State != InitStateNever {
		t.Fatalf("state = %s, want never", run.State)
	}
	r.rt.mu.Lock()
	created, execs := r.rt.n, len(r.rt.execs)
	r.rt.mu.Unlock()
	if created != 0 || execs != 0 {
		t.Fatalf("init_status created %d containers and ran %d commands", created, execs)
	}
}

func TestParseInitRecover(t *testing.T) {
	now := time.Unix(1_000, 0)
	if _, ok := parseInitRecover("found=0\n", now); ok {
		t.Fatal("found=0 recovered a run")
	}
	run, ok := parseInitRecover("found=1\nrevision=3\nstarted=900\ndeadline=950\nhas_ready=1\nworkdir=/w\nalive=1\nexit=\nready=1\n", now)
	if !ok || run.State != InitStateTimeout || run.Revision != 3 || run.workdir != "/w" {
		t.Fatalf("past the deadline = %+v, %v", run, ok)
	}
	run, ok = parseInitRecover("found=1\nalive=0\nexit=0\nready=-\n", now)
	if !ok || run.State != InitStateReady {
		t.Fatalf("without meta = %+v, %v", run, ok)
	}
}
