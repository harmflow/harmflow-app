package domain

import (
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// A 5-field cron and a 30-minute interval are accepted; a 6-field cron with seconds is rejected.
func TestScheduleFormats(t *testing.T) {
	if _, err := ParseSchedule(Schedule{Cron: "0 9 * * 1-5"}, time.UTC); err != nil {
		t.Fatalf("5-field cron rejected: %v", err)
	}
	if _, err := ParseSchedule(Schedule{Interval: "30m"}, time.UTC); err != nil {
		t.Fatalf("interval rejected: %v", err)
	}
	_, err := ParseSchedule(Schedule{Cron: "0 0 9 * * 1-5"}, time.UTC)
	if err == nil || !strings.Contains(err.Error(), "seconds") {
		t.Fatalf("6-field cron: got %v, want a seconds error", err)
	}
	for _, bad := range []string{"60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "a b c d e"} {
		if _, err := ParseCron(bad, time.UTC); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParseSchedule(Schedule{Cron: "0 9 * * *", Timezone: "Mars/Olympus"}, time.UTC); err == nil {
		t.Error("unknown time zone accepted")
	}
}

// The task's time zone governs; no time zone means the system's.
func TestTimeZoneGoverns(t *testing.T) {
	sp := mustLoc(t, "America/Sao_Paulo")
	withTZ, _ := ParseSchedule(Schedule{Cron: "0 9 * * *", Timezone: "America/Sao_Paulo"}, time.UTC)
	noTZ, _ := ParseSchedule(Schedule{Cron: "0 9 * * *"}, time.UTC)
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	got := withTZ.Next(start)
	if want := time.Date(2026, 10, 20, 9, 0, 0, 0, sp); !got.Equal(want) {
		t.Errorf("São Paulo: got %v, want %v", got, want)
	}
	if got := noTZ.Next(start); !got.Equal(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("system zone: got %v", got)
	}
}

// A skipped local time fires once at the next valid time; a repeated one fires once, at its first occurrence.
func TestDaylightSaving(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2026-03-08: clocks jump from 02:00 to 03:00 in New York.
	spring, _ := ParseCron("30 2 * * *", ny)
	got := spring.Next(time.Date(2026, 3, 8, 0, 0, 0, 0, ny))
	if want := time.Date(2026, 3, 8, 3, 0, 0, 0, ny); !got.Equal(want) {
		t.Errorf("spring forward: got %v, want %v", got.In(ny), want)
	}
	if again := spring.Next(got); again.In(ny).Day() != 9 || again.In(ny).Hour() != 2 {
		t.Errorf("spring forward fired twice: next is %v", again.In(ny))
	}
	// 2026-11-01: clocks fall back from 02:00 to 01:00 in New York; 01:30 happens twice.
	fall, _ := ParseCron("30 1 * * *", ny)
	first := fall.Next(time.Date(2026, 11, 1, 0, 0, 0, 0, ny))
	if _, off := first.Zone(); off != -4*3600 {
		t.Errorf("fall back: fired at %v (offset %d), want the first 01:30 (EDT)", first, off)
	}
	second := fall.Next(first)
	if second.In(ny).Day() != 2 {
		t.Errorf("fall back fired twice: next is %v", second.In(ny))
	}
}

func TestCronFields(t *testing.T) {
	r, _ := ParseCron("*/15 9-10 * * MON-FRI", time.UTC)
	got := Occurrences(r, time.Date(2026, 10, 23, 10, 40, 0, 0, time.UTC), 3) // Friday
	want := []time.Time{
		time.Date(2026, 10, 23, 10, 45, 0, 0, time.UTC),
		time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC), // Monday
		time.Date(2026, 10, 26, 9, 15, 0, 0, time.UTC),
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("occurrence %d: got %v, want %v", i, got[i], want[i])
		}
	}
	// Day of month and day of week both restricted: either matches.
	r2, _ := ParseCron("0 0 1 * SUN", time.UTC)
	next := r2.Next(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	if !next.Equal(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("dom or dow: got %v", next)
	}
	if m, _ := ParseCron("@daily", time.UTC); m.Next(time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)).Day() != 2 {
		t.Error("@daily")
	}
}

func TestInterval(t *testing.T) {
	r, err := ParseInterval("2h")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 10, 0, 30, 0, time.UTC)
	if got := r.Next(from); !got.Equal(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("got %v", got)
	}
	for _, bad := range []string{"0m", "30s", "m", "1.5h", "-1h"} {
		if _, err := ParseInterval(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
