package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeEditor writes an executable script that receives the file as its last
// argument. body is shell; $f is the file.
func fakeEditor(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ed.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nf=\"$1\"\n[ $# -gt 1 ] && f=\"$2\"\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *env) script(ref string) string {
	e.t.Helper()
	out, se, code := e.run("", "at", "-c", ref)
	if code != 0 {
		e.t.Fatalf("at -c %s: %d %s", ref, code, se)
	}
	return out
}

func TestEditByNameAndID(t *testing.T) {
	e := newEnv(t)
	id := e.submit("echo one\n", "-n", "ed-me", "now", "+", "1", "hour")
	e.extra = []string{"VISUAL=" + fakeEditor(t, `echo "echo two" >> "$f"`), "EDITOR=/nonexistent"}
	if _, se, code := e.run("", "at", "--edit", "ed-me"); code != 0 || !strings.Contains(se, "updated") {
		t.Fatalf("edit by name: %d %s", code, se)
	}
	s := e.script("ed-me")
	if !strings.Contains(s, "echo one\necho two\n") {
		t.Errorf("commands not updated:\n%s", s)
	}
	// header, env and cd survive untouched, and the job is still queued, executable, named
	for _, want := range []string{"export AT_JOB_NAME='ed-me'", "# ---- user commands ----", "cd '"} {
		if !strings.Contains(s, want) {
			t.Errorf("lost %q:\n%s", want, s)
		}
	}
	if j, ok := e.job("ed-me", false); !ok || j.State != stQueued || j.ID != id {
		t.Errorf("job after edit: %+v %v", j, ok)
	}
	// by number
	if _, se, code := e.run("", "at", "--edit", strconv.Itoa(id)); code != 0 {
		t.Fatalf("edit by id: %d %s", code, se)
	}
	if strings.Count(e.script("ed-me"), "echo two") != 2 {
		t.Errorf("second edit by id not applied")
	}
	if fi, err := os.Stat(filepath.Join(e.spool, "jobs", strconv.Itoa(id))); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("job file mode: %v %v", fi, err)
	}
}

func TestEditEditorChoiceAndArgs(t *testing.T) {
	e := newEnv(t)
	e.submit("echo one\n", "-n", "args", "now", "+", "1", "hour")
	ed := fakeEditor(t, `echo "echo via-$1" >> "$f"`)
	// $EDITOR is used when $VISUAL is unset, and may carry arguments
	e.extra = []string{"VISUAL=", "EDITOR=" + ed + " --wait"}
	if _, se, code := e.run("", "at", "--edit", "args"); code != 0 {
		t.Fatalf("%d %s", code, se)
	}
	if s := e.script("args"); !strings.Contains(s, "echo via---wait") {
		t.Errorf("editor arguments not honored:\n%s", s)
	}
}

func TestEditNoChangeEmptyAndFailure(t *testing.T) {
	e := newEnv(t)
	e.submit("echo one\n", "-n", "keep", "now", "+", "1", "hour")
	before := e.script("keep")

	cases := []struct {
		name, body, want string
		code0            bool
	}{
		{"unchanged", `true`, "no changes", true},
		{"emptied", `: > "$f"`, "empty", false},
		{"blank", `printf '  \n\n' > "$f"`, "empty", false},
		{"fails", `echo "echo garbage" >> "$f"; exit 3`, "failed", false},
	}
	for _, c := range cases {
		e.extra = []string{"VISUAL=" + fakeEditor(t, c.body)}
		_, se, code := e.run("", "at", "--edit", "keep")
		if (code == 0) != c.code0 || !strings.Contains(se, c.want) {
			t.Errorf("%s: code %d, stderr %q", c.name, code, se)
		}
		if got := e.script("keep"); got != before {
			t.Errorf("%s: job was modified:\n%s", c.name, got)
		}
	}
}

