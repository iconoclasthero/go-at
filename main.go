// go-at: at, atq, atrm, batch and atd in one binary. The command is chosen by
// the program name (go-at, go-atq, go-atrm, go-batch, go-atd; the same names
// without "go-" work if you symlink them) or by the first argument:
// `go-at atq`.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"
)

// The terminal is put in raw mode while at> is active. Every way out of the
// program must put it back, so the restore function lives here where die(),
// the signal handler and the normal return path can all reach it.
var (
	termMu      sync.Mutex
	termRestore func()
)

func setTermRestore(f func()) {
	termMu.Lock()
	termRestore = f
	termMu.Unlock()
}

func restoreTerm() {
	termMu.Lock()
	f := termRestore
	termRestore = nil
	termMu.Unlock()
	if f != nil {
		f()
	}
}

func die(format string, a ...any) {
	restoreTerm() // before printing: raw mode has no \n -> \r\n translation
	fmt.Fprintf(os.Stderr, "go-at: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	name := strings.TrimPrefix(filepath.Base(os.Args[0]), "go-")
	args := os.Args[1:]
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--version" || a == "-V" || a == "-version" {
			fmt.Println(versionString())
			return
		}
		if a == "-h" || a == "--help" || a == "-help" {
			io.WriteString(os.Stdout, helpText) // not fmt.Print: the text contains % sections
			return
		}
	}
	args, cfgFlag, err := extractConfigFlag(args)
	if err != nil {
		die("%v", err)
	}
	configFlagPath = cfgFlag
	if err := loadConfig(cfgFlag); err != nil {
		die("%v", err)
	}
	if v := os.Getenv("AT_TIME_FORMAT"); v != "" { // the config file's value was checked on load
		if err := checkTimeFormat(v); err != nil {
			die("AT_TIME_FORMAT: %v", err)
		}
	}
	if strings.TrimPrefix(filepath.Base(os.Args[0]), "go-") == "at" && len(args) == 1 && args[0] == "--show-config" {
		showConfig()
		return
	}
	switch name {
	case "at", "atq", "atrm", "batch", "atd":
	default:
		name = "at"
	}
	if name == "at" && len(args) > 0 {
		switch strings.TrimPrefix(args[0], "go-") {
		case "atq", "atrm", "batch", "atd":
			name, args = strings.TrimPrefix(args[0], "go-"), args[1:]
		}
	}
	switch name {
	case "atq":
		cmdList(args)
	case "atrm":
		cmdRemove(args)
	case "atd":
		cmdDaemon(args)
	case "batch":
		cmdAt(append([]string{"--batch"}, args...))
	default:
		cmdAt(args)
	}
}

func openSpool() *Spool {
	s, err := OpenSpool()
	if err != nil {
		die("cannot open spool: %v", err)
	}
	return s
}

func resolveShell(flagVal string) string {
	for _, c := range []string{flagVal, cfg("AT_SHELL"), "bash"} {
		if c == "" {
			continue
		}
		p, err := exec.LookPath(c)
		if err != nil {
			die("shell %q not found: %v", c, err)
		}
		abs, _ := filepath.Abs(p)
		return abs
	}
	return ""
}

const atUsage = `usage: go-at [options] TIME...        schedule commands (typed at the at> prompt, piped in, or -f FILE)
    -n, --name NAME     name the job (starts with a letter; letters, digits, . _ -)
        --nice N        run at lower priority, N = 0..19
    -b, --batch         wait for low system load before starting (same as go-batch)
    -s SHELL            shell that runs the job (default bash, or $AT_SHELL)
    -f FILE             read the commands from FILE
        --copy SRC      new job from the commands of job or file SRC (needs TIME; opens your editor)
        --no-edit       with --copy: don't open the editor

       go-at -e, --edit JOB                     edit a queued job's commands in $EDITOR
       go-at -R, --reschedule JOB [TIME...]     change when a queued job runs (no TIME: edit it in $EDITOR)
       go-at --rename JOB NEWNAME               name or rename a queued or ended job
       go-at --show [JOB]                       one job in full: times, result, commands, output
       go-at --browse | go-atq --browse         pick from all jobs, queued and ended, and show it
       go-at -l | go-atq [-a|--all] [--json]   list jobs: queued, waiting, running (--all adds ended ones)
       go-at -c JOB                             print the script of a queued or running job
       go-at --log JOB                          print a job's output
       go-atrm JOB...                           remove queued jobs
       go-atrm -k [-s SIGNAL] JOB...            stop running jobs (default signal TERM)
       go-atd [-d]                              the daemon that runs jobs (-d: in the background)

JOB is a number or a name. A name that several ended jobs share needs a number, or
--latest for the newest. Without JOB, on a terminal, you choose (picker: see --show-config).

TIME examples:
  go-at 17:30                          go-at 9:30:15 tomorrow        go-at noon
  go-at now + 2 hours 55 minutes       go-at +2h55m                  go-at in 90 seconds
  go-at 11-25-2026 09:30 + 2 hours 55 minutes
  go-at sunday 9:30 am ET + 1 hour     go-at Nov 25 2026 9am         go-at next friday
Jobs only run while go-atd is running: start it with "go-atd -d" (background)
or "go-atd" (foreground). Set AT_AUTOSTART=1 to have go-at start it when it
finds none.
`

