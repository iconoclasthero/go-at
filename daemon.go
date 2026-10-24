package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// batchLoadLimit: --batch jobs wait until the 1-minute load average is below
// this. Override with AT_BATCH_LOAD.
func batchLoadLimit() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("AT_BATCH_LOAD"), 64); err == nil && v >= 0 {
		return v
	}
	return 1.5
}

func loadAvg() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0 // unknown: don't hold batch jobs forever
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// keepDays: how long records of ended jobs (and their logs) are kept.
// Override with AT_KEEP_DAYS.
func keepDays() int {
	if v, err := strconv.Atoi(os.Getenv("AT_KEEP_DAYS")); err == nil && v >= 0 {
		return v
	}
	return 30
}

type daemon struct {
	s      *Spool
	mu     sync.Mutex
	active map[int]bool // jobs this daemon is itself running
	seen   map[int]bool // orphans already reported
	wg     sync.WaitGroup
}

func (d *daemon) isActive(id int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active[id]
}

func (d *daemon) setActive(id int, on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if on {
		d.active[id] = true
	} else {
		delete(d.active, id)
	}
}

func runDaemon(s *Spool) error {
	lock, err := os.OpenFile(s.path("atd.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("go-atd is already running for spool %s", s.Dir)
	}

	// A daemon started with a trailing & must outlive its terminal.
	signal.Ignore(syscall.SIGHUP)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	d := &daemon{s: s, active: map[int]bool{}, seen: map[int]bool{}}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	fmt.Fprintf(os.Stderr, "go-atd: watching %s\n", s.Dir)
	d.sweep()
	d.prune()
	lastPrune := time.Now()
	for {
		select {
		case <-sig:
			fmt.Fprintln(os.Stderr, "go-atd: shutting down, waiting for running jobs")
			d.wg.Wait()
			return nil
		case <-tick.C:
		}
		jobs, err := s.Queued()
		if err != nil {
			fmt.Fprintln(os.Stderr, "go-atd:", err)
			continue
		}
		now := time.Now()
		for _, j := range jobs {
			if j.Scheduled.After(now) {
				break // sorted by time
			}
			if j.Batch && loadAvg() >= batchLoadLimit() {
				continue
			}
			d.launch(j)
		}
		d.sweep()
		if time.Since(lastPrune) > time.Hour {
			d.prune()
			lastPrune = time.Now()
		}
	}
}

// launch claims the job atomically and runs it in the background.
func (d *daemon) launch(j Job) {
	s := d.s
	id := strconv.Itoa(j.ID)
	claimed := s.path("running", id)
	d.setActive(j.ID, true) // before the rename, so the sweep never sees it as an orphan
	// Hold the spool lock for the rename so go-at --edit can never swap the
	// file between our read of it and the claim.
	unlock, lerr := s.lock()
	if lerr != nil {
		d.setActive(j.ID, false)
		return
	}
	rerr := os.Rename(j.path, claimed)
	unlock()
	if rerr != nil {
		d.setActive(j.ID, false)
		return
	}
	logf, err := os.OpenFile(s.path("logs", id+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		os.Rename(claimed, j.path)
		d.setActive(j.ID, false)
		return
	}
	cmd := exec.Command(claimed)
	// The job gets exactly the environment captured at submission (it exports
	// it itself), never the daemon's: under supervisord/systemd that would
	// leak root's or the service manager's variables into the job.
	cmd.Env = []string{}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // own session: pid == pgid
	started := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(logf, "[go-at: job %s could not start: %v]\n", j.label(), err)
		logf.Close()
		code := 127
		d.finish(j, started, 0, &code, "", err.Error())
		return
	}
	pid := cmd.Process.Pid
	if j.Nice > 0 {
		syscall.Setpriority(syscall.PRIO_PROCESS, pid, j.Nice)
	}
	if err := s.writeProc(j.ID, pid, started); err != nil {
		fmt.Fprintf(os.Stderr, "go-atd: job %s: cannot write pid file: %v\n", j.label(), err)
	}
	fmt.Fprintf(logf, "[go-at: job %s started, process group %d]\n", j.label(), pid)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer logf.Close()
		werr := cmd.Wait()
		var code *int
		sigStr := ""
		note := ""
		if ps := cmd.ProcessState; ps != nil {
			if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				sigStr = sigName(ws.Signal())
			} else {
				c := ps.ExitCode()
				code = &c
			}
		} else if werr != nil {
			note = werr.Error()
		}
		lead := ""
		if !endsWithNewline(jobLogPath(d.s, j.ID)) {
			lead = "\n"
		}
		switch {
		case sigStr != "":
			fmt.Fprintf(logf, "%s[go-at: job %s terminated by %s after %s]\n", lead, j.label(), sigStr, time.Since(started).Round(time.Millisecond))
		case code != nil:
			fmt.Fprintf(logf, "%s[go-at: job %s exited %d after %s]\n", lead, j.label(), *code, time.Since(started).Round(time.Millisecond))
		}
		d.finish(j, started, pid, code, sigStr, note)
	}()
}

