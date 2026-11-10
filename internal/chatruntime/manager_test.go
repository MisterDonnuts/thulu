package chatruntime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"

	// Register the claude-code engine for ParseStream/AuthEnv.
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/opencode"
)

type fakeConn struct {
	io.Reader
	wrote bytes.Buffer
}

func (c *fakeConn) Write(p []byte) (int, error)      { return c.wrote.Write(p) }
func (c *fakeConn) Close() error                     { return nil }
func (c *fakeConn) CloseWrite() error                { return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return nil }
func (c *fakeConn) RemoteAddr() net.Addr             { return nil }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeDocker struct {
	mu       sync.Mutex
	running  map[string]bool
	removed  []string
	stdout   string
	execErr  error
	exitCode int
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{running: map[string]bool{}}
}

func (d *fakeDocker) setRunning(id string, v bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running[id] = v
}

func (d *fakeDocker) ContainerInspect(_ context.Context, id string) (dockertypes.ContainerJSON, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	running, ok := d.running[id]
	if !ok {
		return dockertypes.ContainerJSON{}, errors.New("Error: No such container: " + id)
	}
	return dockertypes.ContainerJSON{
		ContainerJSONBase: &dockertypes.ContainerJSONBase{
			State: &dockertypes.ContainerState{Running: running},
		},
	}, nil
}

func (d *fakeDocker) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.running, id)
	d.removed = append(d.removed, id)
	return nil
}

func (d *fakeDocker) ContainerList(_ context.Context, _ container.ListOptions) ([]dockertypes.Container, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []dockertypes.Container
	for id := range d.running {
		out = append(out, dockertypes.Container{ID: id})
	}
	return out, nil
}

func (d *fakeDocker) ContainerExecCreate(_ context.Context, id string, _ container.ExecOptions) (dockertypes.IDResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.execErr != nil {
		err := d.execErr
		d.execErr = nil
		return dockertypes.IDResponse{}, err
	}
	if !d.running[id] {
		return dockertypes.IDResponse{}, fmt.Errorf("Error response from daemon: container %s is not running", id)
	}
	return dockertypes.IDResponse{ID: "exec-" + id}, nil
}

func (d *fakeDocker) ContainerExecAttach(_ context.Context, _ string, _ container.ExecStartOptions) (dockertypes.HijackedResponse, error) {
	var framed bytes.Buffer
	w := stdcopy.NewStdWriter(&framed, stdcopy.Stdout)
	d.mu.Lock()
	_, _ = w.Write([]byte(d.stdout))
	d.mu.Unlock()
	conn := &fakeConn{Reader: &framed}
	return dockertypes.HijackedResponse{Conn: conn, Reader: bufio.NewReader(&framed)}, nil
}

func (d *fakeDocker) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return container.ExecInspect{ExitCode: d.exitCode}, nil
}

type rig struct {
	mgr     *Manager
	store   *Store
	chats   *chat.Store
	docker  *fakeDocker
	dataDir string

	mu       sync.Mutex
	launched int
	settings *settings.Store
	last     types.LaunchParams
}

const okStream = `{"type":"system","subtype":"init","model":"opus","session_id":"sess-1"}
{"type":"result","result":"hola!","is_error":false,"usage":{"input_tokens":42},"session_id":"sess-1"}
`

func newRig(t *testing.T, maxContainers int) *rig {
	t.Helper()
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	chats, err := chat.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := sett.PutJSON(t.Context(), settings.ChatMaxKey, maxContainers); err != nil {
		t.Fatal(err)
	}

	r := &rig{store: st, chats: chats, docker: newFakeDocker(), dataDir: t.TempDir()}
	r.docker.stdout = okStream
	r.settings = sett
	launch := func(_ context.Context, _ types.AgentType, p types.LaunchParams) (string, error) {
		r.mu.Lock()
		r.last = p
		r.launched++
		id := fmt.Sprintf("ctr-%d", r.launched)
		r.mu.Unlock()
		r.docker.setRunning(id, true)
		return id, nil
	}
	r.mgr = newManagerForTest(st, sett, r.docker, ManagerConfig{
		DataDir: r.dataDir,
		Launch:  launch,
	})
	return r
}

