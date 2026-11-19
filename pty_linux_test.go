//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// ptyRun runs a go-at command with a real terminal as stdin and stderr (stdout
// is a pipe), the way a person at a shell runs it, and lets the test type.
type ptyRun struct {
	t      *testing.T
	cmd    *exec.Cmd
	master *os.File
	mu     sync.Mutex
	buf    bytes.Buffer // everything the terminal showed
	stdout bytes.Buffer
	done   chan struct{}
	code   int
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b[()][A-Za-z0-9]|\x1b[=>]|\r`)

func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Skipf("pty unlock: %v", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		t.Skipf("pty number: %v", e)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pty slave: %v", err)
	}
	// a window size, or fzf has nothing to draw on
	ws := struct{ Row, Col, X, Y uint16 }{40, 120, 0, 0}
	syscall.Syscall(syscall.SYS_IOCTL, s.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
	return m, s
}

func (e *env) pty(name string, args ...string) *ptyRun {
	e.t.Helper()
	c := e.cmd("", name, args...)
	m, s := openPty(e.t)
	p := &ptyRun{t: e.t, cmd: c, master: m, done: make(chan struct{})}
	c.Stdin, c.Stderr = s, s
	c.Stdout = &p.stdout
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	c.Env = append(c.Env, "TERM=xterm")
	if err := c.Start(); err != nil {
		e.t.Fatal(err)
	}
	s.Close()
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := m.Read(b)
			if n > 0 {
				p.mu.Lock()
				p.buf.Write(b[:n])
				p.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		err := c.Wait()
		p.code = 0
		if ee, ok := err.(*exec.ExitError); ok {
			p.code = ee.ExitCode()
		}
		close(p.done)
	}()
	e.t.Cleanup(func() { c.Process.Kill(); m.Close() })
	return p
}

func (p *ptyRun) screen() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ansiRe.ReplaceAllString(p.buf.String(), "")
}

// expect waits until the terminal output contains s.
func (p *ptyRun) expect(s string) {
	p.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.screen(), s) {
			return
		}
		select {
		case <-p.done:
			if strings.Contains(p.screen(), s) {
				return
			}
			p.t.Fatalf("exited (%d) before showing %q; terminal:\n%s", p.code, s, p.screen())
		case <-time.After(20 * time.Millisecond):
		}
	}
	p.t.Fatalf("timed out waiting for %q; terminal:\n%s", s, p.screen())
}

func (p *ptyRun) send(s string) { p.master.WriteString(s) }

// finish waits for the command to end and returns its stdout and exit code.
func (p *ptyRun) finish() (stdout string, code int) {
	p.t.Helper()
	select {
	case <-p.done:
	case <-time.After(8 * time.Second):
		p.t.Fatalf("did not exit; terminal:\n%s", p.screen())
	}
	return p.stdout.String(), p.code
}
