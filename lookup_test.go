//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// endedJob writes a history record (and saved commands) for a finished job, the
// way the daemon leaves them, with a given id.
func (e *env) endedJob(id int, name, cmds string, exit int, ended time.Time) {
	e.t.Helper()
	sp := &Spool{Dir: e.spool}
	for _, d := range []string{"jobs", "running", "logs", "history"} {
		os.MkdirAll(filepath.Join(e.spool, d), 0o700)
	}
	st := stFinished
	if exit != 0 {
		st = stFailed
	}
	st0 := ended.Add(-time.Minute)
	j := Job{ID: id, Name: name, State: st, Created: st0.Add(-time.Hour), Scheduled: st0, Started: &st0, Ended: &ended, Shell: "/bin/bash"}
	j.ExitCode = &exit
	if err := sp.writeHistory(j); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(sp.commandsPath(id), []byte(cmds), 0o600); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.spool, "seq"), []byte(strconv.Itoa(max(id, e.seq()))+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) seq() int {
	b, _ := os.ReadFile(filepath.Join(e.spool, "seq"))
	n := 0
	for _, c := range strings.TrimSpace(string(b)) {
		n = n*10 + int(c-'0')
	}
	return n
}

var fixedEnd = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func TestEndedNamesAreReusable(t *testing.T) {
	e := newEnv(t)
	e.endedJob(3, "backup", "echo old\n", 0, fixedEnd)
	// the name of an ended job can be given to a new job
	id := e.submit("echo new\n", "-n", "backup", "now", "+", "1", "hour")
	if id != 4 {
		t.Fatalf("id %d", id)
	}
	// ...and the active job wins the name
	if out := e.script("backup"); !strings.Contains(out, "echo new") {
		t.Errorf("name should mean the active job:\n%s", out)
	}
	// a second active job may not take it
	_, se, code := e.run("echo x\n", "at", "-n", "backup", "now", "+", "2", "hours")
	if code == 0 || !strings.Contains(se, "already used by job 4") {
		t.Errorf("want a refusal, got %d %q", code, se)
	}
	if len(e.jobs()) != 1 {
		t.Errorf("a refused job must not be queued")
	}
}

func TestAmbiguousEndedName(t *testing.T) {
	e := newEnv(t)
	e.endedJob(1, "w", "echo one\n", 0, fixedEnd)
	e.endedJob(2, "w", "echo two\n", 1, fixedEnd.Add(time.Hour))
	_, se, code := e.run("", "at", "-c", "w")
	if code == 0 || !strings.Contains(se, `matches 2 ended jobs`) || !strings.Contains(se, "--latest") {
		t.Fatalf("want ambiguity error, got %d %q", code, se)
	}
	// the candidates are listed, newest first
	if i, j := strings.Index(se, "\n  2 "), strings.Index(se, "\n  1 "); i < 0 || j < i {
		t.Errorf("candidates missing or not newest-first:\n%s", se)
	}
	// numbers are exact; --latest takes the newest
	if out := e.script("1"); !strings.Contains(out, "echo one") {
		t.Errorf("job 1: %s", out)
	}
	out, se, code := e.run("", "at", "--latest", "-c", "w")
	if code != 0 || !strings.Contains(out, "echo two") {
		t.Errorf("--latest: %d %q %q", code, out, se)
	}
	// a name with a single ended job needs no --latest
	e.endedJob(5, "solo", "echo solo\n", 0, fixedEnd)
	if out := e.script("solo"); !strings.Contains(out, "echo solo") {
		t.Errorf("solo: %s", out)
	}
	// unknown
	if _, se, code := e.run("", "at", "-c", "nope"); code == 0 || !strings.Contains(se, "no such job: nope") {
		t.Errorf("unknown: %d %q", code, se)
	}
}

func TestRenameEndedJob(t *testing.T) {
	e := newEnv(t)
	e.endedJob(7, "", "echo hi\n", 0, fixedEnd)
	out, se, code := e.run("", "at", "--rename", "7", "week9")
	if code != 0 || !strings.Contains(out+se, "job 7 named week9") {
		t.Fatalf("rename: %d %q %q", code, out, se)
	}
	if j, ok := e.job("7", true); !ok || j.Name != "week9" {
		t.Errorf("history record: %+v %v", j, ok)
	}
	// ended jobs may share a name, even one an active job has
	e.endedJob(8, "week9", "echo again\n", 0, fixedEnd.Add(time.Hour))
	e.submit("echo q\n", "-n", "live", "now", "+", "1", "hour")
	if _, se, code := e.run("", "at", "--rename", "8", "live"); code != 0 {
		t.Errorf("an ended job may take an active job's name: %d %q", code, se)
	}
	// but an active job may not take a name another active job has
	e.submit("echo q\n", "-n", "other", "now", "+", "1", "hour")
	if _, se, code := e.run("", "at", "--rename", "other", "live"); code == 0 || !strings.Contains(se, "already used") {
		t.Errorf("collision: %d %q", code, se)
	}
	// bad names are refused
	if _, _, code := e.run("", "at", "--rename", "7", "9bad"); code == 0 {
		t.Errorf("bad name accepted")
	}
}

