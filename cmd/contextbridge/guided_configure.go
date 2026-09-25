package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"unicode"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

const maximumGuidedPromptAttempts = 3

func guideCommand(args []string) error {
	guidedArgs := append([]string(nil), args...)
	guidedArgs = append(guidedArgs, "--interactive=true")
	return clusterConfigureCommandWithIO(guidedArgs, os.Stdin, os.Stdout, interactiveFiles(os.Stdin, os.Stdout), true)
}

func interactiveFiles(input, output *os.File) bool {
	inputInfo, inputErr := input.Stat()
	outputInfo, outputErr := output.Stat()
	return inputErr == nil && outputErr == nil && inputInfo.Mode()&os.ModeCharDevice != 0 && outputInfo.Mode()&os.ModeCharDevice != 0
}

func guideClusterConfiguration(input io.Reader, output io.Writer, cfg config.Config, mode, relayURL, publicURL, name, listen *string, provided map[string]bool, chooseMode bool) (bool, error) {
	reader := bufio.NewReader(input)
	sources := map[string]string{}
	if _, err := fmt.Fprintln(output, "ContextBridge guided setup"); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintln(output, "Only unresolved setup values are requested. Nothing is saved before the final confirmation."); err != nil {
		return false, err
	}

	if provided["mode"] {
		sources["mode"] = "flag"
	} else if chooseMode {
		selected, err := promptGuidedChoice(reader, output, "Role", currentClusterMode(cfg), []string{"local", "client", "relay", "worker", "all"})
		if err != nil {
			return false, err
		}
		*mode = selected
		sources["mode"] = "prompt"
	} else {
		*mode = currentClusterMode(cfg)
		sources["mode"] = "config"
	}
	configuredMode := strings.ToLower(strings.TrimSpace(*mode))
	if configuredMode == "sender" {
		configuredMode = "client"
		*mode = configuredMode
	}
	if !guidedModeAllowed(configuredMode) {
		return false, errors.New("--mode must be local, client (or sender), relay, worker, or all")
	}

	usesRemoteRelay := configuredMode == "client" || configuredMode == "worker"
	if usesRemoteRelay || provided["relay-url"] {
		if provided["relay-url"] {
			sources["relay"] = "flag"
		} else if value := strings.TrimSpace(cfg.Cluster.Worker.RelayURL); value != "" {
			*relayURL = value
			sources["relay"] = "config"
		} else if value := strings.TrimSpace(os.Getenv("CONTEXTBRIDGE_RELAY_URL")); value != "" {
			*relayURL = value
			sources["relay"] = "environment"
		}
		if usesRemoteRelay && strings.TrimSpace(*relayURL) == "" {
			value, err := promptGuidedRequired(reader, output, "Relay URL (HTTPS, or localhost)")
			if err != nil {
				return false, err
			}
			*relayURL = value
			sources["relay"] = "prompt"
		}
		if value := strings.TrimSpace(*relayURL); value != "" {
			value = strings.TrimRight(value, "/")
			if err := cluster.ValidateRelayURL(value); err != nil {
				return false, fmt.Errorf("--relay-url: %w", err)
			}
			*relayURL = value
		}
	}

	usesLocalRelay := configuredMode == "relay" || configuredMode == "all"
	if usesLocalRelay || provided["public-url"] {
		if provided["public-url"] {
			sources["public"] = "flag"
		} else if value := strings.TrimSpace(cfg.Cluster.Relay.PublicURL); value != "" {
			*publicURL = value
			sources["public"] = "config"
		} else if value := strings.TrimSpace(os.Getenv("CONTEXTBRIDGE_PUBLIC_URL")); value != "" {
			*publicURL = value
			sources["public"] = "environment"
		}
		if value := strings.TrimSpace(*publicURL); value != "" {
			value = strings.TrimRight(value, "/")
			if err := cluster.ValidateRelayURL(value); err != nil || !strings.HasPrefix(strings.ToLower(value), "https://") {
				return false, errors.New("--public-url must be a valid HTTPS relay URL")
			}
			*publicURL = value
		}
	}

	if usesLocalRelay || provided["listen"] {
		if provided["listen"] {
			sources["listen"] = "flag"
		} else if value := strings.TrimSpace(cfg.Cluster.Relay.Listen); value != "" {
			*listen = value
			sources["listen"] = "config"
		} else {
			*listen = "127.0.0.1:32150"
			sources["listen"] = "default"
		}
		if *listen != "auto" && !strings.HasPrefix(*listen, "127.0.0.1:") && !strings.HasPrefix(*listen, "localhost:") {
			return false, errors.New("--listen must use localhost")
		}
	}

	usesWorker := configuredMode == "worker" || configuredMode == "all"
	if usesWorker || provided["name"] {
		if provided["name"] {
			sources["name"] = "flag"
		} else if value := strings.TrimSpace(cfg.Cluster.Worker.NodeName); value != "" && value != "auto" {
			*name = value
			sources["name"] = "config"
		} else if value := strings.TrimSpace(os.Getenv("CONTEXTBRIDGE_NODE_NAME")); value != "" {
			*name = value
			sources["name"] = "environment"
		} else {
			*name, _ = os.Hostname()
			if strings.TrimSpace(*name) == "" {
				*name = "auto"
			}
			sources["name"] = "default"
		}
		if strings.TrimSpace(*name) != *name || len([]rune(*name)) > 100 || strings.IndexFunc(*name, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			return false, errors.New("--name must be a printable name of at most 100 characters")
		}
	}

	if _, err := fmt.Fprintln(output, "\nResolved configuration"); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(output, "  role       %s  [%s]\n", configuredMode, sources["mode"]); err != nil {
		return false, err
	}
	if configuredMode == "client" || configuredMode == "worker" {
		if _, err := fmt.Fprintf(output, "  relay      %s  [%s]\n", guidedURLLabel(*relayURL), sources["relay"]); err != nil {
			return false, err
		}
	}
	if configuredMode == "relay" || configuredMode == "all" {
		if _, err := fmt.Fprintf(output, "  listen     %s  [%s]\n", *listen, sources["listen"]); err != nil {
			return false, err
		}
		if strings.TrimSpace(*publicURL) == "" {
			if _, err := fmt.Fprintln(output, "  public URL not configured  [local/offline use remains available]"); err != nil {
				return false, err
			}
		} else if _, err := fmt.Fprintf(output, "  public URL %s  [%s]\n", guidedURLLabel(*publicURL), sources["public"]); err != nil {
			return false, err
		}
	}
	if configuredMode == "worker" || configuredMode == "all" {
		if _, err := fmt.Fprintf(output, "  node       %s  [%s]\n", *name, sources["name"]); err != nil {
			return false, err
		}
	}
	confirmed, err := promptGuidedConfirmation(reader, output, "Apply this configuration? [y/N]")
	if err != nil {
		return false, err
	}
	return confirmed, nil
}

