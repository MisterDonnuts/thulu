package task

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

type postRunOpts struct {
	NoAdvance bool
	NoAudit   bool
	// The task's stage when the run was launched; empty reads as the run's own stage.
	TaskStage Stage
	// A continuation answers the person who wrote to the agent, so its comment.md is posted; a
	// stage run hands off through verdict.md and its report instead.
	Reply bool
}

func (d *Dispatcher) postStageRun(
	ctx context.Context,
	taskID string,
	stage Stage,
	agentName, containerID, instanceID, outputDir string,
	exitCode int,
	opts postRunOpts,
) {
	posted := d.postRunArtifacts(ctx, taskID, agentName, instanceID, outputDir, opts.Reply)
	d.copyAllScreenshots(ctx, taskID, outputDir)

	t, getErr := d.store.Get(ctx, taskID)
	logx.Warn(getErr, "task.log.post_stage_run_get_task", "task", taskID)

	flagsCreated := 0
	if info, _ := d.stagesFor(t).Get(stage); info.IngestFlags {
		flagsCreated = d.ingestFlagsYAML(ctx, taskID, stage, agentName, outputDir)
	}

	d.commitAndPushWorktrees(ctx, t, taskID, stage, agentName, exitCode)
	locked := d.lockedTestsAfterRun(ctx, t, stage, instanceID, outputDir, exitCode)

	stoppedBy, stopped := d.takeStopped(containerID)
	outcome, turns := runOutcome(outputDir)
	wsID := ""
	if t != nil {
		wsID = t.WorkspaceID
	}
	d.noteRunEnd(ctx, t, taskID, stage, runEnd{
		WorkspaceID: wsID, Agent: agentName, ContainerID: containerID, OutputDir: outputDir,
		Exit: exitCode, Posted: posted, Flags: flagsCreated, Stopped: stopped, StoppedBy: stoppedBy,
		Outcome: outcome, Turns: turns,
	})
	finished := map[string]any{
		"container_id": containerID,
		"instance_id":  instanceID,
		"agent":        agentName,
		"stage":        string(stage),
		"output_dir":   outputDir,
		"exit":         exitCode,
		"artifacts":    posted,
		"flags":        flagsCreated,
	}
	if outcome != "" {
		finished["outcome"] = outcome
		finished["turns"] = turns
	}
	if stopped {
		finished["stopped_by"] = stoppedBy
	}
	logx.Warn(d.store.RecordEvent(ctx, taskID, EventAgentRunFinished, instanceID, "agent:"+agentName,
		NoteOf("task.comments.run_finished_event",
			"stage", d.stageNote(t, stage), "code", exitCode, "artifacts", posted, "flags", flagsCreated).Text(),
		finished), "task.log.record_run_finished", "task", taskID)

	d.captureSession(ctx, taskID, wsID, stage, agentName, outputDir, containerID)

	// Everything above keeps the run's work. What follows decides for the stage the task is in,
	// so a run the task outlived stops here.
	if d.outlivedAfterRun(ctx, taskID, outlivedRun{
		Stage: stage, Agent: agentName, Instance: instanceID, ContainerID: containerID, Exit: exitCode,
	}, opts) {
		return
	}

	if stopped {
		d.store.ReleaseQueuedTestCases(ctx, taskID, "octopus")
		d.setStatus(ctx, taskID, StatusStopped)
		return
	}

	if d.holdForQuestions(ctx, taskID, containerID) {
		return
	}
	// The run a person's access relaunched is over: a case it left queued did not run.
	d.store.ReleaseQueuedTestCases(ctx, taskID, "octopus")

	// Must run after holdForQuestions: a run that parks for answers has not done the work yet.
	d.resetApprovalsAfterRun(ctx, t, stage, exitCode, instanceID)

	// The auditor would hit the same wall, and there is no work to judge.
	opts.NoAudit = opts.NoAudit || d.engineNeverWorked(wsID, agentName, containerID, outputDir, exitCode)

	run := auditedRun{Agent: agentName, Instance: instanceID, OutputDir: outputDir, Exit: exitCode, NoAdvance: opts.NoAdvance}
	// A stage that declared itself failed has nothing for the auditor to certify.
	own, took := d.takeStageVerdict(ctx, t, stage, run)
	if took {
		d.finishStageRun(ctx, taskID, t, stage, run, own)
		return
	}
	if t != nil {
		info, _ := d.stagesFor(t).Get(stage)
		facts := auditFacts{
			Exit: exitCode, OutOfTurns: outcome == engine.OutcomeMaxTurns,
			ExpectsVerdict: info.AgentVerdict && exitCode == 0 && !opts.NoAdvance, Verdict: own.Verdict,
			LockedTouched: locked.Touched, LockedUnchecked: locked.Unchecked,
			Flags: flagsCreated, Artifacts: posted,
		}
		if agentType, ok := d.wantsAudit(ctx, t, stage, &run, opts, facts); ok && d.launchAuditor(ctx, t, stage, agentType, run) {
			return
		}
	}
	d.finishStageRun(ctx, taskID, t, stage, run, stageVerdict{})
}

