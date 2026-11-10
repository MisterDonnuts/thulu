package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func postComment(t *testing.T, rig *runScopeRig, taskID string, body map[string]any) (task.Comment, int) {
	t.Helper()
	w := postJSONAuth(t, rig.h, "/tasks/"+taskID+"/comments", rig.token, body)
	var c task.Comment
	if w.Code == http.StatusCreated {
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return c, w.Code
}

func TestThreads_ReplyJoinsTheThread(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	root, code := postComment(t, rig, rig.a, map[string]any{"body": "why does login fail?"})
	if code != http.StatusCreated {
		t.Fatalf("root = %d", code)
	}
	reply, code := postComment(t, rig, rig.a, map[string]any{"body": "falta el header", "parent_id": root.ID})
	if code != http.StatusCreated || reply.ParentID != root.ID {
		t.Fatalf("reply = %d %+v", code, reply)
	}
	nested, _ := postComment(t, rig, rig.a, map[string]any{"body": "which one?", "parent_id": reply.ID})
	if nested.ParentID != root.ID {
		t.Errorf("reply to a reply hangs off %q, want the root %q", nested.ParentID, root.ID)
	}

	other, _ := postComment(t, rig, rig.b, map[string]any{"body": "otro tema"})
	before := len(commentsOf(t, rig, rig.a))
	for _, parent := range []string{other.ID, "nope"} {
		if _, code := postComment(t, rig, rig.a, map[string]any{"body": "x", "parent_id": parent}); code != http.StatusBadRequest {
			t.Errorf("parent %q = %d, want 400", parent, code)
		}
	}
	if after := len(commentsOf(t, rig, rig.a)); after != before {
		t.Errorf("a refused reply was stored: %d → %d comments", before, after)
	}

	w := getJSON(t, rig.h, "/tasks/"+rig.a, rig.token)
	var got struct {
		Comments []task.Comment `json:"comments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	threads := task.Threads(got.Comments)
	if len(threads) != 1 || len(threads[0].Replies) != 2 {
		t.Errorf("GET /tasks threads = %+v", threads)
	}
}

func TestThreads_BriefGroupsAndCaps(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	root, _ := postComment(t, rig, rig.a, map[string]any{"body": "tema largo"})
	for i := 1; i <= briefThreadReplies+3; i++ {
		postComment(t, rig, rig.a, map[string]any{"body": fmt.Sprintf("respuesta %02d", i), "parent_id": root.ID})
	}
	postComment(t, rig, rig.a, map[string]any{"body": "otro tema"})

	brief := getJSON(t, rig.h, "/tasks/"+rig.a+"/brief", rig.token).Body.String()
	if !strings.Contains(brief, "Thread `"+root.ID+"`") || !strings.Contains(brief, "3 earlier replies not shown") {
		t.Errorf("brief does not mark the thread and its cap:\n%s", brief)
	}
	if strings.Contains(brief, "respuesta 01") || !strings.Contains(brief, fmt.Sprintf("respuesta %02d", briefThreadReplies+3)) {
		t.Errorf("brief kept the wrong replies:\n%s", brief)
	}
	if strings.Index(brief, "respuesta 09") > strings.Index(brief, "otro tema") {
		t.Errorf("the thread is not under its root:\n%s", brief)
	}

	full := getJSON(t, rig.h, "/tasks/"+rig.a+"/brief?thread="+root.ID, rig.token).Body.String()
	if !strings.Contains(full, "respuesta 01") || strings.Contains(full, "not shown") {
		t.Errorf("?thread= does not show the whole thread:\n%s", full)
	}
}

func mentionAgentOnDisk(t *testing.T, srv *Server, name string) {
	t.Helper()
	agDir := filepath.Join(srv.agentsDir, "agents", name)
	if err := os.MkdirAll(agDir, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  agent:\n    image: octopus-docker\n    labels:\n" +
		"      thulu.agent: \"true\"\n      thulu.type: \"" + name + "\"\n      thulu.stages: \"mention\"\n"
	if err := os.WriteFile(filepath.Join(agDir, "docker-compose.yml"), []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestThreads_ReplyWithoutMentionGoesToTheLastAgent(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "thread-route@test.com", "TR", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "thread routing")
	mentionAgentOnDisk(t, srv, "probe-thread")
	ctx := t.Context()
	store := srv.taskStore
	root, _ := store.AddComment(ctx, taskID, task.SourceUser, "p@x", "why does it fail?")
	quiet, _ := store.AddComment(ctx, taskID, task.SourceUser, "p@x", "otro tema")
	_, _ = store.AddReply(ctx, taskID, root.ID, task.SourceAgent, "probe-thread", "falta el header")
	followUp, _ := store.AddReply(ctx, taskID, root.ID, task.SourceUser, "p@x", "which header?")
	humansOnly, _ := store.AddReply(ctx, taskID, quiet.ID, task.SourceUser, "p@x", "sigo yo")

	if got := srv.threadAgent(ctx, followUp); len(got) != 1 || got[0].Name != "probe-thread" {
		t.Errorf("reply in the agent's thread goes to %v, want probe-thread", got)
	}
	if got := srv.threadAgent(ctx, root); got != nil {
		t.Errorf("a root comment goes to %v, want nobody", got)
	}
	if got := srv.threadAgent(ctx, humansOnly); got != nil {
		t.Errorf("a thread with no agent goes to %v, want nobody", got)
	}
}

func TestThreads_ChatTurnCarriesTheThread(t *testing.T) {
	tk := &task.Task{ID: "t1", Title: "deploy", Stage: "in_progress"}
	ask := &task.Comment{ID: "c3", ParentID: "c1", Author: "p@x", Body: "@manager what now?"}
	msg := buildMentionChatMessage(t.Context(), tk, "In progress", []task.Comment{
		{ID: "c1", Author: "p@x", Body: "falla el deploy"},
		{ID: "c2", ParentID: "c1", Source: task.SourceAgent, Author: "devops", Body: "falta la variable"},
		*ask,
	}, ask)
	for _, want := range []string{"`t1`", "**Current stage:** In progress", "**@devops (agent):** falta la variable", "**p@x:** falla el deploy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("turn is missing %q:\n%s", want, msg)
		}
	}
	if n := strings.Count(msg, "what now?"); n != 1 {
		t.Errorf("the message it answers appears %d times:\n%s", n, msg)
	}

	es := buildMentionChatMessage(i18n.WithLocale(t.Context(), "es"), tk, "En curso", []task.Comment{
		{ID: "c2", ParentID: "c1", Source: task.SourceAgent, Author: "devops", Body: "falta la variable"},
	}, ask)
	for _, want := range []string{"Mención en la tarea «deploy»", "**Etapa actual:** En curso", "**@devops (agente):**"} {
		if !strings.Contains(es, want) {
			t.Errorf("a mention from a Spanish request is missing %q:\n%s", want, es)
		}
	}
	if strings.Contains(es, "c1") {
		t.Errorf("the brief carries the thread id:\n%s", es)
	}

	dev := &task.Comment{ID: "c4", ParentID: "c1", Source: task.SourceUser, Author: auth.DevModeAuthorName, Body: "@manager dale"}
	if got := buildMentionChatMessage(i18n.WithLocale(t.Context(), "es"), tk, "En curso", nil, dev); !strings.Contains(got, "**Comentario de quien opera esta instalación:**") {
		t.Errorf("dev-mode comment speaker:\n%s", got)
	}
}

func TestThreads_ApplyNeedsSomebodyToHandItTo(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	root, _ := postComment(t, rig, rig.a, map[string]any{"body": "do we cache the login?"})
	postComment(t, rig, rig.a, map[string]any{"body": "yes, 5 minutes", "parent_id": root.ID})
	path := "/tasks/" + rig.a + "/threads/" + root.ID + "/apply"

	if w := keyDo(t, rig.h, http.MethodPost, path, rig.keyA, map[string]any{}); w.Code != http.StatusForbidden {
		t.Errorf("a run key applied a thread: %d", w.Code)
	}
	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/threads/nope/apply", rig.token, map[string]any{}); w.Code != http.StatusNotFound {
		t.Errorf("unknown thread = %d, want 404", w.Code)
	}
	w := postJSONAuth(t, rig.h, path, rig.token, map[string]any{"note": "with a test"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "first agent reads it") {
		t.Fatalf("apply on a task that never ran = %d: %s", w.Code, w.Body.String())
	}
	if msgs, err := rig.srv.taskStore.ListMessages(t.Context(), rig.a); err != nil || len(msgs) != 0 {
		t.Errorf("conversation = %+v, %v; want nothing written", msgs, err)
	}
	comments := commentsOf(t, rig, rig.a)
	if last := comments[len(comments)-1]; last.Source == task.SourceSystem {
		t.Errorf("a note was left in the thread: %+v", last)
	}
}

type liveRunLauncher struct{ runID string }

func (l liveRunLauncher) LaunchForStage(context.Context, *task.Task, task.Stage, *types.AgentType,
	map[string]string, map[string]string, map[string]string, bool,
) (string, string, string, error) {
	return "", "", "", fmt.Errorf("liveRunLauncher launches nothing")
}
func (l liveRunLauncher) OnExit(context.Context, string, func(int, string, string)) {}
func (l liveRunLauncher) FindAgentType(string, string) *types.AgentType             { return nil }
func (l liveRunLauncher) ContainerAlive(id string) bool                             { return id == l.runID }
func (l liveRunLauncher) ScanExitedTaskAgents(context.Context) ([]task.ExitedTaskAgent, error) {
	return nil, nil
}

func TestThreads_ApplyHandsTheThreadToTheRun(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	ctx := t.Context()
	root, _ := postComment(t, rig, rig.a, map[string]any{"body": "do we cache the login?\nsupport asked for it"})
	postComment(t, rig, rig.a, map[string]any{"body": "yes, 5 minutes", "parent_id": root.ID})

	running, runID := task.StatusRunning, "ctr-live"
	if _, err := rig.srv.taskStore.Update(ctx, rig.a, task.Patch{
		Status: &running, CurrentRunID: &runID,
		StageAssignments: map[task.Stage]string{"backlog": "backend", "in_progress": "backend"},
	}); err != nil {
		t.Fatalf("seed the run: %v", err)
	}
	rig.srv.taskDispatcher = task.NewDispatcher(rig.srv.taskStore, liveRunLauncher{runID: runID},
		rig.srv.projectStore, nil, nil).WithBoards(rig.srv.boards)

	w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/threads/"+root.ID+"/apply", rig.token, map[string]any{"note": "with a test"})
	if w.Code != http.StatusCreated {
		t.Fatalf("apply = %d: %s", w.Code, w.Body.String())
	}

	msgs, err := rig.srv.taskStore.ListMessages(ctx, rig.a)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("conversation = %+v, %v", msgs, err)
	}
	for _, want := range []string{"do we cache the login?", "with a test", "yes, 5 minutes"} {
		if !strings.Contains(msgs[0].Body, want) {
			t.Errorf("the applied message is missing %q:\n%s", want, msgs[0].Body)
		}
	}
	comments := commentsOf(t, rig, rig.a)
	last := comments[len(comments)-1]
	if last.ParentID != root.ID || last.Source != task.SourceSystem || !strings.Contains(last.Body, "sent this thread") {
		t.Errorf("no note in the thread: %+v", last)
	}
	events, err := rig.srv.taskStore.ListEvents(ctx, rig.a)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var payload map[string]string
	for _, ev := range events {
		if ev.Kind == task.EventThreadApplied {
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("thread_applied payload %s: %v", ev.Payload, err)
			}
		}
	}
	if payload["thread_id"] != root.ID || payload["delivery"] != string(task.DeliveryQueued) || payload["agent"] == "" {
		t.Errorf("thread_applied payload = %v, want the thread, delivery %q and the agent", payload, task.DeliveryQueued)
	}
}

func TestThreads_ApplySaysTheSessionAgentIsGone(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	ctx := t.Context()
	rig.srv.agentSource = staticSource{{Name: "architect", Type: "architect"}}
	root, _ := postComment(t, rig, rig.a, map[string]any{"body": "do we cache the login?"})
	postComment(t, rig, rig.a, map[string]any{"body": "yes", "parent_id": root.ID})
	sid, agent, stage := "sess-1", "backend", "in_progress"
	if _, err := rig.srv.taskStore.Update(ctx, rig.a, task.Patch{
		SessionID: &sid, SessionAgent: &agent, SessionStage: &stage,
	}); err != nil {
		t.Fatalf("seed the session: %v", err)
	}

	w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/threads/"+root.ID+"/apply", rig.token, map[string]any{})
	body := w.Body.String()
	if w.Code != http.StatusConflict || !strings.Contains(body, "backend, the agent that worked on this task, no longer exists") {
		t.Fatalf("apply with the session's agent gone = %d: %s", w.Code, body)
	}
	if strings.Contains(body, "has not run yet") {
		t.Errorf("the route says the task never ran: %s", body)
	}
	if msgs, err := rig.srv.taskStore.ListMessages(ctx, rig.a); err != nil || len(msgs) != 0 {
		t.Errorf("conversation = %+v, %v; want nothing written", msgs, err)
	}
}

func TestThreads_ApplyMessageCarriesTheThread(t *testing.T) {
	th := task.Thread{
		Root:    task.Comment{ID: "c1", Author: "p@x", Body: "\ndo we cache the login?\nsupport asked for it"},
		Replies: []task.Comment{{ID: "c2", ParentID: "c1", Source: task.SourceAgent, Author: "architect", Body: "yes, 5 minutes"}},
	}
	msg := applyThreadMessage(t.Context(), th, "  with a test ")
	for _, want := range []string{`"do we cache the login?"`, "with a test", "yes, 5 minutes", "support asked for it"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the applied message is missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "`c1`") {
		t.Errorf("the applied message names the thread by its id:\n%s", msg)
	}
}

func TestThreads_ResolveFoldsTheThread(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	root, _ := postComment(t, rig, rig.a, map[string]any{"body": "which database do we use?"})
	reply, _ := postComment(t, rig, rig.a, map[string]any{"body": "la de siempre", "parent_id": root.ID})
	postComment(t, rig, rig.a, map[string]any{"body": "postgres, cerrado", "parent_id": root.ID})
	path := "/tasks/" + rig.a + "/threads/" + root.ID + "/resolve"
	resolve := map[string]any{"resolved": true}

	if w := keyDo(t, rig.h, http.MethodPost, path, rig.keyA, resolve); w.Code != http.StatusForbidden {
		t.Errorf("a run key resolved a thread: %d", w.Code)
	}
	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/threads/"+reply.ID+"/resolve", rig.token, resolve); w.Code != http.StatusNotFound {
		t.Errorf("resolving a reply = %d, want 404", w.Code)
	}
	w := postJSONAuth(t, rig.h, path, rig.token, resolve)
	var got task.Comment
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.ResolvedAt == nil || got.ResolvedBy == "" {
		t.Fatalf("resolve = %d %s", w.Code, w.Body.String())
	}

	brief := getJSON(t, rig.h, "/tasks/"+rig.a+"/brief", rig.token).Body.String()
	if !strings.Contains(brief, "resolved by") || strings.Contains(brief, "la de siempre") ||
		!strings.Contains(brief, "postgres, cerrado") {
		t.Errorf("the brief does not fold the resolved thread:\n%s", brief)
	}

	postComment(t, rig, rig.a, map[string]any{"body": "mejor lo revisamos", "parent_id": root.ID})
	for _, c := range commentsOf(t, rig, rig.a) {
		if c.ID == root.ID && c.ResolvedAt != nil {
			t.Error("a person's reply did not reopen the thread")
		}
	}
}

func TestThreads_AgentsCallEachOtherWithinALimit(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "agent-hops@test.com", "AH", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "agent hops")
	mentionAgentOnDisk(t, srv, "probe-a")
	mentionAgentOnDisk(t, srv, "probe-b")
	ctx := t.Context()
	store := srv.taskStore
	names := func(ats []*types.AgentType) []string {
		out := []string{}
		for _, at := range ats {
			out = append(out, at.Name)
		}
		return out
	}
	agentSays := func(root, who, body string) *task.Comment {
		c, err := store.AddReply(ctx, taskID, root, task.SourceAgent, who, body)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	root, _ := store.AddComment(ctx, taskID, task.SourceUser, "p@x", "@probe-a check the login")
	if got := names(srv.agentMentions(ctx, agentSays(root.ID, "probe-a", "@probe-a @probe-b what do you see?"))); len(got) != 1 || got[0] != "probe-b" {
		t.Errorf("turn 1 launches %v, want probe-b and not itself", got)
	}
	if got := srv.agentMentions(ctx, agentSays(root.ID, "probe-b", "@probe-a el header")); len(got) != 1 {
		t.Errorf("turn 2 launches %v, want probe-a", names(got))
	}
	if got := srv.agentMentions(ctx, agentSays(root.ID, "probe-a", "@probe-b sure?")); got != nil {
		t.Errorf("turn 3 launches %v, want nobody", names(got))
	}
	comments := commentsOf(t, &runScopeRig{srv: srv}, taskID)
	if last := comments[len(comments)-1]; last.Source != task.SourceSystem || !strings.Contains(last.Body, "@probe-b won't be launched") {
		t.Errorf("no notice of the limit: %+v", last)
	}

	_, _ = store.AddReply(ctx, taskID, root.ID, task.SourceUser, "p@x", "sigan")
	if got := srv.agentMentions(ctx, agentSays(root.ID, "probe-b", "@probe-a dale")); len(got) != 1 {
		t.Errorf("after a person wrote, %v launched, want probe-a", names(got))
	}
}
