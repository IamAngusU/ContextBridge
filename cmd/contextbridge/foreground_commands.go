package main

import (
	"io"

	"github.com/IamAngusU/ContextBridge/internal/terminalui"
)

// foregroundServiceCommandInput takes ownership of stdin only when the panel
// has a visible command row. Hidden services, redirected logs, classic output,
// and very narrow fallback terminals must keep their stdin untouched.
func foregroundServiceCommandInput(input io.Reader, session *terminalui.Session) (<-chan string, func()) {
	if session == nil || !session.LiveCommandEditor() {
		return nil, func() {}
	}
	commands, restore, ok := rawConsoleCommandInput(input, session.SetCommandInput)
	if !ok {
		return nil, func() {}
	}
	return commands, restore
}

// waitForForegroundComponents keeps service ownership separate from display
// commands. HandleCommand may request that an attached console close, but an
// owning run/worker session is configured to refuse that request; only the
// signal context (normally Ctrl+C/SIGTERM) deliberately stops components.
func waitForForegroundComponents(stop func(), session *terminalui.Session, errorsCh <-chan error, commands <-chan string, components int) error {
	for components > 0 {
		select {
		case command, ok := <-commands:
			if !ok {
				commands = nil
				continue
			}
			_ = session.HandleCommand(command)
		case err := <-errorsCh:
			components--
			if err != nil {
				if stop != nil {
					stop()
				}
				return err
			}
		}
	}
	return nil
}
