package task

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/testgit"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type fakeRunners map[string]*runner.Runner

func (f fakeRunners) Get(_ context.Context, ws, id string) (*runner.Runner, error) {
	r := f[id]
	if r == nil || (ws != "" && r.WorkspaceID != ws) {
		return nil, apperr.NotFound("runner.errors.not_found", "id", id)
	}
	return r, nil
}

func machine(id, name string) *runner.Runner {
	return &runner.Runner{ID: id, Name: name, WorkspaceID: testWS, Online: true}
}

func machinesAB() map[string]*runner.Runner {
	return map[string]*runner.Runner{"a": machine("a", "mac-a"), "b": machine("b", "mac-b")}
}

// Cases 11-16, 21 and 55: where a run goes, and why it cannot.
func TestPlacementDecide(t *testing.T) {
	revoked := machinesAB()
	now := time.Now()
	revoked["a"].RevokedAt = &now
	offline := machinesAB()
	offline["a"].Online = false

	cases := []struct {
		name    string
		p       placement
		want    string
		errKey  string
		errName []string
	}{
		{name: "11 no machine project, key profile", p: placement{runners: machinesAB()}},
		{name: "12 machine profile alone", p: placement{profileRunner: "b", runners: machinesAB()}, want: "b"},
		{name: "13 machine project, key profile", p: placement{projectRunners: []string{"a"}, runners: machinesAB()}, want: "a"},
		{name: "14 project and profile on the same machine",
			p: placement{projectRunners: []string{"a"}, profileRunner: "a", runners: machinesAB()}, want: "a"},
		{name: "15 project on A, profile on B",
			p:      placement{projectRunners: []string{"a"}, profileRunner: "b", profileName: "mac b", runners: machinesAB()},
			errKey: "task.errors.placement_conflict", errName: []string{"mac-a", "mac-b", "mac b"}},
		{name: "16 projects on A and B", p: placement{projectRunners: []string{"a", "b"}, runners: machinesAB()},
			errKey: "task.errors.task_mixes_runners", errName: []string{"mac-a", "mac-b"}},
		{name: "21 revoked machine", p: placement{profileRunner: "a", runners: revoked},
			errKey: "task.errors.runner_revoked", errName: []string{"mac-a"}},
		{name: "55 offline machine", p: placement{projectRunners: []string{"a"}, runners: offline},
			errKey: "task.errors.runner_offline", errName: []string{"mac-a"}},
		{name: "unknown machine", p: placement{profileRunner: "gone", runners: machinesAB()},
			errKey: "task.errors.runner_unknown", errName: []string{"gone"}},
		{name: "runners off", p: placement{profileRunner: "a"}, errKey: "task.errors.runners_unavailable"},
		{name: "project whose machine was lost", p: placement{lostProject: "app", runners: machinesAB()},
			errKey: "task.errors.project_runner_lost", errName: []string{"app"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.p.decide()
			if tc.errKey == "" {
				if err != nil || got != tc.want {
					t.Fatalf("decide = (%q, %v), want %q", got, err, tc.want)
				}
				return
			}
			if apperr.Code(err) != tc.errKey {
				t.Fatalf("decide err = %v (key %q), want %s", err, apperr.Code(err), tc.errKey)
			}
			for _, n := range tc.errName {
				if !strings.Contains(apperr.Text(err), n) {
					t.Errorf("err %q does not name %q", apperr.Text(err), n)
				}
			}
		})
	}
}

var credentialVars = []string{
	"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", engine.LLMProfileEnv,
}

func machineProfile() secrets.ResolvedLLM {
	return secrets.ResolvedLLM{Source: "binding", ProfileID: "p1", ProfileName: "mi mac", Engine: engine.DefaultName,
		Model: "opus", Effort: "high", RunnerID: "a"}
}

// Case 12: a machine profile sends the run to its machine with no credential var, with its model.
func TestMachineProfileSendsTheRunToItsMachine(t *testing.T) {
	d := &Dispatcher{llmResolver: resolverReturning(machineProfile(), nil), runners: fakeRunners(machinesAB())}
	env := map[string]string{"ENGINE": "codex", "ANTHROPIC_API_KEY": "sk-ant-stale", "OPENAI_API_KEY": "sk-stale"}
	if err := d.applyLLMAuth(t.Context(), env, testWS, &types.AgentType{Engine: "codex", Model: "gpt-5"}, "dev", nil); err != nil {
		t.Fatal(err)
	}
	if env[runner.RunnerIDEnv] != "a" || env[engine.LLMSourceEnv] != engine.LLMSourceRunnerLogin {
		t.Errorf("placement env = %v", env)
	}
	if env["ENGINE"] != engine.DefaultName || env["MODEL"] != "opus" || env["EFFORT"] != "high" {
		t.Errorf("engine/model = %s/%s/%s, want the profile's", env["ENGINE"], env["MODEL"], env["EFFORT"])
	}
	for _, v := range append(credentialVars, "OPENAI_API_KEY") {
		if _, ok := env[v]; ok {
			t.Errorf("%s reached a run paid by the machine's login", v)
		}
	}
}

