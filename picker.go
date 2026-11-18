package main

// Choosing a job, and settling a name that is already taken.
//
// Both only ever happen when a person is at a terminal (stdin and stderr).
// In a script nothing is asked: a command that needs a choice fails and says
// what the choices are. The setting "picker" (AT_PICKER) selects how:
//
//	auto  fzf if it is installed, otherwise a numbered menu   (default)
//	fzf   fzf only (an error if it is not installed)
//	menu  the numbered menu, even if fzf is installed
//	off   never ask; fail and list the choices
//
// fzf is run as an external program, with the job list on its stdin and a
// preview pane that calls go-at itself ("go-at --show ID"); nothing of it is
// built into go-at.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func checkPicker(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "auto", "fzf", "menu", "off":
		return nil
	}
	return fmt.Errorf("%q is not auto, fzf, menu or off", v)
}

func pickerMode() string {
	if v := strings.ToLower(strings.TrimSpace(cfg("AT_PICKER"))); v != "" {
		return v
	}
	return "auto"
}

// interactive: a person is at the keyboard and can see prompts.
func interactive() bool {
	return isTerminal(int(os.Stdin.Fd())) && isTerminal(int(os.Stderr.Fd()))
}

// canPick: a choice may be put to the user now.
func canPick() bool { return pickerMode() != "off" && interactive() }

// configFlagPath is the --config FILE the program was started with, so that
// a preview run by fzf reads the same settings.
var configFlagPath string

// inReader is the one reader of the terminal's lines, so that nothing a prompt
// has already buffered is lost to the next one.
var inReader = bufio.NewReader(os.Stdin)

func promptLine(msg string) (string, bool) {
	fmt.Fprint(os.Stderr, msg)
	line, err := inReader.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(os.Stderr)
		return "", false
	}
	return strings.TrimSpace(line), true
}

// pickJobs lets the user choose from jobs (one, or several with multi).
// It returns errCancelled if they back out.
func pickJobs(jobs []Job, prompt string, multi bool) ([]Job, error) {
	if len(jobs) == 0 {
		return nil, fmt.Errorf("there are no jobs to choose from")
	}
	switch mode := pickerMode(); mode {
	case "menu":
		return pickMenu(jobs, prompt, multi)
	case "fzf":
		if _, err := exec.LookPath("fzf"); err != nil {
			return nil, fmt.Errorf("picker is set to fzf but fzf is not installed (set picker = auto or menu)")
		}
		return pickFzf(jobs, prompt, multi)
	default: // auto
		if _, err := exec.LookPath("fzf"); err == nil {
			return pickFzf(jobs, prompt, multi)
		}
		return pickMenu(jobs, prompt, multi)
	}
}

// selected maps the first word of each chosen line (the job id) back to jobs.
func selected(jobs []Job, ids []string) []Job {
	var out []Job
	for _, id := range ids {
		n, err := strconv.Atoi(id)
		if err != nil {
			continue
		}
		for _, j := range jobs {
			if j.ID == n {
				out = append(out, j)
				break
			}
		}
	}
	return out
}

func pickFzf(jobs []Job, prompt string, multi bool) ([]Job, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	preview := shQuote(exe)
	if configFlagPath != "" {
		preview += " --config " + shQuote(configFlagPath)
	}
	preview += " --show {1}"
	args := []string{
		"--header-lines=1", "--layout=reverse", "--no-sort",
		"--prompt=" + prompt + "> ",
		"--preview=" + preview, "--preview-window=right,60%,wrap",
	}
	if multi {
		args = append(args, "--multi", "--header=tab: mark several, enter: choose, esc: cancel")
	} else {
		args = append(args, "--header=enter: choose, esc: cancel")
	}
	// the header line is fzf's first line ("--header-lines=1"); the rows follow
	cmd := exec.Command("fzf", args...)
	cmd.Stdin = strings.NewReader(strings.Join(jobTable(jobs), "\n") + "\n")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (ee.ExitCode() == 130 || ee.ExitCode() == 1) {
			return nil, errCancelled // esc / ctrl-c, or nothing matched
		}
		return nil, fmt.Errorf("fzf: %v", err)
	}
	var ids []string
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			ids = append(ids, f[0])
		}
	}
	got := selected(jobs, ids)
	if len(got) == 0 {
		return nil, errCancelled
	}
	return got, nil
}

