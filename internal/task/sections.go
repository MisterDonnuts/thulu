package task

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type ChecklistItem struct {
	Index   int    `json:"index"`
	Text    string `json:"text"`
	Checked bool   `json:"checked"`
}

type Section struct {
	Slug    string          `json:"slug"`
	Title   string          `json:"title"`
	Content string          `json:"content"`
	Items   []ChecklistItem `json:"items,omitempty"`
}

var canonicalSections = []struct {
	Slug  string
	Title string
}{
	{"goal", "Goal"},                             //i18n:ignore the heading is the section's address in the body; its slug must not change
	{"context", "Context"},                       //i18n:ignore section heading, see above
	{"definition-of-done", "Definition of Done"}, //i18n:ignore section heading, see above
	{"test-cases", "Test Cases"},                 //i18n:ignore section heading, see above
	{"out-of-scope", "Out of Scope"},             //i18n:ignore section heading, see above
	{"risks", "Risks"},                           //i18n:ignore section heading, see above
	{"notes", "Notes"},                           //i18n:ignore section heading, see above
}

func CanonicalSectionTitle(slug string) string {
	for _, c := range canonicalSections {
		if c.Slug == slug {
			return c.Title
		}
	}
	return ""
}

func canonicalRank(slug string) int {
	for i, c := range canonicalSections {
		if c.Slug == slug {
			return i
		}
	}
	return -1
}

// Must stay in sync with the slugify in web/src/pages/tasks.astro.
func SlugifySection(title string) string {
	var b strings.Builder
	lastHyphen := true
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

var (
	sectionHeadingRe = regexp.MustCompile(`^##\s+(.+?)\s*$`)
	fenceRe          = regexp.MustCompile("^\\s*(```|~~~)")
	checklistRe      = regexp.MustCompile(`^(\s*[-*]\s+)\[([ xX])\]\s*(.*)$`)
)

func ParseSections(body string) (preamble string, sections []Section) {
	lines := strings.Split(body, "\n")
	inFence := false
	var cur *Section
	var buf []string

	flush := func() {
		content := strings.Trim(strings.Join(buf, "\n"), "\n")
		if cur == nil {
			preamble = content
		} else {
			cur.Content = content
			cur.Items = parseChecklist(content)
			sections = append(sections, *cur)
		}
		buf = nil
	}

	for _, line := range lines {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			buf = append(buf, line)
			continue
		}
		if !inFence {
			if m := sectionHeadingRe.FindStringSubmatch(line); m != nil {
				flush()
				cur = &Section{Title: m[1], Slug: SlugifySection(m[1])}
				continue
			}
		}
		buf = append(buf, line)
	}
	flush()
	return preamble, sections
}

func parseChecklist(content string) []ChecklistItem {
	var items []ChecklistItem
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := checklistRe.FindStringSubmatch(line); m != nil {
			items = append(items, ChecklistItem{
				Index:   len(items) + 1,
				Text:    m[3],
				Checked: m[2] == "x" || m[2] == "X",
			})
		}
	}
	return items
}

func serializeSections(preamble string, sections []Section) string {
	var parts []string
	if strings.TrimSpace(preamble) != "" {
		parts = append(parts, strings.Trim(preamble, "\n"))
	}
	for _, sec := range sections {
		block := "## " + sec.Title
		if strings.TrimSpace(sec.Content) != "" {
			block += "\n\n" + strings.Trim(sec.Content, "\n")
		}
		parts = append(parts, block)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func SetSection(body, slug, title, content string) (string, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "", apperr.Invalid("task.errors.section_slug_required")
	}
	if title == "" {
		title = CanonicalSectionTitle(slug)
	}
	preamble, sections := ParseSections(body)

	for i := range sections {
		if sections[i].Slug == slug {
			sections[i].Content = content
			if title != "" {
				sections[i].Title = title
			}
			return serializeSections(preamble, sections), nil
		}
	}

	if title == "" {
		return "", apperr.Invalid("task.errors.section_needs_title", "slug", slug)
	}
	neu := Section{Slug: slug, Title: title, Content: content}
	rank := canonicalRank(slug)
	if rank < 0 {
		sections = append(sections, neu)
	} else {
		pos := len(sections)
		for i, sec := range sections {
			r := canonicalRank(sec.Slug)
			if r < 0 || r > rank {
				pos = i
				break
			}
		}
		sections = append(sections[:pos:pos], append([]Section{neu}, sections[pos:]...)...)
	}
	return serializeSections(preamble, sections), nil
}

func RemoveSection(body, slug string) (string, bool) {
	preamble, sections := ParseSections(body)
	kept := sections[:0]
	found := false
	for _, sec := range sections {
		if sec.Slug == slug {
			found = true
			continue
		}
		kept = append(kept, sec)
	}
	if !found {
		return body, false
	}
	return serializeSections(preamble, kept), true
}

func SetChecklistItem(body, slug string, index int, checked bool, note string) (string, error) {
	preamble, sections := ParseSections(body)
	for i := range sections {
		if sections[i].Slug != slug {
			continue
		}
		content, total, ok := toggleChecklistLine(sections[i].Content, index, checked, note)
		if !ok {
			return "", apperr.Invalid("task.errors.checklist_index_not_found", "slug", slug, "count", total, "index", index)
		}
		sections[i].Content = content
		sections[i].Items = parseChecklist(content)
		return serializeSections(preamble, sections), nil
	}
	return "", apperr.NotFound("task.errors.section_not_found", "slug", slug)
}

func toggleChecklistLine(content string, index int, checked bool, note string) (string, int, bool) {
	lines := strings.Split(content, "\n")
	n := 0
	inFence := false
	for j, line := range lines {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := checklistRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n++
		if n != index {
			continue
		}
		mark := " "
		if checked {
			mark = "x"
		}
		text := m[3]
		if strings.TrimSpace(note) != "" {
			text += " — " + strings.TrimSpace(note)
		}
		lines[j] = m[1] + "[" + mark + "] " + text
		return strings.Join(lines, "\n"), n, true
	}
	return "", n, false
}
