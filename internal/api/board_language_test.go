package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestBoardReadsInTheReadersLanguage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	seeded := []types.Stage{types.StageBacklog, types.StageInProgress, types.StageDone}
	for _, locale := range []string{"en", "es"} {
		headers := map[string]string{"Accept-Language": locale}
		w := requestWithHeaders(t, h, http.MethodGet, "/boards/default/stages", nil, headers)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: GET stages = %d: %s", locale, w.Code, w.Body.String())
		}
		for _, st := range decodeBoard(t, w) {
			if !slices.Contains(seeded, st.ID) {
				continue
			}
			if want := i18n.T(locale, "stage.labels."+string(st.ID)); st.Label != want {
				t.Errorf("%s: column %s = %q, want %q", locale, st.ID, st.Label, want)
			}
		}
	}

	blocker := createWSTask(t, h, "Blocker")
	blocked := createWSTask(t, h, "Blocked")
	if w := postJSONAuth(t, h, "/tasks/"+blocked+"/dependencies", "",
		map[string]string{"depends_on_task_id": blocker}); w.Code >= 300 {
		t.Fatalf("add dependency = %d: %s", w.Code, w.Body.String())
	}
	for _, locale := range []string{"en", "es"} {
		w := requestWithHeaders(t, h, http.MethodPatch, "/tasks/"+blocked,
			map[string]any{"stage": "in_progress"}, map[string]string{"Accept-Language": locale})
		if w.Code != http.StatusConflict {
			t.Fatalf("%s: blocked move = %d, want 409: %s", locale, w.Code, w.Body.String())
		}
		done := i18n.T(locale, "stage.labels.done")
		want := i18n.T(locale, "api.tasks.errors.blocked_by_dependencies", "stages", done)
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: refusal %s, want it to say %q", locale, w.Body.String(), want)
		}
	}
}
