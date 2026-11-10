package task

import (
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func approvalByID(t *testing.T, list []Approval, id string) Approval {
	t.Helper()
	for i := range list {
		if list[i].ID == id {
			return list[i]
		}
	}
	t.Fatalf("approval %s not in %+v", id, list)
	return Approval{}
}

func newApprovalTask(t *testing.T, store *Store) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	return tk
}

func TestApprovalMigrationCopiesFlags(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := newApprovalTask(t, store)
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	resolved := created.Add(time.Hour)
	insert := func(id, kind, status, payload, by string, resolvedAt *time.Time) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, store.rb(`INSERT INTO task_flags
			(id, task_id, kind, label, status, payload, created_by, resolved_by, resolved_note, created_at, resolved_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`),
			id, tk.ID, kind, "label "+id, status, payload, by, "ana@x", "note "+id, created, resolvedAt); err != nil {
			t.Fatalf("insert flag %s: %v", id, err)
		}
	}
	insert("f-agent", "arch_decision", "pending", "{}", "agent:developer", nil)
	insert("f-system", "infra_impact", "approved", `{"environment_id":"env-1"}`, "agent:developer", &resolved)
	insert("f-person", "custom", "rejected", "{}", "ana@x", &resolved)
	insert("f-orphan", "custom", "pending", "{}", "ana@x", nil)
	if _, err := store.db.ExecContext(ctx, store.rb(`UPDATE task_flags SET task_id = 'gone' WHERE id = 'f-orphan'`)); err != nil {
		t.Fatalf("orphan: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, approvalMigrations[2].SQL); err != nil {
		t.Fatalf("copy: %v", err)
	}
	list, err := store.ListApprovals(ctx, tk.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("copied %d approvals, want 3: %+v", len(list), list)
	}
	agent := approvalByID(t, list, "f-agent")
	if agent.Source != ApprovalFromAgent || agent.Status != ApprovalPending || agent.Key != "arch_decision" || agent.Stage != "" {
		t.Errorf("agent flag = %+v", agent)
	}
	system := approvalByID(t, list, "f-system")
	if system.Source != ApprovalFromSystem || system.Status != ApprovalApproved || system.DecidedBy != "ana@x" {
		t.Errorf("proposal flag = %+v", system)
	}
	if system.DecidedAt == nil || !system.DecidedAt.Equal(resolved) {
		t.Errorf("decided_at = %v, want %v (UTC kept)", system.DecidedAt, resolved)
	}
	person := approvalByID(t, list, "f-person")
	if person.Source != ApprovalFromPerson || person.Status != ApprovalChangesRequested || person.DecidedNote != "note f-person" {
		t.Errorf("person flag = %+v", person)
	}
	if !person.CreatedAt.Equal(created) {
		t.Errorf("created_at = %v, want %v", person.CreatedAt, created)
	}
	if _, err := store.db.ExecContext(ctx, approvalMigrations[2].SQL); err != nil {
		t.Fatalf("copy again: %v", err)
	}
	if again, _ := store.ListApprovals(ctx, tk.ID); len(again) != 3 {
		t.Errorf("after a second copy: %d approvals, want 3", len(again))
	}
}

