package github

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// The GitHub Actions app: what a job's check run reports as its app when only the Actions API was
// readable.
const actionsAppID = 15368

// CheckResult is one check run, or one Actions job when the token could not read check runs.
type CheckResult struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	AppID      int64  `json:"-"`
}

// CommitStatus is one context of the commit status API, which older integrations still report to.
type CommitStatus struct {
	Context string `json:"context"`
	State   string `json:"state"`
}

// RequiredContext is a check a branch requires before a merge. AppID 0 accepts any app.
type RequiredContext struct {
	Context string
	AppID   int64
}

// Requirement is what the base branch asks of a pull request, from branch protection and rulesets
// together.
type Requirement struct {
	Contexts []RequiredContext
	// The branch must be up to date with the base before merging.
	Strict bool
}

// Two sources pinning one context to different apps accept either.
func (r *Requirement) add(c RequiredContext) {
	for i := range r.Contexts {
		if r.Contexts[i].Context == c.Context {
			if r.Contexts[i].AppID != c.AppID {
				r.Contexts[i].AppID = 0
			}
			return
		}
	}
	r.Contexts = append(r.Contexts, c)
}

// Required states. Only passed and none let a merge through.
const (
	RequiredNone    = "none"
	RequiredPassed  = "passed"
	RequiredPending = "pending"
	RequiredFailing = "failing"
	RequiredUnknown = "unknown"
)

// RequiredStatus is how a pull request stands against the checks its base branch requires.
type RequiredStatus struct {
	State    string   `json:"state"`
	Required []string `json:"required,omitempty"`
	Failing  []string `json:"failing,omitempty"`
	// Running, queued or not reported yet; Missing repeats the ones not reported at all.
	Pending []string `json:"pending,omitempty"`
	Missing []string `json:"missing,omitempty"`
	// The base requires an up-to-date branch and this one is behind it.
	Behind bool `json:"behind,omitempty"`
}

// Holds says whether a merge has to wait: a required check is not green, the branch is behind a
// base that asks for it, or the requirement could not be read.
func (r *RequiredStatus) Holds() bool {
	switch r.State {
	case RequiredPending, RequiredFailing, RequiredUnknown:
		return true
	}
	return r.Behind
}

type branchResponse struct {
	Protection struct {
		Enabled              bool `json:"enabled"`
		RequiredStatusChecks *struct {
			Contexts []string `json:"contexts"`
			Checks   []struct {
				Context string `json:"context"`
				AppID   *int64 `json:"app_id"`
			} `json:"checks"`
			Strict bool `json:"strict"`
		} `json:"required_status_checks"`
	} `json:"protection"`
}

type branchRule struct {
	Type       string `json:"type"`
	Parameters struct {
		RequiredStatusChecks []struct {
			Context       string `json:"context"`
			IntegrationID int64  `json:"integration_id"`
		} `json:"required_status_checks"`
		Strict bool `json:"strict_required_status_checks_policy"`
	} `json:"parameters"`
}

// BranchRequirement reads the checks branch requires from its branch protection (GET
// /branches/{b}, which the App reads with contents access) and from the rulesets that apply to it
// (GET /rules/branches/{b}, metadata access). A source the token may not read counts as asking for
// nothing; any other failure is an error, since the requirement is then unknown.
func BranchRequirement(ctx context.Context, token, owner, repo, branch string) (Requirement, error) {
	if token == "" {
		return Requirement{}, apperr.Invalid("github.errors.token_required")
	}
	var req Requirement
	var b branchResponse
	endpoint := reposURL(owner, repo) + "/branches/" + url.PathEscape(branch)
	if err := readOptional(ctx, token, endpoint, &b); err != nil {
		return Requirement{}, err
	}
	if rsc := b.Protection.RequiredStatusChecks; b.Protection.Enabled && rsc != nil {
		req.Strict = rsc.Strict
		for _, c := range rsc.Checks {
			rc := RequiredContext{Context: c.Context}
			if c.AppID != nil && *c.AppID > 0 {
				rc.AppID = *c.AppID
			}
			req.add(rc)
		}
		// contexts repeats checks without their apps: only a name checks lacks adds anything.
		for _, c := range rsc.Contexts {
			if !slices.ContainsFunc(req.Contexts, func(r RequiredContext) bool { return r.Context == c }) {
				req.add(RequiredContext{Context: c})
			}
		}
	}
	if err := addRulesetChecks(ctx, token, owner, repo, branch, &req); err != nil {
		return Requirement{}, err
	}
	return req, nil
}

func addRulesetChecks(ctx context.Context, token, owner, repo, branch string, req *Requirement) error {
	//i18n:ignore API path and query
	base := reposURL(owner, repo) + "/rules/branches/" + url.PathEscape(branch) + "?per_page=100&page="
	for page := 1; page <= maxPages; page++ {
		var rules []branchRule
		if err := readOptional(ctx, token, base+strconv.Itoa(page), &rules); err != nil {
			return err
		}
		for i := range rules {
			if rules[i].Type != "required_status_checks" {
				continue
			}
			p := rules[i].Parameters
			req.Strict = req.Strict || p.Strict
			for _, c := range p.RequiredStatusChecks {
				req.add(RequiredContext{Context: c.Context, AppID: c.IntegrationID})
			}
		}
		if len(rules) < 100 {
			break
		}
	}
	return nil
}

