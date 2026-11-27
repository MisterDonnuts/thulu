package workshop

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/builtin"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestFlowLabelsReadInTheViewersLocale(t *testing.T) {
	c, err := LoadCatalog(flowFS(t, 1, nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	f, ok := c.Flow("mini")
	if !ok {
		t.Fatal("mini flow missing")
	}
	// Installed by someone reading English: the stored names are the English ones.
	spec := f.Spec("en", nil, nil)

	board := f.LocalizeBoard(spec.Info, "es")
	if board.Label != "Flujo mini" {
		t.Errorf("board label = %q, want Flujo mini", board.Label)
	}

	stages := spec.Stages
	stages[2].Label = "Shipping dock" // a person renamed ship after the install
	got := f.LocalizeStages(stages, "es")
	want := map[types.Stage]string{"in_progress": "Construir", "ship": "Shipping dock"}
	for _, st := range got {
		if w, ok := want[st.ID]; ok && st.Label != w {
			t.Errorf("%s label = %q, want %q", st.ID, st.Label, w)
		}
	}
	if spec.Stages[1].Label != "Build" {
		t.Errorf("LocalizeStages changed its input: %q", spec.Stages[1].Label)
	}

	// Installed in Spanish and read in English comes back to the English names.
	back := f.LocalizeStages(f.Spec("es", nil, nil).Stages, "en")
	if back[1].Label != "Build" {
		t.Errorf("in_progress read in en = %q, want Build", back[1].Label)
	}
}

func TestBuiltinFlowLabelsInSpanish(t *testing.T) {
	c, err := LoadCatalog(builtin.Workshop())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	f, ok := c.Flow("thulu")
	if !ok {
		t.Fatal("thulu flow missing")
	}
	spec := f.Spec("en", nil, nil)
	stages := f.LocalizeStages(spec.Stages, "es")
	want := map[types.Stage]string{
		"test_design": "Diseño de pruebas", "in_progress": "Desarrollo",
		"verification": "Verificación", "review": "Revisión humana",
	}
	for _, st := range stages {
		if w, ok := want[st.ID]; ok && st.Label != w {
			t.Errorf("%s = %q, want %q", st.ID, st.Label, w)
		}
		if st.ID == "refinement" && st.RequiredApprovals[0].Label != "Definición lista" {
			t.Errorf("refinement approval = %q, want Definición lista", st.RequiredApprovals[0].Label)
		}
	}
	tt := spec.Types
	f.LocalizeTypes(tt, "es")
	if tt[0].Label != "Funcionalidad" {
		t.Errorf("feature type = %q, want Funcionalidad", tt[0].Label)
	}
	if b := f.LocalizeBoard(spec.Info, "es"); b.Label != "Flujo Thulu" {
		t.Errorf("board = %q, want Flujo Thulu", b.Label)
	}
}
