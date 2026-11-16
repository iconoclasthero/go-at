package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func nyLoc(t *testing.T) *time.Location {
	t.Helper()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	return ny
}

// ---- issue #1: am/pm by default, format from the config file ---------------

func TestListUsesAMPMByDefault(t *testing.T) {
	e := newEnv(t)
	e.submit("true\n", "-n", "late", "2030-01-15", "23:15")
	e.submit("true\n", "-n", "early", "2030-01-16", "03:05")
	out, se, code := e.run("", "atq")
	if code != 0 {
		t.Fatalf("atq: %d %s", code, se)
	}
	for _, want := range []string{"Tue Jan 15 11:15:00 PM 2030", "Wed Jan 16 03:05:00 AM 2030"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing should contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "23:15") {
		t.Errorf("no 24-hour times by default:\n%s", out)
	}
	// the confirmation printed when a job is created uses it too
	_, se, _ = e.run("true\n", "at", "2030-01-15", "15:55")
	if !strings.Contains(se, "03:55:00 PM 2030 EST") {
		t.Errorf("submit message should be 12-hour with a zone: %q", se)
	}
}

func TestTimeFormatFromConfigFile(t *testing.T) {
	e := newEnv(t)
	e.submit("true\n", "-n", "x", "2030-01-15", "15:55")

	for _, c := range []struct{ conf, want string }{
		{"time_format = 24h\n", "Tue Jan 15 15:55:00 2030"},
		{"time_format = 12h\n", "Tue Jan 15 03:55:00 PM 2030"},
		{"time_format = \"%Y-%m-%d %l:%M%P\"\n", "2030-01-15  3:55pm"},
		{"time_format = %d.%m.%Y %H:%M   # comment\n", "15.01.2030 15:55"},
	} {
		out, se, code := e.run("", "atq", "--config", writeConf(t, c.conf))
		if code != 0 || !strings.Contains(out, c.want) {
			t.Errorf("%q: want %q in\n%s%s (exit %d)", c.conf, c.want, out, se, code)
		}
	}
	// the environment variable overrides the file
	e.extra = []string{"AT_TIME_FORMAT=24h"}
	out, _, _ := e.run("", "atq", "--config", writeConf(t, "time_format = 12h\n"))
	if !strings.Contains(out, "15:55:00") || strings.Contains(out, "PM") {
		t.Errorf("env should win over the file:\n%s", out)
	}
	// JSON is never reformatted
	e.extra = []string{"AT_TIME_FORMAT=%I %p"}
	if j := e.jobs(); len(j) != 1 || j[0].Scheduled.IsZero() {
		t.Errorf("json output must be unaffected: %+v", j)
	}
}

func TestBadTimeFormatIsReported(t *testing.T) {
	e := newEnv(t)
	_, se, code := e.run("", "atq", "--config", writeConf(t, "keep_days = 3\ntime_format = %Q\n"))
	if code == 0 || !strings.Contains(se, "line 2") || !strings.Contains(se, "time_format") {
		t.Errorf("bad config format should name the line: %d %q", code, se)
	}
	e.extra = []string{"AT_TIME_FORMAT=%Q"}
	if _, se, code := e.run("", "atq"); code == 0 || !strings.Contains(se, "AT_TIME_FORMAT") {
		t.Errorf("bad env format should be refused: %d %q", code, se)
	}
}

func TestShowConfigListsTimeFormat(t *testing.T) {
	e := newEnv(t)
	out, _, _ := e.run("", "at", "--show-config")
	if !strings.Contains(out, "time_format") || !strings.Contains(out, "12h") {
		t.Errorf("--show-config should list time_format = 12h:\n%s", out)
	}
}

// ---- issue #2: change the start time ---------------------------------------

