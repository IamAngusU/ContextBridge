package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func scheduleCommand(args []string) error {
	return scheduleCommandWithIO(args, os.Stdin, os.Stdout, interactiveFiles(os.Stdin, os.Stdout))
}

func scheduleCommandWithIO(args []string, stdin io.Reader, output io.Writer, terminal bool) error {
	if len(args) == 0 {
		return errors.New("schedule needs add, list, show, pause, resume, run, or delete")
	}
	action := args[0]
	flags := flag.NewFlagSet("schedule "+action, flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath(), "config path")
	file := flags.String("file", "", "schedule JSON file; - reads stdin")
	interactive := flags.Bool("interactive", false, "guide unresolved schedule-add values in a real terminal")
	if err := parseInterspersedFlags(flags, args[1:]); err != nil {
		return err
	}
	if *interactive && action != "add" {
		return errors.New("--interactive is supported only for schedule add")
	}
	if *interactive && !terminal {
		return errors.New("guided schedule creation requires an interactive terminal; use explicit flags for scripts, pipes, CI, MCP, or services")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	method, path := http.MethodGet, "/v1/schedules"
	var body []byte
	switch action {
	case "add":
		if flags.NArg() != 0 {
			return errors.New("schedule add accepts --file but no positional arguments")
		}
		if *interactive {
			provided := map[string]bool{}
			flags.Visit(func(option *flag.Flag) { provided[option.Name] = true })
			apply, prepared, guideErr := guideScheduleAdd(stdin, output, cfg, file, provided)
			if guideErr != nil {
				return guideErr
			}
			if !apply {
				_, _ = fmt.Fprintln(output, "Cancelled. No schedule was created.")
				return nil
			}
			body = prepared
		}
		if *file == "" {
			return errors.New("--file is required (or use --interactive in a real terminal)")
		}
		method = http.MethodPost
		if !*interactive {
			if *file == "-" {
				body, err = readScheduleInput(stdin)
			} else {
				body, err = readRegularFileBounded(*file, 12<<20)
			}
			if err != nil {
				return fmt.Errorf("read schedule input: %w", err)
			}
		}
	case "list":
		if flags.NArg() != 0 {
			return errors.New("schedule list accepts no positional arguments")
		}
	case "show", "pause", "resume", "run", "delete":
		if flags.NArg() != 1 {
			return errors.New("schedule ID is required")
		}
		id := flags.Arg(0)
		if len(id) > 128 || strings.ContainsAny(id, "/?# ") {
			return errors.New("invalid schedule ID")
		}
		path += "/" + url.PathEscape(id)
		if action == "delete" {
			method = http.MethodDelete
		} else if action != "show" {
			method = http.MethodPost
			path += "/" + action
		}
	default:
		return fmt.Errorf("unknown schedule action %q", action)
	}
	// #nosec G704 -- config.Load validates server.listen as a parsed loopback host:port; path is fixed or a validated schedule ID.
	req, err := http.NewRequest(method, baseURL(cfg)+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// #nosec G704 -- the request target is the validated local ContextBridge service above.
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("schedule returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if *interactive && action == "add" {
		var created struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &created) == nil {
			if id, labelErr := guidedScheduleLabel(created.ID, "", 128); labelErr == nil && id != "" {
				_, err = fmt.Fprintf(output, "Created schedule %s.\n", id)
				return err
			}
		}
		_, err = fmt.Fprintln(output, "Schedule created. Run `contextbridge schedule list` to inspect it.")
		return err
	}
	_, err = output.Write(raw)
	return err
}

func readScheduleInput(reader io.Reader) ([]byte, error) {
	const limit = int64(12 << 20)
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("schedule input exceeds %d bytes", limit)
	}
	return body, nil
}

func resultCommand(args []string) error {
	flags := flag.NewFlagSet("result", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath(), "config path")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("job ID is required")
	}
	id := flags.Arg(0)
	if len(id) > 128 || strings.ContainsAny(id, "/?# ") {
		return errors.New("invalid job ID")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, baseURL(cfg)+"/v1/jobs/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("result returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	_, err = io.Copy(os.Stdout, io.LimitReader(resp.Body, 256<<20))
	return err
}

func failureRate(failed, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(failed) / float64(total)
}

func printFailureBreakdown(label string, totals, failures map[string]uint64) {
	keys := make([]string, 0, len(totals))
	for key := range totals {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("%s %s: %d jobs, %d failed (%.1f%%)\n", label, key, totals[key], failures[key], failureRate(failures[key], totals[key]))
	}
}
