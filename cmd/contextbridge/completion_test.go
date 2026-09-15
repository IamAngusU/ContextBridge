package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletionScriptsCoverBothAliasesAndNestedCommands(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   []string
	}{
		{"powershell", powershellCompletionScript(), []string{"-CommandName contextbridge, cb", "'serve'", "'stop' = @('--config','--force')", "'selftest'", "'selftest' = @('--config','--providers'", "'cluster status' = @('--config','--json')", "--attach-image", "--reasoning"}},
		{"bash", bashCompletionScript(), []string{"# ContextBridge managed completion", "contextbridge cb", "init serve run stop", "stop) candidates=\"--config --force\"", "cluster) candidates=\"status submit chat selftest", "\"cluster status\") candidates=\"--config --json\"", "--attach-image", "--reasoning"}},
		{"zsh", zshCompletionScript(), []string{"#compdef contextbridge cb", "# ContextBridge managed completion", "serve:Run only the local bridge service", "stop:Safely stop the local ContextBridge process", "cluster:Use a remote pool", "status submit chat selftest", "completion)", "--attach-image[", "--job-timeout[", "--managed-service[", "--discover["}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, want := range test.want {
				if !strings.Contains(test.script, want) {
					t.Errorf("completion script is missing %q", want)
				}
			}
			if strings.ContainsAny(test.script, "\x00") || !strings.HasSuffix(test.script, "\n") {
				t.Error("completion script is not clean newline-terminated text")
			}
		})
	}
}

func TestPowerShellCompletionEscapesSingleQuotes(t *testing.T) {
	if got := quotePowerShellList([]string{"plain", "it's"}); got != "'plain','it''s'" {
		t.Fatalf("unexpected PowerShell quoting: %q", got)
	}
}

func TestPowerShellTabExpansionTracksTrailingSpaceAndOptionPosition(t *testing.T) {
	shell := powerShellForTest(t)
	tests := []struct {
		name      string
		line      string
		want      []string
		forbidden []string
	}{
		{"provider value", "contextbridge cluster chat --provider ", []string{"browser", "ollama", "nuextract", "jina"}, []string{"status", "selftest", "--provider"}},
		{"provider partial value", "contextbridge cluster chat --provider b", []string{"browser"}, []string{"status", "--profile"}},
		{"flags after provider value", "contextbridge cluster chat --provider browser ", []string{"--config", "--profile", "--session"}, []string{"status", "selftest"}},
		{"flags after selftest boolean", "contextbridge cluster selftest --run ", []string{"--config", "--image", "--job-timeout"}, []string{"status", "chat"}},
		{"flags after nested json", "contextbridge cluster status --json ", []string{"--config", "--json"}, []string{"--token", "submit", "selftest"}},
		{"short alias provider", "cb cluster chat --provider ", []string{"browser", "ollama"}, []string{"status", "selftest"}},
		{"root selftest shortcut", "contextbridge selftest --run ", []string{"--config", "--image", "--job-timeout"}, []string{"status", "chat"}},
		{"short selftest shortcut", "cb selftest ", []string{"--providers", "--run", "--timeout"}, []string{"status", "chat"}},
		{"short selftest partial flag", "cb selftest --r", []string{"--run"}, []string{"--providers", "status", "chat"}},
		{"nested partial action", "contextbridge cluster se", []string{"selftest"}, []string{"status", "submit"}},
		{"safe stop", "contextbridge stop ", []string{"--config", "--force"}, []string{"--slots", "status"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := runPowerShellTabExpansion(t, shell, test.line)
			matches := map[string]bool{}
			for _, value := range strings.Fields(output) {
				matches[value] = true
			}
			for _, want := range test.want {
				if !matches[want] {
					t.Errorf("TabExpansion2 for %q is missing %s; got %q", test.line, want, output)
				}
			}
			for _, forbidden := range test.forbidden {
				if matches[forbidden] {
					t.Errorf("TabExpansion2 for %q returned wrong candidate %s; got %q", test.line, forbidden, output)
				}
			}
		})
	}
}

func powerShellForTest(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"pwsh", "powershell", "powershell.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("PowerShell is not installed")
	return ""
}

func runPowerShellTabExpansion(t *testing.T, shell, line string) string {
	t.Helper()
	script := powershellCompletionScript() + "\n$line = '" + strings.ReplaceAll(line, "'", "''") + "'\n" +
		"$result = TabExpansion2 -inputScript $line -cursorColumn $line.Length\n" +
		"$result.CompletionMatches | ForEach-Object { $_.CompletionText }\n"
	path := filepath.Join(t.TempDir(), "completion-test.ps1")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(shell, "-NoProfile", "-NonInteractive", "-File", path)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run TabExpansion2 for %q: %v\n%s", line, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestCompletionRootCommandsStayUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, command := range completionRootCommands {
		if seen[command] {
			t.Fatalf("duplicate completion command %q", command)
		}
		seen[command] = true
	}
	for _, required := range []string{"serve", "stop", "console", "cluster", "selftest", "completion", "version"} {
		if !seen[required] {
			t.Errorf("completion root is missing %q", required)
		}
	}
}

