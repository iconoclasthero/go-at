package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// batchLoadLimit: queue "b" jobs wait until the 1-minute load average is
// below this. Override with AT_BATCH_LOAD.
func batchLoadLimit() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("AT_BATCH_LOAD"), 64); err == nil && v > 0 {
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

func runDaemon(s *Spool) error {
	lock, err := os.OpenFile(s.path("atd.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("atd is already running for spool %s", s.Dir)
	}

	// recover jobs that were mid-run when a previous daemon died
	if ents, _ := os.ReadDir(s.path("running")); len(ents) > 0 {
		for _, e := range ents {
			fmt.Fprintf(os.Stderr, "atd: job %s was interrupted by a previous shutdown; not re-running it\n", e.Name())
			os.Rename(s.path("running", e.Name()), s.path("logs", e.Name()+".interrupted"))
		}
	}

	// A daemon started with a trailing & must outlive its terminal.
	signal.Ignore(syscall.SIGHUP)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	var wg sync.WaitGroup
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	fmt.Fprintf(os.Stderr, "atd: watching %s\n", s.Dir)
	for {
		select {
		case <-sig:
			fmt.Fprintln(os.Stderr, "atd: shutting down, waiting for running jobs")
			wg.Wait()
			return nil
		case <-tick.C:
		}
		jobs, err := s.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, "atd:", err)
			continue
		}
		now := time.Now()
		for _, j := range jobs {
			if j.RunAt.After(now) {
				break // sorted by time
			}
			if j.Queue == 'b' && loadAvg() >= batchLoadLimit() {
				continue
			}
			wg.Add(1)
			if !s.launch(j, &wg) {
				wg.Done()
			}
		}
	}
}

// launch claims the job atomically and runs it in the background.
func (s *Spool) launch(j Job, wg *sync.WaitGroup) bool {
	id := strconv.Itoa(j.ID)
	claimed := s.path("running", id)
	if err := os.Rename(j.Path, claimed); err != nil {
		return false
	}
	logf, err := os.OpenFile(s.path("logs", id+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		os.Rename(claimed, j.Path)
		return false
	}
	go func() {
		defer wg.Done()
		defer logf.Close()
		cmd := exec.Command(claimed)
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		started := time.Now()
		err := cmd.Run()
		status := "exit 0"
		if err != nil {
			status = err.Error()
		}
		fmt.Fprintf(logf, "\n[at: job %d finished after %s: %s]\n", j.ID, time.Since(started).Round(time.Millisecond), status)
		os.Remove(claimed)
		fmt.Fprintf(os.Stderr, "atd: job %d done (%s); output in %s\n", j.ID, status, filepath.Join(s.Dir, "logs", id+".log"))
	}()
	return true
}

// detachDaemon starts atd in its own session with output going to
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
	return false, pid, fmt.Errorf("atd did not start; see %s", s.path("atd.log"))
}