func TestEditMissingNewlineAndSpecialChars(t *testing.T) {
	e := newEnv(t)
	e.submit("true\n", "-n", "nl", "now", "+", "1", "hour")
	e.extra = []string{"VISUAL=" + fakeEditor(t, `printf 'echo "$HOME" '"'"'q'"'"' é' > "$f"`)}
	if _, se, code := e.run("", "at", "--edit", "nl"); code != 0 {
		t.Fatalf("%d %s", code, se)
	}
	if s := e.script("nl"); !strings.HasSuffix(s, "echo \"$HOME\" 'q' é\n") {
		t.Errorf("content mangled:\n%s", s)
	}
}

func TestEditRefusals(t *testing.T) {
	e := newEnv(t)
	e.extra = []string{"VISUAL=" + fakeEditor(t, `echo x >> "$f"`)}
	if _, se, code := e.run("", "at", "--edit", "nope"); code == 0 || se == "" {
		t.Errorf("unknown job: %d %q", code, se)
	}
	if _, _, code := e.run("", "at", "--edit"); code == 0 {
		t.Errorf("missing job argument should fail")
	}
	// running jobs cannot be edited
	id := e.submit("sleep 30\n", "-n", "busy", "now")
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "job running", 10e9, func() bool { j, ok := e.job("busy", false); return ok && j.State == stRunning })
	if _, se, code := e.run("", "at", "--edit", "busy"); code == 0 || !strings.Contains(se, "already running") {
		t.Errorf("editing a running job: %d %q", code, se)
	}
	e.run("", "atrm", "-k", strconv.Itoa(id))
}

func TestEditLosesRaceToDaemon(t *testing.T) {
	e := newEnv(t)
	id := e.submit("echo one\n", "-n", "race", "now", "+", "1", "hour")
	i := strconv.Itoa(id)
	// the "editor" plays the daemon: it claims the job while the user is editing
	e.extra = []string{"VISUAL=" + fakeEditor(t,
		`echo "echo mine" >> "$f"; mv "$AT_SPOOL/jobs/`+i+`" "$AT_SPOOL/running/`+i+`"`)}
	_, se, code := e.run("", "at", "--edit", "race")
	if code == 0 || !strings.Contains(se, "was not updated") {
		t.Fatalf("expected refusal: %d %q", code, se)
	}
	saved, err := os.ReadFile(filepath.Join(e.spool, "edit-"+i+".rejected"))
	if err != nil || !strings.Contains(string(saved), "echo mine") {
		t.Errorf("edited text must be saved: %v %q", err, saved)
	}
	run, _ := os.ReadFile(filepath.Join(e.spool, "running", i))
	if strings.Contains(string(run), "echo mine") {
		t.Errorf("edit leaked into a claimed job")
	}
}

func TestEditedJobRuns(t *testing.T) {
	e := newEnv(t)
	out := filepath.Join(t.TempDir(), "out")
	e.submit("echo old > "+out+"\n", "-n", "runme", "now", "+", "1", "hour")
	e.extra = []string{"VISUAL=" + fakeEditor(t, `echo "echo new > `+out+`" > "$f"`)}
	if _, se, code := e.run("", "at", "--edit", "runme"); code != 0 {
		t.Fatalf("%d %s", code, se)
	}
	// make it due, then let the daemon run it
	p := filepath.Join(e.spool, "jobs")
	ents, _ := os.ReadDir(p)
	for _, en := range ents {
		b, _ := os.ReadFile(filepath.Join(p, en.Name()))
		s := string(b)
		i := strings.Index(s, "run=")
		if i < 0 {
			continue
		}
		j := strings.IndexByte(s[i:], ' ')
		s = s[:i] + "run=1" + s[i+j:]
		os.WriteFile(filepath.Join(p, en.Name()), []byte(s), 0o700)
	}
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "edited job ran", 15e9, func() bool { b, _ := os.ReadFile(out); return strings.TrimSpace(string(b)) == "new" })
}

