package main

// Time specification parser for at.
//
// Grammar (informal, order of the pieces is flexible):
//
//	spec     := piece+
//	piece    := time | date | zone | offsets | "now"
//	time     := H:MM[:SS] [am|pm] | HHMM | H am|pm | noon | midnight | teatime
//	date     := today | tomorrow | weekday | "next" weekday
//	          | MM/DD[/YYYY] | MM-DD[-YYYY] | YYYY-MM-DD | DD.MM[.YYYY]
//	          | month DAY [YEAR] | DAY month [YEAR]
//	zone     := utc | et | ct | mt | pt (DST-aware) | an IANA name (America/New_York)
//	offsets  := ("+"|"-") group+ { ("+"|"-") group+ }
//	group    := N unit | H:MM[:SS]      (an optional "and" may separate groups)
//	unit     := s|sec|second(s) | m|min|minute(s) | h|hr|hour(s)
//	          | d|day(s) | w|week(s) | month(s) | y|yr|year(s)
//
// Offsets apply to whatever the rest of the spec resolves to, so
// "11-25-2026 09:30 + 2 hours 55 minutes" is 12:25 on Nov 25.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zone names work even on minimal systems
)

type tokKind int

const (
	tNum tokKind = iota
	tDate
	tWord
	tPlus
	tMinus
)

type token struct {
	kind tokKind
	s    string
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func lex(s string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == ',':
			i++
		case c == '+':
			toks = append(toks, token{tPlus, "+"})
			i++
		case c == '-':
			toks = append(toks, token{tMinus, "-"})
			i++
		case isDigit(c):
			j, kind := i, tNum
			for j < len(s) {
				d := s[j]
				if isDigit(d) {
					j++
					continue
				}
				if j+1 < len(s) && isDigit(s[j+1]) {
					if d == ':' {
						j++
						continue
					}
					if d == '/' || d == '.' || d == '-' {
						kind = tDate
						j++
						continue
					}
				}
				break
			}
			toks = append(toks, token{kind, s[i:j]})
			i = j
		case isLetter(c):
			j := i
			for j < len(s) && (isLetter(s[j]) || s[j] == '/' || s[j] == '_') {
				j++
			}
			toks = append(toks, token{tWord, s[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q", c)
		}
	}
	return toks, nil
}

var months = map[string]time.Month{
	"jan": 1, "january": 1, "feb": 2, "february": 2, "mar": 3, "march": 3,
	"apr": 4, "april": 4, "may": 5, "jun": 6, "june": 6, "jul": 7, "july": 7,
	"aug": 8, "august": 8, "sep": 9, "sept": 9, "september": 9,
	"oct": 10, "october": 10, "nov": 11, "november": 11, "dec": 12, "december": 12,
}

var weekdays = map[string]int{
	"sun": 0, "sunday": 0, "mon": 1, "monday": 1, "tue": 2, "tues": 2, "tuesday": 2,
	"wed": 3, "wednesday": 3, "thu": 4, "thur": 4, "thurs": 4, "thursday": 4,
	"fri": 5, "friday": 5, "sat": 6, "saturday": 6,
}

var zoneAliases = map[string]string{
	"utc": "UTC", "gmt": "UTC", "z": "UTC",
	"et": "America/New_York", "est": "America/New_York", "edt": "America/New_York",
	"ct": "America/Chicago", "cst": "America/Chicago", "cdt": "America/Chicago",
	"mt": "America/Denver", "mst": "America/Denver", "mdt": "America/Denver",
	"pt": "America/Los_Angeles", "pst": "America/Los_Angeles", "pdt": "America/Los_Angeles",
}

func unitOf(w string) (string, bool) {
	switch strings.ToLower(w) {
	case "s", "sec", "secs", "second", "seconds":
		return "s", true
	case "m", "min", "mins", "minute", "minutes":
		return "m", true
	case "h", "hr", "hrs", "hour", "hours":
		return "h", true
	case "d", "day", "days":
		return "d", true
	case "w", "wk", "wks", "week", "weeks":
		return "w", true
	case "month", "months":
		return "mo", true
	case "y", "yr", "yrs", "year", "years":
		return "y", true
	}
	return "", false
}

type offset struct {
	n    int
	unit string
}

type parser struct {
	toks []token
	i    int
	loc  *time.Location

	hasTime  bool
	h, mi, s int

	hasDate bool
	y       int
	mo      time.Month
	d       int

	weekday   int // -1 when unset
	forceNext bool
	relDay    int
	hasRel    bool

	offs []offset
}

func (p *parser) peek() (token, bool) {
	if p.i < len(p.toks) {
		return p.toks[p.i], true
	}
	return token{}, false
}

func (p *parser) peekWord() (string, bool) {
	if t, ok := p.peek(); ok && t.kind == tWord {
		return strings.ToLower(t.s), true
	}
	return "", false
}

func (p *parser) takeAMPM() string {
	if w, ok := p.peekWord(); ok && (w == "am" || w == "pm") {
		p.i++
		return w
	}
	return ""
}

func parseClock(s string) (h, m, sec int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, fmt.Errorf("bad time %q", s)
	}
	nums := make([]int, 3)
	for i, part := range parts {
		nums[i], err = strconv.Atoi(part)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("bad time %q", s)
		}
	}
	return nums[0], nums[1], nums[2], nil
}

