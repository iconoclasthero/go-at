package main

// go-at --edit JOB: open a queued job's commands in $VISUAL / $EDITOR and put
// the result back.
//
// Only the user's commands are edited. The header (time, name, nice), the
// exported environment and the cd line stay exactly as they were captured when
// the job was submitted. The editor sees a private 0700 temp directory, so
// nano's backups/.save files never land anywhere world-readable.
//
// Races: the daemon claims a job by renaming it out of jobs/ while holding the
// spool lock. We take the same lock for the final swap and re-check that the
// job is still queued and its commands are still what we started from. If the
// job started (or changed) while the editor was open, nothing is applied and
// the edited text is saved to a file so it is never lost.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const cmdMarker = "\n# ---- user commands ----\n"

// splitJobScript splits a job script into everything up to and including the
// marker line, and the user's commands.
func splitJobScript(script string) (head, cmds string, ok bool) {
	i := strings.Index(script, cmdMarker)
	if i < 0 {
		return "", "", false
	}
	n := i + len(cmdMarker)
	return script[:n], script[n:], true
}

// editorCommand picks $VISUAL, then $EDITOR, then the first of editor, nano, vi
// that exists. The value may carry arguments ("code -w", "nano -l"), so it is
// run through sh and gets the file as "$1".
func editorCommand() string {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if e := strings.TrimSpace(os.Getenv(v)); e != "" {
			return e
		}
	}
	for _, e := range []string{"editor", "nano", "vi"} {
		if p, err := exec.LookPath(e); err == nil {
			return p
		}
	}
	return ""
}

// runEditor runs the editor on file with the real terminal attached.
func runEditor(editor, file string) error {
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "go-at-edit", file)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Ctrl-C belongs to the editor; don't let it kill us before cleanup.
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGQUIT)
	defer signal.Stop(sig)
	return cmd.Run()
}

// EditJob returns a message to print, or an error.
func (s *Spool) EditJob(ref string) (string, error) {
	j, err := s.Find(ref, false)
	if err != nil {
		return "", err
	}
	if j.State == stRunning {
		return "", fmt.Errorf("job %s is already running; it can no longer be edited (go-atrm -k stops it)", j.label())
	}
	if !j.Active() || j.path == "" {
		return "", fmt.Errorf("job %s is not queued; only queued jobs can be edited", j.label())
	}
	raw, err := os.ReadFile(j.path)
	if err != nil {
		return "", fmt.Errorf("job %s: %w", j.label(), err)
	}
	_, orig, ok := splitJobScript(string(raw))
	if !ok {
		return "", fmt.Errorf("job %s has no command section; refusing to edit", j.label())
	}

	editor := editorCommand()
	if editor == "" {
		return "", errors.New("no editor found: set $VISUAL or $EDITOR")
	}
	dir, err := os.MkdirTemp("", "go-at-edit-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, fmt.Sprintf("go-at-job-%d.sh", j.ID))
	if err := os.WriteFile(tmp, []byte(orig), 0o600); err != nil {
		return "", err
	}

	if err := runEditor(editor, tmp); err != nil {
		return "", fmt.Errorf("editor %q failed (%v); job %s left unchanged", editor, err, j.label())
	}
	b, err := os.ReadFile(tmp)
	if err != nil {
		return "", fmt.Errorf("cannot read the edited file: %w; job %s left unchanged", err, j.label())
	}
	edited := string(b)
	if edited == orig {
		return fmt.Sprintf("job %s: no changes", j.label()), nil
	}
	if strings.TrimSpace(edited) == "" {
		return "", fmt.Errorf("the edited job is empty; job %s left unchanged (use go-atrm to delete it)", j.label())
	}
	if !strings.HasSuffix(edited, "\n") {
		edited += "\n"
	}

	unlock, err := s.lock()
	if err != nil {
		return "", err
	}
	defer unlock()

	// Still queued, and still what we started from?
	cur, err := os.ReadFile(j.path)
	var curHead, curCmds string
	if err == nil {
		curHead, curCmds, ok = splitJobScript(string(cur))
	}
	if err != nil || !ok || curCmds != orig {
		saved := s.path(fmt.Sprintf("edit-%d.rejected", j.ID))
		os.WriteFile(saved, []byte(edited), 0o600)
		why := "it started running (or was removed)"
		if err == nil && ok {
			why = "it was changed by someone else"
		}
		return "", fmt.Errorf("job %s was not updated: %s while you were editing. Your text is saved in %s", j.label(), why, saved)
	}
	tmpJob := s.path("jobs", fmt.Sprintf(".tmp-edit-%d", j.ID))
	if err := os.WriteFile(tmpJob, []byte(curHead+edited), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(tmpJob, j.path); err != nil {
		os.Remove(tmpJob)
		return "", err
	}
	return fmt.Sprintf("job %s updated", j.label()), nil
}
