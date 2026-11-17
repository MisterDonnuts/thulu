package entitlements

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// HourUsage is a workspace's agent time in the current calendar month (UTC).
type HourUsage struct {
	Period string `json:"period"`
	// Agent hours used, rounded to two decimals.
	UsedHours float64 `json:"used_hours"`
	// 0 = no cap.
	IncludedHours int     `json:"included_hours"`
	ExtraHours    float64 `json:"extra_hours"`
	// What the extra hours cost so far, never more than the cap.
	ExtraUSD float64 `json:"extra_usd"`
	CapUSD   float64 `json:"cap_usd"`
	// Share of the included hours used, 0–1+. 0 when there is no cap.
	Ratio       float64   `json:"ratio"`
	ResetsAt    time.Time `json:"resets_at"`
	TrialEndsAt time.Time `json:"trial_ends_at,omitzero"`
	// The hours are the trial's: they cover the 30 days and never renew.
	Trial bool `json:"trial,omitempty"`
}

// Why a launch was refused, or "" when it was admitted.
type Refusal string

const (
	RefusedHours        Refusal = "hours"         // included hours spent and no extra hours allowed
	RefusedCap          Refusal = "cap"           // extra hours reached the workspace's spending cap
	RefusedTrialEnded   Refusal = "trial_ended"   // the 30 days are over
	RefusedTrialHours   Refusal = "trial_hours"   // the trial's hours are spent; they never renew
	RefusedPlanInactive Refusal = "plan_inactive" // the subscription ended or stopped paying
)

// Admission is the answer to "may this workspace start one more agent?". Notice is 80 or 100 the
// first time this month the usage crossed that share of the included hours, so the caller tells
// people once.
type Admission struct {
	OK      bool
	Refusal Refusal
	Notice  int
	Usage   HourUsage
}

