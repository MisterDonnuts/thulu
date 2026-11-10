package task

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type recordingLauncher struct {
	mu     sync.Mutex
	envs   []map[string]string
	code   []map[string]string
	ro     []bool
	exits  map[string]func(int, string, string)
	alive  map[string]bool
	nextID int
}

func newRecordingLauncher() *recordingLauncher {
	return &recordingLauncher{exits: map[string]func(int, string, string){}, alive: map[string]bool{}}
}

func (l *recordingLauncher) LaunchForStage(
	_ context.Context, _ *Task, _ Stage, _ *types.AgentType,
	env map[string]string, worktrees, _ map[string]string, readOnly bool,
) (string, string, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextID++
	id := "ctr-" + string(rune('a'+l.nextID-1))
	l.envs = append(l.envs, env)
	l.code = append(l.code, worktrees)
	l.ro = append(l.ro, readOnly)
	l.alive[id] = true
	return id, env["INSTANCE_ID"], "", nil
}

func (l *recordingLauncher) launchedTasks() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.envs))
	for _, env := range l.envs {
		out = append(out, env["TASK_ID"])
	}
	return out
}

func (l *recordingLauncher) OnExit(_ context.Context, containerID string, cb func(int, string, string)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.exits[containerID] = cb
}

func (l *recordingLauncher) FindAgentType(string, string) *types.AgentType { return nil }

func (l *recordingLauncher) ContainerAlive(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.alive[id]
}

func (l *recordingLauncher) ScanExitedTaskAgents(context.Context) ([]ExitedTaskAgent, error) {
	return nil, nil
}

func (l *recordingLauncher) exit(t *testing.T, containerID string) {
	t.Helper()
	l.exitIn(t, containerID, "")
}

func (l *recordingLauncher) exitIn(t *testing.T, containerID, dir string) {
	t.Helper()
	l.mu.Lock()
	cb := l.exits[containerID]
	delete(l.alive, containerID)
	l.mu.Unlock()
	if cb == nil {
		t.Fatalf("no OnExit registered for %s", containerID)
	}
	cb(0, dir, "")
}

func (l *recordingLauncher) launches() []map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]map[string]string(nil), l.envs...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

func newMentionDispatcher(t *testing.T) (*Dispatcher, *Store, *recordingLauncher) {
	t.Helper()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	l := newRecordingLauncher()
	return NewDispatcher(store, l, nil, nil, nil), store, l
}

func mentionAgent() *types.AgentType {
	return &types.AgentType{
		Name: "devops", Type: "devops",
		ValidStages: []types.Stage{types.StageInProgress, types.StageMention},
	}
}

func TestMentionRunsAlongsideAStageRun(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "busy task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	running := StatusRunning
	runID := "ctr-stage"
	if _, err := store.Update(t.Context(), tk.ID, Patch{Status: &running, CurrentRunID: &runID}); err != nil {
		t.Fatalf("update: %v", err)
	}
	l.mu.Lock()
	l.alive[runID] = true
	l.mu.Unlock()
	full, _ := store.Get(t.Context(), tk.ID)

	d.HandleMention(context.Background(), full, mentionAgent(), &Comment{ID: "c1", Body: "how is it going?"})

	if got := len(l.launches()); got != 1 {
		t.Fatalf("launched %d mention runs while a stage run was alive, want 1", got)
	}
}

func TestRepeatedMentionsAnswerOnceWithEverythingSaid(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "chatty task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full, _ := store.Get(t.Context(), tk.ID)
	at := mentionAgent()

	d.HandleMention(context.Background(), full, at, &Comment{ID: "c1", Body: "primera"})
	d.HandleMention(context.Background(), full, at, &Comment{ID: "c2", ParentID: "c1", Body: "segunda"})
	d.HandleMention(context.Background(), full, at, &Comment{ID: "c3", ParentID: "c1", Body: "tercera"})

	launched := l.launches()
	if len(launched) != 1 {
		t.Fatalf("launched %d containers for three mentions of one agent in one thread, want 1", len(launched))
	}
	if body := launched[0]["MENTION_COMMENT_BODY"]; body != "primera" {
		t.Errorf("first run answers %q, want the first message", body)
	}

	l.exit(t, "ctr-a")
	waitFor(t, func() bool { return len(l.launches()) == 2 })

	body := l.launches()[1]["MENTION_COMMENT_BODY"]
	for _, want := range []string{"segunda", "tercera"} {
		if !strings.Contains(body, want) {
			t.Errorf("follow-up body %q is missing %q", body, want)
		}
	}
}

func TestMentionRefusedForAnAgentThatDoesNotAttendThem(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full, _ := store.Get(t.Context(), tk.ID)
	at := &types.AgentType{Name: "tester", ValidStages: []types.Stage{types.StageTesting}}

	d.HandleMention(context.Background(), full, at, &Comment{ID: "c1", Body: "hola"})

	if got := len(l.launches()); got != 0 {
		t.Fatalf("launched %d runs for an agent that declares no mention stage", got)
	}
}

func requestContext(t *testing.T, acceptLanguage string) context.Context {
	t.Helper()
	var ctx context.Context
	h := i18n.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ctx = context.WithoutCancel(r.Context())
	}))
	req := httptest.NewRequest(http.MethodPost, "/tasks/t/comments", nil)
	req.Header.Set("Accept-Language", acceptLanguage)
	h.ServeHTTP(httptest.NewRecorder(), req)
	return ctx
}

func TestMentionRunSpeaksTheLanguageOfTheRequest(t *testing.T) {
	prev := i18n.DaemonLocale()
	i18n.SetDaemonLocale("en")
	t.Cleanup(func() { i18n.SetDaemonLocale(prev) })
	d, store, l := newMentionDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full, _ := store.Get(t.Context(), tk.ID)

	d.HandleMention(requestContext(t, "es-AR,es;q=0.9"), full, mentionAgent(),
		&Comment{ID: "c1", Body: "@devops run the cases"})
	l.exit(t, "ctr-a")
	d.HandleMention(context.Background(), full, mentionAgent(),
		&Comment{ID: "c2", Body: "@devops otra cosa"})

	launched := l.launches()
	if len(launched) != 2 {
		t.Fatalf("launched %d runs, want 2", len(launched))
	}
	if got := launched[0]["OCTOPUS_LOCALE"]; got != "es" {
		t.Errorf("mention from an es request: OCTOPUS_LOCALE = %q, want es", got)
	}
	if got := launched[1]["OCTOPUS_LOCALE"]; got != "en" {
		t.Errorf("mention with no request: OCTOPUS_LOCALE = %q, want the daemon's en", got)
	}
}