func (d *Dispatcher) finishStageRun(ctx context.Context, taskID string, t *Task, stage Stage, run auditedRun, verdict stageVerdict) {
	// takeStageVerdict already recorded the agent's own verdict.
	if verdict.Verdict != "" && !verdict.fromAgent() {
		logx.Warn(d.store.RecordEvent(ctx, taskID, EventAuditorVerdict, "", auth.AuditorAgent,
			fmt.Sprintf("%s · %s", stage, verdict.Verdict),
			map[string]string{"verdict": verdict.Verdict, "kick_back_to": verdict.KickBackTo, "stage": string(stage)}),
			"task.log.record_auditor_verdict", "task", taskID)
	}

	if !verdict.fromAgent() && verdict.Agent == "" {
		// The auditor speaks for the agent it audited; the block label names that agent.
		verdict.Agent = run.Agent
	}
	if d.applyVerdict(ctx, t, taskID, stage, verdict) {
		return
	}

	if d.deliverLateChanges(ctx, taskID, run.Instance) {
		return
	}

	if pending := d.conv.drain(taskID); pending != "" {
		d.deliverPending(ctx, taskID, pending)
		return
	}

	// The run removed the skip of a column the task had passed: now that it is over, the task goes
	// back there.
	if run.Exit == 0 && d.returnForSkip(ctx, t, stage) {
		return
	}

	if run.NoAdvance {
		d.settleStatus(ctx, taskID, run.Exit, run.OutputDir)
		return
	}
	d.autoAdvanceStage(ctx, t, taskID, stage, run.Exit, run.OutputDir)
}

// Re-read the task: the finished run just wrote its session id and may have moved it.
func (d *Dispatcher) deliverPending(ctx context.Context, taskID, pending string) {
	t, err := d.store.Get(ctx, taskID)
	if err != nil || t == nil {
		logx.Warn(err, "task.log.reload_for_queued_messages", "task", taskID)
		d.systemMessage(ctx, taskID, i18n.Text("task.comments.pending_reload_failed"))
		return
	}
	if err := d.ContinueStage(ctx, t, pending); err != nil {
		d.systemMessage(ctx, taskID, i18n.Text("task.comments.pending_resume_failed", "err", apperr.Text(err)))
		return
	}
	d.systemMessage(ctx, taskID, i18n.Text("task.comments.pending_resuming"))
}

func (d *Dispatcher) settleStatus(ctx context.Context, taskID string, exitCode int, outputDir string) {
	if exitCode != 0 {
		if runHitMaxTurns(outputDir) {
			d.setStatus(ctx, taskID, StatusOutOfTurns)
			return
		}
		d.setStatus(ctx, taskID, StatusFailed)
		return
	}
	d.setStatus(ctx, taskID, StatusIdle)
}

// The entrypoint posts _index.md and comment.md itself and leaves a .posted marker; skip those or
// they post twice. comment.md counts only as a continuation's reply.
func (d *Dispatcher) postRunArtifacts(ctx context.Context, taskID, agentName, instanceID, outputDir string, reply bool) int {
	if _, err := os.Stat(filepath.Join(outputDir, "_index.posted")); err != nil {
		if body, ok := readFile(outputDir, "_index.md"); ok {
			_, err := d.store.AddReport(ctx, taskID, SourceAgent, agentName, ReportKindAgentIndex, "", body, instanceID)
			logx.Warn(err, "task.log.add_agent_index_report", "task", taskID)
		}
	}
	if _, err := os.Stat(filepath.Join(outputDir, "comment.posted")); err != nil && reply {
		if body, ok := readFile(outputDir, "comment.md"); ok {
			logx.Warn(d.postAgentComment(ctx, taskID, "", agentName, body), "task.log.add_agent_comment", "task", taskID)
		}
	}
	d.postVerdictArtifact(ctx, taskID, agentName, instanceID, outputDir)
	return d.countRunReports(ctx, taskID, instanceID)
}

