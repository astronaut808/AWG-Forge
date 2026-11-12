//go:build linux

package main

import "golang.org/x/sys/unix"

func nodeGetTermios(fd int) (*unix.Termios, error) { return unix.IoctlGetTermios(fd, unix.TCGETS) }
func nodeSetTermios(fd int, t *unix.Termios) error { return unix.IoctlSetTermios(fd, unix.TCSETS, t) }
