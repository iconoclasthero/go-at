package main

// A small readline-style single-line editor with no dependencies.
//
// Keys: Left/Right, Home/End, Ctrl-A/E/B/F, Alt-B/F and Ctrl-Left/Right
// (word moves), Backspace, Delete, Ctrl-D (EOF on an empty line, delete
// otherwise), Ctrl-K/U/W (kill to end/start/previous word), Ctrl-Y (yank),
// Ctrl-T (transpose), Ctrl-L (clear), Up/Down and Ctrl-P/N (history),
// Ctrl-C (abort).
//
// Long lines scroll horizontally rather than wrapping, so redraw is always
// a single terminal row. Column widths account for wide (CJK/emoji) and
// zero-width (combining) characters using a small built-in table.
//
// Input is pumped through a goroutine so that an ESC can be told apart from
// the start of an arrow-key sequence by waiting briefly for more bytes,
// instead of guessing from what happens to be buffered: terminals and ssh
// may deliver one key press in several reads.

import (
	"errors"
	"fmt"
	"io"
	"time"
	"unicode"
	"unicode/utf8"
)

var errInterrupt = errors.New("interrupted")

type keyKind int

const (
	kRune keyKind = iota
	kEnter
	kEOF // Ctrl-D
	kInterrupt
	kLeft
	kRight
	kUp
	kDown
	kHome
	kEnd
	kWordLeft
	kWordRight
	kBackspace
	kDelete
	kKillEnd
	kKillStart
	kKillWord
	kKillWordFwd
	kYank
	kTranspose
	kClear
	kNone
)

type key struct {
	kind keyKind
	r    rune
}

type Editor struct {
	src     chan byte
	rerr    error // read error; valid once src is closed
	out     io.Writer
	cols    func() int
	escWait time.Duration // how long to wait for the rest of an escape sequence
	hist    []string
	killed  []rune
}

var errTimeout = errors.New("timeout")

func NewEditor(in io.Reader, out io.Writer, cols func() int) *Editor {
	e := &Editor{src: make(chan byte), out: out, cols: cols, escWait: 200 * time.Millisecond}
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := in.Read(buf)
			for _, b := range buf[:n] {
				e.src <- b
			}
			if err != nil {
				e.rerr = err
				close(e.src)
				return
			}
		}
	}()
	return e
}

func (e *Editor) readByte() (byte, error) {
	b, ok := <-e.src
	if !ok {
		return 0, e.rerr
	}
	return b, nil
}

// readByteWait is readByte with a timeout (errTimeout).
func (e *Editor) readByteWait(d time.Duration) (byte, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case b, ok := <-e.src:
		if !ok {
			return 0, e.rerr
		}
		return b, nil
	case <-t.C:
		return 0, errTimeout
	}
}

func (e *Editor) readRune() (rune, error) {
	b, err := e.readByte()
	if err != nil {
		return 0, err
	}
	if b < utf8.RuneSelf {
		return rune(b), nil
	}
	need := 0
	switch {
	case b >= 0xF0 && b <= 0xF7:
		need = 3
	case b >= 0xE0:
		need = 2
	case b >= 0xC0:
		need = 1
	}
	buf := []byte{b}
	for i := 0; i < need; i++ {
		c, err := e.readByte()
		if err != nil {
			return utf8.RuneError, nil
		}
		buf = append(buf, c)
	}
	r, _ := utf8.DecodeRune(buf)
	return r, nil
}

// readKey decodes one key press. A lone ESC (nothing buffered behind it)
// is reported as kNone.
func (e *Editor) readKey() (key, error) {
	r, err := e.readRune()
	if err != nil {
		return key{}, err
	}
	switch r {
	case 1:
		return key{kind: kHome}, nil
	case 2:
		return key{kind: kLeft}, nil
	case 3:
		return key{kind: kInterrupt}, nil
	case 4:
		return key{kind: kEOF}, nil
	case 5:
		return key{kind: kEnd}, nil
	case 6:
		return key{kind: kRight}, nil
	case 8, 127:
		return key{kind: kBackspace}, nil
	case 9:
		return key{kind: kNone}, nil
	case 10, 13:
		return key{kind: kEnter}, nil
	case 11:
		return key{kind: kKillEnd}, nil
	case 12:
		return key{kind: kClear}, nil
	case 14:
		return key{kind: kDown}, nil
	case 16:
		return key{kind: kUp}, nil
	case 20:
		return key{kind: kTranspose}, nil
	case 21:
		return key{kind: kKillStart}, nil
	case 23:
		return key{kind: kKillWord}, nil
	case 25:
		return key{kind: kYank}, nil
	case 27:
		return e.readEscape()
	}
	if r < 32 || r == utf8.RuneError {
		return key{kind: kNone}, nil
	}
	return key{kind: kRune, r: r}, nil
}