// Count from the store: under Kubernetes the agent uploads reports through the API and nothing is
// in outputDir.
func (d *Dispatcher) countRunReports(ctx context.Context, taskID, instanceID string) int {
	if taskID == "" || instanceID == "" {
		return 0
	}
	reports, err := d.store.ListReports(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.count_run_reports", "task", taskID)
		return 0
	}
	n := 0
	for i := range reports {
		if reports[i].InstanceID == instanceID {
			n++
		}
	}
	return n
}

// Applies the auditor's verdict or the stage agent's own; true means the run's end must not settle
// or move the task.
func (d *Dispatcher) applyVerdict(ctx context.Context, t *Task, taskID string, stage Stage, verdict stageVerdict) bool {
	switch verdict.Verdict {
	case verdictKickBack:
		if t == nil {
			d.block(ctx, taskID, stage, verdictBlockInfo(verdict))
			return true
		}
		d.applyKickBackVerdict(ctx, t, stage, verdict)
		return true
	case verdictBlock:
		if verdict.missing {
			d.blockMissingVerdict(ctx, t, taskID, stage, verdict)
			return true
		}
		if verdict.fromAgent() {
			d.systemComment(ctx, taskID, NoteOf("task.comments.verdict_block",
				"agent", verdict.Agent, "stage", d.stageNote(t, stage), "reason", verdict.reasonCode()))
			d.openStopThread(ctx, taskID, verdict)
		} else {
			d.systemComment(ctx, taskID, NoteOf("task.comments.auditor_block"))
		}
		d.block(ctx, taskID, stage, verdictBlockInfo(verdict))
		return true
	case "needs_more_turns":
		if verdict.fromAgent() {
			return false
		}
		d.handleNeedsMoreTurns(ctx, t, taskID, stage)
		return true
	}
	return false
}

func (d *Dispatcher) applyKickBackVerdict(ctx context.Context, t *Task, stage Stage, verdict stageVerdict) {
	stages := d.stagesFor(t)
	body := quotedVerdictBody(verdict.Body)
	if info, _ := stages.Get(stage); verdict.fromAgent() && info.ReleaseResources {
		// The branch is merged and the worktree gone: going back would rebuild it from merged
		// work. A broken deploy is a new task.
		d.systemComment(ctx, t.ID, NoteOf("task.comments.verdict_kick_back_released",
			"agent", verdict.Agent, "stage", d.stageNote(t, stage), "reason", verdict.reasonCode(), "body", body))
		b := verdictBlockInfo(verdict)
		b.Cause = BlockReleased
		d.block(ctx, t.ID, stage, b)
		return
	}
	target := kickBackTarget(stages, stage, Stage(verdict.KickBackTo))
	redo := !verdict.fromAgent() && Stage(verdict.KickBackTo) == stage && rerunnable(stages, stage)
	if redo {
		target = stage
	}
	// A skip removed during this run sent the task further back than the verdict does: the
	// verdict's send-back must not lose that return.
	if back := d.skipReturnTarget(ctx, t, stage); back != "" && (target == "" || d.stageBefore(t, back, target)) {
		target, redo = back, false
	}
	if target == "" {
		d.systemComment(ctx, t.ID, NoteOf("task.comments.kick_back_no_target", "stage", d.stageNote(t, stage)))
		b := verdictBlockInfo(verdict)
		b.Cause = BlockNoTarget
		d.block(ctx, t.ID, stage, b)
		return
	}
	req := kickBackReq{From: stage, Target: target, Via: ViaKickBack, Actor: auth.AuditorAgent, Agent: verdict.Agent, Reason: verdict.Body,
		Notes: []Note{NoteOf("task.comments.auditor_kick_back", "stage", d.stageNote(t, target))}}
	if redo {
		req.Notes = []Note{NoteOf("task.comments.auditor_kick_back_same", "stage", d.stageNote(t, target))}
	}
	if verdict.fromAgent() && d.repeatedKickBack(ctx, t, stage, target, verdict) {
		return
	}
	if verdict.fromAgent() {
		req.Via, req.Actor = ViaVerdict, "agent:"+verdict.Agent
		req.Notes = []Note{NoteOf("task.comments.verdict_kick_back",
			"agent", verdict.Agent, "from", d.stageNote(t, stage), "stage", d.stageNote(t, target),
			"reason", verdict.reasonCode())}
	}
	d.kickBack(ctx, t, req)
}

