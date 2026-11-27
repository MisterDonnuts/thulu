package task

import (
	_ "embed"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/evidence"
)

// What a blocked case needs from a person comes from one catalog embedded in the binary: the daemon
// validates needs.kind against it, GET /test-cases/need-kinds serves it, and the UI groups and picks
// actions from it. Adding a kind is one entry here plus its two texts in the catalogs.

//go:embed needs_catalog.json
var needsCatalogJSON []byte

// Actions a need kind may offer a person.
const (
	NeedActionAccess      = "access"
	NeedActionByHand      = "by_hand"
	NeedActionSkip        = "skip"
	NeedActionRerun       = "rerun"
	NeedActionReviewVideo = "review_video"
)

var needActions = []string{NeedActionAccess, NeedActionByHand, NeedActionSkip, NeedActionRerun, NeedActionReviewVideo}

// NeedKind is one entry of the catalog. Order picks a case's main need: the lowest is the hardest
// to get, and the case is grouped under it.
type NeedKind struct {
	Kind     string   `json:"kind"`
	Icon     string   `json:"icon"`
	Order    int      `json:"order"`
	Actions  []string `json:"actions"`
	Secrets  bool     `json:"secrets"`
	TitleKey string   `json:"title_key"`
	HelpKey  string   `json:"help_key"`
}

var needKinds = mustLoadNeedKinds(needsCatalogJSON)

func mustLoadNeedKinds(raw []byte) []NeedKind {
	kinds, err := loadNeedKinds(raw)
	if err != nil {
		panic(err)
	}
	return kinds
}

func loadNeedKinds(raw []byte) ([]NeedKind, error) {
	var file struct {
		Kinds []NeedKind `json:"kinds"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, apperr.Wrap(err, "task.errors.needs_catalog")
	}
	seen := map[string]bool{}
	for _, k := range file.Kinds {
		if k.Kind == "" || seen[k.Kind] || k.TitleKey == "" || k.HelpKey == "" || len(k.Actions) == 0 {
			return nil, apperr.Internal("task.errors.needs_catalog_entry", "kind", k.Kind)
		}
		for _, a := range k.Actions {
			if !slices.Contains(needActions, a) {
				return nil, apperr.Internal("task.errors.needs_catalog_action", "kind", k.Kind, "action", a)
			}
		}
		seen[k.Kind] = true
	}
	sort.SliceStable(file.Kinds, func(i, j int) bool { return file.Kinds[i].Order < file.Kinds[j].Order })
	return file.Kinds, nil
}

// NeedKinds is the catalog, hardest to get first.
func NeedKinds() []NeedKind { return slices.Clone(needKinds) }

func findNeedKind(kind string) (NeedKind, bool) {
	for _, k := range needKinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return NeedKind{}, false
}

func needKindNames() string {
	names := make([]string, len(needKinds))
	for i, k := range needKinds {
		names[i] = k.Kind
	}
	return strings.Join(names, ", ")
}

// Need is one thing a blocked case waits for, as the agent declared it on the run.
type Need struct {
	Kind    string       `json:"kind"`
	Label   string       `json:"label"`
	Secrets []NeedSecret `json:"secrets,omitempty"`
}

// NeedSecret names a secret a person can give in the project's evidence environment so the agent
// can run the case itself. Only the name and what it is for; never a value.
type NeedSecret struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// What the agent tried before giving up on a blocked case.
const (
	TriedNone      = "none"
	TriedSimulated = "simulated"
	TriedPartial   = "partial"
)

const (
	// observed is one sentence; the detail goes in note.
	MaxObservedRunes    = 280
	maxTriedResultRunes = 280
	maxNeedLabelRunes   = 200
	maxNeeds            = 10
)

func validTried(s string) bool {
	return s == "" || s == TriedNone || s == TriedSimulated || s == TriedPartial
}

// checkRunFields trims the structured fields of a run and refuses what a writer must fix.
func checkRunFields(run *TestCaseRun) error {
	run.Observed = strings.TrimSpace(run.Observed)
	if utf8.RuneCountInString(run.Observed) > MaxObservedRunes {
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_observed_too_long", "max", MaxObservedRunes)
	}
	run.TriedResult = strings.TrimSpace(run.TriedResult)
	switch {
	case !validTried(run.Tried):
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_tried", "tried", run.Tried)
	case utf8.RuneCountInString(run.TriedResult) > maxTriedResultRunes:
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_tried_result_too_long", "max", maxTriedResultRunes)
	}
	if len(run.Needs) == 0 {
		run.Needs = []Need{}
		return nil
	}
	if run.Verdict != TestCaseBlocked {
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_needs_not_blocked", "verdict", run.Verdict)
	}
	if len(run.Needs) > maxNeeds {
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_too_many_needs", "max", maxNeeds)
	}
	for i := range run.Needs {
		if err := checkNeed(&run.Needs[i]); err != nil {
			return err
		}
	}
	return nil
}

func checkNeed(n *Need) error {
	n.Kind, n.Label = strings.TrimSpace(n.Kind), strings.TrimSpace(n.Label)
	kind, ok := findNeedKind(n.Kind)
	if !ok {
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_need_kind", "kind", n.Kind, "valid", needKindNames())
	}
	if utf8.RuneCountInString(n.Label) > maxNeedLabelRunes {
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_need_label_too_long", "max", maxNeedLabelRunes)
	}
	if len(n.Secrets) > 0 && !kind.Secrets {
		return apperr.Wrap(ErrInvalid, "task.errors.test_case_need_no_secrets", "kind", n.Kind)
	}
	seen := map[string]bool{}
	for i := range n.Secrets {
		sec := &n.Secrets[i]
		sec.Name, sec.Label = strings.TrimSpace(sec.Name), strings.TrimSpace(sec.Label)
		switch {
		case !evidence.ValidSecretName(sec.Name):
			return apperr.Wrap(ErrInvalid, "task.errors.test_case_need_secret_name", "name", sec.Name)
		case seen[sec.Name]:
			return apperr.Wrap(ErrInvalid, "task.errors.test_case_need_secret_twice", "name", sec.Name)
		case utf8.RuneCountInString(sec.Label) > maxNeedLabelRunes:
			return apperr.Wrap(ErrInvalid, "task.errors.test_case_need_label_too_long", "max", maxNeedLabelRunes)
		}
		seen[sec.Name] = true
	}
	return nil
}