// readEscape decodes what follows an ESC. If nothing arrives within escWait
// (or the sequence stalls midway) it was a lone ESC and is ignored.
func (e *Editor) readEscape() (key, error) {
	b, err := e.readByteWait(e.escWait)
	if err != nil {
		if err == errTimeout || err == io.EOF {
			return key{kind: kNone}, nil // lone ESC; a pending EOF surfaces on the next read
		}
		return key{}, err
	}
	switch b {
	case '[':
		var params []byte
		for {
			c, err := e.readByteWait(e.escWait)
			if err != nil {
				return key{kind: kNone}, nil
			}
			if c >= 0x40 && c <= 0x7e {
				return csiKey(string(params), c), nil
			}
			params = append(params, c)
		}
	case 'O':
		c, err := e.readByteWait(e.escWait)
		if err != nil {
			return key{kind: kNone}, nil
		}
		return csiKey("", c), nil
	case 'b':
		return key{kind: kWordLeft}, nil
	case 'f':
		return key{kind: kWordRight}, nil
	case 'd':
		return key{kind: kKillWordFwd}, nil
	case 127, 8:
		return key{kind: kKillWord}, nil
	}
	return key{kind: kNone}, nil
}

func csiKey(params string, final byte) key {
	mod := len(params) > 2 && params[len(params)-2] == ';' && params[len(params)-1] != '1'
	switch final {
	case 'A':
		return key{kind: kUp}
	case 'B':
		return key{kind: kDown}
	case 'C':
		if mod {
			return key{kind: kWordRight}
		}
		return key{kind: kRight}
	case 'D':
		if mod {
			return key{kind: kWordLeft}
		}
		return key{kind: kLeft}
	case 'H':
		return key{kind: kHome}
	case 'F':
		return key{kind: kEnd}
	case '~':
		switch params {
		case "1", "7":
			return key{kind: kHome}
		case "4", "8":
			return key{kind: kEnd}
		case "3":
			return key{kind: kDelete}
		}
	}
	return key{kind: kNone}
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

type lineState struct {
	buf   []rune
	pos   int
	start int // first visible rune (horizontal scroll)
}

func (s *lineState) wordLeft() {
	for s.pos > 0 && !isWordRune(s.buf[s.pos-1]) {
		s.pos--
	}
	for s.pos > 0 && isWordRune(s.buf[s.pos-1]) {
		s.pos--
	}
}

func (s *lineState) wordRight() {
	for s.pos < len(s.buf) && !isWordRune(s.buf[s.pos]) {
		s.pos++
	}
	for s.pos < len(s.buf) && isWordRune(s.buf[s.pos]) {
		s.pos++
	}
}

func (s *lineState) cut(from, to int) []rune {
	cut := append([]rune(nil), s.buf[from:to]...)
	s.buf = append(s.buf[:from], s.buf[to:]...)
	s.pos = from
	return cut
}

func (s *lineState) insert(rs []rune) {
	s.buf = append(s.buf[:s.pos], append(append([]rune(nil), rs...), s.buf[s.pos:]...)...)
	s.pos += len(rs)
}

// runeWidth returns the terminal columns a rune occupies: 0 for combining
// marks, 2 for East Asian wide characters and emoji, 1 otherwise. This is a
// compact approximation of wcwidth, not a full Unicode implementation.
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) || (r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) || (r >= 0x1f300 && r <= 0x1f64f) ||
		(r >= 0x1f900 && r <= 0x1f9ff) || (r >= 0x20000 && r <= 0x3fffd)):
		return 2
	}
	return 1
}

func strWidth(rs []rune) int {
	w := 0
	for _, r := range rs {
		w += runeWidth(r)
	}
	return w
}

