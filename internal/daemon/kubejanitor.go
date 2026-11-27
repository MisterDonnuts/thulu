//go:build !foss

package daemon

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/syncx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// A pod mounting a claim that is gone is stranded for good; the grace only covers a claim that is
// created right after its pod.
const strandedPodGrace = 10 * time.Minute

const (
	// orphanEnvSecretAge covers a launch in flight: the env Secret exists a moment before its pod.
	orphanEnvSecretAge = 10 * time.Minute
	// A pod no node took is deleted this long after its run deadline. One with no deadline gets
	// unscheduledDefaultDeadline, the default run deadline.
	unscheduledPodGrace        = 10 * time.Minute
	unscheduledDefaultDeadline = 4 * time.Hour
)

type kubeJanitor struct {
	rt         *kuberuntime.Runtime
	sandboxMgr *sandbox.Manager
	tasks      *task.Store
	runs       runReconciler

	interval             time.Duration
	fastInterval         time.Duration
	agentExitedTTL       time.Duration
	sandboxExitedTTL     time.Duration
	failedTaskSandboxTTL time.Duration

	secretsForbidden sync.Once
}

func newKubeJanitor(rt *kuberuntime.Runtime, s *sandbox.Manager, t *task.Store, runs runReconciler) *kubeJanitor {
	return &kubeJanitor{
		rt:                   rt,
		sandboxMgr:           s,
		tasks:                t,
		runs:                 runs,
		interval:             1 * time.Hour,
		fastInterval:         1 * time.Minute,
		agentExitedTTL:       2 * time.Hour,
		sandboxExitedTTL:     6 * time.Hour,
		failedTaskSandboxTTL: 6 * time.Hour,
	}
}

// The 2-minute warmup keeps the first sweep clear of the startup reconcile. The fast sweep runs
// at boot and every minute: what it finds runs code or holds credentials.
func (j *kubeJanitor) Start(ctx context.Context) {
	go func() {
		j.SweepLeftovers(ctx)
		syncx.TickUntil(ctx, j.fastInterval, j.SweepLeftovers)
	}()
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Minute):
		}
		j.Sweep(ctx)
		t := time.NewTicker(j.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				j.Sweep(ctx)
			}
		}
	}()
}

func (j *kubeJanitor) Sweep(ctx context.Context) {
	start := time.Now()
	if j.runs != nil {
		j.runs.Reconcile(ctx)
	}
	agents, err := j.rt.SweepExitedPods(ctx, j.agentExitedTTL, j.exitSettled)
	if err != nil {
		log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_exited_pods_failed", "err", err))
	}
	stranded, err := j.rt.SweepStrandedPods(ctx, strandedPodGrace)
	if err != nil {
		log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_exited_pods_failed", "err", err))
	}
	agents += stranded
	unlabeled, err := j.rt.SweepUnlabeledPods(ctx)
	if err != nil {
		log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_exited_pods_failed", "err", err))
	}
	agents += unlabeled
	sandboxes := 0
	if j.sandboxMgr != nil {
		sandboxes = j.sandboxMgr.SweepTerminated(ctx, j.sandboxExitedTTL)
	}
	claims, err := j.rt.SweepWorktreeClaims(ctx, j.claimReleasable)
	if err != nil {
		log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_claims_failed", "err", err))
	}
	log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_sweep_done",
		"elapsed", time.Since(start).Round(time.Second), "agents", agents, "sandboxes", sandboxes, "claims", claims))
}

// SweepLeftovers deletes what a run left that still runs or still holds its credentials: the pods
// of deleted tasks, the env Secrets of pods that are gone, and the pods no node ever took.
func (j *kubeJanitor) SweepLeftovers(ctx context.Context) {
	agents, err := j.rt.SweepOrphanAgentPods(ctx, deletedTaskAgentGrace, j.taskGone)
	if err != nil {
		log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_orphan_pods_failed", "err", err))
	}
	unscheduled, err := j.rt.SweepUnscheduledPods(ctx, unscheduledDefaultDeadline, unscheduledPodGrace, j.noteUnscheduled)
	if err != nil {
		log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_orphan_pods_failed", "err", err))
	}
	secrets, err := j.rt.SweepOrphanEnvSecrets(ctx, orphanEnvSecretAge)
	switch {
	case apierrors.IsForbidden(err):
		// A chart older than this daemon grants no list on Secrets; say it once, not every minute.
		j.secretsForbidden.Do(func() {
			log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_secrets_forbidden", "err", err))
		})
	case err != nil:
		log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_orphan_secrets_failed", "err", err))
	}
	if agents+unscheduled+secrets > 0 {
		log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_leftovers_done",
			"agents", agents, "unscheduled", unscheduled, "secrets", secrets))
	}
}

