package stage

import (
	"encoding/json"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const MaxStageApprovals = 16

func encodeApprovals(reqs []types.ApprovalRequirement) (string, error) {
	if len(reqs) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(reqs)
	return string(b), err
}

func decodeApprovals(raw string) ([]types.ApprovalRequirement, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var out []types.ApprovalRequirement
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func validateApprovals(st types.StageInfo, onBoard map[types.Stage]types.StageInfo) error {
	if len(st.RequiredApprovals) > MaxStageApprovals {
		return apperr.Invalid("stage.errors.too_many_approvals",
			"id", st.ID, "count", len(st.RequiredApprovals), "max", MaxStageApprovals)
	}
	keys := make(map[string]bool, len(st.RequiredApprovals))
	for _, req := range st.RequiredApprovals {
		if !boardIDRe.MatchString(req.Key) {
			return apperr.Invalid("stage.errors.approval_key_invalid", "id", st.ID, "key", req.Key)
		}
		if keys[req.Key] {
			return apperr.Invalid("stage.errors.approval_key_duplicate", "id", st.ID, "key", req.Key)
		}
		keys[req.Key] = true
		if req.Label == "" {
			return apperr.Invalid("stage.errors.approval_label_required", "id", st.ID, "key", req.Key)
		}
		if len(req.Label) > maxLabelLen {
			return apperr.Invalid("stage.errors.approval_label_too_long", "id", st.ID, "key", req.Key, "max", maxLabelLen)
		}
		seen := make(map[string]bool, len(req.TaskTypes))
		for _, tt := range req.TaskTypes {
			if seen[tt] {
				return apperr.Invalid("stage.errors.approval_type_duplicate", "id", st.ID, "key", req.Key, "type", tt)
			}
			seen[tt] = true
		}
	}
	switch st.OnChangesRequested {
	case "", types.ChangesResume, types.ChangesKickBack, types.ChangesStay:
	default:
		return apperr.Invalid("stage.errors.on_changes_requested_invalid", "id", st.ID, "mode", st.OnChangesRequested,
			"resume", types.ChangesResume, "kick_back", types.ChangesKickBack, "stay", types.ChangesStay)
	}
	if st.ChangesKickBackTo == "" {
		return nil
	}
	target, ok := onBoard[st.ChangesKickBackTo]
	switch {
	case !ok:
		return apperr.Invalid("stage.errors.changes_kick_back_unknown", "id", st.ID, "stage", st.ChangesKickBackTo)
	case target.ID == st.ID:
		return apperr.Invalid("stage.errors.changes_kick_back_self", "id", st.ID)
	case target.Terminal:
		return apperr.Invalid("stage.errors.changes_kick_back_terminal", "id", st.ID, "stage", st.ChangesKickBackTo)
	case target.Position >= st.Position:
		return apperr.Invalid("stage.errors.changes_kick_back_forward", "id", st.ID, "stage", st.ChangesKickBackTo)
	}
	return nil
}

func ValidateApprovalTypes(board []types.StageInfo, tt []TaskTypeInfo) error {
	offered := make(map[string]bool, len(tt))
	for i := range tt {
		offered[tt[i].ID] = true
	}
	for i := range board {
		st := &board[i]
		for _, req := range st.RequiredApprovals {
			for _, typ := range req.TaskTypes {
				if !offered[typ] {
					return apperr.Invalid("stage.errors.approval_type_unknown",
						"id", st.ID, "key", req.Key, "type", typ)
				}
			}
		}
	}
	return nil
}
