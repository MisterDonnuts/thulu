package entitlements

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// ExtraHoursReport is one closed month of extra hours for the billing provider to charge.
type ExtraHoursReport struct {
	WorkspaceID string
	Customer    string
	Period      string
	// Whole hours, rounded down and never past what the workspace's spending cap pays for.
	Hours int
	// The last second of the month, so the provider bills it in that month's invoice.
	At time.Time
	// Stable per workspace and month: sending the same report twice charges once.
	Identifier string
}

type ExtraHoursReporter interface {
	ReportExtraHours(ctx context.Context, r ExtraHoursReport) error
}

// ExtraHoursReporterFromEnv is set by ee/billing. It returns nil when no billing provider is
// configured, and then extra hours are only shown, never charged.
var ExtraHoursReporterFromEnv func(getenv func(string) string) ExtraHoursReporter

type closedMonth struct {
	workspaceID, period, customer string
	seconds                       int64
	terms                         monthTerms
}

// ReportClosedMonths sends the extra hours of every month that already ended and was not sent yet.
// A month with no extra hours is marked as sent without calling the provider.
func (s *Store) ReportClosedMonths(ctx context.Context, r ExtraHoursReporter) error {
	months, err := s.closedMonths(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, m := range months {
		if err := s.reportMonth(ctx, r, m); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Store) closedMonths(ctx context.Context) ([]closedMonth, error) {
	current, _ := s.period()
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT u.workspace_id, u.period, u.seconds, u.included_hours, u.cap_cents,
		        COALESCE(e.billing_customer, '')
		 FROM workspace_hour_usage u
		 LEFT JOIN workspace_entitlements e ON e.workspace_id = u.workspace_id
		 WHERE u.period < ? AND u.reported_at IS NULL`), current)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var months []closedMonth
	for rows.Next() {
		var m closedMonth
		if err := rows.Scan(&m.workspaceID, &m.period, &m.seconds, &m.terms.includedHours,
			&m.terms.capCents, &m.customer); err != nil {
			return nil, err
		}
		months = append(months, m)
	}
	return months, rows.Err()
}

func (s *Store) reportMonth(ctx context.Context, r ExtraHoursReporter, m closedMonth) error {
	terms := m.terms
	if terms.includedHours < 0 {
		// Counted before the terms were stored: the plan the workspace has now is the best guess.
		ent, err := s.Get(m.workspaceID)
		if err != nil {
			return err
		}
		terms = termsOf(ent)
	}
	if hours := billableExtraHours(terms, m.seconds); hours > 0 {
		if m.customer == "" {
			// Nobody to charge yet; try again next time instead of dropping the month.
			return nil
		}
		start, err := time.Parse("2006-01", m.period)
		if err != nil {
			return apperr.Wrap(err, "entitlements.errors.bad_period", "period", m.period)
		}
		if err := r.ReportExtraHours(ctx, ExtraHoursReport{
			WorkspaceID: m.workspaceID, Customer: m.customer, Period: m.period, Hours: hours,
			At:         start.AddDate(0, 1, 0).Add(-time.Second),
			Identifier: fmt.Sprintf("thulu-extra-hours-%s-%s", m.workspaceID, m.period),
		}); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE workspace_hour_usage SET reported_at = ? WHERE workspace_id = ? AND period = ?`),
		s.now().UTC(), m.workspaceID, m.period)
	return err
}

// The spending cap stops new runs; a run already going finishes past it, and that overrun is on us.
func billableExtraHours(t monthTerms, seconds int64) int {
	if t.includedHours <= 0 || t.capCents <= 0 {
		return 0
	}
	extra := float64(seconds)/3600 - float64(t.includedHours)
	if extra <= 0 {
		return 0
	}
	capHours := float64(t.capCents) / 100 / ExtraHourUSD
	return int(math.Floor(min(extra, capHours)))
}
