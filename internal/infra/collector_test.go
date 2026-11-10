package infra

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/system"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

type fakeSettings struct{ agents, mentions, sandboxes, chats int }

func (f fakeSettings) AgentMaxContainers(context.Context) (int, error)   { return f.agents, nil }
func (f fakeSettings) MentionMaxContainers(context.Context) (int, error) { return f.mentions, nil }
func (f fakeSettings) SandboxMax(context.Context) (int, error)           { return f.sandboxes, nil }
func (f fakeSettings) ChatMax(context.Context) (int, error)              { return f.chats, nil }

func copyProc(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(filepath.Join("testdata", "proc"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join("testdata", "proc"), p)
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		writeSized(t, filepath.Join(dst, rel), 0)
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func waitSlow(t *testing.T, c *Collector) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.slowBusy.Load() {
		if time.Now().After(deadline) {
			t.Fatal("slow pass did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCollectorEndToEnd(t *testing.T) {
	proc := copyProc(t)
	data := t.TempDir()
	writeSized(t, filepath.Join(data, "logs", "a.log"), 64)

	t0 := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	now := t0
	agentLabels := func(ws, stage string) map[string]string {
		return map[string]string{agent.LabelManaged: "true", agent.LabelType: "coder", agent.LabelStage: stage, agent.LabelWorkspaceID: ws}
	}
	f := &fakeDocker{
		info: system.Info{ServerVersion: "27.5.1", NCPU: 8},
		list: []types.Container{
			{ID: "a1", Names: []string{"/run-a"}, State: "running", Labels: agentLabels("ws1", "build")},
			{ID: "a2", Names: []string{"/run-b"}, State: "running", Labels: agentLabels("ws1", "build")},
			{ID: "s1", Names: []string{"/sbx"}, State: "running", Labels: map[string]string{sandbox.SandboxLabel: "true", agent.LabelWorkspaceID: "ws2"}},
			{ID: "x1", Names: []string{"/old"}, State: "exited", Labels: agentLabels("ws2", "review")},
		},
		stats: map[string][]container.StatsResponse{},
		df:    types.DiskUsage{LayersSize: 1234},
	}
	for _, id := range []string{"a1", "a2", "s1"} {
		f.stats[id] = []container.StatsResponse{
			statsAt(t0, 0, 0, 1000, 0),
			statsAt(t0.Add(15*time.Second), 1e9, 8e9, 2000, 0),
		}
	}

	c := New(Config{
		ProcRoot: proc,
		DataDir:  data,
		Docker:   f,
		Settings: fakeSettings{agents: 0, mentions: 2, sandboxes: 5, chats: 3},
		Admission: func() Admission {
			return Admission{Agent: Gate{Cap: 4, Inflight: 2, Waiting: 3}, Mention: Gate{Inflight: 1}}
		},
		Plan:     func(ws string) (string, int) { return "team-" + ws, 2 },
		Interval: 15 * time.Second,
		Now:      func() time.Time { return now },
	})

	ctx := context.Background()
	c.tick(ctx)
	waitSlow(t, c)

	writeSized(t, filepath.Join(proc, "stat"), 0)
	if err := os.WriteFile(filepath.Join(proc, "stat"), []byte("cpu  1300 0 600 8300 500 0 0 0 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now = t0.Add(15 * time.Second)
	c.tick(ctx)

	s := c.Overview()
	if s.Runtime.Mode != "docker" || s.Runtime.Docker.ServerVersion != "27.5.1" {
		t.Errorf("runtime = %+v", s.Runtime)
	}
	if s.Runtime.Docker.ImagesSize != 1234 {
		t.Errorf("system df not merged: %+v", s.Runtime.Docker)
	}
	if !s.Host.ProcAvailable || s.Host.Threads != 8 || s.Host.Cores != 4 || s.Host.Kernel != "6.8.0-45-generic" {
		t.Errorf("host = %+v", s.Host)
	}
	if !near(s.Host.CPUPercent, 400.0/700*100) {
		t.Errorf("cpu = %v", s.Host.CPUPercent)
	}
	if !near(s.Host.Memory.Percent, 25) {
		t.Errorf("memory = %+v", s.Host.Memory)
	}
	if len(s.Host.Disks) == 0 || s.Host.Disks[0].Roles[0] != "data" {
		t.Errorf("disks = %+v, want the data dir first", s.Host.Disks)
	}
	if s.Totals != (ContainerTotals{Total: 4, Running: 3}) {
		t.Errorf("totals = %+v", s.Totals)
	}
	if len(s.Kinds) != 2 || s.Kinds[0].Kind != KindAgent || s.Kinds[0].Running != 2 || s.Kinds[0].Count != 3 ||
		!near(s.Kinds[0].CPUCores, 1) || s.Kinds[1].Kind != KindSandbox {
		t.Errorf("kinds = %+v", s.Kinds)
	}
	if s.Octopus.ActiveRunsTotal != 2 || len(s.Octopus.ActiveRuns) != 1 || s.Octopus.ActiveRuns[0].Count != 2 {
		t.Errorf("active runs = %+v", s.Octopus.ActiveRuns)
	}
	limits := map[string]Limit{}
	for _, l := range s.Octopus.Limits {
		limits[l.Key] = l
	}
	if limits[limitAgents] != (Limit{Key: limitAgents, Cap: 4, InUse: 2}) ||
		limits[limitMentions] != (Limit{Key: limitMentions, Cap: 2, InUse: 1}) ||
		limits[limitSandbox] != (Limit{Key: limitSandbox, Cap: 5, InUse: 1}) ||
		limits[limitChats] != (Limit{Key: limitChats, Cap: 3, InUse: 0}) {
		t.Errorf("limits = %+v", s.Octopus.Limits)
	}
	if s.Octopus.Queue.Agent.Waiting != 3 {
		t.Errorf("queue = %+v", s.Octopus.Queue)
	}
	if len(s.Octopus.Workspaces) != 2 || s.Octopus.Workspaces[0].ID != "ws1" ||
		s.Octopus.Workspaces[0].RunningAgents != 2 || s.Octopus.Workspaces[0].Plan != "team-ws1" {
		t.Errorf("workspaces = %+v", s.Octopus.Workspaces)
	}
	if s.Storage.Total != 64 {
		t.Errorf("storage = %+v", s.Storage)
	}

	h := c.History(time.Hour)
	if len(h) != 2 || h[1].Running != 3 || h[1].ActiveRuns != 2 || !h[1].T.Equal(now) {
		t.Errorf("history = %+v", h)
	}
	if got := c.History(10 * time.Second); len(got) != 1 {
		t.Errorf("a 10s window holds %d points, want the latest only", len(got))
	}
	if cs := c.Containers(); len(cs) != 4 || cs[0].Kind != KindAgent || cs[3].Name != "old" {
		t.Errorf("containers = %+v, want running first and the stopped one last", cs)
	}
}

func TestCollectorWithoutSources(t *testing.T) {
	c := New(Config{ProcRoot: filepath.Join(t.TempDir(), "absent")})
	c.tick(context.Background())
	waitSlow(t, c)
	s := c.Overview()
	if s.Runtime.Mode != "none" || s.Host.ProcAvailable || s.Totals.Total != 0 {
		t.Errorf("snapshot = %+v", s)
	}
	if s.Host.Threads == 0 {
		t.Error("thread count should fall back to the Go runtime's")
	}
	if len(c.History(time.Hour)) != 1 {
		t.Error("a sample without sources still records a point")
	}
}

func TestWorkspaceNamesResolveSanitizedIDs(t *testing.T) {
	n := workspaceNames{"A1B2c3d4_e5": "Acme"}
	if id, name := n.resolve("A1B2c3d4_e5"); id != "A1B2c3d4_e5" || name != "Acme" {
		t.Errorf("raw id = %q %q", id, name)
	}
	if id, name := n.resolve("a1b2c3d4-e5"); id != "A1B2c3d4_e5" || name != "Acme" {
		t.Errorf("sanitized id = %q %q", id, name)
	}
	if id, name := n.resolve("gone"); id != "gone" || name != "" {
		t.Errorf("unknown id = %q %q", id, name)
	}
}

type brokenSettings struct{ fakeSettings }

func (brokenSettings) SandboxMax(context.Context) (int, error) { return 0, errors.New("db down") }

func TestLimitsReportUnreadableSettings(t *testing.T) {
	c := New(Config{Settings: brokenSettings{fakeSettings{agents: 4, chats: 2}}})
	limits, probs := c.limits(context.Background(), Admission{}, runningAgg{byKind: map[Kind]int{KindSandbox: 1}})
	if limits[0].Cap != 4 || limits[3].Cap != 2 || limits[2].InUse != 1 {
		t.Errorf("limits = %+v", limits)
	}
	if len(probs) != 1 || probs[0].Source != "settings" {
		t.Errorf("problems = %+v, want the sandbox.max read failure", probs)
	}
}

func TestLimitsCountWhatIsRunning(t *testing.T) {
	cs := []Container{
		{Kind: KindAgent, Stage: "in_progress", running: true},
		{Kind: KindAgent, Stage: "review", running: true},
		{Kind: KindAgent, Stage: "mention", running: true},
		{Kind: KindSandbox, running: true},
	}
	c := New(Config{SandboxesHeld: func(context.Context) (int, error) { return 2, nil }})
	limits, probs := c.limits(context.Background(), Admission{Agent: Gate{Inflight: 1}}, aggregateRunning(cs))
	if len(probs) != 0 {
		t.Fatalf("problems = %+v", probs)
	}
	if got := limits[0].InUse; got != 2 {
		t.Errorf("agents in use = %d, want the 2 stage containers", got)
	}
	if got := limits[1].InUse; got != 1 {
		t.Errorf("mentions in use = %d, want 1", got)
	}
	if got := limits[2].InUse; got != 2 {
		t.Errorf("sandboxes in use = %d, want the 2 slots held", got)
	}

	c = New(Config{SandboxesHeld: func(context.Context) (int, error) { return 0, errors.New("db down") }})
	limits, probs = c.limits(context.Background(), Admission{Agent: Gate{Inflight: 3}}, aggregateRunning(cs))
	if limits[0].InUse != 3 || limits[2].InUse != 1 || len(probs) != 1 {
		t.Errorf("limits = %+v, problems = %+v", limits, probs)
	}
}

func TestNetDevPrefersTheHostsInit(t *testing.T) {
	proc := copyProc(t)
	c := New(Config{ProcRoot: proc})
	if _, hostNet, err := c.netDev(); err != nil || hostNet {
		t.Fatalf("netDev without 1/net/dev = host %v, %v; want the fallback", hostNet, err)
	}
	b, err := os.ReadFile(filepath.Join(proc, "net", "dev"))
	if err != nil {
		t.Fatal(err)
	}
	writeSized(t, filepath.Join(proc, "1", "net", "dev"), 0)
	if err := os.WriteFile(filepath.Join(proc, "1", "net", "dev"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, hostNet, err := c.netDev(); err != nil || !hostNet {
		t.Errorf("netDev with 1/net/dev = host %v, %v; want the host's counters", hostNet, err)
	}
}
