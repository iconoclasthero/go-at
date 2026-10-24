# go-at

A from-scratch Go take on `at`: schedule a command to run later. One binary,
installed under five names that never collide with the system `at`:

    go-at     schedule a job                  go-atrm   remove or stop jobs
    go-atq    list jobs                       go-batch  schedule a job that waits for low load
    go-atd    the daemon that runs them

No dependencies, no root, no setuid. This is **not a drop-in replacement** for
`at`; see [Compatibility decisions](#compatibility-decisions).

What's different, on purpose:

1. **Jobs run in bash** by default (`-s SHELL` or `$AT_SHELL` to change it).
2. **`at>` has a real line editor**: arrows, Home/End, word jumps, Ctrl-K/U/W/Y,
   history. Ctrl-D ends input, Ctrl-C cancels.
3. **Seconds** work everywhere: `9:30:15`, `+ 90 seconds`.
4. **Mixed-unit offsets**: `go-at 11-25-2026 09:30 + 2 hours 55 minutes`.
5. **Plain words, not codes.** States are `queued`, `running`, `finished`...
   Jobs can have **names**. You can see a running job's **process group** and
   **stop it** without having captured `$$`.

## Install

    cd go-at
    make test                 # go vet + unit and end-to-end tests
    make install              # ~/.local/bin/{go-at,go-atq,go-atrm,go-batch,go-atd}

Needs Go **1.24.7 or newer** to build (the `go` line in `go.mod`; no older release is supported). Prefer a currently supported Go release for security fixes. For a fully static binary that
needs no libc (handy on seedboxes and minimal containers) use `make static`
instead of `make`, then `make install`; cross-compile with e.g.
`GOARCH=arm64 make static`. A plain `go build` links libc dynamically, because
Go's `os/user` uses cgo when it is available; check with `ldd go-at`. Make sure `~/.local/bin` is on
your `PATH`. If you want the classic names, symlink them yourself
(`ln -s go-at ~/.local/bin/at`); the binary answers to either spelling.

## Example: wait for something, then poll until it appears

    $ go-at --name steelers-arr 2026-10-25 09:30 + 2 hours 55 minutes
    at Sun Oct 25 12:25:00 2026 EDT
    at> until steelers-arr; do sleep 60; ((++i)); printf %s\\n "$i"; done
    at> <Ctrl-D>
    job 10 (steelers-arr) at Sun Oct 25 12:25:00 2026 EDT

    $ go-atq
    ID  NAME          STATE    WHEN                              DETAIL
    10  steelers-arr  running  started Sun Oct 25 12:25:00 2026  pgid 48213

    $ go-atrm -k steelers-arr         # stops the loop and its sleep
    sent SIGTERM to job 10 (steelers-arr); process group 48213

    $ go-atq --all
    ID  NAME          STATE       WHEN                            DETAIL
    10  steelers-arr  terminated  ended Sun Oct 25 14:02:11 2026  SIGTERM, ran 1h37m11s

    $ go-at --log steelers-arr        # everything the job printed

## States

Every job is in exactly one state, always shown as a word:

| state | meaning |
| :- | :- |
| `queued` | waiting for its time |
| `waiting` | due, but `--batch` is holding it until system load is low |
| `running` | its process group is alive |
| `finished` | exited 0 |
| `failed` | exited non-zero (the exit code is shown) |
| `terminated` | killed by a signal (the signal is shown) |
| `interrupted` | the daemon stopped while it was running and the job is gone; **never re-run** |

`go-atq` lists queued, waiting and running jobs. `go-atq --all` adds ended
ones, kept for 30 days (`$AT_KEEP_DAYS`). `go-atq --json` is for scripts and any
future UI: one object per job with `id`, `name`, `state`, `scheduled`,
`started`, `ended`, `pgid`, `nice`, `batch`, `exit_code`, `signal`, `log`.

## Names and ids

A job has a number (assigned in order, just a handle) and optionally a name.
Anywhere a job is expected you can use either: `go-atrm steelers-arr`,
`go-at -c 10`. Names start with a letter (so they can't be mistaken for numbers
or options), may contain letters, digits, `.`, `_`, `-`, and must be unique
among queued and running jobs.

## Finding and stopping a running job

While a job runs the daemon keeps `<spool>/running/<id>.pid`, a bare number:
the job's **process-group id**. Each job runs in its own session, so the shell,
your `until` loop and every `sleep` it starts share that group. Use it directly:

    kill -- -"$(cat ~/.local/state/at/running/10.pid)"      # note the minus

or let go-atrm do it: `go-atrm -k NAME` sends SIGTERM to the whole group
(`-s KILL`, `-s INT`, `-s 1`... to choose). Plain `go-atrm` only removes queued
jobs and refuses to touch a running one. Scripts can also use the `AT_JOB_ID`
and `AT_JOB_NAME` variables every job gets.

### What "finished" means, and background processes

- **A job is finished when its shell exits.** The daemon does not wait for
  other processes the job started. `sleep 300 &` as the last line gives a job
  that finishes immediately while the `sleep` keeps going.
- **Background processes are the script's responsibility.** Anything a job
  deliberately leaves running (`&`, `nohup`, `setsid`, daemons) can outlive the
  job. If you want a job to stay "running" until its work is done, run it in
  the foreground or end the script with `wait`.
- **`-k` only reaches a running job.** It signals the job's process group, which
  includes background children *while the shell is still running*. The pid
  records are deleted when the shell exits, so after that the job is
  `finished`, `go-atrm -k` refuses, and go-at no longer tracks leftover
  descendants. (A script that must be killable afterwards should record its own
  pids.)

### When `-k` refuses to signal (fail closed)

Before sending anything, `-k` has to prove that the recorded pid is still the
process the daemon started, because the OS may have reused the number. On
Linux all of these must hold:

1. the pid exists and leads its own process group;
2. `<spool>/running/<id>.proc` holds a valid, nonzero recorded start time;
3. the live `/proc/<pid>/stat` start time can be read and equals it.

If any of that cannot be established, **nothing is signalled**. A mismatch
means the pid now belongs to something else, and `-k` says the job is no longer
running; a missing, unreadable or invalid record is reported as "its process
identity could not be verified; nothing was signalled", and `go-atq` shows
that job as running with "process identity unverified". Without `/proc` (macOS)
the start-time proof is impossible, so only the first check applies, which
**cannot rule out a recycled pid**. This guards against signalling the wrong
process; it is not a guarantee against every race, and `-k` handles a process
that exits just before the signal arrives.

## Writing and editing commands in your editor

    go-at --compose --name nightly 02:00      # also -C: write the commands in $VISUAL
    go-at --edit steelers-arr                 # also -e: change a queued job (name or number)

Both use `$VISUAL`, else `$EDITOR` (the value may include arguments, e.g.
`EDITOR="nano -l"`). If neither is set you get a message saying so, with an
example (`VISUAL=/usr/bin/nano go-at --compose ...`); there is no fallback
editor. The interactive `at>` prompt tells you about `--compose` when you start.

- **Edit** shows only your commands; the time, name, environment and working
  directory captured at submission are kept. Quitting without changes prints
  "no changes"; an empty result or an editor that exits non-zero leaves the job
  untouched. Only queued/waiting jobs can be edited. If the job starts while
  you are in the editor, nothing is applied and your text goes to
  `<spool>/edit-<id>.rejected`. The run time cannot be changed: remove and
  resubmit.
- **Syntax check:** after you save, the commands get a `bash -n` check. At a
  terminal you are asked `[e]dit again, [s]ave anyway, [a]bort`; without a
  terminal the text is not applied and is kept in `<spool>/edit-<id>.rejected`
  (or `compose.rejected`). Only syntax is checked, not whether commands exist.
- The temp file lives in a private (0700) directory and is removed afterwards.
- Tested against real nano 7.2 on a pty.

## Priority and batch

- `--nice N` (0 to 19): run at lower priority; shown as `nice=N`.
- `--batch` (or `go-batch`): hold the job until the 1-minute load average is
  below 1.5 (`$AT_BATCH_LOAD`); until then it reads `waiting`.

There are no queue letters.

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

## Running the daemon

Jobs only run while `go-atd` is running; `go-at` and `go-atq` warn when it
isn't. It runs as **you**, never as root (see below).

    go-atd -d       # start in the background; silent no-op if already running
    go-atd          # foreground (what a service manager wants)

**systemd:** `make install-service` installs and starts a user unit
(`loginctl enable-linger $USER` keeps it alive after you log out). As root you
can instead run a system unit with `User=you` and avoid `--user` sessions.

**supervisord** (one entry per user). supervisord must itself run as root for
`user=` to take effect (note the lowercase `user=`; `User=` is the systemd
spelling). Put this in e.g. `/etc/supervisor/conf.d/go-atd.conf` (the directory
your main config `[include]`s), then
`sudo supervisorctl reread && sudo supervisorctl update`:

    [program:go-atd]
    command=/usr/local/bin/go-atd
    user=you
    directory=/home/you
    environment=HOME="/home/you",USER="you",LOGNAME="you"
    autostart=true
    autorestart=true
    startsecs=3
    stopsignal=TERM
    stopwaitsecs=30
    stdout_logfile=/home/you/.local/state/at/supervisor.out.log
    stderr_logfile=/home/you/.local/state/at/supervisor.err.log
    stdout_logfile_maxbytes=1MB
    stderr_logfile_maxbytes=1MB
    stdout_logfile_backups=2
    stderr_logfile_backups=2

- Use the foreground command: no `-d` (supervisord must own the process).
- Set `HOME` (or `AT_SPOOL`) so the daemon and your login shell agree on the
  spool; otherwise jobs queue and never run.
- Paths in supervisord configs must be absolute (`~` is not expanded), and the
  log directory must exist before the program starts
  (`mkdir -p ~/.local/state/at`). supervisord opens the log files as root, so
  they end up root-owned; use `stdout_logfile=NONE` to skip them, since
  `<spool>/atd.log` has the daemon's own messages.
- A SIGTERM waits for running jobs; supervisord sends SIGKILL after
  `stopwaitsecs`, which does not kill jobs (they run in their own sessions).
- Check: `ps -o user,pid,cmd -C go-atd` shows you, not root; then, as you,
  `go-at --name smoke now + 1 minute` and `go-atq`.
- **Environment:** a job gets exactly the environment captured when you
  submitted it, never the daemon's. (Earlier builds let supervisord's own
  variables leak into jobs.)

