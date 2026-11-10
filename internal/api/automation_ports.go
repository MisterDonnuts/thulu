package api

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func (s *Server) AutomationDeps() automation.Deps {
	return automation.Deps{
		Tasks:   &automationTasks{s: s},
		MCP:     &automationMCP{s: s, caller: mcp.NewCaller()},
		Scripts: &automationScripts{s: s},
		Metrics: &automationMetrics{s: s},
		Creds:   &automationCreds{s: s},
		Keys:    &automationKeys{s: s},
	}
}

type automationTasks struct{ s *Server }

func (a *automationTasks) CreateTask(ctx context.Context, req automation.NewTask) (string, string, error) {
	id, agent, err := a.createTask(ctx, req)
	if id != "" {
		// Published after the stage move so the payload names the landing stage; a task whose move
		// was refused still exists and is still published.
		if t, getErr := a.s.taskStore.Get(ctx, id); getErr == nil {
			a.s.publishTaskAutomationEvent(context.WithoutCancel(ctx), automation.EventTaskCreated, t, map[string]any{
				"source":                       "automation",
				automation.PayloadAutomationID: req.AutomationID,
				automation.PayloadChainDepth:   req.ChainDepth,
			})
		}
	}
	return id, agent, err
}

func (a *automationTasks) resolveNewTask(ctx context.Context, req automation.NewTask) (string, map[task.Stage]string, error) {
	if req.WorkspaceID == "" {
		return "", nil, apperr.Invalid("api.automations.errors.task_without_workspace")
	}
	boardID := req.BoardID
	if boardID == "" {
		boardID = a.s.boards.DefaultBoardIDFor(req.WorkspaceID)
	}
	if b, ok := a.s.boardInfo(boardID); !ok || !workspaceVisible(b.WorkspaceID, req.WorkspaceID, false) {
		return "", nil, apperr.NotFound("api.automations.errors.unknown_board", "board", boardID)
	}
	assigns := map[task.Stage]string{}
	for _, pid := range req.ProjectIDs {
		proj, err := a.s.projectStore.Get(ctx, pid)
		if err != nil || !workspaceVisible(proj.WorkspaceID, req.WorkspaceID, false) {
			return "", nil, apperr.NotFound("api.automations.errors.project_not_found", "id", pid)
		}
		for k, v := range proj.StageTemplate {
			if _, ok := assigns[task.Stage(k)]; !ok {
				assigns[task.Stage(k)] = v
			}
		}
	}
	for stage, agent := range req.StageAssignments {
		assigns[task.Stage(stage)] = agent
	}
	return boardID, assigns, nil
}

// Must agree with the agent createTask returns: the engine checks the recipe's budget with it
// before creating.
func (a *automationTasks) PlannedAgent(ctx context.Context, req automation.NewTask) (string, error) {
	boardID, assigns, err := a.resolveNewTask(ctx, req)
	if err != nil {
		return "", err
	}
	if req.Stage == "" {
		return "", nil
	}
	reg := a.s.boards.BoardFor(boardID, req.TypeID)
	stage := task.Stage(req.Stage)
	if stage == reg.Initial() {
		return "", nil
	}
	if info, _ := reg.Get(stage); info.Terminal {
		return "", nil
	}
	return assigns[stage], nil
}

func (a *automationTasks) createTask(ctx context.Context, req automation.NewTask) (string, string, error) {
	boardID, assigns, err := a.resolveNewTask(ctx, req)
	if err != nil {
		return "", "", err
	}
	t := &task.Task{
		Title:            req.Title,
		Body:             req.Body,
		ProjectIDs:       req.ProjectIDs,
		StageAssignments: assigns,
		Tags:             req.Tags,
		Priority:         req.Priority,
		WorkspaceID:      req.WorkspaceID,
		BoardID:          boardID,
		TypeID:           req.TypeID,
	}
	if cerr := a.s.taskStore.Create(ctx, t); cerr != nil {
		return "", "", cerr
	}
	a.s.recordEvent(ctx, t.ID, task.EventTaskCreated, "", "automation", t.Title, nil)
	a.s.emitSystem(ctx, t.WorkspaceID, audit.EventKind("task.created"), "task.created", t.ID, "task",
		i18n.Text("api.automations.audit.task_created", "title", t.Title),
		map[string]string{"task_id": t.ID, "title": t.Title, "source": "automation"})

	if req.Stage == "" || task.Stage(req.Stage) == t.Stage {
		return t.ID, "", nil
	}
	from := t.Stage
	stage := task.Stage(req.Stage)
	gate, err := a.s.taskStore.StageExitGate(ctx, t, from, stage, task.GateAutomation)
	if err != nil {
		return t.ID, "", err
	}
	if !gate.Passed {
		return t.ID, "", apperr.Conflict("api.automations.errors.stage_gated", "stage", req.Stage, "approvals", gate.Labels())
	}
	updated, err := a.s.taskStore.Update(ctx, t.ID, task.Patch{Stage: &stage})
	if err != nil {
		return t.ID, "", apperr.Wrap(err, "api.automations.errors.enter_stage_failed", "stage", req.Stage)
	}
	a.s.recordEvent(ctx, t.ID, task.EventStageEntered, "", "automation",
		fmt.Sprintf("%s → %s", from, updated.Stage),
		map[string]string{"from": string(from), "to": string(updated.Stage)})
	a.s.taskDispatcher.HandleStageEntered(context.WithoutCancel(ctx), updated, from)
	if info, _ := a.s.stagesFor(updated).Get(updated.Stage); info.Terminal {
		return t.ID, "", nil
	}
	return t.ID, updated.AssignedAgent(updated.Stage), nil
}

