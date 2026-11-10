package tokenopt

import (
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Rendered in the source locale regardless of OCTOPUS_LOCALE: markers like "Question:" are part of
// the delegation contract the CLI and evals rely on.

func Instructions() string {
	return i18n.T(i18n.SourceLocale, "tokenopt.prompts.bulk_reader")
}

func UserMessage(req Request) string {
	var b strings.Builder
	for _, f := range req.Files {
		b.WriteString(`<file path="`) //i18n:ignore markup of the delegation contract
		b.WriteString(escapeAttr(f.Path))
		b.WriteString("\">\n")
		b.WriteString(f.Content)
		if !strings.HasSuffix(f.Content, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("</file>\n\n")
	}
	b.WriteString(i18n.T(i18n.SourceLocale, "tokenopt.prompts.question", "question", req.Question))
	return b.String()
}

// File content is deliberately not escaped: the worker reads code as code.
func escapeAttr(s string) string {
	return strings.NewReplacer(`&`, "&amp;", `"`, "&quot;", `<`, "&lt;", `>`, "&gt;").Replace(s)
}
