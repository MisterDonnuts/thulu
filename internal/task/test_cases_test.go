package task

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
)

func newCaseTask(t *testing.T, s *Store, ws, title string) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: ws, Title: title}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return tk
}

func addCases(t *testing.T, s *Store, taskID string, in ...TestCase) []TestCase {
	t.Helper()
	out, err := s.AddTestCases(t.Context(), taskID, "qa", in)
	if err != nil {
		t.Fatalf("AddTestCases: %v", err)
	}
	return out
}

func keysOf(cases []TestCase) []string {
	out := make([]string, len(cases))
	for i := range cases {
		out[i] = cases[i].Key
	}
	return out
}

func TestTestCases_AddAllocatesKeysAndPositions(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "login")

	first := addCases(t, s, tk.ID, TestCase{Title: "valid login"}, TestCase{Title: "wrong password", Priority: TestCasePriorityHigh})
	if got := keysOf(first); !equalSlices(got, []string{"TC-1", "TC-2"}) {
		t.Fatalf("keys = %v, want TC-1 TC-2", got)
	}
	if first[0].Status != TestCasePending || first[0].Priority != TestCasePriorityMedium || first[0].CreatedBy != "qa" {
		t.Errorf("defaults not applied: %+v", first[0])
	}
	more := addCases(t, s, tk.ID, TestCase{Key: "TC-7", Title: "seven"}, TestCase{Key: "AUTH-1", Title: "auth"}, TestCase{Title: "next"})
	if got := keysOf(more); !equalSlices(got, []string{"TC-7", "AUTH-1", "TC-8"}) {
		t.Fatalf("keys = %v, want TC-7 AUTH-1 TC-8", got)
	}

	list, err := s.ListTestCases(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(list); !equalSlices(got, []string{"TC-1", "TC-2", "TC-7", "AUTH-1", "TC-8"}) {
		t.Errorf("order = %v, want insertion order", got)
	}
	for i := range list {
		if list[i].Position != i+1 {
			t.Errorf("%s position = %d, want %d", list[i].Key, list[i].Position, i+1)
		}
		if list[i].Runs == nil {
			t.Errorf("%s runs is nil, want []", list[i].Key)
		}
	}

	got, err := s.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TestCases) != 5 {
		t.Errorf("Get carries %d test cases, want 5", len(got.TestCases))
	}
}

func TestTestCases_ConcurrentAddsGetDistinctKeys(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "race")

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.AddTestCases(t.Context(), tk.ID, "qa", []TestCase{{Title: fmt.Sprintf("case %d", i)}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent add: %v", err)
		}
	}
	list, err := s.ListTestCases(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	keys := keysOf(list)
	sort.Strings(keys)
	positions := map[int]bool{}
	for i := range list {
		positions[list[i].Position] = true
	}
	if len(list) != n || len(positions) != n {
		t.Fatalf("got %d cases with %d distinct positions, want %d each: %v", len(list), len(positions), n, keys)
	}
}

func TestTestCases_KeysAreNotReused(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "reuse")
	addCases(t, s, tk.ID, TestCase{Title: "one"}, TestCase{Title: "two"}, TestCase{Title: "three"})
	if err := s.DeleteTestCase(t.Context(), tk.ID, "TC-3"); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(addCases(t, s, tk.ID, TestCase{Title: "four"})); !equalSlices(got, []string{"TC-4"}) {
		t.Fatalf("after deleting TC-3 the new key = %v, want TC-4", got)
	}
	addCases(t, s, tk.ID, TestCase{Key: "TC-9", Title: "nine"})
	if err := s.DeleteTestCase(t.Context(), tk.ID, "TC-9"); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(addCases(t, s, tk.ID, TestCase{Title: "ten"})); !equalSlices(got, []string{"TC-10"}) {
		t.Fatalf("after deleting TC-9 the new key = %v, want TC-10", got)
	}
}