func (a *automationTasks) StageAgent(ctx context.Context, workspaceID, taskID string) (string, bool, error) {
	t, err := a.taskInWorkspace(ctx, workspaceID, taskID)
	if err != nil {
		return "", false, err
	}
	if info, _ := a.s.stagesFor(t).Get(t.Stage); info.Terminal {
		return "", false, nil
	}
	name := t.AssignedAgent(t.Stage)
	if name == "" {
		return "", false, nil
	}
	at := a.s.findAgentByHandle(t.WorkspaceID, name)
	if at == nil {
		return "", false, nil
	}
	return name, at.AnswersMentions(), nil
}

func (a *automationTasks) CommentTask(ctx context.Context, workspaceID, taskID, body string) (string, error) {
	t, err := a.taskInWorkspace(ctx, workspaceID, taskID)
	if err != nil {
		return "", err
	}
	if _, err := a.s.taskStore.AddComment(ctx, taskID, task.SourceAutomation, "automation", body); err != nil {
		return t.Title, err
	}
	return t.Title, nil
}

// Another tenant's task gets the same not-found as a missing one, so a recipe cannot probe for ids.
func (a *automationTasks) taskInWorkspace(ctx context.Context, workspaceID, taskID string) (*task.Task, error) {
	t, err := a.s.taskStore.Get(ctx, taskID)
	if err != nil || t == nil || !workspaceVisible(t.WorkspaceID, workspaceID, false) {
		return nil, apperr.NotFound("api.automations.errors.task_not_found", "id", taskID)
	}
	return t, nil
}

// Must use the same comment-and-dispatch path as a person's @mention; the prechecks run here so a
// step never reports waking an agent the dispatcher then refuses.
func (a *automationTasks) MentionAgent(ctx context.Context, workspaceID, taskID, agent, body string) (string, error) {
	t, err := a.taskInWorkspace(ctx, workspaceID, taskID)
	if err != nil {
		return "", err
	}
	handle := strings.TrimPrefix(agent, "@")
	agentType := a.s.findAgentByHandle(t.WorkspaceID, handle)
	if agentType == nil {
		return t.Title, apperr.NotFound("api.automations.errors.agent_not_found", "agent", handle)
	}
	if !agentType.AnswersMentions() {
		return t.Title, apperr.Conflict("api.automations.errors.agent_no_mention_stage", "agent", handle)
	}
	if !strings.Contains(body, "@"+handle) {
		body = "@" + handle + " " + body
	}
	c, err := a.s.taskStore.AddComment(ctx, taskID, task.SourceAutomation, "automation", body)
	if err != nil {
		return t.Title, err
	}
	if !a.s.mentionPrechecked(ctx, taskID, c.ThreadID(), agentType) {
		return t.Title, apperr.Conflict("api.automations.errors.agent_cannot_answer", "agent", handle)
	}
	go a.s.taskDispatcher.HandleMention(context.WithoutCancel(ctx), t, agentType, c)
	return t.Title, nil
}

func (s *Server) findAgentByHandle(ws, handle string) *types.AgentType {
	discovered, err := s.agents(ws).All()
	if err != nil {
		log.Printf("[automation] %s", i18n.Log("api.automations.log.discover_agents_failed", "err", err))
		return nil
	}
	for i := range discovered {
		if discovered[i].Name == handle || discovered[i].Type == handle {
			return &discovered[i]
		}
	}
	return nil
}

func (a *automationTasks) TaskOpen(ctx context.Context, taskID string) (bool, error) {
	t, err := a.s.taskStore.GetRow(ctx, taskID)
	if err != nil || t == nil {
		return false, nil // deleted counts as closed, not as an error
	}
	info, _ := a.s.stagesFor(t).Get(t.Stage)
	return !info.Terminal, nil
}

