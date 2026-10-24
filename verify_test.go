package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Every way the start-time proof can fail must end with NO signal sent; only a
// complete, matching record lets go-atrm -k through. (Linux: needs /proc.)
func TestKillFailsClosed(t *testing.T) {
	if !haveProc() {
		t.Skip("no /proc on this system")
	}
	e := newEnv(t)
	cases := []struct {
		name    string
		proc    string // contents of N.proc; "-" = file absent; "REAL" = correct record
		wantMsg string
		dies    bool
	}{
		{"missing .proc file", "-", "could not be verified", false},
		{"no startticks line", "pid=1\nstarted=1\n", "could not be verified", false},
		{"zero start time", "startticks=0\n", "could not be verified", false},
		{"garbage start time", "startticks=abc\n", "could not be verified", false},
		{"empty .proc file", "", "could not be verified", false},
		{"mismatched start time", "startticks=1\n", "not running", false},
		{"valid matching record", "REAL", "sent SIGTERM", true},
	}
	for i, c := range cases {
		id := 20 + i
		pid := spawnSession(t)
		writeRunning(t, e, id)
		run := filepath.Join(e.spool, "running")
		os.WriteFile(filepath.Join(run, strconv.Itoa(id)+".pid"), []byte(strconv.Itoa(pid)+"\n"), 0o600)
		switch c.proc {
		case "-":
		case "REAL":
			writeProcFiles(t, e, id, pid)
		default:
			os.WriteFile(filepath.Join(run, strconv.Itoa(id)+".proc"), []byte(c.proc), 0o600)
		}
		so, se, code := e.run("", "atrm", "-k", strconv.Itoa(id))
		out := so + se
		if !strings.Contains(out, c.wantMsg) || (code == 0) != c.dies {
			t.Errorf("%s: code %d, output %q (want %q)", c.name, code, out, c.wantMsg)
		}
		if !c.dies {
			if strings.Contains(c.wantMsg, "verified") && !strings.Contains(out, "nothing was signalled") {
				t.Errorf("%s: should say nothing was signalled: %q", c.name, out)
			}
			if syscall.Kill(pid, 0) != nil {
				t.Errorf("%s: the process was signalled although unverified", c.name)
			}
		} else {
			waitFor(t, "verified job to die", 5e9, func() bool { return groupGone(pid) })
		}
	}
}

// If /proc/PID/stat cannot be read, verification fails rather than guessing.
func TestVerifyUnreadableStat(t *testing.T) {
	if !haveProc() {
		t.Skip("no /proc on this system")
	}
	e := newEnv(t)
	sp := &Spool{Dir: e.spool}
	os.MkdirAll(filepath.Join(e.spool, "running"), 0o700)
	pid := spawnSession(t)
	writeRunning(t, e, 30)
	writeProcFiles(t, e, 30, pid)
	if got, err := sp.verifyJobPID(30); err != nil || got != pid {
		t.Fatalf("valid record must verify: %d %v", got, err)
	}
	old := procStatFmt
	procStatFmt = filepath.Join(e.spool, "no-such-dir", "%d")
	defer func() { procStatFmt = old }()
	if _, err := sp.verifyJobPID(30); err == nil || !strings.Contains(err.Error(), "could not be verified") {
		t.Errorf("unreadable stat must fail closed, got %v", err)
	}
}

// A job is finished when its shell exits; it does not wait for background
// descendants, and they are no longer reachable through go-atrm -k.
func TestBackgroundDescendantsOutliveTheJob(t *testing.T) {
	e := newEnv(t)
	pidFile := filepath.Join(t.TempDir(), "bg.pid")
	id := e.submit("sleep 300 &\necho $! > "+pidFile+"\n", "-n", "bg", "now")
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "job finished though its child lives", 15e9, func() bool {
		j, ok := e.job("bg", true)
		return ok && j.State == stFinished
	})
	b, _ := os.ReadFile(pidFile)
	bg, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if bg <= 1 || syscall.Kill(bg, 0) != nil {
		t.Fatalf("background process should still be alive (pid %q)", b)
	}
	defer syscall.Kill(bg, syscall.SIGKILL)
	if _, se, code := e.run("", "atrm", "-k", strconv.Itoa(id)); code == 0 || !strings.Contains(se, "not running") {
		t.Errorf("-k on a finished job must refuse: %d %q", code, se)
	}
	if syscall.Kill(bg, 0) != nil {
		t.Errorf("-k on a finished job must not touch the leftover process")
	}
}
