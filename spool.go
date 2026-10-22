package main

import (
	"bufio"
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
//	<spool>/jobs/<id>      pending job: an executable bash script
//	<spool>/running/<id>   claimed by the daemon while executing
//	<spool>/logs/<id>.log  combined stdout/stderr of the last run
//	<spool>/seq            job counter
//	<spool>/atd.lock       held (flock) by a running daemon
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
	for _, sub := range []string{"jobs", "running", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	return &Spool{Dir: dir}, nil
}

func (s *Spool) path(parts ...string) string {
	return filepath.Join(append([]string{s.Dir}, parts...)...)
}

func (s *Spool) nextID() (int, error) {
	f, err := os.OpenFile(s.path("seq"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, fmt.Errorf("locking job counter: %w", err)
	}
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
	// never hand out an id that is still in use, even if the counter was reset
	for fileExists(s.path("jobs", strconv.Itoa(id))) || fileExists(s.path("running", strconv.Itoa(id))) {
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

type Job struct {
	ID      int
	Queue   byte
	RunAt   time.Time
	Created time.Time
	User    string
	Path    string
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// variables that are read-only, session-specific, or meaningless later
var skipEnv = map[string]bool{
	"_": true, "SHELLOPTS": true, "BASHOPTS": true, "BASH_VERSINFO": true, "BASH_VERSION": true,
	"EUID": true, "UID": true, "PPID": true, "GROUPS": true, "TERM": true, "DISPLAY": true,
	"SHLVL": true, "OLDPWD": true, "PWD": true, "COLUMNS": true, "LINES": true,
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (s *Spool) AddJob(runAt time.Time, queue byte, shell, commands string) (int, error) {
	id, err := s.nextID()
	if err != nil {
		return 0, err
	}
	u := "unknown"
	if cu, err := user.Current(); err == nil {
		u = cu.Username
	}
	cwd, _ := os.Getwd()

	var b strings.Builder
	fmt.Fprintf(&b, "#!%s\n", shell)
	fmt.Fprintf(&b, "# atjob id=%d queue=%c run=%d created=%d user=%s\n", id, queue, runAt.Unix(), time.Now().Unix(), u)
	fmt.Fprintf(&b, "umask %04o\n", currentUmask())
	for _, kv := range os.Environ() {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || !envName.MatchString(name) || skipEnv[name] || strings.HasPrefix(name, "BASH_FUNC_") {
			continue
		}
		fmt.Fprintf(&b, "export %s=%s\n", name, shQuote(val))
	}
	fmt.Fprintf(&b, "cd %s || { echo \"at: cannot cd to %s\" >&2; exit 1; }\n", shQuote(cwd), strings.ReplaceAll(cwd, `"`, `\"`))
	b.WriteString("# ---- user commands ----\n")
	b.WriteString(commands)
	if !strings.HasSuffix(commands, "\n") {
		b.WriteString("\n")
	}
	tmp := s.path("jobs", fmt.Sprintf(".tmp-%d", id))
	if err := os.WriteFile(tmp, []byte(b.String()), 0o700); err != nil {
		return 0, err
	}
	return id, os.Rename(tmp, s.path("jobs", strconv.Itoa(id)))
}

func currentUmask() int {
	m := syscall.Umask(0o22)
	syscall.Umask(m)
	return m
}

func parseHeader(path string) (Job, error) {
	f, err := os.Open(path)
	if err != nil {
		return Job{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // shebang
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), "# atjob ") {
		return Job{}, fmt.Errorf("%s: not an at job", path)
	}
	j := Job{Path: path}
	for _, f := range strings.Fields(sc.Text()[len("# atjob "):]) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "id":
			j.ID, _ = strconv.Atoi(v)
		case "queue":
			if len(v) > 0 {
				j.Queue = v[0]
			}
		case "run":
			n, _ := strconv.ParseInt(v, 10, 64)
			j.RunAt = time.Unix(n, 0)
		case "created":
			n, _ := strconv.ParseInt(v, 10, 64)
			j.Created = time.Unix(n, 0)
		case "user":
			j.User = v
		}
	}
	return j, nil
}

func (s *Spool) List() ([]Job, error) {
	ents, err := os.ReadDir(s.path("jobs"))
	if err != nil {
		return nil, err
	}
	var jobs []Job
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if j, err := parseHeader(s.path("jobs", e.Name())); err == nil {
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(a, b int) bool {
		if !jobs[a].RunAt.Equal(jobs[b].RunAt) {
			return jobs[a].RunAt.Before(jobs[b].RunAt)
		}
		return jobs[a].ID < jobs[b].ID
	})
	return jobs, nil
}
