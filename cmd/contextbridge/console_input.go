package main

import (
	"bufio"
	"io"
	"os"
	"strings"
	"unicode"
)

// consoleCommandInput uses a tiny line editor when stdin is a real terminal.
// Owning the echo lets the panel redraw without erasing partially typed text.
// Pipes retain normal line-oriented behavior for scripts and tests.
func consoleCommandInput(input io.Reader, liveEditor bool, onEdit func(string)) (<-chan string, func()) {
	if liveEditor {
		if commands, restore, ok := rawConsoleCommandInput(input, onEdit); ok {
			return commands, restore
		}
	}
	commands := make(chan string, 16)
	go func() {
		defer close(commands)
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			commands <- scanner.Text()
		}
	}()
	return commands, func() {}
}

func rawConsoleCommandInput(input io.Reader, onEdit func(string)) (<-chan string, func(), bool) {
	file, ok := input.(*os.File)
	if !ok {
		return nil, func() {}, false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil, func() {}, false
	}
	restore, raw := makeConsoleInputRaw(file)
	if !raw {
		return nil, func() {}, false
	}
	commands := make(chan string, 16)
	go readConsoleCommands(bufio.NewReader(file), commands, onEdit)
	return commands, restore, true
}

func readConsoleCommands(reader *bufio.Reader, commands chan<- string, onEdit func(string)) {
	defer close(commands)
	buffer := []rune{}
	escapeSequence := false
	for {
		character, _, err := reader.ReadRune()
		if err != nil {
			return
		}
		if escapeSequence {
			// Ignore terminal cursor/function-key escape sequences. Normal text,
			// including bracketed paste contents, continues after the terminator.
			if character == '~' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' {
				escapeSequence = false
			}
			continue
		}
		switch character {
		case 27:
			escapeSequence = true
		case '\r', '\n':
			command := strings.TrimSpace(string(buffer))
			buffer = buffer[:0]
			if onEdit != nil {
				onEdit("")
			}
			commands <- command
		case '\b', 127:
			if len(buffer) > 0 {
				buffer = buffer[:len(buffer)-1]
				if onEdit != nil {
					onEdit(string(buffer))
				}
			}
		case 3, 4, 26: // Ctrl+C, Ctrl+D, Ctrl+Z when delivered as input bytes.
			return
		case 12: // Ctrl+L
			buffer = buffer[:0]
			if onEdit != nil {
				onEdit("")
			}
			commands <- "clear"
		default:
			if !unicode.IsControl(character) && len(buffer) < 512 {
				buffer = append(buffer, character)
				if onEdit != nil {
					onEdit(string(buffer))
				}
			}
		}
	}
}

func consoleExitRequested(input io.Reader) <-chan struct{} {
	exit := make(chan struct{})
	commands, _ := consoleCommandInput(input, false, nil)
	go func() {
		for command := range commands {
			if isConsoleExitCommand(command) {
				close(exit)
				return
			}
		}
	}()
	return exit
}

func isConsoleExitCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "exit", "quit", "q", ":q":
		return true
	default:
		return false
	}
}
