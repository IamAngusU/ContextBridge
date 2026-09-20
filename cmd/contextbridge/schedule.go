package main

import (
	"bytes"
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
	if len(args) == 0 {
		return errors.New("schedule needs add, list, show, pause, resume, run, or delete")
	}
	action := args[0]
	flags := flag.NewFlagSet("schedule "+action, flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath(), "config path")
	file := flags.String("file", "", "schedule JSON file; - reads stdin")
	if err := parseInterspersedFlags(flags, args[1:]); err != nil {
		return err
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
		if *file == "" {
			return errors.New("--file is required")
		}
		method = http.MethodPost
		if *file == "-" {
			body, err = readScheduleInput(os.Stdin)
		} else {
			body, err = readRegularFileBounded(*file, 12<<20)
		}
		if err != nil {
			return fmt.Errorf("read schedule input: %w", err)
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
	_, err = os.Stdout.Write(raw)
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
