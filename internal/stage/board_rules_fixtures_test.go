package stage

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// The column editor (web/src/lib/board-rules.ts) mirrors these rules to lock a control before the
// save fails. Both sides judge the boards in this file; the web test checks every mirrored rule
// has a case.
const boardRulesFixtures = "../../web/src/lib/board-rules.fixtures.json"

type boardRulesFile struct {
	Cases []struct {
		Name   string            `json:"name"`
		Error  string            `json:"error"`
		Stages []types.StageInfo `json:"stages"`
	} `json:"cases"`
	NotMirrored map[string]string `json:"not_mirrored"`
}

func loadBoardRules(t *testing.T) boardRulesFile {
	t.Helper()
	raw, err := os.ReadFile(boardRulesFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var f boardRulesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestBoardRulesFixtures_DaemonAgreesWithTheEditor(t *testing.T) {
	for _, c := range loadBoardRules(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			err := ValidateBoard(c.Stages)
			if c.Error == "" {
				if err != nil {
					t.Fatalf("the editor takes this board, the daemon refuses it: %v", err)
				}
				return
			}
			if got := apperr.Code(err); got != "stage.errors."+c.Error {
				t.Fatalf("want stage.errors.%s, got %q (%v)", c.Error, got, err)
			}
		})
	}
}

// A new rule in validate.go or approvals.go must land in the fixtures, mirrored in board-rules.ts,
// or in not_mirrored with the check that covers it.
func TestBoardRulesFixtures_EveryDaemonRuleIsAccountedFor(t *testing.T) {
	f := loadBoardRules(t)
	known := make(map[string]bool, len(f.Cases)+len(f.NotMirrored))
	for _, c := range f.Cases {
		known[c.Error] = true
	}
	for k := range f.NotMirrored {
		known[k] = true
	}
	key := regexp.MustCompile(`"stage\.errors\.([a-z_]+)"`)
	for _, src := range []string{"validate.go", "approvals.go"} {
		raw, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range key.FindAllStringSubmatch(string(raw), -1) {
			if !known[m[1]] {
				t.Errorf("%s: stage.errors.%s has no fixture and no not_mirrored entry", src, m[1])
			}
		}
	}
}