func (e *Editor) render(prompt string, s *lineState) {
	cols := 80
	if e.cols != nil {
		if c := e.cols(); c > 0 {
			cols = c
		}
	}
	pw := strWidth([]rune(prompt))
	w := cols - pw - 1
	if w < 8 {
		w = 8
	}
	if s.start > len(s.buf) {
		s.start = len(s.buf)
	}
	if s.pos < s.start {
		s.start = s.pos
	}
	// scroll right until the cursor column fits inside the visible window
	for s.start < s.pos && strWidth(s.buf[s.start:s.pos]) > w-1 {
		s.start++
	}
	end, used := s.start, 0
	for end < len(s.buf) && used+runeWidth(s.buf[end]) <= w {
		used += runeWidth(s.buf[end])
		end++
	}
	fmt.Fprintf(e.out, "\r%s%s\x1b[K\r", prompt, string(s.buf[s.start:end]))
	if n := pw + strWidth(s.buf[s.start:s.pos]); n > 0 {
		fmt.Fprintf(e.out, "\x1b[%dC", n)
	}
}

// ReadLine reads one edited line. It returns io.EOF if Ctrl-D is pressed on
// an empty line and errInterrupt on Ctrl-C. The terminal must already be in
// raw mode (the caller's job; this keeps the editor testable).
func (e *Editor) ReadLine(prompt string) (string, error) {
	s := &lineState{}
	histIdx := len(e.hist)
	var stash []rune
	e.render(prompt, s)
	for {
		k, err := e.readKey()
		if err != nil {
			if err == io.EOF && len(s.buf) > 0 {
				fmt.Fprint(e.out, "\r\n")
				return string(s.buf), nil
			}
			return "", err
		}
		switch k.kind {
		case kRune:
			s.insert([]rune{k.r})
		case kEnter:
			fmt.Fprint(e.out, "\r\n")
			line := string(s.buf)
			if line != "" && (len(e.hist) == 0 || e.hist[len(e.hist)-1] != line) {
				e.hist = append(e.hist, line)
			}
			return line, nil
		case kEOF:
			if len(s.buf) == 0 {
				fmt.Fprint(e.out, "\r\n")
				return "", io.EOF
			}
			if s.pos < len(s.buf) {
				s.cut(s.pos, s.pos+1)
			}
		case kInterrupt:
			fmt.Fprint(e.out, "^C\r\n")
			return "", errInterrupt
		case kLeft:
			if s.pos > 0 {
				s.pos--
			}
		case kRight:
			if s.pos < len(s.buf) {
				s.pos++
			}
		case kHome:
			s.pos = 0
		case kEnd:
			s.pos = len(s.buf)
		case kWordLeft:
			s.wordLeft()
		case kWordRight:
			s.wordRight()
		case kBackspace:
			if s.pos > 0 {
				s.cut(s.pos-1, s.pos)
			}
		case kDelete:
			if s.pos < len(s.buf) {
				s.cut(s.pos, s.pos+1)
			}
		case kKillEnd:
			if s.pos < len(s.buf) {
				e.killed = s.cut(s.pos, len(s.buf))
			}
		case kKillStart:
			if s.pos > 0 {
				e.killed = s.cut(0, s.pos)
			}
		case kKillWord: // unix-word-rubout: back to previous whitespace
			from := s.pos
			for from > 0 && s.buf[from-1] == ' ' {
				from--
			}
			for from > 0 && s.buf[from-1] != ' ' {
				from--
			}
			if from < s.pos {
				e.killed = s.cut(from, s.pos)
			}
		case kKillWordFwd:
			to := s.pos
			save := s.pos
			s.wordRight()
			to, s.pos = s.pos, save
			if to > s.pos {
				e.killed = s.cut(s.pos, to)
			}
		case kYank:
			s.insert(e.killed)
		case kTranspose:
			if len(s.buf) >= 2 {
				if s.pos == len(s.buf) {
					s.pos--
				}
				if s.pos > 0 {
					s.buf[s.pos-1], s.buf[s.pos] = s.buf[s.pos], s.buf[s.pos-1]
					s.pos++
				}
			}
		case kClear:
			fmt.Fprint(e.out, "\x1b[H\x1b[2J")
		case kUp:
			if histIdx > 0 {
				if histIdx == len(e.hist) {
					stash = append([]rune(nil), s.buf...)
				}
				histIdx--
				s.buf = []rune(e.hist[histIdx])
				s.pos = len(s.buf)
			}
		case kDown:
			if histIdx < len(e.hist) {
				histIdx++
				if histIdx == len(e.hist) {
					s.buf = append([]rune(nil), stash...)
				} else {
					s.buf = []rune(e.hist[histIdx])
				}
				s.pos = len(s.buf)
			}
		}
		e.render(prompt, s)
	}
}
