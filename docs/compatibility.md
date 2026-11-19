# Compatibility, safety and limits

How go-at differs from the classic `at`, what is still missing, and its known limits.

[← back to the README](../README.md)

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
| `-V` | print the version | `-V`, `--version` | done |
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
