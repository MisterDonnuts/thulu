package task

import (
	"encoding/json"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type Note struct {
	Key   string
	parts []error
}

func NoteOf(key string, args ...any) Note {
	return Note{Key: key, parts: []error{apperr.New(key, args...)}}
}

func (n Note) Add(key string, args ...any) Note {
	n.parts = append(append([]error(nil), n.parts...), apperr.New(key, args...))
	if n.Key == "" {
		n.Key = key
	}
	return n
}

func (n Note) Append(other Note) Note {
	if len(other.parts) == 0 {
		return n
	}
	n.parts = append(append([]error(nil), n.parts...), other.parts...)
	if n.Key == "" {
		n.Key = other.Key
	}
	return n
}

type gap struct{}

func (gap) Error() string { return " " }

func (n Note) Then(other Note) Note {
	if other.Empty() {
		return n
	}
	if n.Empty() {
		return other
	}
	n.parts = append(append([]error(nil), n.parts...), gap{})
	return n.Append(other)
}

func (n Note) Empty() bool { return len(n.parts) == 0 }

func (n Note) Render(locale string) string {
	var b strings.Builder
	for _, p := range n.parts {
		b.WriteString(apperr.Localize(p, locale))
	}
	return b.String()
}

func (n Note) Text() string { return n.Render(i18n.DaemonLocale()) }

func (n Note) Texts() map[string]string {
	out := map[string]string{}
	for _, l := range i18n.Default().Locales() {
		out[l] = n.Render(l)
	}
	return out
}

func encodeTexts(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeTexts(s string) map[string]string {
	if s == "" {
		return nil
	}
	var m map[string]string
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}

func (c *Comment) textFor(locale string) string {
	if t, ok := c.BodyI18n[locale]; ok && t != "" {
		return t
	}
	return c.Body
}
