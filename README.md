# go-at

A from-scratch Go take on `at`. One binary (`go-at`), five command names:
`at`, `atq`, `atrm`, `batch`, `atd`. No dependencies, no root, no setuid.

This is **not a drop-in replacement** for Debian `at`. It keeps the familiar
commands and fixes the parts that annoyed its author; see
[Compatibility decisions](#compatibility-decisions) for exactly where it differs.

What's different, on purpose:

1. **Jobs run in bash** by default (`-s SHELL` or `$AT_SHELL` to change it).
2. **`at>` has a real line editor**: arrows, Home/End, Ctrl-A/E, word jumps
   (Alt-B/F, Ctrl-Left/Right), Delete, Ctrl-K/U/W/Y, history (Up/Down). Ctrl-D
   ends input, Ctrl-C cancels.
3. **Seconds** work everywhere: `9:30:15`, `+ 90 seconds`, `now + 5 sec`.
4. **Mixed-unit offsets**: `at 11-25-2026 09:30 + 2 hours 55 minutes`,
   `at +2h55m`, `at now + 1 day 2 hours - 5 minutes`, `at now + 2:55`.

## Install

    cd go-at
    make test                 # go vet + unit and end-to-end tests
    make install              # ~/.local/bin/{go-at,at,atq,atrm,batch,atd}

Put `~/.local/bin` **before** `/usr/bin` in `PATH`, or you will keep running the
system `at`. (`hash -r; which at` to check.) Needs Go to build; tested with 1.24
and newer.

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

Resolution rules:

- A bare clock time that has passed means tomorrow; a bare weekday means its
  next occurrence; a month/day without a year that has passed means next year.
- A date that contradicts its weekday (`sunday 11-25-2026`, which is a
  Wednesday) is an error rather than a guess.
- Numeric dates are US order (`MM-DD-YYYY`, `MM/DD/YYYY`) except `YYYY-MM-DD`
  and dotted `DD.MM.YYYY`.

## Running the daemon

Jobs only run while `atd` is running. `at` warns when it isn't.

    atd -d          # start in the background; silent no-op if already running
    atd             # foreground (what a service manager wants)

**With systemd:** `make install-service` installs and starts a user unit.

**Without systemd** (shared hosts, seedboxes): a bare `atd &` is not enough,
since nothing restarts it if it dies or the machine reboots. `atd -d` is built
to be run repeatedly, so let cron be the watchdog:

    @reboot   $HOME/.local/bin/atd -d
    * * * * * $HOME/.local/bin/atd -d

It takes a lock on the spool, so at most one daemon ever runs; the cron line does
nothing while it's alive and restarts it within a minute if it isn't. Daemon
output goes to `<spool>/atd.log`. `atd` ignores SIGHUP, so it survives its
terminal closing. Alternatively set `AT_AUTOSTART=1` and `at` will start the
daemon itself whenever it finds none (that still does not survive a reboot).

## Compatibility decisions

Things that differ from Debian `at`, each chosen deliberately:

- **Spool and privileges.** Per-user spool (`$AT_SPOOL`, else
  `~/.local/state/at`), no setuid, no system-wide `atd`, no `at.allow` /
  `at.deny`. A user can only schedule and see their own jobs.
- **Environment.** Jobs capture the submitting shell's environment as `export`
  lines, except session-specific variables (`TERM`, `DISPLAY`, `PWD`, `OLDPWD`,
  `SHLVL`, `_`, `UID`, `EUID`, `PPID`, `GROUPS`, `SHELLOPTS`, `BASHOPTS`,
  `BASH_VERSION*`, `COLUMNS`, `LINES`) and exported bash functions. The working
  directory and `umask` at submission time are restored. `at -c ID` prints the
  exact script.
- **Shell.** bash by default, resolved to an absolute path at submission time
  and written as the script's shebang. The job file is a plain script; you can
  read or run it by hand.
- **Output.** Written to `<spool>/logs/<id>.log`, with a trailing line giving the
  exit status. **No mail.** If you want a notification, put it in the job.
- **A date with no clock time** (`at tomorrow`, `at friday`) uses the current
  time of day.
- **Options supported:** `-q`, `-f`, `-s`, `-l`, `-d`/`-r`, `-c`, `-b`, plus
  `atq [-q]`, `atrm ID...`, `batch`. Everything else in `at(1)` (`-m`, `-v`,
  `-t`, `-V`, ...) is not implemented.
- **`batch`** (queue `b`) waits for the 1-minute load average to drop below 1.5
  (`$AT_BATCH_LOAD`); on systems without `/proc/loadavg` it just runs.
- **Late jobs.** A job that came due while the daemon was down runs as soon as
  the daemon starts.

### What happens if the daemon dies mid-job

The job is **not** retried. When `atd` starts it finds scripts still marked as
running, logs that they were interrupted, and moves them to
`<spool>/logs/<id>.interrupted` instead of re-running them: the daemon cannot
know how far a job got, and re-running a half-finished download or transfer can
repeat side effects. If a job must survive crashes, make the script itself
idempotent and resumable (for example, skip finished steps and write a
"succeeded" marker only after the last step), and have your own check look for
that marker. Jobs that were merely pending are unaffected and run normally.

## Terminal safety

While `at>` is active the terminal is in raw mode. It is restored on normal
exit, on Ctrl-C/Ctrl-D, on SIGTERM/SIGHUP/SIGINT/SIGQUIT, and on any error
exit. Only SIGKILL can leave it raw; run `stty sane` or `reset` if that happens.

## Known limits

- Long input lines scroll horizontally instead of wrapping.
- Character widths use a built-in approximation of `wcwidth` (CJK and emoji are
  2 columns, combining marks 0). Unusual scripts, ZWJ emoji sequences and
  terminal-specific width quirks can still misplace the cursor.
- Tab does nothing (no completion).
- Linux is tested. macOS cross-compiles but is untested; no BSD support.
