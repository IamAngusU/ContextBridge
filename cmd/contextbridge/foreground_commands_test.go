package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/terminalui"
)

func TestForegroundServiceCommandsDoNotConsumeRedirectedInput(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "contextbridge-output-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	session := terminalui.NewWithStyle(output, "panel")
	defer session.Close()
	session.EnableServiceCommands()
	session.Banner("v0.test", "worker")
	commands, restore := foregroundServiceCommandInput(strings.NewReader("help\n"), session)
	defer restore()
	if commands != nil {
		t.Fatal("redirected service unexpectedly took ownership of stdin")
	}
}

func TestForegroundExitCommandWaitsForTheServiceComponent(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "contextbridge-output-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	session := terminalui.NewWithStyle(output, "panel")
	defer session.Close()
	session.EnableServiceCommands()

	commands := make(chan string)
	errorsCh := make(chan error)
	done := make(chan error, 1)
	stopped := make(chan struct{}, 1)
	go func() {
		done <- waitForForegroundComponents(func() { stopped <- struct{}{} }, session, errorsCh, commands, 1)
	}()

	commands <- "exit"
	select {
	case err := <-done:
		t.Fatalf("display command stopped the foreground service: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-stopped:
		t.Fatal("display command cancelled the service context")
	default:
	}
	errorsCh <- nil
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean component stop returned an error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground wait did not return after its component stopped")
	}
}