func TestRescheduleWithSpec(t *testing.T) {
	ny := nyLoc(t)
	e := newEnv(t)
	id := e.submit("echo keep-me\n", "-n", "rs", "now", "+", "1", "hour")
	before := e.script("rs")

	_, se, code := e.run("", "at", "--reschedule", "rs", "2030-02-03", "9:30", "am")
	if code != 0 || !strings.Contains(se, "rescheduled") || !strings.Contains(se, "09:30:00 AM") {
		t.Fatalf("reschedule: %d %q", code, se)
	}
	j, ok := e.job("rs", false)
	if !ok || j.ID != id || j.State != stQueued {
		t.Fatalf("job must keep its number and stay queued: %+v %v", j, ok)
	}
	if got := j.Scheduled.In(ny).Format("2006-01-02 15:04"); got != "2030-02-03 09:30" {
		t.Errorf("scheduled %s", got)
	}
	after := e.script("rs")
	// only the run time differs: everything else in the script is byte-for-byte the same
	norm := func(s string) string { return runTokenRe.ReplaceAllString(s, " run=X") }
	if before == after || norm(before) != norm(after) {
		t.Errorf("only the run time may change:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if fi, err := os.Stat(filepath.Join(e.spool, "jobs", strconv.Itoa(id))); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("job file mode: %v %v", fi, err)
	}
	// short flag, by number, with the relative syntax from the command line
	_, se, code = e.run("", "at", "-R", strconv.Itoa(id), "2030-02-03", "09:00", "+", "2h", "55m")
	if code != 0 {
		t.Fatalf("-R: %d %q", code, se)
	}
	j, _ = e.job("rs", false)
	if got := j.Scheduled.In(ny).Format("2006-01-02 15:04"); got != "2030-02-03 11:55" {
		t.Errorf("relative spec: %s", got)
	}
	if _, se, code := e.run("", "at", "-R", "rs", "2030-02-03", "11:55"); code != 0 || !strings.Contains(se, "already runs") {
		t.Errorf("same time should be a no-op: %d %q", code, se)
	}
}

func TestRescheduleRefusals(t *testing.T) {
	e := newEnv(t)
	e.submit("true\n", "-n", "q", "now", "+", "1", "hour")
	orig, _ := e.job("q", false)
	for _, args := range [][]string{
		{"--reschedule", "q", "yesterday 9am"},
		{"--reschedule", "q", "not a time"},
		{"--reschedule", "nope", "tomorrow"},
		{"--reschedule"},
	} {
		if _, se, code := e.run("", "at", args...); code == 0 || se == "" {
			t.Errorf("%v should fail: %d %q", args, code, se)
		}
	}
	if j, _ := e.job("q", false); !j.Scheduled.Equal(orig.Scheduled) {
		t.Errorf("failed reschedules must change nothing")
	}
	// a running job cannot be rescheduled
	id := e.submit("sleep 30\n", "-n", "busy", "now")
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "job running", 10e9, func() bool { j, ok := e.job("busy", false); return ok && j.State == stRunning })
	if _, se, code := e.run("", "at", "-R", "busy", "tomorrow"); code == 0 || !strings.Contains(se, "already running") {
		t.Errorf("rescheduling a running job: %d %q", code, se)
	}
	e.run("", "atrm", "-k", strconv.Itoa(id))
}

func TestRescheduleInEditor(t *testing.T) {
	ny := nyLoc(t)
	e := newEnv(t)
	e.submit("true\n", "-n", "ed", "now", "+", "1", "hour")

	// the editor sees the current time in a parseable form above the discard line
	seen := filepath.Join(t.TempDir(), "seen")
	e.extra = []string{"VISUAL=" + fakeEditor(t, `cp "$f" `+seen+`; sed -i '1s/.*/2031-03-04 10:00:00/' "$f"`)}
	if _, se, code := e.run("", "at", "--reschedule", "ed"); code != 0 || !strings.Contains(se, "10:00:00 AM") {
		t.Fatalf("editor reschedule: %d %q", code, se)
	}
	b, _ := os.ReadFile(seen)
	lines := strings.Split(string(b), "\n")
	if _, err := time.ParseInLocation("2006-01-02 15:04:05", lines[0], ny); err != nil || !strings.Contains(string(b), rescheduleMarker) {
		t.Errorf("editor should show the time, then the discard line:\n%s", b)
	}
	if j, _ := e.job("ed", false); j.Scheduled.In(ny).Format("2006-01-02 15:04") != "2031-03-04 10:00" {
		t.Errorf("scheduled %v", j.Scheduled)
	}

	// leaving it alone, and text below the marker, change nothing
	before, _ := e.job("ed", false)
	e.extra = []string{"VISUAL=" + fakeEditor(t, `echo "bogus text below the line" >> "$f"`)}
	if _, se, code := e.run("", "at", "--reschedule", "ed"); code != 0 || !strings.Contains(se, "no changes") {
		t.Errorf("untouched editor: %d %q", code, se)
	}
	if after, _ := e.job("ed", false); !after.Scheduled.Equal(before.Scheduled) {
		t.Errorf("time changed although nothing was edited")
	}
	// a bad time or a failing editor leaves the job alone
	e.extra = []string{"VISUAL=" + fakeEditor(t, `echo "garbage in" > "$f"`)}
	if _, _, code := e.run("", "at", "--reschedule", "ed"); code == 0 {
		t.Errorf("bad time from the editor should fail")
	}
	e.extra = []string{"VISUAL=" + fakeEditor(t, `exit 3`)}
	if _, _, code := e.run("", "at", "--reschedule", "ed"); code == 0 {
		t.Errorf("failing editor should fail")
	}
	if after, _ := e.job("ed", false); !after.Scheduled.Equal(before.Scheduled) {
		t.Errorf("time changed after failures")
	}
	// no editor configured: say how to set one
	e.extra = []string{"VISUAL=", "EDITOR="}
	if _, se, code := e.run("", "at", "--reschedule", "ed"); code == 0 || !strings.Contains(se, "VISUAL") {
		t.Errorf("no editor: %d %q", code, se)
	}
}

