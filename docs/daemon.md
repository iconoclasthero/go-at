# Running the daemon

go-atd: starting it, supervising it, stopping it, and what happens after a crash.

[← back to the README](../README.md)

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
marker only after the last step. The commands of an interrupted job are kept in
`<spool>/history/<id>.sh`.
