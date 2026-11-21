package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStrftime(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	night := time.Date(2026, 11, 15, 23, 15, 0, 0, ny) // Sunday
	early := time.Date(2026, 11, 2, 3, 5, 9, 0, ny)    // Monday
	midnight := time.Date(2026, 12, 6, 0, 0, 0, 0, ny)
	noon := time.Date(2026, 12, 6, 12, 30, 0, 0, ny)
	cases := []struct {
		t    time.Time
		spec string
		want string
	}{
		{night, timeFormat12, "Sun Nov 15 11:15:00 PM 2026"},
		{night, timeFormat24, "Sun Nov 15 23:15:00 2026"},
		{early, timeFormat12, "Mon Nov  2 03:05:09 AM 2026"},
		{early, timeFormat24, "Mon Nov  2 03:05:09 2026"},
		{midnight, timeFormat12, "Sun Dec  6 12:00:00 AM 2026"},
		{noon, timeFormat12, "Sun Dec  6 12:30:00 PM 2026"},
		{early, "%l:%M%P", " 3:05am"},
		{early, "%k|%H", " 3|03"},
		{night, "%A, %B %d, %Y", "Sunday, November 15, 2026"},
		{night, "%F %T %Z %z", "2026-11-15 23:15:00 EST -0500"},
		{night, "%R | %r", "23:15 | 11:15:00 PM"},
		{night, "%d/%m/%y day %j", "15/11/26 day 319"},
		{night, "%FT%T%:z", "2026-11-15T23:15:00-05:00"},
		{night, "%D %-d/%-m %-I:%M", "11/15/26 15/11 11:15"},
		{early, "%-H:%-M:%-S", "3:5:9"},
		{night, "%s", strconv.FormatInt(night.Unix(), 10)},
		{night, "100%%", "100%"},
		{night, "no sections", "no sections"},
	}
	for _, c := range cases {
		got, err := strftime(c.t, c.spec)
		if err != nil || got != c.want {
			t.Errorf("strftime(%q) = %q, %v; want %q", c.spec, got, err, c.want)
		}
	}
}

func TestCheckTimeFormat(t *testing.T) {
	for _, ok := range []string{"12h", "24H", "12", "24", "%I:%M %p", "%a %b %e %Y"} {
		if err := checkTimeFormat(ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"%q", "%", "%I:%M %Q", "abc%"} {
		if err := checkTimeFormat(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestTimeFormatSetting(t *testing.T) {
	ts := time.Date(2026, 11, 15, 23, 15, 0, 0, time.UTC)
	t.Setenv("AT_TIME_FORMAT", "")
	if got := fmtWhen(ts); got != "Sun Nov 15 11:15:00 PM 2026" {
		t.Errorf("default must be 12-hour, got %q", got)
	}
	t.Setenv("AT_TIME_FORMAT", "24h")
	if got := fmtWhen(ts); got != "Sun Nov 15 23:15:00 2026" {
		t.Errorf("24h: %q", got)
	}
	t.Setenv("AT_TIME_FORMAT", "%I:%M %p")
	if got := fmtWhenZone(ts); got != "11:15 PM UTC" {
		t.Errorf("zone is appended when the format has none: %q", got)
	}
	t.Setenv("AT_TIME_FORMAT", "%I:%M %p %Z")
	if got := fmtWhenZone(ts); got != "11:15 PM UTC" || strings.Count(got, "UTC") != 1 {
		t.Errorf("zone must not be doubled: %q", got)
	}
}
