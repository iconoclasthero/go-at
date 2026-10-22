//go:build linux

package main

// End-to-end tests: build the real binary, run it as at/atq/atrm/atd against
// a throwaway spool, and check what a user would see.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

var binDir string

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
	for _, n := range []string{"at", "atq", "atrm", "batch", "atd"} {
		os.Symlink(exe, filepath.Join(dir, n))
	}
	binDir = dir
	os.Exit(m.Run())
}

type env struct {
	t     *testing.T
	spool string
}

func newEnv(t *testing.T) *env {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	return &env{t: t, spool: t.TempDir()}
}

func (e *env) cmd(stdin string, name string, args ...string) *exec.Cmd {
	c := exec.Command(filepath.Join(binDir, name), args...)
	c.Env = append(os.Environ(), "AT_SPOOL="+e.spool, "TZ=America/New_York", "AT_AUTOSTART=", "AT_SHELL=")
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

var jobRe = regexp.MustCompile(`job (\d+) at`)

func (e *env) submit(stdin string, args ...string) int {
	e.t.Helper()
	_, se, code := e.run(stdin, "at", args...)
	m := jobRe.FindStringSubmatch(se)
	if code != 0 || m == nil {
		e.t.Fatalf("at %v: exit %d: %s", args, code, se)
	}
	id, _ := strconv.Atoi(m[1])
	return id
}

func TestSubmitListCatRemove(t *testing.T) {
	e := newEnv(t)
	id := e.submit("echo hello\n", "now", "+", "1", "hour")
	out, _, _ := e.run("", "atq")
	if !regexp.MustCompile(fmt.Sprintf(`^%d\t.* a \S+\n$`, id)).MatchString(out) {
		t.Errorf("atq output: %q", out)
	}
	script, _, _ := e.run("", "at", "-c", strconv.Itoa(id))
	first := strings.SplitN(script, "\n", 2)[0]
	if !strings.HasPrefix(first, "#!") || filepath.Base(first) != "bash" {
		t.Errorf("job should run under bash, shebang is %q", first)
	}
	if !strings.Contains(script, "echo hello") {
		t.Errorf("job script lost the command:\n%s", script)
	}
	if _, _, code := e.run("", "atrm", strconv.Itoa(id)); code != 0 {
		t.Errorf("atrm failed")
	}
	if out, _, _ := e.run("", "atq"); out != "" {
		t.Errorf("job still listed after atrm: %q", out)
	}
	if _, se, code := e.run("", "atrm", "999"); code == 0 || !strings.Contains(se, "no such job") {
		t.Errorf("atrm of a missing job: code %d, %q", code, se)
	}
}

func TestMixedUnitsAndSeconds(t *testing.T) {
	e := newEnv(t)
	d := time.Now().AddDate(0, 0, 30)
	date := d.Format("01-02-2006")
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
		out, _, _ := e.run("", "atq")
		var line string
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, strconv.Itoa(id)+"\t") {
				line = l
			}
		}
		if !strings.Contains(line, " "+c.want+" ") {
			t.Errorf("%v: want %s in %q", c.spec, c.want, line)
		}
	}
}

