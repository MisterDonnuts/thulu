package api

import (
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/automation"
	ghrest "github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// The fixed template a deploy watch becomes. Nothing in it comes from a prompt: the source and the
// target passed the closed menu (project.ValidateDeployWatch), the repo comes from the project and
// the board, type and stage from the task that registered the watch.

// A deploy fires once per deployment or run; the debounce folds its state changes together, as the
// watch deploy templates of the automations page do.
const (
	deployWatchDebounce   = 2 * time.Minute
	deployWatchTimeout    = 5 * time.Minute
	deployWatchAgentRuns  = 20
	deployWatchPollTimout = 2 * time.Minute
)

// Template expressions, not messages.
const (
	//i18n:ignore automation template expression
	exprSHA = "{{ trigger.payload.sha }}"
	//i18n:ignore automation template expression
	exprConclusion = "{{ trigger.payload.conclusion }}"
	//i18n:ignore automation template expression
	exprDeployState = "{{ trigger.payload.deploy_state }}"
	//i18n:ignore automation template expression
	exprObservedState = "{{ steps.observe.state }}"
	//i18n:ignore automation template expression
	exprObservedVersion = "{{ steps.observe.version }}"
	//i18n:ignore automation template expression
	exprObservedFingerprint = "{{ steps.observe.fingerprint }}"
)

const observeStep = "observe"

func (s *Server) deployWatchSpec(w *project.DeployWatch, proj *project.Project, stage task.Stage) (string, error) {
	name := templateSafe(proj.Name)
	spec := automation.Spec{
		Name:        i18n.Text("api.deploy_watches.spec.name", "project", name, "source", w.Source, "target", w.Target),
		Description: i18n.Text("api.deploy_watches.spec.description", "project", name),
		Policy: automation.Policy{
			Concurrency:        automation.ConcurrencyForbid,
			MaxAgentRunsPerDay: deployWatchAgentRuns,
		},
	}
	release := &automation.TaskStep{
		Board:       w.BoardID,
		Type:        w.TypeID,
		Stage:       string(stage),
		ProjectIDs:  []string{proj.ID},
		OnDuplicate: automation.DuplicateSkip,
	}
	fingerprint := "deploy-watch-" + w.ID + "-"
	switch w.Source {
	case project.WatchGitHubWorkflow, project.WatchGitHubDeployment:
		owner, repo, ok := ghrest.ParseRemote(proj.RepoURL)
		if !ok {
			return "", apperr.Invalid("api.deploy_watches.errors.not_on_github", "project", proj.Name)
		}
		full := strings.ToLower(owner + "/" + repo)
		spec.Policy.Debounce = automation.Duration(deployWatchDebounce)
		spec.Policy.Timeout = automation.Duration(deployWatchTimeout)
		if w.Source == project.WatchGitHubWorkflow {
			spec.Trigger = automation.Trigger{Event: automation.EventWorkflowRun, Filter: workflowFilter(full, w.Target)}
			release.Title = i18n.Text("api.deploy_watches.spec.task_title_github", "project", name, "sha", exprSHA, "state", exprConclusion)
			release.Body = i18n.Text("api.deploy_watches.spec.task_body_workflow",
				//i18n:ignore automation template expressions
				"workflow", "{{ trigger.payload.workflow_name }}", "state", exprConclusion,
				//i18n:ignore automation template expressions
				"branch", "{{ trigger.payload.head_branch }}", "url", "{{ trigger.payload.run_url }}", "sha", exprSHA)
			release.Fingerprint = fingerprint + exprSHA + "-" + exprConclusion
		} else {
			spec.Trigger = automation.Trigger{Event: automation.EventDeployment, Filter: deploymentFilter(full, w.Target)}
			release.Title = i18n.Text("api.deploy_watches.spec.task_title_github", "project", name, "sha", exprSHA, "state", exprDeployState)
			release.Body = i18n.Text("api.deploy_watches.spec.task_body_deployment",
				//i18n:ignore automation template expressions
				"environment", "{{ trigger.payload.environment }}", "state", exprDeployState, "sha", exprSHA,
				//i18n:ignore automation template expressions
				"url", "{{ trigger.payload.target_url }}", "batches", "{{ trigger.payload.evidence_batch_ids }}")
			release.Fingerprint = fingerprint + exprSHA + "-" + exprDeployState
		}
		spec.Steps = []automation.Step{{ID: "release", Task: release}}
	case project.WatchCloud, project.WatchHTTP:
		spec.Trigger = automation.Trigger{Schedule: everySchedule(w.Interval())}
		spec.Policy.Timeout = automation.Duration(deployWatchPollTimout)
		release.Title = i18n.Text("api.deploy_watches.spec.task_title_polled", "project", name,
			"state", exprObservedState, "version", exprObservedVersion)
		release.Body = i18n.Text("api.deploy_watches.spec.task_body_polled",
			"target", templateSafe(w.Target), "state", exprObservedState, "version", exprObservedVersion,
			//i18n:ignore automation template expressions
			"previous_state", "{{ steps.observe.previous_state }}", "previous_version", "{{ steps.observe.previous_version }}",
			//i18n:ignore automation template expressions
			"detail", "{{ steps.observe.detail }}")
		release.Fingerprint = fingerprint + exprObservedFingerprint
		spec.Steps = []automation.Step{
			{ID: observeStep, DeployWatch: &automation.DeployWatchStep{Watch: w.ID}},
			//i18n:ignore automation condition expression
			{ID: "release", When: "steps.observe.changed", Task: release},
		}
	default:
		return "", apperr.Invalid("project.errors.deploy_watch_source_invalid", "source", w.Source, "known", "")
	}
	out, err := yaml.Marshal(spec)
	if err != nil {
		return "", apperr.Wrap(err, "api.deploy_watches.errors.spec_invalid")
	}
	return string(out), nil
}

// The workflow is matched by path when the target is a file under .github/workflows, else by name.
// Only completed runs on the default branch (when the event names it) that ended in success or
// failure open a task.
func workflowFilter(repo, target string) string {
	field := "trigger.payload.workflow_name"
	if strings.HasPrefix(target, ".github/workflows/") {
		field = "trigger.payload.workflow_path"
	}
	//i18n:ignore automation filter expression
	return `lower(trigger.payload.repo) == ` + strconv.Quote(repo) +
		` && trigger.payload.action == "completed"` +
		` && (trigger.payload.conclusion == "success" || trigger.payload.conclusion == "failure")` +
		` && (trigger.payload.default_branch == "" || trigger.payload.head_branch == trigger.payload.default_branch)` +
		` && ` + field + ` == ` + strconv.Quote(target)
}

func deploymentFilter(repo, environment string) string {
	//i18n:ignore automation filter expression
	return `lower(trigger.payload.repo) == ` + strconv.Quote(repo) +
		` && trigger.payload.environment == ` + strconv.Quote(environment) +
		` && (trigger.payload.deploy_state == "success" || trigger.payload.deploy_state == "failure"` +
		` || trigger.payload.deploy_state == "error")`
}

// everySchedule writes the interval in its shortest form ("@every 15m", "@every 2h").
func everySchedule(d time.Duration) string {
	if d%time.Hour == 0 {
		return "@every " + strconv.Itoa(int(d/time.Hour)) + "h"
	}
	return "@every " + strconv.Itoa(int(d/time.Minute)) + "m"
}

// templateSafe keeps a name from opening a template expression in a title or body.
func templateSafe(s string) string {
	return strings.NewReplacer("{", "(", "}", ")").Replace(s)
}
