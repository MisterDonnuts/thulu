package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func gatedTask(t *testing.T, store *Store) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: "ship it", Stage: "verify"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	return tk
}

func expiredFault() []credentials.Fault {
	return []credentials.Fault{{
		CredentialID: "cred_1",
		Name:         "prod-aws",
		Provider:     "aws",
		State:        credentials.StateExpired,
		Reason:       credentials.ReasonExpired,
		Detail:       `credential "prod-aws" expired on 2026-07-01`,
		Tools:        []string{"thulu-cloud/logs_search", "thulu-cloud/metrics_query"},
	}}
}

func TestCredentialGate_StopsTheLaunch(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	d.WithCredentialGate(func(context.Context, *Task) ([]credentials.Fault, error) {
		return expiredFault(), nil
	})
	tk := gatedTask(t, store)

	// launcher is nil: getting past the gate would panic here.
	d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})

	after, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != StatusCredentialBlocked {
		t.Errorf("status = %q, want %q", after.Status, StatusCredentialBlocked)
	}
}

func TestCredentialGate_ExplainsItselfOnTheTimeline(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	d.WithCredentialGate(func(context.Context, *Task) ([]credentials.Fault, error) {
		return expiredFault(), nil
	})
	tk := gatedTask(t, store)

	d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})

	body := lastComment(t, store, tk.ID)
	for _, want := range []string{"prod-aws", "expired", "logs_search", "switch it off"} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Errorf("blocked-launch comment does not mention %q:\n%s", want, body)
		}
	}
}

func TestCredentialGate_RecordsAnEvent(t *testing.T) {
	d, store := newStageTestDispatcher(t, customBoard())
	d.WithCredentialGate(func(context.Context, *Task) ([]credentials.Fault, error) {
		return expiredFault(), nil
	})
	tk := gatedTask(t, store)

	d.launchStage(context.Background(), tk, tk.Stage, "developer", &types.AgentType{Name: "developer"}, launchOpts{})

	events, err := store.ListEvents(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, ev := range events {
		if ev.Kind == EventCredentialBlocked {
			if !strings.Contains(ev.Summary, "prod-aws") {
				t.Errorf("event summary %q does not name the credential", ev.Summary)
			}
			if want := i18n.Text("task.comments.credential_expired", "name", "prod-aws"); ev.Summary != want {
				t.Errorf("event summary %q, want %q", ev.Summary, want)
			}
			var p credentialBlockedEvent
			if err := json.Unmarshal(ev.Payload, &p); err != nil || len(p.Faults) != 1 ||
				p.Faults[0].Name != "prod-aws" || p.Faults[0].State != credentials.StateExpired {
				t.Errorf("payload = %s, want the credential and its state", ev.Payload)
			}
			return
		}
	}
	t.Error("no credential_blocked event on the timeline")
}

func TestCredentialGate_LetsAHealthyRunThrough(t *testing.T) {
	d, _ := newStageTestDispatcher(t, customBoard())
	d.WithCredentialGate(func(context.Context, *Task) ([]credentials.Fault, error) {
		return nil, nil
	})
	if !d.credentialsReady(context.Background(), &Task{WorkspaceID: testWS, ID: "t1"}) {
		t.Error("a workspace with no faults was refused")
	}
}

func TestCredentialGate_LaunchesWhenTheCheckIsUnavailable(t *testing.T) {
	d, _ := newStageTestDispatcher(t, customBoard())
	d.WithCredentialGate(func(context.Context, *Task) ([]credentials.Fault, error) {
		return nil, errors.New("database is down")
	})
	if !d.credentialsReady(context.Background(), &Task{WorkspaceID: testWS, ID: "t1"}) {
		t.Error("an unavailable gate blocked the launch")
	}
}

func TestCredentialGate_AbsentGateNeverBlocks(t *testing.T) {
	d, _ := newStageTestDispatcher(t, customBoard())
	if !d.credentialsReady(context.Background(), &Task{WorkspaceID: testWS, ID: "t1"}) {
		t.Error("a dispatcher with no gate refused a launch")
	}
}

func TestCredentialGate_WithNilKeepsTheExistingGate(t *testing.T) {
	d, _ := newStageTestDispatcher(t, customBoard())
	d.WithCredentialGate(func(context.Context, *Task) ([]credentials.Fault, error) {
		return expiredFault(), nil
	})
	d.WithCredentialGate(nil)
	if d.credentialsReady(context.Background(), &Task{WorkspaceID: testWS, ID: "t1"}) {
		t.Error("WithCredentialGate(nil) cleared a configured gate")
	}
}