// A second send-back from stage for the same single-round reason blocks the task where it is. The
// verdict being applied is already recorded, so a count of two means it happened before.
func (d *Dispatcher) repeatedKickBack(ctx context.Context, t *Task, stage, target Stage, verdict stageVerdict) bool {
	code := verdict.reasonCode()
	if !singleRoundReasons[code] || d.store.CountVerdictKickBacks(ctx, t.ID, stage, code) < 2 {
		return false
	}
	// Same rule as kickBack: a person who moved the task meanwhile wins.
	if cur, err := d.store.GetRow(ctx, t.ID); err != nil || cur.Stage != stage {
		return true
	}
	d.systemComment(ctx, t.ID, NoteOf("task.comments.verdict_kick_back_repeated",
		"agent", verdict.Agent, "from", d.stageNote(t, stage), "stage", d.stageNote(t, target),
		"reason", code, "body", quotedVerdictBody(verdict.Body)))
	b := verdictBlockInfo(verdict)
	b.Cause = BlockKickBackRepeated
	d.block(ctx, t.ID, stage, b)
	return true
}

func (d *Dispatcher) handleNeedsMoreTurns(ctx context.Context, t *Task, taskID string, stage Stage) {
	const maxAutoRelaunches = 3
	// Per entry into the stage: a task the flow sends back here gets its relaunches again.
	runs := d.store.CountRunsSinceStageEntry(ctx, taskID, stage)
	if runs >= maxAutoRelaunches {
		d.systemComment(ctx, taskID, NoteOf("task.comments.auditor_relaunch_limit", "max", maxAutoRelaunches))
		d.setStatus(ctx, taskID, StatusNeedsMoreTurns)
		return
	}
	if t == nil {
		d.setStatus(ctx, taskID, StatusNeedsMoreTurns)
		return
	}
	d.systemComment(ctx, taskID, NoteOf("task.comments.auditor_relaunching", "run", runs+1, "max", maxAutoRelaunches))
	if err := d.RelaunchStage(ctx, t, 0); err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.needs_more_turns_relaunch_failed", "task", taskID, "err", err))
		d.setStatus(ctx, taskID, StatusNeedsMoreTurns)
	}
}

func (d *Dispatcher) autoAdvanceStage(ctx context.Context, t *Task, taskID string, stage Stage, exitCode int, outputDir string) {
	if exitCode != 0 {
		d.settleStatus(ctx, taskID, exitCode, outputDir)
		return
	}
	stages := d.stagesFor(t)
	info, _ := stages.Get(stage)
	if !info.AutoAdvance {
		d.setStatus(ctx, taskID, StatusIdle)
		return
	}
	// Re-read: flags.yaml of this very run may just have set the flow flags.
	if fresh, err := d.store.GetRow(ctx, taskID); t != nil && err == nil && fresh != nil {
		t.FlowFlags = fresh.FlowFlags
		t.StageSkips = fresh.StageSkips
	}
	next, skipped := d.advanceTarget(ctx, t, stage)
	if next == "" {
		return
	}
	if d.holdForApprovals(ctx, t, taskID, stage, next) {
		return
	}

	// Reset before the move: HandleStageEntered sets running if it launches, and a later reset
	// would clobber it.
	d.setStatus(ctx, taskID, StatusIdle)
	// Only from stage: a person, or a merge on GitHub, may have moved the task while it ran.
	updated, err := d.store.UpdateIfStage(ctx, taskID, stage, Patch{Stage: &next})
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.auto_advance_update_failed", "err", err))
		return
	}
	d.recordSkipped(ctx, t, skipped)
	moved := map[string]any{"from": string(stage), "to": string(next), "via": ViaAutoAdvance}
	if len(skipped) > 0 {
		moved["skipped"] = skipped
	}
	logx.Warn(d.store.RecordEvent(ctx, taskID, EventStageEntered, "", "octopus",
		fmt.Sprintf("%s → %s", stage, next), moved),
		"task.log.record_auto_advance", "task", taskID)
	d.stageMoved(updated, stage)
	d.HandleStageEntered(ctx, updated, stage)
}