func (r *rig) launchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.launched
}

func testAgent() *types.AgentType {
	return &types.AgentType{
		Name: "manager", Type: "manager", Dir: "/tmp/agents/manager",
		ValidStages: []types.Stage{types.StageChat},
		Model:       "opus",
	}
}

func testChat(id string) *chat.Chat {
	return &chat.Chat{ID: id, WorkspaceID: "ws-1", AgentType: "manager"}
}

func TestExecTurn_CreatesContainerAndParsesResult(t *testing.T) {
	r := newRig(t, 5)
	c := testChat("chat-a1")

	res, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "hola")
	if err != nil {
		t.Fatalf("ExecTurn: %v", err)
	}
	if res.Text != "hola!" {
		t.Errorf("Text: got %q", res.Text)
	}
	if res.SessionID != "sess-1" {
		t.Errorf("SessionID: got %q", res.SessionID)
	}
	if res.InputTokens != 42 {
		t.Errorf("InputTokens: got %d", res.InputTokens)
	}
	if r.launchCount() != 1 {
		t.Errorf("expected 1 launch, got %d", r.launchCount())
	}

	if _, err := os.Stat(filepath.Join(r.dataDir, "chats", c.ID, "claude")); err != nil {
		t.Errorf("state dir missing: %v", err)
	}

	if _, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "otra"); err != nil {
		t.Fatalf("second ExecTurn: %v", err)
	}
	if r.launchCount() != 1 {
		t.Errorf("warm container should be reused; launches=%d", r.launchCount())
	}
}

func TestExecTurn_CollectsToolCalls(t *testing.T) {
	r := newRig(t, 5)
	r.docker.stdout = `{"type":"system","subtype":"init","model":"opus","session_id":"sess-1"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"src/app.ts"}},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"npm test"}}]}}
{"type":"result","result":"listo","is_error":false,"session_id":"sess-1"}
`
	res, err := r.mgr.ExecTurn(context.Background(), testChat("chat-tools"), testAgent(), AcquireOpts{}, "try it")
	if err != nil {
		t.Fatalf("ExecTurn: %v", err)
	}
	want := []chat.ToolCall{{Name: "Read", Hint: "src/app.ts"}, {Name: "Bash", Hint: "npm test"}}
	if len(res.Tools) != len(want) {
		t.Fatalf("Tools = %+v, want %+v", res.Tools, want)
	}
	for i := range want {
		if res.Tools[i] != want[i] {
			t.Errorf("Tools[%d] = %+v, want %+v", i, res.Tools[i], want[i])
		}
	}
}

func TestExecTurn_SelfHealsDeadContainer(t *testing.T) {
	r := newRig(t, 5)
	c := testChat("chat-b2")

	if _, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatal(err)
	}
	r.docker.setRunning("ctr-1", false)

	res, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "seguimos?")
	if err != nil {
		t.Fatalf("ExecTurn after kill: %v", err)
	}
	if res.Text != "hola!" {
		t.Errorf("Text: got %q", res.Text)
	}
	if r.launchCount() != 2 {
		t.Errorf("expected recreate (2 launches), got %d", r.launchCount())
	}
}

func TestExecTurn_SelfHealsExecRace(t *testing.T) {
	r := newRig(t, 5)
	c := testChat("chat-b3")

	if _, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatal(err)
	}
	r.docker.execErr = errors.New("Error response from daemon: container ctr-1 is not running")

	if _, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "retry"); err != nil {
		t.Fatalf("ExecTurn should self-heal the exec race: %v", err)
	}
	if r.launchCount() != 2 {
		t.Errorf("expected recreate, launches=%d", r.launchCount())
	}
}

