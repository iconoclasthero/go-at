//go:build linux

package main

// End-to-end tests: build the real binary, run it as go-at/go-atq/go-atrm/
// go-atd against a throwaway spool, and check what a user would see.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var binDir string

// syncBuf is a bytes.Buffer that is safe to read while a child process is
// still writing to it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "go-at-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	exe := filepath.Join(dir, "go-at")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	for _, n := range []string{"atq", "atrm", "batch", "atd"} {
		os.Symlink(exe, filepath.Join(dir, "go-"+n))
	}
	binDir = dir
	os.Exit(m.Run())
}

type env struct {
	t     *testing.T
	spool string
	extra []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	t.Parallel()
	return &env{t: t, spool: t.TempDir()}
}

// cmd builds a command for "at", "atq", "atrm", "batch" or "atd".
func (e *env) cmd(stdin string, name string, args ...string) *exec.Cmd {
	c := exec.Command(filepath.Join(binDir, "go-"+name), args...)
	c.Env = append(os.Environ(), "AT_SPOOL="+e.spool, "TZ=America/New_York", "AT_AUTOSTART=", "AT_SHELL=", "AT_KEEP_DAYS=")
	c.Env = append(c.Env, e.extra...)
	c.Stdin = strings.NewReader(stdin)
	return c
}

// run returns stdout, stderr and the exit code.
func (e *env) run(stdin string, name string, args ...string) (string, string, int) {
	c := e.cmd(stdin, name, args...)
	var so, se bytes.Buffer
	c.Stdout, c.Stderr = &so, &se
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return so.String(), se.String(), code
}

var jobRe = regexp.MustCompile(`job (\d+)(?: \(([^)]*)\))? at`)

func (e *env) submit(stdin string, args ...string) int {
	e.t.Helper()
	_, se, code := e.run(stdin, "at", args...)
	m := jobRe.FindStringSubmatch(se)
	if code != 0 || m == nil {
		e.t.Fatalf("go-at %v: exit %d: %s", args, code, se)
	}
	id, _ := strconv.Atoi(m[1])
	return id
}

// jobs returns go-atq --json output.
func (e *env) jobs(args ...string) []Job {
	e.t.Helper()
	out, se, code := e.run("", "atq", append([]string{"--json"}, args...)...)
	if code != 0 {
		e.t.Fatalf("go-atq --json: %d %s", code, se)
	}
	var js []Job
	if err := json.Unmarshal([]byte(out), &js); err != nil {
		e.t.Fatalf("bad json %q: %v", out, err)
	}
	return js
}

func (e *env) job(ref string, all bool) (Job, bool) {
	var args []string
	if all {
		args = append(args, "--all")
	}
	for _, j := range e.jobs(args...) {
		if strconv.Itoa(j.ID) == ref || j.Name == ref {
			return j, true
		}
	}
	return Job{}, false
}

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// startDaemon runs go-atd in the foreground; the returned func stops it.
func (e *env) startDaemon() (stop func(), stderr *syncBuf) {
	e.t.Helper()
	d := e.cmd("", "atd")
	se := &syncBuf{}
	d.Stderr = se
	if err := d.Start(); err != nil {
		e.t.Fatal(err)
	}
	waitFor(e.t, "daemon to take its lock", 5*time.Second, func() bool {
		return exec.Command("true").Run() == nil && (&Spool{Dir: e.spool}).DaemonRunning()
	})
	return func() {
		d.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { d.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			d.Process.Kill()
			<-done
		}
	}, se
}

func groupGone(pgid int) bool { return syscall.Kill(-pgid, 0) == syscall.ESRCH }

// ---- submitting, listing, removing -----------------------------------------

