package domain

import (
	"strconv"
	"strings"
	"time"
)

// Rule computes the firings of a schedule: a 5-field cron expression or a cron macro in the task's IANA time zone, or
// a simple interval. Granularity is the minute; there is no seconds field.
type Rule interface {
	// Next returns the first firing strictly after the given instant.
	Next(after time.Time) time.Time
	String() string
}

// ParseSchedule builds the rule of a schedule. A schedule with no time zone uses the system's.
func ParseSchedule(s Schedule, system *time.Location) (Rule, error) {
	loc := system
	if s.Timezone != "" {
		l, err := time.LoadLocation(s.Timezone)
		if err != nil {
			return nil, TextErr("timezone_unknown", `unknown time zone "{{timezone}}"`, "timezone", s.Timezone)
		}
		loc = l
	}
	switch {
	case s.Cron != "" && s.Interval != "":
		return nil, TextErr("schedule_cron_and_interval", "a schedule is a cron expression or an interval, not both")
	case s.Cron != "":
		return ParseCron(s.Cron, loc)
	case s.Interval != "":
		return ParseInterval(s.Interval)
	default:
		return nil, TextErr("schedule_empty", "a schedule needs a cron expression or an interval")
	}
}

// IntervalRule fires every fixed duration after its previous firing.
type IntervalRule struct {
	Every time.Duration
	text  string
}

// ParseInterval reads an interval: a whole, positive number of minutes, hours, or days, such as 30m, 2h, or 1d.
func ParseInterval(text string) (*IntervalRule, error) {
	t := strings.TrimSpace(text)
	if len(t) < 2 {
		return nil, invalidInterval(text)
	}
	n, err := strconv.Atoi(t[:len(t)-1])
	if err != nil || n <= 0 {
		return nil, invalidInterval(text)
	}
	var unit time.Duration
	switch t[len(t)-1] {
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	default:
		return nil, TextErr("interval_unit_invalid", `invalid interval "{{interval}}": the unit is m, h, or d`, "interval", text)
	}
	return &IntervalRule{Every: time.Duration(n) * unit, text: t}, nil
}

func invalidInterval(text string) error {
	return TextErr("interval_invalid",
		`invalid interval "{{interval}}": use a whole number of minutes, hours, or days, such as 30m`, "interval", text)
}

// Next implements Rule: the instant, truncated to the minute, plus the interval.
func (r *IntervalRule) Next(after time.Time) time.Time {
	return after.Truncate(time.Minute).Add(r.Every)
}

// String implements Rule: the interval as the task wrote it, trimmed.
func (r *IntervalRule) String() string { return r.text }

// CronRule is a 5-field cron expression, or the one a cron macro stands for, evaluated in wall-clock time of its
// location.
type CronRule struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
	loc                           *time.Location
	text                          string
}

// cronMacros are the macros a schedule may use in place of 5 fields, spelled in lowercase as cron writes them; the
// task schema's pattern accepts the same spellings.
var cronMacros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8,
	"SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
var dayNames = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}

// ParseCron parses a 5-field expression or a lowercase cron macro, such as @daily; an expression with a seconds field
// is rejected, and so is a macro in any other case, such as @DAILY.
func ParseCron(expr string, loc *time.Location) (*CronRule, error) {
	text := strings.TrimSpace(expr)
	if m, ok := cronMacros[text]; ok {
		text = m
	}
	fields := strings.Fields(text)
	if len(fields) == 6 {
		return nil, TextErr("cron_seconds", `cron expression "{{cron}}" has 6 fields: seconds are not supported, use 5 fields`,
			"cron", expr)
	}
	if len(fields) != 5 {
		return nil, TextErr("cron_field_count",
			`cron expression "{{cron}}" must have 5 fields: minute hour day-of-month month day-of-week`, "cron", expr)
	}
	r := &CronRule{loc: loc, text: strings.TrimSpace(expr)}
	var err error
	if r.minute, err = parseField(fields[0], 0, 59, nil, "minute"); err != nil {
		return nil, err
	}
	if r.hour, err = parseField(fields[1], 0, 23, nil, "hour"); err != nil {
		return nil, err
	}
	if r.dom, err = parseField(fields[2], 1, 31, nil, "day_of_month"); err != nil {
		return nil, err
	}
	if r.month, err = parseField(fields[3], 1, 12, monthNames, "month"); err != nil {
		return nil, err
	}
	if r.dow, err = parseField(fields[4], 0, 7, dayNames, "day_of_week"); err != nil {
		return nil, err
	}
	if r.dow&(1<<7) != 0 { // 7 is Sunday too
		r.dow |= 1
	}
	r.domStar = fields[2] == "*" || strings.HasPrefix(fields[2], "*/")
	r.dowStar = fields[4] == "*" || strings.HasPrefix(fields[4], "*/")
	return r, nil
}

