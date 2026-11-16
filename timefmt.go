package main

// How times are shown in listings and messages.
//
// The setting is "time_format" in the config file (environment: AT_TIME_FORMAT).
// There is deliberately no command-line flag for it. It is one of:
//
//	12h   (default)  Sun Nov 15 11:15:00 PM 2026
//	24h              Sun Nov 15 23:15:00 2026
//	a custom format built from % sections, like date(1) and strftime(3)
//
// Sections:
//
//	%a Sun   %A Sunday   %b Nov   %B November   %h same as %b
//	%d 05    %e " 5"     %m 11    %y 26         %Y 2026     %j day of year
//	%H 23    %k "23"     %I 11    %l "11"       %M 15       %S 00
//	%p AM/PM %P am/pm    %Z EST   %z -0500
//	%F 2026-11-15        %T 23:15:00   %R 23:15   %r 11:15:00 PM
//	%% a literal %       %n newline    %t tab
//
// %e, %k and %l pad with a space instead of a zero, so columns stay aligned.

import (
	"fmt"
	"strings"
	"time"
)

const (
	timeFormat12 = "%a %b %e %I:%M:%S %p %Y"
	timeFormat24 = "%a %b %e %H:%M:%S %Y"
)

// timeFormatSpec resolves the setting to a % format; the presets are expanded.
func timeFormatSpec() string {
	v := strings.TrimSpace(cfg("AT_TIME_FORMAT"))
	switch strings.ToLower(v) {
	case "", "12h", "12":
		return timeFormat12
	case "24h", "24":
		return timeFormat24
	}
	return v
}

// checkTimeFormat is the config-file check for time_format.
func checkTimeFormat(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "12h", "12", "24h", "24":
		return nil
	}
	_, err := strftime(time.Now(), v)
	return err
}

func pad2(n int) string { return fmt.Sprintf("%02d", n) }
func sp2(n int) string  { return fmt.Sprintf("%2d", n) }

// strftime formats t by the % sections above. An unknown section is an error
// so a typo in the config file is reported rather than printed.
func strftime(t time.Time, spec string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(spec); i++ {
		c := spec[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(spec) {
			return "", fmt.Errorf("format ends with a lone %%")
		}
		h12 := t.Hour() % 12
		if h12 == 0 {
			h12 = 12
		}
		switch spec[i] {
		case 'a':
			b.WriteString(t.Format("Mon"))
		case 'A':
			b.WriteString(t.Format("Monday"))
		case 'b', 'h':
			b.WriteString(t.Format("Jan"))
		case 'B':
			b.WriteString(t.Format("January"))
		case 'd':
			b.WriteString(pad2(t.Day()))
		case 'e':
			b.WriteString(sp2(t.Day()))
		case 'm':
			b.WriteString(pad2(int(t.Month())))
		case 'y':
			b.WriteString(pad2(t.Year() % 100))
		case 'Y':
			fmt.Fprintf(&b, "%d", t.Year())
		case 'j':
			fmt.Fprintf(&b, "%03d", t.YearDay())
		case 'H':
			b.WriteString(pad2(t.Hour()))
		case 'k':
			b.WriteString(sp2(t.Hour()))
		case 'I':
			b.WriteString(pad2(h12))
		case 'l':
			b.WriteString(sp2(h12))
		case 'M':
			b.WriteString(pad2(t.Minute()))
		case 'S':
			b.WriteString(pad2(t.Second()))
		case 'p':
			b.WriteString(t.Format("PM"))
		case 'P':
			b.WriteString(t.Format("pm"))
		case 'Z':
			b.WriteString(t.Format("MST"))
		case 'z':
			b.WriteString(t.Format("-0700"))
		case 'F':
			b.WriteString(t.Format("2006-01-02"))
		case 'T':
			b.WriteString(t.Format("15:04:05"))
		case 'R':
			b.WriteString(t.Format("15:04"))
		case 'r':
			s, _ := strftime(t, "%I:%M:%S %p")
			b.WriteString(s)
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '%':
			b.WriteByte('%')
		default:
			return "", fmt.Errorf("unknown section %%%c (see go-at --help, CONFIGURATION)", spec[i])
		}
	}
	return b.String(), nil
}

// fmtWhen formats t the way the user asked for (time_format). A bad format
// cannot get here (it is rejected at startup), but fall back rather than print
// nothing.
func fmtWhen(t time.Time) string {
	s, err := strftime(t, timeFormatSpec())
	if err != nil {
		s, _ = strftime(t, timeFormat12)
	}
	return s
}

// fmtWhenZone is fmtWhen plus the zone name, unless the format already has one.
func fmtWhenZone(t time.Time) string {
	s := fmtWhen(t)
	if spec := timeFormatSpec(); strings.Contains(spec, "%Z") || strings.Contains(spec, "%z") {
		return s
	}
	return s + " " + t.Format("MST")
}
