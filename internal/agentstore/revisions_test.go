package agentstore

import (
	"errors"
	"testing"
)

func TestEveryChangeLandsInTheHistory(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "developer",
		"prompts/main.md", []byte("v1"), "ana"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "developer",
		"prompts/main.md", []byte("v2"), "beto"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.DeleteFile(ctx, GlobalTenant, KindAgent, "developer",
		"prompts/main.md", "cata"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	revs, err := s.ListRevisions(ctx, GlobalTenant, KindAgent, "developer", "prompts/main.md", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("history has %d entries, want 3: %+v", len(revs), revs)
	}
	if revs[0].Action != ActionDeleted || revs[0].Actor != "cata" {
		t.Fatalf("newest entry = %+v, want a delete by cata", revs[0])
	}
	if revs[1].Action != ActionUpdated || revs[1].Actor != "beto" {
		t.Fatalf("middle entry = %+v, want an update by beto", revs[1])
	}
	if revs[2].Action != ActionCreated || revs[2].Actor != "ana" {
		t.Fatalf("oldest entry = %+v, want a create by ana", revs[2])
	}
	if revs[0].SHA != "" || revs[0].PrevSHA == "" {
		t.Fatalf("delete entry must carry only a predecessor: %+v", revs[0])
	}
}

func TestRewritingIdenticalBytesIsNotAChange(t *testing.T) {
	s, ctx := newStore(t)

	for i := 0; i < 3; i++ {
		if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "discovery",
			"prompt.md", []byte("same"), "t"); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	revs, err := s.ListRevisions(ctx, GlobalTenant, KindSkill, "discovery", "prompt.md", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("history has %d entries after three identical writes, want 1", len(revs))
	}
}

func TestRestoreBringsBackAnEarlierVersion(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "refiner",
		"prompts/main.md", []byte("the good prompt"), "ana"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "refiner",
		"prompts/main.md", []byte("the bad prompt"), "beto"); err != nil {
		t.Fatalf("update: %v", err)
	}

	revs, err := s.ListRevisions(ctx, GlobalTenant, KindAgent, "refiner", "prompts/main.md", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	restored, err := s.RestoreRevision(ctx, GlobalTenant, revs[len(revs)-1].ID, ModeReset, "cata")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if string(restored.Body) != "the good prompt" {
		t.Fatalf("restored body = %q", restored.Body)
	}
	live, err := s.ReadFile(ctx, GlobalTenant, KindAgent, "refiner", "prompts/main.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(live.Body) != "the good prompt" {
		t.Fatalf("live body after restore = %q", live.Body)
	}

	after, err := s.ListRevisions(ctx, GlobalTenant, KindAgent, "refiner", "prompts/main.md", 0)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != 3 {
		t.Fatalf("history has %d entries after a restore, want 3", len(after))
	}
	if after[0].Action != ActionRestored || after[0].Actor != "cata" {
		t.Fatalf("restore not recorded as its own entry: %+v", after[0])
	}
}

func TestRestoringADeleteBringsTheFileBack(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "tester",
		"prompts/testing.md", []byte("how to test"), "ana"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DeleteFile(ctx, GlobalTenant, KindAgent, "tester",
		"prompts/testing.md", "beto"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	revs, err := s.ListRevisions(ctx, GlobalTenant, KindAgent, "tester", "prompts/testing.md", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := s.RestoreRevision(ctx, GlobalTenant, revs[0].ID, ModeRevert, "cata"); err != nil {
		t.Fatalf("revert the delete: %v", err)
	}
	got, err := s.ReadFile(ctx, GlobalTenant, KindAgent, "tester", "prompts/testing.md")
	if err != nil {
		t.Fatalf("file did not come back: %v", err)
	}
	if string(got.Body) != "how to test" {
		t.Fatalf("restored body = %q", got.Body)
	}
}

func TestPruneKeepsTheNewestPerFile(t *testing.T) {
	s, ctx := newStore(t)

	for i := 0; i < 6; i++ {
		body := []byte{byte('a' + i)}
		if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "discovery", "prompt.md", body, "t"); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "other", "prompt.md", body, "t"); err != nil {
			t.Fatalf("put other %d: %v", i, err)
		}
	}
	if _, err := s.PruneRevisions(ctx, GlobalTenant, 2); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, owner := range []string{"discovery", "other"} {
		revs, err := s.ListRevisions(ctx, GlobalTenant, KindSkill, owner, "prompt.md", 0)
		if err != nil {
			t.Fatalf("list %s: %v", owner, err)
		}
		if len(revs) != 2 {
			t.Fatalf("%s kept %d entries, want 2 (prune is per file)", owner, len(revs))
		}
		if revs[0].Action != ActionUpdated {
			t.Fatalf("%s kept the wrong end of the history: %+v", owner, revs[0])
		}
	}
}

func TestBlobsStayRestorableWhileTheHistoryHoldsThem(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "architect",
		"prompts/main.md", []byte("original"), "ana"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "architect",
		"prompts/main.md", []byte("replacement"), "beto"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := s.GCBlobs(ctx); err != nil {
		t.Fatalf("gc: %v", err)
	}
	revs, err := s.ListRevisions(ctx, GlobalTenant, KindAgent, "architect", "prompts/main.md", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got, err := s.RestoreRevision(ctx, GlobalTenant, revs[len(revs)-1].ID, ModeReset, "cata")
	if err != nil {
		t.Fatalf("restore after gc: %v", err)
	}
	if string(got.Body) != "original" {
		t.Fatalf("restored body = %q", got.Body)
	}
}

func TestRevertUndoesTheChangeWithoutNeedingAnEarlierEntry(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "refiner",
		"prompts/main.md", []byte("the good prompt"), "import"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM content_revisions`); err != nil {
		t.Fatalf("clear history: %v", err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "refiner",
		"prompts/main.md", []byte("BROKEN"), "beto"); err != nil {
		t.Fatalf("break it: %v", err)
	}

	revs, err := s.ListRevisions(ctx, GlobalTenant, KindAgent, "refiner", "prompts/main.md", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("expected exactly the bad edit in the log, got %d", len(revs))
	}
	if _, err := s.RestoreRevision(ctx, GlobalTenant, revs[0].ID, ModeRevert, "cata"); err != nil {
		t.Fatalf("revert: %v", err)
	}
	got, err := s.ReadFile(ctx, GlobalTenant, KindAgent, "refiner", "prompts/main.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got.Body) != "the good prompt" {
		t.Fatalf("revert produced %q", got.Body)
	}
}

func TestRevertingACreationRemovesTheFile(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "brandnew",
		"prompt.md", []byte("oops"), "ana"); err != nil {
		t.Fatalf("create: %v", err)
	}
	revs, err := s.ListRevisions(ctx, GlobalTenant, KindSkill, "brandnew", "prompt.md", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := s.RestoreRevision(ctx, GlobalTenant, revs[0].ID, ModeRevert, "beto"); err != nil {
		t.Fatalf("revert the creation: %v", err)
	}
	if _, err := s.ReadFile(ctx, GlobalTenant, KindSkill, "brandnew", "prompt.md"); err == nil {
		t.Fatal("reverting a creation left the file in place")
	}
}

func TestResettingToADeletionIsRejected(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "gone",
		"prompt.md", []byte("bye"), "ana"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DeleteFile(ctx, GlobalTenant, KindSkill, "gone", "prompt.md", "ana"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	revs, err := s.ListRevisions(ctx, GlobalTenant, KindSkill, "gone", "prompt.md", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := s.RestoreRevision(ctx, GlobalTenant, revs[0].ID, ModeReset, "beto"); err == nil {
		t.Fatal("resetting to a deletion should fail")
	}
}

func TestListDeletedFindsWhatTheFileListLost(t *testing.T) {
	s, ctx := newStore(t)
	put := func(path, body string) {
		t.Helper()
		if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "notes", path, []byte(body), "ana"); err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
	}
	put("prompt.md", "rules")
	put("references/a.md", "first")
	put("references/b.md", "second")
	if err := s.DeleteFile(ctx, GlobalTenant, KindSkill, "notes", "references/a.md", "beto"); err != nil {
		t.Fatalf("delete a: %v", err)
	}
	if err := s.DeleteFile(ctx, GlobalTenant, KindSkill, "notes", "references/b.md", "beto"); err != nil {
		t.Fatalf("delete b: %v", err)
	}
	put("references/b.md", "second again")

	got, err := s.ListDeleted(ctx, GlobalTenant, KindSkill, "notes")
	if err != nil {
		t.Fatalf("list deleted: %v", err)
	}
	if len(got) != 1 || got[0].Path != "references/a.md" || got[0].Actor != "beto" {
		t.Fatalf("deleted = %+v, want references/a.md by beto", got)
	}
	if got[0].Size != len("first") {
		t.Fatalf("size = %d, want the size of what was lost (%d)", got[0].Size, len("first"))
	}

	owners, err := s.ListDeletedOwners(ctx, GlobalTenant, KindSkill)
	if err != nil {
		t.Fatalf("list deleted owners: %v", err)
	}
	if len(owners) != 0 {
		t.Fatalf("deleted owners = %+v, want none", owners)
	}
}

func TestRestoreDeletedBringsTheWholeOwnerBack(t *testing.T) {
	s, ctx := newStore(t)
	for path, body := range map[string]string{
		"prompt.md": "rules", "skill.yaml": "name: notes\n", "references/old.md": "stale",
	} {
		if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "notes", path, []byte(body), "ana"); err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
	}
	if err := s.DeleteFile(ctx, GlobalTenant, KindSkill, "notes", "references/old.md", "ana"); err != nil {
		t.Fatalf("delete old: %v", err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "notes", "prompt.md", []byte("rules v2"), "ana"); err != nil {
		t.Fatalf("edit prompt: %v", err)
	}
	for _, path := range []string{"prompt.md", "skill.yaml"} {
		if err := s.DeleteFile(ctx, GlobalTenant, KindSkill, "notes", path, "beto"); err != nil {
			t.Fatalf("delete %s: %v", path, err)
		}
	}

	owners, err := s.ListDeletedOwners(ctx, GlobalTenant, KindSkill)
	if err != nil {
		t.Fatalf("list deleted owners: %v", err)
	}
	if len(owners) != 1 || owners[0].Name != "notes" || owners[0].DeletedAt.IsZero() {
		t.Fatalf("deleted owners = %+v, want notes", owners)
	}

	files, err := s.RestoreDeleted(ctx, GlobalTenant, KindSkill, "notes", "cata")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("restored %d files, want 2", len(files))
	}
	f, err := s.ReadFile(ctx, GlobalTenant, KindSkill, "notes", "prompt.md")
	if err != nil || string(f.Body) != "rules v2" {
		t.Fatalf("prompt.md after restore = %q, %v", f.Body, err)
	}
	if _, err := s.ReadFile(ctx, GlobalTenant, KindSkill, "notes", "references/old.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a file deleted before the skill came back with it: %v", err)
	}
	revs, err := s.ListRevisions(ctx, GlobalTenant, KindSkill, "notes", "prompt.md", 1)
	if err != nil || len(revs) != 1 || revs[0].Action != ActionRestored || revs[0].Actor != "cata" {
		t.Fatalf("newest prompt.md revision = %+v, %v; want a restore by cata", revs, err)
	}
	if owners, _ := s.ListDeletedOwners(ctx, GlobalTenant, KindSkill); len(owners) != 0 {
		t.Fatalf("still listed as deleted after the restore: %+v", owners)
	}
	if _, err := s.RestoreDeleted(ctx, GlobalTenant, KindSkill, "notes", "cata"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restoring with nothing deleted = %v, want ErrNotFound", err)
	}
}
