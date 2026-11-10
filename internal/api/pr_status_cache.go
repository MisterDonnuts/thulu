package api

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const prStatusTTL = 30 * time.Second

const prStatusCacheMax = 512

type prStatusCache struct {
	mu      sync.Mutex
	entries map[string]prCacheEntry
	// gen counts invalidations: a fetch that started before one may hold what the webhook just made
	// stale, so put drops it.
	gen uint64
	now func() time.Time
}

type prCacheEntry struct {
	repo   string
	number int
	sha    string
	ref    string
	status *github.PRStatus
	at     time.Time
}

func (c *prStatusCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *prStatusCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

func (c *prStatusCache) get(url string) (*github.PRStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[url]
	if !ok || c.clock().Sub(e.at) >= prStatusTTL {
		return nil, false
	}
	return e.status, true
}

func (c *prStatusCache) put(gen uint64, url, owner, repo string, number int, st *github.PRStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	if c.entries == nil {
		c.entries = map[string]prCacheEntry{}
	}
	now := c.clock()
	if _, ok := c.entries[url]; !ok && len(c.entries) >= prStatusCacheMax {
		oldest := ""
		for k, e := range c.entries {
			if now.Sub(e.at) >= prStatusTTL {
				delete(c.entries, k)
				continue
			}
			if oldest == "" || e.at.Before(c.entries[oldest].at) {
				oldest = k
			}
		}
		if len(c.entries) >= prStatusCacheMax {
			delete(c.entries, oldest)
		}
	}
	c.entries[url] = prCacheEntry{
		repo:   strings.ToLower(owner + "/" + repo),
		number: number,
		sha:    st.HeadSHA,
		ref:    st.Head,
		status: st,
		at:     now,
	}
}

func (c *prStatusCache) invalidate(repo string, numbers []int, shas, refs []string) {
	repo = strings.ToLower(repo)
	whole := len(numbers) == 0 && len(shas) == 0 && len(refs) == 0
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for k, e := range c.entries {
		if e.repo != repo {
			continue
		}
		if whole || slices.Contains(numbers, e.number) ||
			(e.sha != "" && slices.Contains(shas, e.sha)) ||
			(e.ref != "" && slices.Contains(refs, e.ref)) {
			delete(c.entries, k)
		}
	}
}

type prHookRefs struct {
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number int `json:"number"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	CheckSuite  prHookRun `json:"check_suite"`
	CheckRun    prHookRun `json:"check_run"`
	WorkflowRun prHookRun `json:"workflow_run"`
	WorkflowJob prHookRun `json:"workflow_job"`
}

type prHookRun struct {
	HeadSHA      string `json:"head_sha"`
	HeadBranch   string `json:"head_branch"`
	PullRequests []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
	CheckSuite struct {
		HeadBranch string `json:"head_branch"`
	} `json:"check_suite"`
}

var prStatusEvents = map[string]bool{
	"pull_request":        true,
	"pull_request_review": true,
	"check_suite":         true,
	"check_run":           true,
	"workflow_run":        true,
	"workflow_job":        true,
}

func (s *Server) invalidatePRStatus(event string, payload []byte) {
	if !prStatusEvents[event] {
		return
	}
	var ev prHookRefs
	if err := json.Unmarshal(payload, &ev); err != nil {
		logx.Warn(err, "api.github.log.webhook_decode_failed", "event", event)
		return
	}
	if ev.Repository.FullName == "" {
		return
	}
	var numbers []int
	var shas, refs []string
	addRef := func(sha, ref string) {
		if sha != "" {
			shas = append(shas, sha)
		}
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	if pr := ev.PullRequest; pr.Number > 0 {
		numbers = append(numbers, pr.Number)
		addRef(pr.Head.SHA, pr.Head.Ref)
	}
	for _, run := range []prHookRun{ev.CheckSuite, ev.CheckRun, ev.WorkflowRun, ev.WorkflowJob} {
		addRef(run.HeadSHA, run.HeadBranch)
		addRef("", run.CheckSuite.HeadBranch)
		for _, pr := range run.PullRequests {
			numbers = append(numbers, pr.Number)
		}
	}
	s.gh.prs.invalidate(ev.Repository.FullName, numbers, shas, refs)
}