func (d *Dispatcher) recordPromptEvent(ctx context.Context, taskID, instanceID, containerID, agent, stage, outputDir string) {
	d.recordPromptFileEvent(ctx, taskID, instanceID, containerID, agent, stage, outputDir,
		".thulu-prompt.md", EventAgentPromptInit, "agent:"+agent, i18n.Text("task.labels.initial_prompt"), 20*time.Second)
}

func (d *Dispatcher) recordAuditorPromptEvent(ctx context.Context, taskID, instanceID, containerID, agent, stage, outputDir string) {
	d.recordPromptFileEvent(ctx, taskID, instanceID, containerID, agent, stage, outputDir,
		".thulu-auditor-prompt.md", EventAuditorPrompt, "auditor", i18n.Text("task.labels.auditor_prompt"), 0)
}

func (d *Dispatcher) recordPromptFileEvent(ctx context.Context,
	taskID, instanceID, containerID, agent, stage, outputDir, file string,
	kind EventKind, actor, label string, wait time.Duration,
) {
	if outputDir == "" {
		return
	}
	deadline := time.Now().Add(wait)
	var data []byte
	for {
		if b, err := safepath.ReadFile(outputDir, file); err == nil && len(b) > 0 {
			data = b
			break
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(data) == 0 {
		return
	}
	body := string(data)
	summary := i18n.Text("task.comments.prompt_event_summary", "label", label, "count", len(body))
	logx.Warn(d.store.RecordEvent(ctx, taskID, kind, instanceID, actor, summary,
		map[string]any{
			"instance_id":  instanceID,
			"container_id": containerID,
			"agent":        agent,
			"stage":        stage,
			"prompt":       body,
			"size":         len(body),
		}), "task.log.record_prompt_event", "kind", kind, "task", taskID)
}

// The auditor's feedback.md and a stage agent's verdict.md share this front matter.
type stageVerdict struct {
	Verdict    string `yaml:"verdict"`
	KickBackTo string `yaml:"kick_back_to,omitempty"`
	Score      string `yaml:"score,omitempty"`
	Reason     string `yaml:"reason,omitempty"`
	// The optional fields for people and the next stage (headline, needs_person, delta, next).
	verdictFacts `yaml:",inline"`
	// What follows the front matter: the feedback the target stage reads.
	Body string `yaml:"-"`
	// "" is the auditor; verdictFromAgent is the stage's own agent.
	Source string `yaml:"-"`
	Agent  string `yaml:"-"`
	// The stage takes the agent's verdict and the run left none that reads; applyVerdict blocks.
	missing bool
	// With missing: the run's outputs did not all reach the daemon, so the verdict may be among
	// what the upload lost.
	undelivered bool
	// The run instance whose verdict this is; set for a stage agent's own verdict.
	instance string
}

const (
	verdictContinue  = "continue"
	verdictKickBack  = "kick_back"
	verdictBlock     = "block"
	verdictFromAgent = "agent"
)

func (v stageVerdict) fromAgent() bool { return v.Source == verdictFromAgent }

// Who stopped the task with this verdict: the stage's agent with its reason code, or the auditor of
// the agent it audited.
func verdictBlockInfo(v stageVerdict) BlockInfo {
	if v.fromAgent() {
		return BlockInfo{Cause: BlockVerdict, Agent: v.Agent, Reason: v.reasonCode()}
	}
	return BlockInfo{Cause: BlockAuditor, Agent: v.Agent}
}

// Docker bind-mounts the run dir; a pod uploads feedback.md to /tasks/{id}/auditor-feedback, so
// fall back to the stored report.
// Disk wins: a stored report may be an earlier upload for the same instance.
func (d *Dispatcher) auditorVerdict(ctx context.Context, taskID, instanceID, outputDir string) stageVerdict {
	return d.readVerdict(ctx, taskID, instanceID, outputDir, "feedback.md", ReportKindAuditorFeedback)
}

func (d *Dispatcher) readVerdict(ctx context.Context, taskID, instanceID, outputDir, file, kind string) stageVerdict {
	if v := readVerdictFile(outputDir, file); v.Verdict != "" {
		return v
	}
	if taskID == "" || instanceID == "" {
		return stageVerdict{}
	}
	reports, err := d.store.ListReports(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.read_auditor_feedback", "task", taskID)
		return stageVerdict{}
	}
	for i := len(reports) - 1; i >= 0; i-- {
		r := reports[i]
		if r.Kind == kind && r.InstanceID == instanceID {
			return parseVerdict(r.Body)
		}
	}
	return stageVerdict{}
}

func readVerdictFile(outputDir, file string) stageVerdict {
	if outputDir == "" {
		return stageVerdict{}
	}
	data, err := safepath.ReadFile(outputDir, file)
	if err != nil {
		return stageVerdict{}
	}
	return parseVerdict(string(data))
}

func parseVerdict(body string) stageVerdict {
	s := strings.TrimSpace(body)
	if !strings.HasPrefix(s, "---") {
		return stageVerdict{}
	}
	parts := strings.SplitN(s, "---", 3)
	if len(parts) < 3 {
		return stageVerdict{}
	}
	var v stageVerdict
	logx.Warn(yaml.Unmarshal([]byte(parts[1]), &v), "task.log.parse_auditor_verdict")
	v.Verdict = strings.TrimSpace(v.Verdict)
	v.Body = strings.TrimSpace(parts[2])
	return v
}

func (d *Dispatcher) copyAllScreenshots(ctx context.Context, taskID, outputDir string) {
	logx.Warn(filepath.Walk(outputDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if filepath.Base(filepath.Dir(p)) != "screenshots" {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" {
			return nil
		}
		d.attachFile(ctx, taskID, outputDir, p, info)
		return nil
	}), "task.log.walk_screenshots", "task", taskID)
}

func (d *Dispatcher) attachFile(ctx context.Context, taskID, outputDir, srcPath string, info os.FileInfo) {
	attachmentsDir := d.store.AttachmentsDir()
	if err := os.MkdirAll(attachmentsDir, 0o750); err != nil {
		return
	}
	if err := safepath.MkdirAll(attachmentsDir, taskID, 0o750); err != nil {
		return
	}
	filename := SanitizeFilename(info.Name())
	relPath := filepath.Join(taskID, fmt.Sprintf("%d-%s", time.Now().UnixNano(), filename))
	dstPath := filepath.Join(attachmentsDir, relPath)

	// outputDir is container-written: open through an os.Root so a planted symlink can't exfiltrate
	// a file outside it.
	rel, relErr := safepath.Rel(outputDir, srcPath)
	if relErr != nil {
		return
	}
	src, err := safepath.Open(outputDir, rel)
	if err != nil {
		return
	}
	defer src.Close()
	guard, err := d.store.ReserveAttachment(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.attachment_over_quota", "task", taskID, "file", filename)
		return
	}
	defer guard.Done()
	dst, err := safepath.Create(attachmentsDir, relPath)
	if err != nil {
		return
	}
	defer dst.Close()
	written, err := io.Copy(dst, guard.Limit(src))
	if err == nil {
		err = guard.Check(written)
	}
	if err != nil {
		if IsStorageQuotaExceeded(err) {
			logx.Warn(err, "task.log.attachment_over_quota", "task", taskID, "file", filename)
		}
		logx.Warn(os.Remove(dstPath), "task.log.remove_partial_attachment", "path", dstPath)
		return
	}
	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	_, attErr := d.store.AddAttachment(ctx, NewAttachment{
		TaskID:   taskID,
		Author:   string(SourceAgent),
		Filename: filename,
		MimeType: mimeType,
		Path:     dstPath,
		Size:     written,
	})
	logx.Warn(attErr, "task.log.record_attachment", "task", taskID)
}

// Read through an os.Root: the agent container may have planted a symlink in its output dir.
func readFile(dir, name string) (string, bool) {
	data, err := safepath.ReadFile(dir, name)
	if err != nil {
		return "", false
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return "", false
	}
	return body, true
}

type flagsYAMLDoc struct {
	flowSpec `yaml:",inline"`
	Flags    []flagYAMLItem `yaml:"flags"`
}

type flagYAMLItem struct {
	Kind    string                 `yaml:"kind"`
	Label   string                 `yaml:"label"`
	Payload map[string]interface{} `yaml:"payload,omitempty"`
}

// UnmarshalYAML also takes a bare string as a custom flag with that label. Agents write
// `flags: ["..."]` often enough, and refusing it threw away the whole file, surface and risk
// included.
func (f *flagYAMLItem) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		f.Label = value.Value
		return nil
	}
	type plain flagYAMLItem
	return value.Decode((*plain)(f))
}

