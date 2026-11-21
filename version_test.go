package main

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFormatVersion(t *testing.T) {
	base := buildInfo{Go: "go1.24.7", OS: "linux", Arch: "amd64"}
	rev := "8a86b72aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cases := []struct {
		mod  func(*buildInfo)
		want string
	}{
		{func(b *buildInfo) { b.Revision, b.Time, b.CGO = rev, "2026-11-16T21:03:11Z", "0" },
			"go-at 8a86b72 (2026-11-16 21:03 UTC), go1.24.7 linux/amd64, static"},
		{func(b *buildInfo) { b.Revision, b.Time, b.CGO, b.Modified = rev, "2026-11-16T21:03:11Z", "1", true },
			"go-at 8a86b72+modified (2026-11-16 21:03 UTC), go1.24.7 linux/amd64, dynamic (cgo)"},
		{func(b *buildInfo) { b.Version, b.Revision = "v1.2.3", rev },
			"go-at v1.2.3 8a86b72, go1.24.7 linux/amd64"},
		{func(b *buildInfo) { b.Version = "v1.2.3" },
			"go-at v1.2.3, go1.24.7 linux/amd64"},
		{func(b *buildInfo) { b.CGO = "0" },
			"go-at (unknown commit: not built from a git checkout), go1.24.7 linux/amd64, static"},
	}
	for _, c := range cases {
		b := base
		c.mod(&b)
		if got := formatVersion(b); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	e := newEnv(t)
	for _, c := range [][]string{{"at", "--version"}, {"at", "-V"}, {"atq", "--version"}, {"atrm", "--version"}, {"batch", "-V"}, {"atd", "--version"}} {
		out, se, code := e.run("", c[0], c[1:]...)
		if code != 0 || !strings.HasPrefix(out, "go-at ") || strings.Count(out, "\n") != 1 || se != "" {
			t.Errorf("%v: code %d stdout %q stderr %q", c, code, out, se)
		}
	}
	// it needs no spool, no config and no daemon, and does not create anything
	if len(e.jobs()) != 0 {
		t.Errorf("--version must not touch the spool")
	}
}

func TestDaemonLogsItsVersion(t *testing.T) {
	e := newEnv(t)
	stop, stderr := e.startDaemon()
	defer stop()
	waitFor(t, "daemon start-up lines", 5*time.Second, func() bool { return strings.Contains(stderr.String(), "watching") })
	log := stderr.String()
	if n := strings.Count(log, "go-atd: version "); n != 1 {
		t.Fatalf("want one version line, got %d:\n%s", n, log)
	}
	// it comes before "watching" and names the platform, like --version does
	// (the daemon binary is built separately by the harness, so its commit differs from the test's)
	i, j := strings.Index(log, "go-atd: version "), strings.Index(log, "go-atd: watching")
	line := log[i : i+strings.Index(log[i:], "\n")]
	if i < 0 || j < i || !strings.Contains(line, runtime.GOOS+"/"+runtime.GOARCH) || strings.Contains(line, "go-at go-at") {
		t.Errorf("version line missing, misplaced or malformed:\n%s", log)
	}
}

// Every line the daemon logs, and the markers go-at adds to a job's log, start
// with a timestamp, for tracing.
func TestLogTimestamps(t *testing.T) {
	e := newEnv(t)
	id := e.submit("echo out\nexit 3\n", "-n", "stamped", "now", "+", "1", "second")
	stop, stderr := e.startDaemon()
	defer stop()
	waitFor(t, "job to end", 10*time.Second, func() bool {
		j, ok := e.job("stamped", true)
		return ok && j.State == "failed"
	})
	ts := `\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d[+-]\d\d:\d\d` // what date -Is prints
	lineRe := regexp.MustCompile(`^` + ts + ` go-atd: `)
	log := strings.TrimSpace(stderr.String())
	for _, l := range strings.Split(log, "\n") {
		if !lineRe.MatchString(l) {
			t.Errorf("daemon log line without a timestamp: %q", l)
		}
	}
	for _, want := range []string{"go-atd: version ", "go-atd: watching ", fmt.Sprintf("go-atd: job %d (stamped) started, process group", id), "failed"} {
		if !strings.Contains(log, want) {
			t.Errorf("daemon log lacks %q:\n%s", want, log)
		}
	}
	out, _, _ := e.run("", "at", "--log", "stamped")
	markers := regexp.MustCompile(`(?m)^\[`+ts+` go-at: job \d+ \(stamped\) (started, process group \d+|exited 3 after \S+)\]$`).FindAllString(out, -1)
	if len(markers) != 2 || !strings.Contains(out, "\nout\n") {
		t.Errorf("want a stamped start and exit marker around the job's own output, got:\n%s", out)
	}
}