func TestSubmitListCatRemove(t *testing.T) {
	e := newEnv(t)
	id := e.submit("echo hello\n", "now", "+", "1", "hour")

	out, _, _ := e.run("", "atq")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "STATE") {
		t.Fatalf("atq should print a header and one row:\n%s", out)
	}
	if !strings.Contains(lines[1], "queued") || !strings.HasPrefix(lines[1], strconv.Itoa(id)) {
		t.Errorf("row should say 'queued': %q", lines[1])
	}
	for _, l := range lines {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("trailing whitespace in %q", l)
		}
	}
	if j, ok := e.job(strconv.Itoa(id), false); !ok || j.State != "queued" || j.User == "" {
		t.Errorf("json: %+v ok=%v", j, ok)
	}

	script, _, _ := e.run("", "at", "-c", strconv.Itoa(id))
	first := strings.SplitN(script, "\n", 2)[0]
	if !strings.HasPrefix(first, "#!") || filepath.Base(first) != "bash" {
		t.Errorf("job should run under bash, shebang is %q", first)
	}
	if !strings.Contains(script, "echo hello") {
		t.Errorf("job script lost the command:\n%s", script)
	}

	if so, _, code := e.run("", "atrm", strconv.Itoa(id)); code != 0 || !strings.Contains(so, "removed job") {
		t.Errorf("atrm: %d %q", code, so)
	}
	if out, _, _ := e.run("", "atq"); out != "" {
		t.Errorf("job still listed after atrm: %q", out)
	}
	if _, se, code := e.run("", "atrm", "999"); code == 0 || !strings.Contains(se, "no such job") {
		t.Errorf("atrm of a missing job: code %d, %q", code, se)
	}
	// the same list via the subcommand and the -l alias
	e.submit("true\n", "now", "+", "1", "hour")
	a, _, _ := e.run("", "at", "-l")
	b, _, _ := e.run("", "at", "atq")
	if a == "" || a != b {
		t.Errorf("at -l and 'go-at atq' should agree:\n%q\n%q", a, b)
	}
}

func TestNames(t *testing.T) {
	e := newEnv(t)
	id := e.submit("true\n", "--name", "steelers-arr", "now", "+", "1", "hour")
	if out, _, _ := e.run("", "atq"); !strings.Contains(out, "steelers-arr") {
		t.Errorf("name missing from atq:\n%s", out)
	}
	if _, se, code := e.run("true\n", "at", "-n", "steelers-arr", "now", "+", "2", "hours"); code == 0 ||
		!strings.Contains(se, "already used by job "+strconv.Itoa(id)) {
		t.Errorf("duplicate name should be refused, naming the job: %d %q", code, se)
	}
	for _, bad := range []string{"123", "-x", "has space", "", strings.Repeat("a", 65), "9lives"} {
		if bad == "" {
			continue
		}
		if _, se, code := e.run("true\n", "at", "-n", bad, "now"); code == 0 {
			t.Errorf("name %q should be rejected (%s)", bad, se)
		}
	}
	// by name: script, then removal; the name is free again afterwards
	if script, _, code := e.run("", "at", "-c", "steelers-arr"); code != 0 || !strings.Contains(script, "export AT_JOB_NAME='steelers-arr'") {
		t.Errorf("-c by name: %d\n%s", code, script)
	}
	if _, _, code := e.run("", "atrm", "steelers-arr"); code != 0 {
		t.Errorf("remove by name failed")
	}
	e.submit("true\n", "-n", "steelers-arr", "now", "+", "1", "hour")
}

func TestMixedUnitsAndSeconds(t *testing.T) {
	e := newEnv(t)
	ny, _ := time.LoadLocation("America/New_York")
	date := time.Now().AddDate(0, 0, 30).Format("01-02-2006")
	cases := []struct {
		spec []string
		want string
	}{
		{[]string{date, "09:30", "+", "2", "hours", "55", "minutes"}, "12:25:00"},
		{[]string{date, "09:30:15", "+", "1", "h", "2", "m", "3", "s"}, "10:32:18"},
		{[]string{date, "09:30", "+2h55m"}, "12:25:00"},
		{[]string{date, "09:30", "+", "2:55"}, "12:25:00"},
	}
	for _, c := range cases {
		id := e.submit("true\n", c.spec...)
		j, ok := e.job(strconv.Itoa(id), false)
		if !ok {
			t.Fatalf("%v: job missing", c.spec)
		}
		if got := j.Scheduled.In(ny).Format("15:04:05"); got != c.want {
			t.Errorf("%v: scheduled %s, want %s", c.spec, got, c.want)
		}
	}
}

