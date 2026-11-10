package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// The auditor runs in its own container with its own run key, read-only repos and no tools.
// Read its verdict only from its own output dir or its key's report, never from the agent's output.

// Persisted in the auditor_run_started event payload; Reconcile finishes the stage from it after a
// restart.
type auditedRun struct {
	Agent     string `json:"agent"`
	Instance  string `json:"instance"`
	OutputDir string `json:"output_dir"`
	Exit      int    `json:"exit"`
	NoAdvance bool   `json:"no_advance,omitempty"`
}

const auditInstanceAnchor = "-audit-"

func (d *Dispatcher) wantsAudit(workspaceID string, stage Stage, run auditedRun, opts postRunOpts) (*types.AgentType, bool) {
	if opts.NoAudit || canceledExit(run.Exit) {
		return nil, false
	}
	agentType := d.launcher.FindAgentType(workspaceID, run.Agent)
	if agentType == nil || !agentType.Auditor.RunsForStage(string(stage)) {
		return nil, false
	}
	return agentType, true
}

// The agent as its auditor runs it on stage. On another engine nothing tied to the agent's engine
// carries over: its model, effort and image would not fit, and its bound profile holds the wrong key.
func (d *Dispatcher) auditorAgentType(ctx context.Context, workspaceID string, agentType *types.AgentType, stage Stage) *types.AgentType {
	eng := agentType.Auditor.EngineForStage(string(stage))
	if eng == "" || eng == d.runEngine(ctx, workspaceID, agentType, agentType.Name) {
		return agentType
	}
	view := *agentType
	view.Engine, view.Model, view.Effort = eng, "", ""
	view.Image, view.Dir = "", ""
	view.IgnoreLLMBinding = true
	return &view
}

func auditorRunScope(t *Task, stage Stage, view *types.AgentType, audited string) auth.RunScope {
	scope := auth.RunScope{TaskID: t.ID, Agent: auth.AuditorAgent, Stage: string(stage), LLMAgent: audited}
	if view.IgnoreLLMBinding {
		scope.LLMEngine = view.Engine
	}
	return scope
}

func buildAuditorEnv(t *Task, stage Stage, instanceID, baseURL string, agentType *types.AgentType, run auditedRun) map[string]string {
	env := buildStageEnv(t, stage, run.Agent, instanceID, baseURL, agentType, "")
	delete(env, "MCP_TOOLS")
	delete(env, "SKILLS")
	applyAuditorEnv(env, agentType.Auditor, string(stage))
	env["AUDIT_ONLY"] = "1"
	env["AUDITED_INSTANCE"] = run.Instance
	env["AUDITED_AGENT"] = run.Agent
	env["AUDITED_EXIT"] = fmt.Sprint(run.Exit)
	return env
}

func (d *Dispatcher) launchAuditor(ctx context.Context, t *Task, stage Stage, agentType *types.AgentType, run auditedRun) bool {
	slotWS := t.WorkspaceID
	if err := d.slots.acquire(ctx, slotWS); err != nil {
		return false
	}
	instanceID := types.SanitizeContainerName(fmt.Sprintf(
		"%s%s%s-%s-%d", run.Agent, auditInstanceAnchor, t.ID, stage, time.Now().UnixMilli()))
	agentType = d.auditorAgentType(ctx, t.WorkspaceID, agentType, stage)
	env := buildAuditorEnv(t, stage, instanceID, d.agentBaseURL, agentType, run)
	runKeyID := d.mintRunKey(ctx, env, t, auditorRunScope(t, stage, agentType, run.Agent))
	// Callers must have released the slot before calling failed. Returns true: an audit that was
	// asked for and could not run must not let the task through as if it had passed.
	failed := func(err error) bool {
		d.revokeRunKey(ctx, runKeyID)
		d.systemComment(ctx, t.ID, NoteOf("task.comments.auditor_launch_failed",
			"stage", d.stageNote(t, stage), "err", err))
		d.setStatus(ctx, t.ID, StatusBlocked)
		return true
	}
	if err := d.applyLLMAuth(ctx, env, t.WorkspaceID, agentType, run.Agent, t); err != nil {
		d.slots.release(slotWS)
		return failed(err)
	}
	wtMounts, _ := d.buildStageMounts(ctx, t)
	started, err := d.launchWithRoom(ctx, roomRun{
		ws: slotWS, gate: d.slots,
		launch: func(lctx context.Context) (launched, error) {
			cid, iid, out, lerr := d.launcher.LaunchForStage(lctx, t, stage, agentType, env, wtMounts, nil, true)
			return launched{cid, iid, out}, lerr
		},
		parked: func(why error) {
			d.systemComment(ctx, t.ID, NoteOf("task.comments.auditor_queued",
				"stage", d.stageNote(t, stage), "reason", why, "minutes", d.maxQueueMinutes()))
		},
		wanted: func(wctx context.Context) bool {
			cur, gerr := d.store.Get(wctx, t.ID)
			return gerr == nil && cur.Stage == stage
		},
	})
	if errors.Is(err, errRoomDropped) {
		// Return true: finishing the stage here would move a task somebody already moved.
		d.revokeRunKey(ctx, runKeyID)
		return true
	}
	if err != nil {
		return failed(err)
	}
	containerID, instID, outputDir := started.containerID, started.instanceID, started.outputDir

	_, updErr := d.store.Update(ctx, t.ID, Patch{CurrentRunID: &containerID, CurrentInstance: &instID})
	logx.Warn(updErr, "task.log.point_task_at_auditor", "task", t.ID)
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventAuditorRunStarted, instID, auth.AuditorAgent,
		i18n.Text("task.comments.auditor_run_started_event", "stage", stage, "agent", run.Agent),
		map[string]any{
			"container_id": containerID,
			"instance_id":  instID,
			"stage":        string(stage),
			"output_dir":   outputDir,
			"audited":      run,
		}), "task.log.record_auditor_started", "task", t.ID)

	stopDeadline := d.armDeadline(ctx, t.WorkspaceID, t.ID, containerID, func(minutes int) {
		d.systemComment(ctx, t.ID, NoteOf("task.comments.run_deadline_exceeded",
			"stage", d.stageNote(t, stage), "agent", auth.AuditorAgent, "minutes", minutes, "instance", instID))
	})
	d.launcher.OnExit(ctx, containerID, func(exitCode int, finalOutputDir, _ string) {
		stopDeadline()
		d.slots.release(slotWS)
		d.room.kick(slotWS)
		d.revokeRunKey(ctx, runKeyID)
		if finalOutputDir == "" {
			finalOutputDir = outputDir
		}
		d.finishAudit(ctx, t.ID, stage, run, containerID, instID, finalOutputDir, exitCode)
	})
	return true
}

