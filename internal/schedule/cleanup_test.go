package schedule

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// parseCron — valid expressions
// ─────────────────────────────────────────────────────────────────────────────

// fixedTime builds a deterministic time in UTC for use as the "from" argument
// in next() calls. Using a fixed reference makes expected results predictable.
func fixedTime(year int, month time.Month, day, hour, min int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, time.UTC)
}

func TestParseCron_ValidExpressions(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"every_minute", "* * * * *"},
		{"every_5_minutes", "*/5 * * * *"},
		{"every_hour", "0 * * * *"},
		{"every_6_hours", "0 */6 * * *"},
		{"daily_midnight", "0 0 * * *"},
		{"weekly_sunday", "0 0 * * 0"},
		{"first_of_month", "0 0 1 * *"},
		{"specific_time", "30 14 * * *"},
		{"range_minutes", "0-5 * * * *"},
		{"list_hours", "0 0,6,12,18 * * *"},
		{"step_with_range", "0-30/5 * * * *"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			next, err := parseCron(tc.expr)
			if err != nil {
				t.Errorf("parseCron(%q): unexpected error: %v", tc.expr, err)
				return
			}
			if next == nil {
				t.Errorf("parseCron(%q): returned nil function", tc.expr)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parseCron — invalid expressions
// ─────────────────────────────────────────────────────────────────────────────

func TestParseCron_InvalidExpressions(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"empty", ""},
		{"too_few_fields", "* * * *"},
		{"too_many_fields", "* * * * * *"},
		{"minute_out_of_range", "60 * * * *"},
		{"hour_out_of_range", "0 24 * * *"},
		{"day_out_of_range", "0 0 32 * *"},
		{"month_out_of_range", "0 0 1 13 *"},
		{"dow_out_of_range", "0 0 * * 7"},
		{"invalid_step_zero", "*/0 * * * *"},
		{"invalid_range_inverted", "10-5 * * * *"},
		{"non_numeric", "abc * * * *"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCron(tc.expr)
			if err == nil {
				t.Errorf("parseCron(%q): expected error, got nil", tc.expr)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// next() — verify computed fire times
// ─────────────────────────────────────────────────────────────────────────────

func TestCronNext_EveryMinute(t *testing.T) {
	next, err := parseCron("* * * * *")
	if err != nil {
		t.Fatalf("parseCron: %v", err)
	}
	from := fixedTime(2026, time.January, 15, 10, 30)
	got := next(from)
	want := fixedTime(2026, time.January, 15, 10, 31)
	if !got.Equal(want) {
		t.Errorf("next = %v, want %v", got, want)
	}
}

func TestCronNext_Every5Minutes(t *testing.T) {
	next, err := parseCron("*/5 * * * *")
	if err != nil {
		t.Fatalf("parseCron: %v", err)
	}

	cases := []struct {
		from time.Time
		want time.Time
	}{
		// Exactly on the hour — next is 5 minutes later.
		{
			fixedTime(2026, time.January, 15, 10, 0),
			fixedTime(2026, time.January, 15, 10, 5),
		},
		// At :03 — next is :05.
		{
			fixedTime(2026, time.January, 15, 10, 3),
			fixedTime(2026, time.January, 15, 10, 5),
		},
		// At :55 — next is :00 of the next hour.
		{
			fixedTime(2026, time.January, 15, 10, 55),
			fixedTime(2026, time.January, 15, 11, 0),
		},
	}
	for _, tc := range cases {
		got := next(tc.from)
		if !got.Equal(tc.want) {
			t.Errorf("from %v: next = %v, want %v", tc.from, got, tc.want)
		}
	}
}

func TestCronNext_DailyMidnight(t *testing.T) {
	next, err := parseCron("0 0 * * *")
	if err != nil {
		t.Fatalf("parseCron: %v", err)
	}
	from := fixedTime(2026, time.January, 15, 10, 30)
	got := next(from)
	want := fixedTime(2026, time.January, 16, 0, 0)
	if !got.Equal(want) {
		t.Errorf("next = %v, want %v", got, want)
	}
}

func TestCronNext_HourRollover(t *testing.T) {
	// "0 * * * *" fires at the top of each hour.
	next, err := parseCron("0 * * * *")
	if err != nil {
		t.Fatalf("parseCron: %v", err)
	}
	from := fixedTime(2026, time.January, 15, 23, 30)
	got := next(from)
	want := fixedTime(2026, time.January, 16, 0, 0)
	if !got.Equal(want) {
		t.Errorf("next = %v, want %v", got, want)
	}
}

func TestCronNext_MonthRollover(t *testing.T) {
	// "0 0 1 * *" fires at midnight on the 1st of each month.
	next, err := parseCron("0 0 1 * *")
	if err != nil {
		t.Fatalf("parseCron: %v", err)
	}
	from := fixedTime(2026, time.January, 15, 0, 0)
	got := next(from)
	want := fixedTime(2026, time.February, 1, 0, 0)
	if !got.Equal(want) {
		t.Errorf("next = %v, want %v", got, want)
	}
}

func TestCronNext_AlwaysInFuture(t *testing.T) {
	// For any valid expression, next() must always return a time strictly
	// after the input — we verify this for a range of "from" values.
	exprs := []string{
		"* * * * *",
		"*/5 * * * *",
		"0 */6 * * *",
		"0 0 1 * *",
		"30 14 * * *",
	}
	from := fixedTime(2026, time.June, 1, 12, 0)
	for _, expr := range exprs {
		next, err := parseCron(expr)
		if err != nil {
			t.Fatalf("parseCron(%q): %v", expr, err)
		}
		got := next(from)
		if !got.After(from) {
			t.Errorf("parseCron(%q): next(%v) = %v is not after from", expr, from, got)
		}
	}
}

func TestCronNext_SpecificDayOfWeek(t *testing.T) {
	// "0 9 * * 1" fires at 09:00 every Monday.
	next, err := parseCron("0 9 * * 1")
	if err != nil {
		t.Fatalf("parseCron: %v", err)
	}
	// 2026-01-15 is a Thursday.
	from := fixedTime(2026, time.January, 15, 12, 0)
	got := next(from)
	// Next Monday is 2026-01-19.
	want := fixedTime(2026, time.January, 19, 9, 0)
	if !got.Equal(want) {
		t.Errorf("next = %v, want %v (next Monday)", got, want)
		t.Logf("got weekday: %v", got.Weekday())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parseField
// ─────────────────────────────────────────────────────────────────────────────

func TestParseField_Wildcard(t *testing.T) {
	f, err := parseField("*", 0, 5)
	if err != nil {
		t.Fatalf("parseField: %v", err)
	}
	if len(f.values) != 6 { // 0,1,2,3,4,5
		t.Errorf("got %d values, want 6", len(f.values))
	}
	if f.values[0] != 0 || f.values[5] != 5 {
		t.Errorf("values: got %v, want [0 1 2 3 4 5]", f.values)
	}
}

func TestParseField_SingleValue(t *testing.T) {
	f, err := parseField("3", 0, 59)
	if err != nil {
		t.Fatalf("parseField: %v", err)
	}
	if len(f.values) != 1 || f.values[0] != 3 {
		t.Errorf("got %v, want [3]", f.values)
	}
}

func TestParseField_Range(t *testing.T) {
	f, err := parseField("2-5", 0, 59)
	if err != nil {
		t.Fatalf("parseField: %v", err)
	}
	want := []int{2, 3, 4, 5}
	if len(f.values) != len(want) {
		t.Fatalf("got %v, want %v", f.values, want)
	}
	for i, v := range want {
		if f.values[i] != v {
			t.Errorf("values[%d]: got %d, want %d", i, f.values[i], v)
		}
	}
}

func TestParseField_Step(t *testing.T) {
	f, err := parseField("*/15", 0, 59)
	if err != nil {
		t.Fatalf("parseField: %v", err)
	}
	want := []int{0, 15, 30, 45}
	if len(f.values) != len(want) {
		t.Fatalf("got %v, want %v", f.values, want)
	}
	for i, v := range want {
		if f.values[i] != v {
			t.Errorf("values[%d]: got %d, want %d", i, f.values[i], v)
		}
	}
}

func TestParseField_List(t *testing.T) {
	f, err := parseField("0,15,30,45", 0, 59)
	if err != nil {
		t.Fatalf("parseField: %v", err)
	}
	want := []int{0, 15, 30, 45}
	if len(f.values) != len(want) {
		t.Fatalf("got %v, want %v", f.values, want)
	}
	for i, v := range want {
		if f.values[i] != v {
			t.Errorf("values[%d]: got %d, want %d", i, f.values[i], v)
		}
	}
}

func TestParseField_DeduplicatesAndSorts(t *testing.T) {
	// "5,3,5,1,3" should produce [1, 3, 5].
	f, err := parseField("5,3,5,1,3", 0, 59)
	if err != nil {
		t.Fatalf("parseField: %v", err)
	}
	want := []int{1, 3, 5}
	if len(f.values) != len(want) {
		t.Fatalf("got %v, want %v", f.values, want)
	}
	for i, v := range want {
		if f.values[i] != v {
			t.Errorf("values[%d]: got %d, want %d", i, f.values[i], v)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// dedupSorted
// ─────────────────────────────────────────────────────────────────────────────

func TestDedupSorted(t *testing.T) {
	cases := []struct {
		input []int
		want  []int
	}{
		{nil, nil},
		{[]int{}, []int{}},
		{[]int{3, 1, 2}, []int{1, 2, 3}},
		{[]int{5, 5, 3, 3, 1}, []int{1, 3, 5}},
		{[]int{1}, []int{1}},
		{[]int{2, 1}, []int{1, 2}},
	}
	for _, tc := range cases {
		got := dedupSorted(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("dedupSorted(%v): got %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("dedupSorted(%v)[%d]: got %d, want %d", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}