func pickMenu(jobs []Job, prompt string, multi bool) ([]Job, error) {
	lines := jobTable(jobs)
	fmt.Fprintf(os.Stderr, "    %s\n", lines[0])
	for i, l := range lines[1:] {
		fmt.Fprintf(os.Stderr, "%3d %s\n", i+1, l)
	}
	ask := fmt.Sprintf("%s [1-%d, q to cancel]: ", prompt, len(jobs))
	if multi {
		ask = fmt.Sprintf("%s [numbers like 1,3 or 2-4, q to cancel]: ", prompt)
	}
	for {
		ans, ok := promptLine(ask)
		if !ok || ans == "" || strings.EqualFold(ans, "q") {
			return nil, errCancelled
		}
		idx, err := parseChoices(ans, len(jobs), multi)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			continue
		}
		var out []Job
		for _, i := range idx {
			out = append(out, jobs[i-1])
		}
		return out, nil
	}
}

// parseChoices reads "2", or with multi "1,3" and "2-4", as 1-based numbers.
func parseChoices(ans string, n int, multi bool) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	add := func(i int) error {
		if i < 1 || i > n {
			return fmt.Errorf("%d is not between 1 and %d", i, n)
		}
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
		return nil
	}
	for _, part := range strings.FieldsFunc(ans, func(r rune) bool { return r == ',' || r == ' ' }) {
		if lo, hi, ok := strings.Cut(part, "-"); ok && multi {
			a, e1 := strconv.Atoi(lo)
			b, e2 := strconv.Atoi(hi)
			if e1 != nil || e2 != nil || a > b {
				return nil, fmt.Errorf("%q is not a range like 2-4", part)
			}
			for i := a; i <= b; i++ {
				if err := add(i); err != nil {
					return nil, err
				}
			}
			continue
		}
		i, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", part)
		}
		if err := add(i); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 || (!multi && len(out) != 1) {
		return nil, fmt.Errorf("choose one number")
	}
	return out, nil
}

// ---- a name that is already taken -----------------------------------------

// suffixedName is name-ID, shortened if need be to stay a valid name.
func suffixedName(name string, id int) string {
	suf := "-" + strconv.Itoa(id)
	if len(name)+len(suf) > 64 {
		name = name[:64-len(suf)]
	}
	return name + suf
}

// activeOwner is the active job (other than exceptID) that has name.
func (s *Spool) activeOwner(name string, exceptID int) (Job, bool) {
	a, err := s.Active()
	if err != nil {
		return Job{}, false
	}
	for _, j := range a {
		if j.Name == name && j.ID != exceptID {
			return j, true
		}
	}
	return Job{}, false
}

func nameTakenError(name string, owner Job) error {
	return fmt.Errorf("the name %q is already used by job %d (%s); pick another name or remove that job first", name, owner.ID, owner.State)
}

// settleName makes sure name is free among active jobs. When it is not and a
// person is at the terminal they choose between name-<job number> and a new
// name, instead of having to start over; anywhere else it is an error.
// selfID is the job being renamed, or 0 for a job about to be created (whose
// number is not known yet: suffixAtCreate says to append it when it is).
func (s *Spool) settleName(name string, selfID int) (final string, suffixAtCreate bool, err error) {
	for {
		owner, taken := s.activeOwner(name, selfID)
		if !taken {
			return name, false, nil
		}
		if !interactive() {
			return "", false, nameTakenError(name, owner)
		}
		fmt.Fprintf(os.Stderr, "go-at: the name %q is already used by job %d (%s).\n", name, owner.ID, owner.State)
		offer := ""
		switch {
		case selfID == 0:
			fmt.Fprintf(os.Stderr, "  1) use %s-<job number>  (this job's number is appended)\n", name)
			offer = "1"
		default:
			if sn := suffixedName(name, selfID); !s.nameInUse(sn, selfID) {
				fmt.Fprintf(os.Stderr, "  1) use %s\n", sn)
				offer = "1"
			}
		}
		fmt.Fprintln(os.Stderr, "  2) enter a different name\n  q) cancel")
		def := "2"
		if offer != "" {
			def = "1"
		}
		ans, ok := promptLine("Choice [" + def + "]: ")
		if !ok || strings.EqualFold(ans, "q") {
			return "", false, errCancelled
		}
		if ans == "" {
			ans = def
		}
		switch {
		case ans == "1" && offer != "":
			if selfID == 0 {
				return name, true, nil
			}
			return suffixedName(name, selfID), false, nil
		case ans == "2":
			nn, ok := promptLine("New name: ")
			if !ok {
				return "", false, errCancelled
			}
			if !nameRe.MatchString(nn) {
				fmt.Fprintln(os.Stderr, "  a name starts with a letter, then letters, digits, '.', '_' or '-' (up to 64 characters)")
				continue
			}
			name = nn // loop: the new name may be taken too
		default:
			fmt.Fprintln(os.Stderr, "  please answer 1, 2 or q")
		}
	}
}

func (s *Spool) nameInUse(name string, exceptID int) bool {
	_, taken := s.activeOwner(name, exceptID)
	return taken
}
