package task

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"

	// blank import: registers claude-code so engineTakesStreamingInput can
	// answer for the default engine, as it does in the daemon.
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
)

type convLauncher struct {
	*recordingLauncher
	agent *types.AgentType

	mu      sync.Mutex
	steered [][]byte
	steerFn func() error
}

func (l *convLauncher) FindAgentType(string, string) *types.AgentType { return l.agent }

func (l *convLauncher) SteerRun(_ context.Context, _ string, payload []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.steerFn != nil {
		if err := l.steerFn(); err != nil {
			return err
		}
	}
	l.steered = append(l.steered, payload)
	return nil
}

func (l *convLauncher) steeredMessages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.steered))
	for _, p := range l.steered {
		out = append(out, string(p))
	}
	return out
}

func newConvDispatcher(t *testing.T) (*Dispatcher, *Store, *convLauncher) {
	t.Helper()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	l := &convLauncher{
		recordingLauncher: newRecordingLauncher(),
		agent: &types.AgentType{
			Name: "developer", Type: "developer",
			ValidStages: []types.Stage{types.StageInProgress},
		},
	}
	return NewDispatcher(store, l, nil, nil, nil), store, l
}

func liveRun(t *testing.T, store *Store, l *convLauncher, taskID, runID string) *Task {
	t.Helper()
	running := StatusRunning
	if _, err := store.Update(t.Context(), taskID, Patch{Status: &running, CurrentRunID: &runID}); err != nil {
		t.Fatalf("update: %v", err)
	}
	l.recordingLauncher.mu.Lock()
	l.recordingLauncher.alive[runID] = true
	l.recordingLauncher.mu.Unlock()
	full, err := store.Get(t.Context(), taskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return full
}

func withSession(t *testing.T, store *Store, taskID, sid, agent string, stage Stage) *Task {
	t.Helper()
	stageStr := string(stage)
	if _, err := store.Update(t.Context(), taskID, Patch{
		SessionID: &sid, SessionAgent: &agent, SessionStage: &stageStr,
	}); err != nil {
		t.Fatalf("update session: %v", err)
	}
	full, err := store.Get(t.Context(), taskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return full
}

func TestConverseSteersARunThatIsStillWorking(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full := liveRun(t, store, l, tk.ID, "ctr-live")
	d.steerable.Store("ctr-live", true)

	res, err := d.Converse(context.Background(), full, "use the -race flag", "you@example.com", true)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if res.Delivery != DeliverySteered {
		t.Fatalf("delivery = %q, want %q", res.Delivery, DeliverySteered)
	}
	msgs := l.steeredMessages()
	if len(msgs) != 1 {
		t.Fatalf("steered %d messages, want 1", len(msgs))
	}
	for _, want := range []string{`"text":"use the -race flag"`, `"interrupt":true`} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("payload %s is missing %s", msgs[0], want)
		}
	}
	msgsInChat, err := store.ListMessages(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgsInChat) != 1 || msgsInChat[0].Body != "use the -race flag" {
		t.Fatalf("chat = %+v, want the message recorded", msgsInChat)
	}
	if msgsInChat[0].Delivery != string(DeliverySteered) {
		t.Errorf("delivery on the row = %q, want %q", msgsInChat[0].Delivery, DeliverySteered)
	}
	comments, err := store.ListComments(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("comments: %v", err)
	}
	if len(comments) != 0 {
		t.Fatalf("comments = %+v, want the thread left alone", comments)
	}
}

func TestConverseQueuesWhenTheRunCannotBeReachedAndDeliversOnExit(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageInProgress}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	liveRun(t, store, l, tk.ID, "ctr-a")
	full := withSession(t, store, tk.ID, "sess-1", "developer", types.StageInProgress)

	res, err := d.Converse(context.Background(), full, "rename the field", "you", false)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if res.Delivery != DeliveryQueued {
		t.Fatalf("delivery = %q, want %q", res.Delivery, DeliveryQueued)
	}
	if got := len(l.launches()); got != 0 {
		t.Fatalf("launched %d runs while one was alive, want 0", got)
	}

	l.recordingLauncher.mu.Lock()
	cb := l.recordingLauncher.exits["ctr-a"]
	delete(l.recordingLauncher.alive, "ctr-a")
	l.recordingLauncher.mu.Unlock()
	if cb == nil {
		d.postStageRun(context.Background(), tk.ID, types.StageInProgress, "developer",
			"ctr-a", "inst-a", "", 0, postRunOpts{})
	}
	waitFor(t, func() bool { return len(l.launches()) == 1 })

	env := l.launches()[0]
	if env["SESSION_ID"] != "sess-1" {
		t.Errorf("SESSION_ID = %q, want the session the run left behind", env["SESSION_ID"])
	}
	if !strings.Contains(env["RESUME_MESSAGE"], "rename the field") {
		t.Errorf("RESUME_MESSAGE = %q, want the queued message", env["RESUME_MESSAGE"])
	}
}