func (p *parser) setTime(h, m, s int, ampm string) error {
	if p.hasTime {
		return fmt.Errorf("time given more than once")
	}
	if ampm != "" {
		if h < 1 || h > 12 {
			return fmt.Errorf("hour %d out of range for %s", h, ampm)
		}
		if ampm == "am" && h == 12 {
			h = 0
		} else if ampm == "pm" && h < 12 {
			h += 12
		}
	}
	if h > 23 || m > 59 || s > 59 {
		return fmt.Errorf("invalid time %02d:%02d:%02d", h, m, s)
	}
	p.hasTime, p.h, p.mi, p.s = true, h, m, s
	return nil
}

func (p *parser) setDate(y int, mo time.Month, d int) error {
	if p.hasDate || p.hasRel {
		return fmt.Errorf("date given more than once")
	}
	if mo < 1 || mo > 12 || d < 1 || d > 31 {
		return fmt.Errorf("invalid date")
	}
	p.hasDate, p.y, p.mo, p.d = true, y, mo, d
	return nil
}

func fixYear(y int) int {
	if y < 100 {
		return y + 2000
	}
	return y
}

func (p *parser) parseDateTok(s string) error {
	atoi := func(x string) (int, error) { return strconv.Atoi(x) }
	var parts []string
	var order string
	switch {
	case strings.Contains(s, ":"):
		return fmt.Errorf("cannot mix a date and a time in %q (separate them with a space)", s)
	case strings.Contains(s, "/"):
		parts, order = strings.Split(s, "/"), "mdy"
	case strings.Contains(s, "."):
		parts, order = strings.Split(s, "."), "dmy"
	default:
		parts = strings.Split(s, "-")
		if len(parts[0]) == 4 {
			order = "ymd"
		} else {
			order = "mdy"
		}
	}
	if len(parts) < 2 || len(parts) > 3 || (order == "ymd" && len(parts) != 3) {
		return fmt.Errorf("bad date %q", s)
	}
	n := make([]int, len(parts))
	for i, part := range parts {
		v, err := atoi(part)
		if err != nil {
			return fmt.Errorf("bad date %q", s)
		}
		n[i] = v
	}
	var y, m, d int
	switch order {
	case "mdy":
		m, d = n[0], n[1]
		if len(n) == 3 {
			y = fixYear(n[2])
		}
	case "dmy":
		d, m = n[0], n[1]
		if len(n) == 3 {
			y = fixYear(n[2])
		}
	case "ymd":
		y, m, d = n[0], n[1], n[2]
	}
	return p.setDate(y, time.Month(m), d)
}

// optional trailing 4-digit year after "month day"
func (p *parser) takeYear() int {
	if t, ok := p.peek(); ok && t.kind == tNum && len(t.s) == 4 {
		if v, _ := strconv.Atoi(t.s); v >= 1970 {
			p.i++
			return v
		}
	}
	return 0
}

