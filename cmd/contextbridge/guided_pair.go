package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func guidePairing(input io.Reader, output io.Writer, cfg config.Config, relayURL, identityFile, name *string, provided map[string]bool) (bool, error) {
	reader := bufio.NewReader(input)
	sources := map[string]string{}
	if _, err := fmt.Fprintln(output, "ContextBridge guided pairing"); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintln(output, "Only unresolved safe values are requested. No pairing request or identity write happens before confirmation."); err != nil {
		return false, err
	}

	if provided["relay"] {
		sources["relay"] = "flag"
	} else if value := strings.TrimSpace(cfg.Cluster.Worker.RelayURL); value != "" {
		*relayURL = value
		sources["relay"] = "config"
	} else if value := strings.TrimSpace(os.Getenv("CONTEXTBRIDGE_RELAY_URL")); value != "" {
		*relayURL = value
		sources["relay"] = "environment"
	} else {
		value, err := promptGuidedRequired(reader, output, "Relay URL (HTTPS, or localhost)")
		if err != nil {
			return false, err
		}
		*relayURL = value
		sources["relay"] = "prompt"
	}
	*relayURL = strings.TrimRight(strings.TrimSpace(*relayURL), "/")
	if err := cluster.ValidateRelayURL(*relayURL); err != nil {
		return false, fmt.Errorf("--relay: %w", err)
	}

	if provided["identity"] {
		sources["identity"] = "flag"
	} else {
		*identityFile = cfg.Cluster.Worker.IdentityFile
		sources["identity"] = "config"
	}
	if strings.TrimSpace(*identityFile) == "" {
		return false, errors.New("--identity or cluster.worker.identity_file is required")
	}
	if err := validatePairIdentityPath(*identityFile); err != nil {
		return false, err
	}
	*identityFile = filepath.Clean(*identityFile)
	identityExists := false
	if info, err := os.Lstat(*identityFile); err == nil {
		if !info.Mode().IsRegular() {
			return false, errors.New("--identity already exists and is not a regular file")
		}
		identityExists = true
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect --identity: %w", err)
	}

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

	if _, err := fmt.Fprintln(output, "\nResolved pairing"); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(output, "  relay      %s  [%s]\n", guidedURLLabel(*relayURL), sources["relay"]); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(output, "  node       %s  [%s]\n", *name, sources["name"]); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(output, "  identity   %s  [%s]\n", *identityFile, sources["identity"]); err != nil {
		return false, err
	}
	if identityExists {
		if _, err := fmt.Fprintln(output, "  identity   existing regular file; replaced only after relay approval"); err != nil {
			return false, err
		}
	}
	if _, err := fmt.Fprintf(output, "  groups     %d  [config]\n", len(cfg.Cluster.Worker.Groups)); err != nil {
		return false, err
	}
	return promptGuidedConfirmation(reader, output, "Send this pairing request? [y/N]")
}

func validatePairIdentityPath(value string) error {
	if len([]rune(value)) > 4096 || strings.IndexFunc(value, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return errors.New("--identity must be a printable path of at most 4096 characters")
	}
	return nil
}
