package terminalui

import (
	"fmt"
	"os"
)

// DrawTransientStatus writes one bounded in-place status row using the same
// Windows-native and VT fallbacks as the service panel. It is intended for
// short-lived foreground commands such as cluster selftest.
func DrawTransientStatus(output *os.File, line string) {
	if output == nil {
		return
	}
	width := terminalWidth(output)
	if width <= 0 {
		width = 120
	}
	line = clipANSIColumns(line, max(1, width-1))
	if drawConsoleStatus(output, line) {
		return
	}
	fmt.Fprintf(output, "\r\x1b[2K%s", line)
}

// ClearTransientStatus removes a row previously drawn by
// DrawTransientStatus without adding it to normal terminal scrollback.
func ClearTransientStatus(output *os.File) {
	if output == nil || clearConsoleStatus(output) {
		return
	}
	fmt.Fprint(output, "\r\x1b[2K")
}
