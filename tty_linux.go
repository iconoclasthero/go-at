//go:build linux

package main

import "syscall"

const (
	ioctlGet = syscall.TCGETS
	ioctlSet = syscall.TCSETS
)
