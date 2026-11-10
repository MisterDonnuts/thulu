package chat_test

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/chat"
)

func TestMessageInLocale(t *testing.T) {
	m := &chat.Message{Text: "**Agent error:** boom"}
	if got := m.InLocale("es"); got != m {
		t.Errorf("a message without renderings was copied: %+v", got)
	}
	m.SetTexts(map[string]string{"en": "**Agent error:** boom", "es": "**Error del agente:** explotó"})
	if got := m.InLocale("es").Text; got != "**Error del agente:** explotó" {
		t.Errorf("es text = %q", got)
	}
	if got := m.InLocale("fr").Text; got != "**Agent error:** boom" {
		t.Errorf("a locale with no rendering got %q, want the stored text", got)
	}
	if m.Text != "**Agent error:** boom" {
		t.Errorf("InLocale changed the stored message: %q", m.Text)
	}
	var none *chat.Message
	if none.InLocale("es") != nil {
		t.Error("nil message is not nil in a locale")
	}
}