func TestNiceAndBatch(t *testing.T) {
	e := newEnv(t)
	id := e.submit("true\n", "--nice", "5", "now", "+", "1", "hour")
	if out, _, _ := e.run("", "atq"); !strings.Contains(out, "nice=5") {
		t.Errorf("nice should be shown as nice=5:\n%s", out)
	}
	if j, _ := e.job(strconv.Itoa(id), false); j.Nice != 5 {
		t.Errorf("json nice = %d", j.Nice)
	}
	for _, n := range []string{"20", "-1", "x"} {
		if _, _, code := e.run("true\n", "at", "--nice", n, "now"); code == 0 {
			t.Errorf("--nice %s should be rejected", n)
		}
	}
	// the old queue letters are gone, with a pointer to what replaced them
	if _, se, code := e.run("true\n", "at", "-q", "b", "now"); code == 0 || !strings.Contains(se, "--batch") {
		t.Errorf("-q should explain the replacement: %d %q", code, se)
	}
	// batch: a plain word in the output, not a queue letter
	e.extra = []string{"AT_BATCH_LOAD=1000000"}
	_, se, code := e.run("true\n", "batch")
	if code != 0 || !jobRe.MatchString(se) {
		t.Fatalf("batch: %d %s", code, se)
	}
	bid, _ := strconv.Atoi(jobRe.FindStringSubmatch(se)[1])
	j, _ := e.job(strconv.Itoa(bid), false)
	if !j.Batch {
		t.Errorf("batch job not marked batch: %+v", j)
	}
	e.run("", "atrm", strconv.Itoa(bid))
	// a due batch job on a busy machine says "waiting", not a letter
	e.extra = []string{"AT_BATCH_LOAD=0"}
	e.submit("true\n", "--batch", "now")
	if out, _, _ := e.run("", "atq"); !strings.Contains(out, "waiting") || !strings.Contains(out, "waiting for load below") {
		t.Errorf("due batch job under load should read 'waiting':\n%s", out)
	}
}

func TestBadInput(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		stdin string
		args  []string
		msg   string
	}{
		{"true\n", []string{"now", "+", "1:90"}, "below 60"},
		{"true\n", []string{"yesterday"}, "yesterday"},
		{"true\n", []string{"11-25-2026", "09:30", "sunday"}, "Wednesday"},
		{"true\n", nil, "usage"},
		{"   \n", []string{"now", "+", "1", "hour"}, "no commands"},
	} {
		_, se, code := e.run(c.stdin, "at", c.args...)
		if code == 0 || !strings.Contains(se, c.msg) {
			t.Errorf("%v: exit %d, stderr %q (want %q)", c.args, code, se, c.msg)
		}
	}
	if out, _, _ := e.run("", "atq"); out != "" {
		t.Errorf("failed submissions must not leave jobs: %q", out)
	}
}

func TestJobRecordsEnvCwdUmask(t *testing.T) {
	e := newEnv(t)
	work := t.TempDir()
	c := e.cmd("true\n", "at", "-n", "envjob", "now", "+", "1", "hour")
	c.Dir = work
	c.Env = append(c.Env, "GOAT_TEST=it's \"quoted\" $HOME", "AT_JOB_ID=stale-from-a-parent-job")
	var se bytes.Buffer
	c.Stderr = &se
	if err := c.Run(); err != nil {
		t.Fatal(se.String())
	}
	id := jobRe.FindStringSubmatch(se.String())[1]
	script, _, _ := e.run("", "at", "-c", id)
	for _, want := range []string{
		`export GOAT_TEST='it'\''s "quoted" $HOME'`,
		"cd '" + work + "'",
		"umask ",
		"export AT_JOB_ID=" + id + "\n",
		"export AT_JOB_NAME='envjob'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "stale-from-a-parent-job") {
		t.Errorf("AT_JOB_ID must not be inherited from the submitting environment")
	}
}