func TestAllocateKeys_StartsPastTheCounter(t *testing.T) {
	cases := []TestCase{{Title: "a"}, {Key: "TC-2", Title: "b"}, {Title: "c"}}
	seq, err := allocateKeys([]TestCase{{Key: "TC-1"}}, cases, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(cases); !equalSlices(got, []string{"TC-6", "TC-2", "TC-7"}) {
		t.Errorf("keys = %v, want TC-6 TC-2 TC-7", got)
	}
	if seq != 7 {
		t.Errorf("seq = %d, want 7", seq)
	}
	if _, err := allocateKeys([]TestCase{{Key: "TC-1"}}, []TestCase{{Key: "TC-1"}}, 0); !errors.Is(err, ErrConflict) {
		t.Errorf("taken key: err = %v, want ErrConflict", err)
	}
}

func TestTestCases_AddValidation(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "v")
	addCases(t, s, tk.ID, TestCase{Key: "TC-1", Title: "one"})

	long := make([]rune, 201)
	for i := range long {
		long[i] = 'x'
	}
	for name, tc := range map[string]struct {
		in   []TestCase
		want error
	}{
		"no cases":        {nil, ErrInvalid},
		"empty title":     {[]TestCase{{Title: "  "}}, ErrInvalid},
		"long title":      {[]TestCase{{Title: string(long)}}, ErrInvalid},
		"bad key":         {[]TestCase{{Key: "1-TC", Title: "x"}}, ErrInvalid},
		"bad priority":    {[]TestCase{{Title: "x", Priority: "urgent"}}, ErrInvalid},
		"bad status":      {[]TestCase{{Title: "x", Status: "done"}}, ErrInvalid},
		"taken key":       {[]TestCase{{Key: "TC-1", Title: "x"}}, ErrConflict},
		"duplicate batch": {[]TestCase{{Key: "TC-5", Title: "x"}, {Key: "TC-5", Title: "y"}}, ErrConflict},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.AddTestCases(t.Context(), tk.ID, "qa", tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	list, _ := s.ListTestCases(t.Context(), tk.ID)
	if len(list) != 1 {
		t.Errorf("refused batches left rows behind: %v", keysOf(list))
	}
	if _, err := s.AddTestCases(t.Context(), "missing", "qa", []TestCase{{Title: "x"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("add to missing task: err = %v, want ErrNotFound", err)
	}
}

func TestTestCases_UpdatePatchAndReorder(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "u")
	addCases(t, s, tk.ID, TestCase{Title: "a"}, TestCase{Title: "b"}, TestCase{Title: "c"})

	title, status, covered := "  a2 ", TestCaseSkipped, true
	got, err := s.UpdateTestCase(t.Context(), tk.ID, "TC-1", "lead", TestCasePatch{Title: &title, Status: &status, Covered: &covered})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "a2" || got.Status != TestCaseSkipped || !got.Covered || got.UpdatedBy != "lead" || got.Description != "" {
		t.Errorf("patch not applied as partial update: %+v", got)
	}

	bad := "nope"
	if _, err := s.UpdateTestCase(t.Context(), tk.ID, "TC-1", "lead", TestCasePatch{Priority: &bad}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad priority: err = %v, want ErrInvalid", err)
	}
	empty := ""
	if _, err := s.UpdateTestCase(t.Context(), tk.ID, "TC-1", "lead", TestCasePatch{Title: &empty}); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty title: err = %v, want ErrInvalid", err)
	}
	if _, err := s.UpdateTestCase(t.Context(), tk.ID, "TC-9", "lead", TestCasePatch{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing case: err = %v, want ErrNotFound", err)
	}

	first, last := 1, 99
	if got, err = s.UpdateTestCase(t.Context(), tk.ID, "TC-3", "lead", TestCasePatch{Position: &first}); err != nil || got.Position != 1 {
		t.Fatalf("move to front: pos=%v err=%v", got, err)
	}
	if got, err = s.UpdateTestCase(t.Context(), tk.ID, got.ID, "lead", TestCasePatch{Position: &last}); err != nil || got.Position != 3 {
		t.Fatalf("move by id past the end: pos=%v err=%v", got, err)
	}
	list, _ := s.ListTestCases(t.Context(), tk.ID)
	if keys := keysOf(list); !equalSlices(keys, []string{"TC-1", "TC-2", "TC-3"}) {
		t.Errorf("order = %v, want TC-1 TC-2 TC-3", keys)
	}
	for i := range list {
		if list[i].Position != i+1 {
			t.Errorf("%s position = %d, want %d", list[i].Key, list[i].Position, i+1)
		}
	}
}

func TestTestCases_RecordRun(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "r")
	other := newCaseTask(t, s, testWS, "other")
	addCases(t, s, tk.ID, TestCase{Title: "a"})

	att := func(taskID, name string) string {
		a, err := s.AddAttachment(t.Context(), NewAttachment{TaskID: taskID, Filename: name, Path: "/nowhere/" + name})
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	video, take, capture := att(tk.ID, "run.webm"), att(tk.ID, "take.json"), att(tk.ID, "shot.png")
	comment, err := s.AddComment(t.Context(), tk.ID, SourceAgent, "qa-agent", "report")
	if err != nil {
		t.Fatal(err)
	}

	run, c, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{
		Verdict: TestCaseFail, Observed: "500", VideoAttachmentID: video, TakeAttachmentID: take,
		CommentID: comment.ID, CaptureAttachmentIDs: []string{capture, capture}, Author: "qa-agent",
	})
	if err != nil {
		t.Fatalf("RecordTestCaseRun: %v", err)
	}
	if c.Status != TestCaseFail || !c.Covered || c.UpdatedBy != "qa-agent" || len(c.Runs) != 1 {
		t.Errorf("case not marked by the run: %+v", c)
	}
	if run.TaskID != tk.ID || len(run.CaptureAttachmentIDs) != 1 || c.Runs[0].CaptureAttachmentIDs[0] != capture {
		t.Errorf("run not stored as given: %+v", run)
	}

	if _, c, err = s.RecordTestCaseRun(t.Context(), tk.ID, c.ID, TestCaseRun{Verdict: TestCasePass}); err != nil {
		t.Fatal(err)
	}
	if c.Status != TestCasePass || len(c.Runs) != 2 || c.Runs[0].Verdict != TestCasePass {
		t.Errorf("latest run should lead and set status: %+v", c)
	}

	foreignAtt := att(other.ID, "theirs.webm")
	foreignComment, _ := s.AddComment(t.Context(), other.ID, SourceAgent, "x", "theirs")
	for name, r := range map[string]TestCaseRun{
		"foreign video":   {Verdict: TestCasePass, VideoAttachmentID: foreignAtt},
		"foreign take":    {Verdict: TestCasePass, TakeAttachmentID: foreignAtt},
		"foreign capture": {Verdict: TestCasePass, CaptureAttachmentIDs: []string{capture, foreignAtt}},
		"foreign comment": {Verdict: TestCasePass, CommentID: foreignComment.ID},
		"missing file":    {Verdict: TestCasePass, VideoAttachmentID: "gone"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", r); !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
		})
	}
	if _, _, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{Verdict: TestCaseSkipped}); !errors.Is(err, ErrInvalid) {
		t.Errorf("skipped verdict: err = %v, want ErrInvalid", err)
	}
	c, _ = s.GetTestCase(t.Context(), tk.ID, "TC-1")
	if len(c.Runs) != 2 {
		t.Errorf("refused runs were recorded: %d runs", len(c.Runs))
	}
}

