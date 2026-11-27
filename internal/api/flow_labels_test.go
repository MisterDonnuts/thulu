package api

import (
	"net/http"
	"slices"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Installed by an English reader, the board reads in Spanish for a Spanish one; a column a person
// renamed keeps the name they gave it.
func TestIntegration_ThuluFlowReadsInTheReadersLanguage(t *testing.T) {
	t.Parallel()
	_, h, token := newFlowServer(t)
	applyThulu(t, h, token)
	if w := patchJSONAuth(t, h, "/stages/verification?board=thulu", token,
		map[string]any{"label": "QA gate"}); w.Code != http.StatusOK {
		t.Fatalf("rename verification = %d: %s", w.Code, w.Body.String())
	}

	es := map[string]string{"Authorization": "Bearer " + token, "Accept-Language": "es"}
	w := requestWithHeaders(t, h, http.MethodGet, "/boards/thulu/stages", nil, es)
	labels := map[types.Stage]string{}
	for _, st := range decodeAs[[]types.StageInfo](t, w, "stages es") {
		labels[st.ID] = st.Label
	}
	want := map[types.Stage]string{
		"in_progress": "Desarrollo", "review": "Revisión humana", "verification": "QA gate",
	}
	for id, label := range want {
		if labels[id] != label {
			t.Errorf("%s = %q, want %q", id, labels[id], label)
		}
	}

	boards := decodeBoards(t, requestWithHeaders(t, h, http.MethodGet, "/boards", nil, es))
	i := slices.IndexFunc(boards.Boards, func(b stagepkg.BoardInfo) bool { return b.ID == "thulu" })
	if i < 0 || boards.Boards[i].Label != "Flujo Thulu" {
		t.Errorf("boards = %+v", boards.Boards)
	}

	type typesResp struct {
		Types []stagepkg.TaskTypeInfo `json:"task_types"`
	}
	tt := decodeAs[typesResp](t, requestWithHeaders(t, h, http.MethodGet, "/boards/thulu/types", nil, es), "types es")
	j := slices.IndexFunc(tt.Types, func(x stagepkg.TaskTypeInfo) bool { return x.ID == "chore" })
	if j < 0 || tt.Types[j].Label != "Mantenimiento" {
		t.Errorf("types = %+v", tt.Types)
	}
}