// Also used by POST /tasks/{id}/flags/import, where Kubernetes agents upload flags.yaml; both paths
// must produce identical flags, flow flags and skips. actor.By signs what the file raises; the rest
// of actor decides whether its `skip:` list may add skips (MayAddSkips).
func ParseAndAddFlags(ctx context.Context, store *Store, taskID string, actor SkipActor, data []byte) (FlagsImport, error) {
	createdBy := actor.By
	var doc flagsYAMLDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return FlagsImport{}, apperr.WrapKind(apperr.KindInvalid, err, "task.errors.parse_flags_yaml")
	}
	var out FlagsImport
	for _, item := range doc.Flags {
		if strings.TrimSpace(item.Label) == "" {
			continue
		}
		f := &Flag{
			Kind:      FlagKind(item.Kind),
			Label:     item.Label,
			CreatedBy: createdBy,
		}
		if f.Kind == "" {
			f.Kind = FlagCustom
		}
		if len(item.Payload) > 0 {
			if raw, err := json.Marshal(item.Payload); err == nil {
				f.Payload = raw
			}
		}
		if err := store.AddFlagFrom(ctx, taskID, f, ApprovalFromAgent); err != nil {
			log.Printf("[task] %s", i18n.Log("task.log.add_flag_failed", "err", err))
			continue
		}
		logx.Warn(store.RecordEvent(ctx, taskID, EventFlagRaised, f.ID, createdBy,
			fmt.Sprintf("%s · %s", f.Kind, f.Label), nil), "task.log.record_flag_raised", "task", taskID)
		out.Created++
	}
	store.applyFlowSpec(ctx, taskID, actor, doc.flowSpec, &out)
	return out, nil
}

