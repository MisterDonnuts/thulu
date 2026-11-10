package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// A page of check runs or Actions jobs exceeds call's 256 KiB limit.
const readLimit = 2 << 20

const maxPages = 3

const maxWorkflowRuns = 20

type PullRequest struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	MergeableState string `json:"mergeable_state"`
	HTMLURL        string `json:"html_url"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	ChangedFiles       int       `json:"changed_files"`
	Additions          int       `json:"additions"`
	Deletions          int       `json:"deletions"`
	Commits            int       `json:"commits"`
	UpdatedAt          time.Time `json:"updated_at"`
	RequestedReviewers []struct {
		Login string `json:"login"`
	} `json:"requested_reviewers"`
	RequestedTeams []struct {
		Slug string `json:"slug"`
	} `json:"requested_teams"`
}

func GetPR(ctx context.Context, token, owner, repo string, number int) (*PullRequest, error) {
	if token == "" {
		return nil, apperr.Invalid("github.errors.token_required")
	}
	var out PullRequest
	endpoint := reposURL(owner, repo) + "/pulls/" + strconv.Itoa(number)
	if err := callLimit(ctx, token, http.MethodGet, endpoint, nil, &out, readLimit); err != nil {
		return nil, err
	}
	return &out, nil
}

const (
	PRStateOpen   = "open"
	PRStateDraft  = "draft"
	PRStateMerged = "merged"
	PRStateClosed = "closed"
)

func StateOf(pr *PullRequest) string {
	switch {
	case pr.Merged:
		return PRStateMerged
	case pr.State == "closed":
		return PRStateClosed
	case pr.Draft:
		return PRStateDraft
	}
	return PRStateOpen
}

const (
	MergeableReady     = "ready"
	MergeableBlocked   = "blocked"
	MergeableBehind    = "behind"
	MergeableConflicts = "conflicts"
	MergeableUnknown   = "unknown"
)

// GitHub still merges "unstable" (non-required check failed) and "has_hooks", so they map to ready.
// GitHub computes mergeable_state lazily and answers "unknown" until it has.
func MergeableOf(pr *PullRequest) string {
	if pr.Merged || pr.State == "closed" {
		return MergeableUnknown
	}
	switch pr.MergeableState {
	case "clean", "unstable", "has_hooks":
		return MergeableReady
	case "blocked", "draft":
		return MergeableBlocked
	case "behind":
		return MergeableBehind
	case "dirty":
		return MergeableConflicts
	}
	return MergeableUnknown
}

type Review struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	State string `json:"state"`
}

func ListReviews(ctx context.Context, token, owner, repo string, number int) ([]Review, error) {
	if token == "" {
		return nil, apperr.Invalid("github.errors.token_required")
	}
	//i18n:ignore API path and query
	base := reposURL(owner, repo) + "/pulls/" + strconv.Itoa(number) + "/reviews?per_page=100&page="
	var all []Review
	for page := 1; page <= maxPages; page++ {
		var batch []Review
		if err := callLimit(ctx, token, http.MethodGet, base+strconv.Itoa(page), nil, &batch, readLimit); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

const (
	ReviewNone             = "none"
	ReviewPending          = "pending"
	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
)

func ReviewStateOf(reviews []Review, requested bool) string {
	latest := map[string]string{}
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			latest[strings.ToLower(r.User.Login)] = r.State
		}
	}
	approved := false
	for _, st := range latest {
		switch st {
		case "CHANGES_REQUESTED":
			return ReviewChangesRequested
		case "APPROVED":
			approved = true
		}
	}
	switch {
	case approved:
		return ReviewApproved
	case requested:
		return ReviewPending
	}
	return ReviewNone
}

const (
	CISourceChecks  = "checks"
	CISourceActions = "actions"
)

const (
	CIStateNone    = "none"
	CIStateRunning = "running"
	CIStatePassed  = "passed"
	CIStateFailed  = "failed"
)

// Total can exceed Passed+Failed+Running: unknown conclusions are counted but not sorted.
type CIStatus struct {
	Source  string `json:"source"`
	Total   int    `json:"total"`
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Running int    `json:"running"`
	State   string `json:"state"`
}

func (c *CIStatus) add(status, conclusion string) {
	c.Total++
	switch status {
	case "queued", "in_progress", "pending", "waiting", "requested":
		c.Running++
		return
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		c.Passed++
	case "failure", "timed_out", "cancelled", "action_required", "stale", "startup_failure":
		c.Failed++
	}
}

func (c *CIStatus) settle() {
	switch {
	case c.Total == 0:
		c.State = CIStateNone
	case c.Running > 0:
		c.State = CIStateRunning
	case c.Failed > 0:
		c.State = CIStateFailed
	default:
		c.State = CIStatePassed
	}
}

func CheckRunsCI(ctx context.Context, token, owner, repo, sha string) (CIStatus, error) {
	ci := CIStatus{Source: CISourceChecks}
	//i18n:ignore API path and query
	base := reposURL(owner, repo) + "/commits/" + url.PathEscape(sha) + "/check-runs?filter=latest&per_page=100&page="
	seen := 0
	for page := 1; page <= maxPages; page++ {
		var batch struct {
			TotalCount int `json:"total_count"`
			CheckRuns  []struct {
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"check_runs"`
		}
		if err := callLimit(ctx, token, http.MethodGet, base+strconv.Itoa(page), nil, &batch, readLimit); err != nil {
			return CIStatus{}, err
		}
		for _, r := range batch.CheckRuns {
			ci.add(r.Status, r.Conclusion)
		}
		seen += len(batch.CheckRuns)
		if len(batch.CheckRuns) == 0 || seen >= batch.TotalCount {
			break
		}
	}
	ci.settle()
	return ci, nil
}

