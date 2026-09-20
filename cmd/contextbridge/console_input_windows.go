//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func makeConsoleInputRaw(input *os.File) (func(), bool) {
	handle := windows.Handle(input.Fd())
	var original uint32
	if windows.GetConsoleMode(handle, &original) != nil {
		return func() {}, false
	}
	mode := original &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT)
	// Keep processed input so Ctrl+C still reaches signal.NotifyContext.
	mode |= windows.ENABLE_PROCESSED_INPUT
	if windows.SetConsoleMode(handle, mode) != nil {
		return func() {}, false
	}
	return func() { _ = windows.SetConsoleMode(handle, original) }, true
}
