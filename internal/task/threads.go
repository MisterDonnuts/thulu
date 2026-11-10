package task

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func (c *Comment) ThreadID() string {
	if c.ParentID != "" {
		return c.ParentID
	}
	return c.ID
}

type Thread struct {
	Root    Comment
	Replies []Comment
}

// comments must be oldest first, as ListComments returns them; a reply seen before its root becomes
// a root.
func Threads(comments []Comment) []Thread {
	threads := make([]Thread, 0, len(comments))
	at := make(map[string]int, len(comments))
	for i := range comments {
		c := comments[i]
		if c.ParentID != "" {
			if idx, ok := at[c.ParentID]; ok {
				threads[idx].Replies = append(threads[idx].Replies, c)
				continue
			}
		}
		at[c.ID] = len(threads)
		threads = append(threads, Thread{Root: c})
	}
	return threads
}

func FindThread(threads []Thread, rootID string) (Thread, bool) {
	for i := range threads {
		if threads[i].Root.ID == rootID {
			return threads[i], true
		}
	}
	return Thread{}, false
}

func (th Thread) Messages() []Comment {
	return append([]Comment{th.Root}, th.Replies...)
}

func (th Thread) Title(maxRunes int) string {
	line := ""
	for l := range strings.SplitSeq(th.Root.Body, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	if r := []rune(line); maxRunes > 0 && len(r) > maxRunes {
		line = strings.TrimSpace(string(r[:maxRunes-1])) + "…"
	}
	return line
}

func (th Thread) LastAgent() string {
	msgs := th.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Source == SourceAgent {
			return msgs[i].Author
		}
	}
	return ""
}

func (th Thread) AgentTurnsSincePerson() int {
	n := 0
	msgs := th.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Source == SourceUser {
			return n
		}
		if msgs[i].Source == SourceAgent {
			n++
		}
	}
	return n
}

func (th Thread) Since(agent string) []Comment {
	msgs := th.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Source == SourceAgent && msgs[i].Author == agent {
			return msgs[i+1:]
		}
	}
	return msgs
}

func (c *Comment) Speaker() string { return c.SpeakerIn(i18n.DaemonLocale()) }

// Source comes from the credential that posted the comment, so an agent cannot read as a person.
func (c *Comment) SpeakerIn(locale string) string {
	if c.Source == SourceAgent {
		return i18n.T(locale, "task.labels.agent_speaker", "name", c.Author)
	}
	if c.Source == SourceUser && c.Author == auth.DevModeAuthorName {
		return i18n.T(locale, "task.labels.operator_speaker")
	}
	return c.Author
}

func Transcript(msgs []Comment, skipID string, maxBytes int) string {
	return TranscriptIn(i18n.DaemonLocale(), msgs, skipID, maxBytes)
}

func TranscriptIn(locale string, msgs []Comment, skipID string, maxBytes int) string {
	var b strings.Builder
	for i := range msgs {
		c := &msgs[i]
		if c.ID == skipID || c.Source == SourceSystem {
			continue
		}
		fmt.Fprintf(&b, "**%s:** %s\n\n", c.SpeakerIn(locale), strings.TrimSpace(c.Body))
	}
	text := b.String()
	if len(text) <= maxBytes {
		return text
	}
	cut := len(text) - maxBytes
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	return i18n.T(locale, "task.prompts.earlier_messages_trimmed") + text[cut:]
}