**Neither** (plain shared host): let cron be the watchdog. `go-atd -d` takes a
lock, so at most one daemon ever runs, and it does nothing while one is alive:

    @reboot   $HOME/.local/bin/go-atd -d
    * * * * * $HOME/.local/bin/go-atd -d

Daemon output goes to `<spool>/atd.log`. `go-atd` ignores SIGHUP, so it survives
its terminal closing. `AT_AUTOSTART=1` makes `go-at` start the daemon itself
when it finds none (that does not survive a reboot).

### Stopping the daemon, and crashes

SIGTERM/SIGINT make the daemon stop starting jobs and **wait for running jobs
to finish**, so their outcome is recorded; with a long-running job a stop can
take that long. If the daemon is killed anyway (SIGKILL, a supervisor timeout),
jobs keep running because they live in their own sessions. A restarted daemon
leaves live ones alone (`go-atq` still shows them running and `go-atrm -k` still
works) and, once such a job is gone, records it as `interrupted`, since its exit
status is unknown. A reboot ends the jobs too. **A job is never re-run
automatically**: it may have partly completed, and repeating a half-finished
download or transfer can repeat side effects. If a job must survive crashes,
make the script itself idempotent and resumable and have it write a "succeeded"
marker only after the last step. Interrupted scripts are kept as
`<spool>/logs/<id>.interrupted`.