func (p *parser) parseNum(t token) error {
	if strings.Contains(t.s, ":") {
		h, m, s, err := parseClock(t.s)
		if err != nil {
			return err
		}
		return p.setTime(h, m, s, p.takeAMPM())
	}
	v, _ := strconv.Atoi(t.s)
	if ap := p.takeAMPM(); ap != "" {
		return p.setTime(v, 0, 0, ap)
	}
	if w, ok := p.peekWord(); ok {
		if mo, isMonth := months[w]; isMonth {
			p.i++
			return p.setDate(p.takeYear(), mo, v)
		}
	}
	if len(t.s) == 3 || len(t.s) == 4 {
		return p.setTime(v/100, v%100, 0, "")
	}
	return fmt.Errorf("don't know what %q means here (try 9:30, 9am, or add a unit like \"%s minutes\")", t.s, t.s)
}

func (p *parser) parseOffsets(sign int) error {
	for {
		got := 0
		for {
			t, ok := p.peek()
			if ok && t.kind == tWord && strings.EqualFold(t.s, "and") && got > 0 {
				p.i++
				continue
			}
			if !ok || t.kind != tNum {
				break
			}
			if strings.Contains(t.s, ":") {
				h, m, s, err := parseClock(t.s)
				if err != nil {
					return err
				}
				p.offs = append(p.offs, offset{sign * h, "h"}, offset{sign * m, "m"}, offset{sign * s, "s"})
				p.i++
				got++
				continue
			}
			if p.i+1 >= len(p.toks) || p.toks[p.i+1].kind != tWord {
				break
			}
			u, isUnit := unitOf(p.toks[p.i+1].s)
			if !isUnit {
				break
			}
			n, _ := strconv.Atoi(t.s)
			p.offs = append(p.offs, offset{sign * n, u})
			p.i += 2
			got++
		}
		if got == 0 {
			return fmt.Errorf("expected a number and a unit (like \"2 hours\") after +/-")
		}
		t, ok := p.peek()
		if ok && (t.kind == tPlus || t.kind == tMinus) {
			sign = 1
			if t.kind == tMinus {
				sign = -1
			}
			p.i++
			continue
		}
		return nil
	}
}

func (p *parser) parseWord(t token) error {
	w := strings.ToLower(t.s)
	switch w {
	case "now", "at", "on", "of", "the":
		return nil
	case "noon":
		return p.setTime(12, 0, 0, "")
	case "midnight":
		return p.setTime(0, 0, 0, "")
	case "teatime":
		return p.setTime(16, 0, 0, "")
	case "today", "tomorrow":
		if p.hasDate || p.weekday >= 0 || p.hasRel {
			return fmt.Errorf("date given more than once")
		}
		p.hasRel = true
		if w == "tomorrow" {
			p.relDay = 1
		}
		return nil
	case "in":
		return p.parseOffsets(1)
	case "next":
		nw, ok := p.peekWord()
		if !ok {
			return fmt.Errorf("expected a unit or weekday after \"next\"")
		}
		if u, isUnit := unitOf(nw); isUnit {
			p.i++
			p.offs = append(p.offs, offset{1, u})
			return nil
		}
		if wd, isDay := weekdays[nw]; isDay {
			p.i++
			p.weekday, p.forceNext = wd, true
			return nil
		}
		return fmt.Errorf("don't understand \"next %s\"", nw)
	case "am", "pm":
		return fmt.Errorf("%q without an hour", t.s)
	}
	if wd, ok := weekdays[w]; ok {
		if p.weekday >= 0 || p.hasRel {
			return fmt.Errorf("date given more than once")
		}
		p.weekday = wd
		return nil
	}
	if mo, ok := months[w]; ok {
		nt, ok := p.peek()
		if !ok || nt.kind != tNum || strings.Contains(nt.s, ":") || len(nt.s) > 2 {
			return fmt.Errorf("expected a day of the month after %q", t.s)
		}
		p.i++
		d, _ := strconv.Atoi(nt.s)
		return p.setDate(p.takeYear(), mo, d)
	}
	if name, ok := zoneAliases[w]; ok {
		loc, err := time.LoadLocation(name)
		if err != nil {
			return err
		}
		p.loc = loc
		return nil
	}
	if strings.Contains(t.s, "/") {
		loc, err := time.LoadLocation(t.s)
		if err != nil {
			return fmt.Errorf("unknown time zone %q", t.s)
		}
		p.loc = loc
		return nil
	}
	return fmt.Errorf("don't understand %q", t.s)
}

