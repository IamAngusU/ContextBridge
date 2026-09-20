//go:build linux || darwin || freebsd || openbsd || netbsd

package terminalui

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func Topmost() (func(), error) {
	return nil, errors.New("--topmost is only available for a Windows console")
}

func enableVirtualTerminal(_ *os.File) bool {
	return true
}

// The panel asks for the current viewport on every redraw. IoctlGetWinsize
// therefore follows terminal resizes/SIGWINCH without retaining stale startup
// dimensions or requiring a second signal-driven state machine.
func terminalWidth(output *os.File) int {
	width, _ := terminalSize(output)
	return width
}

func terminalHeight(output *os.File) int {
	_, height := terminalSize(output)
	return height
}

func terminalSize(output *os.File) (int, int) {
	if output == nil {
		return 0, 0
	}
	window, err := unix.IoctlGetWinsize(int(output.Fd()), unix.TIOCGWINSZ)
	if err != nil || window.Col == 0 || window.Row == 0 {
		return 0, 0
	}
	return int(window.Col), int(window.Row)
}

func drawConsoleStatus(_ *os.File, _ string) bool { return false }

func clearConsoleStatus(_ *os.File) bool { return false }

// Unix terminal emulators do not expose portable selection state to the
// foreground process. The alternate-screen panel keeps its own redraws out of
// normal scrollback; copy-mode behavior remains owned by the emulator.
func consoleSelectionActive(_ *os.File) bool { return false }