## Files and settings

Per-user spool: `$AT_SPOOL`, else `$XDG_STATE_HOME/at`, else `~/.local/state/at`.

    jobs/<id>          queued job: a plain bash script (go-at -c shows it)
    running/<id>       the same script while it runs, plus <id>.pid and <id>.proc
    history/<id>.json  outcome record of an ended job
    logs/<id>.log      everything the job printed, with start/finish markers

| variable | effect |
| :- | :- |
| `AT_SPOOL` | spool directory |
| `AT_SHELL` | shell for jobs (default `bash`) |
| `AT_AUTOSTART=1` | `go-at` starts the daemon when none runs |
| `AT_BATCH_LOAD` | load-average limit for `--batch` (default 1.5; 0 holds batch jobs until raised) |
| `AT_KEEP_DAYS` | how long ended-job records and logs are kept (default 30) |
| `AT_JOB_ID`, `AT_JOB_NAME` | set inside every job |

## Compatibility decisions

Where this differs from Debian `at`, each on purpose:

- **Single-user model, no privileges.** Nothing is setuid, the daemon never
  changes uid, and there is no `at.allow`/`at.deny`. Each user runs their own
  daemon with their own spool. **Never run `go-atd` as root against a spool other
  users can write to**: it runs whatever is in `jobs/`, so that would let them
  run code as root. Run it as the user whose jobs it is (supervisord `user=`,
  systemd `User=`). It does not yet verify spool ownership or permissions.
