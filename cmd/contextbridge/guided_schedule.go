package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

type guidedScheduleSummary struct {
	Name string `json:"name"`
	Job  struct {
		Route     string `json:"route"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Reasoning string `json:"reasoning"`
	} `json:"job"`
	Timing struct {
		Type     string `json:"type"`
		Timezone string `json:"timezone"`
	} `json:"timing"`
	Steps   []json.RawMessage `json:"steps"`
	Enabled *bool             `json:"enabled"`
}

// guideScheduleAdd resolves and snapshots one operator-authored schedule file
// before confirmation. It never prints prompt/step content, and the exact bytes
// reviewed here are the bytes sent after confirmation so a path swap cannot
// change the request between the summary and admission.
func guideScheduleAdd(input io.Reader, output io.Writer, cfg config.Config, file *string, provided map[string]bool) (bool, []byte, error) {
	reader := bufio.NewReader(input)
	if _, err := fmt.Fprintln(output, "ContextBridge guided schedule creation"); err != nil {
		return false, nil, err
	}
	if _, err := fmt.Fprintln(output, "Only the missing schedule file is requested. Its prompt and step content stay hidden."); err != nil {
		return false, nil, err
	}

	source := "flag"
	if strings.TrimSpace(*file) == "" {
		value, err := promptGuidedRequired(reader, output, "Schedule JSON file")
		if err != nil {
			return false, nil, err
		}
		*file = value
		source = "prompt"
	} else if !provided["file"] {
		source = "resolved"
	}
	cleanFile, err := guidedSubmissionPath(*file)
	if err != nil {
		return false, nil, err
	}
	if cleanFile == "-" {
		return false, nil, errors.New("guided schedule creation requires a regular file; use --file - only in non-interactive mode")
	}
	*file = cleanFile
	body, err := readRegularFileBounded(cleanFile, 12<<20)
	if err != nil {
		return false, nil, fmt.Errorf("read schedule input: %w", err)
	}
	summary, err := inspectGuidedSchedule(body)
	if err != nil {
		return false, nil, err
	}

	if _, err := fmt.Fprintln(output, "\nResolved schedule"); err != nil {
		return false, nil, err
	}
	if _, err := fmt.Fprintf(output, "  file        %s  [%s]\n", cleanFile, source); err != nil {
		return false, nil, err
	}
	if _, err := fmt.Fprintf(output, "  service     %s  [config]\n", guidedURLLabel(baseURL(cfg))); err != nil {
		return false, nil, err
	}
	if _, err := fmt.Fprintf(output, "  name        %s\n", summary.Name); err != nil {
		return false, nil, err
	}
	if _, err := fmt.Fprintf(output, "  timing      %s · timezone %s\n", summary.Timing.Type, summary.Timing.Timezone); err != nil {
		return false, nil, err
	}
	if _, err := fmt.Fprintf(output, "  route       %s · provider %s · model %s · reasoning %s\n", summary.Job.Route, summary.Job.Provider, summary.Job.Model, summary.Job.Reasoning); err != nil {
		return false, nil, err
	}
	if _, err := fmt.Fprintf(output, "  work        base job + %d follow-up step(s) · prompt/content hidden\n", len(summary.Steps)); err != nil {
		return false, nil, err
	}
	enabled := summary.Enabled == nil || *summary.Enabled
	if _, err := fmt.Fprintf(output, "  enabled     %s\n", onOffLabel(enabled)); err != nil {
		return false, nil, err
	}
	apply, err := promptGuidedConfirmation(reader, output, "Create this durable schedule? [y/N]")
	if err != nil {
		return false, nil, err
	}
	return apply, body, nil
}

func inspectGuidedSchedule(body []byte) (guidedScheduleSummary, error) {
	var summary guidedScheduleSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		return summary, fmt.Errorf("schedule JSON: %w", err)
	}
	var err error
	if summary.Name, err = guidedScheduleLabel(summary.Name, "", 100); err != nil {
		return summary, fmt.Errorf("schedule name: %w", err)
	}
	if summary.Name == "" {
		return summary, errors.New("schedule name is required")
	}
	if summary.Timing.Type, err = guidedScheduleLabel(strings.ToLower(summary.Timing.Type), "", 16); err != nil {
		return summary, fmt.Errorf("schedule timing: %w", err)
	}
	if summary.Timing.Type == "" {
		return summary, errors.New("schedule timing.type is required")
	}
	if summary.Timing.Timezone, err = guidedScheduleLabel(summary.Timing.Timezone, "Local", 100); err != nil {
		return summary, fmt.Errorf("schedule timezone: %w", err)
	}
	if summary.Job.Route, err = guidedScheduleLabel(summary.Job.Route, "default", 100); err != nil {
		return summary, fmt.Errorf("schedule route: %w", err)
	}
	if summary.Job.Provider, err = guidedScheduleLabel(summary.Job.Provider, "auto", 100); err != nil {
		return summary, fmt.Errorf("schedule provider: %w", err)
	}
	if summary.Job.Model, err = guidedScheduleLabel(summary.Job.Model, "auto", 100); err != nil {
		return summary, fmt.Errorf("schedule model: %w", err)
	}
	if summary.Job.Reasoning, err = guidedScheduleLabel(summary.Job.Reasoning, "auto", 100); err != nil {
		return summary, fmt.Errorf("schedule reasoning: %w", err)
	}
	if len(summary.Steps) > 4 {
		return summary, errors.New("schedule has more than four follow-up steps")
	}
	return summary, nil
}

func guidedScheduleLabel(value, fallback string, maximum int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	if len(value) > maximum || !utf8.ValidString(value) || strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
	}) >= 0 {
		return "", fmt.Errorf("must be printable text of at most %d bytes", maximum)
	}
	return value, nil
}
