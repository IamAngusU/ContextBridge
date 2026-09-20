//go:build linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func makeConsoleInputRaw(input *os.File) (func(), bool) {
	fd := int(input.Fd())
	original, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return func() {}, false
	}
	mode := *original
	mode.Lflag &^= unix.ECHO | unix.ICANON
	mode.Cc[unix.VMIN] = 1
	mode.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &mode); err != nil {
		return func() {}, false
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, original) }, true
}
