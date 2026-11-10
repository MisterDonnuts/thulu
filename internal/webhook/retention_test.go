package webhook

import (
	"testing"
	"time"
)

func TestPruneBefore(t *testing.T) {
	s := newInternalStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -40)
	cutoff := now.AddDate(0, 0, -30)

	for _, d := range []struct {
		id string
		at time.Time
	}{{"d-old", old}, {"d-new", now}} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO webhook_deliveries
			(id, endpoint_id, event, payload, created_at) VALUES ($1, 'ep', 'task.created', '{}', $2)`,
			d.id, d.at); err != nil {
			t.Fatalf("insert delivery: %v", err)
		}
	}
	for _, o := range []struct {
		id, status string
		at         time.Time
	}{
		{"o-delivered-old", statusDelivered, old},
		{"o-failed-old", statusFailed, old},
		{"o-pending-old", statusPending, old},
		{"o-delivered-new", statusDelivered, now},
	} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO webhook_outbox
			(id, endpoint_id, event, payload, status, next_attempt_at, created_at, updated_at)
			VALUES ($1, 'ep', 'task.created', '{}', $2, $3, $3, $3)`, o.id, o.status, o.at); err != nil {
			t.Fatalf("insert outbox: %v", err)
		}
	}

	deliveries, outbox, err := s.PruneBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	if deliveries != 1 || outbox != 2 {
		t.Errorf("pruned deliveries=%d outbox=%d, want 1 and 2", deliveries, outbox)
	}
	var left []string
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM webhook_outbox ORDER BY id`)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(left) != 2 || left[0] != "o-delivered-new" || left[1] != "o-pending-old" {
		t.Errorf("outbox left = %v, want [o-delivered-new o-pending-old]", left)
	}
}
