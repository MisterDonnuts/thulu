package task

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func criteriaOf(t *testing.T, s *Store, taskID string) []Criterion {
	t.Helper()
	list, err := s.ListCriteria(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func criterionKeys(list []Criterion) []string {
	out := make([]string, len(list))
	for i := range list {
		out[i] = list[i].Key
	}
	return out
}

func TestSetCriteria_ValidatesAndRewritesTheSection(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "login")

	for name, in := range map[string][]CriterionInput{
		"bad key":   {{Key: "AC1", Text: "x"}},
		"lowercase": {{Key: "ac-1", Text: "x"}},
		"repeated":  {{Key: "AC-1", Text: "x"}, {Key: "AC-1", Text: "y"}},
		"no text":   {{Key: "AC-1", Text: "  "}},
		"too long":  {{Key: "AC-1", Text: strings.Repeat("x", maxCriterionRunes+1)}},
	} {
		if _, err := s.SetCriteria(t.Context(), tk.ID, "pm", in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}

	list, err := s.SetCriteria(t.Context(), tk.ID, "pm", []CriterionInput{
		{Key: "AC-2", Text: "Wrong password shows an error"},
		{Key: "AC-1", Text: "Valid login lands on the dashboard\nwithin two seconds"},
	})
	if err != nil {
		t.Fatalf("SetCriteria: %v", err)
	}
	if got := criterionKeys(list); !slices.Equal(got, []string{"AC-2", "AC-1"}) || list[0].Position >= list[1].Position {
		t.Fatalf("rows = %+v, want the given order", list)
	}
	got, _ := s.Get(t.Context(), tk.ID)
	want := "AC-2  Wrong password shows an error\nAC-1  Valid login lands on the dashboard\n      within two seconds"
	if content, ok := criteriaContent(got.Body); !ok || strings.TrimSpace(content) != want {
		t.Errorf("section = %q, want %q", content, want)
	}
	if len(got.Criteria) != 2 {
		t.Errorf("Get carries %d criteria, want 2", len(got.Criteria))
	}

	// An empty list removes the section and the rows.
	if _, err := s.SetCriteria(t.Context(), tk.ID, "pm", nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(t.Context(), tk.ID)
	if _, ok := criteriaContent(got.Body); ok || len(criteriaOf(t, s, tk.ID)) != 0 {
		t.Errorf("empty list left section or rows: %q", got.Body)
	}
}

func TestCriteria_MarkdownBodyFillsTheTable(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "md", Body: "## Acceptance criteria\n\n- **AC-1**: users log in\n- AC-2. errors show\n  in red\n"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	list := criteriaOf(t, s, tk.ID)
	if got := criterionKeys(list); !slices.Equal(got, []string{"AC-1", "AC-2"}) || list[1].Text != "errors show\nin red" {
		t.Fatalf("rows from create = %+v", list)
	}

	body := "## Acceptance criteria\n\n| Key | Criterion |\n|---|---|\n| AC-1 | users log in |\n| AC-3 | logout works |\n"
	if _, err := s.Update(t.Context(), tk.ID, Patch{Body: &body}); err != nil {
		t.Fatal(err)
	}
	if got := criterionKeys(criteriaOf(t, s, tk.ID)); !slices.Equal(got, []string{"AC-1", "AC-3"}) {
		t.Fatalf("rows from a table = %v, want AC-1 AC-3", got)
	}

	// Prose without AC-n lines does not wipe the rows; removing the section does.
	prose := "## Acceptance criteria\n\nWe will decide later.\n"
	if _, err := s.Update(t.Context(), tk.ID, Patch{Body: &prose}); err != nil {
		t.Fatal(err)
	}
	if n := len(criteriaOf(t, s, tk.ID)); n != 2 {
		t.Errorf("prose replaced the rows: %d left", n)
	}
	none := "## Description\n\nnothing"
	if _, err := s.Update(t.Context(), tk.ID, Patch{Body: &none}); err != nil {
		t.Fatal(err)
	}
	if n := len(criteriaOf(t, s, tk.ID)); n != 0 {
		t.Errorf("a removed section left %d rows", n)
	}
}

func TestParseCriteria(t *testing.T) {
	content := "```\nAC-1  plain line\n```\n1. [x] AC-2) numbered, checked\n* __AC-3__ — dashed\ncontinued\n\nnot a criterion\nAC-1 repeated keeps the first\n"
	got := ParseCriteria(content)
	want := []CriterionInput{
		{Key: "AC-1", Text: "plain line"},
		{Key: "AC-2", Text: "numbered, checked"},
		{Key: "AC-3", Text: "dashed\ncontinued"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ParseCriteria = %q, want %q", got, want)
	}
	if round := ParseCriteria(FormatCriteria(want)); !slices.Equal(round, want) {
		t.Errorf("format then parse = %+v, want %+v", round, want)
	}
}

// The section cannot carry a blank line inside a criterion or a leading list mark, so a criterion
// written with PUT comes back from the body a little different. Editing another criterion in the
// description must not count it as changed: it keeps its exact text and its approval.
func TestCriteria_BodyRoundTripKeepsUntouchedCriteria(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "round trip")
	in := []CriterionInput{
		{Key: "AC-1", Text: "Given a list\n\nWhen exported\nThen it arrives"},
		{Key: "AC-2", Text: "- the user sees the banner"},
		{Key: "AC-3", Text: "**bold** stays"},
		{Key: "AC-4", Text: "logout works"},
	}
	if _, err := s.SetCriteria(t.Context(), tk.ID, "pm", in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveCriteria(t.Context(), tk.ID, "alice", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEvent(t.Context(), tk.ID, EventTestsLocked, "", "qa", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(t.Context(), tk.ID)
	body := strings.Replace(got.Body, "AC-4  logout works", "AC-4  logout works everywhere", 1)
	if body == got.Body {
		t.Fatalf("AC-4 line not found in %q", got.Body)
	}
	if _, err := s.Update(t.Context(), tk.ID, Patch{Body: &body}); err != nil {
		t.Fatal(err)
	}
	list := criteriaOf(t, s, tk.ID)
	for i := range 3 {
		if list[i].Text != in[i].Text || list[i].ApprovedBy != "alice" {
			t.Errorf("%s = %q approved by %q, want its exact text and approval kept", list[i].Key, list[i].Text, list[i].ApprovedBy)
		}
	}
	if list[3].Text != "logout works everywhere" || list[3].ApprovedBy != "" {
		t.Errorf("AC-4 = %+v, want the new text without approval", list[3])
	}
	evs := eventsOf(t, s, tk.ID, EventCriteriaChanged)
	if len(evs) != 1 {
		t.Fatalf("criteria_changed events = %v, want 1", evs)
	}
	if keys, _ := evs[0]["keys"].([]any); len(keys) != 1 || keys[0] != "AC-4" {
		t.Errorf("changed keys = %v, want only AC-4", evs[0]["keys"])
	}
	// A real change still counts, also on a criterion whose stored text the section cannot carry.
	body = strings.Replace(body, "the user sees the banner", "the user sees the alert", 1)
	if _, err := s.Update(t.Context(), tk.ID, Patch{Body: &body}); err != nil {
		t.Fatal(err)
	}
	if list = criteriaOf(t, s, tk.ID); list[1].Text != "the user sees the alert" || list[1].ApprovedBy != "" {
		t.Errorf("AC-2 after a real edit = %+v", list[1])
	}
}

func TestCriteria_ApprovalAndChangeAfterLock(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "lock")
	if _, err := s.SetCriteria(t.Context(), tk.ID, "pm", []CriterionInput{{Key: "AC-1", Text: "one"}, {Key: "AC-2", Text: "two"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveCriteria(t.Context(), tk.ID, "alice", []string{"AC-9"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown key: err = %v, want ErrInvalid", err)
	}
	list, err := s.ApproveCriteria(t.Context(), tk.ID, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ApprovedBy != "alice" || c.ApprovedAt == nil {
			t.Fatalf("not approved: %+v", c)
		}
	}
	addCases(t, s, tk.ID, TestCase{Title: "covers one", Covers: []string{"AC-1"}}, TestCase{Title: "covers two", Covers: []string{"AC-2"}})

	// Before Test design locks the tests, a change is only an edit.
	if _, err := s.SetCriteria(t.Context(), tk.ID, "pm", []CriterionInput{{Key: "AC-1", Text: "one!"}, {Key: "AC-2", Text: "two"}}); err != nil {
		t.Fatal(err)
	}
	if evs := eventsOf(t, s, tk.ID, EventCriteriaChanged); len(evs) != 0 {
		t.Fatalf("criteria_changed before the lock: %v", evs)
	}
	list = criteriaOf(t, s, tk.ID)
	if list[0].ApprovedBy != "" || list[1].ApprovedBy != "alice" {
		t.Errorf("approval after a text change = %+v, want AC-1 cleared and AC-2 kept", list)
	}

	if err := s.RecordEvent(t.Context(), tk.ID, EventTestsLocked, "", "qa", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	body := "## Acceptance criteria\n\nAC-1  one!\nAC-2  two, changed\nAC-3  three\n"
	if _, err := s.Update(t.Context(), tk.ID, Patch{Body: &body}); err != nil {
		t.Fatal(err)
	}
	evs := eventsOf(t, s, tk.ID, EventCriteriaChanged)
	if len(evs) != 1 {
		t.Fatalf("criteria_changed events = %v, want 1", evs)
	}
	keys, _ := evs[0]["keys"].([]any)
	cases, _ := evs[0]["cases"].([]any)
	if len(keys) != 2 || keys[0] != "AC-2" || keys[1] != "AC-3" || len(cases) != 1 || cases[0] != "TC-2" {
		t.Errorf("payload = %v, want keys AC-2 AC-3 and case TC-2", evs[0])
	}
	if list = criteriaOf(t, s, tk.ID); list[1].ApprovedBy != "" {
		t.Errorf("changed AC-2 kept its approval: %+v", list[1])
	}
}

func TestCovers_ValidatedAgainstTheCriteria(t *testing.T) {
	s := newTestStore(t)
	tk := newCaseTask(t, s, testWS, "covers")

	// No criteria yet: any AC-n passes, so agents that write cases first keep working.
	addCases(t, s, tk.ID, TestCase{Title: "early", Covers: []string{"AC-7"}})
	if _, err := s.AddTestCases(t.Context(), tk.ID, "qa", []TestCase{{Title: "bad", Covers: []string{"login"}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad covers shape: err = %v, want ErrInvalid", err)
	}

	if _, err := s.SetCriteria(t.Context(), tk.ID, "pm", []CriterionInput{{Key: "AC-1", Text: "one"}, {Key: "AC-2", Text: "two"}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddTestCases(t.Context(), tk.ID, "qa", []TestCase{{Title: "unknown", Covers: []string{"AC-1", "AC-5"}}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "AC-5") || !strings.Contains(err.Error(), "AC-1, AC-2") {
		t.Errorf("unknown covers: err = %v, want ErrInvalid naming AC-5 and the valid keys", err)
	}
	cases := addCases(t, s, tk.ID, TestCase{Title: "ok", Covers: []string{"AC-2", "AC-2"}, Preconditions: "a seeded user"})
	if !slices.Equal(cases[0].Covers, []string{"AC-2"}) || cases[0].Preconditions != "a seeded user" {
		t.Errorf("stored case = %+v", cases[0])
	}
	bad := []string{"AC-9"}
	if _, err := s.UpdateTestCase(t.Context(), tk.ID, cases[0].Key, "qa", TestCasePatch{Covers: &bad}); !errors.Is(err, ErrInvalid) {
		t.Errorf("update with unknown covers: err = %v, want ErrInvalid", err)
	}
}

func TestCovers_DerivedFromTheDescription(t *testing.T) {
	for _, tc := range []struct {
		desc          string
		covers        []string
		preconditions string
	}{
		{"Covers: AC-1, AC-3. Then log in.", []string{"AC-1", "AC-3"}, ""},
		{"Log in.\nCovers AC-2\nPreconditions: a user with 2FA\non staging\n\nSteps: …", []string{}, "a user with 2FA\non staging"},
		{"cubre: AC-4\nprecondiciones: usuario creado", []string{"AC-4"}, "usuario creado"},
		{"No markers here, AC-1 alone.", []string{}, ""},
	} {
		c := TestCase{Description: tc.desc}
		deriveCaseFields(&c)
		if !slices.Equal(c.Covers, tc.covers) || c.Preconditions != tc.preconditions {
			t.Errorf("%q → covers %v preconditions %q, want %v %q", tc.desc, c.Covers, c.Preconditions, tc.covers, tc.preconditions)
		}
	}
	explicit := TestCase{Description: "Covers: AC-1", Covers: []string{"AC-2"}}
	deriveCaseFields(&explicit)
	if !slices.Equal(explicit.Covers, []string{"AC-2"}) {
		t.Errorf("a set field lost to the description: %v", explicit.Covers)
	}
}