func jobLogPath(s *Spool, id int) string { return s.path("logs", strconv.Itoa(id)+".log") }

// endsWithNewline reports whether the file is empty or ends with '\n', so the
// closing marker starts on its own line without a stray blank one.
func endsWithNewline(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return true
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], st.Size()-1); err != nil {
		return true
	}
	return b[0] == '\n'
}

// finish records the outcome, then cleans up the running files.
func (d *daemon) finish(j Job, started time.Time, pid int, code *int, sigStr, note string) {
	s := d.s
	ended := time.Now()
	rec := j
	rec.Started, rec.Ended, rec.PGID, rec.ExitCode, rec.Signal = tp(started), tp(ended), pid, code, sigStr
	rec.Log = s.path("logs", strconv.Itoa(j.ID)+".log")
	switch {
	case sigStr != "":
		rec.State = stTerminated
	case code != nil && *code == 0:
		rec.State = stFinished
	default:
		rec.State = stFailed
	}
	if err := s.writeHistory(rec); err != nil {
		fmt.Fprintf(os.Stderr, "go-atd: job %s: cannot write history: %v\n", j.label(), err)
	}
	os.Remove(s.path("running", strconv.Itoa(j.ID)))
	s.removeProc(j.ID)
	d.setActive(j.ID, false)
	detail := rec.State
	switch {
	case sigStr != "":
		detail += " (" + sigStr + ")"
	case code != nil:
		detail += fmt.Sprintf(" (exit %d)", *code)
	case note != "":
		detail += " (" + note + ")"
	}
	fmt.Fprintf(os.Stderr, "go-atd: job %s %s; output in %s\n", j.label(), detail, rec.Log)
}

// sweep deals with entries in running/ that this daemon is not running itself:
// left behind by a daemon that died. If the job's process is still alive it is
// left alone (go-atq shows it as running and go-atrm -k can stop it); if it is
// gone, the job is recorded as interrupted and NOT re-run, since it may have
// partly completed.
func (d *daemon) sweep() {
	s := d.s
	ents, err := os.ReadDir(s.path("running"))
	if err != nil {
		return
	}
	for _, e := range ents {
		if !isNumeric(e.Name()) {
			continue
		}
		id, _ := strconv.Atoi(e.Name())
		if d.isActive(id) {
			continue
		}
		j, err := parseHeader(s.path("running", e.Name()))
		if err != nil {
			continue
		}
		if pid, err := s.verifyJobPID(id); err == nil || errors.Is(err, errUnverified) {
			if !d.seen[id] {
				d.seen[id] = true
				fmt.Fprintf(os.Stderr, "go-atd: job %s is still running from a previous daemon (process group %d); leaving it alone\n", j.label(), pid)
			}
			continue
		}
		delete(d.seen, id)
		if !fileExists(s.historyPath(id)) {
			rec := j
			rec.State = stInterrupted
			if pi := s.readProc(id); !pi.started.IsZero() {
				rec.Started = tp(pi.started)
			}
			rec.Ended = tp(time.Now())
			rec.Log = s.path("logs", e.Name()+".log")
			s.writeHistory(rec)
			fmt.Fprintf(os.Stderr, "go-atd: job %s was interrupted by a previous shutdown; not re-running it\n", j.label())
		}
		// keep the script for diagnosis, out of the way of the running/ directory
		os.Rename(s.path("running", e.Name()), s.path("logs", e.Name()+".interrupted"))
		s.removeProc(id)
	}
}

// prune deletes history records (and their logs) older than keepDays().
func (d *daemon) prune() {
	s := d.s
	cutoff := time.Now().AddDate(0, 0, -keepDays())
	hist, err := s.History()
	if err != nil {
		return
	}
	for _, j := range hist {
		if j.Ended != nil && j.Ended.Before(cutoff) {
			id := strconv.Itoa(j.ID)
			os.Remove(s.historyPath(j.ID))
			os.Remove(s.path("logs", id+".log"))
			os.Remove(s.path("logs", id+".interrupted"))
		}
	}
}

// detachDaemon starts go-atd in its own session with output going to
// <spool>/atd.log, and returns once it holds the lock. If a daemon is already
// running it does nothing and returns nil, so it is safe to run from cron every
// minute as a watchdog.
func detachDaemon(s *Spool) (started bool, pid int, err error) {
	if s.DaemonRunning() {
		return false, 0, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, 0, err
	}
	logf, err := os.OpenFile(s.path("atd.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return false, 0, err
	}
	defer logf.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return false, 0, err
	}
	defer null.Close()
	cmd := exec.Command(exe, "atd")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return false, 0, err
	}
	pid = cmd.Process.Pid
	cmd.Process.Release()
	for i := 0; i < 40; i++ { // up to 2s for the child to take the lock
		if s.DaemonRunning() {
			return true, pid, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false, pid, fmt.Errorf("go-atd did not start; see %s", s.path("atd.log"))
}
