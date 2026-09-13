//go:build windows

package terminalui

import (
	"os"

	"golang.org/x/sys/windows"
)

func enableVirtualTerminal(output *os.File) bool {
	handle := windows.Handle(output.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return false
	}
	return windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