func TestTestCases_ImportFromBody(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "body")
	addCases(t, s, tk.ID, TestCase{Title: "Already Here"})
	body := "Intro\n\n## Goal\n- [ ] not a case\n\n## Test Cases\n- [ ] Login works\n- [x] Logout works\n- [ ] already here\n- [ ] Login works\n"

	created, err := s.ImportTestCasesFromBody(t.Context(), tk.ID, "qa", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 || created[0].Title != "Login works" || created[1].Title != "Logout works" {
		t.Fatalf("created = %+v, want Login works + Logout works", created)
	}
	if created[0].Status != TestCasePending || created[0].Covered {
		t.Errorf("unchecked item: %+v", created[0])
	}
	if created[1].Status != TestCasePass || !created[1].Covered {
		t.Errorf("checked item should be pass and covered: %+v", created[1])
	}
	if created[0].Key != "TC-2" || created[1].Key != "TC-3" {
		t.Errorf("keys = %s %s, want TC-2 TC-3", created[0].Key, created[1].Key)
	}

	again, err := s.ImportTestCasesFromBody(t.Context(), tk.ID, "qa", body)
	if err != nil || len(again) != 0 {
		t.Errorf("second import created %d (err %v), want 0", len(again), err)
	}
	none, err := s.ImportTestCasesFromBody(t.Context(), tk.ID, "qa", "no sections")
	if err != nil || len(none) != 0 {
		t.Errorf("body without the section created %d (err %v)", len(none), err)
	}
}

func TestTestCases_ImportFromBodyKeys(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "body keys")
	addCases(t, s, tk.ID, TestCase{Title: "Existing"})
	body := "## Test Cases\n- [ ] TC-7: Login works\n- [ ] Logout works\n- [ ] TC-1: key taken by Existing\n- [ ] QA-2 - Dash separator\n- [ ] TC-7: same key twice\n"

	created, err := s.ImportTestCasesFromBody(t.Context(), tk.ID, "qa", body)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(created))
	for i, c := range created {
		got[i] = c.Key + " " + c.Title
	}
	want := []string{"TC-7 Login works", "TC-8 Logout works", "QA-2 Dash separator"}
	if !slices.Equal(got, want) {
		t.Errorf("created = %q, want %q", got, want)
	}
}