func TestCorruptCounter(t *testing.T) {
	e := newEnv(t)
	os.WriteFile(filepath.Join(e.spool, "seq"), []byte("garbage\n"), 0o600)
	_, se, code := e.run("true\n", "at", "now", "+", "1", "hour")
	if code == 0 || !strings.Contains(se, "corrupt") {
		t.Errorf("want a clear error for a corrupt counter, got exit %d: %q", code, se)
	}
}

// ---- the daemon ------------------------------------------------------------

func TestDaemonRunsJobsInBash(t *testing.T) {
	e := newEnv(t)
	marker := filepath.Join(t.TempDir(), "marker")
	// arrays and [[ ]] only exist in bash
	script := fmt.Sprintf("a=(x y z); [[ ${#a[@]} -eq 3 ]] && echo \"bash:${BASH_VERSION%%%%.*} $AT_JOB_ID $AT_JOB_NAME\" > %s\n", marker)
	id := e.submit(script, "-n", "bashjob", "now", "+", "1", "second")
	stop, _ := e.startDaemon()
	defer stop()

	waitFor(t, "job to run", 10*time.Second, func() bool { _, err := os.Stat(marker); return err == nil })
	b, _ := os.ReadFile(marker)
	if !strings.HasPrefix(string(b), "bash:") || !strings.HasSuffix(strings.TrimSpace(string(b)), fmt.Sprintf(" %d bashjob", id)) {
		t.Errorf("job did not run under bash with AT_JOB_ID/AT_JOB_NAME: %q", b)
	}
	waitFor(t, "job to be recorded as finished", 5*time.Second, func() bool {
		j, ok := e.job("bashjob", true)
		return ok && j.State == "finished"
	})
	if out, _, _ := e.run("", "atq"); out != "" {
		t.Errorf("ended jobs must not show in plain atq: %q", out)
	}
	if _, se, code := e.run("", "atd"); code == 0 || !strings.Contains(se, "already running") {
		t.Errorf("second daemon should refuse: %d %q", code, se)
	}
}

func TestFinishedAndFailed(t *testing.T) {
	e := newEnv(t)
	ok := e.submit("echo fine\n", "-n", "good", "now", "+", "1", "second")
	bad := e.submit("echo broke; exit 3\n", "-n", "bad", "now", "+", "1", "second")
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "both jobs to end", 10*time.Second, func() bool {
		a, _ := e.job(strconv.Itoa(ok), true)
		b, _ := e.job(strconv.Itoa(bad), true)
		return a.Ended != nil && b.Ended != nil
	})
	a, _ := e.job("good", true)
	b, _ := e.job("bad", true)
	if a.State != "finished" || a.ExitCode == nil || *a.ExitCode != 0 {
		t.Errorf("good: %+v", a)
	}
	if b.State != "failed" || b.ExitCode == nil || *b.ExitCode != 3 {
		t.Errorf("bad: %+v", b)
	}
	if out, _, _ := e.run("", "atq", "--all"); !strings.Contains(out, "finished") || !strings.Contains(out, "failed") || !strings.Contains(out, "exit 3") {
		t.Errorf("atq --all should use plain words:\n%s", out)
	}
	if out, _, code := e.run("", "at", "--log", "bad"); code != 0 || !strings.Contains(out, "broke") || !strings.Contains(out, "exited 3") {
		t.Errorf("--log: %d %q", code, out)
	}
	if _, se, code := e.run("", "at", "-c", "bad"); code == 0 || !strings.Contains(se, "has ended") {
		t.Errorf("-c of an ended job should explain: %d %q", code, se)
	}
	if _, se, code := e.run("", "atrm", "bad"); code == 0 || !strings.Contains(se, "already failed") {
		t.Errorf("atrm of an ended job: %d %q", code, se)
	}
}

