# Jobs: states, names, stopping, priority

What a job is, what its states mean, how names and numbers work, and how to find and stop a running job.

[← back to the README](../README.md)

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
Anywhere a job is expected you can use either: `go-atrm task-name`,
`go-at -c 10`. Names start with a letter (so they can't be mistaken for numbers
or options), may contain letters, digits, `.`, `_`, `-`, and must be unique
among queued and running jobs.

Once a job has ended its name is free again, so a recurring job can keep one
name (`backup`) and the history can hold many jobs called `backup`. A name that
an active job has always means that job. A name that several *ended* jobs share
is never guessed at: on a terminal you choose from them, anywhere else the
command fails with the list and a non-zero exit, and you give a number or
`--latest` (the newest):

    go-at --log --latest backup

Giving a new job (or renaming one) a name an active job has is refused; at a
terminal you are offered `NAME-<job number>` or a new name instead. In a
script it is always an error: `go-at -n NAME` never adds a suffix by itself.

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

## Priority and batch

- `--nice N` (0 to 19): run at lower priority; shown as `nice=N`.
- `--batch` (or `go-batch`): hold the job until the 1-minute load average is
  below 1.5 (`$AT_BATCH_LOAD`); until then it reads `waiting`.

There are no queue letters.