func (d *Dispatcher) finishAudit(
	ctx context.Context, taskID string, stage Stage, run auditedRun,
	containerID, instanceID, outputDir string, exitCode int,
) {
	d.postAuditArtifacts(ctx, taskID, instanceID, outputDir)
	d.recordAuditorPromptEvent(ctx, taskID, instanceID, containerID, run.Agent, string(stage), outputDir)
	logx.Warn(d.store.RecordEvent(ctx, taskID, EventAuditorRunFinished, instanceID, auth.AuditorAgent,
		i18n.Text("task.comments.auditor_run_finished_event", "stage", stage, "code", exitCode),
		map[string]any{
			"container_id": containerID,
			"instance_id":  instanceID,
			"stage":        string(stage),
			"exit":         exitCode,
			"audited":      run.Instance,
		}), "task.log.record_auditor_finished", "task", taskID)

	t, err := d.store.Get(ctx, taskID)
	logx.Warn(err, "task.log.finish_audit_get_task", "task", taskID)
	if canceledExit(exitCode) {
		run.Exit = exitCode
		d.finishStageRun(ctx, taskID, t, stage, run, auditorVerdict{})
		return
	}
	verdict := d.auditorVerdict(ctx, taskID, instanceID, outputDir)
	if verdict.Verdict == "" {
		// The stage was meant to pass through its auditor, so a run with no verdict cannot move it on.
		note := NoteOf("task.comments.auditor_no_verdict",
			"stage", d.stageNote(t, stage), "code", exitCode, "instance", instanceID)
		wsID := ""
		if t != nil {
			wsID = t.WorkspaceID
		}
		d.systemComment(ctx, taskID, note.Then(d.engineFailureNotice(wsID, run.Agent, containerID, outputDir)))
		d.setStatus(ctx, taskID, StatusBlocked)
		return
	}
	d.finishStageRun(ctx, taskID, t, stage, run, verdict)
}

func canceledExit(code int) bool { return code == 130 || code == 143 }

// A *.posted marker means the container uploaded the file itself (pods); otherwise the daemon posts
// it from the dir (Docker).
func (d *Dispatcher) postAuditArtifacts(ctx context.Context, taskID, instanceID, outputDir string) {
	if _, err := os.Stat(filepath.Join(outputDir, "feedback.posted")); err != nil {
		if body, ok := readFile(outputDir, "feedback.md"); ok {
			_, err := d.store.AddReport(ctx, taskID, SourceAuditor, auth.AuditorAgent, ReportKindAuditorFeedback, "", body, instanceID)
			logx.Warn(err, "task.log.add_auditor_feedback", "task", taskID)
		}
	}
	if _, err := os.Stat(filepath.Join(outputDir, "auditor-comment.posted")); err != nil {
		if body, ok := readFile(outputDir, "auditor-comment.md"); ok {
			_, err := d.store.AddComment(ctx, taskID, SourceAuditor, auth.AuditorAgent, body)
			logx.Warn(err, "task.log.add_auditor_comment", "task", taskID)
		}
	}
}

func (d *Dispatcher) reconcileAudit(ctx context.Context, e ExitedTaskAgent, taskID string, stage Stage) {
	raw, ok := d.store.EventPayloadForInstance(ctx, EventAuditorRunStarted, e.InstanceID)
	var started struct {
		Audited auditedRun `json:"audited"`
	}
	if !ok || json.Unmarshal(raw, &started) != nil || started.Audited.Agent == "" {
		started.Audited = auditedRun{Agent: e.AgentType}
	}
	d.finishAudit(ctx, taskID, stage, started.Audited, e.ContainerID, e.InstanceID, e.OutputDir, e.ExitCode)
}

// The feedback route checks this against the auditor key's scope so a verdict only lands on the run
// that key was minted for.
func IsAuditInstanceFor(instanceID, taskID, stage string) bool {
	gotTask, gotStage, ok := parseAuditInstanceID(instanceID)
	return ok && gotTask == taskID && string(gotStage) == stage
}

func parseAuditInstanceID(instanceID string) (taskID string, stage Stage, ok bool) {
	return parseStageInstanceID(instanceID, auditInstanceAnchor)
}