func parseField(f string, min, max int, names map[string]int, field string) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(f, ",") {
		step := 1
		rangePart := part
		if i := strings.IndexByte(part, '/'); i >= 0 {
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s <= 0 {
				return 0, TextErr("cron_step_invalid", `invalid step in {{fieldName}} field "{{value}}"`, fieldParams(field, f)...)
			}
			step = s
			rangePart = part[:i]
		}
		lo, hi := min, max
		switch {
		case rangePart == "*":
		case strings.Contains(rangePart, "-"):
			ends := strings.SplitN(rangePart, "-", 2)
			a, err := fieldValue(ends[0], names)
			if err != nil {
				return 0, TextErr("cron_value_invalid", `invalid {{fieldName}} field "{{value}}"`, fieldParams(field, f)...)
			}
			b, err := fieldValue(ends[1], names)
			if err != nil {
				return 0, TextErr("cron_value_invalid", `invalid {{fieldName}} field "{{value}}"`, fieldParams(field, f)...)
			}
			lo, hi = a, b
		default:
			v, err := fieldValue(rangePart, names)
			if err != nil {
				return 0, TextErr("cron_value_invalid", `invalid {{fieldName}} field "{{value}}"`, fieldParams(field, f)...)
			}
			lo = v
			if strings.Contains(part, "/") {
				hi = max
			} else {
				hi = v
			}
		}
		if lo < min || hi > max || lo > hi {
			return 0, TextErr("cron_value_out_of_range", `{{fieldName}} field "{{value}}" is out of range {{min}}-{{max}}`,
				append(fieldParams(field, f), "min", strconv.Itoa(min), "max", strconv.Itoa(max))...)
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

// fieldParams name a cron field for the desktop, which translates it, and in en-US for the daemon's own text.
func fieldParams(field, value string) []string {
	return []string{"field", field, "fieldName", strings.ReplaceAll(field, "_", " "), "value", value}
}

func fieldValue(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToUpper(s)]; ok {
			return v, nil
		}
	}
	return strconv.Atoi(s)
}

// String implements Rule: the cron expression as the task wrote it, trimmed.
func (r *CronRule) String() string { return r.text }

// Location is the time zone the rule is evaluated in.
func (r *CronRule) Location() *time.Location { return r.loc }

type civil struct {
	y        int
	mo       time.Month
	d, h, mi int
}

func civilOf(t time.Time) civil {
	y, mo, d := t.Date()
	return civil{y, mo, d, t.Hour(), t.Minute()}
}

func (c civil) less(o civil) bool {
	if c.y != o.y {
		return c.y < o.y
	}
	if c.mo != o.mo {
		return c.mo < o.mo
	}
	if c.d != o.d {
		return c.d < o.d
	}
	if c.h != o.h {
		return c.h < o.h
	}
	return c.mi < o.mi
}

// asUTC carries civil fields in a time.Time for calendar arithmetic, free of any zone transition.
func (c civil) asUTC() time.Time { return time.Date(c.y, c.mo, c.d, c.h, c.mi, 0, 0, time.UTC) }

func (r *CronRule) matchDay(t time.Time) bool {
	domOK := r.dom&(1<<uint(t.Day())) != 0
	dowOK := r.dow&(1<<uint(t.Weekday())) != 0
	if r.domStar || r.dowStar {
		return domOK && dowOK
	}
	return domOK || dowOK // both restricted: either matches, as in standard cron
}

// Next walks wall-clock minutes in the rule's location. A local time skipped by a daylight-saving change fires at
// the next valid time; a local time that occurs twice fires once, at its first occurrence.
func (r *CronRule) Next(after time.Time) time.Time {
	start := civilOf(after.In(r.loc)).asUTC().Add(time.Minute)
	c := start
	limit := start.AddDate(5, 0, 0)
	for c.Before(limit) {
		if r.month&(1<<uint(c.Month())) == 0 {
			c = time.Date(c.Year(), c.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !r.matchDay(c) {
			c = time.Date(c.Year(), c.Month(), c.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if r.hour&(1<<uint(c.Hour())) == 0 {
			c = time.Date(c.Year(), c.Month(), c.Day(), c.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if r.minute&(1<<uint(c.Minute())) == 0 {
			c = c.Add(time.Minute)
			continue
		}
		if t, ok := r.resolve(civilOf(c), after); ok {
			return t
		}
		c = c.Add(time.Minute)
	}
	return time.Time{}
}

// resolve turns a matching wall-clock time into the instant it fires at.
func (r *CronRule) resolve(want civil, after time.Time) (time.Time, bool) {
	t := time.Date(want.y, want.mo, want.d, want.h, want.mi, 0, 0, r.loc)
	if civilOf(t) != want {
		// The local time does not exist: fire at the first instant whose wall clock passes it.
		for x := t.Add(-3 * time.Hour); !x.After(t.Add(time.Hour)); x = x.Add(time.Minute) {
			if !civilOf(x).less(want) && x.After(after) {
				return x, true
			}
		}
		return time.Time{}, false
	}
	// The local time may occur twice; take the earlier instant.
	for _, back := range []time.Duration{time.Hour, 30 * time.Minute} {
		if e := t.Add(-back); civilOf(e) == want && e.After(after) {
			t = e
			break
		}
	}
	if !t.After(after) {
		return time.Time{}, false
	}
	return t, true
}

// Occurrences lists the next n firings after the given instant.
func Occurrences(r Rule, after time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	t := after
	for i := 0; i < n; i++ {
		t = r.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out
}
