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
	// How many times a task may be sent back (auditor, stage verdict, changes requested) before it
	// is blocked for a person. 0 means no cap.
	MaxKickBacks int `json:"max_kick_backs,omitempty"`
	// The Workshop flow the board was installed from; nil on boards made by hand. Only the
	// Workshop writes it; the board PATCH leaves it alone. A pointer keeps BoardInfo cheap to
	// copy in the many loops over BoardList.
	Flow *BoardFlow `json:"flow,omitempty"`
}

// Stored in boards.flow, flow_revision and flow_hash.
type BoardFlow struct {
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	Hash     string `json:"hash,omitempty"`
	// Edited says the board no longer matches Hash: someone changed its columns or types since
	// the Workshop wrote it. GET /boards fills it on read; it is never stored.
	Edited bool `json:"edited,omitempty"`
}

func (b BoardInfo) flowColumns() (name string, revision int, hash string) {
	if b.Flow == nil {
		return "", 0, ""
	}
	return b.Flow.Name, b.Flow.Revision, b.Flow.Hash
}

// A cap above this is no cap a person would choose; it is a typo.
const MaxKickBacksLimit = 20

var (
	flowNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	flowHashRe = regexp.MustCompile(`^[0-9a-f]{0,128}$`)
)

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
	if b.MaxKickBacks < 0 || b.MaxKickBacks > MaxKickBacksLimit {
		return apperr.Invalid("stage.errors.max_kick_backs_invalid", "id", b.ID, "value", b.MaxKickBacks, "max", MaxKickBacksLimit)
	}
	name, revision, hash := b.flowColumns()
	if b.Flow != nil && !flowNameRe.MatchString(name) {
		return apperr.Invalid("stage.errors.flow_invalid", "id", b.ID, "flow", name)
	}
	if revision < 0 || !flowHashRe.MatchString(hash) {
		return apperr.Invalid("stage.errors.flow_revision_invalid", "id", b.ID)
	}
	return nil
}

// The dispatcher reads per-board settings through the read-only Boards view, which carries them in
// BoardList.
func BoardInfoOf(b Boards, id string) (BoardInfo, bool) {
	if b == nil {
		return BoardInfo{}, false
	}
	list := b.BoardList()
	for i := range list {
		if list[i].ID == id {
			return list[i], true
		}
	}
	return BoardInfo{}, false
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
