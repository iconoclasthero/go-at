package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Spool layout (per user, no root or setuid needed):
//
//	<spool>/jobs/<id>            queued job: an executable bash script
//	<spool>/running/<id>         claimed by the daemon while executing
//	<spool>/running/<id>.pid     the running job's process-group id (bare number)
//	<spool>/running/<id>.proc    extras used to verify that pid before signalling
//	<spool>/history/<id>.json    outcome record, written when a job ends
//	<spool>/logs/<id>.log        combined stdout/stderr of the run
//	<spool>/seq, .lock           job counter and the lock that guards it
//	<spool>/atd.lock, atd.log    held by a running daemon; its output when detached
//
// Default spool: $AT_SPOOL, else $XDG_STATE_HOME/at, else ~/.local/state/at.

type Spool struct{ Dir string }

func OpenSpool() (*Spool, error) {
	dir := os.Getenv("AT_SPOOL")
	if dir == "" {
		if x := os.Getenv("XDG_STATE_HOME"); x != "" {
			dir = filepath.Join(x, "at")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			dir = filepath.Join(home, ".local", "state", "at")
		}
	}
	for _, sub := range []string{"jobs", "running", "logs", "history"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	return &Spool{Dir: dir}, nil
}

func (s *Spool) path(parts ...string) string {
	return filepath.Join(append([]string{s.Dir}, parts...)...)
}

// lock serializes job submission so ids and names stay unique.
func (s *Spool) lock() (unlock func(), err error) {
	f, err := os.OpenFile(s.path(".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking spool: %w", err)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// nextIDLocked must be called with the spool lock held.
func (s *Spool) nextIDLocked() (int, error) {
	f, err := os.OpenFile(s.path("seq"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return 0, fmt.Errorf("reading job counter: %w", err)
	}
	id := 0
	if t := strings.TrimSpace(string(data)); t != "" {
		if id, err = strconv.Atoi(t); err != nil || id < 0 {
			return 0, fmt.Errorf("job counter %s is corrupt (contains %q); fix or delete it", s.path("seq"), t)
		}
	}
	id++
	// never hand out an id that is still in use or has history, even if the counter was reset
	for fileExists(s.path("jobs", strconv.Itoa(id))) || fileExists(s.path("running", strconv.Itoa(id))) ||
		fileExists(s.historyPath(id)) {
		id++
	}
	if err := f.Truncate(0); err != nil {
		return 0, fmt.Errorf("updating job counter: %w", err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(id)+"\n"), 0); err != nil {
		return 0, fmt.Errorf("updating job counter: %w", err)
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("updating job counter: %w", err)
	}
	return id, nil
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// DaemonRunning reports whether some process holds the daemon lock.
func (s *Spool) DaemonRunning() bool {
	f, err := os.OpenFile(s.path("atd.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// ---- submitting ------------------------------------------------------------

// Names start with a letter, so they can never be confused with a numeric id
// or with a command-line option.
var nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// variables that are read-only, session-specific, or meaningless later
var skipEnv = map[string]bool{
	"_": true, "SHELLOPTS": true, "BASHOPTS": true, "BASH_VERSINFO": true, "BASH_VERSION": true,
	"EUID": true, "UID": true, "PPID": true, "GROUPS": true, "TERM": true, "DISPLAY": true,
	"SHLVL": true, "OLDPWD": true, "PWD": true, "COLUMNS": true, "LINES": true,
	"AT_JOB_ID": true, "AT_JOB_NAME": true, // set per job below; never inherited from a parent job
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func currentUmask() int {
	m := syscall.Umask(0o22)
	syscall.Umask(m)
	return m
}

type Submission struct {
	RunAt    time.Time
	Name     string
	Nice     int
	Batch    bool
	Shell    string
	Commands string
}

func (s *Spool) AddJob(sub Submission) (int, error) {
	unlock, err := s.lock()
	if err != nil {
		return 0, err
	}
	defer unlock()

	if sub.Name != "" {
		active, err := s.Active()
		if err != nil {
			return 0, err
		}
		for _, j := range active {
			if j.Name == sub.Name {
				return 0, fmt.Errorf("the name %q is already used by job %d (%s); pick another name or remove that job first", sub.Name, j.ID, j.State)
			}
		}
	}
	id, err := s.nextIDLocked()
	if err != nil {
		return 0, err
	}
	u := "unknown"
	if cu, err := user.Current(); err == nil {
		u = cu.Username
	}
	cwd, _ := os.Getwd()

	var b strings.Builder
	fmt.Fprintf(&b, "#!%s\n", sub.Shell)
	batch := 0
	if sub.Batch {
		batch = 1
	}
	fmt.Fprintf(&b, "# atjob id=%d run=%d created=%d user=%s nice=%d batch=%d", id, sub.RunAt.Unix(), time.Now().Unix(), u, sub.Nice, batch)
	if sub.Name != "" {
		fmt.Fprintf(&b, " name=%s", sub.Name)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "umask %04o\n", currentUmask())
	for _, kv := range os.Environ() {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || !envName.MatchString(name) || skipEnv[name] || strings.HasPrefix(name, "BASH_FUNC_") {
			continue
		}
		fmt.Fprintf(&b, "export %s=%s\n", name, shQuote(val))
	}
	fmt.Fprintf(&b, "export AT_JOB_ID=%d\n", id)
	if sub.Name != "" {
		fmt.Fprintf(&b, "export AT_JOB_NAME=%s\n", shQuote(sub.Name))
	}
	fmt.Fprintf(&b, "cd %s || { echo \"go-at: cannot cd to %s\" >&2; exit 1; }\n", shQuote(cwd), strings.ReplaceAll(cwd, `"`, `\"`))
	b.WriteString("# ---- user commands ----\n")
	b.WriteString(sub.Commands)
	if !strings.HasSuffix(sub.Commands, "\n") {
		b.WriteString("\n")
	}
	tmp := s.path("jobs", fmt.Sprintf(".tmp-%d", id))
	if err := os.WriteFile(tmp, []byte(b.String()), 0o700); err != nil {
		return 0, err
	}
	return id, os.Rename(tmp, s.path("jobs", strconv.Itoa(id)))
}

// ---- reading jobs ----------------------------------------------------------

func parseHeader(path string) (Job, error) {
	f, err := os.Open(path)
	if err != nil {
		return Job{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	sc.Scan() // shebang
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), "# atjob ") {
		return Job{}, fmt.Errorf("%s: not a job file", path)
	}
	j := Job{path: path, State: stQueued}
	for _, f := range strings.Fields(sc.Text()[len("# atjob "):]) {
		k, v, _ := strings.Cut(f, "=")
		n, _ := strconv.ParseInt(v, 10, 64)
		switch k {
		case "id":
			j.ID = int(n)
		case "run":
			j.Scheduled = time.Unix(n, 0)
		case "created":
			j.Created = time.Unix(n, 0)
		case "user":
			j.User = v
		case "name":
			j.Name = v
		case "nice":
			j.Nice = int(n)
		case "batch":
			j.Batch = n != 0
		case "queue": // header written by earlier versions: queue b meant batch
			j.Batch = v == "b"
		}
	}
	return j, nil
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func sortJobs(jobs []Job) {
	sort.Slice(jobs, func(a, b int) bool {
		if !jobs[a].Scheduled.Equal(jobs[b].Scheduled) {
			return jobs[a].Scheduled.Before(jobs[b].Scheduled)
		}
		return jobs[a].ID < jobs[b].ID
	})
}

// Queued lists jobs waiting for their time (state queued, or waiting if a
// batch job is due but the machine is busy).
func (s *Spool) Queued() ([]Job, error) {
	ents, err := os.ReadDir(s.path("jobs"))
	if err != nil {
		return nil, err
	}
	var jobs []Job
	now := time.Now()
	for _, e := range ents {
		if !isNumeric(e.Name()) {
			continue
		}
		j, err := parseHeader(s.path("jobs", e.Name()))
		if err != nil {
			continue
		}
		if j.Batch && !now.Before(j.Scheduled) && loadAvg() >= batchLoadLimit() {
			j.State = stWaiting
		}
		j.Log = s.path("logs", e.Name()+".log")
		jobs = append(jobs, j)
	}
	sortJobs(jobs)
	return jobs, nil
}

// Running lists jobs the daemon has claimed. One whose process is gone and
// which has no history record yet is reported as interrupted until the daemon
// sweeps it.
func (s *Spool) Running() ([]Job, error) {
	ents, err := os.ReadDir(s.path("running"))
	if err != nil {
		return nil, err
	}
	var jobs []Job
	for _, e := range ents {
		if !isNumeric(e.Name()) {
			continue
		}
		j, err := parseHeader(s.path("running", e.Name()))
		if err != nil {
			continue
		}
		j.Log = s.path("logs", e.Name()+".log")
		if pid, ok := s.liveJobPID(j.ID); ok {
			j.State, j.PGID = stRunning, pid
			if pi := s.readProc(j.ID); !pi.started.IsZero() {
				j.Started = tp(pi.started)
			}
		} else if info, err := e.Info(); err == nil && !fileExists(s.pidPath(j.ID)) && time.Since(info.ModTime()) < 3*time.Second {
			j.State = stRunning // just claimed; pid file not written yet
		} else {
			j.State = stInterrupted
			if pi := s.readProc(j.ID); !pi.started.IsZero() {
				j.Started = tp(pi.started)
			}
		}
		jobs = append(jobs, j)
	}
	sortJobs(jobs)
	return jobs, nil
}

// Active is everything queued, waiting or running (not finished history).
func (s *Spool) Active() ([]Job, error) {
	q, err := s.Queued()
	if err != nil {
		return nil, err
	}
	r, err := s.Running()
	if err != nil {
		return nil, err
	}
	var out []Job
	for _, j := range append(r, q...) {
		if j.State != stInterrupted {
			out = append(out, j)
		}
	}
	sortJobs(out)
	return out, nil
}

// History lists ended jobs, oldest first.
func (s *Spool) History() ([]Job, error) {
	ents, err := os.ReadDir(s.path("history"))
	if err != nil {
		return nil, err
	}
	var jobs []Job
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(s.path("history", e.Name()))
		if err != nil {
			continue
		}
		var j Job
		if json.Unmarshal(b, &j) != nil || j.ID == 0 {
			continue
		}
		j.Log = s.path("logs", strconv.Itoa(j.ID)+".log")
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(a, b int) bool {
		ea, eb := jobs[a].Ended, jobs[b].Ended
		if ea != nil && eb != nil && !ea.Equal(*eb) {
			return ea.Before(*eb)
		}
		return jobs[a].ID < jobs[b].ID
	})
	return jobs, nil
}

// Find resolves a job id or name. Active jobs win; with history, the most
// recently ended match is used.
func (s *Spool) Find(ref string, includeHistory bool) (Job, error) {
	match := func(j Job) bool {
		if isNumeric(ref) {
			n, _ := strconv.Atoi(ref)
			return j.ID == n
		}
		return j.Name == ref
	}
	all, err := s.Running()
	if err != nil {
		return Job{}, err
	}
	q, err := s.Queued()
	if err != nil {
		return Job{}, err
	}
	for _, j := range append(all, q...) {
		if match(j) {
			return j, nil
		}
	}
	if includeHistory {
		h, err := s.History()
		if err != nil {
			return Job{}, err
		}
		for i := len(h) - 1; i >= 0; i-- {
			if match(h[i]) {
				return h[i], nil
			}
		}
	}
	return Job{}, fmt.Errorf("no such job: %s", ref)
}