// A 403 or 404 leaves out unchanged: the token may not read that source, or the repository has
// none of it.
func readOptional(ctx context.Context, token, endpoint string, out any) error {
	err := callLimit(ctx, token, http.MethodGet, endpoint, nil, out, readLimit)
	if k := apperr.KindOf(err); err != nil && (k == apperr.KindForbidden || k == apperr.KindNotFound) {
		return nil
	}
	return err
}

// CommitStatuses reads the combined commit status of sha: the latest state of each context.
func CommitStatuses(ctx context.Context, token, owner, repo, sha string) ([]CommitStatus, error) {
	var out struct {
		Statuses []CommitStatus `json:"statuses"`
	}
	//i18n:ignore API path and query
	endpoint := reposURL(owner, repo) + "/commits/" + url.PathEscape(sha) + "/status?per_page=100"
	if err := readOptional(ctx, token, endpoint, &out); err != nil {
		return nil, err
	}
	return out.Statuses, nil
}

type verdict int

const (
	verdictMissing verdict = iota
	verdictPassed
	verdictPending
	verdictFailing
)

func checkVerdict(c CheckResult) verdict {
	switch c.Status {
	case "queued", "in_progress", "pending", "waiting", "requested":
		return verdictPending
	}
	switch c.Conclusion {
	case "success", "neutral", "skipped":
		return verdictPassed
	case "":
		return verdictPending
	}
	return verdictFailing
}

func statusVerdict(s CommitStatus) verdict {
	switch s.State {
	case "success":
		return verdictPassed
	case "pending":
		return verdictPending
	}
	return verdictFailing
}

// contextVerdict is the worst of what reported under c: a failing run beats a passing rerun of
// another suite, the way GitHub refuses the merge.
func contextVerdict(c RequiredContext, checks []CheckResult, statuses []CommitStatus) verdict {
	worst := verdictMissing
	for i := range checks {
		if checks[i].Name == c.Context && (c.AppID == 0 || checks[i].AppID == 0 || checks[i].AppID == c.AppID) {
			worst = max(worst, checkVerdict(checks[i]))
		}
	}
	if c.AppID == 0 {
		for i := range statuses {
			if statuses[i].Context == c.Context {
				worst = max(worst, statusVerdict(statuses[i]))
			}
		}
	}
	return worst
}

// EvaluateRequired sorts the required contexts by how the head commit's checks and statuses
// reported them. mergeable is MergeableOf the pull request, for the up-to-date rule.
func EvaluateRequired(req Requirement, checks []CheckResult, statuses []CommitStatus, mergeable string) RequiredStatus {
	if len(req.Contexts) == 0 {
		return RequiredStatus{State: RequiredNone, Behind: req.Strict && mergeable == MergeableBehind}
	}
	st := RequiredStatus{State: RequiredPassed, Behind: req.Strict && mergeable == MergeableBehind}
	for _, c := range req.Contexts {
		st.Required = append(st.Required, c.Context)
		switch contextVerdict(c, checks, statuses) {
		case verdictFailing:
			st.Failing = append(st.Failing, c.Context)
		case verdictPending:
			st.Pending = append(st.Pending, c.Context)
		case verdictMissing:
			st.Pending = append(st.Pending, c.Context)
			st.Missing = append(st.Missing, c.Context)
		case verdictPassed:
		}
	}
	switch {
	case len(st.Failing) > 0:
		st.State = RequiredFailing
	case len(st.Pending) > 0:
		st.State = RequiredPending
	}
	return st
}

// needsStatuses: the commit status API is one more call, worth it only for a required context no
// check run answered.
func needsStatuses(req Requirement, checks []CheckResult) bool {
	for _, c := range req.Contexts {
		if c.AppID == 0 && !slices.ContainsFunc(checks, func(r CheckResult) bool { return r.Name == c.Context }) {
			return true
		}
	}
	return false
}

// readRequired evaluates an open pull request against its base branch's required checks. A closed
// or merged one asks nothing. A requirement that cannot be read comes back unknown, which holds.
func readRequired(ctx context.Context, token, owner, repo string, pr *PullRequest, ci CIStatus) RequiredStatus {
	if pr.Merged || pr.State == "closed" || pr.Base.Ref == "" {
		return RequiredStatus{State: RequiredNone}
	}
	req, err := BranchRequirement(ctx, token, owner, repo, pr.Base.Ref)
	if err != nil {
		logx.Warn(err, "github.log.required_checks_read_failed", "repo", owner+"/"+repo)
		return RequiredStatus{State: RequiredUnknown}
	}
	var statuses []CommitStatus
	if needsStatuses(req, ci.Checks) && pr.Head.SHA != "" {
		if statuses, err = CommitStatuses(ctx, token, owner, repo, pr.Head.SHA); err != nil {
			logx.Warn(err, "github.log.required_checks_read_failed", "repo", owner+"/"+repo)
			return RequiredStatus{State: RequiredUnknown}
		}
	}
	return EvaluateRequired(req, ci.Checks, statuses, MergeableOf(pr))
}
