# at (Go)

A from-scratch Go take on `at`. One binary, five names: `at`, `atq`, `atrm`,
`batch`, `atd`. No dependencies, no root, no setuid.

What's different from classic `at`:

1. **Jobs run in bash** by default (`-s SHELL` or `$AT_SHELL` to change it).
2. **`at>` has a real line editor**: arrows, Home/End, Ctrl-A/E, word jumps
   (Alt-B/F, Ctrl-Left/Right), Delete, Ctrl-K/U/W/Y, history (Up/Down). Ctrl-D ends input.
3. **Seconds** work everywhere: `9:30:15`, `+ 90 seconds`, `now + 5 sec`.
4. **Mixed-unit offsets**: `at 11-25-2026 09:30 + 2 hours 55 minutes`,
   `at +2h55m`, `at now + 1 day 2 hours - 5 minutes`, `at now + 2:55`.

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
offsets keep wall-clock time. A bare past clock time means tomorrow, a bare
weekday means its next occurrence, and a date that doesn't match its weekday
(`sunday 11-25-2026`) is an error rather than a guess.

## Running

    make install              # ~/.local/bin/{at,atq,atrm,batch,atd,at-go}
    make install-service      # systemd user unit; or just run `atd &`

    echo 'make backup' | at 02:30 tomorrow
    at now + 2 hours 55 minutes          # interactive at> prompt
    atq ; at -c 3 ; atrm 3

The spool is per-user: `$AT_SPOOL`, else `~/.local/state/at`. Job output goes
to `<spool>/logs/<id>.log` (no mail). Jobs only run while `atd` is running;
`at` warns if it isn't. Jobs that were due while the daemon was down run on
startup. `batch` (queue `b`) waits for 1-min load < 1.5 (`$AT_BATCH_LOAD`).

## Known limits

- Long input lines scroll horizontally instead of wrapping; East Asian wide
  characters are counted as one column.
- No mail, no `at.allow`/`at.deny`, no system-wide spool.
- Linux is tested; macOS cross-compiles but is untested; no BSD support.