func TestCompletionDoesNotInventClusterStatusTokenFlag(t *testing.T) {
	powerShell := powershellCompletionScript()
	start := strings.Index(powerShell, "'cluster status' =")
	if start < 0 {
		t.Fatal("PowerShell completion has no cluster status option declaration")
	}
	line := strings.SplitN(powerShell[start:], "\n", 2)[0]
	if strings.Contains(line, "--token") {
		t.Fatalf("PowerShell completion invented an unsupported cluster status flag: %s", line)
	}

	bash := bashCompletionScript()
	start = strings.Index(bash, `"cluster status")`)
	if start < 0 {
		t.Fatal("bash completion has no cluster status option declaration")
	}
	line = strings.SplitN(bash[start:], "\n", 2)[0]
	if strings.Contains(line, "--token") {
		t.Fatalf("bash completion invented an unsupported cluster status flag: %s", line)
	}
}

func TestBashCompletionOffersRootCommandFlagsAtCurrentWord(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	tests := []struct {
		name      string
		words     string
		cursor    int
		want      []string
		forbidden []string
	}{
		{"run", "contextbridge run --", 2, []string{"--config", "--slots", "--topmost"}, nil},
		{"stop", "contextbridge stop --", 2, []string{"--config", "--force"}, nil},
		{"submit", "contextbridge submit --", 2, []string{"--file", "--artifacts"}, nil},
		{"review", "contextbridge review --", 2, []string{"--job-dir"}, nil},
		{"pair", "contextbridge pair --", 2, []string{"--relay", "--identity", "--name"}, nil},
		{"worker", "contextbridge worker --", 2, []string{"--slots", "--providers", "--no-updates"}, nil},
		{"status", "contextbridge status --", 2, []string{"--config", "--json"}, []string{"--token"}},
		{"doctor", "contextbridge doctor --", 2, []string{"--config", "--json"}, nil},
		{"models", "contextbridge models --", 2, []string{"--config", "--json", "--discover"}, nil},
		{"hardware", "contextbridge hardware --", 2, []string{"--json"}, []string{"--config"}},
		{"update root", "contextbridge update --", 2, []string{"--force", "--managed-service", "--relay-only"}, nil},
		{"update action", "contextbridge update apply --", 3, []string{"--config", "--force"}, nil},
		{"cluster selftest", "contextbridge cluster selftest --", 3, []string{"--run", "--job-timeout"}, nil},
		{"root selftest", "contextbridge selftest --", 2, []string{"--providers", "--run", "--job-timeout"}, nil},
		{"short root selftest", "cb selftest --", 2, []string{"--providers", "--run"}, nil},
		{"after selftest boolean", "contextbridge cluster selftest --run ''", 4, []string{"--image", "--job-timeout"}, nil},
		{"after chat boolean", "contextbridge cluster chat --e2ee ''", 4, []string{"--session", "--profile"}, nil},
		{"after root boolean", "contextbridge status --json ''", 3, []string{"--config"}, []string{"--token"}},
		{"after update boolean", "contextbridge update apply --force ''", 4, []string{"--json", "--relay-only"}, nil},
		{"cluster status", "contextbridge cluster status --", 3, []string{"--config", "--json"}, []string{"--token"}},
		{"short alias", "cb run --", 2, []string{"--slots"}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := runBashCompletion(t, test.words, test.cursor)
			fields := map[string]bool{}
			for _, value := range strings.Fields(output) {
				fields[value] = true
			}
			for _, want := range test.want {
				if !fields[want] {
					t.Errorf("completion for %q is missing %s; got %q", test.words, want, output)
				}
			}
			for _, forbidden := range test.forbidden {
				if fields[forbidden] {
					t.Errorf("completion for %q invented %s; got %q", test.words, forbidden, output)
				}
			}
		})
	}
}

func runBashCompletion(t *testing.T, words string, cursor int) string {
	t.Helper()
	command := exec.Command("bash", "-s")
	command.Stdin = strings.NewReader(bashCompletionScript() + "\nCOMP_WORDS=(" + words + ")\nCOMP_CWORD=" + fmt.Sprint(cursor) + "\n_contextbridge_complete\nprintf '%s\\n' \"${COMPREPLY[*]}\"\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run generated bash completion: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}