func TestSwappedRenameArguments(t *testing.T) {
	e := newEnv(t)
	id := e.submit("echo q\n", "-n", "w11", "now", "+", "1", "hour")
	_, se, code := e.run("", "at", "--rename", "W11-Eagles", itoaT(id))
	if code == 0 || !strings.Contains(se, "did you mean") || !strings.Contains(se, "--rename "+itoaT(id)+" W11-Eagles") {
		t.Errorf("want a swapped-arguments hint, got %d %q", code, se)
	}
	if j, _ := e.job(itoaT(id), false); j.Name != "w11" {
		t.Errorf("nothing may change: %+v", j)
	}
}

func itoaT(n int) string { return strconv.Itoa(n) }

func TestShow(t *testing.T) {
	e := newEnv(t)
	e.endedJob(4, "done", "echo finished-job\nexit 3\n", 3, fixedEnd)
	os.WriteFile(filepath.Join(e.spool, "logs", "4.log"), []byte("line one\nline two\n"), 0o600)
	out, se, code := e.run("", "at", "--show", "4")
	if code != 0 {
		t.Fatalf("%d %q", code, se)
	}
	for _, w := range []string{"done", "Commands:", "echo finished-job", "line two", "3"} {
		if !strings.Contains(out, w) {
			t.Errorf("--show output lacks %q:\n%s", w, out)
		}
	}
	id := e.submit("echo queued-cmd\n", "-n", "q", "now", "+", "1", "hour")
	out, _, code = e.run("", "at", "--show", "q")
	if code != 0 || !strings.Contains(out, "echo queued-cmd") || !strings.Contains(out, itoaT(id)) {
		t.Errorf("queued --show: %d\n%s", code, out)
	}
	// without a job and without a terminal it is an error, not a hang
	if _, se, code := e.run("", "at", "--show"); code == 0 || !strings.Contains(se, "no job given") {
		t.Errorf("no job: %d %q", code, se)
	}
}

func TestCopy(t *testing.T) {
	e := newEnv(t)
	e.extra = []string{"VISUAL=" + fakeEditor(t, `true`), "EDITOR=/nonexistent"}
	id := e.submit("echo original\n", "-n", "orig", "--nice", "5", "--batch", "now", "+", "1", "hour")
	out, se, code := e.run("", "at", "-n", "dup", "--copy", "orig", "now", "+", "2", "hours")
	if code != 0 || !strings.Contains(se, "copied from") {
		t.Fatalf("copy: %d %q %q", code, out, se)
	}
	m := jobRe.FindStringSubmatch(se)
	cp := m[1]
	if cp == itoaT(id) {
		t.Fatal("copy reused the job number")
	}
	s := e.script(cp)
	if !strings.Contains(s, "echo original") || !strings.Contains(s, "nice=5") || !strings.Contains(s, "batch=1") {
		t.Errorf("copy lost something:\n%s", s)
	}
	// the original is untouched
	if o := e.script(itoaT(id)); !strings.Contains(o, "echo original") {
		t.Errorf("original changed")
	}
	// --nice overrides
	_, se, code = e.run("", "at", "--nice", "1", "--copy", "orig", "now", "+", "3", "hours")
	if code != 0 {
		t.Fatalf("copy --nice: %q", se)
	}
	if s := e.script(jobRe.FindStringSubmatch(se)[1]); !strings.Contains(s, "nice=1") {
		t.Errorf("--nice did not override:\n%s", s)
	}
	// TIME is required
	if _, se, code := e.run("", "at", "--copy", "orig"); code == 0 || !strings.Contains(se, "TIME") {
		t.Errorf("missing TIME: %d %q", code, se)
	}
	// can't mix with -f / --compose
	if _, _, code := e.run("", "at", "-f", "x", "--copy", "orig", "now", "+", "1", "hour"); code == 0 {
		t.Errorf("--copy with -f accepted")
	}
}