// The scenario this project exists for: a long until-loop that you need to
// find and stop without having captured $$ yourself.
func TestRunningJobPidAndKill(t *testing.T) {
	e := newEnv(t)
	id := e.submit("until false; do echo tick; sleep 1000; done\n", "--name", "steelers-arr", "now", "+", "1", "second")
	stop, _ := e.startDaemon()
	defer stop()

	pidFile := filepath.Join(e.spool, "running", strconv.Itoa(id)+".pid")
	waitFor(t, "pid file", 10*time.Second, func() bool { _, err := os.Stat(pidFile); return err == nil })
	raw, _ := os.ReadFile(pidFile)
	if !regexp.MustCompile(`^\d+\n$`).Match(raw) {
		t.Fatalf("pid file must be a bare number: %q", raw)
	}
	pgid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))

	j, ok := e.job("steelers-arr", false)
	if !ok || j.State != "running" || j.PGID != pgid || j.Started == nil {
		t.Fatalf("running job should be listed with its pgid: %+v ok=%v", j, ok)
	}
	if out, _, _ := e.run("", "atq"); !strings.Contains(out, "running") || !strings.Contains(out, fmt.Sprintf("pgid %d", pgid)) {
		t.Errorf("atq should show 'running' and the pgid:\n%s", out)
	}
	// the process really is the group leader, and sleep is in its group
	if pg, err := syscall.Getpgid(pgid); err != nil || pg != pgid {
		t.Errorf("job pid %d should lead its own group, got %d %v", pgid, pg, err)
	}

	// plain atrm must refuse and point at -k
	if _, se, code := e.run("", "atrm", "steelers-arr"); code == 0 || !strings.Contains(se, "go-atrm -k") {
		t.Errorf("atrm on a running job: %d %q", code, se)
	}
	if groupGone(pgid) {
		t.Fatalf("plain atrm must not have killed the job")
	}
	// -k stops the whole group (the loop AND its sleep), by name
	if so, se, code := e.run("", "atrm", "-k", "steelers-arr"); code != 0 || !strings.Contains(so, "SIGTERM") {
		t.Fatalf("atrm -k: %d %q %q", code, so, se)
	}
	waitFor(t, "process group to die", 10*time.Second, func() bool { return groupGone(pgid) })
	waitFor(t, "history record", 10*time.Second, func() bool {
		h, ok := e.job("steelers-arr", true)
		return ok && h.State == "terminated"
	})
	h, _ := e.job("steelers-arr", true)
	if h.Signal != "SIGTERM" || h.Ended == nil {
		t.Errorf("history: %+v", h)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Errorf("pid file should be removed when the job ends")
	}
	if out, _, _ := e.run("", "at", "--log", "steelers-arr"); !strings.Contains(out, "tick") || !strings.Contains(out, "terminated by SIGTERM") {
		t.Errorf("log: %q", out)
	}
	if _, se, code := e.run("", "atrm", "-k", "steelers-arr"); code == 0 {
		t.Errorf("-k on an ended job should fail: %q", se)
	}
}

func spawnSession(t *testing.T) int {
	t.Helper()
	c := exec.Command("sleep", "300")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	// reap it as soon as it exits, as init would for a real orphan; an
	// unreaped zombie still answers kill(pid, 0)
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }()
	t.Cleanup(func() { syscall.Kill(-c.Process.Pid, syscall.SIGKILL); <-done })
	return c.Process.Pid
}