func TestExecTurn_ErrorResult(t *testing.T) {
	r := newRig(t, 5)
	r.docker.stdout = `{"type":"result","result":"boom","is_error":true}` + "\n"
	c := testChat("chat-c3")

	_, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "hola")
	if err == nil {
		t.Fatal("expected error from is_error result")
	}
}

func TestSingleFlight(t *testing.T) {
	r := newRig(t, 5)
	if err := r.mgr.BeginTurn("chat-x"); err != nil {
		t.Fatal(err)
	}
	if err := r.mgr.BeginTurn("chat-x"); !errors.Is(err, ErrTurnInFlight) {
		t.Errorf("expected ErrTurnInFlight, got %v", err)
	}
	if !r.mgr.TurnInFlight("chat-x") {
		t.Error("TurnInFlight should report true")
	}
	r.mgr.EndTurn("chat-x")
	if err := r.mgr.BeginTurn("chat-x"); err != nil {
		t.Errorf("BeginTurn after EndTurn: %v", err)
	}
}

func TestCapacity_EvictsOldestIdle(t *testing.T) {
	r := newRig(t, 1)

	a, b := testChat("chat-d4"), testChat("chat-e5")
	if _, err := r.mgr.ExecTurn(context.Background(), a, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.mgr.ExecTurn(context.Background(), b, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatalf("ExecTurn for second chat: %v", err)
	}
	rt, err := r.store.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Status != StatusEvicted {
		t.Errorf("chat A should be evicted, status=%s", rt.Status)
	}
	if n, _ := r.store.CountLive(); n != 1 {
		t.Errorf("live count: got %d, want 1", n)
	}
}

func TestCapacity_RefusesWhenAllInFlight(t *testing.T) {
	r := newRig(t, 1)

	a, b := testChat("chat-f6"), testChat("chat-g7")
	if _, err := r.mgr.ExecTurn(context.Background(), a, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatal(err)
	}
	if err := r.mgr.BeginTurn(a.ID); err != nil {
		t.Fatal(err)
	}
	defer r.mgr.EndTurn(a.ID)

	_, err := r.mgr.Acquire(context.Background(), b, testAgent(), AcquireOpts{})
	if !errors.Is(err, ErrNoCapacity) {
		t.Errorf("expected ErrNoCapacity, got %v", err)
	}
}

func TestWorkspaceLimit_EvictsWithinWorkspace(t *testing.T) {
	r := newRig(t, 10)
	r.mgr.workspaceLimit = func(string) int { return 1 }

	a, b := testChat("chat-h8"), testChat("chat-i9")
	if _, err := r.mgr.Acquire(context.Background(), a, testAgent(), AcquireOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.mgr.Acquire(context.Background(), b, testAgent(), AcquireOpts{}); err != nil {
		t.Fatalf("Acquire for second chat in same workspace: %v", err)
	}
	rtA, err := r.store.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rtA.Status != StatusEvicted {
		t.Errorf("chat A should be evicted, status=%s", rtA.Status)
	}
	if n, _ := r.store.CountLiveForWorkspace("ws-1"); n != 1 {
		t.Errorf("workspace live count: got %d, want 1", n)
	}
}

func TestWorkspaceLimit_RefusesWhenAllInFlight(t *testing.T) {
	r := newRig(t, 10)
	r.mgr.workspaceLimit = func(string) int { return 1 }

	a, b := testChat("chat-h8b"), testChat("chat-i9b")
	if _, err := r.mgr.Acquire(context.Background(), a, testAgent(), AcquireOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := r.mgr.BeginTurn(a.ID); err != nil {
		t.Fatal(err)
	}
	defer r.mgr.EndTurn(a.ID)

	_, err := r.mgr.Acquire(context.Background(), b, testAgent(), AcquireOpts{})
	if !IsWorkspaceAtCap(err) {
		t.Errorf("expected ErrWorkspaceAtCap, got %v", err)
	}
}

func TestWorkspaceLimit_DoesNotEvictOtherWorkspaces(t *testing.T) {
	r := newRig(t, 10)
	r.mgr.workspaceLimit = func(string) int { return 1 }

	other := &chat.Chat{ID: "chat-other", WorkspaceID: "ws-other", AgentType: "manager"}
	if _, err := r.mgr.Acquire(context.Background(), other, testAgent(), AcquireOpts{}); err != nil {
		t.Fatal(err)
	}
	a := testChat("chat-h8c")
	if _, err := r.mgr.Acquire(context.Background(), a, testAgent(), AcquireOpts{}); err != nil {
		t.Fatal(err)
	}
	b := testChat("chat-i9c")
	if _, err := r.mgr.Acquire(context.Background(), b, testAgent(), AcquireOpts{}); err != nil {
		t.Fatalf("Acquire in ws-1: %v", err)
	}
	rtOther, err := r.store.Get(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rtOther.Status == StatusEvicted {
		t.Errorf("other workspace chat must not be evicted by ws-1 pressure")
	}
}

func TestReapIdle(t *testing.T) {
	r := newRig(t, 5)
	c := testChat("chat-j1")
	if _, err := r.mgr.Acquire(context.Background(), c, testAgent(), AcquireOpts{}); err != nil {
		t.Fatal(err)
	}
	rt, _ := r.store.Get(c.ID)
	rt.LastActivityAt = time.Now().Add(-2 * time.Hour)
	if err := r.store.Upsert(rt); err != nil {
		t.Fatal(err)
	}

	r.mgr.ReapIdle(context.Background())

	got, _ := r.store.Get(c.ID)
	if got.Status != StatusEvicted {
		t.Errorf("idle container should be evicted, status=%s", got.Status)
	}
}

func TestReconcile(t *testing.T) {
	r := newRig(t, 5)
	c := testChat("chat-k2")
	if _, err := r.mgr.Acquire(context.Background(), c, testAgent(), AcquireOpts{}); err != nil {
		t.Fatal(err)
	}
	r.docker.setRunning("ctr-1", false)
	delete(r.docker.running, "ctr-1")

	r.mgr.Reconcile(context.Background())

	got, _ := r.store.Get(c.ID)
	if got.Status != StatusEvicted {
		t.Errorf("vanished container should be marked released, status=%s", got.Status)
	}
}

func TestEvictAndDropState(t *testing.T) {
	r := newRig(t, 5)
	c := testChat("chat-l3")
	if _, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(r.dataDir, "chats", c.ID)
	if _, err := os.Stat(stateDir); err != nil {
		t.Fatal("state dir should exist")
	}

	if err := r.mgr.DropState(context.Background(), c.ID); err != nil {
		t.Fatalf("DropState: %v", err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Error("state dir should be removed")
	}
	if _, err := r.store.Get(c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("runtime row should be deleted, got %v", err)
	}

	if err := r.mgr.Evict(context.Background(), c.ID); err != nil {
		t.Errorf("Evict after delete should be nil, got %v", err)
	}
}

func TestDeleteChatRemovesRawLog(t *testing.T) {
	r := newRig(t, 5)
	agentDir := t.TempDir()
	at := testAgent()
	at.Dir = agentDir
	svc := NewService(r.chats, r.mgr, chat.NewBus(), "", nil).
		WithAgentSource(stubSource{all: []types.AgentType{*at}})

	c := testChat("chat-rawlog")
	if err := r.chats.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	runs := filepath.Join(agentDir, "runs")
	if err := os.MkdirAll(runs, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{c.ID + ".jsonl", "chat-other.jsonl"} {
		if err := os.WriteFile(filepath.Join(runs, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.DeleteChat(t.Context(), c.ID); err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runs, c.ID+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("raw log survived the delete (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(runs, "chat-other.jsonl")); err != nil {
		t.Errorf("another chat's raw log was removed: %v", err)
	}
}