func TestRescheduledAndRenamedJobRuns(t *testing.T) {
	e := newEnv(t)
	out := filepath.Join(t.TempDir(), "out")
	e.submit(`echo "$AT_JOB_NAME" > `+out+"\n", "now", "+", "1", "hour")
	if _, se, code := e.run("", "at", "--rename", "1", "fresh"); code != 0 {
		t.Fatalf("rename: %d %s", code, se)
	}
	if _, se, code := e.run("", "at", "--reschedule", "fresh", "in", "2", "seconds"); code != 0 {
		t.Fatalf("reschedule: %d %s", code, se)
	}
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "job ran", 20e9, func() bool { b, _ := os.ReadFile(out); return strings.TrimSpace(string(b)) == "fresh" })
}

// ---- issue #3: name or rename a job ---------------------------------------

func TestRenameJob(t *testing.T) {
	e := newEnv(t)
	id := e.submit("echo hi\nexport AT_JOB_NAME=in-the-commands\n", "now", "+", "1", "hour")
	if j, _ := e.job(strconv.Itoa(id), false); j.Name != "" {
		t.Fatalf("starts unnamed: %+v", j)
	}
	// first name
	if _, se, code := e.run("", "at", "--rename", strconv.Itoa(id), "first"); code != 0 || !strings.Contains(se, "named first") {
		t.Fatalf("name: %d %q", code, se)
	}
	if j, ok := e.job("first", false); !ok || j.ID != id || j.State != stQueued {
		t.Fatalf("after naming: %+v %v", j, ok)
	}
	s := e.script("first")
	if !strings.Contains(s, "export AT_JOB_NAME='first'\n") || !strings.Contains(s, "name=first") {
		t.Errorf("header and export must carry the name:\n%s", s)
	}
	// rename by name, then by number; the old name goes away
	if _, se, code := e.run("", "at", "--rename", "first", "second"); code != 0 || !strings.Contains(se, "renamed from first to second") {
		t.Fatalf("rename: %d %q", code, se)
	}
	if _, ok := e.job("first", false); ok {
		t.Errorf("old name must no longer resolve")
	}
	if _, se, code := e.run("", "at", "--rename", strconv.Itoa(id), "third.v2"); code != 0 {
		t.Fatalf("rename by id: %d %q", code, se)
	}
	s = e.script("third.v2")
	if strings.Count(s, "name=") != 1 || strings.Contains(s, "name=second") || strings.Contains(s, "'second'") {
		t.Errorf("stale name left behind:\n%s", s)
	}
	// the user's own commands are never touched, even ones that look like the export
	if !strings.HasSuffix(s, "# ---- user commands ----\necho hi\nexport AT_JOB_NAME=in-the-commands\n") {
		t.Errorf("commands changed:\n%s", s)
	}
	if fi, err := os.Stat(filepath.Join(e.spool, "jobs", strconv.Itoa(id))); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("job file mode: %v %v", fi, err)
	}
	if _, se, code := e.run("", "at", "--rename", "third.v2", "third.v2"); code != 0 || !strings.Contains(se, "already named") {
		t.Errorf("same name: %d %q", code, se)
	}
}

func TestRenameRefusals(t *testing.T) {
	e := newEnv(t)
	e.submit("true\n", "-n", "one", "now", "+", "1", "hour")
	e.submit("true\n", "-n", "two", "now", "+", "1", "hour")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--rename", "two", "one"}, "already used"},
		{[]string{"--rename", "two", "9starts-with-digit"}, "bad job name"},
		{[]string{"--rename", "two", "has space"}, "bad job name"},
		{[]string{"--rename", "nope", "x"}, "no such job"},
		{[]string{"--rename", "two"}, "usage"},
		{[]string{"--rename"}, "usage"},
	} {
		if _, se, code := e.run("", "at", c.args...); code == 0 || !strings.Contains(se, c.want) {
			t.Errorf("%v: want failure containing %q, got %d %q", c.args, c.want, code, se)
		}
	}
	if _, ok := e.job("two", false); !ok {
		t.Errorf("refused renames must change nothing")
	}
	// a running job is left alone
	id := e.submit("sleep 30\n", "-n", "busy", "now")
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "job running", 10e9, func() bool { j, ok := e.job("busy", false); return ok && j.State == stRunning })
	if _, se, code := e.run("", "at", "--rename", "busy", "other"); code == 0 || !strings.Contains(se, "already running") {
		t.Errorf("renaming a running job: %d %q", code, se)
	}
	e.run("", "atrm", "-k", strconv.Itoa(id))
}
