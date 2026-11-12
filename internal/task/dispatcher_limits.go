package task

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// HourQuota decides whether a workspace may start one more agent run. It counts nothing: the hour
// meter counts pod time while pods run, so a failed launch needs no refund.
type HourQuota interface {
	AdmitRun(ctx context.Context, workspaceID string) (entitlements.Admission, error)
}

// Stopping must make the launcher's OnExit callback fire as for any other exit.
type RunKiller interface {
	KillRun(ctx context.Context, runID string) error
}

func (d *Dispatcher) WithHourQuota(q HourQuota) *Dispatcher {
	d.quota = q
	return d
}

func (d *Dispatcher) WithRunDeadline(fn func(workspaceID string) time.Duration) *Dispatcher {
	d.runDeadline = fn
	return d
}

// A quota that cannot be read lets the run through: a database hiccup in a billing check must not
// stop every board. The first launch past 80% or 100% of the included hours tells people on the task.
func (d *Dispatcher) admitRun(ctx context.Context, t *Task) (entitlements.Admission, bool) {
	if d.quota == nil {
		return entitlements.Admission{OK: true}, true
	}
	a, err := d.quota.AdmitRun(ctx, t.WorkspaceID)
	if err != nil {
		logx.Warn(err, "task.log.hour_quota_check", "task", t.ID)
		return entitlements.Admission{OK: true}, true
	}
	if a.Notice > 0 {
		d.systemComment(ctx, t.ID, hoursNotice(a))
	}
	return a, a.OK
}

func hoursNotice(a entitlements.Admission) Note {
	u := a.Usage
	args := []any{"included", u.IncludedHours, "period", u.Period, "used", u.UsedHours,
		"cap", fmtUSD(u.CapUSD), "resets", u.ResetsAt.Format("2006-01-02")}
	switch {
	case u.Trial:
		return NoteOf("task.comments.hours_notice_80_trial", args...)
	case a.Notice >= 100:
		return NoteOf("task.comments.hours_notice_100", args...)
	case u.CapUSD > 0:
		return NoteOf("task.comments.hours_notice_80", args...)
	default:
		return NoteOf("task.comments.hours_notice_80_no_extra", args...)
	}
}

// The keys are spelled out so the catalog guard can see every one of them.
func refusalNote(a entitlements.Admission) Note {
	key := "task.comments.hours_spent"
	switch a.Refusal {
	case entitlements.RefusedHours:
	case entitlements.RefusedCap:
		key = "task.comments.hours_cap_reached"
	case entitlements.RefusedTrialEnded:
		key = "task.comments.trial_ended"
	case entitlements.RefusedTrialHours:
		key = "task.comments.trial_hours_spent"
	case entitlements.RefusedPlanInactive:
		key = "task.comments.plan_inactive"
	}
	return NoteOf(key, refusalArgs(a)...)
}

func refusalErr(a entitlements.Admission) error {
	key := "task.errors.hours_spent"
	switch a.Refusal {
	case entitlements.RefusedHours:
	case entitlements.RefusedCap:
		key = "task.errors.hours_cap_reached"
	case entitlements.RefusedTrialEnded:
		key = "task.errors.trial_ended"
	case entitlements.RefusedTrialHours:
		key = "task.errors.trial_hours_spent"
	case entitlements.RefusedPlanInactive:
		key = "task.errors.plan_inactive"
	}
	return apperr.Precondition(key, refusalArgs(a)...)
}

func refusalArgs(a entitlements.Admission) []any {
	u := a.Usage
	return []any{"included", u.IncludedHours, "period", u.Period, "extra", u.ExtraHours,
		"cap", fmtUSD(u.CapUSD), "resets", u.ResetsAt.Format("2006-01-02"),
		"trial_end", u.TrialEndsAt.Format("2006-01-02")}
}

func fmtUSD(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

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
