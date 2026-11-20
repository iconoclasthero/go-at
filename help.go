package main

// Printed by --HELP on any of the go-* commands. -h / --help, and the error for
// a command that was not understood, print the short atUsage instead.
const helpText = `go-at: at, atq, atrm, batch and atd in one program.

SCHEDULE   go-at [options] TIME...
  Commands come from the at> prompt (a real line editor: arrows, Home/End,
  history), from a pipe, from -f FILE, or from your editor with --compose.
  Jobs run in bash with the environment, working directory and umask you had
  when you submitted them.

  -n, --name NAME     give the job a unique name (starts with a letter; then
                      letters, digits, . _ -; up to 64). Names work anywhere a
                      job number does.
      --nice N        run at lower priority, N = 0..19 (never raises priority)
  -b, --batch         wait until system load is low (AT_BATCH_LOAD, default
                      1.5) before starting; TIME may then be left out
  -C, --compose       write the commands in $VISUAL (else $EDITOR) instead of
                      at>; saved text gets a bash -n syntax check
  -f FILE             read the commands from FILE; with --compose, FILE is the
                      starting text in the editor (a template; FILE is not changed)
      --config FILE   use this config file (any go-* command)
      --show-config   print the effective settings and where each came from
  -V, --version       print the version: commit, build time, static or dynamic
  -s SHELL            shell that runs the job (default bash, or $AT_SHELL)

CHANGE     go-at -e, --edit JOB
  Edit a queued job's commands in $VISUAL (else $EDITOR). The time, name and
  captured environment are kept. Refused once the job is running. If the job
  starts while you are editing, nothing is applied and your text is saved to
  <spool>/edit-<id>.rejected.
           go-at -R, --reschedule JOB [TIME...]
  Change when a queued job runs. TIME is anything go-at accepts (tomorrow 9am,
  2026-11-11 11:00 + 2h 55m). Without TIME the start time opens in your editor
  above a line saying everything below it is discarded.
           go-at --rename JOB NEWNAME
  Name an unnamed job, or rename one (same rules as --name). Ended jobs can be
  renamed too, which is how a finished job is made easy to find. A name an
  active job has is refused, or on a terminal you are offered NAME-<job number>
  or a new name. Without JOB, on a terminal, you choose the job.
  All of these keep the job's number, environment and commands.

COPY      go-at --copy JOB-or-FILE [--no-edit] [-n NAME] TIME...
  A new job from the commands of a queued, running or ended job (a name or
  number), or from a file (anything with a "/" in it, or starting with "~").
  It runs in the current environment and directory, keeps --nice, --batch and
  the shell of the original (--nice and -s override), and needs a TIME. Your
  editor opens on the commands first; --no-edit skips that.

SHOW      go-at --show [JOB]      go-atq --browse
  One job in full: times, result, options, commands and the end of its output.
  --browse picks from all jobs (queued and ended) and shows the one you choose.

NAMES     --latest
  A name that several ended jobs share is not guessed at: the command fails and
  lists them (use a number). --latest takes the newest, for scripts. Active
  names are unique and always win.

PICKER    setting picker = auto | fzf | menu | off
  When a command needs a job and none was named, and you are at a terminal:
  fzf if it is installed (with a preview that runs go-at --show), else a
  numbered menu. go-atrm lets you mark several. "off" never asks. Scripts are
  never asked anything: the command fails and lists the choices.

LIST      go-at -l | go-atq [-a|--all] [--json]
  Table of ID, NAME, STATE, WHEN, DETAIL. States are plain words: queued,
  waiting (due, held back by --batch), running, and with --all: finished,
  failed, terminated, interrupted. --json is for scripts. A running job's
  DETAIL shows its pid/process group.

INSPECT    go-at -c JOB      print a job's script (ended jobs: the commands you typed)
           go-at --log JOB   print a job's output (also kept after it ends)
           go-at --show JOB  everything about one job: times, result, commands, output end

STOP       go-atrm JOB...                  remove queued jobs (go-at -d / -r do the same)
           go-atrm -k [-s SIGNAL] JOB...   signal running jobs' whole process
                                           group (default TERM; -s KILL, 9...);
                                           long forms: --kill, --signal
  The pid is checked against the recorded start time first, so a recycled
  pid is never signalled.

DAEMON     go-atd [-d]
  Runs the jobs. Foreground by default (use that under supervisord/systemd);
  -d backgrounds it and does nothing if one is already running, so it is safe
  in a cron watchdog. AT_AUTOSTART=1 makes go-at start it when none is running.
  Runs as the user who starts it and only handles that user's jobs.
  Every line it logs starts with a timestamp (ISO 8601 like date -Is, see
  log_time_format); so do the start and exit markers go-at writes into a job's log.

TIME EXAMPLES
  go-at 17:30                          go-at 9:30:15 tomorrow
  go-at noon                           go-at next friday
  go-at now + 2 hours 55 minutes       go-at +2h55m       go-at in 90 seconds
  go-at 11-25-2026 09:30 + 2 hours 55 minutes
  go-at sunday 9:30 am ET + 1 hour     go-at Nov 25 2026 9am
  go-at -n steelers-arr 2026-10-25 09:30 + 2 hours 55 minutes
  Date forms: MM-DD-YYYY, MM/DD/YYYY, YYYY-MM-DD, DD.MM.YYYY, "nov 25 2026",
  weekdays, today, tomorrow. Zones: utc, et, ct, mt, pt or IANA names
  (America/New_York). Offsets mix units freely: seconds, minutes, hours, days,
  weeks, months, years. Hours and smaller are elapsed time; days and larger
  keep the wall-clock time.

CONFIGURATION
  Priority: command-line flag, then environment variable, then config file,
  then default. The config file is --config FILE, else $AT_CONFIG, else
  $XDG_CONFIG_HOME/go-at.conf (~/.config/go-at.conf). Lines are "key = value";
  # starts a comment. go-at --show-config prints every setting and its source.

  config key     environment      default
  spool          AT_SPOOL         $XDG_STATE_HOME/at, else ~/.local/state/at
  shell          AT_SHELL         bash
  autostart      AT_AUTOSTART     no   (go-at starts go-atd if it is not running)
  batch_load     AT_BATCH_LOAD    1.5  (load average below which --batch starts)
  keep_days      AT_KEEP_DAYS     30   (history, logs and saved commands)
  keep_commands  AT_KEEP_COMMANDS yes  (save commands of ended jobs; go-at -c)
  picker         AT_PICKER        auto (auto, fzf, menu or off; see PICKER)
  time_format    AT_TIME_FORMAT   12h  (am/pm; or 24h, or % sections, see below)
  log_time_format AT_LOG_TIME_FORMAT iso (stamp of every daemon log line and job-log
                                     marker; iso = what "date -Is" prints, or % sections)

  time_format: "12h" (Sun Nov 15 11:15:00 PM 2026), "24h" (Sun Nov 15 23:15:00
  2026) or a format of % sections as in date(1). Config file or environment
  only, there is no command-line option. Example: time_format = %a %b %e %l:%M %p
    %a Sun  %A Sunday  %b Nov  %B November  %d 05  %e " 5"  %m 11  %y 26  %Y 2026
    %H 23  %k " 3"  %I 11  %l "11"  %M 15  %S 00  %p AM/PM  %P am/pm  %Z EST
    %z -0500  %F 2026-11-15  %T 23:15:00  %R 23:15  %r 11:15:00 PM  %j yearday  %% %
    %:z -05:00  %s epoch seconds  %N nanoseconds  %D 11/15/26  %-d %-m %-I... unpadded
  log_time_format takes the same sections; "iso" is %FT%T%:z, e.g. 2026-11-20T05:24:26-05:00.

ENVIRONMENT
  AT_CONFIG        path of the config file
  VISUAL, EDITOR   editor for --compose, --edit, --copy and --reschedule
  Inside a job: AT_JOB_ID and, if named, AT_JOB_NAME.

Classic -q queue letters are not used: use --nice and --batch.
`
