package strx_test

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/strx"
)

func TestSplitCSVTrimsAndDropsBlanks(t *testing.T) {
	got := strx.SplitCSV("repo, ,workflow")
	if len(got) != 2 || got[0] != "repo" || got[1] != "workflow" {
		t.Fatalf("SplitCSV = %v, want [repo workflow]", got)
	}
}

func TestSplitCSVIsNilWhenThereIsNothing(t *testing.T) {
	for _, in := range []string{"", "   ", ",", "  ,  ,  "} {
		if got := strx.SplitCSV(in); got != nil {
			t.Errorf("SplitCSV(%q) = %v, want nil", in, got)
		}
	}
}