func currentClusterMode(cfg config.Config) string {
	switch {
	case cfg.Cluster.Relay.Enabled && cfg.Cluster.Worker.Enabled:
		return "all"
	case cfg.Cluster.Relay.Enabled:
		return "relay"
	case cfg.Cluster.Worker.Enabled:
		return "worker"
	case strings.TrimSpace(cfg.Cluster.Worker.RelayURL) != "":
		return "client"
	default:
		return "local"
	}
}

func guidedModeAllowed(mode string) bool {
	switch mode {
	case "local", "client", "relay", "worker", "all":
		return true
	default:
		return false
	}
}

func promptGuidedChoice(reader *bufio.Reader, output io.Writer, label, fallback string, allowed []string) (string, error) {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	for attempt := 0; attempt < maximumGuidedPromptAttempts; attempt++ {
		value, err := promptGuidedLine(reader, output, fmt.Sprintf("%s (%s) [%s]", label, strings.Join(allowed, "/"), fallback))
		if err != nil {
			return "", err
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			value = fallback
		}
		if value == "sender" {
			value = "client"
		}
		if _, ok := allowedSet[value]; ok {
			return value, nil
		}
		if _, err := fmt.Fprintln(output, "Choose one of:", strings.Join(allowed, ", ")); err != nil {
			return "", err
		}
	}
	return "", errors.New("guided setup stopped after three invalid answers; no configuration was changed")
}

func promptGuidedRequired(reader *bufio.Reader, output io.Writer, label string) (string, error) {
	for attempt := 0; attempt < maximumGuidedPromptAttempts; attempt++ {
		value, err := promptGuidedLine(reader, output, label)
		if err != nil {
			return "", err
		}
		if value = strings.TrimSpace(value); value != "" {
			return value, nil
		}
		if _, err := fmt.Fprintln(output, "A value is required."); err != nil {
			return "", err
		}
	}
	return "", errors.New("guided setup stopped after three empty answers; no configuration was changed")
}

func promptGuidedConfirmation(reader *bufio.Reader, output io.Writer, label string) (bool, error) {
	value, err := promptGuidedLine(reader, output, label)
	if err != nil {
		return false, err
	}
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "y" || value == "yes", nil
}

func promptGuidedLine(reader *bufio.Reader, output io.Writer, label string) (string, error) {
	if _, err := fmt.Fprint(output, label+": "); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		if errors.Is(err, io.EOF) {
			return "", errors.New("guided setup cancelled before confirmation; no configuration was changed")
		}
		return "", fmt.Errorf("read guided setup input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func guidedURLLabel(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "[invalid URL]"
	}
	return strings.ToLower(parsed.Scheme) + "://" + parsed.Host
}