func (a *automationTasks) QueryTasks(ctx context.Context, q automation.TaskQuery) ([]automation.TaskSummary, error) {
	all, err := a.s.taskStore.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]automation.TaskSummary, 0, len(all))
	for i := range all {
		t := &all[i]
		if !taskMatchesQuery(t, q) {
			continue
		}
		info, _ := a.s.stagesFor(t).Get(t.Stage)
		if q.Open && info.Terminal {
			continue
		}
		out = append(out, automation.TaskSummary{
			ID: t.ID, Title: t.Title, Stage: string(t.Stage), Status: string(t.Status),
			BoardID: t.BoardID, Tags: t.Tags, PRURLs: t.PRURLs,
			UpdatedAt: t.UpdatedAt, Open: !info.Terminal,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func taskMatchesQuery(t *task.Task, q automation.TaskQuery) bool {
	switch {
	case !workspaceVisible(t.WorkspaceID, q.WorkspaceID, false):
		return false
	case q.BoardID != "" && t.BoardID != q.BoardID:
		return false
	case q.Stage != "" && string(t.Stage) != q.Stage:
		return false
	case q.Tag != "" && !containsString(t.Tags, q.Tag):
		return false
	case q.ProjectID != "" && !containsString(t.ProjectIDs, q.ProjectID):
		return false
	case q.WithPR && len(t.PRURLs) == 0:
		return false
	case q.StaleFor > 0 && time.Since(t.UpdatedAt) < q.StaleFor:
		return false
	}
	return true
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

type automationMCP struct {
	s      *Server
	caller toolCaller
}

type toolCaller interface {
	Call(ctx context.Context, sseURL, tool string, args map[string]any, apiKey string) (map[string]any, error)
}

func (a *automationMCP) CallTool(ctx context.Context, server, tool string, args map[string]any, apiKey string) (map[string]any, error) {
	up, ok := a.s.mcp.fwd.Lookup(server)
	if !ok {
		known := strings.Join(wiredServerNames(a.s.mcp.fwd.Snapshot()), ", ")
		return nil, apperr.NotFound("api.automations.errors.mcp_not_wired", "server", server, "known", known)
	}
	if a.s.mcpProxyRegistration(ctx, up).Auth == mcp.AuthCredential {
		return nil, apperr.Invalid("api.automations.errors.mcp_credential_unsupported", "server", server)
	}
	if up.Transport != mcp.TransportSSE {
		return nil, apperr.Invalid("api.automations.errors.mcp_http_unsupported", "server", server)
	}
	key := ""
	if up.ForwardsCredential {
		key = apiKey
	}
	return a.caller.Call(ctx, up.URL, tool, args, key)
}

func wiredServerNames(urls map[string]string) []string {
	out := make([]string, 0, len(urls))
	for k := range urls {
		out = append(out, mcp.EnvKeyToName(k))
	}
	sort.Strings(out)
	return out
}

type automationScripts struct{ s *Server }

// owner is `auto:<run id>`, not a task id; this relies on the sandbox table keying on an opaque
// string with no foreign key to tasks.
func (a *automationScripts) RunScript(ctx context.Context, owner string, req automation.ScriptRequest) (automation.ScriptResult, error) {
	if a.s.sandbox.mgr == nil {
		return automation.ScriptResult{}, apperr.Unavailable("api.automations.errors.no_sandbox_manager")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if timeout > automation.MaxScriptTimeout {
		timeout = automation.MaxScriptTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, acquire, err := a.s.sandbox.mgr.Exec(execCtx, owner, automationSandboxStage,
		sandbox.AcquireDockerOpts{WorkspaceID: req.WorkspaceID},
		sandbox.ExecOpts{Command: req.Script, Env: req.Env, TimeoutMs: int(timeout.Milliseconds())})
	if err != nil {
		return automation.ScriptResult{}, err
	}
	if res == nil {
		// nil result with no error means queued at the install's sandbox cap, not empty output.
		return automation.ScriptResult{}, apperr.Unavailable("api.automations.errors.sandbox_queue_full", "position", acquire.QueuePosition)
	}
	return automation.ScriptResult{Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode}, nil
}

const automationSandboxStage = "automation"

func (a *automationScripts) ReleaseSandbox(ctx context.Context, owner string) error {
	if a.s.sandbox.mgr == nil {
		return nil
	}
	return a.s.sandbox.mgr.Release(ctx, owner)
}

type automationMetrics struct{ s *Server }

func (a *automationMetrics) Aggregate(
	ctx context.Context, workspaceID, envID, metric string, window time.Duration, agg string,
) (float64, bool, error) {
	if a.s.envMetrics == nil {
		return 0, false, apperr.Unavailable("api.automations.errors.env_metrics_disabled")
	}
	if !a.s.environmentInWorkspace(ctx, workspaceID, envID) {
		return 0, false, apperr.NotFound("api.automations.errors.environment_not_found", "environment", envID)
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	to := time.Now().UTC()
	from := to.Add(-window)

	values, err := a.s.metricValues(ctx, envID, metric, from, to)
	if err != nil {
		return 0, false, err
	}
	if len(values) == 0 {
		return 0, false, nil
	}
	return reduce(values, agg), true, nil
}

func (s *Server) metricValues(ctx context.Context, envID, metric string, from, to time.Time) ([]float64, error) {
	if isFixedSignal(metric) {
		points, err := s.envMetrics.Series(ctx, envID, envmetrics.Signal(metric), "", from, to)
		if err != nil {
			return nil, err
		}
		out := make([]float64, 0, len(points))
		for _, p := range points {
			out = append(out, p.Value)
		}
		return out, nil
	}
	customs, err := s.envMetrics.ListCustomMetrics(ctx, envID)
	if err != nil {
		return nil, err
	}
	for i := range customs {
		if customs[i].Name != metric {
			continue
		}
		points, err := s.envMetrics.CustomSeries(ctx, customs[i].ID, from, to)
		if err != nil {
			return nil, err
		}
		out := make([]float64, 0, len(points))
		for _, p := range points {
			out = append(out, p.Value)
		}
		return out, nil
	}
	return nil, apperr.NotFound("api.automations.errors.metric_not_found", "metric", metric, "environment", envID)
}

func (s *Server) environmentInWorkspace(ctx context.Context, ws, envID string) bool {
	if s.projectStore == nil || envID == "" {
		return false
	}
	env, err := s.projectStore.FindEnvironment(ctx, envID)
	if err != nil {
		return false
	}
	proj, err := s.projectStore.Get(ctx, env.ProjectID)
	return err == nil && workspaceVisible(proj.WorkspaceID, ws, false)
}

func isFixedSignal(name string) bool {
	for _, sig := range []envmetrics.Signal{
		envmetrics.SignalLatencyP50, envmetrics.SignalLatencyP95, envmetrics.SignalErrorRate,
		envmetrics.SignalThroughput, envmetrics.SignalCPU, envmetrics.SignalMemory,
		envmetrics.SignalRestarts, envmetrics.SignalCostDaily, envmetrics.SignalSaturation,
	} {
		if string(sig) == name {
			return true
		}
	}
	return false
}

func reduce(values []float64, agg string) float64 {
	switch agg {
	case "max":
		out := values[0]
		for _, v := range values {
			if v > out {
				out = v
			}
		}
		return out
	case "min":
		out := values[0]
		for _, v := range values {
			if v < out {
				out = v
			}
		}
		return out
	case "sum":
		var sum float64
		for _, v := range values {
			sum += v
		}
		return sum
	case "last":
		return values[len(values)-1]
	default:
		var sum float64
		for _, v := range values {
			sum += v
		}
		return sum / float64(len(values))
	}
}

type automationCreds struct{ s *Server }

// The secret must reach only the outgoing Authorization header, never the recipe, the run context
// or the history.
func (a *automationCreds) Secret(ctx context.Context, workspaceID, name string) (string, error) {
	if a.s.credStore == nil {
		return "", apperr.Unavailable("api.automations.errors.no_credential_store")
	}
	creds, err := a.s.credStore.List(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	for _, c := range creds {
		if c.Name != name {
			continue
		}
		if c.Disabled {
			return "", apperr.Conflict("api.automations.errors.credential_disabled", "name", name)
		}
		payload, err := a.s.credStore.Payload(ctx, workspaceID, c.ID)
		if err != nil {
			return "", err
		}
		for _, key := range []string{"token", "api_key", "key", "secret", "password"} {
			if v := payload[key]; v != "" {
				return v, nil
			}
		}
		if len(payload) == 1 {
			for _, v := range payload {
				return v, nil
			}
		}
		return "", apperr.Unprocessable("api.automations.errors.credential_no_token", "name", name, "fields", strings.Join(sortedKeys(payload), ", "))
	}
	return "", apperr.NotFound("api.automations.errors.credential_not_found", "name", name)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type automationKeys struct{ s *Server }

func (a *automationKeys) MintAutomationKey(ctx context.Context, workspaceID, automationID, runID string, ttl time.Duration) (string, string, error) {
	if a.s.authStore == nil {
		return "", "", apperr.Unavailable("api.automations.errors.no_auth_store")
	}
	return a.s.authStore.MintRunKey(ctx, auth.RunScope{
		Agent: "automation:" + automationID + ":" + runID,
		Stage: automationSandboxStage,
		// No TaskID: a key that claimed one would carry write access to somebody's card.
	}, workspaceID, ttl)
}

func (a *automationKeys) RevokeAPIKey(ctx context.Context, keyID string) error {
	if a.s.authStore == nil {
		return nil
	}
	return a.s.authStore.RevokeAPIKey(ctx, keyID)
}