func TestCopyEditsAndFiles(t *testing.T) {
	e := newEnv(t)
	e.endedJob(2, "past", "echo from-history\n", 0, fixedEnd)
	// an ended job is copied from its saved commands; the editor can change them
	e.extra = []string{"VISUAL=" + fakeEditor(t, `echo "echo edited" >> "$f"`), "EDITOR=/nonexistent"}
	_, se, code := e.run("", "at", "--copy", "past", "now", "+", "1", "hour")
	if code != 0 {
		t.Fatalf("%q", se)
	}
	s := e.script(jobRe.FindStringSubmatch(se)[1])
	if !strings.Contains(s, "echo from-history") || !strings.Contains(s, "echo edited") {
		t.Errorf("ended-job copy or editor edit missing:\n%s", s)
	}
	// --no-edit skips the editor (this one would add a line)
	_, se, code = e.run("", "at", "--no-edit", "--copy", "past", "now", "+", "2", "hours")
	if code != 0 {
		t.Fatalf("%q", se)
	}
	if s := e.script(jobRe.FindStringSubmatch(se)[1]); strings.Contains(s, "echo edited") {
		t.Errorf("--no-edit still ran the editor")
	}
	// a file: ./ and ~/ are paths, a bare word is a job
	os.WriteFile(filepath.Join(e.spool, "cmds.sh"), []byte("echo from-file\n"), 0o600)
	for _, p := range []string{filepath.Join(e.spool, "cmds.sh"), "~/cmds.sh"} {
		_, se, code = e.run("", "at", "--no-edit", "--copy", p, "now", "+", "3", "hours")
		if code != 0 {
			t.Fatalf("copy %s: %q", p, se)
		}
		if s := e.script(jobRe.FindStringSubmatch(se)[1]); !strings.Contains(s, "echo from-file") {
			t.Errorf("file %s not used:\n%s", p, s)
		}
	}
	if _, se, code := e.run("", "at", "--no-edit", "--copy", "cmds.sh", "now", "+", "3", "hours"); code == 0 || !strings.Contains(se, "no such job") {
		t.Errorf("bare word must be a job name: %d %q", code, se)
	}
}

func TestPickerSetting(t *testing.T) {
	e := newEnv(t)
	if _, se, code := e.run("", "at", "--config", writeConf(t, "picker = sideways\n"), "-l"); code == 0 || !strings.Contains(se, "picker") {
		t.Errorf("bad picker accepted: %d %q", code, se)
	}
	out, _, code := e.run("", "at", "--config", writeConf(t, "picker = menu\n"), "--show-config")
	if code != 0 || !strings.Contains(out, "picker") || !strings.Contains(out, "menu") {
		t.Errorf("--show-config: %q", out)
	}
}

// ---- at a terminal ---------------------------------------------------------

func TestCollisionPromptSuffix(t *testing.T) {
	e := newEnv(t)
	e.submit("echo a\n", "-n", "dup", "now", "+", "1", "hour")
	p := e.pty("at", "-n", "dup", "-f", e.cmdFile(), "now", "+", "2", "hours")
	p.expect("already used by job 1")
	p.expect("Choice [1]:")
	p.send("\n") // the default: name-<job number>
	_, code := p.finish()
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, p.screen())
	}
	j, ok := e.job("dup-2", false)
	if !ok || j.ID != 2 {
		t.Errorf("want job 2 named dup-2, jobs: %+v\n%s", e.jobs(), p.screen())
	}
}

func TestCollisionPromptNewNameAndCancel(t *testing.T) {
	e := newEnv(t)
	e.submit("echo a\n", "-n", "dup", "now", "+", "1", "hour")
	p := e.pty("at", "-n", "dup", "-f", e.cmdFile(), "now", "+", "2", "hours")
	p.expect("Choice [1]:")
	p.send("2\n")
	p.expect("New name:")
	p.send("dup\n") // taken again: asked again
	p.expect("Choice [1]:")
	p.send("2\n")
	p.expect("New name:")
	p.send("fresh\n")
	if _, code := p.finish(); code != 0 {
		t.Fatalf("exit %d:\n%s", code, p.screen())
	}
	if _, ok := e.job("fresh", false); !ok {
		t.Errorf("fresh not created: %+v", e.jobs())
	}
	// cancel changes nothing
	p = e.pty("at", "-n", "fresh", "-f", e.cmdFile(), "now", "+", "2", "hours")
	p.expect("Choice [1]:")
	p.send("q\n")
	if _, code := p.finish(); code != 130 {
		t.Errorf("cancel exit %d", code)
	}
	if n := len(e.jobs()); n != 2 {
		t.Errorf("cancel queued a job: %d jobs", n)
	}
}

func TestRenameCollisionPrompt(t *testing.T) {
	e := newEnv(t)
	e.submit("echo a\n", "-n", "one", "now", "+", "1", "hour")
	id := e.submit("echo b\n", "-n", "two", "now", "+", "2", "hours")
	p := e.pty("at", "--rename", itoaT(id), "one")
	p.expect("1) use one-2")
	p.send("1\n")
	if _, code := p.finish(); code != 0 {
		t.Fatalf("exit %d:\n%s", code, p.screen())
	}
	if _, ok := e.job("one-2", false); !ok {
		t.Errorf("not renamed: %+v", e.jobs())
	}
}

