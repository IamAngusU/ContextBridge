//go:build !windows

package terminalui

import (
	"errors"
	"os"
)

func Topmost() (func(), error) {
	return nil, errors.New("--topmost is only available for a Windows console")
}

func enableVirtualTerminal(_ *os.File) bool {
	return true
}

func terminalWidth(_ *os.File) int {
	return 0
}

func drawConsoleStatus(_ *os.File, _ string) bool { return false }

func clearConsoleStatus(_ *os.File) bool { return false }
