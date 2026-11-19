package main

// go-at --show JOB   everything about one job: when, how it ended, its
//                    commands and the end of its output
// go-at --browse     choose a job from all of them (queued, running, ended)
//                    with the same details shown beside the list when fzf is
//                    used, then print them
//
// The commands are only the ones you typed. The environment captured with a
// queued job is never shown here (it can hold secrets), and ended jobs do not
// keep it at all.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const showLogLines = 15

func (s *Spool) showJob(j Job) {
	w := os.Stdout
	fmt.Fprintf(w, "Job %s: %s\n", j.label(), j.State)
	row := func(k, v string) { fmt.Fprintf(w, "  %-10s %s\n", k, v) }
	when := func(t time.Time) string { return fmtWhenZone(t.Local()) }
	if !j.Scheduled.IsZero() {
		row("scheduled", when(j.Scheduled))
	}
	if !j.Created.IsZero() {
		row("created", when(j.Created))
	}
	if j.Started != nil {
		row("started", when(*j.Started))
	}
	if j.Ended != nil {
		ended := when(*j.Ended)
		if j.Started != nil {
			ended += "  (ran " + j.Ended.Sub(*j.Started).Round(time.Second).String() + ")"
		}
		row("ended", ended)
	}
	switch {
	case j.Signal != "":
		row("result", "killed by "+j.Signal)
	case j.ExitCode != nil:
		row("result", fmt.Sprintf("exit %d", *j.ExitCode))
	}
	opts := []string{fmt.Sprintf("nice %d", j.Nice)}
	if j.Batch {
		opts = append(opts, "batch")
	}
	if j.Shell != "" {
		opts = append(opts, "shell "+j.Shell)
	}
	row("options", strings.Join(opts, ", "))
	if j.Log != "" {
		row("output", j.Log)
	}

	fmt.Fprintln(w, "Commands:")
	cmds, err := s.jobCommands(j)
	if err != nil {
		fmt.Fprintf(w, "  (%v)\n", err)
	} else {
		indent(w, cmds)
	}
	if lines := tailLines(j.Log, showLogLines); len(lines) > 0 {
		fmt.Fprintf(w, "Output (last %d lines):\n", len(lines))
		for _, l := range lines {
			fmt.Fprintf(w, "  %s\n", l)
		}
	}
}

func indent(w io.Writer, text string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		fmt.Fprintln(w, "  (none)")
		return
	}
	for _, l := range strings.Split(text, "\n") {
		fmt.Fprintf(w, "  %s\n", l)
	}
}

// jobCommands is the commands part of a job: from its script while it is
// queued or running, from the saved copy once it has ended.
func (s *Spool) jobCommands(j Job) (string, error) {
	if j.path != "" {
		b, err := os.ReadFile(j.path)
		if err != nil {
			return "", fmt.Errorf("cannot read the job: %v", err)
		}
		if _, cmds, ok := splitJobScript(string(b)); ok {
			return cmds, nil
		}
		return "", fmt.Errorf("the job file has no command section")
	}
	b, err := os.ReadFile(s.commandsPath(j.ID))
	if err != nil {
		return "", fmt.Errorf("the commands were not kept (keep_commands is off, or the job ended before they were saved)")
	}
	return string(b), nil
}

// tailLines is the last n lines of a file, reading only its end.
func tailLines(path string, n int) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const window = 64 * 1024
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := int64(0)
	if st.Size() > window {
		off = st.Size() - window
	}
	f.Seek(off, io.SeekStart)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var lines []string
	first := true
	for sc.Scan() {
		if first && off > 0 { // the window starts mid-line
			first = false
			continue
		}
		first = false
		lines = append(lines, sc.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (s *Spool) browse() {
	if !canPick() {
		die("--browse needs a terminal (and the picker setting must not be off)")
	}
	cands, err := s.candidates(anyJob)
	if err != nil {
		die("%v", err)
	}
	got, err := pickJobs(cands, "job", false)
	if err != nil {
		fatal(err)
	}
	s.showJob(got[0])
}
