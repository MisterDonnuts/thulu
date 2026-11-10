package chatruntime

import (
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func TestErrorTexts(t *testing.T) {
	texts := errorTexts(apperr.Precondition("chatruntime.errors.no_prompt", "agent", "redactor", "stage", "chat"))
	if !strings.HasPrefix(texts["es"], "**Error del agente:** redactor no puede chatear") {
		t.Errorf("es = %q", texts["es"])
	}
	if !strings.HasPrefix(texts["en"], "**Agent error:** redactor can't chat") {
		t.Errorf("en = %q", texts["en"])
	}
	if strings.Contains(texts["es"], "`") {
		t.Errorf("the message shows literal backticks: %q", texts["es"])
	}
}
