//go:build !foss

package daemon

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// A pod mounting a claim that is gone is stranded for good; the grace only covers a claim that is
// created right after its pod.
const strandedPodGrace = 10 * time.Minute

type kubeJanitor struct {
	rt         *kuberuntime.Runtime
	sandboxMgr *sandbox.Manager
	tasks      *task.Store
	runs       runReconciler

	interval             time.Duration
	agentExitedTTL       time.Duration
	sandboxExitedTTL     time.Duration
	failedTaskSandboxTTL time.Duration
}

type runReconciler interface {
	Reconcile(ctx context.Context)
	ExitSettled(ctx context.Context, instanceID string) bool
}

func newKubeJanitor(rt *kuberuntime.Runtime, s *sandbox.Manager, t *task.Store, runs runReconciler) *kubeJanitor {
	return &kubeJanitor{
		rt:                   rt,
		sandboxMgr:           s,
		tasks:                t,
		runs:                 runs,
		interval:             1 * time.Hour,
		agentExitedTTL:       2 * time.Hour,
		sandboxExitedTTL:     6 * time.Hour,
		failedTaskSandboxTTL: 6 * time.Hour,
	}
}

// The 2-minute warmup keeps the first sweep clear of the startup reconcile.
func (j *kubeJanitor) Start(ctx context.Context) {
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
	if j.sandboxMgr != nil {
		if err := j.sandboxMgr.Release(ctx, ownerID); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (j *kubeJanitor) groupFinished(ctx context.Context, root *task.Task) (bool, error) {
	done, err := j.tasks.AllInGroupDone(ctx, root.ID)
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
		// A subtask has no children, so AllInGroupDone on it asks whether it sits in a terminal
		// stage.
		if childDone, err := j.tasks.AllInGroupDone(ctx, children[i].ID); err != nil || !childDone {
			return false, err
		}
	}
	return true, nil
}