// writeProcFiles records pid the way the daemon does, including its real start time.
func writeProcFiles(t *testing.T, e *env, id, pid int) {
	t.Helper()
	ticks, ok := procStartTicks(pid)
	if !ok {
		t.Skip("no /proc start times on this system")
	}
	os.WriteFile(filepath.Join(e.spool, "running", fmt.Sprintf("%d.proc", id)),
		[]byte(fmt.Sprintf("pid=%d\nstarted=%d\nstartticks=%d\n", pid, time.Now().Unix(), ticks)), 0o600)
	os.WriteFile(filepath.Join(e.spool, "running", fmt.Sprintf("%d.pid", id)), []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func writeRunning(t *testing.T, e *env, id int, extra ...string) {
	t.Helper()
	body := fmt.Sprintf("#!/bin/bash\n# atjob id=%d run=1 created=1 user=x nice=0 batch=0 name=orphan%d\nsleep 300\n", id, id)
	os.WriteFile(filepath.Join(e.spool, "seq"), []byte(strconv.Itoa(id)+"\n"), 0o600)
	os.MkdirAll(filepath.Join(e.spool, "running"), 0o700)
	if err := os.WriteFile(filepath.Join(e.spool, "running", strconv.Itoa(id)), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestCrashedJobIsNotRerun(t *testing.T) {
	e := newEnv(t)
	marker := filepath.Join(t.TempDir(), "ran")
	writeRunning(t, e, 7)
	os.WriteFile(filepath.Join(e.spool, "running", "7"),
		[]byte("#!/bin/bash\n# atjob id=7 run=1 created=1 user=x nice=0 batch=0 name=crashed\ntouch "+marker+"\n"), 0o700)
	// no pid file at all: a daemon that died right after claiming it
	stop, se := e.startDaemon()
	waitFor(t, "sweep to record it", 5*time.Second, func() bool {
		j, ok := e.job("crashed", true)
		return ok && j.State == "interrupted"
	})
	stop()
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("interrupted job was re-run")
	}
	if !strings.Contains(se.String(), "interrupted") {
		t.Errorf("daemon should say it set the job aside: %q", se.String())
	}
	if _, err := os.Stat(filepath.Join(e.spool, "logs", "7.interrupted")); err != nil {
		t.Errorf("interrupted job script should be kept in logs/: %v", err)
	}
	if out, _, _ := e.run("", "atq", "--all"); !strings.Contains(out, "interrupted") {
		t.Errorf("atq --all should say 'interrupted':\n%s", out)
	}
}

// A job that outlives its daemon must not be mislabeled as interrupted while
// it is still running, and can still be stopped.
func TestLiveOrphanIsLeftAlone(t *testing.T) {
	e := newEnv(t)
	pid := spawnSession(t)
	writeRunning(t, e, 8)
	writeProcFiles(t, e, 8, pid)
	stop, se := e.startDaemon()
	defer stop()
	time.Sleep(2500 * time.Millisecond) // several sweeps

	if syscall.Kill(pid, 0) != nil {
		t.Fatalf("daemon touched a job that is still running")
	}
	j, ok := e.job("orphan8", false)
	if !ok || j.State != "running" || j.PGID != pid {
		t.Errorf("live orphan should read as running with its pgid: %+v ok=%v", j, ok)
	}
	if !strings.Contains(se.String(), "still running") {
		t.Errorf("daemon should mention it: %q", se.String())
	}
	if _, _, code := e.run("", "atrm", "-k", "orphan8"); code != 0 {
		t.Fatalf("could not stop the orphan")
	}
	waitFor(t, "orphan to die", 5*time.Second, func() bool { return groupGone(pid) })
	waitFor(t, "daemon to record it as interrupted", 5*time.Second, func() bool {
		h, ok := e.job("orphan8", true)
		return ok && h.State == "interrupted"
	})
}

// A stale pid file must never get an unrelated process killed.
func TestStalePidIsNeverKilled(t *testing.T) {
	e := newEnv(t)

	// (a) the pid now belongs to a process that does not lead its own group
	notLeader := exec.Command("sleep", "300")
	if err := notLeader.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { notLeader.Process.Kill(); notLeader.Wait() })
	writeRunning(t, e, 9)
	os.WriteFile(filepath.Join(e.spool, "running", "9.pid"), []byte(strconv.Itoa(notLeader.Process.Pid)+"\n"), 0o600)

	// (b) it does lead a group, but it is not the process we recorded
	leader := spawnSession(t)
	writeRunning(t, e, 10)
	os.WriteFile(filepath.Join(e.spool, "running", "10.pid"), []byte(strconv.Itoa(leader)+"\n"), 0o600)
	os.WriteFile(filepath.Join(e.spool, "running", "10.proc"), []byte("pid=1\nstarted=1\nstartticks=1\n"), 0o600)
	if _, ok := procStartTicks(leader); !ok {
		t.Skip("no /proc start times on this system")
	}

	for _, name := range []string{"orphan9", "orphan10"} {
		j, ok := e.job(name, false)
		if !ok || j.State == "running" || j.PGID != 0 {
			t.Errorf("%s has a stale pid and must not be listed as running: %+v ok=%v", name, j, ok)
		}
	}
	for _, ref := range []string{"9", "10"} {
		if _, se, code := e.run("", "atrm", "-k", ref); code == 0 {
			t.Errorf("atrm -k %s should refuse (stale pid): %q", ref, se)
		}
	}
	if syscall.Kill(notLeader.Process.Pid, 0) != nil || syscall.Kill(leader, 0) != nil {
		t.Errorf("an unrelated process was killed")
	}
}

func TestHistoryIsPruned(t *testing.T) {
	e := newEnv(t)
	e.extra = []string{"AT_KEEP_DAYS=1"}
	old := time.Now().AddDate(0, 0, -3)
	fresh := time.Now()
	for _, d := range []string{"history", "logs"} {
		if err := os.MkdirAll(filepath.Join(e.spool, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for id, ended := range map[int]time.Time{3: old, 4: fresh} {
		rec, _ := json.Marshal(Job{ID: id, Name: fmt.Sprintf("h%d", id), State: "finished", Ended: &ended})
		if err := os.WriteFile(filepath.Join(e.spool, "history", fmt.Sprintf("%d.json", id)), rec, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e.spool, "logs", fmt.Sprintf("%d.log", id)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "old record to be pruned", 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(e.spool, "history", "3.json"))
		return err != nil
	})
	if _, err := os.Stat(filepath.Join(e.spool, "logs", "3.log")); err == nil {
		t.Errorf("pruned job's log should go too")
	}
	if _, err := os.Stat(filepath.Join(e.spool, "history", "4.json")); err != nil {
		t.Errorf("recent record must be kept")
	}
}

func TestDaemonDetachIsIdempotent(t *testing.T) {
	e := newEnv(t)
	_, se, code := e.run("", "atd", "-d")
	m := regexp.MustCompile(`pid (\d+)`).FindStringSubmatch(se)
	if code != 0 || m == nil {
		t.Fatalf("go-atd -d: exit %d: %s", code, se)
	}
	pid, _ := strconv.Atoi(m[1])
	defer syscall.Kill(pid, syscall.SIGTERM)

	// watchdog-style repeat: silent no-op, exit 0
	if out, se, code := e.run("", "atd", "-d"); code != 0 || out != "" || se != "" {
		t.Errorf("repeat go-atd -d should be silent: exit %d out %q err %q", code, out, se)
	}
	if _, se, _ := e.run("true\n", "at", "now", "+", "1", "hour"); strings.Contains(se, "not running") {
		t.Errorf("go-at warned although the daemon runs: %q", se)
	}
	syscall.Kill(pid, syscall.SIGTERM)
	waitFor(t, "daemon to exit", 5*time.Second, func() bool { return syscall.Kill(pid, 0) != nil })
	if _, se, _ := e.run("true\n", "at", "now", "+", "1", "hour"); !strings.Contains(se, "not running") {
		t.Errorf("expected a warning with no daemon, got %q", se)
	}
}

func TestAutostart(t *testing.T) {
	e := newEnv(t)
	e.extra = []string{"AT_AUTOSTART=1"}
	_, se, code := e.run("true\n", "at", "now", "+", "1", "hour")
	m := regexp.MustCompile(`started go-atd \(pid (\d+)\)`).FindStringSubmatch(se)
	if code != 0 || m == nil {
		t.Fatalf("expected autostart message, got %d %q", code, se)
	}
	pid, _ := strconv.Atoi(m[1])
	syscall.Kill(pid, syscall.SIGTERM)
}

func TestOverdueWarning(t *testing.T) {
	e := newEnv(t)
	e.submit("true\n", "now", "+", "1", "second")
	time.Sleep(4500 * time.Millisecond)
	_, se, _ := e.run("", "atq")
	if !strings.Contains(se, "not running") || !strings.Contains(se, "overdue") {
		t.Errorf("atq should warn about overdue jobs with no daemon: %q", se)
	}
}
