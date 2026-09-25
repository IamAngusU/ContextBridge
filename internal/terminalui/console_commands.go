package terminalui

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

// ConsoleSubmitOptions is the deliberately small, shell-free subset of
// routing controls accepted by the interactive `send` command.
type ConsoleSubmitOptions struct {
	Provider         string
	Model            string
	Group            string
	SessionID        string
	Profile          string
	Reasoning        string
	Egress           string
	MaxCostUSD       float64
	MaxCostSet       bool
	FreshSession     bool
	EphemeralSession bool
}

// ConsoleCommandLimits combines a user-configurable character guard with the
// authenticated relay's current payload contract. PayloadOverheadBytes is the
// exact encoded job payload size excluding the JSON string that holds Prompt.
type ConsoleCommandLimits struct {
	MaxPromptCharacters  int
	MaxPayloadBytes      int
	PayloadOverheadBytes int
	SessionProvider      string
}

type parsedConsoleSend struct {
	prompt    string
	options   ConsoleSubmitOptions
	set       map[string]bool
	pending   string
	usesFlags bool
	hasPrompt bool
	err       string
}

var consoleSendValueFlags = []string{
	"--provider", "--model", "--group", "--session", "--profile", "--reasoning", "--egress", "--max-cost-usd",
}

var consoleSendBooleanFlags = []string{"--new-session", "--new-session-per-job"}

func defaultConsoleCommandLimits() ConsoleCommandLimits {
	return ConsoleCommandLimits{MaxPromptCharacters: maximumConsolePromptRunes, SessionProvider: "adapter"}
}

func normalizeConsoleCommandLimits(limits ConsoleCommandLimits) ConsoleCommandLimits {
	if limits.MaxPromptCharacters < 64 || limits.MaxPromptCharacters > 65536 {
		limits.MaxPromptCharacters = maximumConsolePromptRunes
	}
	if limits.MaxPayloadBytes < 0 {
		limits.MaxPayloadBytes = 0
	}
	if limits.PayloadOverheadBytes < 0 || limits.PayloadOverheadBytes >= limits.MaxPayloadBytes && limits.MaxPayloadBytes > 0 {
		limits.PayloadOverheadBytes = 0
	}
	limits.SessionProvider = strings.ToLower(strings.TrimSpace(limits.SessionProvider))
	if limits.SessionProvider == "" {
		limits.SessionProvider = "adapter"
	}
	return limits
}

func (s *Session) effectiveCommandLimitsLocked() ConsoleCommandLimits {
	if s.commandLimits.MaxPromptCharacters == 0 {
		return defaultConsoleCommandLimits()
	}
	return normalizeConsoleCommandLimits(s.commandLimits)
}

func (s *Session) commandInputLimitLocked() int {
	limits := s.effectiveCommandLimitsLocked()
	// Keep enough room to type one visibly invalid value and receive a useful
	// red diagnostic instead of silently truncating exactly at the valid edge.
	return min(65792, limits.MaxPromptCharacters+256)
}

func (s *Session) parseSendIntentLocked(command string) (ConsoleIntent, string, bool) {
	parsed := s.parseConsoleSendLocked(command)
	if parsed.err != "" {
		return ConsoleIntent{}, "Send rejected · " + parsed.err, false
	}
	if parsed.pending != "" {
		return ConsoleIntent{}, "Send rejected · " + parsed.pending + " needs a value", false
	}
	if parsed.usesFlags && !parsed.hasPrompt {
		return ConsoleIntent{}, "Send rejected · add `-- TEXT` after the routing flags", false
	}
	valid, reason, _, _ := s.validateConsolePromptLocked(parsed.prompt)
	if !valid {
		return ConsoleIntent{}, "Send rejected · " + reason, false
	}
	return ConsoleIntent{Action: ConsoleIntentSend, Argument: parsed.prompt, Submit: parsed.options}, "", true
}

