//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd

package main

import "os"

func makeConsoleInputRaw(_ *os.File) (func(), bool) { return func() {}, false }
