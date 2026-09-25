package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootUsageIsTaskOrientedAndExplainsConsoleBoundary(t *testing.T) {
	var output bytes.Buffer
	writeUsage(&output)
	shown := output.String()
	for _, want := range []string{
		"START HERE",
		"SEND AND INSPECT WORK",
		"POOL AND ROUTING",
		"LOCAL RESOURCES AND INTEGRATIONS",
		"OPERATE AND MAINTAIN",
		"contextbridge guide",
		"contextbridge cluster lan init|join|status",
		"contextbridge COMMAND --help",
		"console input controls only the live view",
		`"exit" closes an attached console without stopping the service`,
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("root usage is missing %q:\n%s", want, shown)
		}
	}
	if strings.Contains(shown, "\tcontextbridge") {
		t.Fatalf("root usage contains inconsistent tab indentation:\n%s", shown)
	}
}

func TestDispatcherHelpDoesNotNeedConfigOrNetwork(t *testing.T) {
	for _, item := range []struct {
		path []string
		want string
	}{
		{[]string{"schedule"}, "add|list|show|pause|resume|run|delete"},
		{[]string{"cluster"}, "Observe:  status, events, node"},
		{[]string{"cluster", "agent"}, "agent auto|plan|run"},
		{[]string{"cluster", "lan"}, "lan init|join|status"},
		{[]string{"cluster", "receipt"}, "receipt show|export|verify|keygen"},
	} {
		var output bytes.Buffer
		if !writeCommandGroupHelp(&output, item.path) {
			t.Fatalf("help path %q was not recognized", strings.Join(item.path, " "))
		}
		if !strings.Contains(output.String(), item.want) {
			t.Errorf("help path %q is missing %q: %s", strings.Join(item.path, " "), item.want, output.String())
		}
	}
	var output bytes.Buffer
	if writeCommandGroupHelp(&output, []string{"definitely-unknown"}) {
		t.Fatal("unknown help path was accepted")
	}
}
