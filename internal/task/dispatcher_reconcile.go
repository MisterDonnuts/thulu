package task

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Idempotent through the finish event postStageRun writes. The Kubernetes janitor re-runs it before
// every sweep, which is safe only because
// the kube launcher's scan leaves out pods a live exit watch still owns.
func (d *Dispatcher) Reconcile(ctx context.Context) {
	d.reconcileMu.Lock()
	defer d.reconcileMu.Unlock()
	exited, err := d.launcher.ScanExitedTaskAgents(ctx)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.reconcile_scan_failed", "err", err))
		return
	}
	counts := map[reconcileOutcome]int{}
	for _, e := range exited {
		counts[d.reconcileExit(ctx, e)]++
	}
	log.Printf("[task] %s", i18n.Log("task.log.reconcile_summary",
		"scanned", len(exited), "processed", counts[reconcileProcessed], "done", counts[reconcileAlreadyDone],
		"gone", counts[reconcileTaskGone], "unparsed", counts[reconcileUnparsed]))
}

type reconcileOutcome int

const (
	reconcileProcessed reconcileOutcome = iota
	reconcileAlreadyDone
	reconcileTaskGone
	reconcileUnparsed
)

func (d *Dispatcher) reconcileExit(ctx context.Context, e ExitedTaskAgent) reconcileOutcome {
	if taskID, stage, ok := parseTaskInstanceID(e.InstanceID); ok {
		return d.replayOrphan(ctx, e, taskID, d.store.HasFinishEventForInstance, func() {
			log.Printf("[task] %s", i18n.Log("task.log.reconcile_replay_stage",
				"instance", e.InstanceID, "task", taskID, "stage", stage, "code", e.ExitCode))
			// The launch-kind label is how a replay knows the run's kind; without it an orphaned
			// continuation is audited and advances the task.
			d.postStageRun(ctx, taskID, stage, e.AgentType, e.ContainerID, e.InstanceID, e.OutputDir, e.ExitCode,
				ParseLaunchKind(string(e.LaunchKind)).postRunOpts())
		})
	}
	if taskID, stage, ok := parseAuditInstanceID(e.InstanceID); ok {
		return d.replayOrphan(ctx, e, taskID, d.store.HasAuditFinishEventForInstance, func() {
			log.Printf("[task] %s", i18n.Log("task.log.reconcile_finish_audit",
				"instance", e.InstanceID, "task", taskID, "stage", stage, "code", e.ExitCode))
			d.reconcileAudit(ctx, e, taskID, stage)
		})
	}
	if taskID, threadID, ok := parseMentionInstanceID(e.InstanceID); ok {
		return d.replayOrphan(ctx, e, taskID, d.store.HasMentionFinishEventForInstance, func() {
			log.Printf("[task] %s", i18n.Log("task.log.reconcile_replay_mention",
				"instance", e.InstanceID, "task", taskID, "code", e.ExitCode))
			d.postMentionRun(ctx, taskID, threadID, e.AgentType, e.ContainerID, e.InstanceID, e.OutputDir, e.ExitCode)
		})
	}
	return reconcileUnparsed
}

func (d *Dispatcher) replayOrphan(ctx context.Context, e ExitedTaskAgent, taskID string,
	finished func(context.Context, string) bool, replay func(),
) reconcileOutcome {
	if finished(ctx, e.InstanceID) {
		return reconcileAlreadyDone
	}
	if _, err := d.store.Get(ctx, taskID); err != nil {
		return reconcileTaskGone
	}
	replay()
	return reconcileProcessed
}

// The Kubernetes janitor deletes a terminated agent pod only when this holds, so a pod whose exit a
// restart lost survives until Reconcile replays it.
func (d *Dispatcher) ExitSettled(ctx context.Context, instanceID string) bool {
	if taskID, _, ok := parseTaskInstanceID(instanceID); ok {
		return d.store.HasFinishEventForInstance(ctx, instanceID) || d.taskGone(ctx, taskID)
	}
	if taskID, _, ok := parseAuditInstanceID(instanceID); ok {
		return d.store.HasAuditFinishEventForInstance(ctx, instanceID) || d.taskGone(ctx, taskID)
	}
	if taskID, _, ok := parseMentionInstanceID(instanceID); ok {
		return d.store.HasMentionFinishEventForInstance(ctx, instanceID) || d.taskGone(ctx, taskID)
	}
	return true
}

func (d *Dispatcher) taskGone(ctx context.Context, taskID string) bool {
	_, err := d.store.GetRow(ctx, taskID)
	return errors.Is(err, ErrNotFound)
}

func (d *Dispatcher) ReconcileDone(ctx context.Context) {
	tasks, err := d.store.List(ctx)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.reconcile_done_list_failed", "err", err))
		return
	}
	cleaned := 0
	for i := range tasks {
		if info, _ := d.stagesFor(&tasks[i]).Get(tasks[i].Stage); info.Terminal {
			d.cleanupDoneTask(ctx, &tasks[i])
			cleaned++
		}
	}
	log.Printf("[task] %s", i18n.Log("task.log.reconcile_done_cleaned", "count", cleaned))
}

func InstanceTaskID(instanceID string) (string, bool) {
	if id, _, ok := parseTaskInstanceID(instanceID); ok {
		return id, true
	}
	if id, _, ok := parseAuditInstanceID(instanceID); ok {
		return id, true
	}
	if id, _, ok := parseMentionInstanceID(instanceID); ok {
		return id, true
	}
	return "", false
}

func InstanceOfTask(instanceID, taskID string) bool {
	if got, _, ok := parseTaskInstanceID(instanceID); ok && got == taskID {
		return true
	}
	if got, _, ok := parseAuditInstanceID(instanceID); ok && got == taskID {
		return true
	}
	got, _, ok := parseMentionInstanceID(instanceID)
	return ok && got == taskID
}

func InstanceStage(instanceID string) (Stage, bool) {
	if _, stage, ok := parseTaskInstanceID(instanceID); ok {
		return stage, true
	}
	if _, stage, ok := parseAuditInstanceID(instanceID); ok {
		return stage, true
	}
	if _, _, ok := parseMentionInstanceID(instanceID); ok {
		return types.StageMention, true
	}
	return "", false
}

func parseTaskInstanceID(instanceID string) (taskID string, stage Stage, ok bool) {
	return parseStageInstanceID(instanceID, "-task-")
}

// Must match the instance ids launchStage and the auditor launch mint.
func parseStageInstanceID(instanceID, anchor string) (taskID string, stage Stage, ok bool) {
	// Last occurrence: an agent name may contain the anchor, the task id and stage after it cannot.
	idx := strings.LastIndex(instanceID, anchor)
	if idx < 0 {
		return "", "", false
	}
	tail := instanceID[idx+len(anchor):]
	// taskID has no hyphens (hex-generated), so split from the right.
	lastDash := strings.LastIndex(tail, "-")
	if lastDash < 0 {
		return "", "", false
	}
	timestampPart := tail[lastDash+1:]
	if _, err := strconv.Atoi(timestampPart); err != nil {
		return "", "", false
	}
	rest := tail[:lastDash]
	stageDash := strings.LastIndex(rest, "-")
	if stageDash < 0 {
		return "", "", false
	}
	stageStr := rest[stageDash+1:]
	taskID = rest[:stageDash]
	// Shape-only, not registry membership: a run whose stage was deleted mid-flight must still
	// reconcile its artifacts and commit.
	if taskID == "" || strings.Contains(taskID, "-") || !types.ValidStageID(stageStr) {
		return "", "", false
	}
	return taskID, Stage(stageStr), true
}
