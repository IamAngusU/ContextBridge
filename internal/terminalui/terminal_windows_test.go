//go:build windows

package terminalui

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestNativeConsoleCellsAreFixedWidthAndColored(t *testing.T) {
	cells := consoleCells("\x1b[32m◉GPT\x1b[0m \x1b[2mIMG\x1b[0m longer than row", 12, 7)
	if len(cells) != 12 || cells[0].char != '◉' || cells[0].attributes != 10 {
		t.Fatalf("unexpected colored console cells: %+v", cells)
	}
	if cells[5].char != 'I' || cells[5].attributes != 7 {
		t.Fatalf("missing gray unsupported indicator: %+v", cells)
	}
	for _, cell := range cells {
		if cell.char == '\x1b' {
			t.Fatal("VT escape was written as a visible console character")
		}
	}
	blank := consoleCells("", 12, 7)
	for _, cell := range blank {
		if cell.char != ' ' || cell.attributes != 7 {
			t.Fatalf("native clear did not overwrite the full row: %+v", blank)
		}
	}
}

func TestNativeConsoleRowWriteLeavesCursorInPlace(t *testing.T) {
	if os.Getenv("CONTEXTBRIDGE_CONSOLE_PROBE") != "1" {
		t.Skip("requires an interactive Windows console")
	}
	info, ok := consoleStatusInfo(os.Stdout)
	if !ok {
		t.Fatal("stdout is not a Windows console")
	}
	if info.CursorPosition.Y < info.Window.Top || info.CursorPosition.Y > info.Window.Bottom {
		t.Fatal("the console cursor is outside the visible window")
	}
	if !writeStatusRow(windows.Handle(os.Stdout.Fd()), info.CursorPosition.Y,
		consoleCells("\x1b[32m◉GPT\x1b[0m TXT", int(info.Size.X), info.Attributes)) {
		t.Fatal("WriteConsoleOutputW rejected the status row")
	}
	var after windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(os.Stdout.Fd()), &after); err != nil {
		t.Fatal(err)
	}
	if after.CursorPosition != info.CursorPosition {
		t.Fatalf("native row write moved cursor from %+v to %+v", info.CursorPosition, after.CursorPosition)
	}
	if !clearConsoleStatus(os.Stdout) {
		t.Fatal("native row clear was unavailable")
	}
}
