package main

// Job model, states, and the per-job process files.
//
// A job is in exactly one of these states, always shown as a word:
//
//	queued       waiting for its time
//	waiting      due, but held back by --batch until system load is low
//	running      process group alive
//	finished     exited 0
//	failed       exited non-zero
//	terminated   killed by a signal (go-atrm -k, or anything else)
//	interrupted  the daemon stopped while it was running and the job is gone
//
// queued/waiting/running are derived from the spool directories; the rest are
// read from history records, which the daemon writes when a job ends.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	stQueued      = "queued"
	stWaiting     = "waiting"
	stRunning     = "running"
	stFinished    = "finished"
	stFailed      = "failed"
	stTerminated  = "terminated"
	stInterrupted = "interrupted"
)

type Job struct {
	ID         int        `json:"id"`
	Name       string     `json:"name,omitempty"`
	State      string     `json:"state"`
	User       string     `json:"user"`
	Created    time.Time  `json:"created"`
	Scheduled  time.Time  `json:"scheduled"`
	Started    *time.Time `json:"started,omitempty"`
	Ended      *time.Time `json:"ended,omitempty"`
	PGID       int        `json:"pgid,omitempty"`
	Unverified bool       `json:"pid_unverified,omitempty"`
	Nice       int        `json:"nice,omitempty"`
	Batch      bool       `json:"batch,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Signal     string     `json:"signal,omitempty"`
	Log        string     `json:"log,omitempty"`

	path string // script file while queued or running
}

func tp(t time.Time) *time.Time { return &t }

func (j Job) Active() bool {
	return j.State == stQueued || j.State == stWaiting || j.State == stRunning
}

// label is "19" or "19 (steelers-arr)" for messages.
func (j Job) label() string {
	if j.Name != "" {
		return fmt.Sprintf("%d (%s)", j.ID, j.Name)
	}
	return strconv.Itoa(j.ID)
}

var sigNames = map[syscall.Signal]string{
	syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGABRT: "SIGABRT", syscall.SIGKILL: "SIGKILL", syscall.SIGSEGV: "SIGSEGV",
	syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM",
	syscall.SIGUSR1: "SIGUSR1", syscall.SIGUSR2: "SIGUSR2",
}

func sigName(s syscall.Signal) string {
	if n, ok := sigNames[s]; ok {
		return n
	}
	return fmt.Sprintf("signal %d", int(s))
}

// parseSignal accepts TERM, SIGTERM, term, or a number.
func parseSignal(s string) (syscall.Signal, error) {
	if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 65 {
		return syscall.Signal(n), nil
	}
	u := strings.ToUpper(strings.TrimPrefix(strings.ToUpper(s), "SIG"))
	for sig, name := range sigNames {
		if strings.TrimPrefix(name, "SIG") == u {
			return sig, nil
		}
	}
	return 0, fmt.Errorf("unknown signal %q (try TERM, INT, HUP, KILL, or a number)", s)
}

// ---- per-job process files -------------------------------------------------
//
//	running/<id>.pid    bare number + newline: the job's process-group id
//	                    (jobs run in their own session, so pid == pgid).
//	                    Safe to use directly:  kill -- -"$(cat running/19.pid)"
//	running/<id>.proc   key=value extras used to verify the process is still
//	                    the same one before signalling it (guards PID reuse)

func (s *Spool) pidPath(id int) string  { return s.path("running", strconv.Itoa(id)+".pid") }
func (s *Spool) procPath(id int) string { return s.path("running", strconv.Itoa(id)+".proc") }

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Spool) writeProc(id, pid int, started time.Time) error {
	// .proc first, so anything that can see the .pid can also verify it
	extra := fmt.Sprintf("pid=%d\nstarted=%d\n", pid, started.Unix())
	if t, ok := procStartTicks(pid); ok {
		extra += fmt.Sprintf("startticks=%d\n", t)
	}
	if err := writeFileAtomic(s.procPath(id), []byte(extra), 0o600); err != nil {
		return err
	}
	return writeFileAtomic(s.pidPath(id), []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func (s *Spool) removeProc(id int) {
	os.Remove(s.pidPath(id))
	os.Remove(s.procPath(id))
}

type procInfo struct {
	started time.Time
	ticks   uint64
}

func (s *Spool) readProc(id int) procInfo {
	var pi procInfo
	b, err := os.ReadFile(s.procPath(id))
	if err != nil {
		return pi
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(line, "=")
		n, _ := strconv.ParseInt(v, 10, 64)
		switch k {
		case "started":
			pi.started = time.Unix(n, 0)
		case "startticks":
			pi.ticks = uint64(n)
		}
	}
	return pi
}

// procStartTicks returns the process start time in clock ticks since boot
// (Linux /proc). On systems without /proc it reports false and callers fall
// back to weaker checks.
// procStatFmt is a variable so tests can simulate an unreadable /proc entry.
var procStatFmt = "/proc/%d/stat"

func procStartTicks(pid int) (uint64, bool) {
	b, err := os.ReadFile(fmt.Sprintf(procStatFmt, pid))
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // the command name may contain spaces and parens
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0, false
	}
	v, err := strconv.ParseUint(f[19], 10, 64) // field 22: starttime
	return v, err == nil
}

var (
	errNotRunning = errors.New("its process is no longer running")
	errUnverified = errors.New("its process identity could not be verified")
)

// haveProc reports whether /proc process start times are available (Linux).
func haveProc() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
}

// verifyJobPID returns the job's process-group id only when it is certain the
// process is the one we started. It fails closed: where /proc exists (Linux),
// the .proc file must hold a nonzero recorded start time AND the live
// /proc/PID/stat start time must be readable and equal to it. Any doubt means
// no signal is ever sent. Without /proc (e.g. macOS) the start-time proof is
// impossible, so the weaker fallback applies: the pid exists and leads its own
// process group. That cannot rule out a recycled pid.
//
// Errors: errNotRunning (gone, or the pid now belongs to something else) and
// errUnverified (a process is there but we cannot prove it is the job).
// The caller must still tolerate the process vanishing before it signals.
func (s *Spool) verifyJobPID(id int) (int, error) {
	b, err := os.ReadFile(s.pidPath(id))
	if err != nil {
		return 0, errNotRunning
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return 0, errNotRunning
	}
	if err := syscall.Kill(pid, 0); err != nil && err != syscall.EPERM {
		return 0, errNotRunning
	}
	if pg, err := syscall.Getpgid(pid); err != nil || pg != pid {
		return 0, errNotRunning
	}
	if haveProc() {
		want := s.readProc(id).ticks
		if want == 0 {
			return 0, fmt.Errorf("%w (no start time was recorded for it)", errUnverified)
		}
		got, ok := procStartTicks(pid)
		if !ok {
			return 0, fmt.Errorf("%w (cannot read /proc/%d/stat)", errUnverified, pid)
		}
		if got != want {
			return 0, errNotRunning // the pid was reused by another process
		}
	}
	return pid, nil
}

// liveJobPID is verifyJobPID for callers that only need yes or no.
func (s *Spool) liveJobPID(id int) (int, bool) {
	pid, err := s.verifyJobPID(id)
	return pid, err == nil
}

// ---- history records -------------------------------------------------------

func (s *Spool) historyPath(id int) string {
	return s.path("history", strconv.Itoa(id)+".json")
}

func (s *Spool) writeHistory(j Job) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.historyPath(j.ID), append(b, '\n'), 0o600)
}