func TestQueuesAndBatch(t *testing.T) {
	e := newEnv(t)
	e.submit("true\n", "-q", "c", "now", "+", "1", "hour")
	e.submit("true\n", "now", "+", "1", "hour")
	if out, _, _ := e.run("", "atq", "-q", "c"); strings.Count(out, "\n") != 1 || !strings.Contains(out, " c ") {
		t.Errorf("atq -q c: %q", out)
	}
	if out, _, _ := e.run("", "at", "-l"); strings.Count(out, "\n") != 2 {
		t.Errorf("at -l should list both jobs: %q", out)
	}
	_, se, code := e.run("true\n", "batch")
	if code != 0 || !jobRe.MatchString(se) {
		t.Fatalf("batch: %d %s", code, se)
	}
	if out, _, _ := e.run("", "atq", "-q", "b"); strings.Count(out, "\n") != 1 {
		t.Errorf("batch job should be in queue b: %q", out)
	}
	if _, _, code := e.run("true\n", "at", "-q", "ab", "now"); code == 0 {
		t.Errorf("multi-letter queue should be rejected")
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
	c := e.cmd("true\n", "at", "now", "+", "1", "hour")
	c.Dir = work
	c.Env = append(c.Env, "GOAT_TEST=it's \"quoted\" $HOME")
	var se bytes.Buffer
	c.Stderr = &se
	if err := c.Run(); err != nil {
		t.Fatal(se.String())
	}
	id := jobRe.FindStringSubmatch(se.String())[1]
	script, _, _ := e.run("", "at", "-c", id)
	if !strings.Contains(script, `export GOAT_TEST='it'\''s "quoted" $HOME'`) {
		t.Errorf("env not quoted safely:\n%s", script)
	}
	if !strings.Contains(script, "cd '"+work+"'") {
		t.Errorf("cwd not recorded:\n%s", script)
	}
	if !strings.Contains(script, "umask ") {
		t.Errorf("umask not recorded")
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

func TestDaemonRunsJobsInBash(t *testing.T) {
	e := newEnv(t)
	marker := filepath.Join(t.TempDir(), "marker")
	// arrays and [[ ]] only exist in bash
	script := fmt.Sprintf("a=(x y z); [[ ${#a[@]} -eq 3 ]] && echo \"bash:${BASH_VERSION%%%%.*}\" > %s\n", marker)
	e.submit(script, "now", "+", "1", "second")

	d := e.cmd("", "atd")
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { d.Process.Signal(syscall.SIGTERM); d.Wait() }()

	waitFor(t, "job to run", 8*time.Second, func() bool { _, err := os.Stat(marker); return err == nil })
	b, _ := os.ReadFile(marker)
	if !strings.HasPrefix(string(b), "bash:") {
		t.Errorf("job did not run under bash: %q", b)
	}
	waitFor(t, "job to leave the queue", 3*time.Second, func() bool {
		out, _, _ := e.run("", "atq")
		return out == ""
	})
	if _, se, code := e.run("", "atd"); code == 0 || !strings.Contains(se, "already running") {
		t.Errorf("second daemon should refuse: %d %q", code, se)
	}
}

func TestDaemonDetachIsIdempotent(t *testing.T) {
	e := newEnv(t)
	_, se, code := e.run("", "atd", "-d")
	m := regexp.MustCompile(`pid (\d+)`).FindStringSubmatch(se)
	if code != 0 || m == nil {
		t.Fatalf("atd -d: exit %d: %s", code, se)
	}
	pid, _ := strconv.Atoi(m[1])
	defer syscall.Kill(pid, syscall.SIGTERM)

	// watchdog-style repeat: silent no-op, exit 0
	if out, se, code := e.run("", "atd", "-d"); code != 0 || out != "" || se != "" {
		t.Errorf("repeat atd -d should be silent: exit %d out %q err %q", code, out, se)
	}
	// and `at` no longer warns
	if _, se, _ := e.run("true\n", "at", "now", "+", "1", "hour"); strings.Contains(se, "not running") {
		t.Errorf("at warned although atd runs: %q", se)
	}
	// killing it makes the warning come back
	syscall.Kill(pid, syscall.SIGTERM)
	waitFor(t, "daemon to exit", 5*time.Second, func() bool { return syscall.Kill(pid, 0) != nil })
	if _, se, _ := e.run("true\n", "at", "now", "+", "1", "hour"); !strings.Contains(se, "not running") {
		t.Errorf("expected a warning with no daemon, got %q", se)
	}
}

func TestAutostart(t *testing.T) {
	e := newEnv(t)
	c := e.cmd("true\n", "at", "now", "+", "1", "hour")
	c.Env = append(c.Env, "AT_AUTOSTART=1")
	var se bytes.Buffer
	c.Stderr = &se
	if err := c.Run(); err != nil {
		t.Fatal(se.String())
	}
	m := regexp.MustCompile(`started atd \(pid (\d+)\)`).FindStringSubmatch(se.String())
	if m == nil {
		t.Fatalf("expected autostart message, got %q", se.String())
	}
	pid, _ := strconv.Atoi(m[1])
	syscall.Kill(pid, syscall.SIGTERM)
}

func TestCrashedJobIsNotRerun(t *testing.T) {
	e := newEnv(t)
	// simulate a daemon that died mid-job: a claimed job left in running/
	os.WriteFile(filepath.Join(e.spool, "seq"), []byte("7\n"), 0o600)
	os.MkdirAll(filepath.Join(e.spool, "running"), 0o700)
	marker := filepath.Join(t.TempDir(), "ran")
	os.WriteFile(filepath.Join(e.spool, "running", "7"),
		[]byte("#!/bin/bash\n# atjob id=7 queue=a run=1 created=1 user=x\ntouch "+marker+"\n"), 0o700)
	d := e.cmd("", "atd")
	var se bytes.Buffer
	d.Stderr = &se
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	d.Process.Signal(syscall.SIGTERM)
	d.Wait()
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("interrupted job was re-run")
	}
	if !strings.Contains(se.String(), "interrupted") {
		t.Errorf("daemon should say it set the job aside: %q", se.String())
	}
	if _, err := os.Stat(filepath.Join(e.spool, "logs", "7.interrupted")); err != nil {
		t.Errorf("interrupted job script should be kept in logs/: %v", err)
	}
}