func cmdAt(args []string) {
	fs := flag.NewFlagSet("go-at", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, atUsage) }
	var name, file, shellFlag string
	var nice int
	var batch, list, rm, rm2, cat, all, asJSON, showLogFlag, edit, compose, resched, rename, show, browse, copyJob, noEdit, latest bool
	fs.StringVar(&name, "n", "", "")
	fs.StringVar(&name, "name", "", "")
	fs.IntVar(&nice, "nice", 0, "")
	fs.BoolVar(&batch, "b", false, "")
	fs.BoolVar(&batch, "batch", false, "")
	fs.StringVar(&file, "f", "", "")
	fs.StringVar(&shellFlag, "s", "", "")
	fs.BoolVar(&list, "l", false, "")
	fs.BoolVar(&rm, "d", false, "")
	fs.BoolVar(&rm2, "r", false, "")
	fs.BoolVar(&cat, "c", false, "")
	fs.BoolVar(&all, "a", false, "")
	fs.BoolVar(&all, "all", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	fs.BoolVar(&showLogFlag, "log", false, "")
	fs.BoolVar(&edit, "edit", false, "")
	fs.BoolVar(&edit, "e", false, "")
	fs.BoolVar(&compose, "compose", false, "")
	fs.BoolVar(&compose, "C", false, "")
	fs.BoolVar(&resched, "reschedule", false, "")
	fs.BoolVar(&resched, "R", false, "")
	fs.BoolVar(&rename, "rename", false, "")
	fs.BoolVar(&show, "show", false, "")
	fs.BoolVar(&browse, "browse", false, "")
	fs.BoolVar(&copyJob, "copy", false, "")
	fs.BoolVar(&noEdit, "no-edit", false, "")
	fs.BoolVar(&latest, "latest", false, "")
	oldQueue := fs.String("q", "", "")
	fs.Parse(args)
	latestFlag = latest

	switch {
	case list && browse, browse:
		openSpool().browse()
		return
	case list:
		listJobs(all, asJSON)
		return
	case rm || rm2:
		removeJobs(fs.Args(), false, "TERM")
		return
	case cat:
		catJobs(fs.Args())
		return
	case showLogFlag:
		showLog(fs.Args())
		return
	case show:
		if len(fs.Args()) > 1 {
			die("usage: go-at --show [JOB]   (JOB is a number or a name)")
		}
		sp := openSpool()
		sp.showJob(sp.jobFor(optArg(fs.Args()), anyJob))
		return
	case edit:
		if len(fs.Args()) > 1 {
			die("usage: go-at --edit [JOB]   (JOB is a number or a name; without it you choose from the queue)")
		}
		sp := openSpool()
		msg, err := sp.EditJob(sp.jobFor(optArg(fs.Args()), queuedJob).ref())
		if err != nil {
			die("%v", err)
		}
		fmt.Fprintln(os.Stderr, msg)
		return
	case resched:
		sp := openSpool()
		var ref string
		var spec string
		if len(fs.Args()) > 0 {
			ref, spec = fs.Arg(0), strings.Join(fs.Args()[1:], " ")
		}
		msg, err := sp.RescheduleJob(sp.jobFor(ref, queuedJob).ref(), spec)
		if err != nil {
			die("%v", err)
		}
		fmt.Fprintln(os.Stderr, msg)
		return
	case rename:
		renameCmd(fs.Args())
		return
	}

	if copyJob && file != "" {
		die("--copy takes its commands from SRC; -f cannot be combined with it")
	}
	if copyJob && compose {
		die("--copy already opens the commands in your editor; --compose is not needed (use --no-edit to skip the editor)")
	}

	if *oldQueue != "" {
		die("queue letters are gone: use --nice N to lower priority and --batch to wait for low load")
	}
	if noEdit && !copyJob {
		die("--no-edit only applies to --copy")
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if nice < 0 || nice > 19 {
		die("--nice must be between 0 and 19 (go-at never raises priority)")
	}
	if name != "" && !nameRe.MatchString(name) {
		die("bad job name %q: start with a letter, then letters, digits, '.', '_' or '-' (up to 64 characters)", name)
	}

	sp := openSpool()
	rest := fs.Args()
	var src copySource
	if copyJob {
		if len(rest) < 2 {
			die("usage: go-at --copy [-n NAME] [--nice N] [-b] [-s SHELL] [--no-edit] SRC TIME...\n  SRC is a job (number or name) or a file (./name.sh, ~/x.sh, /path); TIME is when the copy runs")
		}
		var err error
		if src, err = sp.loadCopySource(rest[0]); err != nil {
			fatal(err)
		}
		rest = rest[1:]
		// the copy keeps the source's settings unless told otherwise
		if !set["nice"] {
			nice = src.nice
		}
		if !batch {
			batch = src.batch
		}
	}

	spec := strings.Join(rest, " ")
	if batch && spec == "" && !copyJob {
		spec = "now"
	}
	if strings.TrimSpace(spec) == "" {
		fs.Usage()
		os.Exit(1)
	}
	runAt, err := ParseSpec(spec, time.Now(), time.Local)
	if err != nil {
		die("%v", err)
	}

	// settle the name before the commands are typed, so that a clash costs nothing
	suffixID := false
	if name != "" {
		var err error
		if name, suffixID, err = sp.settleName(name, 0); err != nil {
			fatal(err)
		}
	}

	shellName := shellFlag
	if shellName == "" && src.shell != "" {
		if _, err := os.Stat(src.shell); err == nil {
			shellName = src.shell
		}
	}
	shell := resolveShell(shellName)
	var commands string
	switch {
	case copyJob:
		commands = src.commands
		if !noEdit {
			commands = composeCommands(sp, src.commands, shell)
		}
	case compose:
		// -f FILE with --compose: the file's text is the starting point in the editor
		initial := ""
		if file == "-" {
			die("--compose cannot read the commands from stdin (-f -); use -f FILE")
		}
		if file != "" {
			b, err := os.ReadFile(file)
			if err != nil {
				die("%v", err)
			}
			initial = string(b)
		}
		commands = composeCommands(sp, initial, shell)
	default:
		commands = readCommands(file, runAt)
	}
	if strings.TrimSpace(commands) == "" {
		die("no commands given; job not created")
	}
	id, name, err := sp.AddJob(Submission{RunAt: runAt, Name: name, Nice: nice, Batch: batch, Shell: shell, Commands: commands, SuffixID: suffixID})
	if err != nil {
		die("%v", err)
	}
	label := fmt.Sprint(id)
	if name != "" {
		label = fmt.Sprintf("%d (%s)", id, name)
	}
	fmt.Fprintf(os.Stderr, "job %s at %s\n", label, fmtWhenZone(runAt))
	if copyJob {
		fmt.Fprintf(os.Stderr, "  copied from %s\n", src.desc)
	}
	if !sp.DaemonRunning() {
		if cfgBool("AT_AUTOSTART", false) {
			if started, pid, err := detachDaemon(sp); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not start go-atd: %v\n", err)
			} else if started {
				fmt.Fprintf(os.Stderr, "started go-atd (pid %d)\n", pid)
			}
		} else {
			fmt.Fprintln(os.Stderr, "warning: go-atd is not running, so this job will not run until you start it (run: go-atd -d)")
		}
	}
}

