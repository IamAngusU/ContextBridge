package main

import (
	"strings"
	"testing"
)

func TestChatE2EEToggle(t *testing.T) {
	state := &chatState{}
	if handled, _ := state.command("/e2ee on"); !handled || !state.e2ee {
		t.Fatal("/e2ee on did not enable encryption")
	}
	if handled, _ := state.command("/e2ee off"); !handled || state.e2ee {
		t.Fatal("/e2ee off did not disable encryption")
	}
	if _, message := state.command("/e2ee perhaps"); message == "" || state.e2ee {
		t.Fatal("invalid E2EE value was not rejected")
	}
}

func TestChatArtifactRequirement(t *testing.T) {
	state := &chatState{artifactDir: "test"}
	if handled, _ := state.command("/min-artifacts 2"); !handled || state.minArtifacts != 2 {
		t.Fatal("file requirement was not enabled")
	}
	if _, message := state.command("/min-artifacts 13"); message == "" || state.minArtifacts != 2 {
		t.Fatal("invalid file requirement was not rejected")
	}
}

func TestChatImageModeRequiresArtifactSaving(t *testing.T) {
	state := &chatState{}
	if _, message := state.command("/image on"); message == "" || state.requireImage {
		t.Fatal("image mode without artifact saving was not rejected")
	}
	state.artifactDir = "test"
	if _, message := state.command("/image on"); message == "" || !state.requireImage {
		t.Fatal("image mode was not enabled")
	}
}

func TestChatMusicModeNeedsGeminiAndDoesNotMixWithImages(t *testing.T) {
	state := &chatState{provider: "browser", profile: "chatgpt", artifactDir: "test"}
	if _, message := state.command("/music on"); message == "" || state.requireMusic {
		t.Fatal("music mode was allowed on a non-Gemini tab")
	}
	state.profile = ""
	if _, message := state.command("/music on"); message == "" || !state.requireMusic || state.profile != "gemini" {
		t.Fatal("music mode did not select Gemini")
	}
	if _, message := state.command("/profile chatgpt"); message == "" || state.profile != "gemini" {
		t.Fatal("music mode allowed an incompatible profile switch")
	}
	if _, message := state.command("/image on"); message == "" || !state.requireImage || state.requireMusic {
		t.Fatal("image mode did not replace music mode")
	}
}

func TestChatImageSeriesRequirement(t *testing.T) {
	state := &chatState{provider: "browser", profile: "chatgpt", artifactDir: "test"}
	if _, message := state.command("/min-images 3"); message == "" || !state.requireImage || state.minImages != 3 {
		t.Fatal("image series requirement was not enabled")
	}
	if _, message := state.command("/min-images 13"); message == "" || state.minImages != 3 {
		t.Fatal("invalid image series count changed state")
	}
	if _, message := state.command("/image off"); message == "" || state.requireImage || state.minImages != 0 {
		t.Fatal("image off did not clear series requirement")
	}
}

func TestChatFreshBrowserSessionMetadata(t *testing.T) {
	state := &chatState{newChat: true}
	if got := state.jobMetadata(); got["contextbridge_new_chat"] != true || got["contextbridge_new_chat_per_job"] != nil {
		t.Fatalf("unexpected new-session metadata: %#v", got)
	}
	state.newChatPerJob = true
	if got := state.jobMetadata(); got["contextbridge_new_chat"] != true || got["contextbridge_new_chat_per_job"] != true {
		t.Fatalf("unexpected per-job metadata: %#v", got)
	}
	state.requireMusic = true
	if got := state.jobMetadata(); got["contextbridge_music_tool"] != true || got["contextbridge_new_chat_per_job"] != true {
		t.Fatalf("music and new-chat metadata were not preserved together: %#v", got)
	}
	state.foregroundNewChat = true
	if got := state.jobMetadata(); got["contextbridge_foreground_new_chat"] != true {
		t.Fatalf("foreground new-chat metadata was not set: %#v", got)
	}
}

func TestChatFreshSessionRejectsNonBrowserProvider(t *testing.T) {
	for _, option := range []string{"--new-chat", "--new-chat-per-job"} {
		err := clusterChatCommand([]string{"--provider", "ollama", option, "--prompt", "unused"})
		if err == nil || !strings.Contains(err.Error(), "require --provider browser") {
			t.Fatalf("%s must reject a non-browser provider: %v", option, err)
		}
	}
}

func TestChatProfileRejectsNonBrowserProvider(t *testing.T) {
	err := clusterChatCommand([]string{"--provider", "ollama", "--profile", "gemini", "--prompt", "unused"})
	if err == nil || !strings.Contains(err.Error(), "--profile requires") {
		t.Fatalf("local provider accepted a browser profile: %v", err)
	}
}
