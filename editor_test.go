package main

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func readOne(t *testing.T, input string) string {
	t.Helper()
	var out strings.Builder
	e := NewEditor(strings.NewReader(input), &out, func() int { return 80 })
	got, err := e.ReadLine("at> ")
	if err != nil {
		t.Fatalf("%q: %v", input, err)
	}
	return got
}

func TestEditing(t *testing.T) {
	const (
		left  = "\x1b[D"
		right = "\x1b[C"
		home  = "\x1b[H"
		end   = "\x1b[F"
		del   = "\x1b[3~"
		cL    = "\x1b[1;5D"
		cR    = "\x1b[1;5C"
	)
	cases := []struct{ name, in, want string }{
		{"plain", "echo hi\r", "echo hi"},
		{"the bug report: arrows move, not insert", `echo "why can't i use the arrow keys?"` + left + left + left + "X\r", `echo "why can't i use the arrow keyXs?"`},
		{"left insert", "abc" + left + left + "X\r", "aXbc"},
		{"ss3 arrows", "abc\x1bOD\x1bODX\r", "aXbc"},
		{"right", "abc" + left + left + left + right + "X\r", "aXbc"},
		{"home/end", "abc" + home + "1" + end + "2\r", "1abc2"},
		{"home/end alt codes", "abc\x1b[1~1\x1b[4~2\r", "1abc2"},
		{"ctrl-a/e", "abc\x011\x052\r", "1abc2"},
		{"delete", "abc" + left + del + "\r", "ab"},
		{"delete at end is no-op", "abc" + del + "\r", "abc"},
		{"backspace mid", "abc" + left + "\x7f\r", "ac"},
		{"ctrl-left", "hello world" + cL + cL + "X\r", "Xhello world"},
		{"ctrl-right", "hello world" + home + cR + "X\r", "helloX world"},
		{"alt-b/f", "foo bar baz\x1bb\x1bbX\x1bf\x1bfY\r", "foo Xbar bazY"},
		{"ctrl-w", "foo bar\x17\r", "foo "},
		{"ctrl-k", "foobar" + left + left + left + "\x0b\r", "foo"},
		{"ctrl-u", "foobar" + left + left + left + "\x15\r", "bar"},
		{"yank", "foobar\x15\x19\x19\r", "foobarfoobar"},
		{"ctrl-d deletes under cursor", "abc" + left + left + "\x04\r", "ac"},
		{"transpose", "ab\x14\r", "ba"},
		{"alt-d", "foo bar" + home + "\x1bd\r", " bar"},
		{"utf-8", "é" + left + "x\r", "xé"},
		{"utf-8 backspace", "aé\x7f\r", "a"},
		{"ignore tab", "a\tb\r", "ab"},
	}
	for _, c := range cases {
		if got := readOne(t, c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHistory(t *testing.T) {
	var out strings.Builder
	in := "one\rtwo\r" + "\x1b[A\x1b[A\r" + "\x1b[A\x1b[B\x1b[Bx\r"
	e := NewEditor(strings.NewReader(in), &out, nil)
	for i, want := range []string{"one", "two", "one", "x"} {
		got, err := e.ReadLine("> ")
		if err != nil || got != want {
			t.Fatalf("line %d: got %q err %v, want %q", i, got, err, want)
		}
	}
}

func TestEOFAndInterrupt(t *testing.T) {
	var out strings.Builder
	e := NewEditor(strings.NewReader("\x04"), &out, nil)
	if _, err := e.ReadLine("> "); err != io.EOF {
		t.Errorf("want EOF, got %v", err)
	}
	e = NewEditor(strings.NewReader("abc\x03"), &out, nil)
	if _, err := e.ReadLine("> "); err != errInterrupt {
		t.Errorf("want interrupt, got %v", err)
	}
	// EOF from the stream with partial input returns what was typed
	e = NewEditor(strings.NewReader("partial"), &out, nil)
	if got, err := e.ReadLine("> "); err != nil || got != "partial" {
		t.Errorf("got %q %v", got, err)
	}
}

func TestLongLineScrolls(t *testing.T) {
	var out strings.Builder
	long := strings.Repeat("abcdefghij", 20)
	e := NewEditor(strings.NewReader(long+"\x1b[D\x1b[DX\r"), &out, func() int { return 20 })
	got, err := e.ReadLine("at> ")
	if err != nil || got != long[:len(long)-2]+"X"+long[len(long)-2:] {
		t.Fatalf("got %q err %v", got, err)
	}
	// no single redraw may emit more visible text than fits on one row
	for _, chunk := range strings.Split(out.String(), "\r") {
		if i := strings.Index(chunk, "\x1b[K"); i >= 0 && len([]rune(chunk[:i])) > 20 {
			t.Fatalf("redraw wider than terminal: %q", chunk)
		}
	}
}

// An arrow key delivered in several separate reads (slow ssh link, odd
// terminal, tmux) must still be one Left, not ESC + literal "[D".
func TestSplitEscapeSequence(t *testing.T) {
	for _, parts := range [][]string{
		{"abc", "\x1b", "[", "D", "X\r"},
		{"abc", "\x1b[", "D", "X\r"},
		{"abc", "\x1b", "[D", "X\r"},
		{"abc", "\x1bO", "D", "X\r"},
		{"abc", "\x1b", "[", "1;5", "D", "X\r"}, // ctrl-left, split inside the parameters
	} {
		pr, pw := io.Pipe()
		e := NewEditor(pr, io.Discard, nil)
		go func() {
			for _, p := range parts {
				pw.Write([]byte(p))
				time.Sleep(25 * time.Millisecond)
			}
		}()
		got, err := e.ReadLine("> ")
		want := "abXc"
		if len(parts) > 3 && parts[3] == "1;5" {
			want = "Xabc" // word-left from the end of "abc"
		}
		if err != nil || got != want {
			t.Errorf("%q: got %q err %v, want %q", parts, got, err, want)
		}
	}
}

// A lone ESC (nothing follows) is ignored and does not eat the next key.
func TestLoneEscape(t *testing.T) {
	pr, pw := io.Pipe()
	e := NewEditor(pr, io.Discard, nil)
	e.escWait = 20 * time.Millisecond
	go func() {
		pw.Write([]byte("ab\x1b"))
		time.Sleep(100 * time.Millisecond) // well past escWait
		pw.Write([]byte("c\r"))
	}()
	if got, err := e.ReadLine("> "); err != nil || got != "abc" {
		t.Errorf("got %q err %v, want abc", got, err)
	}
}

func lastCursorCol(out string) int {
	i := strings.LastIndex(out, "\x1b[")
	var n int
	if i >= 0 {
		fmt.Sscanf(out[i:], "\x1b[%dC", &n)
	}
	return n
}

func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		name, in string
		col      int // cursor column after typing in (prompt "> " is 2 wide)
	}{
		{"ascii", "ab", 4},
		{"wide cjk is 2 columns", "日本", 6},
		{"emoji is 2 columns", "a😀", 5},
		{"combining mark is 0 columns", "éx", 4},
	}
	for _, c := range cases {
		var out strings.Builder
		e := NewEditor(strings.NewReader(c.in), &out, func() int { return 80 })
		e.ReadLine("> ") // returns at EOF with the partial line
		if got := lastCursorCol(out.String()); got != c.col {
			t.Errorf("%s: cursor column %d, want %d", c.name, got, c.col)
		}
	}
}

func TestWideCharsScroll(t *testing.T) {
	var out strings.Builder
	long := strings.Repeat("日本語", 20) // 120 columns
	e := NewEditor(strings.NewReader(long), &out, func() int { return 30 })
	got, err := e.ReadLine("at> ")
	if err != nil || got != long {
		t.Fatalf("lost input: %v", err)
	}
	for _, chunk := range strings.Split(out.String(), "\r") {
		if i := strings.Index(chunk, "\x1b[K"); i >= 0 {
			shown := strings.TrimPrefix(chunk[:i], "at> ")
			if w := strWidth([]rune(shown)); w > 30-4 {
				t.Fatalf("visible text is %d columns, window is %d", w, 30-4)
			}
		}
	}
}
