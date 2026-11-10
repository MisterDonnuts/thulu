package stage

import (
	"regexp"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const DefaultBoardID = "default"

const (
	MaxBoards         = 64
	maxDescriptionLen = 280
)

// Ids travel as a Prometheus label, a query parameter and a JSON key; do not loosen this shape.
var boardIDRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

func ValidBoardID(s string) bool { return boardIDRe.MatchString(s) }

type BoardInfo struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Empty means "visible to every tenant" to the visibility rule; only SingleBoard (no database)
	// may leave it empty.
	WorkspaceID string `json:"workspace_id,omitempty"`
	Position    int    `json:"position"`
	// Exactly one per workspace; an install-wide default would hand other tenants a flow they do
	// not own.
	Default    bool `json:"default,omitempty"`
	StageCount int  `json:"stage_count,omitempty"`
	TaskCount  int  `json:"task_count,omitempty"`
	OpenCount  int  `json:"open_count,omitempty"`
}

func ValidateBoardInfo(b BoardInfo) error {
	if !ValidBoardID(b.ID) {
		return apperr.Invalid("stage.errors.invalid_board_id", "id", b.ID)
	}
	if b.Label == "" {
		return apperr.Invalid("stage.errors.board_label_required", "id", b.ID)
	}
	if len(b.Label) > maxLabelLen {
		return apperr.Invalid("stage.errors.board_label_too_long", "id", b.ID, "max", maxLabelLen)
	}
	if len(b.Description) > maxDescriptionLen {
		return apperr.Invalid("stage.errors.board_description_too_long", "id", b.ID, "max", maxDescriptionLen)
	}
	// An unowned board is readable by every tenant (the state migration 4 erased); callers without
	// a workspace use FallbackWorkspaceID.
	if b.WorkspaceID == "" {
		return apperr.Invalid("stage.errors.board_workspace_required", "id", b.ID)
	}
	return nil
}

type Boards interface {
	Board(id string) Registry
	// Every flow question about a specific task must go through BoardFor, not Board.
	BoardFor(boardID, typeID string) Registry
	BoardList() []BoardInfo
	BoardTypes(boardID string) []TaskTypeInfo
	TypeCatalog() []TaskTypeInfo
	DefaultBoardID() string
	// Callers that know the workspace must use this, not DefaultBoardID, or the task lands on a
	// board its workspace cannot see.
	DefaultBoardIDFor(workspaceID string) string
	Catalog() []types.StageInfo
}

type singleBoard struct{ r Registry }

func (s singleBoard) Board(string) Registry { return s.r }

func (s singleBoard) BoardFor(string, string) Registry { return s.r }
func (s singleBoard) BoardTypes(string) []TaskTypeInfo { return nil }
func (s singleBoard) TypeCatalog() []TaskTypeInfo      { return nil }
func (s singleBoard) BoardList() []BoardInfo {
	return []BoardInfo{{
		ID: DefaultBoardID, Label: i18n.Text("stage.labels.default_board"), Position: 10,
		Default: true, StageCount: len(s.r.List()),
	}}
}
func (s singleBoard) DefaultBoardID() string          { return DefaultBoardID }
func (s singleBoard) DefaultBoardIDFor(string) string { return DefaultBoardID }
func (s singleBoard) Catalog() []types.StageInfo      { return s.r.List() }

func SingleBoard(r Registry) Boards {
	if r == nil {
		r = Static()
	}
	return singleBoard{r: r}
}

func StaticBoards() Boards { return SingleBoard(Static()) }