// ParseSpec resolves a time specification to an absolute time.
// def is the zone used unless the spec names one.
func ParseSpec(spec string, now time.Time, def *time.Location) (time.Time, error) {
	toks, err := lex(spec)
	if err != nil {
		return time.Time{}, err
	}
	if len(toks) == 0 {
		return time.Time{}, fmt.Errorf("empty time specification")
	}
	p := &parser{toks: toks, loc: def, weekday: -1}
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		p.i++
		switch t.kind {
		case tPlus:
			err = p.parseOffsets(1)
		case tMinus:
			err = p.parseOffsets(-1)
		case tDate:
			err = p.parseDateTok(t.s)
		case tNum:
			err = p.parseNum(t)
		case tWord:
			err = p.parseWord(t)
		}
		if err != nil {
			return time.Time{}, err
		}
	}
	return p.resolve(now)
}

func (p *parser) resolve(now time.Time) (time.Time, error) {
	loc := p.loc
	nowL := now.In(loc).Truncate(time.Second)
	y, mo, d := nowL.Date()
	h, mi, s := nowL.Clock()
	yearGiven := p.hasDate && p.y != 0
	if p.hasDate {
		if yearGiven {
			y = p.y
		}
		mo, d = p.mo, p.d
	}
	if p.hasTime {
		h, mi, s = p.h, p.mi, p.s
	}
	if p.hasDate {
		chk := time.Date(y, mo, d, 0, 0, 0, 0, loc)
		if chk.Day() != d {
			return time.Time{}, fmt.Errorf("%s %d does not exist in %d", mo, d, y)
		}
		if p.weekday >= 0 && int(chk.Weekday()) != p.weekday {
			return time.Time{}, fmt.Errorf("%s is a %s, not a %s",
				chk.Format("2006-01-02"), chk.Weekday(), time.Weekday(p.weekday))
		}
	}
	if p.weekday >= 0 && !p.hasDate {
		delta := (p.weekday - int(nowL.Weekday()) + 7) % 7
		if p.forceNext && delta == 0 {
			delta = 7
		}
		d += delta
	}
	d += p.relDay

	build := func(dayShift, yearShift int) time.Time {
		t := time.Date(y+yearShift, mo, d+dayShift, h, mi, s, 0, loc)
		for _, o := range p.offs {
			switch o.unit {
			case "s":
				t = t.Add(time.Duration(o.n) * time.Second)
			case "m":
				t = t.Add(time.Duration(o.n) * time.Minute)
			case "h":
				t = t.Add(time.Duration(o.n) * time.Hour)
			case "d":
				t = t.AddDate(0, 0, o.n)
			case "w":
				t = t.AddDate(0, 0, 7*o.n)
			case "mo":
				t = t.AddDate(0, o.n, 0)
			case "y":
				t = t.AddDate(o.n, 0, 0)
			}
		}
		return t
	}

	res := build(0, 0)
	if res.Before(nowL) {
		switch {
		case p.hasDate && !yearGiven:
			res = build(0, 1)
		case p.weekday >= 0 && !p.hasDate && !p.hasRel:
			res = build(7, 0)
		case !p.hasDate && p.weekday < 0 && !p.hasRel && p.hasTime:
			res = build(1, 0)
		}
	}
	if res.Before(nowL) {
		return time.Time{}, fmt.Errorf("that time (%s) is in the past", res.Format("Mon Jan _2 15:04:05 2006 MST"))
	}
	return res, nil
}