func TestMenuPickerOmittedJob(t *testing.T) {
	e := newEnv(t)
	e.extra = []string{"AT_PICKER=menu"}
	e.submit("echo first\n", "-n", "aa", "now", "+", "1", "hour")
	e.submit("echo second\n", "-n", "bb", "now", "+", "2", "hours")
	p := e.pty("at", "-c")
	p.expect("[1-2, q to cancel]")
	p.send("2\n")
	out, code := p.finish()
	if code != 0 || !strings.Contains(out, "echo second") {
		t.Errorf("code %d stdout %q\n%s", code, out, p.screen())
	}
	// a wrong answer is asked again; q cancels with nothing done
	p = e.pty("at", "-c")
	p.expect("[1-2, q to cancel]")
	p.send("9\n")
	p.expect("not between 1 and 2")
	p.send("q\n")
	if _, code := p.finish(); code != 130 {
		t.Errorf("cancel exit %d", code)
	}
}

func TestPickerOff(t *testing.T) {
	e := newEnv(t)
	e.extra = []string{"AT_PICKER=off"}
	e.submit("echo first\n", "now", "+", "1", "hour")
	p := e.pty("at", "-c")
	p.expect("no job given")
	if _, code := p.finish(); code == 0 {
		t.Errorf("picker=off must not ask")
	}
}

func TestAtrmMultiSelectMenu(t *testing.T) {
	e := newEnv(t)
	e.extra = []string{"AT_PICKER=menu"}
	for i := 1; i <= 4; i++ {
		e.submit("echo q\n", "now", "+", itoaT(i), "hours")
	}
	p := e.pty("atrm")
	p.expect("numbers like 1,3 or 2-4")
	p.send("1,3-4\n")
	if _, code := p.finish(); code != 0 {
		t.Fatalf("exit %d:\n%s", code, p.screen())
	}
	left := e.jobs()
	if len(left) != 1 || left[0].ID != 2 {
		t.Errorf("want only job 2 left, got %+v", left)
	}
}

func TestBrowse(t *testing.T) {
	e := newEnv(t)
	e.extra = []string{"AT_PICKER=menu"}
	e.endedJob(1, "old", "echo old-cmd\n", 2, fixedEnd)
	e.submit("echo q\n", "-n", "soon", "now", "+", "1", "hour")
	p := e.pty("atq", "--browse")
	p.expect("[1-2, q to cancel]")
	p.send("2\n") // ended jobs come after active ones
	out, code := p.finish()
	if code != 0 || !strings.Contains(out, "echo old-cmd") {
		t.Errorf("code %d stdout %q\n%s", code, out, p.screen())
	}
}

// fzfPath is a real fzf: the one in $FZF_BIN (the CI job and a developer can
// point at it), else one on PATH.
func realFzf(t *testing.T) string {
	if p := os.Getenv("FZF_BIN"); p != "" {
		return p
	}
	if p, err := exec.LookPath("fzf"); err == nil {
		return p
	}
	t.Skip("fzf not installed (set FZF_BIN to test the fzf picker)")
	return ""
}

func TestFzfPicker(t *testing.T) {
	fzf := realFzf(t)
	e := newEnv(t)
	e.extra = []string{"PATH=" + filepath.Dir(fzf) + ":" + os.Getenv("PATH")}
	e.submit("echo first\n", "-n", "alpha", "now", "+", "1", "hour")
	e.submit("echo second\n", "-n", "bravo", "now", "+", "2", "hours")
	p := e.pty("at", "-c")
	p.expect("bravo")
	p.send("bravo")
	time.Sleep(300 * time.Millisecond)
	p.send("\r")
	out, code := p.finish()
	if code != 0 || !strings.Contains(out, "echo second") {
		t.Errorf("code %d stdout %q\n%s", code, out, p.screen())
	}
	// escape cancels
	p = e.pty("at", "-c")
	p.expect("alpha")
	time.Sleep(300 * time.Millisecond)
	p.send("\x1b")
	if _, code := p.finish(); code != 130 {
		t.Errorf("esc exit %d\n%s", code, p.screen())
	}
	// picker = fzf with no fzf on PATH is an error, not a silent menu
	e2 := &env{t: t, spool: t.TempDir()}
	e2.submit("echo q\n", "now", "+", "1", "hour")
	e2.extra = []string{"AT_PICKER=fzf", "PATH=/nonexistent"}
	p = e2.pty("at", "-c")
	p.expect("fzf is not installed")
}

// cmdFile is a file of commands for -f.
func (e *env) cmdFile() string {
	p := filepath.Join(e.spool, "typed.sh")
	os.WriteFile(p, []byte("echo typed\n"), 0o600)
	return p
}