func (d *Dispatcher) ingestFlagsYAML(ctx context.Context, taskID string, stage Stage, agentName, outputDir string) int {
	// The agent may upload flags itself and leave flags.posted; in Docker mode the dir is shared,
	// so ingesting again would raise every flag twice.
	if _, err := os.Stat(filepath.Join(outputDir, "flags.posted")); err == nil {
		return 0
	}
	data, err := safepath.ReadFile(outputDir, "flags.yaml")
	if err != nil {
		return 0
	}
	actor := SkipActor{By: auth.Author{Name: agentName, Agent: true}.Label(), Agent: true, RunStage: stage, RunTask: taskID}
	res, err := ParseAndAddFlags(ctx, d.store, taskID, actor, data)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.flags_yaml_parse_failed", "err", err))
		d.systemComment(ctx, taskID, NoteOf("task.comments.flags_yaml_unparsable", "err", err))
		return 0
	}
	return res.Created
}

// How a run ended, for its comment.
type runEnd struct {
	WorkspaceID, Agent, ContainerID, OutputDir string
	Exit, Posted, Flags, Turns                 int
	Stopped                                    bool
	StoppedBy, Outcome                         string
}

// A clean exit is the agent_run_finished event alone; a person reads a comment only when the run
// failed, ran out of turns or was stopped. runFailureNote still runs on a clean exit: it records the
// LLM profile's outcome.
func (d *Dispatcher) noteRunEnd(ctx context.Context, t *Task, taskID string, stage Stage, r runEnd) {
	note := NoteOf("task.comments.run_finished", "stage", d.stageNote(t, stage), "code", r.Exit, "count", r.Posted)
	if r.Stopped {
		note = NoteOf("task.comments.run_stopped", "stage", d.stageNote(t, stage), "who", personArg(r.StoppedBy), "count", r.Posted)
	}
	if r.Flags > 0 {
		note = note.Add("task.comments.run_flags_raised", "count", r.Flags)
	}
	if !r.Stopped {
		note = note.Then(d.runFailureNote(ctx, r.WorkspaceID, r.Agent, r.ContainerID, r.OutputDir, r.Exit))
	}
	if !r.Stopped && r.Exit != 0 && r.Outcome == engine.OutcomeMaxTurns {
		note = note.Then(NoteOf("task.comments.run_out_of_turns", "count", r.Turns))
	}
	if r.Stopped || r.Exit != 0 {
		d.systemComment(ctx, taskID, note)
	}
}
