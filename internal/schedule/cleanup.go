package schedule

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StartCleanup starts a background goroutine that runs CleanExpired on the
// schedule defined by cronExpr (standard 5-field crontab format).
// The goroutine exits when ctx is cancelled.
// Returns an error immediately if cronExpr is syntactically invalid.
func StartCleanup(ctx context.Context, store *Store, cronExpr string) error {
	next, err := parseCron(cronExpr)
	if err != nil {
		return fmt.Errorf("schedule cleanup: invalid cron expression %q: %w", cronExpr, err)
	}

	go func() {
		for {
			now := time.Now()
			n   := next(now)
			wait := n.Sub(now)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
				store.CleanExpired() //nolint:errcheck
			}
		}
	}()
	return nil
}

// RunCleanup runs one cleanup pass and returns the number of files removed.
// Called by `gmmff cleanup`.
func RunCleanup(cfg *Config) (int, error) {
	st, err := NewStore(cfg)
	if err != nil {
		return 0, err
	}
	return st.CleanExpired()
}

// ─────────────────────────────────────────────────────────────────────────────
// Minimal 5-field crontab parser
// Fields: minute  hour  dayOfMonth  month  dayOfWeek
// Supported syntax: * , - /
// ─────────────────────────────────────────────────────────────────────────────

type cronField struct {
	values []int
}

type cronSchedule struct {
	minute     cronField
	hour       cronField
	dayOfMonth cronField
	month      cronField
	dayOfWeek  cronField
}

// parseCron parses a 5-field crontab expression and returns a function
// that computes the next fire time after a given time.
func parseCron(expr string) (func(time.Time) time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}

	sched := &cronSchedule{}
	var err error
	if sched.minute, err     = parseField(fields[0], 0, 59);  err != nil { return nil, fmt.Errorf("minute: %w", err) }
	if sched.hour, err       = parseField(fields[1], 0, 23);  err != nil { return nil, fmt.Errorf("hour: %w", err) }
	if sched.dayOfMonth, err = parseField(fields[2], 1, 31);  err != nil { return nil, fmt.Errorf("dom: %w", err) }
	if sched.month, err      = parseField(fields[3], 1, 12);  err != nil { return nil, fmt.Errorf("month: %w", err) }
	if sched.dayOfWeek, err  = parseField(fields[4], 0, 6);   err != nil { return nil, fmt.Errorf("dow: %w", err) }

	return func(from time.Time) time.Time {
		return sched.next(from)
	}, nil
}

// next returns the next time after 'from' that matches the schedule.
func (s *cronSchedule) next(from time.Time) time.Time {
	// Advance by one minute to ensure we get a future time.
	t := from.Truncate(time.Minute).Add(time.Minute)

	// Search up to 4 years ahead to avoid infinite loop on impossible schedules.
	limit := t.Add(4 * 365 * 24 * time.Hour)
	for t.Before(limit) {
		if !contains(s.month.values, int(t.Month())) {
			t = nextMonth(t)
			continue
		}
		if !contains(s.dayOfMonth.values, t.Day()) || !contains(s.dayOfWeek.values, int(t.Weekday())) {
			t = t.AddDate(0, 0, 1).Truncate(24 * time.Hour)
			continue
		}
		if !contains(s.hour.values, t.Hour()) {
			t = t.Truncate(time.Hour).Add(time.Hour)
			continue
		}
		if !contains(s.minute.values, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return limit
}

func nextMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m+1, 1, 0, 0, 0, 0, t.Location())
}

func contains(vals []int, v int) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}

// parseField parses a single cron field into a sorted list of matching values.
func parseField(field string, min, max int) (cronField, error) {
	var values []int

	for _, part := range strings.Split(field, ",") {
		if strings.Contains(part, "/") {
			// Step: */5, 0-30/5
			sub := strings.SplitN(part, "/", 2)
			step, err := strconv.Atoi(sub[1])
			if err != nil || step <= 0 {
				return cronField{}, fmt.Errorf("invalid step in %q", part)
			}
			lo, hi := min, max
			if sub[0] != "*" {
				lo, hi, err = parseRange(sub[0], min, max)
				if err != nil {
					return cronField{}, err
				}
			}
			for v := lo; v <= hi; v += step {
				values = append(values, v)
			}
		} else if strings.Contains(part, "-") {
			// Range: 1-5
			lo, hi, err := parseRange(part, min, max)
			if err != nil {
				return cronField{}, err
			}
			for v := lo; v <= hi; v++ {
				values = append(values, v)
			}
		} else if part == "*" {
			for v := min; v <= max; v++ {
				values = append(values, v)
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil || n < min || n > max {
				return cronField{}, fmt.Errorf("value %q out of range [%d,%d]", part, min, max)
			}
			values = append(values, n)
		}
	}

	return cronField{values: dedupSorted(values)}, nil
}

func parseRange(s string, min, max int) (int, int, error) {
	parts := strings.SplitN(s, "-", 2)
	lo, err1 := strconv.Atoi(parts[0])
	hi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("invalid range %q", s)
	}
	if lo < min || hi > max || lo > hi {
		return 0, 0, fmt.Errorf("range %q out of bounds [%d,%d]", s, min, max)
	}
	return lo, hi, nil
}

func dedupSorted(vals []int) []int {
	if len(vals) == 0 {
		return vals
	}
	seen := make(map[int]bool, len(vals))
	out  := make([]int, 0, len(vals))
	for _, v := range vals {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	// Simple insertion sort — fields are small (max 60 elements).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
