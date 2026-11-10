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
}

func (d *Dispatcher) postStageRun(
	ctx context.Context,
	taskID string,
	stage Stage,
	agentName, containerID, instanceID, outputDir string,
	exitCode int,
	opts postRunOpts,
) {
	posted := d.postRunArtifacts(ctx, taskID, agentName, instanceID, outputDir)
	d.copyAllScreenshots(ctx, taskID, outputDir)

	t, getErr := d.store.Get(ctx, taskID)
	logx.Warn(getErr, "task.log.post_stage_run_get_task", "task", taskID)

	flagsCreated := 0
	if info, _ := d.stagesFor(t).Get(stage); info.IngestFlags {
		flagsCreated = d.ingestFlagsYAML(ctx, taskID, agentName, outputDir)
	}

	d.commitAndPushWorktrees(ctx, t, taskID, stage, agentName, exitCode)

	stoppedBy, stopped := d.takeStopped(containerID)
	outcome, turns := runOutcome(outputDir)
	stats := NoteOf("task.comments.run_finished", "stage", d.stageNote(t, stage), "code", exitCode, "count", posted)
	if stopped {
		stats = NoteOf("task.comments.run_stopped", "stage", d.stageNote(t, stage), "who", personArg(stoppedBy), "count", posted)
	}
	if flagsCreated > 0 {
		stats = stats.Add("task.comments.run_flags_raised", "count", flagsCreated)
	}
	wsID := ""
	if t != nil {
		wsID = t.WorkspaceID
	}
	if !stopped {
		stats = stats.Then(d.runFailureNote(ctx, wsID, agentName, containerID, outputDir, exitCode))
	}
	if !stopped && exitCode != 0 && outcome == engine.OutcomeMaxTurns {
		stats = stats.Then(NoteOf("task.comments.run_out_of_turns", "count", turns))
	}
	d.systemComment(ctx, taskID, stats)
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

	if stopped {
		d.setStatus(ctx, taskID, StatusStopped)
		return
	}

	if d.holdForQuestions(ctx, taskID, containerID) {
		return
	}

	// Must run after holdForQuestions: a run that parks for answers has not done the work yet.
	d.resetApprovalsAfterRun(ctx, t, stage, exitCode, instanceID)

	// The auditor would hit the same wall, and there is no work to judge.
	opts.NoAudit = opts.NoAudit || d.engineNeverWorked(wsID, agentName, containerID, outputDir, exitCode)

	run := auditedRun{Agent: agentName, Instance: instanceID, OutputDir: outputDir, Exit: exitCode, NoAdvance: opts.NoAdvance}
	if t != nil {
		if agentType, ok := d.wantsAudit(t.WorkspaceID, stage, run, opts); ok && d.launchAuditor(ctx, t, stage, agentType, run) {
			return
		}
	}
	d.finishStageRun(ctx, taskID, t, stage, run, auditorVerdict{})
}

