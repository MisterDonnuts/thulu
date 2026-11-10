package agentstore

import (
	"errors"
	"testing"
)

func recordSettings(t *testing.T, s *Store, before, after string) {
	t.Helper()
	var b []byte
	if before != "" {
		b = []byte(before)
	}
	if err := s.InTx(t.Context(), func(tx *Tx) error {
		return tx.RecordSettings(GlobalTenant, "backend", b, []byte(after), "tester")
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
}

func TestSettingsHistoryRecordsEachChange(t *testing.T) {
	s, ctx := newStore(t)

	recordSettings(t, s, `{"v":1}`, `{"v":2}`)
	recordSettings(t, s, `{"v":2}`, `{"v":2}`)
	recordSettings(t, s, `{"v":2}`, `{"v":3}`)

	revs, err := s.ListRevisions(ctx, GlobalTenant, KindAgent, "backend", SettingsPath, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("revisions = %d, want 3: %+v", len(revs), revs)
	}
	want := []string{`{"v":3}`, `{"v":2}`, `{"v":1}`}
	for i, rev := range revs {
		body, err := s.RevisionBody(ctx, GlobalTenant, rev.ID)
		if err != nil || string(body) != want[i] {
			t.Fatalf("revision %d = %q (%v), want %q", i, body, err, want[i])
		}
	}
	if revs[2].Action != ActionCreated || revs[0].Action != ActionUpdated {
		t.Fatalf("actions = %s…%s", revs[2].Action, revs[0].Action)
	}
}

func TestSettingsHistoryIsNotAFile(t *testing.T) {
	s, ctx := newStore(t)
	recordSettings(t, s, "", `{"v":1}`)

	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "backend", SettingsPath, []byte("x"), "tester"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("writing the settings path as a file = %v, want ErrInvalidPath", err)
	}
	files, err := s.ListOwner(ctx, GlobalTenant, KindAgent, "backend")
	if err != nil || len(files) != 0 {
		t.Fatalf("the history became a file: %+v (%v)", files, err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "backend", "prompts/main.md", []byte("rules"), "tester"); err != nil {
		t.Fatalf("put prompt: %v", err)
	}
	revs, err := s.ListRevisions(ctx, GlobalTenant, KindAgent, "backend", SettingsPath, 0)
	if err != nil || len(revs) != 1 || revs[0].Path != SettingsPath {
		t.Fatalf("settings history = %+v (%v), want only the settings revision", revs, err)
	}
	if _, err := s.RestoreRevision(ctx, GlobalTenant, revs[0].ID, ModeReset, "tester"); !errors.Is(err, ErrSettingsRestore) {
		t.Fatalf("restore = %v, want ErrSettingsRestore", err)
	}
}
