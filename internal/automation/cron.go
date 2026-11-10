package automation

import (
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Schedule struct {
	expr string
	loc  *time.Location

	every time.Duration

	minute  uint64
	hour    uint64
	dom     uint64
	month   uint64
	dow     uint64
	domStar bool
	dowStar bool
}

func (s *Schedule) Location() *time.Location { return s.loc }

// Bounds Next so an unsatisfiable expression (Feb 30) errors instead of spinning; four years covers
// the leap-year cycle.
const maxSearchDays = 366 * 4

func ParseCron(expr, tz string) (*Schedule, error) {
	loc := time.UTC
	if strings.TrimSpace(tz) != "" {
		l, err := time.LoadLocation(strings.TrimSpace(tz))
		if err != nil {
			return nil, apperr.Invalid("automation.errors.cron_unknown_timezone", "tz", tz)
		}
		loc = l
	}
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return nil, apperr.Invalid("automation.errors.cron_empty")
	}
	s := &Schedule{expr: raw, loc: loc}

	if strings.HasPrefix(raw, "@") {
		shorthand, ok := expandShorthand(raw)
		if !ok {
			if d, err := parseEvery(raw); err == nil {
				s.every = d
				return s, nil
			}
			return nil, apperr.Invalid("automation.errors.cron_unknown_shorthand", "expr", raw)
		}
		raw = shorthand
	}

	fields := strings.Fields(raw)
	if len(fields) != 5 {
		return nil, apperr.Invalid("automation.errors.cron_field_count", "count", len(fields), "expr", expr)
	}

	var err error
	if s.minute, _, err = parseField(fields[0], 0, 59, nil); err != nil {
		return nil, invalidWrap(err, "automation.errors.cron_minute")
	}
	if s.hour, _, err = parseField(fields[1], 0, 23, nil); err != nil {
		return nil, invalidWrap(err, "automation.errors.cron_hour")
	}
	if s.dom, s.domStar, err = parseField(fields[2], 1, 31, nil); err != nil {
		return nil, invalidWrap(err, "automation.errors.cron_day_of_month")
	}
	if s.month, _, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return nil, invalidWrap(err, "automation.errors.cron_month")
	}
	if s.dow, s.dowStar, err = parseField(fields[4], 0, 7, dowNames); err != nil {
		return nil, invalidWrap(err, "automation.errors.cron_day_of_week")
	}
	if s.dow&(1<<7) != 0 {
		s.dow |= 1 << 0
		s.dow &^= 1 << 7
	}
	return s, nil
}

func expandShorthand(raw string) (string, bool) {
	switch raw {
	case "@hourly":
		return "0 * * * *", true
	case "@daily", "@midnight":
		return "0 0 * * *", true
	case "@weekly":
		return "0 0 * * 0", true
	case "@monthly":
		return "0 0 1 * *", true
	case "@yearly", "@annually":
		return "0 0 1 1 *", true
	}
	return "", false
}