- **Command names** carry a `go-` prefix so they never shadow the system `at`;
  the two don't share a spool.
- **Output format.** `go-atq` prints a table with a header and word states, not
  the classic one-line `id<TAB>date queue user`. Scripts that parse `atq` need
  `--json`. Queue letters and `-q` are gone (use `--nice`/`--batch`); a
  classic `-q` gives an error that says so.
- **Environment.** Jobs capture the submitting shell's environment as `export`
  lines, except session-specific variables (`TERM`, `DISPLAY`, `PWD`, `OLDPWD`,
  `SHLVL`, `_`, `UID`, `EUID`, `PPID`, `GROUPS`, `SHELLOPTS`, `BASHOPTS`,
  `BASH_VERSION*`, `COLUMNS`, `LINES`) and exported bash functions. The working
  directory and `umask` at submission are restored. `go-at -c` prints the exact
  script. The spool is private to you (`0700`), but the script holds your
  environment, including any secrets in it.
- **Output** goes to a log file; **no mail.** (Classic `at` emails a job's
  output to the local user through the system's mail program, which on most
  modern machines goes nowhere.) Put a notification in the job if you want one.
- **Late jobs.** A job that came due while the daemon was down runs as soon as
  it starts.
- **Options:** `-n/--name`, `--nice`, `-b/--batch`, `-s`, `-f`, `-l`, `-d`/`-r`,
  `-c`, `--log`, `-a/--all`, `--json`, and `go-atrm -k/-s`. Everything else in
  `at(1)` (`-m`, `-v`, `-t`, `-V`, ...) is not implemented.

## Compatibility roadmap

Goal: someone who knows `at` can switch without relearning it. Rules:

1. A classic flag either works as it always did, is an alias for a better
   spelling, or **fails loudly** and says what to use instead. It is never
   silently ignored or given a different meaning.
2. New features get long names first (`--name`, `--nice`), so the short-flag
   space stays classic.
3. Every classic flag gets a test, so the command set cannot drift quietly.

Status against the classic option set (taken from the `at(1)` man page):

| classic | meaning | go-at today | plan |
| :- | :- | :- | :- |
| `-f file`, `-c`, `-r`, `-d`, `-b` | read job from file; print job; remove; batch | works as classic | keep |
| `-l` | list jobs | works, but `at -l 5` ignores the `5` (breaks rule 1) | list only the named jobs |
| `-q letter` | queue; niceness is 2 x (letter - a); uppercase also gets batch rules | refused, with a message | accept; map to `--nice` / `--batch` |
| `-t [[CC]YY]MMDDhhmm[.ss]` | absolute time (POSIX) | missing | add |
| `-o fmt` | time format for the listing | missing | add |
| `-v` | show the run time before reading the job | missing | add |
| `-V` | print the version | missing | add |
| `-M` | never mail | missing | accept as a no-op (go-at never mails) |
| `-m`, `-u user` | mail on completion; mail recipient | missing | fail with "mail is not supported" |

Ideas taken from reviewing other `at` rewrites (not built): `--max-late
DURATION` to skip a job that is overdue by more than that (recorded as a
`skipped` state); changing a queued job's run time; history filters
(`--since/--until/--state/--name`); a man page and shell completions;
`--count`; and, only if ever
needed, a documented system-wide mode.

Also planned: an opt-in `--classic` flag for `go-atq` that prints the old
one-line format for scripts that parse it (the default stays plain words);
bare-digit dates such as `MMDDYY` in time specs; and `--shell` as the main
spelling of `-s SHELL`, the one short flag go-at invented, which older `at`
versions used differently.

## Terminal safety

While `at>` is active the terminal is in raw mode. It is restored on normal
exit, Ctrl-C/Ctrl-D, SIGTERM/SIGHUP/SIGINT/SIGQUIT, and any error exit. Only
SIGKILL can leave it raw; run `stty sane` or `reset` if that happens.

## Known limits

- Long input lines scroll horizontally instead of wrapping.
- Character widths use a built-in approximation of `wcwidth` (CJK and emoji are
  2 columns, combining marks 0); unusual scripts and ZWJ emoji sequences can
  still misplace the cursor. Tab does nothing (no completion).
- Process verification before `-k` uses Linux `/proc` start times and fails
  closed; elsewhere only the weaker group-leader check exists (see above).
- Linux is tested. macOS cross-compiles but is untested; no BSD support.
