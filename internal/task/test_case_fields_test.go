package task

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNeedKinds_Catalog(t *testing.T) {
	kinds := NeedKinds()
	var names []string
	for _, k := range kinds {
		names = append(names, k.Kind)
		if k.Icon == "" || len(k.Actions) == 0 || k.TitleKey != "task.needs."+k.Kind+".title" || k.HelpKey != "task.needs."+k.Kind+".help" {
			t.Errorf("incomplete kind: %+v", k)
		}
		if k.Secrets != slices.Contains(k.Actions, NeedActionAccess) {
			t.Errorf("%s: secrets = %v but actions %v", k.Kind, k.Secrets, k.Actions)
		}
	}
	for _, want := range []string{"account", "third_party_account", "device", "environment", "judgment", "other"} {
		if !slices.Contains(names, want) {
			t.Errorf("catalog lacks %s: %v", want, names)
		}
	}
	if !slices.IsSortedFunc(kinds, func(a, b NeedKind) int { return a.Order - b.Order }) {
		t.Errorf("kinds not in order: %v", names)
	}
	if _, err := loadNeedKinds([]byte(`{"kinds":[{"kind":"x","title_key":"a","help_key":"b","actions":["fly"]}]}`)); err == nil {
		t.Error("an unknown action loaded")
	}
}

func TestRecordRun_StructuredFields(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "fields")
	addCases(t, s, tk.ID, TestCase{Title: "a"})

	needs := []Need{{Kind: "account", Label: "Admin login", Secrets: []NeedSecret{{Name: "ADMIN_PASSWORD", Label: "Password"}}}}
	run, c, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{
		Verdict: TestCaseBlocked, Observed: "login wall", Needs: needs, Tried: TriedSimulated,
		TriedResult: "a fake user got 401", WritesData: true, Author: "qa",
	})
	if err != nil {
		t.Fatalf("RecordTestCaseRun: %v", err)
	}
	if c.Status != TestCaseBlocked || run.Round != 1 || !run.WritesData || run.Tried != TriedSimulated {
		t.Errorf("run = %+v", run)
	}
	got, _ := s.GetTestCase(t.Context(), tk.ID, "TC-1")
	if r := got.Runs[0]; len(r.Needs) != 1 || r.Needs[0].Secrets[0].Name != "ADMIN_PASSWORD" || r.TriedResult != "a fake user got 401" {
		t.Errorf("stored run = %+v", r)
	}

	for name, r := range map[string]TestCaseRun{
		"observed too long":   {Verdict: TestCaseFail, Observed: strings.Repeat("x", MaxObservedRunes+1)},
		"needs on a fail":     {Verdict: TestCaseFail, Needs: needs},
		"unknown kind":        {Verdict: TestCaseBlocked, Needs: []Need{{Kind: "magic"}}},
		"secrets on a device": {Verdict: TestCaseBlocked, Needs: []Need{{Kind: "device", Secrets: []NeedSecret{{Name: "PIN"}}}}},
		"bad secret name":     {Verdict: TestCaseBlocked, Needs: []Need{{Kind: "account", Secrets: []NeedSecret{{Name: "pass word"}}}}},
		"secret twice":        {Verdict: TestCaseBlocked, Needs: []Need{{Kind: "account", Secrets: []NeedSecret{{Name: "A"}, {Name: "A"}}}}},
		"unknown tried":       {Verdict: TestCaseBlocked, Tried: "maybe"},
	} {
		if _, _, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	_, _, err = s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{Verdict: TestCaseFail, Observed: strings.Repeat("x", MaxObservedRunes+1)})
	if err == nil || !strings.Contains(err.Error(), "note") {
		t.Errorf("observed error = %v, want it to point at note", err)
	}
	// 280 characters of any script fit.
	if _, _, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{Verdict: TestCaseFail, Observed: strings.Repeat("ñ", MaxObservedRunes)}); err != nil {
		t.Errorf("280 runes refused: %v", err)
	}
}

