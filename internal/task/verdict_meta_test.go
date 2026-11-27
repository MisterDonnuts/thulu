package task

import (
	"reflect"
	"strings"
	"testing"
)

func TestVerdictMetaReadsTheOptionalFields(t *testing.T) {
	body := `---
verdict: block
reason: needs_decision
headline: "Testing finished: 5 of 12 cases passed, 7 need a person"
ask: Give the agent a card it can pay with.
needs_person:
  - ref: TC-3
    ask: Pay with a real card
    why: real_account
  - {ref: TC-7, ask: Try it on an iPhone, why: phone}
  - Approve the copy change
delta:
  - "new: TC-9"
  - "fixed: TC-2"
next: Human review checks the copy.
---
Body line.
`
	got := VerdictMetaOf(body)
	want := &VerdictMeta{
		Verdict: "block", Reason: "needs_decision",
		Headline: "Testing finished: 5 of 12 cases passed, 7 need a person",
		Ask:      "Give the agent a card it can pay with.",
		NeedsPerson: []NeedsPerson{
			{Ref: "TC-3", Ask: "Pay with a real card", Why: "real_account"},
			{Ref: "TC-7", Ask: "Try it on an iPhone", Why: NeedsPersonOther},
			{Ask: "Approve the copy change", Why: NeedsPersonOther},
		},
		Delta: []string{"new: TC-9", "fixed: TC-2"},
		Next:  "Human review checks the copy.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("meta = %+v\nwant %+v", got, want)
	}
}

// Format never blocks: only verdict is required, and a field of the wrong shape is read as far as
// it goes instead of failing the front matter.
func TestVerdictMetaToleratesOddShapes(t *testing.T) {
	cases := map[string]struct {
		body string
		want VerdictMeta
	}{
		"verdict only": {
			body: "---\nverdict: continue\n---\n## All 12 cases pass\n\nDetails.\n",
			want: VerdictMeta{Verdict: "continue", Headline: "All 12 cases pass"},
		},
		"delta as a string, next as a list": {
			body: "---\nverdict: kick_back\nkick_back_to: dev\ndelta: still open TC-4\nnext: [fix TC-4, rerun]\n---\n",
			want: VerdictMeta{Verdict: "kick_back", KickBackTo: "dev", Delta: "still open TC-4", Next: "fix TC-4; rerun"},
		},
		"needs_person as one mapping, headline too long": {
			body: "---\nverdict: block\nheadline: " + strings.Repeat("word ", 30) + "\nneeds_person: {ref: TC-1, ask: log in}\n---\n",
			want: VerdictMeta{Verdict: "block", Headline: strings.TrimSpace(strings.Repeat("word ", 18)) + "…",
				NeedsPerson: []NeedsPerson{{Ref: "TC-1", Ask: "log in", Why: NeedsPersonOther}}},
		},
		"needs_person of the wrong type": {
			body: "---\nverdict: block\nneeds_person: 3\n---\nTC-3 needs a person\n",
			want: VerdictMeta{Verdict: "block", Headline: "TC-3 needs a person",
				NeedsPerson: []NeedsPerson{{Ask: "3", Why: NeedsPersonOther}}},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := VerdictMetaOf(c.body)
			if got == nil {
				t.Fatal("no meta")
			}
			if len([]rune(got.Headline)) > maxVerdictHeadline {
				t.Errorf("headline has %d runes", len([]rune(got.Headline)))
			}
			if !reflect.DeepEqual(*got, c.want) {
				t.Errorf("meta = %+v\nwant %+v", *got, c.want)
			}
		})
	}
	if VerdictMetaOf("no front matter") != nil {
		t.Error("a body with no verdict has meta")
	}
}

func TestStageVerdictReportsCarryTheirMeta(t *testing.T) {
	_, store := newKickDispatcher(t, 0)
	tk := createIn(t, store, "verify", "")
	ctx := t.Context()
	if _, err := store.AddReport(ctx, tk.ID, SourceAgent, "qa", ReportKindStageVerdict, "",
		"---\nverdict: block\nheadline: Seven cases need a person\n---\n", "i1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddReport(ctx, tk.ID, SourceAgent, "qa", ReportKindAgentIndex, "", "---\nverdict: x\n---\n", "i1"); err != nil {
		t.Fatal(err)
	}
	reports, err := store.ListReports(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || reports[0].VerdictMeta == nil || reports[0].VerdictMeta.Headline != "Seven cases need a person" {
		t.Fatalf("reports = %+v, want the verdict's meta", reports)
	}
	if reports[1].VerdictMeta != nil {
		t.Errorf("an index report carries verdict meta: %+v", reports[1].VerdictMeta)
	}
}
