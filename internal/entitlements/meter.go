package entitlements

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/MisterDonnuts/thulu/internal/logx"
)

// PodSample is one pod as the runtime sees it at a tick.
type PodSample struct {
	// Unique per pod incarnation (namespace, name and start), so a pod recreated under the same name
	// starts its own count.
	Key         string
	WorkspaceID string
	// A sandbox reserves about half of what an agent does (0.5 CPU and 0.7 GiB against 1 CPU and
	// 1.7 GiB) and counts half an hour per hour.
	Sandbox bool
	// When the pod's first container started. Zero while the pod is still being scheduled or pulling
	// its image: that time is not billed.
	StartedAt time.Time
	// When its last container stopped. Zero while it runs.
	EndedAt time.Time
}

// PodSource lists the pods that count toward agent hours. The Kubernetes runtime implements it.
type PodSource interface {
	MeteredPods(ctx context.Context) ([]PodSample, error)
}

const (
	sandboxWeight = 0.5
	// Rows of pods that ended long ago only guard against counting them twice; a month and a bit is
	// plenty.
	podMeterRetention = 40 * 24 * time.Hour
	// DefaultMeterInterval bounds what a pod that ends between two ticks goes unbilled: the time after
	// the last tick it was seen running.
	DefaultMeterInterval = 30 * time.Second
)

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Meter counts the time each sampled pod ran since the last time it was counted. Calling it twice
// with the same samples counts nothing the second time.
func (s *Store) Meter(ctx context.Context, samples []PodSample, now time.Time) error {
	now = now.UTC()
	terms := map[string]monthTerms{}
	var firstErr error
	for i := range samples {
		p := samples[i]
		if p.Key == "" || p.WorkspaceID == "" || p.StartedAt.IsZero() {
			continue
		}
		t, ok := terms[p.WorkspaceID]
		if !ok {
			ent, err := s.Get(p.WorkspaceID)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			t = termsOf(ent)
			terms[p.WorkspaceID] = t
		}
		if err := s.meterPod(ctx, p, t, now); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// monthTerms is what a month's extra hours are billed by.
type monthTerms struct {
	includedHours int
	capCents      int
}

// termsOf an inactive workspace is "unknown" (-1): a run that was going when the subscription ended
// keeps counting, and must not overwrite the terms the month had while it was paid.
func termsOf(ent Entitlement) monthTerms {
	if ent.Plan == PlanInactive {
		return monthTerms{includedHours: -1, capCents: -1}
	}
	return monthTerms{includedHours: ent.HourQuotaMonthly, capCents: int(math.Round(ent.ExtraCap() * 100))}
}

func (s *Store) meterPod(ctx context.Context, p PodSample, terms monthTerms, now time.Time) error {
	end := now
	if !p.EndedAt.IsZero() && p.EndedAt.Before(end) {
		end = p.EndedAt.UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	from := p.StartedAt.UTC()
	var accounted time.Time
	err = tx.QueryRowContext(ctx, s.rb(
		`SELECT accounted_until FROM pod_meter WHERE pod_key = ? FOR UPDATE`), p.Key).Scan(&accounted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	case accounted.After(from):
		from = accounted.UTC()
	}
	if !end.After(from) {
		return nil
	}
	weight := 1.0
	if p.Sandbox {
		weight = sandboxWeight
	}
	for _, piece := range splitByMonth(from, end) {
		secs := int64(math.Round(piece.end.Sub(piece.start).Seconds() * weight))
		if err := s.addSeconds(ctx, tx, p.WorkspaceID, piece.start.Format("2006-01"), secs, terms); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.rb(
		`INSERT INTO pod_meter (pod_key, workspace_id, accounted_until) VALUES (?, ?, ?)
		 ON CONFLICT (pod_key) DO UPDATE SET accounted_until = excluded.accounted_until`),
		p.Key, p.WorkspaceID, end); err != nil {
		return err
	}
	return tx.Commit()
}

type span struct{ start, end time.Time }

// A run that goes past midnight on the last day counts in both months.
func splitByMonth(from, to time.Time) []span {
	var out []span
	for from.Before(to) {
		next := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
		if !next.Before(to) {
			out = append(out, span{from, to})
			break
		}
		out = append(out, span{from, next})
		from = next
	}
	return out
}

func (s *Store) addSeconds(ctx context.Context, db execer, workspaceID, period string, seconds int64, t monthTerms) error {
	if workspaceID == "" || seconds <= 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, s.rb(
		`INSERT INTO workspace_hour_usage (workspace_id, period, seconds, included_hours, cap_cents)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (workspace_id, period) DO UPDATE
		 SET seconds = workspace_hour_usage.seconds + excluded.seconds,
		     included_hours = CASE WHEN excluded.included_hours < 0
		                      THEN workspace_hour_usage.included_hours ELSE excluded.included_hours END,
		     cap_cents = CASE WHEN excluded.cap_cents < 0
		                 THEN workspace_hour_usage.cap_cents ELSE excluded.cap_cents END`),
		workspaceID, period, seconds, t.includedHours, t.capCents)
	return err
}

func (s *Store) prunePodMeter(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.rb(`DELETE FROM pod_meter WHERE accounted_until < ?`),
		now.Add(-podMeterRetention))
	return err
}

// RunMeter samples src every interval until ctx ends. Only the daemon that holds the singleton lease
// may run it, or two daemons would each count the same pods.
func (s *Store) RunMeter(ctx context.Context, src PodSource, interval time.Duration, report ExtraHoursReporter) {
	if interval <= 0 {
		interval = DefaultMeterInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	lastHousekeeping := time.Time{}
	for {
		now := s.now().UTC()
		samples, err := src.MeteredPods(ctx)
		logx.Warn(err, "entitlements.log.meter_list")
		if err == nil {
			logx.Warn(s.Meter(ctx, samples, now), "entitlements.log.meter_count")
		}
		if now.Sub(lastHousekeeping) >= time.Hour {
			lastHousekeeping = now
			logx.Warn(s.prunePodMeter(ctx, now), "entitlements.log.meter_prune")
			if report != nil {
				logx.Warn(s.ReportClosedMonths(ctx, report), "entitlements.log.meter_report")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