func (s *Session) parseConsoleSendLocked(command string) parsedConsoleSend {
	rest := commandArgument(command)
	parsed := parsedConsoleSend{set: map[string]bool{}}
	if !strings.HasPrefix(rest, "--") {
		parsed.prompt = strings.TrimSpace(rest)
		parsed.hasPrompt = parsed.prompt != ""
		return parsed
	}
	parsed.usesFlags = true
	header, prompt, separator := splitConsoleFlagPrompt(rest)
	parsed.prompt = strings.TrimSpace(prompt)
	parsed.hasPrompt = separator && parsed.prompt != ""
	fields := strings.Fields(header)
	for index := 0; index < len(fields); index++ {
		name, inline, hasInline := strings.Cut(fields[index], "=")
		name = strings.ToLower(name)
		if !strings.HasPrefix(name, "--") {
			parsed.err = fmt.Sprintf("unexpected value %q before `-- TEXT`", fields[index])
			return parsed
		}
		if parsed.set[name] {
			parsed.err = name + " may be set only once"
			return parsed
		}
		parsed.set[name] = true
		if containsString(consoleSendBooleanFlags, name) {
			if hasInline {
				parsed.err = name + " does not accept a value"
				return parsed
			}
			switch name {
			case "--new-session":
				parsed.options.FreshSession = true
			case "--new-session-per-job":
				parsed.options.FreshSession = true
				parsed.options.EphemeralSession = true
			}
			continue
		}
		if !containsString(consoleSendValueFlags, name) {
			parsed.err = "unknown send flag " + name
			return parsed
		}
		value := inline
		if !hasInline {
			if index+1 >= len(fields) || strings.HasPrefix(fields[index+1], "--") {
				parsed.pending = name
				return parsed
			}
			index++
			value = fields[index]
		}
		value = strings.TrimSpace(value)
		if value == "" {
			parsed.pending = name
			return parsed
		}
		switch name {
		case "--provider":
			parsed.options.Provider = value
		case "--model":
			parsed.options.Model = value
		case "--group":
			parsed.options.Group = value
		case "--session":
			parsed.options.SessionID = value
		case "--profile":
			parsed.options.Profile = value
		case "--reasoning":
			parsed.options.Reasoning = value
		case "--egress":
			if value != "local_only" && value != "remote_allowed" {
				parsed.err = "--egress must be local_only or remote_allowed"
				return parsed
			}
			parsed.options.Egress = value
		case "--max-cost-usd":
			cost, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
				parsed.err = "--max-cost-usd must be a finite number at or above 0"
				return parsed
			}
			parsed.options.MaxCostUSD, parsed.options.MaxCostSet = cost, true
		}
	}
	if parsed.set["--new-session"] && parsed.set["--new-session-per-job"] {
		parsed.err = "--new-session and --new-session-per-job are mutually exclusive"
		return parsed
	}
	adapter := s.effectiveCommandLimitsLocked().SessionProvider
	provider := strings.ToLower(parsed.options.Provider)
	if provider == "" {
		for _, name := range []string{"--profile", "--reasoning", "--new-session", "--new-session-per-job"} {
			if parsed.set[name] {
				parsed.err = name + " requires --provider " + adapter
				return parsed
			}
		}
	}
	if provider != "" && provider != adapter {
		for _, name := range []string{"--profile", "--reasoning", "--new-session", "--new-session-per-job"} {
			if parsed.set[name] {
				parsed.err = name + " requires --provider " + adapter
				return parsed
			}
		}
	}
	for name, value := range map[string]string{
		"--provider": parsed.options.Provider, "--model": parsed.options.Model, "--group": parsed.options.Group,
		"--profile": parsed.options.Profile, "--reasoning": parsed.options.Reasoning,
	} {
		if value != "" && invalidRoutingLabel(value, 160) {
			parsed.err = name + " must be 1–160 UTF-8 bytes without control characters"
			return parsed
		}
	}
	if parsed.options.Provider != "" && (len([]byte(parsed.options.Provider)) > 80 || strings.Contains(parsed.options.Provider, "..")) {
		parsed.err = "--provider must be 1–80 safe UTF-8 bytes"
	}
	if parsed.options.Profile != "" && len([]byte(parsed.options.Profile)) > 80 {
		parsed.err = "--profile must be 1–80 UTF-8 bytes"
	}
	if parsed.options.SessionID != "" && invalidRoutingLabel(parsed.options.SessionID, 128) {
		parsed.err = "--session must be 1–128 UTF-8 bytes without control characters"
	}
	return parsed
}

func splitConsoleFlagPrompt(value string) (header, prompt string, found bool) {
	for index := 0; index < len(value); {
		for index < len(value) && unicode.IsSpace(rune(value[index])) {
			index++
		}
		start := index
		for index < len(value) && !unicode.IsSpace(rune(value[index])) {
			index++
		}
		if start < index && value[start:index] == "--" {
			return strings.TrimSpace(value[:start]), strings.TrimSpace(value[index:]), true
		}
	}
	return strings.TrimSpace(value), "", false
}

func invalidRoutingLabel(value string, maximumBytes int) bool {
	return value == "" || strings.TrimSpace(value) != value || len([]byte(value)) > maximumBytes || strings.IndexFunc(value, unicode.IsControl) >= 0
}

func (s *Session) validateConsolePromptLocked(prompt string) (valid bool, reason string, characters, payloadBytes int) {
	limits := s.effectiveCommandLimitsLocked()
	characters = utf8.RuneCountInString(prompt)
	encoded, _ := json.Marshal(prompt)
	payloadBytes = limits.PayloadOverheadBytes + len(encoded)
	if characters < 1 {
		return false, "prompt length must be at least 1 character", characters, payloadBytes
	}
	if characters > limits.MaxPromptCharacters {
		return false, fmt.Sprintf("prompt length must be 1–%d characters", limits.MaxPromptCharacters), characters, payloadBytes
	}
	if limits.MaxPayloadBytes > 0 && payloadBytes > limits.MaxPayloadBytes {
		return false, fmt.Sprintf("encoded payload is %d bytes; relay limit is %d", payloadBytes, limits.MaxPayloadBytes), characters, payloadBytes
	}
	return true, "", characters, payloadBytes
}

