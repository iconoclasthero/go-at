# go-at 🐐

A from-scratch Go take on `at`: schedule a command to run later. One binary,
installed under five names that never collide with the system `at`:

    go-at     schedule a job                  go-atrm   remove or stop jobs
    go-atq    list jobs                       go-batch  schedule a job that waits for low load
    go-atd    the daemon that runs them

No dependencies, no root, no setuid. This is **not a drop-in replacement** for
`at`; see [Compatibility](docs/compatibility.md).

What's different, on purpose:

1. **Jobs run in bash** by default (`-s SHELL` or `$AT_SHELL` to change it).
2. **`at>` has a real line editor**: arrows, Home/End, word jumps, Ctrl-K/U/W/Y,
   history. Ctrl-D ends input, Ctrl-C cancels.
3. **Seconds** work everywhere: `9:30:15`, `+ 90 seconds`.
4. **Mixed-unit offsets**: `go-at 11-25-2026 09:30 + 2 hours 55 minutes`.
5. **Plain words, not codes.** States are `queued`, `running`, `finished`...
   Jobs can have **names** (`--name <job name>`). You can see a running job's **process group** and
   **stop it** without having captured `$$`.

## Install

    git clone https://github.com/iconoclasthero/go-at && cd go-at
    make test                             # go vet + unit and end-to-end tests
    make install                          # ~/.local/bin/{go-at,go-atq,go-atrm,go-batch,go-atd}
    make static                           # a static binary that needs no libc

Needs Go **1.24.7 or newer** to build. Make sure `~/.local/bin` is on your
`PATH`. No Go on the machine that runs it, a prebuilt binary from GitHub, cross
compiling, and upgrading a running daemon are in
[docs/building.md](docs/building.md). `go-at --version` says exactly what you
have.

## Example: wait for something, then poll until it appears

    $ go-at --name task-name 2026-10-25 09:30 + 2 hours 55 minutes
    at Sun Oct 25 12:25:00 2026 EDT
    at> until task-name; do sleep 60; ((++i)); printf %s\\n "$i"; done
    at> <Ctrl-D>
    job 10 (task-name) at Sun Oct 25 12:25:00 2026 EDT

    $ go-atq
    ID  NAME          STATE    WHEN                              DETAIL
    10  task-name  running  started Sun Oct 25 12:25:00 2026  pgid 48213

    $ go-atrm -k task-name         # stops the loop and its sleep
    sent SIGTERM to job 10 (task-name); process group 48213

    $ go-atq --all
    ID  NAME          STATE       WHEN                            DETAIL
    10  task-name  terminated  ended Sun Oct 25 14:02:11 2026  SIGTERM, ran 1h37m11s

    $ go-at --log task-name        # everything the job printed

## Everyday commands

    go-at -n NAME TIME          schedule (type commands at at>, or pipe, -f FILE, --compose)
    go-at --copy JOB TIME       new job from an old one (or a file)
    go-atq [-a]                 list jobs (-a adds ended ones); go-atq --browse to look around
    go-at --show JOB            one job in full: times, result, commands, output
    go-at --log JOB             its output          go-at -c JOB   its script
    go-at -e JOB                edit commands       go-at -R JOB   change the start time
    go-at --rename JOB NAME     name or rename a job, ended ones too
    go-atrm JOB...              remove queued jobs  go-atrm -k JOB   stop a running one
    go-atd [-d]                 the daemon that runs the jobs

`JOB` is a number or a name; leave it out at a terminal and you choose from a
list (fzf if you have it). `go-at --help` is a one-screen reminder and
`go-at --HELP` the complete reference.

## Documentation

| | |
|---|---|
| [Building and installing](docs/building.md) | static builds, prebuilt binaries, no Go on the target, upgrading |
| [Jobs](docs/jobs.md) | states, names and numbers, finding and stopping a running job, priority and batch |
| [Writing, editing, copying, choosing](docs/editing.md) | `--compose`, `--edit`, `--reschedule`, `--rename`, `--copy`, the picker |
| [Time specs](docs/time-specs.md) | everything accepted as a time |
| [The daemon](docs/daemon.md) | running it under supervisord or systemd, stopping it, crashes |
| [Files and settings](docs/configuration.md) | the spool, the config file, every setting, the time format |
| [Compatibility, safety, limits](docs/compatibility.md) | how it differs from `at`, what is missing |
