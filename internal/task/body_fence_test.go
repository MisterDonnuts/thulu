package task

import "testing"

func TestUnwrapMarkdownFence(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"md fence", "```md\n## Goal\n\n- one\n```\n", "## Goal\n\n- one\n"},
		{"markdown tag, tildes, blank edges", "\n\n~~~~ Markdown\n# T\n~~~~\n\n", "# T\n"},
		{"crlf", "```md\r\n## Goal\r\n```\r\n", "## Goal\n"},
		{"closing fence may be longer", "```md\nx\n`````\n", "x\n"},
		{"untagged fence is code", "```\n## Goal\n```\n", "```\n## Goal\n```\n"},
		{"other language is code", "```go\nfunc main() {}\n```\n", "```go\nfunc main() {}\n```\n"},
		{"text after the fence", "```md\n## Goal\n```\nmore\n", "```md\n## Goal\n```\nmore\n"},
		{"text before the fence", "intro\n```md\n## Goal\n```\n", "intro\n```md\n## Goal\n```\n"},
		{"fence closes early", "```md\na\n```\n\n```md\nb\n```\n", "```md\na\n```\n\n```md\nb\n```\n"},
		{"shorter closer does not close", "````md\na\n```\nb\n````\n", "a\n```\nb\n"},
		{"never closed", "```md\n## Goal\n", "```md\n## Goal\n"},
		{"empty", "", ""},
		{"plain body", "## Goal\n- one\n", "## Goal\n- one\n"},
	}
	for _, c := range cases {
		if got := UnwrapMarkdownFence(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