func TestSplitBodyItemKey(t *testing.T) {
	for _, tc := range []struct{ in, key, title string }{
		{"TC-3: login works", "TC-3", "login works"},
		{"TC-3 — login works", "TC-3", "login works"},
		{"TC-12a. edge", "TC-12a", "edge"},
		{"login works", "", "login works"},
		{"TC-3", "", "TC-3"},
		{"Wi-Fi 6: connects", "", "Wi-Fi 6: connects"},
	} {
		if key, title := splitBodyItemKey(tc.in); key != tc.key || title != tc.title {
			t.Errorf("splitBodyItemKey(%q) = %q, %q; want %q, %q", tc.in, key, title, tc.key, tc.title)
		}
	}
}

func TestTestCases_WorkspaceListing(t *testing.T) {
	s := newTestStore(t)
	a := newCaseTask(t, s, testWS, "Checkout flow")
	b := newCaseTask(t, s, testWS, "Profile page")
	foreign := newCaseTask(t, s, "ws_other", "Checkout elsewhere")
	if err := s.writeProjectLinks(t.Context(), s.db, b.ID, []string{"proj_1"}); err != nil {
		t.Fatal(err)
	}
	addCases(t, s, a.ID, TestCase{Title: "pay with card"}, TestCase{Title: "pay_with 100%"})
	addCases(t, s, b.ID, TestCase{Title: "edit name"})
	addCases(t, s, foreign.ID, TestCase{Title: "pay with card"})
	if _, _, err := s.RecordTestCaseRun(t.Context(), a.ID, "TC-1", TestCaseRun{Verdict: TestCaseFail}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordTestCaseRun(t.Context(), a.ID, "TC-1", TestCaseRun{Verdict: TestCasePass}); err != nil {
		t.Fatal(err)
	}

	list := func(f TestCaseFilter) ([]TestCaseRow, map[string]int) {
		t.Helper()
		if !f.Unrestricted && f.WorkspaceID == "" {
			f.WorkspaceID = testWS
		}
		rows, counts, err := s.ListWorkspaceTestCases(t.Context(), f)
		if err != nil {
			t.Fatalf("ListWorkspaceTestCases(%+v): %v", f, err)
		}
		return rows, counts.Status
	}

	rows, counts := list(TestCaseFilter{})
	if _, all, _ := s.ListWorkspaceTestCases(t.Context(), TestCaseFilter{WorkspaceID: testWS}); all.Covered[TestCasePass] != 1 || all.Covered[TestCasePending] != 0 {
		t.Errorf("covered counts = %v, want the one pass case covered", all.Covered)
	}
	if len(rows) != 3 || counts[TestCasePass] != 1 || counts[TestCasePending] != 2 || counts[TestCaseFail] != 0 {
		t.Fatalf("workspace rows = %d counts = %v, want 3 rows, 1 pass, 2 pending", len(rows), counts)
	}
	if rows[0].TaskID != a.ID || rows[0].Key != "TC-1" || rows[1].Key != "TC-2" || rows[2].TaskID != b.ID {
		t.Errorf("order: %s/%s %s/%s %s/%s", rows[0].TaskID, rows[0].Key, rows[1].TaskID, rows[1].Key, rows[2].TaskID, rows[2].Key)
	}
	if rows[0].TaskTitle != "Checkout flow" || rows[0].BoardID == "" || rows[0].RunsCount != 2 ||
		rows[0].LastRun == nil || rows[0].LastRun.Verdict != TestCasePass || rows[1].LastRun != nil {
		t.Errorf("row decoration: %+v", rows[0])
	}

	if rows, counts = list(TestCaseFilter{Statuses: []string{TestCasePending}}); len(rows) != 2 ||
		counts[TestCasePending] != 2 || counts[TestCasePass] != 1 {
		t.Errorf("status filter: %d rows, counts %v", len(rows), counts)
	}
	if _, counts = list(TestCaseFilter{Statuses: []string{TestCasePass}, ProjectID: "proj_1"}); counts[TestCasePending] != 1 || counts[TestCasePass] != 0 {
		t.Errorf("counts keep the other filters: %v", counts)
	}
	if rows, _ = list(TestCaseFilter{Uncovered: true}); len(rows) != 2 {
		t.Errorf("uncovered: %d rows, want 2", len(rows))
	}
	if rows, _ = list(TestCaseFilter{ProjectID: "proj_1"}); len(rows) != 1 || rows[0].TaskID != b.ID {
		t.Errorf("project filter: %+v", rows)
	}
	if rows, _ = list(TestCaseFilter{BoardID: "no-such-board"}); len(rows) != 0 {
		t.Errorf("board filter: %d rows, want 0", len(rows))
	}
	if rows, _ = list(TestCaseFilter{Q: "checkout"}); len(rows) != 2 {
		t.Errorf("q on task title: %d rows, want 2", len(rows))
	}
	if rows, _ = list(TestCaseFilter{Q: "tc-2"}); len(rows) != 1 {
		t.Errorf("q on key: %d rows, want 1", len(rows))
	}
	if rows, _ = list(TestCaseFilter{Q: "_with 100%"}); len(rows) != 1 || rows[0].Title != "pay_with 100%" {
		t.Errorf("q escaping: %+v", rows)
	}
	if rows, counts = list(TestCaseFilter{Limit: 1}); len(rows) != 1 || counts[TestCasePending]+counts[TestCasePass] != 3 {
		t.Errorf("limit pages rows but not counts: %d rows, counts %v", len(rows), counts)
	}
	if rows, _ = list(TestCaseFilter{Unrestricted: true}); len(rows) != 4 {
		t.Errorf("unrestricted: %d rows, want 4", len(rows))
	}
	if _, _, err := s.ListWorkspaceTestCases(t.Context(), TestCaseFilter{WorkspaceID: testWS, Statuses: []string{"done"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown status: err = %v, want ErrInvalid", err)
	}
}

func TestTestCases_DeleteAndCascade(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "d")
	addCases(t, s, tk.ID, TestCase{Title: "a"}, TestCase{Title: "b"})
	if _, _, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{Verdict: TestCasePass}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteTestCase(t.Context(), tk.ID, "TC-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTestCase(t.Context(), tk.ID, "TC-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
	countRows := func(table string) int {
		var n int
		if err := s.db.QueryRowContext(t.Context(), s.rb(`SELECT count(*) FROM `+table+` WHERE task_id = ?`), tk.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countRows("test_case_runs"); n != 0 {
		t.Errorf("deleting a case left %d runs", n)
	}

	if _, _, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-2", TestCaseRun{Verdict: TestCasePass}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), tk.ID); err != nil {
		t.Fatal(err)
	}
	if countRows("test_cases")+countRows("test_case_runs") != 0 {
		t.Error("deleting the task left test case rows")
	}
}

func TestTestCases_CopyAndFork(t *testing.T) {
	s := newTestStore(t)
	src := newCaseTask(t, s, testWS, "src")
	addCases(t, s, src.ID, TestCase{Title: "a", Steps: "1. go", Priority: TestCasePriorityCritical}, TestCase{Key: "AUTH-1", Title: "b"})
	if _, _, err := s.RecordTestCaseRun(t.Context(), src.ID, "TC-1", TestCaseRun{Verdict: TestCaseFail}); err != nil {
		t.Fatal(err)
	}

	check := func(name, dstID, wantCreator string) {
		t.Helper()
		got, err := s.ListTestCases(t.Context(), dstID)
		if err != nil {
			t.Fatal(err)
		}
		if keys := keysOf(got); !equalSlices(keys, []string{"TC-1", "AUTH-1"}) {
			t.Fatalf("%s keys = %v", name, keys)
		}
		c := got[0]
		if c.Status != TestCasePending || c.Covered || len(c.Runs) != 0 || c.Steps != "1. go" ||
			c.Priority != TestCasePriorityCritical || c.CreatedBy != wantCreator {
			t.Errorf("%s copy = %+v", name, c)
		}
	}

	clone := newCaseTask(t, s, testWS, "clone")
	if err := s.CopyTestCases(t.Context(), src.ID, clone.ID, "cloner"); err != nil {
		t.Fatal(err)
	}
	check("clone", clone.ID, "cloner")

	fork := newCaseTask(t, s, testWS, "fork")
	if err := s.ForkData(t.Context(), src.ID, fork.ID); err != nil {
		t.Fatal(err)
	}
	check("fork", fork.ID, "qa")

	c, _ := s.GetTestCase(t.Context(), src.ID, "TC-1")
	if c.Status != TestCaseFail || len(c.Runs) != 1 {
		t.Errorf("source changed by the copy: %+v", c)
	}
}