// Case 11: a key profile without machine projects stays in a container.
func TestKeyProfileStaysInAContainer(t *testing.T) {
	key := secrets.ResolvedLLM{Source: "binding", ProfileID: "p2", Engine: engine.DefaultName, Credential: "sk-ant-api-x"}
	d := &Dispatcher{llmResolver: resolverReturning(key, nil), runners: fakeRunners(machinesAB())}
	env := map[string]string{"ENGINE": engine.DefaultName}
	if err := d.applyLLMAuth(t.Context(), env, testWS, claudeAgent, "dev", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := env[runner.RunnerIDEnv]; ok || env["ANTHROPIC_API_KEY"] != "sk-ant-api-x" {
		t.Errorf("env = %v, want the key in a container", env)
	}
}

// Case 18: the triggering person's login never pays a run on a machine.
func TestMachineProfileBeatsThePersonalLogin(t *testing.T) {
	d := &Dispatcher{llmResolver: resolverReturning(machineProfile(), nil), runners: fakeRunners(machinesAB())}
	d.WithPersonalLogins(fakeLogins{"ada": "octopus-claude-ada"})
	ctx := WithTriggeredBy(t.Context(), "ada")
	env := map[string]string{"ENGINE": engine.DefaultName}
	if err := d.applyRunLLM(ctx, env, &Task{WorkspaceID: testWS}, claudeAgent, "dev"); err != nil {
		t.Fatal(err)
	}
	if env[engine.LLMSourceEnv] != engine.LLMSourceRunnerLogin || env[EnvPersonalLoginVolume] != "" {
		t.Errorf("env = %v, want the machine's login", env)
	}
}

// Case 21 for the readiness check: a profile on a live machine is ready to chat, one on a revoked
// machine is not.
func TestCredentialReadyTakesALiveMachine(t *testing.T) {
	ms := machinesAB()
	d := &Dispatcher{llmResolver: resolverReturning(machineProfile(), nil), runners: fakeRunners(ms)}
	if err := d.LLMCredentialReady(t.Context(), testWS, claudeAgent, "dev"); err != nil {
		t.Fatalf("online machine err = %v", err)
	}
	now := time.Now()
	ms["a"].RevokedAt = &now
	if err := d.LLMCredentialReady(t.Context(), testWS, claudeAgent, "dev"); apperr.Code(err) != "task.errors.runner_revoked" {
		t.Fatalf("revoked machine err = %v", err)
	}
}

// Case 39 for the auth: a chat on a machine profile goes to the machine on its login.
func TestChatOnAMachineProfileGoesToTheMachine(t *testing.T) {
	touched := 0
	d := &Dispatcher{
		llmResolver:  resolverReturning(machineProfile(), nil),
		runners:      fakeRunners(machinesAB()),
		llmProfTouch: func(context.Context, string, string) { touched++ },
	}
	env := map[string]string{"ENGINE": engine.DefaultName}
	if err := d.ApplyLLMAuth(t.Context(), env, testWS, claudeAgent, "dev"); err != nil {
		t.Fatal(err)
	}
	if env[runner.RunnerIDEnv] != "a" || env[engine.LLMSourceEnv] != engine.LLMSourceRunnerLogin || touched != 1 {
		t.Errorf("touched = %d, env = %v; want machine a on its login", touched, env)
	}
}

type machineFixture struct {
	d        *Dispatcher
	store    *Store
	projects *project.Store
	l        *recordingLauncher
	runners  fakeRunners
}

func newMachineFixture(t *testing.T) *machineFixture {
	t.Helper()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatal(err)
	}
	projects, err := project.NewStore(db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	l := newRecordingLauncher()
	rs := fakeRunners(machinesAB())
	d := NewDispatcher(store, l, projects, nil, nil).WithRunners(rs)
	return &machineFixture{d: d, store: store, projects: projects, l: l, runners: rs}
}

func (f *machineFixture) task(t *testing.T, projectIDs ...string) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: "placed", ProjectIDs: projectIDs}
	if err := f.store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Case 17: a Thulu project next to a machine project goes to that machine.