func TestLogTimeFormatSetting(t *testing.T) {
	e := newEnv(t)
	e.submit("echo hi\n", "-n", "fmt", "now", "+", "1", "second")
	e.extra = []string{"AT_LOG_TIME_FORMAT=<%s>"}
	stop, stderr := e.startDaemon()
	defer stop()
	waitFor(t, "job to end", 10*time.Second, func() bool { j, ok := e.job("fmt", true); return ok && j.State == "finished" })
	if !regexp.MustCompile(`(?m)^<\d{10}> go-atd: watching`).MatchString(stderr.String()) {
		t.Errorf("custom log format not used:\n%s", stderr.String())
	}
	if out, _, _ := e.run("", "at", "--log", "fmt"); !regexp.MustCompile(`\[<\d{10}> go-at: job \d+ \(fmt\) started`).MatchString(out) {
		t.Errorf("custom log format not used in the job log:\n%s", out)
	}
	// a bad format never stops anything: commands work, the daemon runs the job,
	// says so in its log, and stamps with the default
	for _, how := range []string{"env", "file"} {
		e2 := &env{t: t, spool: t.TempDir()}
		bad := "%Q"
		if how == "env" {
			e2.extra = []string{"AT_LOG_TIME_FORMAT=" + bad}
		} else {
			e2.extra = []string{"AT_CONFIG=" + writeConf(t, "log_time_format = "+bad+"\n")}
		}
		if _, se, code := e2.run("", "atq"); code != 0 {
			t.Errorf("%s: a bad log_time_format must not break go-atq: %d %q", how, code, se)
		}
		e2.submit("echo hi\n", "-n", "bad", "now", "+", "1", "second")
		stop2, log2 := e2.startDaemon()
		waitFor(t, "job to end with a bad log format ("+how+")", 10*time.Second, func() bool { j, ok := e2.job("bad", true); return ok && j.State == "finished" })
		stop2()
		l := log2.String()
		if !strings.Contains(l, `log_time_format "%Q" is not usable`) || !strings.Contains(l, "using iso") {
			t.Errorf("%s: no note about the bad format in the daemon log:\n%s", how, l)
		}
		for _, line := range strings.Split(strings.TrimSpace(l), "\n") {
			if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d[+-]\d\d:\d\d go-atd: `).MatchString(line) {
				t.Errorf("%s: line not stamped with the default: %q", how, line)
			}
		}
		if out, _, _ := e2.run("", "at", "--log", "bad"); !regexp.MustCompile(`\[\d{4}-\d\d-\d\dT\S+ go-at: job \d+ \(bad\) started`).MatchString(out) {
			t.Errorf("%s: job log marker not stamped with the default:\n%s", how, out)
		}
		if so, _, _ := e2.run("", "at", "--show-config"); !strings.Contains(so, "not usable") {
			t.Errorf("%s: --show-config does not warn:\n%s", how, so)
		}
	}
	e.extra = nil
	out, _, _ := e.run("", "at", "--config", writeConf(t, "log_time_format = iso\n"), "--show-config")
	if !strings.Contains(out, "log_time_format") {
		t.Errorf("--show-config lacks the setting:\n%s", out)
	}
}
