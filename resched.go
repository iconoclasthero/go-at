package main

// go-at --reschedule JOB [TIME...]   change when a queued job runs
// go-at --rename JOB NEWNAME         name, or rename, a queued or an ended job
//
// Both only change the "# atjob" header line of the job script (and, for a
// name, the exported AT_JOB_NAME line above the commands). The job keeps its
// number, its captured environment and its commands, so nothing is killed and
// re-created. As with --edit, the change is applied under the spool lock, which
// the daemon also holds when it claims a job, and only if the job is still
// queued; if it started in the meantime nothing is changed.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const rescheduleMarker = "# ---- everything below this line is discarded ----"

var runTokenRe = regexp.MustCompile(` run=\d+`)

// setRunTime returns script with the run time in its header line replaced.
func setRunTime(script string, at time.Time) (string, error) {
	first, rest, ok := strings.Cut(script, "\n")
	if !ok {
		return "", fmt.Errorf("not a job file")
	}
	hdr, tail, ok := strings.Cut(rest, "\n")
	if !ok || !strings.HasPrefix(hdr, "# atjob ") || !runTokenRe.MatchString(hdr) {
		return "", fmt.Errorf("job header not recognised")
	}
	hdr = runTokenRe.ReplaceAllLiteralString(hdr, " run="+strconv.FormatInt(at.Unix(), 10))
	return first + "\n" + hdr + "\n" + tail, nil
}

// setJobName returns script with its name set to name (in the header and in the
// exported AT_JOB_NAME). Only the part above the user's commands is touched.
func setJobName(script, name string) (string, error) {
	head, cmds, ok := splitJobScript(script)
	if !ok {
		return "", fmt.Errorf("job has no command section")
	}
	first, rest, ok := strings.Cut(head, "\n")
	if !ok {
		return "", fmt.Errorf("not a job file")
	}
	hdr, tail, ok := strings.Cut(rest, "\n")
	if !ok || !strings.HasPrefix(hdr, "# atjob ") {
		return "", fmt.Errorf("job header not recognised")
	}
	var keep []string
	for _, f := range strings.Fields(hdr) {
		if !strings.HasPrefix(f, "name=") {
			keep = append(keep, f)
		}
	}
	hdr = strings.Join(keep, " ") + " name=" + name
	tail = replaceNameExport(tail, name)
	return first + "\n" + hdr + "\n" + tail + cmds, nil
}

// replaceNameExport sets the "export AT_JOB_NAME=..." line of the job's
// preamble. The real one is the last such line (it is written after the
// captured environment), so a captured variable that happens to contain the
// text can never be mistaken for it.
func replaceNameExport(preamble, name string) string {
	line := "export AT_JOB_NAME=" + shQuote(name) + "\n"
	const key = "\nexport AT_JOB_NAME="
	pre := "\n" + preamble // so the first line can match "\nexport..." too
	if i := strings.LastIndex(pre, key); i >= 0 {
		end := strings.IndexByte(pre[i+1:], '\n')
		if end >= 0 {
			return (pre[:i+1] + line + pre[i+1+end+1:])[1:]
		}
	}
	// no name before: put it right after AT_JOB_ID, else just before the commands
	const idKey = "\nexport AT_JOB_ID="
	if i := strings.LastIndex(pre, idKey); i >= 0 {
		if end := strings.IndexByte(pre[i+1:], '\n'); end >= 0 {
			at := i + 1 + end + 1
			return (pre[:at] + line + pre[at:])[1:]
		}
	}
	return preamble + line
}

// modifyQueued applies change to a queued job's script under the spool lock.
func (s *Spool) modifyQueued(ref string, check func(Job) error, change func(string) (string, error)) (Job, error) {
	unlock, err := s.lock()
	if err != nil {
		return Job{}, err
	}
	defer unlock()

	j, err := s.Find(ref, false)
	if err != nil {
		return Job{}, err
	}
	if j.State == stRunning {
		return Job{}, fmt.Errorf("job %s is already running; it can no longer be changed (go-atrm -k stops it)", j.label())
	}
	if !j.Active() || j.path == "" {
		return Job{}, fmt.Errorf("job %s is not queued; only queued jobs can be changed", j.label())
	}
	if check != nil {
		if err := check(j); err != nil {
			return Job{}, err
		}
	}
	raw, err := os.ReadFile(j.path)
	if err != nil {
		return Job{}, fmt.Errorf("job %s: %w (it may have just started)", j.label(), err)
	}
	out, err := change(string(raw))
	if err != nil {
		return Job{}, fmt.Errorf("job %s: %v", j.label(), err)
	}
	tmp := s.path("jobs", fmt.Sprintf(".tmp-mod-%d", j.ID))
	if err := os.WriteFile(tmp, []byte(out), 0o700); err != nil {
		return Job{}, err
	}
	if err := os.Rename(tmp, j.path); err != nil {
		os.Remove(tmp)
		return Job{}, err
	}
	return j, nil
}

