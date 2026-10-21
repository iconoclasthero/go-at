//go:build linux || darwin

package main

import (
	"syscall"
	"unsafe"
)

func ioctl(fd, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

func isTerminal(fd int) bool {
	var t syscall.Termios
	return ioctl(uintptr(fd), ioctlGet, unsafe.Pointer(&t)) == nil
}

// makeRaw puts fd in raw mode (no echo, no line buffering, no signal keys,
// no output post-processing) and returns a function that restores it.
func makeRaw(fd int) (restore func(), err error) {
	var old syscall.Termios
	if err = ioctl(uintptr(fd), ioctlGet, unsafe.Pointer(&old)); err != nil {
		return nil, err
	}
	raw := old
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err = ioctl(uintptr(fd), ioctlSet, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	return func() { ioctl(uintptr(fd), ioctlSet, unsafe.Pointer(&old)) }, nil
}

func termCols(fd int) int {
	var ws struct{ Row, Col, X, Y uint16 }
	if err := ioctl(uintptr(fd), syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err != nil || ws.Col == 0 {
		return 80
	}
	return int(ws.Col)
}