// composeCommands opens initial in the editor and returns what was saved.
func composeCommands(sp *Spool, initial, shell string) string {
	editor, err := editorCommand("--compose")
	if err != nil {
		die("%v", err)
	}
	commands, err := editText(editor, initial, "go-at-new-job.sh", shell, sp.path("compose.rejected"))
	if err != nil {
		die("%v; job not created", err)
	}
	return commands
}

func optArg(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return ""
}

// renameCmd is go-at --rename JOB NEWNAME. With only the name, on a terminal,
// you choose the job.
func renameCmd(args []string) {
	sp := openSpool()
	var ref, name string
	switch len(args) {
	case 2:
		ref, name = args[0], args[1]
		// the arguments the wrong way round: "--rename W11-Eagles 27"
		if _, err := sp.Resolve(ref, resolveOpts{history: true, latest: true}); err != nil {
			if _, err2 := sp.Resolve(name, resolveOpts{history: true, latest: true}); err2 == nil {
				die("no such job: %s; did you mean:  go-at --rename %s %s", ref, name, ref)
			}
		}
	case 1:
		if !canPick() {
			die("usage: go-at --rename JOB NEWNAME   (JOB is a number or a name)")
		}
		name = args[0]
	default:
		die("usage: go-at --rename JOB NEWNAME   (JOB is a number or a name; with just NEWNAME you choose the job)")
	}
	if !nameRe.MatchString(name) {
		die("bad job name %q: start with a letter, then letters, digits, '.', '_' or '-' (up to 64 characters)", name)
	}
	j := sp.jobFor(ref, anyJob)
	if j.Active() {
		var err error
		if name, _, err = sp.settleName(name, j.ID); err != nil {
			fatal(err)
		}
	}
	msg, err := sp.RenameJob(j, name)
	if err != nil {
		die("%v", err)
	}
	fmt.Fprintln(os.Stderr, msg)
}

