package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/netguard"
)

func (e *Engine) stepMCP(ctx context.Context, st *runState, step *Step) (map[string]any, error) {
	if e.deps.MCP == nil {
		return nil, apperr.Unavailable("automation.errors.mcp_unavailable")
	}
	args, err := renderArgs(step.MCP.Args, st.ctx)
	if err != nil {
		return nil, err
	}
	result, err := e.deps.MCP.CallTool(ctx, step.MCP.Server, step.MCP.Tool, args, st.apiKey)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"json": result}
	if text, ok := mcpText(result); ok {
		out["text"] = text
		var parsed any
		if json.Unmarshal([]byte(text), &parsed) == nil {
			out["json"] = parsed
		}
	}
	return out, nil
}

func mcpText(result map[string]any) (string, bool) {
	content, ok := result["content"].([]any)
	if !ok {
		return "", false
	}
	var parts []string
	for _, c := range content {
		block, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := block["text"].(string); ok {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

func renderArgs(args, ctx map[string]any) (map[string]any, error) {
	if args == nil {
		return map[string]any{}, nil
	}
	var walk func(v any) (any, error)
	walk = func(v any) (any, error) {
		switch t := v.(type) {
		case string:
			return renderTemplate(t, ctx)
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, item := range t {
				r, err := walk(item)
				if err != nil {
					return nil, err
				}
				out[k] = r
			}
			return out, nil
		case []any:
			out := make([]any, len(t))
			for i, item := range t {
				r, err := walk(item)
				if err != nil {
					return nil, err
				}
				out[i] = r
			}
			return out, nil
		default:
			return v, nil
		}
	}
	rendered, err := walk(args)
	if err != nil {
		return nil, err
	}
	out, ok := rendered.(map[string]any)
	if !ok {
		return nil, apperr.Invalid("automation.errors.args_not_map", "value", rendered)
	}
	return out, nil
}

// Recipe URLs are user-typed and fetched from inside the deployment: netguard refuses loopback,
// private,
// metadata and the host's own addresses at connect time and on every redirect.
// OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS opts ranges back in.
var automationHTTPClient = netguard.NewEgress().Client(30 * time.Second)

const maxHTTPBody = 1 << 20

func (e *Engine) stepHTTP(ctx context.Context, st *runState, step *Step) (map[string]any, error) {
	timeout := step.HTTP.Timeout.Duration()
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := e.buildHTTPRequest(reqCtx, st, step)
	if err != nil {
		return nil, err
	}
	resp, err := automationHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	if err != nil {
		return nil, err
	}
	truncated := len(raw) > maxHTTPBody
	if truncated {
		raw = raw[:maxHTTPBody]
	}
	out := map[string]any{
		"status_code": float64(resp.StatusCode),
		"text":        string(raw),
		"truncated":   truncated,
	}
	var parsed any
	if json.Unmarshal(raw, &parsed) == nil {
		out["json"] = parsed
	}
	if resp.StatusCode >= 400 {
		// Return the body with the error: continue_on_error recipes read it.
		return out, apperr.New("automation.errors.http_status", "status", resp.StatusCode, "url", req.URL)
	}
	return out, nil
}

func (e *Engine) buildHTTPRequest(ctx context.Context, st *runState, step *Step) (*http.Request, error) {
	url, err := renderTemplate(step.HTTP.URL, st.ctx)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, apperr.Invalid("automation.errors.http_url_scheme", "url", url)
	}
	body, err := renderTemplate(step.HTTP.Body, st.ctx)
	if err != nil {
		return nil, err
	}
	method := strings.ToUpper(step.HTTP.Method)
	if method == "" {
		method = http.MethodGet
	}
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range step.HTTP.Headers {
		rendered, rErr := renderTemplate(v, st.ctx)
		if rErr != nil {
			return nil, rErr
		}
		req.Header.Set(k, rendered)
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if step.HTTP.Credential == "" {
		return req, nil
	}
	// Resolve the secret per run; never write it into the recipe, the history row or the step
	// output.
	if e.deps.Creds == nil {
		return nil, apperr.Unavailable("automation.errors.credential_store_missing", "name", step.HTTP.Credential)
	}
	secret, err := e.deps.Creds.Secret(ctx, st.automation.WorkspaceID, step.HTTP.Credential)
	if err != nil {
		return nil, apperr.Wrap(err, "automation.errors.credential_lookup", "name", step.HTTP.Credential)
	}
	//i18n:ignore HTTP authorization scheme, not a message
	req.Header.Set("Authorization", "Bearer "+secret)
	return req, nil
}

// A non-zero exit is deliberately not an error: recipes react to exit_code via `when` or a
// condition.
func (e *Engine) stepScript(ctx context.Context, st *runState, step *Step) (map[string]any, error) {
	if e.deps.Scripts == nil {
		return nil, apperr.Unavailable("automation.errors.script_unavailable")
	}
	script, err := renderTemplate(step.Script.Run, st.ctx)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(step.Script.Env))
	for k, v := range step.Script.Env {
		rendered, rErr := renderTemplate(v, st.ctx)
		if rErr != nil {
			return nil, rErr
		}
		env[k] = rendered
	}
	timeout := step.Script.Timeout.Duration()
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	st.usedSandbox = true
	res, err := e.deps.Scripts.RunScript(ctx, st.owner(), ScriptRequest{
		WorkspaceID: st.automation.WorkspaceID,
		Script:      script,
		Env:         env,
		Timeout:     timeout,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"stdout":    res.Stdout,
		"stderr":    res.Stderr,
		"exit_code": float64(res.ExitCode),
	}
	var parsed any
	if json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &parsed) == nil {
		out["json"] = parsed
	}
	return out, nil
}

func (e *Engine) stepQuery(ctx context.Context, st *runState, step *Step) (map[string]any, error) {
	switch step.Query.Source {
	case "tasks":
		if e.deps.Tasks == nil {
			return nil, apperr.Unavailable("automation.errors.task_query_unavailable")
		}
		limit := step.Query.Limit
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		tasks, err := e.deps.Tasks.QueryTasks(ctx, TaskQuery{
			WorkspaceID: st.automation.WorkspaceID,
			BoardID:     step.Query.Board,
			Stage:       step.Query.Stage,
			Tag:         step.Query.Tag,
			ProjectID:   step.Query.ProjectID,
			WithPR:      step.Query.WithPR,
			Open:        step.Query.Open,
			StaleFor:    step.Query.StaleFor.Duration(),
			Limit:       limit,
		})
		if err != nil {
			return nil, err
		}
		// The expression language only understands plain map/slice trees, hence the JSON
		// round-trip.
		items, err := toAnySlice(tasks)
		if err != nil {
			return nil, err
		}
		return map[string]any{"json": map[string]any{"tasks": items, "count": float64(len(items))}, "count": float64(len(items))}, nil

	case "metric":
		if e.deps.Metrics == nil {
			return nil, apperr.Unavailable("automation.errors.metric_query_unavailable")
		}
		window := step.Query.Window.Duration()
		if window <= 0 {
			window = 15 * time.Minute
		}
		agg := step.Query.Agg
		if agg == "" {
			agg = "avg"
		}
		value, ok, err := e.deps.Metrics.Aggregate(ctx, st.automation.WorkspaceID, step.Query.Environment, step.Query.Metric, window, agg)
		if err != nil {
			return nil, err
		}
		return map[string]any{"json": map[string]any{
			"value": value, "has_data": ok, "metric": step.Query.Metric,
		}, "value": value, "has_data": ok}, nil
	}
	return nil, apperr.Invalid("automation.errors.unknown_query_source", "source", step.Query.Source)
}

func toAnySlice(v any) ([]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out []any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *Engine) stepTask(ctx context.Context, st *runState, step *Step) (map[string]any, error) {
	if e.deps.Tasks == nil {
		return nil, apperr.Unavailable("automation.errors.task_unavailable")
	}
	title, err := renderTemplate(step.Task.Title, st.ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		return nil, apperr.Invalid("automation.errors.task_title_empty")
	}
	body, err := renderTemplate(step.Task.Body, st.ctx)
	if err != nil {
		return nil, err
	}

	fingerprint := ""
	if step.Task.Fingerprint != "" {
		if fingerprint, err = renderTemplate(step.Task.Fingerprint, st.ctx); err != nil {
			return nil, err
		}
		fingerprint = strings.TrimSpace(fingerprint)
	}

	if fingerprint != "" {
		existing, handled, result, dupErr := e.handleDuplicate(ctx, st, step, fingerprint, title, body)
		if dupErr != nil {
			return nil, dupErr
		}
		if handled {
			return result, nil
		}
		_ = existing
	}

	req := NewTask{
		WorkspaceID:      st.automation.WorkspaceID,
		Title:            title,
		Body:             body,
		BoardID:          step.Task.Board,
		TypeID:           step.Task.Type,
		Stage:            step.Task.Stage,
		ProjectIDs:       step.Task.ProjectIDs,
		StageAssignments: step.Task.StageAssignments,
		Tags:             step.Task.Tags,
		Priority:         step.Task.Priority,
		AutomationID:     st.automation.ID,
		ChainDepth:       st.depth + 1,
	}
	// The budget counts agent wake-ups only: a task no agent will pick up is never refused.
	planned, err := e.deps.Tasks.PlannedAgent(ctx, req)
	if err == nil && planned != "" {
		err = e.checkBudget(ctx, st)
	}
	if err != nil {
		e.releaseClaim(ctx, st, fingerprint)
		return nil, err
	}

	taskID, agent, err := e.deps.Tasks.CreateTask(ctx, req)
	if err != nil {
		e.releaseClaim(ctx, st, fingerprint)
		return nil, err
	}
	// Loop guards look up later events about this task (stage moves, failures) here; without the
	// row a recipe fires on its own output.
	logx.Warn(e.store.RecordTaskOrigin(ctx, taskID, st.automation.ID, st.automation.WorkspaceID, req.ChainDepth),
		"automation.log.record_origin_failed", "task", taskID)
	if fingerprint != "" {
		if err := e.store.PointFingerprint(ctx, st.automation.ID, fingerprint, taskID); err != nil {
			return nil, err
		}
	}
	if agent != "" {
		st.run.AgentRuns++
	}
	st.run.ItemsEmitted++
	return map[string]any{
		"task_id": taskID, "title": title, "created": true, "fingerprint": fingerprint, "agent": agent,
	}, nil
}

func (e *Engine) releaseClaim(ctx context.Context, st *runState, fingerprint string) {
	if fingerprint == "" {
		return
	}
	logx.Warn(e.store.ReleaseFingerprint(ctx, st.automation.ID, fingerprint),
		"automation.log.release_fingerprint", "fingerprint", fingerprint)
}

func (e *Engine) handleDuplicate(
	ctx context.Context, st *runState, step *Step, fingerprint, title, body string,
) (existing string, handled bool, result map[string]any, err error) {
	if step.Task.OnDuplicate == DuplicateCreate {
		return "", false, nil, nil
	}
	// The claim is an insert, so two concurrent runs cannot both think they are first.
	existing, claimed, err := e.store.ClaimFingerprint(ctx, st.automation.ID, st.automation.WorkspaceID, fingerprint, "")
	if err != nil {
		return "", false, nil, err
	}
	if claimed {
		return "", false, nil, nil
	}
	if existing != "" {
		open, openErr := e.deps.Tasks.TaskOpen(ctx, existing)
		if openErr != nil {
			return "", false, nil, openErr
		}
		if !open {
			if err := e.store.ReleaseFingerprint(ctx, st.automation.ID, fingerprint); err != nil {
				return "", false, nil, err
			}
			if _, _, err := e.store.ClaimFingerprint(ctx, st.automation.ID, st.automation.WorkspaceID, fingerprint, ""); err != nil {
				return "", false, nil, err
			}
			return "", false, nil, nil
		}
	}
	if step.Task.OnDuplicate == DuplicateSkip || existing == "" {
		return existing, true, map[string]any{
			"task_id": existing, "created": false, "duplicate": true, "fingerprint": fingerprint,
		}, nil
	}
	comment := "**" + title + "**\n\n" + body
	if _, err := e.deps.Tasks.CommentTask(ctx, st.automation.WorkspaceID, existing, comment); err != nil {
		return existing, false, nil, err
	}
	st.run.ItemsEmitted++
	return existing, true, map[string]any{
		"task_id": existing, "title": title, "created": false, "duplicate": true, "commented": true, "fingerprint": fingerprint,
	}, nil
}

func (e *Engine) stepMention(ctx context.Context, st *runState, step *Step) (map[string]any, error) {
	if e.deps.Tasks == nil {
		return nil, apperr.Unavailable("automation.errors.mention_unavailable")
	}
	taskID, err := renderTemplate(step.Mention.TaskID, st.ctx)
	if err != nil {
		return nil, err
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, apperr.Invalid("automation.errors.mention_task_empty")
	}
	body, err := renderTemplate(step.Mention.Body, st.ctx)
	if err != nil {
		return nil, err
	}
	agent := step.Mention.Agent
	skipped := ""
	if agent == "" {
		var answers bool
		if agent, answers, err = e.deps.Tasks.StageAgent(ctx, st.automation.WorkspaceID, taskID); err != nil {
			return nil, err
		}
		if !answers {
			skipped, agent = agent, ""
		}
	}
	var title string
	if agent == "" {
		if title, err = e.deps.Tasks.CommentTask(ctx, st.automation.WorkspaceID, taskID, body); err != nil {
			return nil, err
		}
		st.run.ItemsEmitted++
		out := map[string]any{"task_id": taskID, "title": title, "commented": true}
		if skipped != "" {
			out["skipped_agent"] = skipped
		}
		return out, nil
	}
	err = e.checkBudget(ctx, st)
	if err != nil {
		return nil, err
	}
	if title, err = e.deps.Tasks.MentionAgent(ctx, st.automation.WorkspaceID, taskID, agent, body); err != nil {
		return nil, err
	}
	st.run.AgentRuns++
	st.run.ItemsEmitted++
	return map[string]any{"task_id": taskID, "title": title, "agent": agent, "mentioned": true}, nil
}

// Enforced before the spend: automations burn the client's model credits unattended.
func (e *Engine) checkBudget(ctx context.Context, st *runState) error {
	limit := st.automation.Parsed.Policy.MaxAgentRunsPerDay
	if limit <= 0 {
		return nil
	}
	spent, err := e.store.AgentRunsSince(ctx, st.automation.ID, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		return err
	}
	// Count this run's own wake-ups too: the run row is only written at the end, so a fanout would
	// overshoot the limit.
	if spent+st.run.AgentRuns >= limit {
		return apperr.Wrap(ErrBudgetExhausted, "automation.errors.budget_spent", "spent", spent+st.run.AgentRuns, "limit", limit)
	}
	return nil
}
