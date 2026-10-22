package main

import (
	"testing"
	"time"
)

func TestParseSpec(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 17, 50, 0, 0, ny) // Wednesday

	cases := []struct {
		spec string
		want string // 2006-01-02 15:04:05 in ny, unless zone given
	}{
		{"now", "2026-10-21 17:50:00"},
		{"now + 2 hours 55 minutes", "2026-10-21 20:45:00"},
		{"+2h55m", "2026-10-21 20:45:00"},
		{"+ 2 hours and 55 minutes", "2026-10-21 20:45:00"},
		{"in 3 days", "2026-10-24 17:50:00"},
		{"now + 1 hour 30 min 15 sec", "2026-10-21 19:20:15"},
		{"now + 2 hours - 5 minutes", "2026-10-21 19:45:00"},
		{"now + 90 seconds", "2026-10-21 17:51:30"},
		{"now + 2:55", "2026-10-21 20:45:00"},
		{"next week", "2026-10-28 17:50:00"},
		{"11-25-2026 09:30 + 2 hours 55 minutes", "2026-11-25 12:25:00"},
		{"11-25-2026 09:30 ET + 2 hours 55 minutes", "2026-11-25 12:25:00"},
		{"2026-11-25 09:30 + 2h 55m", "2026-11-25 12:25:00"},
		{"sunday 09:30 + 2 hours 55 minutes", "2026-10-25 12:25:00"},
		{"Sun 11-29-2026 9:30 AM + 2 hours 55 minutes", "2026-11-29 12:25:00"},
		{"09:30:15 tomorrow", "2026-10-22 09:30:15"},
		{"9:30 pm", "2026-10-21 21:30:00"},
		{"9am", "2026-10-22 09:00:00"},
		{"noon tomorrow", "2026-10-22 12:00:00"},
		{"teatime", "2026-10-22 16:00:00"},
		{"1730", "2026-10-22 17:30:00"},
		{"09:30 + 2 hours", "2026-10-22 11:30:00"},
		{"20:00 + 2 hours", "2026-10-21 22:00:00"},
		{"friday", "2026-10-23 17:50:00"},
		{"wednesday 9am", "2026-10-28 09:00:00"},
		{"next wednesday 9am", "2026-10-28 09:00:00"},
		{"nov 25 2026 9:30", "2026-11-25 09:30:00"},
		{"25 nov 2026 09:30", "2026-11-25 09:30:00"},
		{"nov 25, 2026 9:30am", "2026-11-25 09:30:00"},
		{"jan 5 8am", "2027-01-05 08:00:00"},
		{"25.11.2026 09:30", "2026-11-25 09:30:00"},
		{"11/25/2026 09:30", "2026-11-25 09:30:00"},
		{"12am tomorrow", "2026-10-22 00:00:00"},
		{"12pm tomorrow", "2026-10-22 12:00:00"},
		{"Pacific/Auckland 9am tomorrow", "2026-10-22 16:00:00"}, // already Oct 22 in NZ, so 9am Oct 23 NZDT
	}
	for _, c := range cases {
		got, err := ParseSpec(c.spec, now, ny)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.spec, err)
			continue
		}
		if s := got.In(ny).Format("2006-01-02 15:04:05"); s != c.want {
			t.Errorf("%q: got %s, want %s", c.spec, s, c.want)
		}
	}

	// explicit zone
	got, err := ParseSpec("09:30 11/25/2026 UTC", now, ny)
	if err != nil || got.UTC().Format("2006-01-02 15:04") != "2026-11-25 09:30" {
		t.Errorf("UTC zone: %v %v", got, err)
	}

	bad := []string{
		"", "foo", "25:00", "9:75", "yesterday", "now - 5 minutes",
		"10-1-2026 9am", "feb 30 2027", "11-25-2026 09:30 sunday", // Nov 25 2026 is a Wednesday
		"+", "+ hours", "5", "13pm", "9am 10am", "tomorrow today",
		"now + 1:90", "now + 1:30:75", "now + 0:60",
	}
	for _, s := range bad {
		if got, err := ParseSpec(s, now, ny); err == nil {
			t.Errorf("%q: expected error, got %v", s, got)
		}
	}
}

func TestDST(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, ny)
	// 2026-11-01 is the fall-back day. Day offsets keep wall-clock time;
	// hour offsets are absolute elapsed time.
	got, err := ParseSpec("11-01-2026 00:30 + 3 hours", now, ny)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sub(time.Date(2026, 11, 1, 0, 30, 0, 0, ny)) != 3*time.Hour {
		t.Errorf("hours should be elapsed time, got %v", got)
	}
}
