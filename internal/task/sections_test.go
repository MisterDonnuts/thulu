package task

import (
	"strings"
	"testing"
)

const sampleBody = `Intro paragraph before any section.

## Goal

Export the report list to PDF from the dashboard.

## Definition of Done

- [ ] Export button visible on the reports page
- [x] PDF contains all visible columns
- [ ] Works in Chrome and Firefox

## Test Cases

- [ ] Empty report list exports a valid PDF

## Diseño técnico

` + "```" + `
## this heading is inside a fence and must be ignored
- [ ] this checklist line too
` + "```" + `

Custom section content.
`

func TestSlugifySection(t *testing.T) {
	cases := map[string]string{
		"Definition of Done": "definition-of-done",
		"Goal":               "goal",
		"  Test  Cases  ":    "test-cases",
		"Diseño técnico":     "diseño-técnico",
		"Out of Scope!":      "out-of-scope",
	}
	for in, want := range cases {
		if got := SlugifySection(in); got != want {
			t.Errorf("SlugifySection(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSections(t *testing.T) {
	preamble, secs := ParseSections(sampleBody)
	if !strings.Contains(preamble, "Intro paragraph") {
		t.Fatalf("preamble lost: %q", preamble)
	}
	if len(secs) != 4 {
		t.Fatalf("want 4 sections, got %d: %+v", len(secs), secs)
	}
	dod := secs[1]
	if dod.Slug != "definition-of-done" {
		t.Fatalf("slug = %q", dod.Slug)
	}
	if len(dod.Items) != 3 {
		t.Fatalf("want 3 DoD items, got %d", len(dod.Items))
	}
	if !dod.Items[1].Checked || dod.Items[0].Checked {
		t.Errorf("checked state wrong: %+v", dod.Items)
	}
	if dod.Items[2].Index != 3 {
		t.Errorf("index not 1-based sequential: %+v", dod.Items[2])
	}
	custom := secs[3]
	if custom.Slug != "diseño-técnico" {
		t.Fatalf("custom slug = %q", custom.Slug)
	}
	if len(custom.Items) != 0 {
		t.Errorf("fenced checklist leaked: %+v", custom.Items)
	}
	if !strings.Contains(custom.Content, "## this heading is inside a fence") {
		t.Errorf("fenced heading split the section: %q", custom.Content)
	}
}

func TestSetSectionReplaceAndRoundtrip(t *testing.T) {
	out, err := SetSection(sampleBody, "goal", "", "New goal text.")
	if err != nil {
		t.Fatal(err)
	}
	pre, secs := ParseSections(out)
	if !strings.Contains(pre, "Intro paragraph") {
		t.Errorf("preamble lost on rewrite")
	}
	if secs[0].Content != "New goal text." {
		t.Errorf("goal content = %q", secs[0].Content)
	}
	if len(secs) != 4 || secs[1].Slug != "definition-of-done" || len(secs[1].Items) != 3 {
		t.Errorf("sibling sections damaged: %+v", secs)
	}
	if !strings.Contains(out, "## this heading is inside a fence") {
		t.Errorf("fenced block damaged")
	}
}

func TestSetSectionInsertCanonicalOrder(t *testing.T) {
	body := "## Goal\n\nG.\n\n## Risks\n\nR.\n"
	out, err := SetSection(body, "definition-of-done", "", "- [ ] item one")
	if err != nil {
		t.Fatal(err)
	}
	_, secs := ParseSections(out)
	got := []string{secs[0].Slug, secs[1].Slug, secs[2].Slug}
	want := []string{"goal", "definition-of-done", "risks"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if secs[1].Title != "Definition of Done" {
		t.Errorf("canonical title not applied: %q", secs[1].Title)
	}
}

func TestSetSectionCustomNeedsTitle(t *testing.T) {
	if _, err := SetSection("", "my-notes", "", "x"); err == nil {
		t.Fatal("expected error for new custom section without title")
	}
	out, err := SetSection("", "my-notes", "My Notes", "x")
	if err != nil {
		t.Fatal(err)
	}
	_, secs := ParseSections(out)
	if len(secs) != 1 || secs[0].Slug != "my-notes" {
		t.Fatalf("custom section not created: %+v", secs)
	}
}

func TestSetSectionEmptyBody(t *testing.T) {
	out, err := SetSection("", "goal", "", "The goal.")
	if err != nil {
		t.Fatal(err)
	}
	if out != "## Goal\n\nThe goal.\n" {
		t.Fatalf("out = %q", out)
	}
}

func TestSetChecklistItem(t *testing.T) {
	out, err := SetChecklistItem(sampleBody, "definition-of-done", 1, true, "verified in report R3")
	if err != nil {
		t.Fatal(err)
	}
	_, secs := ParseSections(out)
	items := secs[1].Items
	if !items[0].Checked {
		t.Fatalf("item not checked: %+v", items[0])
	}
	if !strings.Contains(items[0].Text, "— verified in report R3") {
		t.Errorf("note not appended: %q", items[0].Text)
	}
	out2, err := SetChecklistItem(out, "definition-of-done", 2, false, "")
	if err != nil {
		t.Fatal(err)
	}
	_, secs2 := ParseSections(out2)
	if secs2[1].Items[1].Checked {
		t.Errorf("item 2 still checked")
	}
	if secs2[1].Items[1].Text != "PDF contains all visible columns" {
		t.Errorf("text mutated: %q", secs2[1].Items[1].Text)
	}
}

func TestSetChecklistItemErrors(t *testing.T) {
	if _, err := SetChecklistItem(sampleBody, "definition-of-done", 9, true, ""); err == nil {
		t.Fatal("expected out-of-range error")
	}
	if _, err := SetChecklistItem(sampleBody, "nope", 1, true, ""); err == nil {
		t.Fatal("expected missing-section error")
	}
}

func TestRemoveSection(t *testing.T) {
	body := "Intro\n\n## Test Cases\n\n- [ ] TC-1: login\n\n## Notes\n\nkeep me\n"
	got, ok := RemoveSection(body, "test-cases")
	if !ok {
		t.Fatal("section not found")
	}
	if want := "Intro\n\n## Notes\n\nkeep me\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if same, ok := RemoveSection(got, "test-cases"); ok || same != got {
		t.Errorf("removing a missing section changed the body: %q, %v", same, ok)
	}
}
