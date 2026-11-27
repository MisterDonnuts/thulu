// Package deploycontents answers which tasks a deployed commit carries: a task is in the deploy when
// the commit its merge left on the default branch is an ancestor of the deployed one (git
// merge-base --is-ancestor), and it is reverted when the deployed history holds "This reverts
// commit <merge_sha>" after it.
package deploycontents

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Commit struct {
	SHA     string
	Message string
}

type Comparison struct {
	// base is head or one of its ancestors.
	Ancestor bool
	// The commits in base..head, any order. Only read when the caller asked for them.
	Commits []Commit
	// False when Commits stopped at the source's limit.
	Complete bool
}

// Repo is one repository's history, read through GitHub's compare API or the daemon's clone.
type Repo interface {
	Compare(ctx context.Context, base, head string, commits bool) (Comparison, error)
}

type Candidate struct {
	TaskID   string
	Title    string
	Stage    string
	BoardID  string
	MergeSHA string
}

// Included marshals as true, false or "unknown": unknown is a question the source could not answer
// (a commit it does not have, GitHub down), never a no.
type Included int

const (
	IncludedUnknown Included = iota
	IncludedYes
	IncludedNo
)

func (i Included) MarshalJSON() ([]byte, error) {
	switch i {
	case IncludedYes:
		return []byte("true"), nil
	case IncludedNo:
		return []byte("false"), nil
	case IncludedUnknown:
	}
	return []byte(`"unknown"`), nil
}

func (i *Included) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch v {
	case true:
		*i = IncludedYes
	case false:
		*i = IncludedNo
	default:
		*i = IncludedUnknown
	}
	return nil
}

// Reasons a task's inclusion is unknown.
const (
	ReasonNoMergeSHA    = "no_merge_sha"
	ReasonCompareFailed = "compare_failed"
)

type TaskResult struct {
	TaskID   string   `json:"task_id"`
	Title    string   `json:"title"`
	Stage    string   `json:"stage"`
	BoardID  string   `json:"board_id,omitempty"`
	MergeSHA string   `json:"merge_sha,omitempty"`
	Included Included `json:"included"`
	// Already in the previous deploy; only set when the caller gave one.
	AlreadyDeployed bool `json:"already_deployed,omitempty"`
	// A commit after the merge, inside the deployed history, reverts it: the change is not live.
	Reverted bool     `json:"reverted,omitempty"`
	RevertBy []string `json:"revert_shas,omitempty"`
	// The history after the merge was longer than the source reads: a revert may have gone unseen.
	RevertsPartial bool   `json:"reverts_partial,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

type Revert struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	// The commit it reverts, as the message names it (often abbreviated).
	Reverts string `json:"reverts"`
	TaskID  string `json:"task_id,omitempty"`
}

type Report struct {
	DeployedSHA string       `json:"deployed_sha"`
	PreviousSHA string       `json:"previous_sha,omitempty"`
	Tasks       []TaskResult `json:"tasks"`
	// Every revert in previous..deployed; empty without a previous SHA.
	Reverts []Revert `json:"reverts"`
	// The range previous..deployed was longer than the source reads.
	RangePartial bool `json:"range_partial,omitempty"`
	// previous is not an ancestor of deployed: a rollback, or another branch.
	RangeNotLinear bool   `json:"range_not_linear,omitempty"`
	RangeError     string `json:"range_error,omitempty"`
}

// Parallel compares per report: GitHub answers each in a few hundred ms, and 50 tasks one after
// another would keep the agent waiting.
const workers = 4

// Build checks every candidate against deployed. prev is optional. Errors per task or on the range
// end up in the report; Build itself fails only on a bad deployed SHA.
func Build(ctx context.Context, repo Repo, deployed, prev string, cands []Candidate) (Report, error) {
	if deployed == "" {
		return Report{}, apperr.Invalid("deploycontents.errors.sha_required")
	}
	rep := Report{DeployedSHA: deployed, PreviousSHA: prev, Tasks: make([]TaskResult, len(cands)), Reverts: []Revert{}}
	if prev != "" {
		rep.fillRange(ctx, repo, cands)
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			rep.Tasks[i] = checkTask(ctx, repo, deployed, prev, cands[i])
		}()
	}
	wg.Wait()
	return rep, nil
}

func (rep *Report) fillRange(ctx context.Context, repo Repo, cands []Candidate) {
	cmp, err := repo.Compare(ctx, rep.PreviousSHA, rep.DeployedSHA, true)
	if err != nil {
		rep.RangeError = err.Error()
		return
	}
	rep.RangeNotLinear = !cmp.Ancestor
	rep.RangePartial = !cmp.Complete
	for _, c := range cmp.Commits {
		reverted := RevertedSHA(c.Message)
		if reverted == "" {
			continue
		}
		r := Revert{SHA: c.SHA, Subject: subject(c.Message), Reverts: reverted}
		for i := range cands {
			if sameCommit(cands[i].MergeSHA, reverted) {
				r.TaskID = cands[i].TaskID
				break
			}
		}
		rep.Reverts = append(rep.Reverts, r)
	}
}

func checkTask(ctx context.Context, repo Repo, deployed, prev string, c Candidate) TaskResult {
	res := TaskResult{TaskID: c.TaskID, Title: c.Title, Stage: c.Stage, BoardID: c.BoardID, MergeSHA: c.MergeSHA}
	if c.MergeSHA == "" {
		res.Reason = ReasonNoMergeSHA
		return res
	}
	// One compare answers both: whether the merge is in, and what came after it.
	cmp, err := repo.Compare(ctx, c.MergeSHA, deployed, true)
	if err != nil {
		res.Reason, res.Detail = ReasonCompareFailed, err.Error()
		return res
	}
	if !cmp.Ancestor {
		res.Included = IncludedNo
		return res
	}
	res.Included = IncludedYes
	res.RevertBy = standingReverts(c.MergeSHA, revertsIn(cmp.Commits), 0)
	res.Reverted = len(res.RevertBy) > 0
	res.RevertsPartial = !cmp.Complete && !res.Reverted
	if prev != "" {
		if before, err := repo.Compare(ctx, c.MergeSHA, prev, false); err == nil {
			res.AlreadyDeployed = before.Ancestor
		}
	}
	return res
}

// A revert chain longer than this is not followed further: past it a revert counts as standing.
const maxRevertChain = 8

func revertsIn(commits []Commit) []Commit {
	var out []Commit
	for _, c := range commits {
		if RevertedSHA(c.Message) != "" {
			out = append(out, c)
		}
	}
	return out
}

// standingReverts lists the commits among reverts that revert target and were not themselves
// reverted later: reverting the revert ("Reapply ...") puts the change back.
func standingReverts(target string, reverts []Commit, depth int) []string {
	var out []string
	for _, c := range reverts {
		if !sameCommit(target, RevertedSHA(c.Message)) {
			continue
		}
		if depth < maxRevertChain && len(standingReverts(c.SHA, reverts, depth+1)) > 0 {
			continue
		}
		out = append(out, c.SHA)
	}
	return out
}

// git revert writes "This reverts commit <full sha>."; people shorten it by hand.
//
//i18n:ignore git's fixed revert message, matched and never shown
var revertRe = regexp.MustCompile(`(?m)This reverts commit ([0-9a-fA-F]{7,40})`)

// RevertedSHA returns the commit a revert's message names, or "".
func RevertedSHA(message string) string {
	m := revertRe.FindStringSubmatch(message)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// Either may be abbreviated; 7 characters is git's shortest default.
func sameCommit(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if len(a) < 7 || len(b) < 7 {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func subject(message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return line
}
