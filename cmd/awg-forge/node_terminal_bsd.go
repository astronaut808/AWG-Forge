//go:build darwin || freebsd || openbsd || netbsd

package main

import "golang.org/x/sys/unix"

func nodeGetTermios(fd int) (*unix.Termios, error) { return unix.IoctlGetTermios(fd, unix.TIOCGETA) }
func nodeSetTermios(fd int, t *unix.Termios) error { return unix.IoctlSetTermios(fd, unix.TIOCSETA, t) }
