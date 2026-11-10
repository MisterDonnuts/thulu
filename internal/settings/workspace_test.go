package settings

import (
	"errors"
	"testing"
	"time"
)

func TestWorkspaceSettingsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if _, err := s.GetWorkspaceRaw(ctx, "ws-a", AgentMaxRunMinutesKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unset value: err = %v, want ErrNotFound", err)
	}
	if err := s.PutWorkspaceRaw(ctx, "ws-a", AgentMaxRunMinutesKey, "120"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkspaceRaw(ctx, "ws-a", AgentMaxRunMinutesKey, "150"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetWorkspaceRaw(ctx, "ws-a", AgentMaxRunMinutesKey); err != nil || got != "150" {
		t.Fatalf("after overwrite = %q, %v", got, err)
	}
	all, err := s.ListWorkspace(ctx, "ws-a")
	if err != nil || len(all) != 1 || all[AgentMaxRunMinutesKey] != "150" {
		t.Fatalf("ListWorkspace = %v, %v", all, err)
	}
	if other, _ := s.ListWorkspace(ctx, "ws-b"); len(other) != 0 {
		t.Errorf("ws-b sees %v", other)
	}
	if raw, _ := s.GetRaw(ctx, AgentMaxRunMinutesKey); raw != "0" {
		t.Errorf("install value = %q, want the seeded 0", raw)
	}
	if err := s.DeleteWorkspaceSetting(ctx, "ws-a", AgentMaxRunMinutesKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWorkspaceRaw(ctx, "ws-a", AgentMaxRunMinutesKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("after clear: err = %v", err)
	}
	if err := s.DeleteWorkspaceSetting(ctx, "ws-a", AgentMaxRunMinutesKey); err != nil {
		t.Errorf("second clear: %v", err)
	}
}

func TestPutWorkspaceRawRefuses(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if err := s.PutWorkspaceRaw(ctx, "ws-a", SandboxMaxKey, "99"); err == nil {
		t.Error("stored a host-level key for a workspace")
	}
	if err := s.PutWorkspaceRaw(ctx, "", AgentMaxRunMinutesKey, "30"); err == nil {
		t.Error("stored a value for no workspace")
	}
}

func TestDeleteByWorkspaceRemovesOnlyThatWorkspace(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	for _, ws := range []string{"ws-a", "ws-b"} {
		if err := s.PutWorkspaceRaw(ctx, ws, AgentMaxRunMinutesKey, "45"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteByWorkspace(ctx, "ws-a"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListWorkspace(ctx, "ws-a"); len(got) != 0 {
		t.Errorf("ws-a still has %v", got)
	}
	if got, _ := s.ListWorkspace(ctx, "ws-b"); got[AgentMaxRunMinutesKey] != "45" {
		t.Errorf("ws-b lost its value: %v", got)
	}
}

func TestWorkspaceMaxRunDuration(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if err := s.PutWorkspaceRaw(ctx, "ws-long", AgentMaxRunMinutesKey, "180"); err != nil {
		t.Fatal(err)
	}
	if d := s.WorkspaceMaxRunDuration(ctx, "ws-long", true); d != 180*time.Minute {
		t.Errorf("own value = %s, want 3h", d)
	}
	if d := s.WorkspaceMaxRunDuration(ctx, "ws-other", true); d != SaaSDefaultMaxRunMinutes*time.Minute {
		t.Errorf("no value = %s, want the SaaS default", d)
	}
	if err := s.PutJSON(ctx, AgentMaxRunMinutesCeilingKey, 90); err != nil {
		t.Fatal(err)
	}
	if d := s.WorkspaceMaxRunDuration(ctx, "ws-long", true); d != 90*time.Minute {
		t.Errorf("above a lowered ceiling = %s, want 1h30m", d)
	}
	lim, err := s.WorkspaceRunLimit(ctx, "ws-long", true)
	if err != nil || lim.Value == nil || *lim.Value != 180 || lim.Ceiling != 90 || lim.Effective != 90 {
		t.Errorf("WorkspaceRunLimit = %+v, %v", lim, err)
	}
	if err := s.PutWorkspaceRaw(ctx, "ws-bad", AgentMaxRunMinutesKey, `"soon"`); err != nil {
		t.Fatal(err)
	}
	if d := s.WorkspaceMaxRunDuration(ctx, "ws-bad", true); d != SaaSDefaultMaxRunMinutes*time.Minute {
		t.Errorf("unreadable value = %s, want the SaaS default", d)
	}
}
