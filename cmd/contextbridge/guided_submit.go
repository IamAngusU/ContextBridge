package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

// guideClusterSubmission resolves only the one value that cannot be inferred
// safely: the job contract file. It deliberately does not offer to invent a
// token, idempotency key, E2EE reservation, policy boundary, or job content.
func guideClusterSubmission(input io.Reader, output io.Writer, cfg config.Config, file *string, wait, sealed, stream bool, artifactDir, idempotencyKey, credentialSource string, credentialAvailable bool, provided map[string]bool) (bool, error) {
	reader := bufio.NewReader(input)
	if _, err := fmt.Fprintln(output, "ContextBridge guided cluster submission"); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintln(output, "Only unresolved safe values are requested. No request is sent before the final confirmation."); err != nil {
		return false, err
	}
	source := "flag"
	if strings.TrimSpace(*file) == "" {
		value, err := promptGuidedRequired(reader, output, "Job contract JSON file")
		if err != nil {
			return false, err
		}
		*file = value
		source = "prompt"
	} else if !provided["file"] {
		source = "resolved"
	}
	cleanFile, err := guidedSubmissionPath(*file)
	if err != nil {
		return false, err
	}
	*file = cleanFile

	if _, err := fmt.Fprintln(output, "\nResolved submission"); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(output, "  contract   %s  [%s]\n", *file, source); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(output, "  relay      %s  [config]\n", guidedURLLabel(clusterClientBaseURL(cfg))); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(output, "  wait       %s\n", onOffLabel(wait)); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(output, "  E2EE       %s\n", onOffLabel(sealed)); err != nil {
		return false, err
	}
	if stream {
		if sealed {
			if _, err := fmt.Fprintln(output, "  progress   final encrypted result only [E2EE]"); err != nil {
				return false, err
			}
		} else if _, err := fmt.Fprintln(output, "  progress   enabled"); err != nil {
			return false, err
		}
	}
	if value := strings.TrimSpace(artifactDir); value != "" {
		clean, pathErr := guidedSubmissionPath(value)
		if pathErr != nil {
			return false, fmt.Errorf("--artifacts: %w", pathErr)
		}
		if _, err := fmt.Fprintf(output, "  artifacts  %s\n", clean); err != nil {
			return false, err
		}
	}
	if idempotencyKey != "" {
		if _, err := fmt.Fprintln(output, "  retry key  supplied [exact-request deduplication]"); err != nil {
			return false, err
		}
	}
	if !credentialAvailable {
		return false, errors.New("producer credential is not configured; run `contextbridge cluster login` first or pass --token explicitly")
	}
	if _, err := fmt.Fprintf(output, "  credential %s token [secret hidden]\n", credentialSource); err != nil {
		return false, err
	}
	return promptGuidedConfirmation(reader, output, "Submit this job? [y/N]")
}

func guidedSubmissionPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("path is required")
	}
	if len(value) > 4096 || !utf8.ValidString(value) || strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
	}) >= 0 {
		return "", errors.New("path must be valid printable text of at most 4096 bytes")
	}
	return filepath.Clean(value), nil
}
