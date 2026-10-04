package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
)

func TestChatLocalToolsDoNotSubmitToPool(t *testing.T) {
	state := chatState{localTools: true, relayURL: "http://127.0.0.1:1"}
	if err := state.turn(context.Background(), "Was macht 10 mal 3 / 30?"); err != nil {
		t.Fatal(err)
	}
	if err := state.turn(context.Background(), "Calculate 1/0"); err == nil {
		t.Fatal("division by zero accepted")
	}
	if err := state.turn(context.Background(), "Draft a letter"); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("non-tool request bypassed authentication: %v", err)
	}
}

func TestChatLocalToolsPreserveExplicitContracts(t *testing.T) {
	for _, state := range []chatState{
		{localTools: false}, {localTools: true, e2ee: true}, {localTools: true, profile: "selected"},
		{localTools: true, images: []bridge.ImageInput{{Name: "input.png"}}},
		{localTools: true, requireImage: true}, {localTools: true, minArtifacts: 1},
		{localTools: true, minImages: 1}, {localTools: true, newSession: true},
	} {
		if state.localToolsEligible() {
			t.Fatalf("ignored output/routing constraint: %#v", state)
		}
	}
	state := chatState{}
	if handled, _ := state.command("/tools auto"); !handled || !state.localTools {
		t.Fatal("tools auto did not enable")
	}
	state.command("/tools off")
	if state.localTools {
		t.Fatal("tools off did not disable")
	}
}

func TestMCPLocalToolWorksWithoutServiceAndStrictlyRejectsEffects(t *testing.T) {
	server := newTestMCPServer("http://127.0.0.1:1", "unused")
	responses := runMCPTranscript(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contextbridge.local_tool","arguments":{"prompt":"Was macht 10 mal 3 / 30?"}}}`,
	)
	if result := toolStructured(t, responses[1]); result["tool"] != "calculator" || result["text"] != "10 * 3 / 30 = 1" {
		t.Fatal(result)
	}
	for _, raw := range []string{`{"prompt":"1+1","shell":"echo x"}`, `{"prompt":"1+1","Prompt":"2+2"}`, `{"prompt":"write file.txt"}`, `{"prompt":null}`, `{"prompt":"1 / 0"}`} {
		if _, err := callLocalTool(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