func readCommands(file string, runAt time.Time) string {
	if file != "" && file != "-" {
		b, err := os.ReadFile(file)
		if err != nil {
			die("%v", err)
		}
		return string(b)
	}
	stdin := int(os.Stdin.Fd())
	if file == "" && isTerminal(stdin) && isTerminal(int(os.Stderr.Fd())) {
		cmds, cancelled := readInteractive(runAt)
		if cancelled {
			fmt.Fprintln(os.Stderr, "go-at: cancelled; job not created")
			os.Exit(130)
		}
		return cmds
	}
	b, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		die("%v", err)
	}
	return string(b)
}

// readInteractive prompts with at> and a real line editor. End with Ctrl-D.
// Raw mode is held for the whole session and undone by defer on normal return
// or panic, by die(), and by the signal handler below. Only SIGKILL (which no
// program can catch) can leave the terminal raw; `stty sane` fixes that.
func readInteractive(runAt time.Time) (cmds string, cancelled bool) {
	fmt.Fprint(os.Stderr, "Press Ctrl-D to close and save; Ctrl-C to abort.\n")
	if ed := strings.TrimSpace(os.Getenv("VISUAL")); ed != "" {
		fmt.Fprintf(os.Stderr, "Restart with --compose to use %s.\n", ed)
	} else if ed := strings.TrimSpace(os.Getenv("EDITOR")); ed != "" {
		fmt.Fprintf(os.Stderr, "Restart with --compose to use %s.\n", ed)
	} else {
		fmt.Fprint(os.Stderr, "Restart with --compose to use editor set in \"$VISUAL\" then \"$EDITOR\".\n")
	}
	fmt.Fprintf(os.Stderr, "at %s\n", fmtWhenZone(runAt))
	restore, err := makeRaw(int(os.Stdin.Fd()))
	if err != nil {
		die("cannot set terminal mode: %v", err)
	}
	setTermRestore(restore)
	defer restoreTerm()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)
	defer signal.Stop(sig)
	go func() {
		if s, ok := <-sig; ok {
			restoreTerm()
			fmt.Fprintln(os.Stderr, "\ngo-at: interrupted; job not created")
			os.Exit(128 + int(s.(syscall.Signal)))
		}
	}()

	ed := NewEditor(os.Stdin, os.Stderr, func() int { return termCols(int(os.Stderr.Fd())) })
	var lines []string
	for {
		line, err := ed.ReadLine("at> ")
		if err == io.EOF {
			break
		}
		if err == errInterrupt {
			return "", true
		}
		if err != nil {
			die("%v", err)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n", false
}

// ---- listing ---------------------------------------------------------------

func cmdList(args []string) {
	fs := flag.NewFlagSet("go-atq", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: go-atq [-a|--all] [--json] [--browse]\n  lists queued, waiting and running jobs; --all adds jobs that have ended\n  --browse: choose from all jobs, with their commands and output shown beside the list\n")
	}
	var all, asJSON, browse bool
	fs.BoolVar(&all, "a", false, "")
	fs.BoolVar(&all, "all", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	fs.BoolVar(&browse, "browse", false, "")
	fs.Parse(args)
	if browse {
		openSpool().browse()
		return
	}
	listJobs(all, asJSON)
}

func describe(j Job) (when, detail string) {
	f := func(t time.Time) string { return fmtWhen(t.Local()) }
	var parts []string
	switch j.State {
	case stQueued:
		when = "runs " + f(j.Scheduled)
	case stWaiting:
		when = "due " + f(j.Scheduled)
		parts = append(parts, fmt.Sprintf("waiting for load below %.1f", batchLoadLimit()))
	case stRunning:
		if j.Started != nil {
			when = "started " + f(*j.Started)
		} else {
			when = "starting"
		}
		if j.Unverified {
			parts = append(parts, "process identity unverified; go-atrm -k will refuse")
		} else if j.PGID > 0 {
			parts = append(parts, fmt.Sprintf("pgid %d", j.PGID))
		}
	case stInterrupted:
		if j.Started != nil {
			when = "started " + f(*j.Started)
		} else {
			when = "was due " + f(j.Scheduled)
		}
		parts = append(parts, "daemon stopped while it was running; not re-run")
	default: // finished, failed, terminated
		if j.Ended != nil {
			when = "ended " + f(*j.Ended)
		}
		switch {
		case j.Signal != "":
			parts = append(parts, j.Signal)
		case j.ExitCode != nil:
			parts = append(parts, fmt.Sprintf("exit %d", *j.ExitCode))
		}
		if j.Started != nil && j.Ended != nil {
			parts = append(parts, "ran "+j.Ended.Sub(*j.Started).Round(time.Second).String())
		}
	}
	if j.Batch && j.Active() && j.State != stWaiting {
		parts = append(parts, "batch")
	}
	if j.Nice > 0 {
		parts = append(parts, fmt.Sprintf("nice=%d", j.Nice))
	}
	return when, strings.Join(parts, ", ")
}

// jobTable renders jobs as aligned lines under a header: the go-atq listing,
// and the rows of the picker and of the "which one?" messages. Empty for no jobs.
func jobTable(jobs []Job) []string {
	if len(jobs) == 0 {
		return nil
	}
	var buf strings.Builder
	tw := tabwriter.NewWriter(&buf, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATE\tWHEN\tDETAIL")
	for _, j := range jobs {
		name := j.Name
		if name == "" {
			name = "-"
		}
		when, detail := describe(j)
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", j.ID, name, j.State, when, detail)
	}
	tw.Flush()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines
}

func listJobs(all, asJSON bool) {
	sp := openSpool()
	q, err := sp.Queued()
	if err != nil {
		die("%v", err)
	}
	r, err := sp.Running()
	if err != nil {
		die("%v", err)
	}
	jobs := append(r, q...)
	sortJobs(jobs)
	if all {
		h, err := sp.History()
		if err != nil {
			die("%v", err)
		}
		jobs = append(jobs, h...)
	}

	if asJSON {
		if jobs == nil {
			jobs = []Job{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(jobs)
		return
	}
	for _, line := range jobTable(jobs) {
		fmt.Println(line)
	}
	overdue := 0
	for _, j := range q {
		if j.State == stQueued && time.Since(j.Scheduled) > 3*time.Second {
			overdue++
		}
	}
	if overdue > 0 && !sp.DaemonRunning() {
		fmt.Fprintf(os.Stderr, "warning: go-atd is not running and %d job(s) are overdue (start it with: go-atd -d)\n", overdue)
	}
}

// ---- removing and killing --------------------------------------------------

func cmdRemove(args []string) {
	fs := flag.NewFlagSet("go-atrm", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: go-atrm JOB...                    remove queued jobs\n       go-atrm -k [-s SIGNAL] JOB...     stop running jobs (default SIGTERM, sent to the whole process group)\n")
	}
	var kill bool
	var sig string
	fs.BoolVar(&kill, "k", false, "")
	fs.BoolVar(&kill, "kill", false, "")
	fs.StringVar(&sig, "s", "TERM", "")
	fs.StringVar(&sig, "signal", "TERM", "")
	fs.Parse(args)
	removeJobs(fs.Args(), kill, sig)
}

func removeJobs(refs []string, kill bool, sigStr string) {
	sig, err := parseSignal(sigStr)
	if err != nil {
		die("%v", err)
	}
	sp := openSpool()
	if len(refs) == 0 {
		if !canPick() {
			die("usage: go-atrm [-k] JOB...   (with no JOB, at a terminal, you choose from the queue)")
		}
		kind := activeJob
		if kill {
			kind = runningJob
		}
		cands, err := sp.candidates(kind)
		if err != nil {
			die("%v", err)
		}
		if len(cands) == 0 {
			die("there are no jobs to choose from")
		}
		got, err := pickJobs(cands, "job", true)
		if err != nil {
			fatal(err)
		}
		for _, j := range got {
			refs = append(refs, j.ref())
		}
	}
	rc := 0
	fail := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "go-at: "+format+"\n", a...)
		rc = 1
	}
	for _, ref := range refs {
		j, err := sp.Find(ref, true)
		if err != nil {
			fail("%v", err)
			continue
		}
		if kill {
			switch j.State {
			case stRunning:
				pid, err := sp.verifyJobPID(j.ID)
				if err != nil {
					fail("job %s: %v; nothing was signalled", j.label(), err)
					continue
				}
				// negative pid = the whole process group: the shell and everything it started
				if err := syscall.Kill(-pid, sig); err != nil {
					fail("job %s: cannot signal process group %d: %v", j.label(), pid, err)
					continue
				}
				fmt.Printf("sent %s to job %s; process group %d\n", sigName(sig), j.label(), pid)
			case stQueued, stWaiting:
				fail("job %s is %s, not running; remove it with: go-atrm %s", j.label(), j.State, ref)
			default:
				fail("job %s is not running (%s)", j.label(), j.State)
			}
			continue
		}
		switch j.State {
		case stQueued, stWaiting:
			if err := os.Remove(j.path); err != nil {
				fail("job %s could not be removed (it may have just started): %v", j.label(), err)
				continue
			}
			fmt.Printf("removed job %s\n", j.label())
		case stRunning:
			fail("job %s is running (process group %d); stop it with: go-atrm -k %s", j.label(), j.PGID, ref)
		default:
			fail("job %s is already %s; nothing to remove", j.label(), j.State)
		}
	}
	os.Exit(rc)
}

// ---- inspecting ------------------------------------------------------------

func catJobs(refs []string) {
	sp := openSpool()
	if len(refs) == 0 {
		refs = []string{""} // choose one, at a terminal
	}
	for _, ref := range refs {
		j := sp.jobFor(ref, anyJob)
		ref = j.ref()
		if j.path == "" {
			// ended: only the commands you typed are kept, never the environment
			b, err := os.ReadFile(sp.commandsPath(j.ID))
			if err != nil {
				die("job %s has ended and its commands were not kept (keep_commands is off, or it ended before this was saved); its output is available with: go-at --log %s", j.label(), ref)
			}
			os.Stdout.Write(b)
			continue
		}
		b, err := os.ReadFile(j.path)
		if err != nil {
			die("job %s: %v", j.label(), err)
		}
		os.Stdout.Write(b)
	}
}

func showLog(refs []string) {
	if len(refs) > 1 {
		die("usage: go-at --log [JOB]")
	}
	sp := openSpool()
	j := sp.jobFor(optArg(refs), anyJob)
	f, err := os.Open(j.Log)
	if err != nil {
		die("job %s has no output yet (it is %s)", j.label(), j.State)
	}
	defer f.Close()
	io.Copy(os.Stdout, f)
}

// ---- daemon ----------------------------------------------------------------

func cmdDaemon(args []string) {
	fs := flag.NewFlagSet("go-atd", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: go-atd [-d]\n  (no flag)  run in the foreground\n  -d         start in the background if not already running; silent no-op if it is\n             (safe to run from cron every minute as a watchdog)\n")
	}
	detach := fs.Bool("d", false, "run in the background; do nothing if already running")
	fs.Parse(args)
	sp := openSpool()
	if *detach {
		started, pid, err := detachDaemon(sp)
		if err != nil {
			die("%v", err)
		}
		if started {
			fmt.Fprintf(os.Stderr, "go-atd started (pid %d), log: %s\n", pid, sp.path("atd.log"))
		}
		return
	}
	if err := runDaemon(sp); err != nil {
		die("%v", err)
	}
}
