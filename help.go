package main

// Printed by -h / --help on any of the go-* commands. The bare "go-at" with no
// arguments prints the shorter atUsage instead.
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
  -f FILE             read the commands from FILE
  -s SHELL            shell that runs the job (default bash, or $AT_SHELL)

CHANGE     go-at -e, --edit JOB
  Edit a queued job's commands in $VISUAL (else $EDITOR). The time, name and
  captured environment are kept. Refused once the job is running. If the job
  starts while you are editing, nothing is applied and your text is saved to
  <spool>/edit-<id>.rejected.

LIST       go-at -l | go-atq [-a|--all] [--json]
  Table of ID, NAME, STATE, WHEN, DETAIL. States are plain words: queued,
  waiting (due, held back by --batch), running, and with --all: finished,
  failed, terminated, interrupted. --json is for scripts. A running job's
  DETAIL shows its pid/process group.

INSPECT    go-at -c JOB      print the script of a queued or running job
           go-at --log JOB   print a job's output (also kept after it ends)

STOP       go-atrm JOB...                  remove queued jobs (go-at -d / -r do the same)
           go-atrm -k [-s SIGNAL] JOB...   signal running jobs' whole process
                                           group (default TERM; -s KILL, 9...)
  The pid is checked against the recorded start time first, so a recycled
  pid is never signalled.

DAEMON     go-atd [-d]
  Runs the jobs. Foreground by default (use that under supervisord/systemd);
  -d backgrounds it and does nothing if one is already running, so it is safe
  in a cron watchdog. AT_AUTOSTART=1 makes go-at start it when none is running.
  Runs as the user who starts it and only handles that user's jobs.

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

ENVIRONMENT
  AT_SPOOL         spool directory (default $XDG_STATE_HOME/at, else
                   ~/.local/state/at)
  AT_SHELL         default job shell (default bash)
  AT_BATCH_LOAD    load average below which --batch jobs start (default 1.5)
  AT_KEEP_DAYS     days of finished-job history to keep (default 30)
  AT_AUTOSTART     1 = start go-atd automatically if it is not running
  VISUAL, EDITOR   editor for --compose and --edit
  Inside a job: AT_JOB_ID and, if named, AT_JOB_NAME.

Classic -q queue letters are not used: use --nice and --batch.
`
