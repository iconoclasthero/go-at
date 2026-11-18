# Time specs

Everything go-at accepts after the options as a time.

[← back to the README](../README.md)

## Time specs

    TIME := any order of: clock, date, zone, offsets
    clock   9:30  9:30:15  9:30pm  9am  0930  noon  midnight  teatime
    date    today tomorrow sunday  next friday  11-25-2026  11/25/2026
            2026-11-25  25.11.2026  nov 25 [2026]  25 nov [2026]
    zone    utc  et ct mt pt (DST-aware)  or an IANA name like Europe/Paris
    offset  + or -, then N unit [N unit ...] or H:MM[:SS]
            units: s m h d w month y (and long forms: seconds, minutes, ...)

Offsets apply to whatever the rest of the spec resolves to. Hour/minute/second
offsets are elapsed time (correct across DST changes); day/week/month/year
offsets keep wall-clock time. In `H:MM[:SS]` durations minutes and seconds must
be below 60 (`now + 1:90` is an error, not 2h30m).

- A bare clock time that has passed means tomorrow; a bare weekday means its
  next occurrence; a month/day without a year that has passed means next year.
- A date that contradicts its weekday (`sunday 11-25-2026`, which is a
  Wednesday) is an error rather than a guess.
- Numeric dates are US order (`MM-DD-YYYY`, `MM/DD/YYYY`) except `YYYY-MM-DD`
  and dotted `DD.MM.YYYY`.
- A date with no clock time (`tomorrow`, `friday`) uses the current time of day.
