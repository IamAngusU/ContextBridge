package terminalui

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func commandTestSession() *Session {
	return &Session{
		out:         io.Discard,
		interactive: true, commandEnabled: true, commandClosesView: true, workActions: true,
		commandIntents: make(chan ConsoleIntent, maximumConsoleActionQueue),
		commandLimits: ConsoleCommandLimits{
			MaxPromptCharacters: 64, MaxPayloadBytes: 180, PayloadOverheadBytes: 100, SessionProvider: "adapter",
		},
		observed: ServiceSnapshot{
			AdapterConnected: true,
			LocalProviders:   []string{"ollama"},
			APIProviders:     []string{"deepseek"},
			LocalModels:      []cluster.ModelCapability{{Provider: "ollama", Name: "qwen"}},
		},
		adapterSelections: map[int]adapterSelection{1: {profile: "review", model: "remote-pro", reasoning: "high"}},
	}
}

func TestSendGuideShowsConfiguredCharacterAndRelayByteLimits(t *testing.T) {
	session := commandTestSession()
	session.commandInput = "send hello"
	guide := strings.Join(session.commandGuideLinesLocked(), "\n")
	if !strings.Contains(guide, "[Length 5 · allowed 1–64]") || !strings.Contains(guide, "[Payload 107/180 bytes]") || !strings.Contains(guide, "ready") {
		t.Fatalf("valid guide omitted authoritative limits: %q", guide)
	}
	session.commandInput = "send " + strings.Repeat("x", 65)
	guide = strings.Join(session.commandGuideLinesLocked(), "\n")
	if !strings.Contains(guide, ansiRed) || !strings.Contains(guide, "Length 65") || !strings.Contains(guide, "1–64") {
		t.Fatalf("over-limit prompt was not red and explicit: %q", guide)
	}
	session.commandInput = "send"
	guide = strings.Join(session.commandGuideLinesLocked(), "\n")
	if !strings.Contains(guide, ansiYellow) || !strings.Contains(guide, "Length 0") {
		t.Fatalf("missing prompt was not an incomplete yellow state: %q", guide)
	}
}

func TestSendGuideUsesExactJSONPayloadBytes(t *testing.T) {
	session := commandTestSession()
	prompt := "a\"\\界"
	encoded, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	session.commandInput = "send " + prompt
	guide := strings.Join(session.commandGuideLinesLocked(), "\n")
	want := "Payload " + strconv.Itoa(100+len(encoded)) + "/180 bytes"
	if !strings.Contains(guide, want) {
		t.Fatalf("JSON payload metric missing %q: %q", want, guide)
	}
}

func TestCommandInputLimitReservesRoutingHeaderBeyondPromptLimit(t *testing.T) {
	session := commandTestSession()
	if got, want := session.commandInputLimitLocked(), 64+maximumConsoleCommandOverheadRunes; got != want {
		t.Fatalf("command input limit = %d, want %d", got, want)
	}
	command := "send --provider ollama --model qwen -- " + strings.Repeat("x", 64)
	session.SetCommandInput(command)
	if session.commandInput != command {
		t.Fatalf("valid maximum prompt plus routing header was truncated: got %d runes, want %d", len([]rune(session.commandInput)), len([]rune(command)))
	}
}

func TestSendFlagsExposeOnlyValidNextOptionsAndTypedIntent(t *testing.T) {
	session := commandTestSession()
	session.commandInput = "send --provider "
	guide := strings.Join(session.commandGuideLinesLocked(), "\n")
	for _, wanted := range []string{"Value required for --provider", "adapter", "deepseek", "ollama"} {
		if !strings.Contains(guide, wanted) {
			t.Fatalf("provider guidance omitted %q: %q", wanted, guide)
		}
	}

	command := "send --provider ollama --model qwen --egress local_only -- hello world"
	session.commandInput = command
	guide = strings.Join(session.commandGuideLinesLocked(), "\n")
	if strings.Contains(guide, "--profile") || strings.Contains(guide, "--reasoning") || strings.Contains(guide, "--new-session") {
		t.Fatalf("adapter-only flags remained suggested for ollama: %q", guide)
	}
	if session.HandleCommand(command) {
		t.Fatal("send requested console shutdown")
	}
	select {
	case intent := <-session.commandIntents:
		if intent.Action != ConsoleIntentSend || intent.Argument != "hello world" || intent.Submit.Provider != "ollama" || intent.Submit.Model != "qwen" || intent.Submit.Egress != "local_only" {
			t.Fatalf("typed send intent = %#v", intent)
		}
	default:
		t.Fatal("valid flagged send emitted no intent")
	}
}

func TestSendFlagsRejectContradictionsBeforeIntent(t *testing.T) {
	session := commandTestSession()
	for _, command := range []string{
		"send --provider ollama --reasoning high -- hello",
		"send --provider adapter --new-session --new-session-per-job -- hello",
		"send --provider adapter --unknown value -- hello",
		"send --max-cost-usd NaN -- hello",
	} {
		session.HandleCommand(command)
		if !strings.Contains(session.commandNotice, "Send rejected") {
			t.Fatalf("contradiction was not rejected for %q: %q", command, session.commandNotice)
		}
		select {
		case intent := <-session.commandIntents:
			t.Fatalf("invalid command emitted intent %#v", intent)
		default:
		}
	}
}

func TestJobGuideAndExecutionShareSafeIDBoundary(t *testing.T) {
	session := commandTestSession()
	for _, item := range []struct {
		input string
		color string
	}{
		{"job ", ansiYellow},
		{"job ../foreign", ansiRed},
		{"job owned-1", ""},
	} {
		session.commandInput = item.input
		guide := strings.Join(session.commandGuideLinesLocked(), "\n")
		if !strings.Contains(guide, "allowed 1–128") || item.color != "" && !strings.Contains(guide, item.color) {
			t.Fatalf("job guide for %q = %q", item.input, guide)
		}
	}
	session.HandleCommand("cancel ../foreign")
	if !strings.Contains(session.commandNotice, "job ID") {
		t.Fatalf("unsafe cancellation was not rejected: %q", session.commandNotice)
	}
}

func TestFeatureIndicatorsAreGreenWhenEnabledAndDimWhenDisabled(t *testing.T) {
	label := featureIndicatorLabel([]FeatureState{{Label: "WRK", Enabled: true}, {Label: "RLY", Enabled: false}})
	if !strings.Contains(label, ansiGreen+"WRK"+ansiReset) || !strings.Contains(label, ansiDim+"RLY"+ansiReset) {
		t.Fatalf("feature indicator colors = %q", label)
	}
}
