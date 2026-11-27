package api

import (
	"encoding/json"
	"strconv"
	"strings"
)

type githubWorkflowRunEvent struct {
	WorkflowRun struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Path       string `json:"path"`
		HTMLURL    string `json:"html_url"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HeadBranch string `json:"head_branch"`
		HeadSHA    string `json:"head_sha"`
		Event      string `json:"event"`
	} `json:"workflow_run"`
}

// addGithubEventFields adds to an automation payload what deployment_status and workflow_run carry
// beyond the common fields, and returns the dedupe key for them ("" for any other event): one
// deployment or one workflow run, so a debounce folds its state changes together.
func addGithubEventFields(event string, raw []byte, batchIDs []string, payload map[string]any) string {
	switch event {
	case "deployment_status":
		var ev githubDeployEvent
		if json.Unmarshal(raw, &ev) != nil {
			return ""
		}
		target := ev.DeploymentStatus.EnvironmentURL
		if target == "" {
			target = ev.DeploymentStatus.TargetURL
		}
		payload["environment"] = ev.environment()
		payload["deploy_state"] = ev.DeploymentStatus.State
		payload["sha"] = strings.ToLower(ev.Deployment.SHA)
		payload["ref"] = ev.Deployment.Ref
		payload["deployment_id"] = ev.Deployment.ID
		payload["target_url"] = target
		payload["evidence_batch_ids"] = toAnyList(batchIDs)
		return "deployment:" + strconv.FormatInt(ev.Deployment.ID, 10)
	case "workflow_run":
		var ev githubWorkflowRunEvent
		if json.Unmarshal(raw, &ev) != nil {
			return ""
		}
		run := ev.WorkflowRun
		payload["workflow_name"] = run.Name
		payload["workflow_path"] = run.Path
		payload["run_id"] = run.ID
		payload["run_url"] = run.HTMLURL
		payload["run_status"] = run.Status
		payload["conclusion"] = run.Conclusion
		payload["head_branch"] = run.HeadBranch
		payload["sha"] = strings.ToLower(run.HeadSHA)
		payload["head_sha"] = strings.ToLower(run.HeadSHA)
		payload["run_event"] = run.Event
		return "workflow_run:" + strconv.FormatInt(run.ID, 10)
	}
	return ""
}