func TestFlagsAliasOverApprovals(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := newApprovalTask(t, store)

	f := &Flag{Kind: FlagArchDecision, Label: "new queue", CreatedBy: "agent:developer"}
	if err := store.AddFlag(ctx, tk.ID, f); err != nil {
		t.Fatalf("add flag: %v", err)
	}
	a, err := store.GetApproval(ctx, f.ID)
	if err != nil {
		t.Fatalf("flag is not an approval: %v", err)
	}
	if a.Source != ApprovalFromAgent || a.Stage != "" || a.Key != string(FlagArchDecision) || a.Status != ApprovalPending {
		t.Errorf("approval behind the flag = %+v", a)
	}

	rejected := FlagRejected
	if _, err := store.UpdateFlag(ctx, f.ID, FlagPatch{Status: &rejected}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if a, _ = store.GetApproval(ctx, f.ID); a.Status != ApprovalChangesRequested {
		t.Errorf("rejected flag = %q, want changes_requested", a.Status)
	}

	if _, err := store.SetApprovalStatus(ctx, f.ID, ApprovalWaived, "ana@x", "not ours", ""); err != nil {
		t.Fatalf("waive: %v", err)
	}
	got, err := store.GetFlag(ctx, f.ID)
	if err != nil || got.Status != FlagApproved || got.ResolvedNote != "not ours" {
		t.Errorf("waived approval as a flag = %+v, %v", got, err)
	}

	full, err := store.Get(ctx, tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(full.Approvals) != 1 || len(full.Flags) != 1 || full.Flags[0].ID != f.ID {
		t.Errorf("task carries approvals %+v and flags %+v, want the same row in both", full.Approvals, full.Flags)
	}

	if err := store.DeleteFlag(ctx, f.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := store.ListApprovals(ctx, tk.ID); len(list) != 0 {
		t.Errorf("approvals after deleting the flag: %+v", list)
	}
}

func TestResetStaleApprovals(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := newApprovalTask(t, store)
	reqs := []types.ApprovalRequirement{
		{Key: "qa", Label: "QA", ResetOnChange: true},
		{Key: "design", Label: "Design", ResetOnChange: false},
	}
	open := func(key string, source ApprovalSource) *Approval {
		t.Helper()
		a := &Approval{TaskID: tk.ID, Key: key, Label: key, Stage: "review", Source: source}
		if err := store.insertApproval(ctx, a); err != nil {
			t.Fatalf("insert %s: %v", key, err)
		}
		if _, err := store.SetApprovalStatus(ctx, a.ID, ApprovalApproved, "ana@x", "", "repo@aaa"); err != nil {
			t.Fatalf("approve %s: %v", key, err)
		}
		return a
	}
	qa := open("qa", ApprovalFromStage)
	design := open("design", ApprovalFromStage)
	extra := open("extra", ApprovalFromPerson)

	if out, err := store.ResetStaleApprovals(ctx, tk.ID, "review", reqs, ""); err != nil || len(out) != 0 {
		t.Errorf("no ref reset %+v, %v; want nothing", out, err)
	}
	if out, err := store.ResetStaleApprovals(ctx, tk.ID, "review", reqs, "repo@aaa"); err != nil || len(out) != 0 {
		t.Errorf("same ref reset %+v, %v; want nothing", out, err)
	}
	out, err := store.ResetStaleApprovals(ctx, tk.ID, "review", reqs, "repo@bbb")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("reset %d approvals, want qa and the person's: %+v", len(out), out)
	}
	list, _ := store.ListApprovals(ctx, tk.ID)
	if a := approvalByID(t, list, qa.ID); a.Status != ApprovalPending || a.ApprovedAtRef != "" || a.DecidedBy != "" {
		t.Errorf("qa after a change = %+v, want pending and cleared", a)
	}
	if a := approvalByID(t, list, extra.ID); a.Status != ApprovalPending {
		t.Errorf("person's approval after a change = %q, want pending", a.Status)
	}
	if a := approvalByID(t, list, design.ID); a.Status != ApprovalApproved {
		t.Errorf("design (reset_on_change false) = %q, want still approved", a.Status)
	}
}

func TestListCountsPendingApprovals(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := newApprovalTask(t, store)
	for _, st := range []FlagStatus{FlagPending, FlagRejected, FlagApproved} {
		if err := store.AddFlag(ctx, tk.ID, &Flag{Kind: FlagCustom, Label: string(st), Status: st}); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	list, err := store.ListByWorkspace(ctx, testWS)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range list {
		if list[i].ID == tk.ID {
			if list[i].PendingApprovals != 2 {
				t.Errorf("pending_approvals = %d, want 2 (pending and changes requested)", list[i].PendingApprovals)
			}
			return
		}
	}
	t.Fatal("task not listed")
}

func TestListPendingApprovalsKeepsChangesRequested(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := newApprovalTask(t, store)
	for _, st := range []FlagStatus{FlagPending, FlagRejected, FlagApproved} {
		if err := store.AddFlag(ctx, tk.ID, &Flag{Kind: FlagCustom, Label: string(st), Status: st}); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	other := &Task{WorkspaceID: "other-ws", Title: "elsewhere"}
	if err := store.Create(ctx, other); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.AddFlag(ctx, other.ID, &Flag{Kind: FlagCustom, Label: "elsewhere", Status: FlagPending}); err != nil {
		t.Fatalf("add: %v", err)
	}
	shelved := newApprovalTask(t, store)
	if err := store.AddFlag(ctx, shelved.ID, &Flag{Kind: FlagCustom, Label: "shelved", Status: FlagPending}); err != nil {
		t.Fatalf("add: %v", err)
	}
	archive(t, store, shelved.ID)

	got, err := store.ListPendingApprovals(ctx, testWS, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	statuses := map[ApprovalStatus]int{}
	for _, a := range got {
		if a.TaskID != tk.ID {
			t.Errorf("listed %s of task %s, want only %s", a.ID, a.TaskID, tk.ID)
		}
		statuses[a.Status]++
	}
	if len(got) != 2 || statuses[ApprovalPending] != 1 || statuses[ApprovalChangesRequested] != 1 {
		t.Errorf("inbox approvals = %+v, want one pending and one changes_requested", got)
	}
}

func TestForkCopiesApprovals(t *testing.T) {
	_, store := newStageTestDispatcher(t, gatedBoard("", ""))
	ctx := t.Context()
	src := &Task{WorkspaceID: testWS, Title: "src", Stage: "verify"}
	if err := store.Create(ctx, src); err != nil {
		t.Fatalf("create: %v", err)
	}
	opened, _ := store.ListApprovals(ctx, src.ID)
	if len(opened) != 1 {
		t.Fatalf("source opened %d approvals, want qa", len(opened))
	}
	if _, err := store.SetApprovalStatus(ctx, opened[0].ID, ApprovalApproved, "ana@x", "", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	dst := &Task{WorkspaceID: testWS, Title: "fork", Stage: "verify"}
	if err := store.Create(ctx, dst); err != nil {
		t.Fatalf("create fork: %v", err)
	}
	if err := store.ForkData(ctx, src.ID, dst.ID); err != nil {
		t.Fatalf("fork: %v", err)
	}
	got, _ := store.ListApprovals(ctx, dst.ID)
	if len(got) != 1 || got[0].Status != ApprovalApproved || got[0].ID == opened[0].ID {
		t.Errorf("fork approvals = %+v, want one approved copy with its own id", got)
	}
}

func flagRow(t *testing.T, store *Store, id string) (status, label, note string, ok bool) {
	t.Helper()
	err := store.db.QueryRowContext(t.Context(), store.rb(
		`SELECT status, label, resolved_note FROM task_flags WHERE id = ?`), id).Scan(&status, &label, &note)
	return status, label, note, err == nil
}

func TestFlagsMirrorFollowsTheApprovals(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := newApprovalTask(t, store)
	f := &Flag{Kind: "custom", Label: "ship it?"}
	if err := store.AddFlag(ctx, tk.ID, f); err != nil {
		t.Fatalf("add: %v", err)
	}
	if st, label, _, ok := flagRow(t, store, f.ID); !ok || st != "pending" || label != "ship it?" {
		t.Fatalf("after add: %q %q %v", st, label, ok)
	}
	if _, err := store.SetApprovalStatus(ctx, f.ID, ApprovalChangesRequested, "ana@x", "not yet", ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if st, _, note, _ := flagRow(t, store, f.ID); st != "rejected" || note != "not yet" {
		t.Errorf("after changes requested: %q %q, want rejected with the note", st, note)
	}
	if err := store.SetApprovalLabel(ctx, f.ID, "ship it now?"); err != nil {
		t.Fatalf("label: %v", err)
	}
	if _, label, _, _ := flagRow(t, store, f.ID); label != "ship it now?" {
		t.Errorf("label = %q", label)
	}
	if err := store.DeleteApproval(ctx, f.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, _, ok := flagRow(t, store, f.ID); ok {
		t.Error("the flag outlived its approval")
	}
	staged := &Approval{TaskID: tk.ID, Key: "qa", Label: "QA", Stage: "review", Source: ApprovalFromStage}
	if err := store.AddApproval(ctx, staged); err != nil {
		t.Fatalf("stage approval: %v", err)
	}
	if _, _, _, ok := flagRow(t, store, staged.ID); ok {
		t.Error("a stage approval was mirrored as a flag")
	}
}

func TestSyncFlagsBackAfterARollback(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	tk := newApprovalTask(t, store)
	f := &Flag{Kind: "custom", Label: "ship it?"}
	if err := store.AddFlag(ctx, tk.ID, f); err != nil {
		t.Fatalf("add: %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.db.ExecContext(ctx, store.rb(`UPDATE task_flags
		SET status = 'approved', resolved_by = 'old@x', resolved_note = 'fine', resolved_at = ? WHERE id = ?`), now, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.rb(`INSERT INTO task_flags
		(id, task_id, kind, label, status, payload, created_by, created_at)
		VALUES ('f-old', ?, 'custom', 'raised while rolled back', 'pending', '{}', 'agent:developer', ?)`), tk.ID, now); err != nil {
		t.Fatal(err)
	}

	store.syncFlagsBack(ctx)

	list, _ := store.ListApprovals(ctx, tk.ID)
	if a := approvalByID(t, list, f.ID); a.Status != ApprovalApproved || a.DecidedBy != "old@x" || a.DecidedNote != "fine" {
		t.Errorf("decided while rolled back = %+v", a)
	}
	if a := approvalByID(t, list, "f-old"); a.Status != ApprovalPending || a.Source != ApprovalFromAgent {
		t.Errorf("raised while rolled back = %+v", a)
	}
	if _, err := store.SetApprovalStatus(ctx, "f-old", ApprovalWaived, "ana@x", "skip", ""); err != nil {
		t.Fatal(err)
	}
	store.syncFlagsBack(ctx)
	if a, _ := store.GetApproval(ctx, "f-old"); a.Status != ApprovalWaived {
		t.Errorf("sync turned a waiver into %q", a.Status)
	}
}
