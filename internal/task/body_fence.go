package task

import (
	"regexp"
	"strings"
)

var (
	openMarkdownFence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \t]*(?i:md|markdown)[ \t]*$")
	closeFence        = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \t]*$")
)

func UnwrapMarkdownFence(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	first, last := 0, len(lines)-1
	for first <= last && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	for last >= first && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	if last <= first {
		return body
	}
	open := openMarkdownFence.FindStringSubmatch(lines[first])
	if open == nil {
		return body
	}
	marker := open[1]
	for i := first + 1; i <= last; i++ {
		closer := ""
		if m := closeFence.FindStringSubmatch(lines[i]); len(m) == 2 {
			closer = m[1]
		}
		if len(closer) < len(marker) || closer[0] != marker[0] {
			continue
		}
		if i != last {
			return body
		}
		inner := strings.Join(lines[first+1:last], "\n")
		return strings.TrimRight(inner, " \t\n") + "\n"
	}
	return body
}
