package main

import (
	"strings"
	"testing"
)

func TestChatStatusLabelsStayEnglishAcrossHostLocales(t *testing.T) {
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANG", "de_DE.UTF-8")

	lines := []string{
		chatRequestSummary("adapter", "profile-one", "model-one", "high"),
		chatEndpointReport("model-two", "medium"),
		chatUsedReport("ollama", "model-three"),
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"requested:", "model ", "reasoning ", "Endpoint reports:", "used:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("chat status is missing English label %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"angefragt", "Modell", "Denkstufe", "verwendet"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("chat status leaked German label %q:\n%s", unwanted, joined)
		}
	}
}

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

func TestChatImageSeriesRequirement(t *testing.T) {
	state := &chatState{provider: "adapter", profile: "profile-one", artifactDir: "test"}
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

func TestChatFreshAdapterSessionMetadata(t *testing.T) {
	state := &chatState{newSession: true}
	if got := state.jobMetadata(); got["contextbridge_new_session"] != true || got["contextbridge_new_session_per_job"] != nil {
		t.Fatalf("unexpected new-session metadata: %#v", got)
	}
	state.newSessionPerJob = true
	if got := state.jobMetadata(); got["contextbridge_new_session"] != true || got["contextbridge_new_session_per_job"] != true {
		t.Fatalf("unexpected per-job metadata: %#v", got)
	}
	state.foregroundNewSession = true
	if got := state.jobMetadata(); got["contextbridge_foreground_new_session"] != true {
		t.Fatalf("foreground new-session metadata was not set: %#v", got)
	}
}

func TestChatFreshSessionRejectsNonAdapterProvider(t *testing.T) {
	for _, option := range []string{"--new-session", "--new-session-per-job"} {
		err := clusterChatCommand([]string{"--provider", "ollama", option, "--prompt", "unused"})
		if err == nil || !strings.Contains(err.Error(), "require --provider adapter") {
			t.Fatalf("%s must reject a non-adapter provider: %v", option, err)
		}
	}
}

func TestChatProfileRejectsNonAdapterProvider(t *testing.T) {
	err := clusterChatCommand([]string{"--provider", "ollama", "--profile", "profile-two", "--prompt", "unused"})
	if err == nil || !strings.Contains(err.Error(), "--profile requires") {
		t.Fatalf("local provider accepted a adapter profile: %v", err)
	}
}

func TestChatRejectsBashContinuationInWindowsStyleInvocation(t *testing.T) {
	err := clusterChatCommand([]string{`\\`})
	if err == nil || !strings.Contains(err.Error(), "Bash only") || !strings.Contains(err.Error(), "Windows CMD") {
		t.Fatalf("stray Bash continuation should fail with a cross-shell hint: %v", err)
	}
}

func TestInteractiveChatDoesNotSubmitPastedFlags(t *testing.T) {
	for _, line := range []string{
		`--config /var/lib/contextbridge/config.yml \\`,
		`--profile=profile-one`,
		`--prompt "hello"`,
		`\\`,
	} {
		if !looksLikePastedChatFlag(line) {
			t.Fatalf("pasted command fragment was not recognized: %q", line)
		}
	}
	for _, line := range []string{"Explain --profile in prose", "normal prompt", "/settings"} {
		if looksLikePastedChatFlag(line) {
			t.Fatalf("ordinary interactive input was misclassified: %q", line)
		}
	}
}