// A run with no jobs yet counts as one running job, so a freshly queued workflow does not read as
// no CI.
func ActionsCI(ctx context.Context, token, owner, repo, sha string) (CIStatus, error) {
	ci := CIStatus{Source: CISourceActions}
	var runs struct {
		WorkflowRuns []struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"workflow_runs"`
	}
	//i18n:ignore API path and query
	endpoint := reposURL(owner, repo) + "/actions/runs?per_page=" + strconv.Itoa(maxWorkflowRuns) + "&head_sha=" + url.QueryEscape(sha)
	if err := callLimit(ctx, token, http.MethodGet, endpoint, nil, &runs, readLimit); err != nil {
		return CIStatus{}, err
	}
	for _, run := range runs.WorkflowRuns {
		var jobs struct {
			Jobs []struct {
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"jobs"`
		}
		//i18n:ignore API path and query
		jobsURL := reposURL(owner, repo) + "/actions/runs/" + strconv.FormatInt(run.ID, 10) + "/jobs?filter=latest&per_page=100"
		if err := callLimit(ctx, token, http.MethodGet, jobsURL, nil, &jobs, readLimit); err != nil {
			return CIStatus{}, err
		}
		if len(jobs.Jobs) == 0 {
			if run.Status != "completed" {
				ci.add("queued", "")
			}
			continue
		}
		for _, j := range jobs.Jobs {
			ci.add(j.Status, j.Conclusion)
		}
	}
	ci.settle()
	return ci, nil
}

type PRStatus struct {
	Number       int       `json:"number"`
	Title        string    `json:"title"`
	State        string    `json:"state"`
	Head         string    `json:"head"`
	Base         string    `json:"base"`
	HeadSHA      string    `json:"-"`
	CI           CIStatus  `json:"ci"`
	Review       string    `json:"review"`
	Mergeable    string    `json:"mergeable"`
	ChangedFiles int       `json:"changed_files"`
	Additions    int       `json:"additions"`
	Deletions    int       `json:"deletions"`
	Commits      int       `json:"commits"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func FetchPRStatus(ctx context.Context, token, owner, repo string, number int, checks bool) (*PRStatus, error) {
	pr, err := GetPR(ctx, token, owner, repo, number)
	if err != nil {
		return nil, err
	}
	st := &PRStatus{
		Number:       pr.Number,
		Title:        pr.Title,
		State:        StateOf(pr),
		Head:         pr.Head.Ref,
		Base:         pr.Base.Ref,
		HeadSHA:      pr.Head.SHA,
		Mergeable:    MergeableOf(pr),
		ChangedFiles: pr.ChangedFiles,
		Additions:    pr.Additions,
		Deletions:    pr.Deletions,
		Commits:      pr.Commits,
		UpdatedAt:    pr.UpdatedAt,
		Review:       ReviewNone,
		CI:           CIStatus{State: CIStateNone},
	}
	requested := len(pr.RequestedReviewers)+len(pr.RequestedTeams) > 0
	if reviews, err := ListReviews(ctx, token, owner, repo, number); err != nil {
		logx.Warn(err, "github.log.reviews_read_failed", "repo", owner+"/"+repo, "number", number)
		if requested {
			st.Review = ReviewPending
		}
	} else {
		st.Review = ReviewStateOf(reviews, requested)
	}
	if pr.Head.SHA != "" {
		st.CI = readCI(ctx, token, owner, repo, pr.Head.SHA, checks)
	}
	return st, nil
}

func readCI(ctx context.Context, token, owner, repo, sha string, checks bool) CIStatus {
	if checks {
		ci, err := CheckRunsCI(ctx, token, owner, repo, sha)
		if err == nil {
			return ci
		}
		// 403, or 404 on some GHES versions, means the token may not read check runs; other errors
		// would not be fixed by the fallback.
		if k := apperr.KindOf(err); k != apperr.KindForbidden && k != apperr.KindNotFound {
			logx.Warn(err, "github.log.check_runs_read_failed", "repo", owner+"/"+repo)
			return CIStatus{State: CIStateNone}
		}
	}
	ci, err := ActionsCI(ctx, token, owner, repo, sha)
	if err != nil {
		logx.Warn(err, "github.log.actions_read_failed", "repo", owner+"/"+repo)
		return CIStatus{State: CIStateNone}
	}
	return ci
}
