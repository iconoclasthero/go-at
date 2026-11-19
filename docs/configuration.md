# Files and settings

Where go-at keeps things, and every setting (config file, environment, defaults).

[← back to the README](../README.md)

## Files and settings

Per-user spool: `spool` setting / `$AT_SPOOL`, else `$XDG_STATE_HOME/at`, else
`~/.local/state/at`.

    jobs/<id>          queued job: a plain bash script (go-at -c shows it)
    running/<id>       the same script while it runs, plus <id>.pid and <id>.proc
    history/<id>.json  outcome record of an ended job
    history/<id>.sh    the commands you typed, kept after the job ends (0600)
    logs/<id>.log      everything the job printed, with start/finish markers

`go-at -c JOB` prints the full script of a queued or running job, and for an
ended job the saved commands. Only your commands are kept after a job ends,
**never the captured environment**, which can hold secrets (set
`keep_commands = no` to keep nothing). They are removed with the rest of the
job's record after `keep_days`.

### Configuration

Settings come from, highest priority first: a command-line flag where one
exists (`-s SHELL`), the **environment**, the **config file**, then the default.

The config file is the first of:

1. `--config FILE` (any go-* command; must exist)
2. `$AT_CONFIG` (must exist)
3. `$XDG_CONFIG_HOME/go-at.conf`, else `~/.config/go-at.conf` (optional)

Format: `key = value`, one per line; `#` starts a comment; values may be quoted;
a leading `~/` is your home directory. Unknown keys and bad values are errors
with the line number, so a typo is never silently ignored. See
`go-at.conf.example`. `go-at --show-config` prints every setting, its value and
where it came from.

| config key | environment variable | default |
| :- | :- | :- |
| `spool` | `AT_SPOOL` | `$XDG_STATE_HOME/at`, else `~/.local/state/at` |
| `shell` | `AT_SHELL` | `bash` |
| `autostart` | `AT_AUTOSTART` | `no` (`go-at` starts the daemon when none runs) |
| `batch_load` | `AT_BATCH_LOAD` | `1.5` (0 holds batch jobs until raised) |
| `keep_days` | `AT_KEEP_DAYS` | `30` (records, logs and saved commands of ended jobs) |
| `keep_commands` | `AT_KEEP_COMMANDS` | `yes` |
| `time_format` | `AT_TIME_FORMAT` | `12h` (am/pm; `24h`, or a custom format, see below) |

#### Time format

Times in `go-atq` and in messages are 12-hour with AM/PM by default:
`Sun Nov 15 11:15:00 PM 2026`. Set `time_format = 24h` for the old 24-hour
look, or build your own from `%` sections, as in `date(1)`:

    time_format = %a %b %e %l:%M %p          # Sun Nov 15 11:15 PM
    time_format = %Y-%m-%d %I:%M:%S %P       # 2026-11-15 11:15:00 pm
    time_format = %d.%m.%Y %H:%M             # 15.11.2026 23:15

| | | | |
| :- | :- | :- | :- |
| `%a` Sun | `%A` Sunday | `%b` Nov | `%B` November |
| `%d` 05 | `%e` ` 5` | `%m` 11 | `%y` 26 |
| `%Y` 2026 | `%j` day of year | `%H` 23 | `%k` ` 3` (24h, space-padded) |
| `%I` 03 | `%l` ` 3` (12h, space-padded) | `%M` 05 | `%S` 09 |
| `%p` AM/PM | `%P` am/pm | `%Z` EST | `%z` -0500 |
| `%F` 2026-11-15 | `%T` 23:15:00 | `%R` 23:15 | `%r` 11:15:00 PM |
| `%%` a `%` | `%n` newline | `%t` tab | |

An unknown section is an error with the line number. The format only affects
how times are displayed: `--json` output and the way you type times are
unchanged. It is a config-file (or `AT_TIME_FORMAT`) setting only; there is no
command-line option.

Yes/no values accept yes/no, true/false, on/off, 1/0. `AT_CONFIG` is the one
variable with no config key (it names the file). Inside every job, `AT_JOB_ID`
and `AT_JOB_NAME` are set. The daemon reads the same file, so put `spool` there
(or set `AT_SPOOL` for it) and your shell and the daemon always agree.