// period is the current calendar month (UTC) and the instant the next one starts.
func (s *Store) period() (string, time.Time) {
	now := s.now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start.Format("2006-01"), start.AddDate(0, 1, 0)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func usageOf(ent Entitlement, period string, resets time.Time, seconds int64) HourUsage {
	if ent.Plan.isTrial() && !ent.TrialEndsAt.IsZero() {
		resets = ent.TrialEndsAt // the trial's hours never renew; they end with it
	}
	used := float64(seconds) / 3600
	u := HourUsage{Period: period, UsedHours: round2(used), IncludedHours: ent.HourQuotaMonthly,
		CapUSD: ent.ExtraCap(), ResetsAt: resets, TrialEndsAt: ent.TrialEndsAt, Trial: ent.Plan.isTrial()}
	if ent.HourQuotaMonthly > 0 {
		u.Ratio = used / float64(ent.HourQuotaMonthly)
		extra := max(used-float64(ent.HourQuotaMonthly), 0)
		u.ExtraHours = round2(extra)
		u.ExtraUSD = round2(min(extra*ExtraHourUSD, u.CapUSD))
	}
	return u
}

// secondsUsed is the time that counts against the plan's hours, and the highest notice already
// posted for it. A paid plan counts the current month. The trial counts every month since it started:
// its hours are for the 30 days and do not renew on the 1st.
func (s *Store) secondsUsed(ctx context.Context, ent Entitlement, period string) (int64, int, error) {
	var (
		seconds int64
		warned  int
	)
	if ent.Plan.isTrial() && !ent.TrialEndsAt.IsZero() {
		since := ent.TrialEndsAt.UTC().AddDate(0, 0, -TrialDays).Format("2006-01")
		err := s.db.QueryRowContext(ctx, s.rb(
			`SELECT COALESCE(SUM(seconds), 0), COALESCE(MAX(warned_pct), 0) FROM workspace_hour_usage
			 WHERE workspace_id = ? AND period >= ?`), ent.WorkspaceID, since).Scan(&seconds, &warned)
		return seconds, warned, err
	}
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT seconds, warned_pct FROM workspace_hour_usage WHERE workspace_id = ? AND period = ?`),
		ent.WorkspaceID, period).Scan(&seconds, &warned)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return seconds, warned, err
}

func (s *Store) HourUsage(ctx context.Context, workspaceID string) (HourUsage, error) {
	ent, err := s.Get(workspaceID)
	if err != nil {
		return HourUsage{}, err
	}
	period, resets := s.period()
	seconds, _, err := s.secondsUsed(ctx, ent, period)
	if err != nil {
		return HourUsage{}, err
	}
	return usageOf(ent, period, resets, seconds), nil
}

// AdmitRun decides whether the workspace may start one more agent. It counts nothing: the meter
// counts pod time while pods run. A run that is already going when the hours run out finishes and its
// time counts as extra hours.
func (s *Store) AdmitRun(ctx context.Context, workspaceID string) (Admission, error) {
	if workspaceID == "" {
		return Admission{OK: true}, nil
	}
	ent, err := s.Get(workspaceID)
	if err != nil {
		return Admission{}, err
	}
	if refusal, gateErr := s.planGate(ctx, &ent); gateErr != nil || refusal != "" {
		return Admission{Refusal: refusal, Usage: HourUsage{TrialEndsAt: ent.TrialEndsAt}}, gateErr
	}
	if ent.HourQuotaMonthly <= 0 {
		return Admission{OK: true}, nil
	}
	period, resets := s.period()
	seconds, warned, err := s.secondsUsed(ctx, ent, period)
	if err != nil {
		return Admission{}, err
	}
	u := usageOf(ent, period, resets, seconds)
	if refusal := hoursRefusal(ent, seconds); refusal != "" {
		return Admission{Refusal: refusal, Usage: u}, nil
	}
	return Admission{OK: true, Usage: u, Notice: s.crossedNotice(ctx, workspaceID, period, u.Ratio, warned)}, nil
}

// planGate refuses an inactive plan and an expired trial, and starts the trial on its first run.
func (s *Store) planGate(ctx context.Context, ent *Entitlement) (Refusal, error) {
	if ent.Plan == PlanInactive {
		return RefusedPlanInactive, nil
	}
	if !ent.Plan.isTrial() {
		return "", nil
	}
	if ent.TrialEndsAt.IsZero() {
		// The trial starts with the workspace's first run, so a workspace nobody used keeps its 30 days.
		ent.TrialEndsAt = s.now().UTC().AddDate(0, 0, TrialDays)
		if err := s.startTrial(ctx, *ent); err != nil {
			return "", err
		}
	}
	if !s.now().Before(ent.TrialEndsAt) {
		return RefusedTrialEnded, nil
	}
	return "", nil
}

func hoursRefusal(ent Entitlement, seconds int64) Refusal {
	extra := float64(seconds)/3600 - float64(ent.HourQuotaMonthly)
	if extra < 0 {
		return ""
	}
	capUSD := ent.ExtraCap()
	switch {
	case ent.Plan.isTrial():
		return RefusedTrialHours
	case capUSD <= 0:
		return RefusedHours
	case extra*ExtraHourUSD >= capUSD:
		return RefusedCap
	}
	return ""
}

// crossedNotice is 100 or 80 the first time this month usage reaches that share, else 0.
func (s *Store) crossedNotice(ctx context.Context, workspaceID, period string, ratio float64, warned int) int {
	for _, pct := range []int{100, 80} {
		if ratio*100 < float64(pct) || warned >= pct {
			continue
		}
		if first, err := s.markWarned(ctx, workspaceID, period, pct); err == nil && first {
			return pct
		}
		return 0
	}
	return 0
}

// Persisted before the run starts so a restart in the middle cannot hand out a second trial.
func (s *Store) startTrial(ctx context.Context, ent Entitlement) error {
	stored, err := s.hasRow(ctx, ent.WorkspaceID)
	if err != nil {
		return err
	}
	if !stored {
		return s.Set(ent)
	}
	_, err = s.db.ExecContext(ctx, s.rb(
		`UPDATE workspace_entitlements SET trial_ends_at = ? WHERE workspace_id = ? AND trial_ends_at IS NULL`),
		ent.TrialEndsAt, ent.WorkspaceID)
	return err
}

func (s *Store) hasRow(ctx context.Context, workspaceID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT 1 FROM workspace_entitlements WHERE workspace_id = ?`), workspaceID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// One statement so two launches crossing the line together post the notice once.
func (s *Store) markWarned(ctx context.Context, workspaceID, period string, pct int) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO workspace_hour_usage (workspace_id, period, warned_pct) VALUES (?, ?, ?)
		 ON CONFLICT (workspace_id, period) DO UPDATE SET warned_pct = excluded.warned_pct
		 WHERE workspace_hour_usage.warned_pct < excluded.warned_pct`),
		workspaceID, period, pct)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// AddSeconds counts pod time for a workspace in a period. The meter does this through Meter; tests and
// imports call it directly.
func (s *Store) AddSeconds(ctx context.Context, workspaceID, period string, seconds int64) error {
	ent, err := s.Get(workspaceID)
	if err != nil {
		return err
	}
	return s.addSeconds(ctx, s.db, workspaceID, period, seconds, termsOf(ent))
}

// Err explains a refused admission for callers outside the task board (chats, Evidence batches).
// nil when the run was admitted.
func (a Admission) Err() error {
	if a.OK {
		return nil
	}
	u := a.Usage
	key := "entitlements.errors.refused_hours"
	switch a.Refusal {
	case RefusedHours:
	case RefusedCap:
		key = "entitlements.errors.refused_cap"
	case RefusedTrialEnded:
		key = "entitlements.errors.refused_trial_ended"
	case RefusedTrialHours:
		key = "entitlements.errors.refused_trial_hours"
	case RefusedPlanInactive:
		key = "entitlements.errors.refused_plan_inactive"
	}
	return apperr.Precondition(key,
		"included", u.IncludedHours, "period", u.Period, "cap", strconv.FormatFloat(u.CapUSD, 'f', -1, 64),
		"resets", u.ResetsAt.Format("2006-01-02"), "trial_end", u.TrialEndsAt.Format("2006-01-02"))
}