// RenameJob gives a job a new name, or its first one. A queued or waiting job
// is changed in its script file; an ended one in its history record, which is
// how finished jobs can be organised afterwards. A running job cannot be
// renamed: its script has been claimed and its name is already in its
// environment.
func (s *Spool) RenameJob(j Job, name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("bad job name %q: start with a letter, then letters, digits, '.', '_' or '-' (up to 64 characters)", name)
	}
	if !j.Active() {
		return s.renameEnded(j, name)
	}
	old, err := s.modifyQueued(j.ref(), func(cur Job) error {
		if cur.Name == name {
			return errNoChange
		}
		// an ended job may share the name; an active one may not
		if owner, taken := s.activeOwner(name, cur.ID); taken {
			return nameTakenError(name, owner)
		}
		return nil
	}, func(script string) (string, error) { return setJobName(script, name) })
	return renamed(j, old, name, err)
}

func renamed(j, old Job, name string, err error) (string, error) {
	if err == errNoChange {
		return fmt.Sprintf("job %s: already named %s", j.label(), name), nil
	}
	if err != nil {
		return "", err
	}
	if old.Name == "" {
		return fmt.Sprintf("job %d named %s", old.ID, name), nil
	}
	return fmt.Sprintf("job %d renamed from %s to %s", old.ID, old.Name, name), nil
}

// renameEnded renames an ended job. Names of ended jobs need not be unique,
// so nothing is checked beyond the form of the name.
func (s *Spool) renameEnded(j Job, name string) (string, error) {
	unlock, err := s.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	b, err := os.ReadFile(s.historyPath(j.ID))
	if err != nil {
		return "", fmt.Errorf("job %s has no history record yet (it is %s); try again in a moment", j.label(), j.State)
	}
	var rec Job
	if err := json.Unmarshal(b, &rec); err != nil || rec.ID != j.ID {
		return "", fmt.Errorf("job %s: its history record cannot be read", j.label())
	}
	old := rec
	if rec.Name == name {
		return renamed(j, old, name, errNoChange)
	}
	rec.Name = name
	if err := s.writeHistory(rec); err != nil {
		return "", err
	}
	return renamed(j, old, name, nil)
}

var errNoChange = fmt.Errorf("no change")

// RescheduleJob changes when a queued job runs. spec is a time spec exactly as
// on the command line ("tomorrow 9am", "2026-11-11 11:00 + 2h 55m"); when it is
// empty the current time is opened in the editor.
func (s *Spool) RescheduleJob(ref, spec string) (string, error) {
	j, err := s.Find(ref, false)
	if err != nil {
		return "", err
	}
	if j.State == stRunning {
		return "", fmt.Errorf("job %s is already running; it can no longer be rescheduled (go-atrm -k stops it)", j.label())
	}
	if !j.Active() || j.path == "" {
		return "", fmt.Errorf("job %s is not queued; only queued jobs can be rescheduled", j.label())
	}
	if strings.TrimSpace(spec) == "" {
		spec, err = editRunTime(j)
		if err != nil {
			return "", err
		}
		if spec == "" {
			return fmt.Sprintf("job %s: no changes", j.label()), nil
		}
	}
	at, err := ParseSpec(spec, time.Now(), time.Local)
	if err != nil {
		return "", err
	}
	if at.Equal(j.Scheduled) {
		return fmt.Sprintf("job %s: already runs at %s", j.label(), fmtWhenZone(at.Local())), nil
	}
	if _, err := s.modifyQueued(ref, nil, func(script string) (string, error) { return setRunTime(script, at) }); err != nil {
		return "", err
	}
	return fmt.Sprintf("job %s rescheduled: now runs %s", j.label(), fmtWhenZone(at.Local())), nil
}

// editRunTime shows the job's start time in $VISUAL / $EDITOR, in a form that
// the time parser accepts, above a line that marks the rest as discarded (like
// "systemctl edit"). It returns the new spec, or "" when the user left it alone.
func editRunTime(j Job) (string, error) {
	editor, err := editorCommand("--reschedule")
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "go-at-edit-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	cur := j.Scheduled.Local().Format("2006-01-02 15:04:05")
	initial := cur + "\n\n" + rescheduleMarker + `
# Change the line above to when the job should run, in any form go-at accepts:
#   2026-11-25 09:30            tomorrow 9am            +90m
#   2026-11-11 11:00 + 2h 55m   sunday 9:30 am ET + 1 hour
# Leave it as it is (or empty) to keep the current time.
`
	tmp := filepath.Join(dir, fmt.Sprintf("go-at-job-%d-time", j.ID))
	if err := os.WriteFile(tmp, []byte(initial), 0o600); err != nil {
		return "", err
	}
	if err := runEditor(editor, tmp); err != nil {
		return "", fmt.Errorf("editor %q failed (%v); job %s left unchanged", editor, err, j.label())
	}
	b, err := os.ReadFile(tmp)
	if err != nil {
		return "", fmt.Errorf("cannot read the edited file: %w", err)
	}
	text := string(b)
	if i := strings.Index(text, rescheduleMarker); i >= 0 {
		text = text[:i]
	}
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	spec := strings.Join(lines, " ")
	if spec == cur {
		return "", nil
	}
	return spec, nil
}
