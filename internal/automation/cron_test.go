package automation

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, expr, tz string) *Schedule {
	t.Helper()
	s, err := ParseCron(expr, tz)
	if err != nil {
		t.Fatalf("ParseCron(%q, %q): %v", expr, tz, err)
	}
	return s
}

func next(t *testing.T, s *Schedule, from string) string {
	t.Helper()
	base, err := time.Parse(time.RFC3339, from)
	if err != nil {
		t.Fatalf("bad base time %q: %v", from, err)
	}
	n, err := s.Next(base)
	if err != nil {
		t.Fatalf("Next(%s): %v", from, err)
	}
	return n.Format(time.RFC3339)
}

func TestParseCron_Fields(t *testing.T) {
	cases := []struct {
		expr, tz, from, want string
	}{
		{"*/15 * * * *", "UTC", "2026-09-17T10:07:00Z", "2026-09-17T10:15:00Z"},
		{"0 * * * *", "UTC", "2026-09-17T10:00:00Z", "2026-09-17T11:00:00Z"},
		{"0 9 * * 1", "UTC", "2026-09-17T10:00:00Z", "2026-09-21T09:00:00Z"},
		{"30 2 1 * *", "UTC", "2026-09-17T10:00:00Z", "2026-10-01T02:30:00Z"},
		{"0 0 29 2 *", "UTC", "2026-09-17T10:00:00Z", "2028-02-29T00:00:00Z"},
		{"@hourly", "UTC", "2026-09-17T10:30:00Z", "2026-09-17T11:00:00Z"},
		{"@daily", "UTC", "2026-09-17T10:30:00Z", "2026-09-18T00:00:00Z"},
		{"@weekly", "UTC", "2026-09-17T10:30:00Z", "2026-09-20T00:00:00Z"},
		{"@monthly", "UTC", "2026-09-17T10:30:00Z", "2026-10-01T00:00:00Z"},
		{"5,20,50 * * * *", "UTC", "2026-09-17T10:21:00Z", "2026-09-17T10:50:00Z"},
		{"0 9-17/4 * * *", "UTC", "2026-09-17T10:00:00Z", "2026-09-17T13:00:00Z"},
		{"0 0 * * sun", "UTC", "2026-09-17T10:00:00Z", "2026-09-20T00:00:00Z"},
		{"0 0 * jan *", "UTC", "2026-09-17T10:00:00Z", "2027-01-01T00:00:00Z"},
		{"0 0 * * 7", "UTC", "2026-09-17T10:00:00Z", "2026-09-20T00:00:00Z"},
	}
	for _, c := range cases {
		s := mustParse(t, c.expr, c.tz)
		if got := next(t, s, c.from); got != c.want {
			t.Errorf("%q from %s = %s, want %s", c.expr, c.from, got, c.want)
		}
	}
}

func TestParseCron_DayOfMonthOrDayOfWeek(t *testing.T) {
	s := mustParse(t, "0 0 13 * fri", "UTC")
	if got := next(t, s, "2026-09-17T10:00:00Z"); got != "2026-09-18T00:00:00Z" {
		t.Errorf("dow match = %s", got)
	}
	if got := next(t, s, "2026-09-19T00:00:00Z"); got != "2026-09-25T00:00:00Z" {
		t.Errorf("next after saturday = %s", got)
	}
}

func TestParseCron_TimezoneAndDST(t *testing.T) {
	s := mustParse(t, "0 3 * * *", "America/New_York")
	got := next(t, s, "2026-10-31T20:00:00Z")
	if got != "2026-11-01T03:00:00-05:00" && got != "2026-11-01T03:00:00-04:00" {
		t.Errorf("post-DST firing = %s, want 03:00 local", got)
	}
	n, err := s.Next(time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if h := n.In(s.Location()).Hour(); h != 3 {
		t.Errorf("local hour = %d, want 3", h)
	}
}

func TestParseCron_Every(t *testing.T) {
	s := mustParse(t, "@every 90m", "UTC")
	if got := next(t, s, "2026-09-17T10:00:00Z"); got != "2026-09-17T11:30:00Z" {
		t.Errorf("@every 90m = %s", got)
	}
	if _, err := ParseCron("@every 5s", "UTC"); err == nil {
		t.Error("@every below the 1m floor was accepted")
	}
}

func TestParseCron_Rejects(t *testing.T) {
	bad := []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *",
		"* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *",
		"@nope", "abc * * * *", "5-1 * * * *",
	}
	for _, expr := range bad {
		if _, err := ParseCron(expr, "UTC"); err == nil {
			t.Errorf("ParseCron(%q) accepted an invalid expression", expr)
		}
	}
	if _, err := ParseCron("* * * * *", "Mars/Olympus"); err == nil {
		t.Error("unknown timezone accepted")
	}
}

func TestNext_IsStrictlyAfter(t *testing.T) {
	s := mustParse(t, "* * * * *", "UTC")
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	n, err := s.Next(base)
	if err != nil {
		t.Fatal(err)
	}
	if !n.After(base) {
		t.Fatalf("Next(%s) = %s, not strictly after", base, n)
	}
	if got := n.Sub(base); got != time.Minute {
		t.Errorf("gap = %s, want 1m", got)
	}
}

// Regression: rebuilding the instant from wall-clock fields on DST fall-back made Next loop forever
// at 100% CPU.
func TestNext_TerminatesAcrossDSTFallBack(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, expr := range []string{"0 1 * * *", "30 1 * * *", "0 3 * * *", "*/20 * * * *"} {
			s := mustParse(t, expr, "America/New_York")
			from := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
			for i := 0; i < 5; i++ {
				n, err := s.Next(from)
				if err != nil {
					t.Errorf("%s: %v", expr, err)
					return
				}
				if !n.After(from) {
					t.Errorf("%s: Next(%s) = %s went backwards", expr, from, n)
					return
				}
				from = n
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Next did not terminate across the DST fall-back")
	}
}

func TestNext_SpringForwardSkipsTheMissingHour(t *testing.T) {
	s := mustParse(t, "30 2 * * *", "America/New_York")
	from := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	first, err := s.Next(from)
	if err != nil {
		t.Fatal(err)
	}
	if got := first.In(s.Location()).Format("2006-01-02 15:04"); got != "2026-03-07 02:30" {
		t.Fatalf("first firing = %s", got)
	}
	second, err := s.Next(first)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.In(s.Location()).Format("2006-01-02 15:04"); got != "2026-03-09 02:30" {
		t.Errorf("firing after the skipped hour = %s, want 2026-03-09 02:30", got)
	}
}