func (s *Session) sendCommandGuideLocked(raw string) []string {
	parsed := s.parseConsoleSendLocked(raw)
	usage := "send [routing flags] -- TEXT · or send TEXT"
	if parsed.err != "" {
		return []string{ansiRed + "Invalid · " + parsed.err + ansiReset, usage}
	}
	if parsed.pending != "" {
		choices := s.consoleFlagChoicesLocked(parsed.pending, parsed.options.Provider)
		line := "Value required for " + parsed.pending
		if len(choices) > 0 {
			line += " · " + strings.Join(choices, " · ")
		}
		return []string{ansiYellow + line + ansiReset, usage}
	}
	lines := []string{usage}
	valid, reason, characters, payloadBytes := s.validateConsolePromptLocked(parsed.prompt)
	limits := s.effectiveCommandLimitsLocked()
	length := fmt.Sprintf("[Length %d · allowed 1–%d]", characters, limits.MaxPromptCharacters)
	if limits.MaxPayloadBytes > 0 {
		length += fmt.Sprintf(" [Payload %d/%d bytes]", payloadBytes, limits.MaxPayloadBytes)
	}
	if !parsed.hasPrompt {
		lines = append(lines, ansiYellow+length+" · add `-- TEXT`"+ansiReset)
	} else if !valid {
		lines = append(lines, ansiRed+length+" · "+reason+ansiReset)
	} else {
		lines = append(lines, length+" · ready")
	}
	remaining := s.remainingConsoleFlagsLocked(parsed)
	if len(remaining) > 0 && parsed.usesFlags && !parsed.hasPrompt {
		lines = append(lines, "Next · "+strings.Join(remaining, " · ")+" · -- TEXT")
	}
	return lines
}

func (s *Session) remainingConsoleFlagsLocked(parsed parsedConsoleSend) []string {
	flags := append(append([]string(nil), consoleSendValueFlags...), consoleSendBooleanFlags...)
	adapter := s.effectiveCommandLimitsLocked().SessionProvider
	provider := strings.ToLower(parsed.options.Provider)
	result := make([]string, 0, len(flags))
	for _, flag := range flags {
		if parsed.set[flag] {
			continue
		}
		if provider != "" && provider != adapter && containsString([]string{"--profile", "--reasoning", "--new-session", "--new-session-per-job"}, flag) {
			continue
		}
		if parsed.set["--new-session"] && flag == "--new-session-per-job" || parsed.set["--new-session-per-job"] && flag == "--new-session" {
			continue
		}
		result = append(result, flag)
	}
	return result
}

func (s *Session) consoleFlagChoicesLocked(flag, provider string) []string {
	values := []string{}
	switch flag {
	case "--provider":
		values = append(values, s.observed.LocalProviders...)
		values = append(values, s.observed.APIProviders...)
		if s.observed.AdapterConnected || len(s.adapterSelections) > 0 {
			values = append(values, s.effectiveCommandLimitsLocked().SessionProvider)
		}
	case "--model":
		for _, model := range append(append([]cluster.ModelCapability(nil), s.observed.LocalModels...), s.observed.APIModels...) {
			if provider == "" || strings.EqualFold(provider, model.Provider) {
				values = append(values, model.Name)
			}
		}
		for _, selection := range s.adapterSelections {
			if selection.model != "" && (provider == "" || strings.EqualFold(provider, s.effectiveCommandLimitsLocked().SessionProvider)) {
				values = append(values, selection.model)
			}
		}
	case "--profile":
		for _, selection := range s.adapterSelections {
			values = append(values, selection.profile)
		}
	case "--reasoning":
		for _, selection := range s.adapterSelections {
			values = append(values, selection.reasoning)
		}
	case "--egress":
		values = append(values, "local_only", "remote_allowed")
	}
	clean := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = cleanTerminalLabel(value, 80)
		key := strings.ToLower(value)
		if value != "" && !seen[key] {
			seen[key] = true
			clean = append(clean, value)
		}
	}
	sort.Slice(clean, func(i, j int) bool { return strings.ToLower(clean[i]) < strings.ToLower(clean[j]) })
	if len(clean) > 8 {
		clean = append(clean[:8], "…")
	}
	return clean
}

func (s *Session) jobCommandGuideLocked(command, raw string) []string {
	argument := commandArgument(raw)
	length := utf8.RuneCountInString(argument)
	metric := fmt.Sprintf("[Length %d · allowed 1–128]", length)
	if argument == "" {
		return []string{ansiYellow + metric + " · job ID required" + ansiReset, command + " ID"}
	}
	if err := validateConsoleJobID(argument); err != "" {
		return []string{ansiRed + metric + " · " + err + ansiReset, command + " ID"}
	}
	return []string{metric + " · ready", command + " " + argument}
}

func validateConsoleJobID(value string) string {
	if value == "" || len(value) > 128 || strings.Contains(value, "..") {
		return "job ID must use 1–128 safe characters and not contain '..'"
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		alphaNumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if index == 0 && !alphaNumeric {
			return "job ID must start with an ASCII letter or number"
		}
		if index > 0 && !alphaNumeric && character != '.' && character != '_' && character != '-' {
			return "job ID accepts only ASCII letters, numbers, '.', '_', and '-'"
		}
	}
	return ""
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
