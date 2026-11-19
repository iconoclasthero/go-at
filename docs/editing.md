# Writing, editing, copying and choosing jobs

The editor workflow (`--compose`), changing a queued job, copying a job, and how a job is chosen when you do not name one.

[← back to the README](../README.md)

## Writing and editing commands in your editor

    go-at --compose --name nightly 02:00      # also -C: write the commands in $VISUAL
    go-at --compose -f ~/tpl/job.sh -n nightly 02:00   # start the editor from a file (a template)
    go-at --edit task-name                 # also -e: change a queued job (name or number)

Both use `$VISUAL`, else `$EDITOR` (the value may include arguments, e.g.
`EDITOR="nano -l"`). If neither is set you get a message saying so, with an
example (`VISUAL=/usr/bin/nano go-at --compose ...`); there is no fallback
editor. The interactive `at>` prompt tells you about `--compose` when you start.

- **Edit** shows only your commands; the time, name, environment and working
  directory captured at submission are kept. Quitting without changes prints
  "no changes"; an empty result or an editor that exits non-zero leaves the job
  untouched. Only queued/waiting jobs can be edited. If the job starts while
  you are in the editor, nothing is applied and your text goes to
  `<spool>/edit-<id>.rejected`. To change the run time use `--reschedule`
  (below).
- **Syntax check:** after you save, the commands get a `bash -n` check. At a
  terminal you are asked `[e]dit again, [s]ave anyway, [a]bort`; without a
  terminal the text is not applied and is kept in `<spool>/edit-<id>.rejected`
  (or `compose.rejected`). Only syntax is checked, not whether commands exist.
- The temp file lives in a private (0700) directory and is removed afterwards.
- Tested against real nano 7.2 on a pty.

## Changing the start time and the name of a queued job

    go-at --reschedule nightly tomorrow 9am         # also -R
    go-at -R 17 11-25-2026 09:30 + 2 hours 55 minutes
    go-at --reschedule nightly                      # opens the time in $VISUAL / $EDITOR
    go-at --rename 17 nightly                       # give an unnamed job its first name
    go-at --rename nightly weekly-backup            # rename a named one

- **`--reschedule JOB [TIME...]`** takes the same time specs as scheduling
  does, including offsets (`+ 2h 55m`), and a time in the past is refused. With
  no `TIME` the current start time opens in your editor above a
  `everything below this line is discarded` line (like `systemctl edit`); type
  the new time over it. Leaving it alone prints "no changes".
- **`--rename JOB NEWNAME`** follows the naming rules of `--name` (a letter
  first, then letters, digits, `.` `_` `-`) and a name that another queued or
  running job has is refused. `AT_JOB_NAME` inside the job is updated too.
- Both keep the job's number, environment and commands: nothing is removed and
  re-created. Only queued/waiting jobs can be changed; if a job starts first,
  nothing is applied (the same locking as `--edit`).

Ended jobs can be renamed too (`go-at --rename 27 W11-Eagles`), which is how a
finished job is made easy to find. If you give the arguments the wrong way round
(`--rename W11-Eagles 27`) go-at says so and shows the right order.

## Copying a job, and looking at jobs

    go-at --copy backup tomorrow 3am               # commands of job "backup", opens your editor
    go-at --copy 41 --no-edit -n again +2h         # as is
    go-at --copy ~/jobs/weekly.sh 9am sunday       # from a file

- **`--copy JOB-or-FILE TIME...`** makes a new job from the commands of a
  queued, running or ended job (ended jobs need `keep_commands`, the default),
  or from a file. It is a path when it has a `/` in it or starts with `~`;
  otherwise it is a job number or name. The copy runs in the *current*
  environment and directory (not the original's), keeps `--nice`, `--batch`
  and the shell of the original (give `--nice` or `-s` to override), needs a
  `TIME`, and opens your editor on the commands unless `--no-edit`. It cannot
  be combined with `-f` or `--compose`.
- **`--show [JOB]`** prints one job in full: state, times, result, options, its
  commands and the end of its output.
- **`go-atq --browse`** (or `go-at --browse`) lets you pick from all jobs,
  queued and ended, and shows the one you choose.

### Choosing a job

When a command that needs a job gets none (`go-at --show`, `-c`, `--log`,
`--edit`, `--reschedule`, `--rename NEWNAME`, `go-atrm`) and you are at a
terminal, you choose. The `picker` setting says how: `auto` (default: `fzf` if
it is installed, else a numbered menu), `fzf`, `menu`, or `off`. With fzf the
right-hand pane previews the job by running `go-at --show`; `go-atrm` can mark
several (tab). fzf is only ever run as a separate program: nothing of it is in
go-at. In a script (no terminal) nothing is asked and the command fails.
