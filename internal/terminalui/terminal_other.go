//go:build !windows

package terminalui

import "os"

func enableVirtualTerminal(_ *os.File) bool {
	return true
}
