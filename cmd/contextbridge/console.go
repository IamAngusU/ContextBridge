package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/resourcepacks"
	"github.com/IamAngusU/ContextBridge/internal/systeminfo"
	"github.com/IamAngusU/ContextBridge/internal/terminalui"
)

var errConsoleUnauthorized = errors.New("the local pairing token was rejected; check config.yml")

type consoleStatus struct {
	Version       string                     `json:"version"`
	UptimeSeconds uint64                     `json:"uptime_seconds"`
	Queued        int                        `json:"queued"`
	Completed     int                        `json:"completed"`
	ActiveJobs    int                        `json:"active_jobs"`
	Adapter       bridge.AdapterClientStatus `json:"adapter"`
	Runtime       struct {
		Hardware systeminfo.Snapshot            `json:"hardware"`
		Engines  map[string]bridge.EngineStatus `json:"engines"`
		Packs    []resourcepacks.Pack           `json:"resource_packs"`
	} `json:"runtime"`
	Metrics struct {
		JobsTotal  uint64 `json:"jobs_total"`
		JobsFailed uint64 `json:"jobs_failed"`
	} `json:"metrics"`
	Updates struct {
		Enabled bool `json:"enabled"`
	} `json:"updates"`
	RAG struct {
		Enabled bool `json:"enabled"`
	} `json:"rag"`
}

func consoleCommand(args []string) error {
	flags := flag.NewFlagSet("console", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "scoped producer token for bounded console work actions")
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
	session.EnableCommands()
	actionToken, actionSource := consoleActionCredential(cfg, *token)
	if actionToken != "" && session.EnableWorkActions() {
		actionClient := newClusterAPIClient(clusterBaseURL(cfg), actionToken)
		limitCtx, cancelLimits := context.WithTimeout(ctx, 2*time.Second)
		session.ConfigureCommandLimits(consoleCommandLimits(limitCtx, actionClient, cfg))
		cancelLimits()
		session.Banner(version, "bounded live client · producer credential from "+actionSource+" · host commands are never executed")
		go runConsoleActions(ctx, actionClient, session.CommandIntents(), session)
	} else {
		reason := "configure a scoped producer credential for work actions"
		if actionToken != "" {
			reason = "work actions require an interactive terminal; piped/redirected input stays read-only"
		}
		session.Banner(version, "read-only · "+reason+" · host commands are never executed")
	}
	if cfg.Cluster.Relay.Enabled || cfg.Cluster.Worker.Enabled {
		go watchPoolDisplay(ctx, cfg, session)
	}
	commands, restoreInput := consoleCommandInput(os.Stdin, session.LiveCommandEditor(), session.SetCommandInput)
	defer restoreInput()
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		select {
		case command, ok := <-commands:
			if !ok || session.HandleCommand(command) {
				return nil
			}
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
			session.ObserveService(toServiceSnapshot(status, cfg))
		}
		select {
		case <-ctx.Done():
			return nil
		case command, ok := <-commands:
			if !ok || session.HandleCommand(command) {
				return nil
			}
		case <-time.After(3 * time.Second):
		}
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

func toServiceSnapshot(status consoleStatus, configs ...config.Config) terminalui.ServiceSnapshot {
	snapshot := terminalui.ServiceSnapshot{
		Version: status.Version, UptimeSeconds: status.UptimeSeconds, Queued: status.Queued, Completed: status.Completed, ActiveJobs: status.ActiveJobs,
		AdapterConnected: status.Adapter.Connected, ActiveEndpoints: status.Adapter.ActiveEndpoints, BusyEndpoints: status.Adapter.BusyEndpoints,
		JobsTotal: status.Metrics.JobsTotal, JobsFailed: status.Metrics.JobsFailed,
		ResourcePacks: append([]resourcepacks.Pack(nil), status.Runtime.Packs...),
	}
	features := []terminalui.FeatureState{
		{Label: "UPD", Enabled: status.Updates.Enabled},
		{Label: "RAG", Enabled: status.RAG.Enabled},
	}
	if len(configs) > 0 {
		cfg := configs[0]
		engineAutostart := false
		for _, engine := range cfg.Engines {
			engineAutostart = engineAutostart || engine.AutoStart
		}
		portable := cfg.Portable.Enabled != nil && *cfg.Portable.Enabled
		features = []terminalui.FeatureState{
			{Label: "RLY", Enabled: cfg.Cluster.Relay.Enabled},
			{Label: "WRK", Enabled: cfg.Cluster.Worker.Enabled},
			{Label: "UPD", Enabled: status.Updates.Enabled},
			{Label: "RAG", Enabled: status.RAG.Enabled},
			{Label: "PCK", Enabled: portable},
			{Label: "EAS", Enabled: engineAutostart},
		}
	}
	snapshot.Features = features
	for _, endpoint := range status.Adapter.Endpoints {
		snapshot.Endpoints = append(snapshot.Endpoints, cluster.AdapterSessionCapability{
			EndpointID: endpoint.ID, Profile: endpoint.Profile, State: endpoint.State,
			CurrentModel: endpoint.CurrentModel, CurrentReasoning: endpoint.CurrentReasoning,
		})
	}
	for provider, engine := range status.Runtime.Engines {
		if engine.State != "online" {
			continue
		}
		if engine.Remote {
			snapshot.APIProviders = append(snapshot.APIProviders, provider)
			for _, model := range engine.Models {
				snapshot.APIModels = append(snapshot.APIModels, cluster.ModelCapability{
					Provider: provider, Name: model.Name, Available: model.Available,
				})
			}
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
		snapshot.GPUUtilization = status.Runtime.Hardware.GPUs[0].Utilization
		if len(status.Runtime.Hardware.GPUs) == 1 {
			snapshot.GPU = status.Runtime.Hardware.GPUs[0].Name
		} else {
			snapshot.GPU = fmt.Sprintf("%d GPUs", len(status.Runtime.Hardware.GPUs))
		}
		for _, gpu := range status.Runtime.Hardware.GPUs[1:] {
			snapshot.GPUUtilization = max(snapshot.GPUUtilization, gpu.Utilization)
		}
	} else {
		snapshot.GPU = "Zero-GPU"
	}
	return snapshot
}