func TestComposeCreatesJob(t *testing.T) {
	e := newEnv(t)
	e.extra = []string{"VISUAL=", "EDITOR=" + fakeEditor(t, `printf 'echo composed\n' > "$f"`)}
	_, se, code := e.run("", "at", "--compose", "--name", "cmp", "now", "+", "1", "hour")
	if code != 0 || !strings.Contains(se, "job ") {
		t.Fatalf("%d %s", code, se)
	}
	if s := e.script("cmp"); !strings.HasSuffix(s, "# ---- user commands ----\necho composed\n") {
		t.Errorf("composed commands missing:\n%s", s)
	}
	// -C is the short form; an empty buffer creates nothing
	e.extra = []string{"VISUAL=" + fakeEditor(t, `true`)}
	if _, se, code := e.run("", "at", "-C", "now", "+", "1", "hour"); code == 0 || len(e.jobs()) != 1 {
		t.Errorf("empty compose should not create a job: %d %q", code, se)
	}
	if _, _, code := e.run("", "at", "--compose", "-f", "x", "now"); code == 0 {
		t.Errorf("--compose with -f should fail")
	}
}

func TestEditorUnsetMessage(t *testing.T) {
	e := newEnv(t)
	e.submit("true\n", "-n", "u", "now", "+", "1", "hour")
	e.extra = []string{"VISUAL=", "EDITOR="}
	for _, args := range [][]string{{"--compose", "now"}, {"--edit", "u"}, {"-e", "u"}} {
		flag := strings.Replace(args[0], "-e", "--edit", 1)
		if args[0] == "--edit" {
			flag = "--edit"
		}
		_, se, code := e.run("", "at", args...)
		if code != 1 || !strings.Contains(se, `"$VISUAL" falling back to "$EDITOR" but neither of these is set`) ||
			!strings.Contains(se, "VISUAL=/usr/bin/nano go-at "+flag) {
			t.Errorf("%v: %d %q", args, code, se)
		}
	}
}

func TestSyntaxCheckOnSave(t *testing.T) {
	e := newEnv(t)
	e.submit("echo fine\n", "-n", "syn", "now", "+", "1", "hour")
	before := e.script("syn")
	e.extra = []string{"VISUAL=" + fakeEditor(t, `printf 'if true; then\necho oops\n' > "$f"`)}
	_, se, code := e.run("", "at", "-e", "syn")
	if code == 0 || !strings.Contains(se, "syntax problem") || !strings.Contains(se, "line ") {
		t.Fatalf("expected a syntax refusal: %d %q", code, se)
	}
	if e.script("syn") != before {
		t.Errorf("job changed despite syntax error")
	}
	if b, err := os.ReadFile(filepath.Join(e.spool, "edit-"+strconv.Itoa(e.jobs()[0].ID)+".rejected")); err != nil || !strings.Contains(string(b), "echo oops") {
		t.Errorf("text should be kept: %v", err)
	}
}

func TestJobDoesNotInheritDaemonEnvironment(t *testing.T) {
	e := newEnv(t)
	out := filepath.Join(t.TempDir(), "env")
	e.submit("env > "+out+"\n", "-n", "envjob", "now")
	e.extra = []string{"LEAKED_FROM_DAEMON=yes"} // only the daemon sees this
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "job ran", 15e9, func() bool { j, ok := e.job("envjob", true); return ok && j.State == stFinished })
	b, _ := os.ReadFile(out)
	if strings.Contains(string(b), "LEAKED_FROM_DAEMON") {
		t.Errorf("daemon environment leaked into the job:\n%s", b)
	}
	if !strings.Contains(string(b), "AT_JOB_NAME=envjob") {
		t.Errorf("job lost its own environment:\n%s", b)
	}
}

func TestHelp(t *testing.T) {
	e := newEnv(t)
	for _, c := range [][]string{{"at", "--help"}, {"at", "-h"}, {"atq", "--help"}, {"atrm", "--help"}, {"atd", "--help"}} {
		out, _, code := e.run("", c[0], c[1:]...)
		for _, want := range []string{"--compose", "--edit", "--name", "--nice", "--batch", "go-atrm -k", "waiting", "AT_SPOOL", "terminated"} {
			if code != 0 || !strings.Contains(out, want) {
				t.Errorf("%v: code %d, help lacks %q", c, code, want)
			}
		}
	}
}
