//go:build windows

package terminalui

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	getConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	setWindowPos     = windows.NewLazySystemDLL("user32.dll").NewProc("SetWindowPos")
	writeConsoleRow  = windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleOutputW")
	getSelectionInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleSelectionInfo")
)

type consoleCell struct {
	char       uint16
	attributes uint16
}

type consoleSelectionInfo struct {
	flags     uint32
	anchor    windows.Coord
	selection windows.SmallRect
}

// consoleSelectionActive prevents a panel refresh from invalidating text the
// user is selecting or has selected in a classic Windows console/ConPTY.
func consoleSelectionActive(_ *os.File) bool {
	var info consoleSelectionInfo
	ok, _, _ := getSelectionInfo.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return false
	}
	const selectionInProgress = 0x0001
	const selectionNotEmpty = 0x0002
	const mouseDown = 0x0008
	return info.flags&(selectionInProgress|selectionNotEmpty|mouseDown) != 0
}

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

func terminalHeight(output *os.File) int {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(output.Fd()), &info) != nil {
		return 0
	}
	return int(info.Window.Bottom-info.Window.Top) + 1
}

// A native in-place write avoids the scrollback growth that some Windows
// consoles cause when VT carriage-return/erase sequences meet font zoom or a
// reflowed viewport. WriteConsoleOutputW does not move the cursor at all.
func drawConsoleStatus(output *os.File, line string) bool {
	info, ok := consoleStatusInfo(output)
	if !ok {
		return false
	}
	if info.CursorPosition.Y < info.Window.Top || info.CursorPosition.Y > info.Window.Bottom {
		return true // User is reading scrollback; do not pull the viewport down.
	}
	_ = writeStatusRow(windows.Handle(output.Fd()), info.CursorPosition.Y, consoleCells(line, int(info.Size.X), info.Attributes))
	return true
}

func clearConsoleStatus(output *os.File) bool {
	info, ok := consoleStatusInfo(output)
	if !ok {
		return false
	}
	if info.CursorPosition.Y < info.Window.Top || info.CursorPosition.Y > info.Window.Bottom {
		return true
	}
	_ = writeStatusRow(windows.Handle(output.Fd()), info.CursorPosition.Y, consoleCells("", int(info.Size.X), info.Attributes))
	return true
}

func consoleStatusInfo(output *os.File) (windows.ConsoleScreenBufferInfo, bool) {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(output.Fd()), &info) != nil || info.Size.X < 2 {
		return info, false
	}
	return info, true
}

func writeStatusRow(handle windows.Handle, row int16, cells []consoleCell) bool {
	if len(cells) == 0 || len(cells) > 32767 {
		return false
	}
	// #nosec G115 -- len(cells) is bounded to the positive int16 range above.
	bufferSize := windows.Coord{X: int16(len(cells)), Y: 1}
	bufferOrigin := windows.Coord{}
	// #nosec G115 -- len(cells)-1 is bounded to [0, 32766] above.
	region := windows.SmallRect{Left: 0, Top: row, Right: int16(len(cells) - 1), Bottom: row}
	ok, _, _ := writeConsoleRow.Call(uintptr(handle), uintptr(unsafe.Pointer(&cells[0])),
		uintptr(packConsoleCoord(bufferSize)), uintptr(packConsoleCoord(bufferOrigin)),
		uintptr(unsafe.Pointer(&region)))
	return ok != 0
}

func packConsoleCoord(value windows.Coord) uint32 {
	// #nosec G115 -- Win32 packs the signed 16-bit coordinate bit patterns
	// into a DWORD; these conversions intentionally preserve those bits.
	return uint32(uint16(value.X)) | uint32(uint16(value.Y))<<16
}

func consoleCells(line string, width int, defaultAttributes uint16) []consoleCell {
	if width <= 0 || width > 32767 {
		return nil
	}
	cells := make([]consoleCell, width)
	for index := range cells {
		cells[index] = consoleCell{char: ' ', attributes: defaultAttributes}
	}
	attributes := defaultAttributes
	column := 0
	for index := 0; index < len(line) && column < width; {
		if line[index] == '\x1b' && index+1 < len(line) && line[index+1] == '[' {
			if end := strings.IndexByte(line[index+2:], 'm'); end >= 0 {
				attributes = consoleSGRAttributes(line[index+2:index+2+end], attributes, defaultAttributes)
				index += end + 3
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(line[index:])
		index += size
		if r < 32 || r == 127 {
			continue
		}
		encoded := utf16.Encode([]rune{r})
		for _, character := range encoded {
			if column == width {
				break
			}
			cells[column] = consoleCell{char: character, attributes: attributes}
			column++
		}
	}
	return cells
}

func consoleSGRAttributes(sgr string, current, defaults uint16) uint16 {
	parts := strings.Split(sgr, ";")
	for index := 0; index < len(parts); index++ {
		code, err := strconv.Atoi(parts[index])
		if err != nil {
			continue
		}
		switch code {
		case 0:
			current = defaults
		case 2:
			current = current&0xfff0 | 7
		case 31, 32, 33, 34, 35, 36:
			color := map[int]uint16{31: 4, 32: 2, 33: 6, 34: 1, 35: 5, 36: 3}[code]
			current = current&0xfff0 | color | 8
		case 38:
			if index+2 < len(parts) && parts[index+1] == "5" && parts[index+2] == "208" {
				current = current&0xfff0 | 6 | 8 // closest classic-console orange
				index += 2
			}
		}
	}
	return current
}