func TestConverseResumesTheLastSessionWhenNothingIsRunning(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task", Stage: types.StageTesting}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full := withSession(t, store, tk.ID, "sess-9", "developer", types.StageInProgress)

	res, err := d.Converse(context.Background(), full, "remove the console.log", "you", false)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if res.Delivery != DeliveryContinuation {
		t.Fatalf("delivery = %q, want %q", res.Delivery, DeliveryContinuation)
	}
	waitFor(t, func() bool { return len(l.launches()) == 1 })
	env := l.launches()[0]
	if env["SESSION_ID"] != "sess-9" {
		t.Errorf("SESSION_ID = %q, want sess-9", env["SESSION_ID"])
	}
	if env["STAGE"] != string(types.StageInProgress) {
		t.Errorf("STAGE = %q, want the session's stage", env["STAGE"])
	}
	if _, ok := env["AUDIT_ONLY"]; ok {
		t.Errorf("AUDIT_ONLY = %q: an agent container never carries the auditor", env["AUDIT_ONLY"])
	}
	if env["STEER_ENABLED"] != "1" {
		t.Errorf("STEER_ENABLED = %q, want the continuation to be reachable too", env["STEER_ENABLED"])
	}
}

func TestConverseWithNothingToResumeKeepsItInTheChat(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "fresh task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full, _ := store.Get(t.Context(), tk.ID)

	res, err := d.Converse(context.Background(), full, "start with the parser", "you", false)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if res.Delivery != DeliveryUnreachable {
		t.Fatalf("delivery = %q, want %q", res.Delivery, DeliveryUnreachable)
	}
	if got := len(l.launches()); got != 0 {
		t.Fatalf("launched %d runs for a task with no session, want 0", got)
	}
	msgsInChat, _ := store.ListMessages(t.Context(), tk.ID)
	if len(msgsInChat) != 1 {
		t.Fatalf("chat = %d messages, want the message recorded", len(msgsInChat))
	}
	comments, _ := store.ListComments(t.Context(), tk.ID)
	if len(comments) != 0 {
		t.Fatalf("comments = %d, want nothing written to the thread", len(comments))
	}
	evs, _ := store.ListEvents(t.Context(), tk.ID)
	var p map[string]string
	for _, e := range evs {
		if e.Kind == EventConversationMessage {
			_ = json.Unmarshal(e.Payload, &p)
		}
	}
	if p["delivery"] != string(DeliveryUnreachable) || p["line"] != "start with the parser" {
		t.Errorf("event payload = %v, want the delivery and the message's first line", p)
	}
}

func TestConversationQueueKeepsTheNewestMessages(t *testing.T) {
	l := newConvLane()
	for _, m := range []string{"uno", "dos", "tres", "cuatro", "cinco", "seis"} {
		l.add("t1", m)
	}
	got := l.drain("t1")
	if strings.Contains(got, "uno") {
		t.Errorf("drained %q, want the oldest message dropped past the cap", got)
	}
	if !strings.Contains(got, "seis") || !strings.Contains(got, "dos") {
		t.Errorf("drained %q, want the newest five messages", got)
	}
	if again := l.drain("t1"); again != "" {
		t.Errorf("drained twice: %q, want the queue emptied", again)
	}
}

func TestCaptureSessionReadsTheRunsSessionFile(t *testing.T) {
	d, store, _ := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".session_id"), []byte("sess-from-file\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	d.captureSession(t.Context(), tk.ID, "", types.StageInProgress, "developer", dir, "ctr-x")

	got, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != "sess-from-file" {
		t.Errorf("SessionID = %q, want sess-from-file", got.SessionID)
	}
	if got.SessionAgent != "developer" || got.SessionStage != string(types.StageInProgress) {
		t.Errorf("session owner = %s/%s, want developer/%s", got.SessionAgent, got.SessionStage, types.StageInProgress)
	}
}

func TestApplyConversationEnvArmsSteering(t *testing.T) {
	env := map[string]string{}
	applyConversationEnv(env, &types.AgentType{Name: "developer"}, launchOpts{
		Session: "sess-2", Message: "dale de nuevo",
	}, true)
	if env["SESSION_ID"] != "sess-2" || env["RESUME_MESSAGE"] != "dale de nuevo" {
		t.Errorf("env = %v, want the session and its message", env)
	}
	if env["STEER_ENABLED"] != "1" {
		t.Errorf("STEER_ENABLED = %q, want 1 for an engine with streaming input", env["STEER_ENABLED"])
	}
}

func TestApplyConversationEnvNeedsASteeringLauncher(t *testing.T) {
	env := map[string]string{}
	applyConversationEnv(env, &types.AgentType{Name: "developer"}, launchOpts{Message: "continue"}, false)
	if _, armed := env["STEER_ENABLED"]; armed {
		t.Errorf("STEER_ENABLED = %q on a launcher without SteerRun", env["STEER_ENABLED"])
	}
	if env["RESUME_MESSAGE"] != "continue" {
		t.Errorf("RESUME_MESSAGE = %q, want the message regardless", env["RESUME_MESSAGE"])
	}
}

func TestConverseTargetNamesAGoneSessionAgent(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	fresh, _ := store.Get(t.Context(), tk.ID)
	if agent, gone, ok := d.ConverseTarget(fresh); ok || agent != "" || gone != "" {
		t.Errorf("never ran = (%q, %q, %v), want nobody and nothing gone", agent, gone, ok)
	}
	ran := withSession(t, store, tk.ID, "sess-1", "developer", types.StageInProgress)
	if agent, gone, ok := d.ConverseTarget(ran); !ok || agent != "developer" || gone != "" {
		t.Errorf("resumable = (%q, %q, %v), want developer", agent, gone, ok)
	}
	l.agent = nil
	if agent, gone, ok := d.ConverseTarget(ran); ok || agent != "" || gone != "developer" {
		t.Errorf("agent deleted = (%q, %q, %v), want gone developer", agent, gone, ok)
	}
}