func TestThuluProjectBesideAMachineProjectGoesToTheMachine(t *testing.T) {
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	f := newMachineFixture(t)
	thulu, err := f.projects.Create(t.Context(), "web", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatal(err)
	}
	onA, err := f.projects.CreateOnRunner(t.Context(), "app", "", "a", project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.d.placementFor(t.Context(), testWS, f.task(t, thulu.ID, onA.ID), secrets.ResolvedLLM{Source: "host"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := p.decide(); got != "a" || err != nil {
		t.Errorf("decide = (%q, %v), want a", got, err)
	}
}

// Case 40 for the auth: a task's chat goes where the task's code is, even on a key profile.
func TestTaskChatGoesToItsProjectsMachine(t *testing.T) {
	f := newMachineFixture(t)
	f.d.WithLLMResolver(resolverReturning(secrets.ResolvedLLM{Source: "binding", ProfileID: "k1",
		Engine: engine.DefaultName, Model: "opus"}, nil), nil, nil)
	onA, err := f.projects.CreateOnRunner(t.Context(), "app", "", "a", project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatal(err)
	}
	tk := f.task(t, onA.ID)
	env := map[string]string{"ENGINE": engine.DefaultName, "TASK_ID": tk.ID}
	if err := f.d.ApplyLLMAuth(t.Context(), env, testWS, claudeAgent, "dev"); err != nil {
		t.Fatal(err)
	}
	if env[runner.RunnerIDEnv] != "a" || env["MODEL"] != "opus" || env["ANTHROPIC_API_KEY"] != "" {
		t.Errorf("env = %v, want machine a with the profile's model and no key", env)
	}
}

// A chat outlives its deleted task: its next turn is placed by the profile alone, as a chat
// without a task.
func TestChatOfADeletedTaskFollowsItsProfile(t *testing.T) {
	f := newMachineFixture(t)
	f.d.WithLLMResolver(resolverReturning(machineProfile(), nil), nil, nil)
	tk := f.task(t)
	if err := f.store.Delete(t.Context(), tk.ID); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ENGINE": engine.DefaultName, "TASK_ID": tk.ID}
	if err := f.d.ApplyLLMAuth(t.Context(), env, testWS, claudeAgent, "dev"); err != nil {
		t.Fatalf("chat of a deleted task: %v", err)
	}
	if env[runner.RunnerIDEnv] != "a" {
		t.Errorf("env = %v, want the profile's machine a", env)
	}
}

// Case 19: a mention follows the same rules as a stage run.
func TestMentionGoesWhereItsProfileRuns(t *testing.T) {
	f := newMachineFixture(t)
	f.d.WithLLMResolver(resolverReturning(machineProfile(), nil), nil, nil)
	f.d.HandleMention(context.Background(), f.task(t), mentionAgent(), &Comment{ID: "c1", Body: "hola"})
	launches := f.l.launches()
	if len(launches) != 1 || launches[0][runner.RunnerIDEnv] != "a" {
		t.Fatalf("launches = %v, want one on machine a", launches)
	}
}

// Case 55 at launch: a machine that is not connected refuses the run before anything is queued.
func TestOfflineMachineRefusesTheRunAtLaunch(t *testing.T) {
	f := newMachineFixture(t)
	f.runners["a"].Online = false
	f.d.WithLLMResolver(resolverReturning(machineProfile(), nil), nil, nil)
	tk := f.task(t)
	at := &types.AgentType{Name: "dev", Type: "dev", ValidStages: []types.Stage{types.StageInProgress}}
	f.d.launchStage(t.Context(), tk, types.StageInProgress, "dev", at, launchOpts{})
	if n := len(f.l.launches()); n != 0 {
		t.Fatalf("launched %d runs for an offline machine", n)
	}
	comments, err := f.store.ListComments(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comments {
		if strings.Contains(c.Body, "mac-a") {
			return
		}
	}
	t.Errorf("no comment names the machine: %+v", comments)
}

// Case 20: steering follows the engine the profile resolved, not the agent's label.
func TestSteeringFollowsTheResolvedEngine(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	key := secrets.ResolvedLLM{Source: "binding", ProfileID: "p2", Engine: engine.DefaultName, Credential: "sk-ant-api-x"}
	d.WithLLMResolver(resolverReturning(key, nil), nil, nil)
	tk := &Task{WorkspaceID: testWS, Title: "steer"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	at := &types.AgentType{Name: "developer", Type: "developer", Engine: "codex",
		ValidStages: []types.Stage{types.StageInProgress}}
	d.launchStage(t.Context(), tk, types.StageInProgress, "developer", at, launchOpts{})
	launches := l.launches()
	if len(launches) != 1 {
		t.Fatalf("launches = %d, want 1", len(launches))
	}
	if env := launches[0]; env["ENGINE"] != engine.DefaultName || env["STEER_ENABLED"] != "1" {
		t.Errorf("ENGINE=%q STEER_ENABLED=%q, want steering for the resolved claude-code", env["ENGINE"], env["STEER_ENABLED"])
	}
}
