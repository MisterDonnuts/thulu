package task

import (
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Registering a revoked machine again inserts a new runner id, so the project or profile still
// points at the revoked one: the hint must send the person to repoint them, in both languages.
func TestRunnerRevokedHintRepointsInsteadOfReregistering(t *testing.T) {
	revoked := machinesAB()
	now := time.Now()
	revoked["a"].RevokedAt = &now
	for _, p := range []placement{
		{projectRunners: []string{"a"}, runners: revoked},
		{profileRunner: "a", runners: revoked},
	} {
		_, err := p.decide()
		if apperr.Code(err) != "task.errors.runner_revoked" {
			t.Fatalf("decide err = %v, want runner_revoked", err)
		}
		for locale, places := range map[string][]string{
			"en": {"Where agents run", "Settings → AI engines"},
			"es": {"Dónde corren los agentes", "Configuración → Motores de IA"},
		} {
			msg := apperr.Localize(err, locale)
			if strings.Contains(msg, "thulu runner register") {
				t.Errorf("%s: %q proposes registering again, which never brings the revoked id back", locale, msg)
			}
			for _, want := range append(places, "mac-a") {
				if !strings.Contains(msg, want) {
					t.Errorf("%s: %q does not mention %q", locale, msg, want)
				}
			}
		}
	}
}
