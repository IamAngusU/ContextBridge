//go:build windows

package terminalui

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

var (
	getConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	setWindowPos     = windows.NewLazySystemDLL("user32.dll").NewProc("SetWindowPos")
)

// Topmost pins a classic console window only for this process's lifetime.
// Windows Terminal's ConPTY pseudo-window may not support this operation.
func Topmost() (func(), error) {
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
		return nil, errors.New("no console window is available for --topmost")
	}
	const keepPosition = 0x0001 | 0x0002 | 0x0010
	if ok, _, err := setWindowPos.Call(hwnd, ^uintptr(0), 0, 0, 0, 0, keepPosition); ok == 0 {
		return nil, errors.Join(errors.New("could not pin console window"), err)
	}
	return func() { _, _, _ = setWindowPos.Call(hwnd, ^uintptr(1), 0, 0, 0, 0, keepPosition) }, nil
}

func enableVirtualTerminal(output *os.File) bool {
	handle := windows.Handle(output.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return false
	}
	return windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}

func terminalWidth(output *os.File) int {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(output.Fd()), &info) != nil {
		return 0
	}
	return int(info.Window.Right-info.Window.Left) + 1
}
