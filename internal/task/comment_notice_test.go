package task

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"

	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
)

func postComment(t *testing.T, store *Store, taskID, author, body string) *Comment {
	t.Helper()
	c, err := store.AddComment(t.Context(), taskID, SourceUser, author, body)
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	return c
}

func TestNotifyCommentSteersTheRunAndLeavesTheCommentVerbatim(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full := liveRun(t, store, l, tk.ID, "ctr-live")
	d.steerable.Store("ctr-live", true)

	c := postComment(t, store, tk.ID, "ana", "the new endpoint returns 500 with an empty body")
	res, err := d.NotifyComment(context.Background(), full, c)
	if err != nil {
		t.Fatalf("NotifyComment: %v", err)
	}
	if res.Delivery != DeliverySteered {
		t.Fatalf("delivery = %q, want %q", res.Delivery, DeliverySteered)
	}

	msgs := l.steeredMessages()
	if len(msgs) != 1 {
		t.Fatalf("steered %d payloads, want 1", len(msgs))
	}
	var sent struct {
		Kind      string `json:"kind"`
		Text      string `json:"text"`
		Author    string `json:"author"`
		Interrupt bool   `json:"interrupt"`
	}
	if err := json.Unmarshal([]byte(msgs[0]), &sent); err != nil {
		t.Fatalf("payload %s is not JSON: %v", msgs[0], err)
	}
	for _, want := range []string{
		"New comment from ana on the task",
		"> the new endpoint returns 500 with an empty body",
	} {
		if !strings.Contains(sent.Text, want) {
			t.Errorf("text %q is missing %q", sent.Text, want)
		}
	}
	if sent.Kind != "comment" {
		t.Errorf("kind = %q, want \"comment\" so the queue file says what this is", sent.Kind)
	}
	if sent.Interrupt {
		t.Error("the payload interrupts the run; a comment must not")
	}

	comments, err := store.ListComments(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Body != "the new endpoint returns 500 with an empty body" {
		t.Fatalf("thread = %+v, want the comment exactly as typed", comments)
	}

	chat, err := store.ListMessages(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(chat) != 1 {
		t.Fatalf("chat has %d messages, want 1", len(chat))
	}
	if !strings.HasPrefix(chat[0].Body, "**New comment from ana on the task:**") {
		t.Errorf("chat body = %q, want the framed notice", chat[0].Body)
	}
	if chat[0].Delivery != string(DeliverySteered) {
		t.Errorf("delivery on the row = %q, want %q", chat[0].Delivery, DeliverySteered)
	}
	if chat[0].RunID != "ctr-live" {
		t.Errorf("run on the row = %q, want ctr-live", chat[0].RunID)
	}
}

func TestNotifyCommentWithNoLiveRunLeavesItOnTheThreadAndLaunchesNothing(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full := withSession(t, store, tk.ID, "sess-1", "developer", types.StageInProgress)

	c := postComment(t, store, tk.ID, "ana", "ojo con el timeout")
	res, err := d.NotifyComment(context.Background(), full, c)
	if err != nil {
		t.Fatalf("NotifyComment: %v", err)
	}
	if res.Delivery != DeliveryThread {
		t.Fatalf("delivery = %q, want %q", res.Delivery, DeliveryThread)
	}
	if got := len(l.launches()); got != 0 {
		t.Fatalf("launched %d containers, want 0", got)
	}
	chat, err := store.ListMessages(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(chat) != 0 {
		t.Fatalf("chat = %+v, want nothing recorded", chat)
	}
}

func TestNotifyCommentFallsBackToTheThreadWhenSteeringFails(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	full := liveRun(t, store, l, tk.ID, "ctr-live")
	d.steerable.Store("ctr-live", true)
	l.steerFn = func() error { return context.DeadlineExceeded }

	c := postComment(t, store, tk.ID, "ana", "no llega")
	res, err := d.NotifyComment(context.Background(), full, c)
	if err != nil {
		t.Fatalf("NotifyComment: %v", err)
	}
	if res.Delivery != DeliveryThread {
		t.Fatalf("delivery = %q, want %q", res.Delivery, DeliveryThread)
	}
	chat, err := store.ListMessages(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(chat) != 1 || chat[0].Delivery != string(DeliveryThread) {
		t.Fatalf("chat = %+v, want one message stamped %q", chat, DeliveryThread)
	}
}

func TestSteerStateSeparatesRunningFromReachable(t *testing.T) {
	d, store, l := newConvDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "task"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	check := func(what string, task *Task, wantRunning, wantSteerable bool) {
		t.Helper()
		running, steerable := d.SteerState(task)
		if running != wantRunning || steerable != wantSteerable {
			t.Errorf("%s: SteerState = (%t, %t), want (%t, %t)",
				what, running, steerable, wantRunning, wantSteerable)
		}
	}
	check("nil task", nil, false, false)

	idle, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	check("never launched", idle, false, false)

	full := liveRun(t, store, l, tk.ID, "ctr-live")
	check("alive but unarmed", full, true, false)

	d.steerable.Store("ctr-live", true)
	check("alive and armed", full, true, true)
	if !d.CanSteer(full) {
		t.Error("CanSteer disagrees with SteerState on an armed run")
	}

	d.steerable.Delete("ctr-live")
	check("arming cleared", full, true, false)

	l.recordingLauncher.mu.Lock()
	l.recordingLauncher.alive["ctr-live"] = false
	l.recordingLauncher.mu.Unlock()
	check("container gone", full, false, false)
}

func TestCommentNoticeQuotesEveryLine(t *testing.T) {
	got := commentNotice("ana", "primera\n\nsegunda")
	want := "**New comment from ana on the task:**\n\n> primera\n> \n> segunda"
	if got != want {
		t.Errorf("commentNotice =\n%q\nwant\n%q", got, want)
	}
	if !strings.HasPrefix(commentNotice("", "x"), "**New comment from someone") {
		t.Error("an authorless comment must still name somebody")
	}
}