// One-minute floor: the scheduler ticks every 10s and cannot honor shorter intervals.
func parseEvery(raw string) (time.Duration, error) {
	rest, ok := strings.CutPrefix(raw, "@every ")
	if !ok {
		return 0, apperr.Invalid("automation.errors.cron_not_every")
	}
	d, err := time.ParseDuration(strings.TrimSpace(rest))
	if err != nil {
		return 0, invalidWrap(err, "automation.errors.cron_every")
	}
	if d < time.Minute {
		return 0, apperr.Invalid("automation.errors.cron_every_too_short", "got", d)
	}
	return d, nil
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

func parseField(field string, minVal, maxVal int, names map[string]int) (mask uint64, star bool, err error) {
	if field == "*" {
		return fullMask(minVal, maxVal), true, nil
	}
	for _, part := range strings.Split(field, ",") {
		m, err := parseRange(strings.TrimSpace(part), minVal, maxVal, names)
		if err != nil {
			return 0, false, err
		}
		mask |= m
	}
	if mask == 0 {
		return 0, false, apperr.Invalid("automation.errors.cron_matches_nothing", "field", field)
	}
	return mask, false, nil
}

func fullMask(minVal, maxVal int) uint64 {
	var m uint64
	for i := minVal; i <= maxVal; i++ {
		m |= 1 << uint(i)
	}
	return m
}

func parseRange(part string, minVal, maxVal int, names map[string]int) (uint64, error) {
	if part == "" {
		return 0, apperr.Invalid("automation.errors.cron_empty_element")
	}
	step := 1
	if base, stepStr, ok := strings.Cut(part, "/"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(stepStr))
		if err != nil || n <= 0 {
			return 0, apperr.Invalid("automation.errors.cron_bad_step", "part", part)
		}
		step = n
		part = strings.TrimSpace(base)
	}

	lo, hi := minVal, maxVal
	switch {
	case part == "*":
	case strings.Contains(part, "-"):
		loStr, hiStr, _ := strings.Cut(part, "-")
		var err error
		if lo, err = parseValue(loStr, names); err != nil {
			return 0, err
		}
		if hi, err = parseValue(hiStr, names); err != nil {
			return 0, err
		}
	default:
		v, err := parseValue(part, names)
		if err != nil {
			return 0, err
		}
		lo, hi = v, v
		if step > 1 {
			// "5/15" means from 5 to the end of the field, every 15, matching Vixie cron.
			hi = maxVal
		}
	}
	if lo < minVal || hi > maxVal || lo > hi {
		return 0, apperr.Invalid("automation.errors.cron_out_of_range", "part", part, "min", minVal, "max", maxVal)
	}
	var mask uint64
	for v := lo; v <= hi; v += step {
		mask |= 1 << uint(v)
	}
	return mask, nil
}

func parseValue(s string, names map[string]int) (int, error) {
	s = strings.TrimSpace(s)
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, apperr.Invalid("automation.errors.cron_bad_value", "value", s)
	}
	return v, nil
}

// Every advance is computed from the current instant, never rebuilt from wall-clock fields: on DST
// fall-back time.Date resolves the repeated hour to its first occurrence and the walk loops
// forever.
// Spring forward: a match inside the skipped hour does not fire that day, as in cron and Kubernetes
// CronJob.
func (s *Schedule) Next(after time.Time) (time.Time, error) {
	if s.every > 0 {
		return after.Add(s.every), nil
	}
	t := after.In(s.loc).Truncate(time.Minute).Add(time.Minute)
	deadline := t.AddDate(0, 0, maxSearchDays)
	for t.Before(deadline) {
		switch {
		case !s.matchDay(t):
			t = nextMidnight(t, s.loc)
		case s.hour&(1<<uint(t.Hour())) == 0:
			t = t.Truncate(time.Hour).Add(time.Hour)
		case s.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t, nil
		}
	}
	return time.Time{}, apperr.Invalid("automation.errors.cron_no_firing", "expr", s.expr, "days", maxSearchDays)
}

// Guards zones that change clocks at midnight: if the constructed instant is not after t, step an
// hour so the walk cannot stall.
func nextMidnight(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.Date()
	next := time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	if !next.After(t) {
		return t.Add(time.Hour)
	}
	return next
}

// Vixie cron rule: with both day fields restricted, either one matching is enough.
func (s *Schedule) matchDay(t time.Time) bool {
	if s.month&(1<<uint(int(t.Month()))) == 0 {
		return false
	}
	domOK := s.dom&(1<<uint(t.Day())) != 0
	dowOK := s.dow&(1<<uint(int(t.Weekday()))) != 0
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowOK
	case s.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

func NextRuns(s *Schedule, n int) []string {
	out := make([]string, 0, n)
	t := time.Now()
	for i := 0; i < n; i++ {
		next, err := s.Next(t)
		if err != nil {
			return out
		}
		out = append(out, next.In(s.loc).Format(time.RFC3339))
		t = next
	}
	return out
}
