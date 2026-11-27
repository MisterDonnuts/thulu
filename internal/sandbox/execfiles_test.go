package sandbox

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/gatesprofile"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

// recordingRuntime keeps every command the manager sends, and can refuse the file writes.
type recordingRuntime struct {
	*fakeRuntime
	mu        sync.Mutex
	execs     []ExecOpts
	failWrite bool
}

func (r *recordingRuntime) Exec(ctx context.Context, id string, opts ExecOpts) (*ExecResult, error) {
	r.mu.Lock()
	r.execs = append(r.execs, opts)
	fail := r.failWrite && strings.Contains(opts.Command, "cat > ")
	r.mu.Unlock()
	if fail {
		return &ExecResult{ExitCode: 1, Stderr: "read-only file system"}, nil
	}
	return r.fakeRuntime.Exec(ctx, id, opts)
}

func newRecordingManager(t *testing.T) (*Manager, *recordingRuntime) {
	t.Helper()
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	_ = sett.PutJSON(t.Context(), settings.SandboxAcquireBlockMsKey, 200)
	rt := &recordingRuntime{fakeRuntime: newFakeRuntime()}
	return NewManager(st, sett, rt, ManagerConfig{}), rt
}

func profileFile(content string) ExecFile {
	return ExecFile{Path: SandboxGatesProfilePath, Content: content, Env: gatesprofile.Env}
}

func TestExecWritesItsFilesOnceAndNamesThemInTheEnv(t *testing.T) {
	m, rt := newRecordingManager(t)
	ctx := t.Context()
	run := func(content string, env map[string]string) {
		t.Helper()
		if _, _, err := m.Exec(ctx, "t1", "verification", AcquireDockerOpts{}, ExecOpts{Command: "thulu-gates run", Env: env, Files: []ExecFile{profileFile(content)}}); err != nil {
			t.Fatal(err)
		}
	}
	run(`{"schema":1}`, map[string]string{gatesprofile.Env: "/tmp/mine.json"})
	run(`{"schema":1}`, nil)
	run(`{"schema":1,"projects":{}}`, nil)

	var writes, commands []ExecOpts
	for _, e := range rt.execs {
		if strings.Contains(e.Command, "cat > ") {
			writes = append(writes, e)
		} else if e.Command == "thulu-gates run" {
			commands = append(commands, e)
		}
	}
	if len(writes) != 2 {
		t.Fatalf("writes = %d, want 2 (the first exec and the changed content)", len(writes))
	}
	if writes[0].Stdin != `{"schema":1}` || !strings.Contains(writes[0].Command, SandboxGatesProfilePath) {
		t.Errorf("first write = %+v", writes[0])
	}
	if len(writes[0].Files) != 0 {
		t.Error("the write itself carried files")
	}
	if len(commands) != 3 {
		t.Fatalf("commands = %d, want 3", len(commands))
	}
	for i, c := range commands {
		// Thulu's profile wins over one the command names: the project's lives in Thulu.
		if c.Env[gatesprofile.Env] != SandboxGatesProfilePath {
			t.Errorf("command %d %s = %q, want %q", i, gatesprofile.Env, c.Env[gatesprofile.Env], SandboxGatesProfilePath)
		}
	}
}

func TestExecRunsWithoutAFileItCouldNotWrite(t *testing.T) {
	m, rt := newRecordingManager(t)
	rt.failWrite = true
	if _, _, err := m.Exec(t.Context(), "t1", "verification", AcquireDockerOpts{}, ExecOpts{Command: "thulu-gates run", Files: []ExecFile{profileFile(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	last := rt.execs[len(rt.execs)-1]
	if last.Command != "thulu-gates run" {
		t.Fatalf("last command = %q", last.Command)
	}
	if v, ok := last.Env[gatesprofile.Env]; ok {
		t.Errorf("%s = %q although the file was not written", gatesprofile.Env, v)
	}
}

var _ Runtime = (*recordingRuntime)(nil)

// sameNameRuntime names every sandbox after its task, as the Kubernetes runtime names its pods.
type sameNameRuntime struct{ *recordingRuntime }

func (r sameNameRuntime) Create(_ context.Context, opts CreateOpts) (*CreateResult, error) {
	r.fakeRuntime.mu.Lock()
	defer r.fakeRuntime.mu.Unlock()
	id := "sandbox-" + opts.TaskID
	r.fakeRuntime.alive[id] = true
	return &CreateResult{ContainerID: id, HostPorts: map[int]int{}}, nil
}

// A sandbox that replaces one under the same id starts with an empty /tmp, so the file is written
// again: the manager forgets what it wrote when it releases or creates a container.
func TestExecWritesItsFilesAgainInASandboxRecreatedUnderTheSameID(t *testing.T) {
	m, rt := newRecordingManager(t)
	m.docker = sameNameRuntime{rt}
	ctx := t.Context()
	run := func() {
		t.Helper()
		if _, _, err := m.Exec(ctx, "t1", "verification", AcquireDockerOpts{}, ExecOpts{Command: "thulu-gates run", Files: []ExecFile{profileFile(`{"schema":1}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	run()
	if err := m.Release(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	run()
	writes := 0
	for _, e := range rt.execs {
		if strings.Contains(e.Command, "cat > ") {
			writes++
		}
	}
	if writes != 2 {
		t.Errorf("writes = %d, want 2 (once per sandbox, though both are named sandbox-t1)", writes)
	}
	if len(m.files.written) != 1 {
		t.Errorf("remembered files = %d, want only the live sandbox's", len(m.files.written))
	}
}

// The profile a command gets is the task's, from Thulu: one the agent names in the exec's env is
// dropped, also when the task's projects have none.
func TestExecToolGatesProfileComesFromThulu(t *testing.T) {
	profiles := map[string][]byte{"t1": []byte(`{"schema":1,"projects":{"app":{}}}`)}
	h := (&MCPHandler{}).WithGatesProfiles(func(_ context.Context, taskID string) []byte { return profiles[taskID] })

	withProfile := ExecOpts{Env: map[string]string{gatesprofile.Env: "/tmp/mine.json", "KEEP": "1"}}
	h.withGatesProfile(t.Context(), "t1", &withProfile)
	if _, ok := withProfile.Env[gatesprofile.Env]; ok {
		t.Errorf("the agent's %s survived: %v", gatesprofile.Env, withProfile.Env)
	}
	if withProfile.Env["KEEP"] != "1" {
		t.Errorf("other env lost: %v", withProfile.Env)
	}
	if len(withProfile.Files) != 1 || withProfile.Files[0].Path != SandboxGatesProfilePath ||
		withProfile.Files[0].Env != gatesprofile.Env || withProfile.Files[0].Content != string(profiles["t1"]) {
		t.Errorf("files = %+v", withProfile.Files)
	}

	none := ExecOpts{Env: map[string]string{gatesprofile.Env: "/tmp/mine.json"}}
	h.withGatesProfile(t.Context(), "t2", &none)
	if _, ok := none.Env[gatesprofile.Env]; ok || len(none.Files) != 0 {
		t.Errorf("task without a profile: env %v, files %+v", none.Env, none.Files)
	}

	var nilEnv ExecOpts
	(&MCPHandler{}).withGatesProfile(t.Context(), "t1", &nilEnv)
	if nilEnv.Env != nil || len(nilEnv.Files) != 0 {
		t.Errorf("handler without profiles: %+v", nilEnv)
	}
}