func TestRecordRun_LinksTheVideosSubtitles(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "vtt")
	addCases(t, s, tk.ID, TestCase{Title: "a"}, TestCase{Title: "b"})
	att := func(name string) string {
		a, err := s.AddAttachment(t.Context(), NewAttachment{TaskID: tk.ID, Filename: name, Path: "/nowhere/" + name})
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	video := att("TC-1-login.mp4")
	att("other.vtt")
	chapters := att("TC-1-login.chapters.vtt")
	run, _, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{Verdict: TestCasePass, VideoAttachmentID: video})
	if err != nil {
		t.Fatal(err)
	}
	if run.VTTAttachmentID != chapters {
		t.Errorf("vtt = %q, want the chapters file %q", run.VTTAttachmentID, chapters)
	}
	exact := att("TC-1-login.vtt")
	run, _, _ = s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{Verdict: TestCasePass, VideoAttachmentID: video})
	if run.VTTAttachmentID != exact {
		t.Errorf("vtt = %q, want the exact name %q", run.VTTAttachmentID, exact)
	}
	run, _, _ = s.RecordTestCaseRun(t.Context(), tk.ID, "TC-2", TestCaseRun{Verdict: TestCasePass})
	if run.VTTAttachmentID != "" {
		t.Errorf("a run without video got subtitles %q", run.VTTAttachmentID)
	}
}

func TestQueueTestCases(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "queue")
	addCases(t, s, tk.ID, TestCase{Title: "a"}, TestCase{Title: "b"})
	if _, _, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{Verdict: TestCaseBlocked}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.QueueTestCases(t.Context(), tk.ID, []string{"TC-1", "TC-9"}, "alice"); err == nil {
		t.Fatal("an unknown ref queued")
	}
	if c, _ := s.GetTestCase(t.Context(), tk.ID, "TC-1"); c.Status != TestCaseBlocked {
		t.Fatalf("a refused queue changed TC-1 to %s", c.Status)
	}
	queued, prev, err := s.QueueTestCases(t.Context(), tk.ID, []string{"TC-1", "TC-1"}, "alice")
	if err != nil || len(queued) != 1 || queued[0].Status != TestCaseQueued || prev[queued[0].ID] != TestCaseBlocked {
		t.Fatalf("queue = %+v %v %v", queued, prev, err)
	}
	queuedStatus := TestCaseQueued
	if _, err := s.UpdateTestCase(t.Context(), tk.ID, "TC-2", "qa", TestCasePatch{Status: &queuedStatus}); !errors.Is(err, ErrInvalid) {
		t.Errorf("status edit to queued: err = %v, want ErrInvalid", err)
	}
	if err := s.RestoreTestCaseStatuses(t.Context(), tk.ID, prev, "alice"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetTestCase(t.Context(), tk.ID, "TC-1"); c.Status != TestCaseBlocked {
		t.Errorf("restore left %s", c.Status)
	}
	// A new run takes the case out of the queue.
	if _, _, err := s.QueueTestCases(t.Context(), tk.ID, []string{"TC-1"}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, c, err := s.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{Verdict: TestCasePass}); err != nil || c.Status != TestCasePass {
		t.Errorf("run after queue: %+v %v", c, err)
	}
}

func TestRounds_StampedOnEventsRunsAndWaivers(t *testing.T) {
	_, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "intake", "")
	if _, err := store.AddTestCases(t.Context(), tk.ID, "qa", []TestCase{{Title: "a"}, {Title: "b"}}); err != nil {
		t.Fatal(err)
	}
	move := func(from, to string) {
		t.Helper()
		if err := store.RecordEvent(t.Context(), tk.ID, EventStageEntered, "", "user", "", map[string]string{"from": from, "to": to}); err != nil {
			t.Fatal(err)
		}
	}
	move("intake", "code")
	move("code", "verify")
	if r := store.TaskRound(t.Context(), tk.ID); r != 1 {
		t.Fatalf("round before a send-back = %d, want 1", r)
	}
	move("verify", "code")
	if r := store.TaskRound(t.Context(), tk.ID); r != 2 {
		t.Fatalf("round after a send-back = %d, want 2", r)
	}
	if err := store.RecordEvent(t.Context(), tk.ID, EventAgentRunStarted, "", "qa", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	evs := eventsOfKind(t, store, tk.ID, EventAgentRunStarted)
	if len(evs) != 1 || evs[0].Round != 2 {
		t.Errorf("agent run event = %+v, want round 2", evs)
	}
	run, _, err := store.RecordTestCaseRun(t.Context(), tk.ID, "TC-1", TestCaseRun{Verdict: TestCasePass})
	if err != nil || run.Round != 2 {
		t.Errorf("case run round = %+v %v, want 2", run, err)
	}
	if _, err := store.WaiveTestCases(t.Context(), tk.ID, []string{"TC-2"}, "alice", "out of scope", ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := store.GetTestCase(t.Context(), tk.ID, "TC-2"); len(c.Runs) != 1 || c.Runs[0].Round != 2 {
		t.Errorf("waiver run = %+v, want round 2", c.Runs)
	}
	if got, _ := store.Get(t.Context(), tk.ID); got.Round != 2 {
		t.Errorf("Get round = %d, want 2", got.Round)
	}
}
