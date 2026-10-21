// at, atq, atrm, batch and atd in one binary (dispatch on argv[0], or use
// the first argument as the command name: `at-go atq`).
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const timeFmt = "Mon Jan _2 15:04:05 2006"

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "at: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	name := filepath.Base(os.Args[0])
	args := os.Args[1:]
	switch name {
	case "at", "atq", "atrm", "batch", "atd":
	default:
		if len(args) > 0 {
			switch args[0] {
			case "at", "atq", "atrm", "batch", "atd":
				name, args = args[0], args[1:]
			}
		}
		if name != "atq" && name != "atrm" && name != "batch" && name != "atd" {
			name = "at"
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
		cmdAt(append([]string{"-b"}, args...))
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
	for _, c := range []string{flagVal, os.Getenv("AT_SHELL"), "bash"} {
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

const atUsage = `usage: at [-q queue] [-s shell] [-f file] TIME...      schedule commands (from stdin or -f)
       at -l [-q queue]                                list pending jobs (same as atq)
       at -d|-r ID...                                  remove jobs (same as atrm)
       at -c ID                                        print a job's script
       at -b | batch                                   run when system load is low

TIME examples:
  at 17:30                          at 9:30:15 tomorrow         at noon
  at now + 2 hours 55 minutes       at +2h55m                   at in 90 seconds
  at 11-25-2026 09:30 + 2 hours 55 minutes
  at sunday 9:30 am ET + 1 hour     at Nov 25 2026 9am          at next friday
Jobs run in bash by default (-s or $AT_SHELL to change). Start the daemon
with: atd   (jobs only run while atd is running)
`

func cmdAt(args []string) {
	fs := flag.NewFlagSet("at", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, atUsage) }
	queue := fs.String("q", "", "queue letter (a-z, A-Z); 'b' is the batch queue")
	file := fs.String("f", "", "read commands from file")
	shellFlag := fs.String("s", "", "shell used to run the job (default bash)")
	list := fs.Bool("l", false, "list jobs")
	rm := fs.Bool("d", false, "remove jobs")
	rm2 := fs.Bool("r", false, "remove jobs")
	cat := fs.Bool("c", false, "print job script")
	batch := fs.Bool("b", false, "batch: run when load is low")
	fs.Parse(args)

	switch {
	case *list:
		listJobs(*queue)
		return
	case *rm || *rm2:
		removeJobs(fs.Args())
		return
	case *cat:
		catJobs(fs.Args())
		return
	}

	q := byte('a')
	if *batch {
		q = 'b'
	}
	if *queue != "" {
		if len(*queue) != 1 || !isQueueChar((*queue)[0]) {
			die("bad queue %q (use one letter)", *queue)
		}
		q = (*queue)[0]
	}

	spec := strings.Join(fs.Args(), " ")
	if *batch && spec == "" {
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

	shell := resolveShell(*shellFlag)
	commands := readCommands(*file, runAt)
	if strings.TrimSpace(commands) == "" {
		die("no commands given; job not created")
	}
	sp := openSpool()
	id, err := sp.AddJob(runAt, q, shell, commands)
	if err != nil {
		die("cannot write job: %v", err)
	}
	fmt.Fprintf(os.Stderr, "job %d at %s\n", id, runAt.Format(timeFmt+" MST"))
	if !sp.DaemonRunning() {
		fmt.Fprintln(os.Stderr, "warning: atd is not running, so this job will not run until you start it (run: atd)")
	}
}

func isQueueChar(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

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
		return readInteractive(runAt)
	}
	b, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		die("%v", err)
	}
	return string(b)
}

// readInteractive prompts with at> and a real line editor. End with Ctrl-D.
func readInteractive(runAt time.Time) string {
	fmt.Fprintf(os.Stderr, "at %s\n", runAt.Format(timeFmt+" MST"))
	ed := NewEditor(os.Stdin, os.Stderr, func() int { return termCols(int(os.Stderr.Fd())) })
	var lines []string
	for {
		restore, err := makeRaw(int(os.Stdin.Fd()))
		if err != nil {
			die("cannot set terminal mode: %v", err)
		}
		line, err := ed.ReadLine("at> ")
		restore()
		if err == io.EOF {
			break
		}
		if err == errInterrupt {
			fmt.Fprintln(os.Stderr, "at: cancelled; job not created")
			os.Exit(130)
		}
		if err != nil {
			die("%v", err)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n"
}

func cmdList(args []string) {
	fs := flag.NewFlagSet("atq", flag.ExitOnError)
	queue := fs.String("q", "", "only this queue")
	fs.Parse(args)
	listJobs(*queue)
}

func listJobs(queue string) {
	jobs, err := openSpool().List()
	if err != nil {
		die("%v", err)
	}
	for _, j := range jobs {
		if queue != "" && string(j.Queue) != queue {
			continue
		}
		fmt.Printf("%d\t%s %c %s\n", j.ID, j.RunAt.Local().Format(timeFmt), j.Queue, j.User)
	}
}

func cmdRemove(args []string) {
	removeJobs(args)
}

func removeJobs(ids []string) {
	if len(ids) == 0 {
		die("usage: atrm ID...")
	}
	sp := openSpool()
	rc := 0
	for _, id := range ids {
		if _, err := strconv.Atoi(id); err != nil {
			fmt.Fprintf(os.Stderr, "at: bad job id %q\n", id)
			rc = 1
			continue
		}
		if err := os.Remove(sp.path("jobs", id)); err != nil {
			fmt.Fprintf(os.Stderr, "at: cannot remove job %s: %v\n", id, errText(err))
			rc = 1
		}
	}
	os.Exit(rc)
}

func errText(err error) string {
	if os.IsNotExist(err) {
		return "no such job"
	}
	return err.Error()
}

func catJobs(ids []string) {
	if len(ids) == 0 {
		die("usage: at -c ID")
	}
	sp := openSpool()
	for _, id := range ids {
		if _, err := strconv.Atoi(id); err != nil {
			die("bad job id %q", id)
		}
		b, err := os.ReadFile(sp.path("jobs", id))
		if err != nil {
			die("job %s: %v", id, errText(err))
		}
		os.Stdout.Write(b)
	}
}

func cmdDaemon(args []string) {
	fs := flag.NewFlagSet("atd", flag.ExitOnError)
	fs.Parse(args)
	if err := runDaemon(openSpool()); err != nil {
		die("%v", err)
	}
}