func (d *Dispatcher) finishStageRun(ctx context.Context, taskID string, t *Task, stage Stage, run auditedRun, verdict auditorVerdict) {
	if verdict.Verdict != "" {
		logx.Warn(d.store.RecordEvent(ctx, taskID, EventAuditorVerdict, "", auth.AuditorAgent,
			fmt.Sprintf("%s · %s", stage, verdict.Verdict),
			map[string]string{"verdict": verdict.Verdict, "kick_back_to": verdict.KickBackTo, "stage": string(stage)}),
			"task.log.record_auditor_verdict", "task", taskID)
	}

	if d.applyAuditorVerdict(ctx, t, taskID, stage, verdict) {
		return
	}

	if d.deliverLateChanges(ctx, taskID, run.Instance) {
		return
	}

	if pending := d.conv.drain(taskID); pending != "" {
		d.deliverPending(ctx, taskID, pending)
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
// they post twice.
func (d *Dispatcher) postRunArtifacts(ctx context.Context, taskID, agentName, instanceID, outputDir string) int {
	if _, err := os.Stat(filepath.Join(outputDir, "_index.posted")); err != nil {
		if body, ok := readFile(outputDir, "_index.md"); ok {
			_, err := d.store.AddReport(ctx, taskID, SourceAgent, agentName, ReportKindAgentIndex, "", body, instanceID)
			logx.Warn(err, "task.log.add_agent_index_report", "task", taskID)
		}
	}
	if _, err := os.Stat(filepath.Join(outputDir, "comment.posted")); err != nil {
		if body, ok := readFile(outputDir, "comment.md"); ok {
			logx.Warn(d.postAgentComment(ctx, taskID, "", agentName, body), "task.log.add_agent_comment", "task", taskID)
		}
	}
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

func (d *Dispatcher) applyAuditorVerdict(ctx context.Context, t *Task, taskID string, stage Stage, verdict auditorVerdict) bool {
	switch verdict.Verdict {
	case "kick_back":
		target := Stage(verdict.KickBackTo)
		if stages := d.stagesFor(t); !stages.Valid(target) {
			target = stages.KickbackDefault()
		}
		d.systemComment(ctx, taskID, NoteOf("task.comments.auditor_kick_back", "stage", d.stageNote(t, target)))
		d.setStatus(ctx, taskID, StatusIdle)
		updated, err := d.store.Update(ctx, taskID, Patch{Stage: &target})
		if err != nil {
			log.Printf("[task] %s", i18n.Log("task.log.kick_back_update_failed", "err", err))
			return true
		}
		logx.Warn(d.store.RecordEvent(ctx, taskID, EventStageEntered, "", "auditor",
			fmt.Sprintf("%s → %s (kick_back)", stage, target),
			map[string]string{"from": string(stage), "to": string(target), "via": "kick_back"}),
			"task.log.record_kick_back", "task", taskID)
		d.stageMoved(updated, stage)
		d.HandleStageEntered(ctx, updated, stage)
		return true
	case "block":
		d.systemComment(ctx, taskID, NoteOf("task.comments.auditor_block"))
		d.setStatus(ctx, taskID, StatusBlocked)
		return true
	case "needs_more_turns":
		d.handleNeedsMoreTurns(ctx, t, taskID, stage)
		return true
	}
	return false
}

func (d *Dispatcher) handleNeedsMoreTurns(ctx context.Context, t *Task, taskID string, stage Stage) {
	const maxAutoRelaunches = 3
	runs := d.store.CountRunsForStage(ctx, taskID, stage)
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
	next := stages.Next(stage)
	if next == "" {
		return
	}
	if d.holdForApprovals(ctx, t, taskID, stage, next) {
		return
	}

	// Reset before the move: HandleStageEntered sets running if it launches, and a later reset
	// would clobber it.
	d.setStatus(ctx, taskID, StatusIdle)
	updated, err := d.store.Update(ctx, taskID, Patch{Stage: &next})
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.auto_advance_update_failed", "err", err))
		return
	}
	logx.Warn(d.store.RecordEvent(ctx, taskID, EventStageEntered, "", "octopus",
		fmt.Sprintf("%s → %s", stage, next),
		map[string]string{"from": string(stage), "to": string(next)}),
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

type auditorVerdict struct {
	Verdict    string `yaml:"verdict"`
	KickBackTo string `yaml:"kick_back_to,omitempty"`
	Score      string `yaml:"score,omitempty"`
}

// Docker bind-mounts the run dir; a pod uploads feedback.md to /tasks/{id}/auditor-feedback, so
// fall back to the stored report.
// Disk wins: a stored report may be an earlier upload for the same instance.
func (d *Dispatcher) auditorVerdict(ctx context.Context, taskID, instanceID, outputDir string) auditorVerdict {
	if v := readAuditorVerdict(outputDir); v.Verdict != "" {
		return v
	}
	if taskID == "" || instanceID == "" {
		return auditorVerdict{}
	}
	reports, err := d.store.ListReports(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.read_auditor_feedback", "task", taskID)
		return auditorVerdict{}
	}
	for i := len(reports) - 1; i >= 0; i-- {
		r := reports[i]
		if r.Kind == ReportKindAuditorFeedback && r.InstanceID == instanceID {
			return parseAuditorVerdict(r.Body)
		}
	}
	return auditorVerdict{}
}

func readAuditorVerdict(outputDir string) auditorVerdict {
	data, err := safepath.ReadFile(outputDir, "feedback.md")
	if err != nil {
		return auditorVerdict{}
	}
	return parseAuditorVerdict(string(data))
}

func parseAuditorVerdict(body string) auditorVerdict {
	s := strings.TrimSpace(body)
	if !strings.HasPrefix(s, "---") {
		return auditorVerdict{}
	}
	parts := strings.SplitN(s, "---", 3)
	if len(parts) < 3 {
		return auditorVerdict{}
	}
	var v auditorVerdict
	logx.Warn(yaml.Unmarshal([]byte(parts[1]), &v), "task.log.parse_auditor_verdict")
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
	Flags []struct {
		Kind    string                 `yaml:"kind"`
		Label   string                 `yaml:"label"`
		Payload map[string]interface{} `yaml:"payload,omitempty"`
	} `yaml:"flags"`
}

// Also used by POST /tasks/{id}/flags/import, where Kubernetes agents upload flags.yaml; both paths
// must produce identical flags.
func ParseAndAddFlags(ctx context.Context, store *Store, taskID, createdBy string, data []byte) (int, error) {
	var doc flagsYAMLDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return 0, apperr.WrapKind(apperr.KindInvalid, err, "task.errors.parse_flags_yaml")
	}
	created := 0
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
		created++
	}
	return created, nil
}

func (d *Dispatcher) ingestFlagsYAML(ctx context.Context, taskID, agentName, outputDir string) int {
	// The agent may upload flags itself and leave flags.posted; in Docker mode the dir is shared,
	// so ingesting again would raise every flag twice.
	if _, err := os.Stat(filepath.Join(outputDir, "flags.posted")); err == nil {
		return 0
	}
	data, err := safepath.ReadFile(outputDir, "flags.yaml")
	if err != nil {
		return 0
	}
	created, err := ParseAndAddFlags(ctx, d.store, taskID, auth.Author{Name: agentName, Agent: true}.Label(), data)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.flags_yaml_parse_failed", "err", err))
		d.systemComment(ctx, taskID, NoteOf("task.comments.flags_yaml_unparsable", "err", err))
		return 0
	}
	return created
}
