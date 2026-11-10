package stage

import (
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func okBoard() []types.StageInfo {
	return []types.StageInfo{
		{ID: "start", Label: "Start", Position: 10, Initial: true},
		{ID: "end", Label: "End", Position: 20, Terminal: true},
	}
}

func TestValidateBoardAcceptsDefaults(t *testing.T) {
	if err := ValidateBoard(Defaults()); err != nil {
		t.Fatalf("the shipped board must validate: %v", err)
	}
	if err := ValidateBoard(okBoard()); err != nil {
		t.Fatalf("minimal board must validate: %v", err)
	}
}

func TestValidateBoardMatrix(t *testing.T) {
	huge := make([]types.StageInfo, MaxBoardStages+1)
	for i := range huge {
		huge[i] = types.StageInfo{ID: types.Stage("s" + strings.Repeat("x", i%20)), Label: "S"}
	}

	cases := []struct {
		name    string
		mutate  func([]types.StageInfo) []types.StageInfo
		wantErr string
	}{
		{"empty board", func([]types.StageInfo) []types.StageInfo { return nil }, "at least one stage"},
		{"too many stages", func([]types.StageInfo) []types.StageInfo { return huge }, "too many stages"},
		{"malformed id", func(b []types.StageInfo) []types.StageInfo { b[0].ID = "Bad-Id"; return b }, "invalid stage id"},
		{"reserved virtual id", func(b []types.StageInfo) []types.StageInfo { b[0].ID = "mention"; return b }, "reserved"},
		{"reserved pseudo id", func(b []types.StageInfo) []types.StageInfo { b[0].ID = "archived"; return b }, "reserved"},
		{"duplicate id", func(b []types.StageInfo) []types.StageInfo { b[1].ID = b[0].ID; return b }, "duplicate"},
		{"missing label", func(b []types.StageInfo) []types.StageInfo { b[0].Label = ""; return b }, "needs a label"},
		{"label too long", func(b []types.StageInfo) []types.StageInfo {
			b[0].Label = strings.Repeat("x", maxLabelLen+1)
			return b
		}, "too long"},
		{"non-hex color", func(b []types.StageInfo) []types.StageInfo { b[0].Color = "red"; return b }, "not a hex color"},
		{"css injection color", func(b []types.StageInfo) []types.StageInfo {
			b[0].Color = "#fff;background:url(x)"
			return b
		}, "not a hex color"},
		{"virtual flag persisted", func(b []types.StageInfo) []types.StageInfo { b[0].Virtual = true; return b }, "fixed in code"},
		{"bad wip_group", func(b []types.StageInfo) []types.StageInfo { b[0].WipGroup = "hustle"; return b }, "wip_group"},
		{"initial and terminal", func(b []types.StageInfo) []types.StageInfo { b[0].Terminal = true; return b }, "both initial and terminal"},
		{"terminal auto-advance", func(b []types.StageInfo) []types.StageInfo { b[1].AutoAdvance = true; return b }, "cannot auto-advance"},
		{"bad approval key", func(b []types.StageInfo) []types.StageInfo {
			b[0].RequiredApprovals = []types.ApprovalRequirement{{Key: "Design-OK", Label: "Design"}}
			return b
		}, "approval key"},
		{"duplicate approval key", func(b []types.StageInfo) []types.StageInfo {
			b[0].RequiredApprovals = []types.ApprovalRequirement{{Key: "qa", Label: "QA"}, {Key: "qa", Label: "QA again"}}
			return b
		}, "twice"},
		{"approval without label", func(b []types.StageInfo) []types.StageInfo {
			b[0].RequiredApprovals = []types.ApprovalRequirement{{Key: "qa"}}
			return b
		}, "needs a label"},
		{"unknown changes mode", func(b []types.StageInfo) []types.StageInfo { b[0].OnChangesRequested = "panic"; return b }, "on_changes_requested"},
		{"kick back off board", func(b []types.StageInfo) []types.StageInfo { b[0].ChangesKickBackTo = "nowhere"; return b }, "not on this board"},
		{"kick back to itself", func(b []types.StageInfo) []types.StageInfo { b[0].ChangesKickBackTo = "start"; return b }, "itself"},
		{"kick back to terminal", func(b []types.StageInfo) []types.StageInfo { b[0].ChangesKickBackTo = "end"; return b }, "work finishes"},
		{"kick back forward", func(b []types.StageInfo) []types.StageInfo {
			b = append(b, types.StageInfo{ID: "later", Label: "Later", Position: 15})
			b[0].ChangesKickBackTo = "later"
			return b
		}, "forward"},
		{"no initial", func(b []types.StageInfo) []types.StageInfo { b[0].Initial = false; return b }, "exactly one initial"},
		{"two initials", func(b []types.StageInfo) []types.StageInfo {
			b[1].Initial = true
			b[1].Terminal = false
			b = append(b, types.StageInfo{ID: "t", Label: "T", Terminal: true})
			return b
		}, "exactly one initial"},
		{"no terminal", func(b []types.StageInfo) []types.StageInfo { b[1].Terminal = false; return b }, "at least one terminal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateBoard(c.mutate(okBoard()))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, c.wantErr)
			}
		})
	}
}

func TestValidateBoardAllowsGateWithAutoAdvance(t *testing.T) {
	b := okBoard()
	b[0].BlockedByFlags = true
	b[0].AutoAdvance = true
	b[0].RequiredApprovals = []types.ApprovalRequirement{{Key: "qa", Label: "QA"}}
	if err := ValidateBoard(b); err != nil {
		t.Fatalf("gate + auto_advance must validate: %v", err)
	}
}

func TestValidateApprovalTypes(t *testing.T) {
	b := okBoard()
	b[0].RequiredApprovals = []types.ApprovalRequirement{{Key: "design", Label: "Design", TaskTypes: []string{"feature"}}}
	if err := ValidateApprovalTypes(b, []TaskTypeInfo{{ID: "feature", Label: "Feature"}}); err != nil {
		t.Fatalf("offered type must validate: %v", err)
	}
	err := ValidateApprovalTypes(b, []TaskTypeInfo{{ID: "bug", Label: "Bug"}})
	if err == nil || !strings.Contains(err.Error(), "does not offer") {
		t.Fatalf("a requirement naming a type the board lacks must fail, got %v", err)
	}
	if err := ValidateBoardTypes(b, nil); err == nil {
		t.Fatal("ValidateBoardTypes must refuse dropping a type a requirement names")
	}
}

func TestApprovalRequirementResetDefault(t *testing.T) {
	got, err := decodeApprovals(`[{"key":"qa","label":"QA"},{"key":"ux","label":"UX","reset_on_change":false}]`)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].ResetOnChange || got[1].ResetOnChange {
		t.Errorf("reset_on_change = %v/%v, want true/false", got[0].ResetOnChange, got[1].ResetOnChange)
	}
	if empty, _ := decodeApprovals("[]"); empty != nil {
		t.Errorf("empty array must decode to nil, got %#v", empty)
	}
}
