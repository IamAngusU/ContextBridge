package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/systeminfo"
	"github.com/IamAngusU/ContextBridge/internal/terminalui"
)

var errConsoleUnauthorized = errors.New("the local pairing token was rejected; check config.yml")

type consoleStatus struct {
	Version    string                     `json:"version"`
	Queued     int                        `json:"queued"`
	Completed  int                        `json:"completed"`
	ActiveJobs int                        `json:"active_jobs"`
	Browser    bridge.BrowserClientStatus `json:"browser"`
	Runtime    struct {
		Hardware systeminfo.Snapshot            `json:"hardware"`
		Engines  map[string]bridge.EngineStatus `json:"engines"`
	} `json:"runtime"`
	Metrics struct {
		JobsTotal  uint64 `json:"jobs_total"`
		JobsFailed uint64 `json:"jobs_failed"`
	} `json:"metrics"`
}

func consoleCommand(args []string) error {
	flags := flag.NewFlagSet("console", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	session := terminalui.NewWithStyle(os.Stdout, cfg.Terminal.Style)
	defer session.Close()
	session.Banner(version, "read-only · exit + Enter / Ctrl+C closes this view")
	if cfg.Cluster.Relay.Enabled || cfg.Cluster.Worker.Enabled {
		go watchPoolDisplay(ctx, cfg, session)
	}
	exitRequested := consoleExitRequested(os.Stdin)
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		select {
		case <-exitRequested:
			return nil
		default:
		}
		status, err := fetchConsoleStatus(ctx, client, cfg)
		if errors.Is(err, errConsoleUnauthorized) {
			return err
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			session.ObserveServiceUnavailable("local service not reachable; retrying")
		} else {
			session.ObserveService(toServiceSnapshot(status))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-exitRequested:
			return nil
		case <-time.After(3 * time.Second):
		}
	}
}

func consoleExitRequested(input io.Reader) <-chan struct{} {
	exit := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			if isConsoleExitCommand(scanner.Text()) {
				close(exit)
				return
			}
		}
	}()
	return exit
}

func isConsoleExitCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "exit", "quit", "q", ":q":
		return true
	default:
		return false
	}
}

func fetchConsoleStatus(ctx context.Context, client *http.Client, cfg config.Config) (consoleStatus, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL(cfg)+"/v1/status", nil)
	if err != nil {
		return consoleStatus{}, err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	response, err := client.Do(request)
	if err != nil {
		return consoleStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return consoleStatus{}, errConsoleUnauthorized
	}
	if response.StatusCode != http.StatusOK {
		return consoleStatus{}, fmt.Errorf("service returned HTTP %d", response.StatusCode)
	}
	var status consoleStatus
	if err := json.NewDecoder(io.LimitReader(response.Body, 24<<20)).Decode(&status); err != nil {
		return consoleStatus{}, fmt.Errorf("invalid service status: %w", err)
	}
	if status.Version == "" {
		return consoleStatus{}, errors.New("service status has no version")
	}
	return status, nil
}

func toServiceSnapshot(status consoleStatus) terminalui.ServiceSnapshot {
	snapshot := terminalui.ServiceSnapshot{
		Version: status.Version, Queued: status.Queued, Completed: status.Completed, ActiveJobs: status.ActiveJobs,
		BrowserConnected: status.Browser.Connected, ActiveTabs: status.Browser.ActiveTabs, BusyTabs: status.Browser.BusyTabs,
		JobsTotal: status.Metrics.JobsTotal, JobsFailed: status.Metrics.JobsFailed,
	}
	for _, tab := range status.Browser.Tabs {
		snapshot.Tabs = append(snapshot.Tabs, cluster.BrowserSessionCapability{
			TabID: tab.ID, Profile: tab.Profile, State: tab.State,
			CurrentModel: tab.CurrentModel, CurrentReasoning: tab.CurrentReasoning,
		})
	}
	for provider, engine := range status.Runtime.Engines {
		if engine.State != "online" {
			continue
		}
		snapshot.LocalProviders = append(snapshot.LocalProviders, provider)
		for _, model := range engine.Models {
			snapshot.LocalModels = append(snapshot.LocalModels, cluster.ModelCapability{
				Provider: provider, Name: model.Name, Loaded: model.Loaded, Size: model.Size, VRAM: model.VRAM,
			})
		}
	}
	if len(status.Runtime.Hardware.GPUs) > 0 {
		gpu := status.Runtime.Hardware.GPUs[0]
		snapshot.GPU, snapshot.GPUUtilization = gpu.Name, gpu.Utilization
	} else {
		snapshot.GPU = "Zero-GPU"
	}
	return snapshot
}
