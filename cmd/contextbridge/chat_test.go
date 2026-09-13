package main

import "testing"

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