// taskGone holds only for a run of a task the database answers it does not have; any other error
// keeps the pod.
func (j *kubeJanitor) taskGone(ctx context.Context, pod kuberuntime.RunningPod) bool {
	taskID, ok := task.InstanceTaskID(pod.InstanceID)
	if !ok {
		return false
	}
	_, err := j.tasks.GetRow(ctx, taskID)
	return errors.Is(err, task.ErrNotFound)
}

func (j *kubeJanitor) noteUnscheduled(pod kuberuntime.RunningPod) {
	taskID, ok := task.InstanceTaskID(pod.InstanceID)
	if !ok {
		return
	}
	_, err := j.tasks.AddNote(context.Background(), taskID, "", task.SourceSystem, "octopus",
		task.NoteOf("daemon.comments.unscheduled_pod_deleted", "agent", pod.AgentType, "pod", pod.Name, "instance", pod.InstanceID))
	if err != nil && !errors.Is(err, task.ErrNotFound) {
		log.Printf("[kube-janitor] %s", i18n.Log("daemon.log.kube_janitor_note_failed", "task", taskID, "err", err))
	}
}

func (j *kubeJanitor) exitSettled(ctx context.Context, pod kuberuntime.ExitedPod) bool {
	if j.runs == nil {
		return true
	}
	return j.runs.ExitSettled(ctx, pod.InstanceID)
}

// An owner with no task row may be an automation sandbox ("auto:<run>") the automation engine
// releases itself.
// Its claim may go, but releasing its sandbox could kill an automation mid-run.
func (j *kubeJanitor) claimReleasable(ctx context.Context, ownerID string) (bool, error) {
	root, err := j.tasks.GetRow(ctx, ownerID)
	if errors.Is(err, task.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	finished, err := j.groupFinished(ctx, root)
	if err != nil || !finished {
		return false, err
	}
	// A release_resources stage still runs its release agent, and pulling the sandbox from under
	// it mid-run only makes its next call rebuild it. The next sweep looks again.
	if running, err := j.groupRunning(ctx, root); err != nil || running {
		return false, err
	}
	if j.sandboxMgr != nil {
		if err := j.sandboxMgr.Release(ctx, ownerID); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (j *kubeJanitor) groupRunning(ctx context.Context, root *task.Task) (bool, error) {
	if root.Status == task.StatusRunning {
		return true, nil
	}
	children, err := j.tasks.ListChildren(ctx, root.ID)
	if err != nil {
		return false, err
	}
	for i := range children {
		if children[i].Status == task.StatusRunning {
			return true, nil
		}
	}
	return false, nil
}

func (j *kubeJanitor) groupFinished(ctx context.Context, root *task.Task) (bool, error) {
	done, err := j.tasks.AllInGroupReleased(ctx, root.ID)
	if err != nil || done {
		return done, err
	}
	cutoff := time.Now().Add(-j.failedTaskSandboxTTL)
	parkedCutoff := time.Now().Add(-parkedClaimTTL)
	stale := func(t *task.Task) bool {
		if isParkedStatus(t.Status) {
			return t.UpdatedAt.Before(parkedCutoff)
		}
		return isJanitorReleasableStatus(t.Status) && t.UpdatedAt.Before(cutoff)
	}
	if !stale(root) {
		return false, nil
	}
	children, err := j.tasks.ListChildren(ctx, root.ID)
	if err != nil {
		return false, err
	}
	for i := range children {
		if stale(&children[i]) {
			continue
		}
		// A subtask has no children, so AllInGroupReleased on it asks whether it sits in a terminal
		// stage or one that frees the worktree (release_resources).
		if childDone, err := j.tasks.AllInGroupReleased(ctx, children[i].ID); err != nil || !childDone {
			return false, err
		}
	}
	return true, nil
}
