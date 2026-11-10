package task

import (
	"context"
	"log"
	"time"

	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type RunQuota interface {
	// ok=false must count nothing: a refused run is never refunded.
	ConsumeRun(ctx context.Context, workspaceID string) (entitlements.RunUsage, bool, error)
	RefundRun(ctx context.Context, workspaceID, period string) error
}

// Stopping must make the launcher's OnExit callback fire as for any other exit.
type RunKiller interface {
	KillRun(ctx context.Context, runID string) error
}

func (d *Dispatcher) WithRunQuota(q RunQuota) *Dispatcher {
	d.quota = q
	return d
}

func (d *Dispatcher) WithRunDeadline(fn func(workspaceID string) time.Duration) *Dispatcher {
	d.runDeadline = fn
	return d
}

type quotaTicket struct {
	workspaceID string
	period      string
}

// A quota that cannot be read lets the run through: a database hiccup in a billing check must not
// stop every board.
func (d *Dispatcher) consumeRun(ctx context.Context, t *Task) (quotaTicket, entitlements.RunUsage, bool) {
	if d.quota == nil {
		return quotaTicket{}, entitlements.RunUsage{}, true
	}
	usage, ok, err := d.quota.ConsumeRun(ctx, t.WorkspaceID)
	if err != nil {
		logx.Warn(err, "task.log.run_quota_check", "task", t.ID)
		return quotaTicket{}, entitlements.RunUsage{}, true
	}
	if !ok {
		return quotaTicket{}, usage, false
	}
	return quotaTicket{workspaceID: t.WorkspaceID, period: usage.Period}, usage, true
}

func (d *Dispatcher) refund(ctx context.Context, taskID string, tk quotaTicket) {
	if d.quota == nil || tk.period == "" {
		return
	}
	logx.Warn(d.quota.RefundRun(ctx, tk.workspaceID, tk.period), "task.log.run_quota_refund", "task", taskID)
}

func quotaSpentArgs(u entitlements.RunUsage) []any {
	return []any{"limit", u.Limit, "period", u.Period, "resets", u.ResetsAt.Format("2006-01-02")}
}

// KillRun relies on the run's exit callback to record the end; that callback must call the returned
// stop so a run that ends in time disarms the timer.
func (d *Dispatcher) armDeadline(ctx context.Context, workspaceID, taskID, runID string, expired func(minutes int)) (stop func()) {
	if d.runDeadline == nil {
		return func() {}
	}
	limit := d.runDeadline(workspaceID)
	if limit <= 0 {
		return func() {}
	}
	minutes := int(limit.Round(time.Minute) / time.Minute)
	timer := time.AfterFunc(limit, func() {
		killer, ok := d.launcher.(RunKiller)
		if !ok {
			log.Printf("[task] %s", i18n.Log("task.log.run_deadline_no_killer", "run", runID, "task", taskID, "minutes", minutes))
			return
		}
		expired(minutes)
		logx.Warn(killer.KillRun(ctx, runID), "task.log.run_deadline_kill", "run", runID, "task", taskID)
	})
	return func() { timer.Stop() }
}
