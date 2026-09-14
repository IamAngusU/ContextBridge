package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/llamaruntime"
	"github.com/IamAngusU/ContextBridge/internal/modelregistry"
	"github.com/IamAngusU/ContextBridge/internal/systeminfo"
	"github.com/IamAngusU/ContextBridge/internal/terminalui"
	"github.com/IamAngusU/ContextBridge/internal/updater"
)

var version = "dev"

func main() {
	bridge.Version = version
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = initCommand(os.Args[2:])
	case "serve":
		err = serveCommand(os.Args[2:])
	case "run":
		err = runCommand(os.Args[2:])
	case "submit":
		err = submitCommand(os.Args[2:])
	case "schedule":
		err = scheduleCommand(os.Args[2:])
	case "result":
		err = resultCommand(os.Args[2:])
	case "review":
		err = reviewCommand(os.Args[2:])
	case "health":
		err = healthCommand(os.Args[2:])
	case "dashboard":
		err = dashboardCommand(os.Args[2:])
	case "status":
		err = statusCommand(os.Args[2:])
	case "browser":
		err = browserCommand(os.Args[2:])
	case "doctor":
		err = doctorCommand(os.Args[2:])
	case "hardware":
		err = hardwareCommand(os.Args[2:])
	case "models":
		err = modelsCommand(os.Args[2:])
	case "pull":
		err = pullCommand(os.Args[2:])
	case "runtime":
		err = runtimeCommand(os.Args[2:])
	case "relay":
		err = relayCommand(os.Args[2:])
	case "pair":
		err = pairCommand(os.Args[2:])
	case "worker":
		err = workerCommand(os.Args[2:])
	case "cluster":
		err = clusterCommand(os.Args[2:])
	case "update":
		err = updateCommand(os.Args[2:])
	case "version", "--version", "-version":
		fmt.Println(version)
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		if len(os.Args) > 1 && (os.Args[1] == "run" || os.Args[1] == "serve" || os.Args[1] == "relay" || os.Args[1] == "worker") {
			if rollbackErr := updater.RollbackFailedStart(version); rollbackErr != nil {
				fmt.Fprintln(os.Stderr, "ContextBridge update rollback:", rollbackErr)
			}
		}
		fmt.Fprintln(os.Stderr, "ContextBridge:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `ContextBridge routes trusted local jobs to Ollama or an explicitly paired browser tab.

Usage:
  contextbridge init [--config path]
  contextbridge serve [--config path]
  contextbridge run [--config path] [--slots N] [--topmost]
  contextbridge submit --file job.json [--config path]
  contextbridge schedule add --file schedule.json [--config path]
  contextbridge schedule list|show|pause|resume|run|delete [ID] [--config path]
  contextbridge result JOB_ID [--config path]
  contextbridge review --job-dir path [--config path]
  contextbridge health [--config path]
  contextbridge dashboard [--config path] [--no-open]
  contextbridge status [--config path] [--json]
  contextbridge browser inspect [--config path] [--tab ID]
  contextbridge doctor [--config path] [--json]
  contextbridge hardware [--json]
  contextbridge models [--config path] [--json]
  contextbridge pull [--config path] MODEL
  contextbridge runtime install [--config path] llama.cpp
  contextbridge relay [--config path]
  contextbridge pair [--config path] [--relay URL] [--identity path] [--name NAME]
  contextbridge worker [--config path] [--relay URL] [--identity path] [--name NAME] [--slots N] [--providers LIST] [--models LIST] [--tasks LIST] [--topmost]
  contextbridge cluster status|submit|chat|login|token|pairing [options]
  contextbridge update status|check|apply|enable|disable|auto [options]
  contextbridge version`)
}

func initCommand(args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := config.Default(*path); err != nil {
		return err
	}
	fmt.Printf("Created %s\n", *path)
	fmt.Println("The generated token stays local. Open the file only when pairing the browser extension.")
	return nil
}

func serveCommand(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	session := terminalui.New(os.Stdout)
	defer session.Close()
	logger := log.New(session, "", 0)
	server, err := bridge.NewServer(cfg, logger)
	if err != nil {
		return err
	}
	updateManager, err := updater.New(cfg.Updates, cfg.Storage.Directory, version)
	if err != nil {
		return err
	}
	server.SetUpdater(updateManager)
	updateManager.SetConfigPath(*path)
	updateManager.SetHealthURL(localHealthURL(cfg.Server.Listen))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startUpdater(ctx, updateManager, logger, func(context.Context) bool { return server.Idle() })
	logger.Printf("version %s", version)
	logger.Printf("routes: %d, browser profiles: %d", len(cfg.Routes), len(cfg.BrowserProfiles))
	return server.Run(ctx)
}

func runCommand(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	slots := flags.Int("slots", 0, "session-only worker job limit; 1-64")
	topmost := flags.Bool("topmost", false, "keep this Windows console above other windows for this session")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *slots < 0 || *slots > 64 {
		return errors.New("--slots must be between 1 and 64")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *slots > 0 {
		cfg.Cluster.Worker.MaxConcurrent = *slots
	}
	if *topmost {
		restore, err := terminalui.Topmost()
		if err != nil {
			return err
		}
		defer restore()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errorsCh := make(chan error, 3)
	session := terminalui.New(os.Stdout)
	defer session.Close()
	logger := log.New(session, "", 0)
	local, err := bridge.NewServer(cfg, logger)
	if err != nil {
		return err
	}
	updateManager, err := updater.New(cfg.Updates, cfg.Storage.Directory, version)
	if err != nil {
		return err
	}
	local.SetUpdater(updateManager)
	updateManager.SetConfigPath(*path)
	updateManager.SetHealthURL(localHealthURL(cfg.Server.Listen))
	components := 1
	var relay *cluster.Relay
	var worker *cluster.Worker
	if cfg.Cluster.Relay.Enabled {
		relay, err = cluster.NewRelay(relayConfig(cfg), logger)
		if err != nil {
			return err
		}
		defer relay.Close()
		components++
	}
	if cfg.Cluster.Worker.Enabled {
		worker, err = configuredWorker(cfg)
		if err != nil {
			return err
		}
		components++
	}
	startUpdater(ctx, updateManager, logger, func(context.Context) bool {
		if !local.Idle() || (relay != nil && !relay.Idle()) || (worker != nil && !worker.Idle()) {
			return false
		}
		return true
	})
	session.Banner(version, fmt.Sprintf("%d components · local bridge%s%s", components, enabledLabel(cfg.Cluster.Relay.Enabled, " · relay"), enabledLabel(cfg.Cluster.Worker.Enabled, " · worker")))
	go func() { errorsCh <- local.Run(ctx) }()
	if relay != nil {
		go func() { errorsCh <- relay.Run(ctx) }()
	}
	if worker != nil {
		go func() { errorsCh <- worker.RunWithEvents(ctx, session.HandleWorker) }()
	}
	for i := 0; i < components; i++ {
		if err := <-errorsCh; err != nil {
			stop()
			return err
		}
	}
	return nil
}

func startUpdater(ctx context.Context, manager *updater.Manager, logger *log.Logger, idle func(context.Context) bool) {
	if os.Getenv("CONTEXTBRIDGE_UPDATES_EXTERNAL") == "1" {
		logger.Printf("external privileged updater owns this installation")
		return
	}
	manager.SetIdleCheck(idle)
	go func() {
		if err := manager.ConfirmStartup(ctx); err != nil {
			logger.Printf("update startup check: %v", err)
			os.Exit(75)
		}
	}()
	go manager.Run(ctx, func(result updater.Result, err error) {
		if err != nil {
			logger.Printf("automatic update check: %v", err)
			return
		}
		if !result.Applied {
			return
		}
		logger.Printf("updated to %s; restarting the managed service", result.Status.CurrentVersion)
		if err := updater.RestartCurrentProcess(); err != nil {
			logger.Printf("restart handoff: %v", err)
		}
		os.Exit(75)
	})
}

func updateCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: contextbridge update status|check|apply|enable|disable|auto")
	}
	action := args[0]
	flags := flag.NewFlagSet("update "+action, flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	force := flags.Bool("force", false, "allow replacing a development build")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	managedService := flags.String("managed-service", "", "root-managed systemd service to restart and verify after an update")
	relayOnly := flags.Bool("relay-only", false, "only the local relay health endpoint must be idle")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *managedService != "" {
		if runtime.GOOS != "linux" || os.Geteuid() != 0 || !validManagedService(*managedService) {
			return errors.New("--managed-service requires root on Linux and a contextbridge*.service name")
		}
		if action != "auto" && action != "apply" {
			return errors.New("--managed-service is only valid with update auto or apply")
		}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if action == "self-test" {
		return nil
	}
	manager, err := updater.New(cfg.Updates, cfg.Storage.Directory, version)
	if err != nil {
		return err
	}
	manager.SetHealthURL(localHealthURL(cfg.Server.Listen))
	manager.SetConfigPath(*path)
	if *relayOnly || (cfg.Cluster.Relay.Enabled && !cfg.Cluster.Worker.Enabled) {
		manager.SetHealthURL(localHealthURL(cfg.Cluster.Relay.Listen))
	}
	if *relayOnly && !cfg.Cluster.Relay.Enabled {
		return errors.New("--relay-only requires an enabled local relay")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var value interface{}
	switch action {
	case "status":
		value = manager.LocalStatus()
	case "check":
		status, checkErr := manager.Check(ctx)
		value, err = status, checkErr
	case "apply":
		result, applyErr := manager.Apply(ctx, *force)
		if applyErr == nil && result.Applied && *managedService != "" {
			applyErr = finishManagedUpdate(ctx, manager, *managedService, result.Status.CurrentVersion)
			result.RestartRequired = applyErr != nil
		}
		value, err = result, applyErr
	case "auto":
		manager.SetIdleCheck(func(ctx context.Context) bool { return installedServiceIdle(ctx, cfg, *relayOnly) })
		result, autoErr := manager.Auto(ctx)
		if autoErr == nil && result.Applied && *managedService != "" {
			autoErr = finishManagedUpdate(ctx, manager, *managedService, result.Status.CurrentVersion)
			result.RestartRequired = autoErr != nil
		}
		value, err = result, autoErr
	case "enable", "disable":
		status, setErr := manager.SetEnabled(action == "enable")
		value, err = status, setErr
	default:
		return fmt.Errorf("unknown update command %s", action)
	}
	if *jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(value)
	} else {
		printUpdateResult(value)
	}
	return err
}

// The scheduled updater is a separate process: it must consult the running
// service instead of assuming that a quiet updater means a quiet worker.
func installedServiceIdle(ctx context.Context, cfg config.Config, relayOnly bool) bool {
	check := func(address string) bool {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return false
		}
		if host != "localhost" && host != "" && host != "0.0.0.0" && host != "::" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
			return false
		}
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(probe, http.MethodGet, localHealthURL(address), nil)
		if err != nil {
			return false
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return false
		}
		var health struct {
			Idle bool `json:"idle"`
		}
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&health) == nil && health.Idle
	}
	if relayOnly {
		return cfg.Cluster.Relay.Enabled && check(cfg.Cluster.Relay.Listen)
	}
	if cfg.Cluster.Relay.Enabled && !check(cfg.Cluster.Relay.Listen) {
		return false
	}
	return check(cfg.Server.Listen)
}

func validManagedService(value string) bool {
	if !strings.HasPrefix(value, "contextbridge") || !strings.HasSuffix(value, ".service") || len(value) > 100 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == '@' {
			continue
		}
		return false
	}
	return true
}

func finishManagedUpdate(ctx context.Context, manager *updater.Manager, service, expected string) error {
	restart := func() error {
		commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		output, err := exec.CommandContext(commandCtx, "systemctl", "restart", service).CombinedOutput()
		if err != nil {
			return fmt.Errorf("restart %s: %s: %w", service, strings.TrimSpace(string(output)), err)
		}
		return nil
	}
	if err := restart(); err != nil {
		rollbackErr := updater.RollbackFailedStart(expected)
		restartErr := restart()
		return fmt.Errorf("updated service could not restart (%v); rollback: %v; previous restart: %v", err, rollbackErr, restartErr)
	}
	if err := manager.ConfirmInstalled(ctx, expected); err != nil {
		restartErr := restart()
		return fmt.Errorf("updated service failed health check (%v); previous restart: %v", err, restartErr)
	}
	return nil
}

func localHealthURL(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health"
}

func printUpdateResult(value interface{}) {
	raw, _ := json.Marshal(value)
	var result struct {
		Status  updater.Status `json:"status"`
		Applied bool           `json:"applied"`
	}
	if json.Unmarshal(raw, &result) == nil && result.Status.Repository != "" {
		fmt.Printf("Current: %s\n", result.Status.CurrentVersion)
		fmt.Printf("Available: %s\n", emptyLabel(result.Status.AvailableVersion, "not checked"))
		fmt.Printf("Automatic updates: %s\n", onOffLabel(result.Status.Enabled))
		if result.Applied {
			fmt.Println("The verified update was installed. The managed service will restart with the new version.")
		}
		return
	}
	var status updater.Status
	if json.Unmarshal(raw, &status) == nil && status.Repository != "" {
		fmt.Printf("Current: %s\n", status.CurrentVersion)
		fmt.Printf("Available: %s\n", emptyLabel(status.AvailableVersion, "not checked"))
		fmt.Printf("Automatic updates: %s\n", onOffLabel(status.Enabled))
	}
}

func emptyLabel(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func onOffLabel(value bool) string {
	if value {
		return "enabled"
	}
	return "disabled"
}

func submitCommand(args []string) error {
	flags := flag.NewFlagSet("submit", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	jobPath := flags.String("file", "", "job JSON file")
	artifactDir := flags.String("artifacts", "", "save returned images and files in this directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *jobPath == "" {
		return errors.New("--file is required")
	}
	var raw []byte
	var err error
	if *jobPath == "-" {
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, 12<<20))
	} else {
		raw, err = os.ReadFile(*jobPath)
	}
	if err != nil {
		return err
	}
	var job bridge.Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return err
	}
	submission, err := submit(*path, job)
	if err != nil {
		return err
	}
	if submission.Decision != nil {
		return json.NewEncoder(os.Stdout).Encode(submission.Decision)
	}
	if submission.Output != nil {
		paths, references, err := saveOutputArtifacts(submission.Output, *artifactDir)
		if err != nil {
			return err
		}
		reportSavedArtifacts(paths, references)
		return json.NewEncoder(os.Stdout).Encode(submission.Output)
	}
	return errors.New("bridge returned no output")
}

func reviewCommand(args []string) error {
	flags := flag.NewFlagSet("review", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	jobDir := flags.String("job-dir", "", "InkWall job directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *jobDir == "" && flags.NArg() > 0 {
		*jobDir = flags.Arg(0)
	}
	if *jobDir == "" {
		return errors.New("--job-dir is required")
	}
	job, err := readInkWallJob(*jobDir)
	if err != nil {
		return err
	}
	submission, err := submit(*path, job)
	if err != nil {
		fallback := bridge.ReviewDecision("contextbridge", "unavailable", "bridge_unavailable", 0)
		json.NewEncoder(os.Stdout).Encode(fallback)
		return nil
	}
	if submission.Decision == nil {
		fallback := bridge.ReviewDecision("contextbridge", "invalid", "decision_missing", 0)
		return json.NewEncoder(os.Stdout).Encode(fallback)
	}
	return json.NewEncoder(os.Stdout).Encode(submission.Decision)
}

func healthCommand(args []string) error {
	flags := flag.NewFlagSet("health", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL(cfg) + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned %s", resp.Status)
	}
	return nil
}

func dashboardCommand(args []string) error {
	flags := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	noOpen := flags.Bool("no-open", false, "print the dashboard address without opening a browser")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	base := baseURL(cfg) + "/"
	if *noOpen {
		fmt.Println(base)
		fmt.Println("Open the dashboard and enter the pairing token from your config.")
		return nil
	}
	dashboardURL := base + "#token=" + url.QueryEscape(cfg.Server.Token)
	if err := openBrowser(dashboardURL); err != nil {
		fmt.Println(base)
		return fmt.Errorf("could not open the default browser: %w", err)
	}
	fmt.Println("Opened the local ContextBridge dashboard.")
	return nil
}

func statusCommand(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodGet, baseURL(cfg)+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("service is not reachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 24<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if *asJSON {
		_, err = os.Stdout.Write(raw)
		return err
	}
	var status struct {
		Version   string                     `json:"version"`
		Listen    string                     `json:"listen"`
		Queued    int                        `json:"queued"`
		Completed int                        `json:"completed"`
		Tunnel    bridge.TunnelStatus        `json:"tunnel"`
		Browser   bridge.BrowserClientStatus `json:"browser"`
		Runtime   bridge.RuntimeStatus       `json:"runtime"`
		Metrics   bridge.Metrics             `json:"metrics"`
		Schedules []struct {
			Enabled       bool   `json:"enabled"`
			CurrentRunID  string `json:"current_run_id"`
			WaitingReason string `json:"waiting_reason"`
		} `json:"schedules"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return err
	}
	fmt.Printf("ContextBridge %s\n", status.Version)
	fmt.Printf("Service: online at http://%s\n", status.Listen)
	if status.Tunnel.Connected {
		fmt.Printf("Tunnel: connected to %s\n", status.Tunnel.Target)
	} else {
		fmt.Printf("Tunnel: %s\n", status.Tunnel.State)
	}
	fmt.Printf("Queue: %d waiting, %d completed this session\n", status.Queued, status.Completed)
	if len(status.Schedules) > 0 {
		active, running, waiting := 0, 0, 0
		for _, item := range status.Schedules {
			if item.Enabled {
				active++
			}
			if item.CurrentRunID != "" {
				running++
			}
			if item.WaitingReason != "" {
				waiting++
			}
		}
		fmt.Printf("Schedules: %d total, %d enabled, %d running, %d waiting for resources\n", len(status.Schedules), active, running, waiting)
	}
	if status.Browser.Connected {
		fmt.Printf("Browser: %d tab(s), %d busy, extension %s\n", max(1, status.Browser.ActiveTabs), status.Browser.BusyTabs, status.Browser.ExtensionVersion)
		for _, tab := range status.Browser.Tabs {
			choice := tab.CurrentModel
			if tab.CurrentReasoning != "" {
				choice += " · " + tab.CurrentReasoning
			}
			fmt.Printf("  [%s] %s", tab.State, tab.Title)
			if choice != "" {
				fmt.Printf("  [%s]", choice)
			}
			fmt.Println()
		}
	} else {
		fmt.Println("Browser: not connected")
	}
	fmt.Printf("Jobs: %d total, %d failed (%.1f%%)\n", status.Metrics.JobsTotal, status.Metrics.JobsFailed, failureRate(status.Metrics.JobsFailed, status.Metrics.JobsTotal))
	providers := make([]string, 0, len(status.Metrics.ByProvider))
	for provider := range status.Metrics.ByProvider {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		count := status.Metrics.ByProvider[provider]
		detail := ""
		if samples := status.Metrics.ProviderSamples[provider]; samples > 0 {
			detail = fmt.Sprintf(", %d ms average", status.Metrics.ProviderLatency[provider]/samples)
		}
		if failed := status.Metrics.ProviderFailures[provider]; failed > 0 {
			detail += fmt.Sprintf(", %d failed", failed)
		}
		fmt.Printf("Provider %s: %d jobs%s\n", provider, count, detail)
	}
	printFailureBreakdown("Requested provider", status.Metrics.ByAttemptedProvider, status.Metrics.AttemptedProviderFailures)
	printFailureBreakdown("Requested model", status.Metrics.ByAttemptedModel, status.Metrics.ModelFailures)
	printFailureBreakdown("Requested reasoning", status.Metrics.ByReasoning, status.Metrics.ReasoningFailures)
	printFailureBreakdown("Requested selection", status.Metrics.BySelection, status.Metrics.SelectionFailures)
	for name, engine := range status.Runtime.Engines {
		detail := engine.Affinity
		if detail == "" {
			detail = engine.Model
		}
		fmt.Printf("Engine %s: %s", name, engine.State)
		if detail != "" {
			fmt.Printf(" (%s)", detail)
		}
		fmt.Println()
		if engine.Warning != "" {
			fmt.Printf("  Warning: %s\n", engine.Warning)
		}
		if len(engine.Models) > 0 {
			loaded := 0
			for _, model := range engine.Models {
				if model.Loaded {
					loaded++
					memory := fmt.Sprintf("%s model", formatBytes(uint64(model.Size)))
					if model.VRAM > 0 {
						memory = fmt.Sprintf("%s VRAM", formatBytes(uint64(model.VRAM)))
					}
					fmt.Printf("  Loaded: %s on %s, %s\n", model.Name, model.Affinity, memory)
				}
			}
			fmt.Printf("  Model cache: %d available, %d loaded\n", len(engine.Models), loaded)
		}
	}
	for _, gpu := range status.Runtime.Hardware.GPUs {
		fmt.Printf("GPU: %s, %s, %s free of %s, %d%%, %d°C\n", gpu.Name, gpu.Backend, formatBytes(gpu.MemoryFree), formatBytes(gpu.MemoryTotal), gpu.Utilization, gpu.Temperature)
	}
	return nil
}

func hardwareCommand(args []string) error {
	flags := flag.NewFlagSet("hardware", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	snapshot := systeminfo.Detect(ctx)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(snapshot)
	}
	fmt.Printf("%s/%s", snapshot.OS, snapshot.Architecture)
	if snapshot.OSVersion != "" {
		fmt.Printf(" · %s", snapshot.OSVersion)
	}
	if snapshot.UptimeSeconds > 0 {
		fmt.Printf(" · uptime %s", formatUptime(snapshot.UptimeSeconds))
	}
	fmt.Println()
	fmt.Printf("CPU: %s · %d cores", snapshot.CPU, snapshot.CPUCores)
	if snapshot.CPUFrequencyMHz > 0 {
		fmt.Printf(" · %.2f GHz", float64(snapshot.CPUFrequencyMHz)/1000)
	}
	fmt.Printf(" · %d%% load", snapshot.CPUUtilization)
	fmt.Println()
	fmt.Printf("Memory: %s available of %s", formatBytes(snapshot.MemoryAvailable), formatBytes(snapshot.MemoryTotal))
	if snapshot.MemoryType != "" {
		fmt.Printf(" (%s)", snapshot.MemoryType)
	}
	fmt.Println()
	if len(snapshot.GPUs) == 0 {
		fmt.Println("GPU: no supported telemetry tool detected")
	}
	for _, gpu := range snapshot.GPUs {
		fmt.Printf("GPU: %s, %s, %s free of %s, %d%%, %d°C\n", gpu.Name, gpu.Backend, formatBytes(gpu.MemoryFree), formatBytes(gpu.MemoryTotal), gpu.Utilization, gpu.Temperature)
	}
	for _, backend := range snapshot.Backends {
		state := "not detected"
		if backend.Available {
			state = "available"
		}
		fmt.Printf("Backend %s: %s\n", backend.Name, state)
	}
	return nil
}

func modelsCommand(args []string) error {
	flags := flag.NewFlagSet("models", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	discover := flags.Bool("discover", true, "discover Ollama and model files automatically")
	var scanPaths stringListFlag
	flags.Var(&scanPaths, "path", "model file or directory to scan; repeat for multiple paths")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *discover {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		models, err := modelregistry.Discover(ctx, cfg, scanPaths)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(models)
		}
		if len(models) == 0 {
			fmt.Println("No ready models found. Start Ollama or add --path to a GGUF, ONNX, or SafeTensors directory.")
			return nil
		}
		for _, model := range models {
			state := "installed"
			if model.Ready {
				state = "ready"
			}
			if model.Loaded {
				state = "loaded"
			}
			detail := strings.Join(model.Capabilities, "+")
			if model.Parameters != "" {
				detail += " · " + model.Parameters
			}
			if model.Quantization != "" {
				detail += " · " + model.Quantization
			}
			location := model.Path
			if location == "" {
				location = model.Provider
			}
			fmt.Printf("%-28s  [%-6s]  [%-16s]  [%s RAM est.]  %s\n", model.Name, state, detail, formatBytes(uint64(model.MemoryEstimate)), location)
		}
		return nil
	}
	models := modelregistry.List(cfg)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(models)
	}
	if len(models) == 0 {
		fmt.Println("No models are declared in the config.")
		return nil
	}
	for _, model := range models {
		state := "not installed"
		if model.Installed {
			state = formatBytes(uint64(model.Size))
		}
		fmt.Printf("%-24s %-14s %s\n", model.Name, state, model.Repository+"/"+model.File)
	}
	return nil
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, string(os.PathListSeparator))
}
func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("model path cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

func pullCommand(args []string) error {
	flags := flag.NewFlagSet("pull", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("one configured model alias is required")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	lastLine := ""
	entries, err := modelregistry.Pull(ctx, cfg, flags.Arg(0), func(message string, received, total int64) {
		line := message
		if total > 0 {
			line += fmt.Sprintf(" %d%%", received*100/total)
		}
		if line != lastLine {
			fmt.Println(line)
			lastLine = line
		}
	})
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fmt.Printf("Ready: %s (%s)\n", entry.Path, formatBytes(uint64(entry.Size)))
	}
	return nil
}

func runtimeCommand(args []string) error {
	if len(args) == 0 || args[0] != "install" {
		return errors.New("usage: contextbridge runtime install [--config path] llama.cpp")
	}
	flags := flag.NewFlagSet("runtime install", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 || flags.Arg(0) != "llama.cpp" {
		return errors.New("only llama.cpp is supported by this installer")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	lastLine := ""
	executable, err := llamaruntime.Install(ctx, filepath.Join(cfg.Storage.Directory, "runtime", "llama.cpp"), func(message string, received, total int64) {
		line := message
		if total > 0 {
			line += fmt.Sprintf(" %d%%", received*100/total)
		}
		if line != lastLine {
			fmt.Println(line)
			lastLine = line
		}
	})
	if err != nil {
		return err
	}
	fmt.Println("Runtime ready:", executable)
	return nil
}

func relayCommand(args []string) error {
	flags := flag.NewFlagSet("relay", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !cfg.Cluster.Relay.Enabled {
		return errors.New("cluster.relay.enabled is false in the config")
	}
	logger := log.New(os.Stdout, "ContextBridge relay  ", log.LstdFlags)
	relay, err := cluster.NewRelay(relayConfig(cfg), logger)
	if err != nil {
		return err
	}
	defer relay.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	updateManager, err := updater.New(cfg.Updates, cfg.Storage.Directory, version)
	if err != nil {
		return err
	}
	updateManager.SetHealthURL(localHealthURL(cfg.Cluster.Relay.Listen))
	updateManager.SetConfigPath(*path)
	startUpdater(ctx, updateManager, logger, func(context.Context) bool { return relay.Idle() })
	return relay.Run(ctx)
}

func pairCommand(args []string) error {
	flags := flag.NewFlagSet("pair", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	relayURL := flags.String("relay", "", "public relay URL")
	identityFile := flags.String("identity", "", "identity file for this relay")
	name := flags.String("name", "", "node name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *relayURL == "" {
		*relayURL = cfg.Cluster.Worker.RelayURL
	}
	if *identityFile == "" {
		*identityFile = cfg.Cluster.Worker.IdentityFile
	}
	if *relayURL == "" {
		return errors.New("--relay or cluster.worker.relay_url is required")
	}
	if !strings.HasPrefix(*relayURL, "https://") && !strings.HasPrefix(*relayURL, "http://127.0.0.1:") && !strings.HasPrefix(*relayURL, "http://localhost:") {
		return errors.New("pairing requires HTTPS or a localhost relay URL")
	}
	if *name == "" || *name == "auto" {
		*name, _ = os.Hostname()
	}
	if cfg.Cluster.Relay.Enabled && *relayURL == "http://"+cfg.Cluster.Relay.Listen {
		if err := cluster.BootstrapWorkerIdentity(cfg.Cluster.Relay.Database, *relayURL, *name, *identityFile, cfg.Cluster.Worker.Groups); err != nil {
			return err
		}
		fmt.Println("Local worker paired directly with the relay database.")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cluster.PairWorker(ctx, *relayURL, *name, *identityFile, cfg.Cluster.Worker.Groups, func(pair cluster.PairResponse) {
		fmt.Println("Pair this worker")
		fmt.Println("  Code:", pair.UserCode)
		fmt.Println("  Open:", pair.VerificationURI)
		fmt.Println("Waiting for approval. The code expires at", pair.ExpiresAt.Local().Format(time.RFC1123))
	})
}

func workerCommand(args []string) error {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	relayURL := flags.String("relay", "", "relay URL for this worker session")
	workerName := flags.String("name", "", "session-only display name for this worker")
	identityFile := flags.String("identity", "", "identity file paired to this relay")
	slots := flags.Int("slots", 0, "session-only worker job limit; 1-64")
	providers := flags.String("providers", "", "comma-separated providers this relay may use")
	models := flags.String("models", "", "comma-separated models this relay may use")
	tasks := flags.String("tasks", "", "comma-separated tasks this relay may use")
	groups := flags.String("groups", "", "comma-separated scheduling groups")
	noUpdates := flags.Bool("no-updates", false, "do not run a second updater in this worker process")
	topmost := flags.Bool("topmost", false, "keep this Windows console above other windows for this session")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *slots < 0 || *slots > 64 {
		return errors.New("--slots must be between 1 and 64")
	}
	if *workerName != "" {
		if strings.TrimSpace(*workerName) != *workerName || len([]rune(*workerName)) > 100 || strings.IndexFunc(*workerName, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			return errors.New("--name must be a printable name of at most 100 characters")
		}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *slots > 0 {
		cfg.Cluster.Worker.MaxConcurrent = *slots
	}
	if *workerName != "" {
		cfg.Cluster.Worker.NodeName = *workerName
	}
	if *relayURL != "" {
		cfg.Cluster.Worker.RelayURL = strings.TrimRight(*relayURL, "/")
	}
	if *identityFile != "" {
		cfg.Cluster.Worker.IdentityFile = *identityFile
	}
	if *providers != "" {
		cfg.Cluster.Worker.AllowedProviders = splitWorkerList(*providers)
		if len(cfg.Cluster.Worker.AllowedProviders) == 0 {
			return errors.New("--providers must contain at least one provider")
		}
	}
	if *models != "" {
		cfg.Cluster.Worker.AllowedModels = splitWorkerList(*models)
		if len(cfg.Cluster.Worker.AllowedModels) == 0 {
			return errors.New("--models must contain at least one model")
		}
	}
	if *tasks != "" {
		cfg.Cluster.Worker.AllowedTasks = splitWorkerList(*tasks)
		if len(cfg.Cluster.Worker.AllowedTasks) == 0 {
			return errors.New("--tasks must contain at least one task")
		}
	}
	if *groups != "" {
		cfg.Cluster.Worker.Groups = splitWorkerList(*groups)
		if len(cfg.Cluster.Worker.Groups) == 0 {
			return errors.New("--groups must contain at least one group")
		}
	}
	if *topmost {
		restore, err := terminalui.Topmost()
		if err != nil {
			return err
		}
		defer restore()
	}
	if !cfg.Cluster.Worker.Enabled {
		return errors.New("cluster.worker.enabled is false in the config")
	}
	name := cfg.Cluster.Worker.NodeName
	if name == "" || name == "auto" {
		name, _ = os.Hostname()
	}
	cfg.Cluster.Worker.NodeName = name
	worker, err := configuredWorker(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	session := terminalui.New(os.Stdout)
	defer session.Close()
	logger := log.New(session, "", 0)
	if !*noUpdates {
		updateManager, updateErr := updater.New(cfg.Updates, cfg.Storage.Directory, version)
		if updateErr != nil {
			return updateErr
		}
		updateManager.SetConfigPath(*path)
		startUpdater(ctx, updateManager, logger, func(context.Context) bool { return worker.Idle() })
	}
	session.Banner(version, "worker · "+name)
	return worker.RunWithEvents(ctx, session.HandleWorker)
}

func splitWorkerList(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func relayConfig(cfg config.Config) cluster.RelayConfig {
	return cluster.RelayConfig{Version: version, Listen: cfg.Cluster.Relay.Listen, PublicURL: cfg.Cluster.Relay.PublicURL, Database: cfg.Cluster.Relay.Database, AdminToken: cfg.Cluster.Relay.AdminToken, AllowedOrigins: cfg.Cluster.Relay.AllowedOrigins, MaxJobBytes: cfg.Cluster.Relay.MaxJobBytes, MaxQueuedJobs: cfg.Cluster.Relay.MaxQueue, PairingTTL: time.Duration(cfg.Cluster.Relay.PairingTTLSeconds) * time.Second, Pricing: cfg.Cluster.Pricing, AllowedTasks: cfg.Cluster.Policies.AllowedTasks, MaxAttempts: cfg.Cluster.Policies.MaxAttempts, Pipelines: cfg.Cluster.Pipelines, MaxPipelineRuntime: time.Duration(cfg.Cluster.Policies.MaxRuntime) * time.Second, JobTimeout: time.Duration(cfg.Cluster.Policies.MaxJobRuntime) * time.Second}
}

func configuredWorker(cfg config.Config) (*cluster.Worker, error) {
	name := cfg.Cluster.Worker.NodeName
	if name == "" || name == "auto" {
		name, _ = os.Hostname()
	}
	allowedTasks := cfg.Cluster.Policies.AllowedTasks
	if len(cfg.Cluster.Worker.AllowedTasks) > 0 {
		allowedTasks = cfg.Cluster.Worker.AllowedTasks
	}
	return cluster.LoadWorker(cluster.WorkerConfig{RelayURL: cfg.Cluster.Worker.RelayURL, IdentityFile: cfg.Cluster.Worker.IdentityFile, Name: name, Groups: cfg.Cluster.Worker.Groups, Tags: cfg.Cluster.Worker.Tags, MaxConcurrent: cfg.Cluster.Worker.MaxConcurrent, LocalURL: cfg.Cluster.Worker.LocalURL, LocalToken: cfg.Cluster.Worker.LocalToken, HeartbeatEvery: time.Duration(cfg.Cluster.Worker.HeartbeatSeconds) * time.Second, AllowedTasks: allowedTasks, AllowedProviders: cfg.Cluster.Worker.AllowedProviders, AllowedModels: cfg.Cluster.Worker.AllowedModels, Version: version})
}

func enabledLabel(enabled bool, label string) string {
	if enabled {
		return label
	}
	return ""
}

func freeLocalAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	return listener.Addr().String(), nil
}

func clusterCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: contextbridge cluster status|submit|chat|login|token|pairing")
	}
	switch args[0] {
	case "status":
		return clusterStatusCommand(args[1:])
	case "submit":
		return clusterSubmitCommand(args[1:])
	case "chat":
		return clusterChatCommand(args[1:])
	case "login":
		return clusterLoginCommand(args[1:])
	case "token":
		return clusterTokenCommand(args[1:])
	case "pairing":
		return clusterPairingCommand(args[1:])
	case "configure":
		return clusterConfigureCommand(args[1:])
	case "dashboard":
		return clusterDashboardCommand(args[1:])
	case "pipeline":
		return clusterPipelineCommand(args[1:])
	default:
		return fmt.Errorf("unknown cluster command %s", args[0])
	}
}

func clusterConfigureCommand(args []string) error {
	flags := flag.NewFlagSet("cluster configure", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	mode := flags.String("mode", "local", "local, relay, worker, or all")
	relayURL := flags.String("relay-url", "", "public relay URL for this worker")
	publicURL := flags.String("public-url", "", "public HTTPS URL of this relay")
	name := flags.String("name", "auto", "worker node name")
	listen := flags.String("listen", "", "relay listen address or auto")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	switch *mode {
	case "local":
		cfg.Cluster.Relay.Enabled, cfg.Cluster.Worker.Enabled = false, false
	case "relay":
		cfg.Cluster.Relay.Enabled, cfg.Cluster.Worker.Enabled = true, false
	case "worker":
		cfg.Cluster.Relay.Enabled, cfg.Cluster.Worker.Enabled = false, true
	case "all":
		cfg.Cluster.Relay.Enabled, cfg.Cluster.Worker.Enabled = true, true
	default:
		return errors.New("--mode must be local, relay, worker, or all")
	}
	if *publicURL != "" {
		cfg.Cluster.Relay.PublicURL = strings.TrimRight(*publicURL, "/")
	}
	if *listen == "auto" {
		address, err := freeLocalAddress()
		if err != nil {
			return err
		}
		cfg.Cluster.Relay.Listen = address
	} else if *listen != "" {
		if !strings.HasPrefix(*listen, "127.0.0.1:") && !strings.HasPrefix(*listen, "localhost:") {
			return errors.New("--listen must use localhost")
		}
		cfg.Cluster.Relay.Listen = *listen
	}
	if *relayURL != "" {
		cfg.Cluster.Worker.RelayURL = strings.TrimRight(*relayURL, "/")
	}
	if *mode == "all" && cfg.Cluster.Worker.RelayURL == "" {
		cfg.Cluster.Worker.RelayURL = "http://" + cfg.Cluster.Relay.Listen
	}
	if cfg.Cluster.Worker.Enabled && cfg.Cluster.Worker.RelayURL == "" {
		return errors.New("--relay-url is required for worker mode")
	}
	cfg.Cluster.Worker.NodeName = *name
	if err := config.Save(*path, cfg); err != nil {
		return err
	}
	fmt.Printf("Cluster mode saved: %s\n", *mode)
	if cfg.Cluster.Worker.Enabled {
		fmt.Println("Next: contextbridge pair --config", *path)
	}
	return nil
}

func clusterDashboardCommand(args []string) error {
	flags := flag.NewFlagSet("cluster dashboard", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	noOpen := flags.Bool("no-open", false, "print URL only")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	target := clusterBaseURL(cfg) + "/dashboard/#token=" + url.QueryEscape(cfg.Cluster.Relay.AdminToken)
	if *noOpen {
		fmt.Println(target)
		return nil
	}
	return openBrowser(target)
}

func clusterPipelineCommand(args []string) error {
	flags := flag.NewFlagSet("cluster pipeline", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	name := flags.String("name", "", "pipeline name")
	file := flags.String("file", "", "pipeline input JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *name == "" || *file == "" {
		return errors.New("--name and --file are required")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var run cluster.PipelineRun
	if err := clusterPOST(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/pipelines/"+url.PathEscape(*name)+"/run", cfg.Cluster.Relay.AdminToken, json.RawMessage(raw), &run); err != nil {
		return err
	}
	fmt.Println("Pipeline run:", run.ID)
	for {
		time.Sleep(500 * time.Millisecond)
		if err := clusterGET(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/pipeline-runs/"+url.PathEscape(run.ID), cfg.Cluster.Relay.AdminToken, &run); err != nil {
			return err
		}
		if run.Status == "completed" {
			return json.NewEncoder(os.Stdout).Encode(run.Output)
		}
		if run.Status == "failed" {
			return errors.New(run.Error)
		}
	}
}

func clusterStatusCommand(args []string) error {
	flags := flag.NewFlagSet("cluster status", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	asJSON := flags.Bool("json", false, "print machine-readable pool status")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	token := clusterClientToken(cfg, "")
	var overview cluster.Overview
	if err := clusterGET(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/overview", token, &overview); err != nil {
		return err
	}
	var nodes []cluster.Node
	if err := clusterGET(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/nodes", token, &nodes); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"overview": overview, "nodes": nodes})
	}
	totalSlots, running := 0, 0
	for _, node := range nodes {
		if node.Connected {
			totalSlots += node.Capabilities.MaxConcurrent
			running += node.Capabilities.Running
		}
	}
	fmt.Printf("Pool  [%d/%d PCs online]  [%d/%d slots busy]  [%d queued]\n", overview.NodesOnline, overview.NodesTotal, running, totalSlots, overview.JobsByState[cluster.JobQueued])
	for _, node := range nodes {
		state := "offline"
		if node.Connected {
			state = "online"
		}
		memory := fmt.Sprintf("%s/%s RAM free", formatBytes(node.Capabilities.MemoryFree), formatBytes(node.Capabilities.MemoryTotal))
		if node.Capabilities.MemoryType != "" {
			memory += " · " + node.Capabilities.MemoryType
		}
		cpu := fmt.Sprintf("%d CPU cores", node.Capabilities.CPUCores)
		if node.Capabilities.CPUFrequency > 0 {
			cpu += fmt.Sprintf(" · %.2f GHz", float64(node.Capabilities.CPUFrequency)/1000)
		}
		cpu += fmt.Sprintf(" · %d%% load", node.Capabilities.CPUUtilization)
		fmt.Printf("  %s  [%s]  [%d/%d jobs]  [%s]  [%s]\n", node.Name, state, node.Capabilities.Running, max(1, node.Capabilities.MaxConcurrent), cpu, memory)
		system := strings.TrimSpace(node.Capabilities.OSVersion)
		if system == "" {
			system = node.Capabilities.OS + "/" + node.Capabilities.Architecture
		}
		if node.Capabilities.AgentVersion != "" {
			system += " · agent " + node.Capabilities.AgentVersion
		}
		if node.Capabilities.UptimeSeconds > 0 {
			system += " · uptime " + formatUptime(node.Capabilities.UptimeSeconds)
		}
		fmt.Printf("      System  [%s]\n", system)
		for _, gpu := range node.Capabilities.GPUs {
			fmt.Printf("      GPU  [%s · %s/%s free · %d%% · %d°C]\n", gpu.Name, formatBytes(gpu.MemoryFree), formatBytes(gpu.MemoryTotal), gpu.Utilization, gpu.Temperature)
		}
		if len(node.Capabilities.Models) > 0 {
			names := make([]string, 0, min(6, len(node.Capabilities.Models)))
			for _, model := range node.Capabilities.Models {
				names = append(names, model.Name)
				if len(names) == 6 {
					break
				}
			}
			fmt.Printf("      Models  [%s]\n", strings.Join(names, " · "))
		}
	}
	fmt.Printf("Jobs  [%d completed]  [%d failed]  [%.2f compute hours]\n", overview.JobsByState[cluster.JobCompleted], overview.JobsByState[cluster.JobFailed], float64(overview.Usage.ComputeMS)/3600000)
	return nil
}

func formatUptime(seconds uint64) string {
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func clusterSubmitCommand(args []string) error {
	flags := flag.NewFlagSet("cluster submit", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	file := flags.String("file", "", "cluster job JSON file")
	token := flags.String("token", "", "producer token; defaults to local admin token")
	wait := flags.Bool("wait", true, "wait for a final result")
	stream := flags.Bool("stream", false, "print progressive browser text to stderr while waiting")
	artifactDir := flags.String("artifacts", "", "save returned images and files in this directory")
	sealed := flags.Bool("e2ee", false, "encrypt payload for the selected worker")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("--file is required")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *token == "" {
		*token = clusterClientToken(cfg, "")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var input cluster.SubmitRequest
	if err := json.Unmarshal(raw, &input); err != nil {
		return err
	}
	shared := ""
	if *sealed {
		var reservation cluster.AssignmentResponse
		if err := clusterPOST(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/assign", *token, input.Requirements, &reservation); err != nil {
			return err
		}
		envelope, sharedKey, err := cluster.SealFor(reservation.Assignment.PublicKey, input.Payload, []byte("job:"+reservation.Assignment.JobID+":"+reservation.Assignment.NodeID))
		if err != nil {
			return err
		}
		shared = sharedKey
		input.ID = reservation.Assignment.JobID
		input.Payload = nil
		input.Sealed = envelope
		input.AssignmentID = reservation.Assignment.ID
		input.AssignmentSecret = reservation.Secret
	}
	var job cluster.Job
	if err := clusterPOST(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/jobs", *token, input, &job); err != nil {
		return err
	}
	fmt.Println("Queued:", job.ID)
	if !*wait {
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	lastProgress := ""
	lastSequence := uint64(0)
	streamed := false
	if *stream && *sealed {
		fmt.Fprintln(os.Stderr, "Progress streaming is disabled for E2EE jobs; waiting for the encrypted final result.")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		if err := clusterGET(ctx, clusterBaseURL(cfg)+"/v1/cluster/jobs/"+url.PathEscape(job.ID), *token, &job); err != nil {
			return err
		}
		if *stream && !*sealed && job.Progress != nil && job.Progress.Sequence > lastSequence {
			current := job.Progress.Text
			if strings.HasPrefix(current, lastProgress) {
				fmt.Fprint(os.Stderr, strings.TrimPrefix(current, lastProgress))
			} else {
				if streamed {
					fmt.Fprintln(os.Stderr)
				}
				fmt.Fprint(os.Stderr, current)
			}
			lastProgress = current
			lastSequence = job.Progress.Sequence
			streamed = true
		}
		switch job.Status {
		case cluster.JobCompleted:
			if streamed {
				fmt.Fprintln(os.Stderr)
			}
			if job.SealedResult != nil {
				raw, err := cluster.OpenResponse(shared, job.SealedResult, []byte("result:"+job.ID+":"+job.AssignedNode))
				if err != nil {
					return err
				}
				raw, paths, references, err := materializeClusterArtifacts(raw, *artifactDir)
				if err != nil {
					return err
				}
				reportSavedArtifacts(paths, references)
				_, err = os.Stdout.Write(append(raw, '\n'))
				return err
			}
			raw, paths, references, err := materializeClusterArtifacts(job.Result, *artifactDir)
			if err != nil {
				return err
			}
			reportSavedArtifacts(paths, references)
			_, err = os.Stdout.Write(append(raw, '\n'))
			return err
		case cluster.JobFailed, cluster.JobCancelled:
			return fmt.Errorf("job %s: %s", job.Status, job.Error)
		}
	}
}

func clusterTokenCommand(args []string) error {
	flags := flag.NewFlagSet("cluster token", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	role := flags.String("role", "producer", "producer or observer")
	subject := flags.String("subject", "client", "token label")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	var output map[string]interface{}
	if err := clusterPOST(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/tokens", cfg.Cluster.Relay.AdminToken, map[string]interface{}{"role": *role, "subject": *subject}, &output); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(output)
}

func clusterLoginCommand(args []string) error {
	flags := flag.NewFlagSet("cluster login", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	tokenFile := flags.String("token-file", "", "file containing a producer token or token JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *tokenFile == "" {
		return errors.New("--token-file is required so credentials do not enter shell history")
	}
	raw, err := os.ReadFile(*tokenFile)
	if err != nil {
		return err
	}
	if len(raw) > 32<<10 {
		return errors.New("token file is unexpectedly large")
	}
	token := strings.TrimSpace(string(raw))
	var envelope struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Token != "" {
		token = strings.TrimSpace(envelope.Token)
	}
	if !strings.HasPrefix(token, "cb_") || len(token) < 24 || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return errors.New("token file does not contain a valid ContextBridge credential")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	cfg.Cluster.ClientToken = token
	if err := config.Save(*path, cfg); err != nil {
		return err
	}
	fmt.Println("Producer credential saved. cluster chat and cluster submit are ready.")
	return nil
}

func clusterPairingCommand(args []string) error {
	flags := flag.NewFlagSet("cluster pairing", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	approve := flags.String("approve", "", "approve pairing code")
	deny := flags.String("deny", "", "deny pairing code")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	decision, code := "approve", *approve
	if *deny != "" {
		decision, code = "deny", *deny
	}
	if code == "" {
		var pairings []cluster.Pairing
		if err := clusterGET(context.Background(), clusterBaseURL(cfg)+"/v1/pairings", cfg.Cluster.Relay.AdminToken, &pairings); err != nil {
			return err
		}
		for _, pairing := range pairings {
			fmt.Printf("%s  %-24s expires %s\n", pairing.UserCode, pairing.NodeName, pairing.ExpiresAt.Local().Format("15:04:05"))
		}
		return nil
	}
	var result cluster.Pairing
	return clusterPOST(context.Background(), clusterBaseURL(cfg)+"/v1/pairings/"+url.PathEscape(code)+"/"+decision, cfg.Cluster.Relay.AdminToken, map[string]interface{}{}, &result)
}

func clusterBaseURL(cfg config.Config) string {
	if cfg.Cluster.Relay.PublicURL != "" {
		return strings.TrimRight(cfg.Cluster.Relay.PublicURL, "/")
	}
	if cfg.Cluster.Worker.RelayURL != "" {
		return strings.TrimRight(cfg.Cluster.Worker.RelayURL, "/")
	}
	return "http://" + cfg.Cluster.Relay.Listen
}

func clusterGET(ctx context.Context, target, token string, output interface{}) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 24<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, output)
}

func clusterPOST(ctx context.Context, target, token string, input, output interface{}) error {
	raw, _ := json.Marshal(input)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 24<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if output != nil {
		return json.Unmarshal(body, output)
	}
	return nil
}

func formatBytes(value uint64) string {
	if value == 0 {
		return "unknown"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	size := float64(value)
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		command = exec.Command("open", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func submit(configPath string, job bridge.Job) (bridge.Submission, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return bridge.Submission{}, err
	}
	raw, _ := json.Marshal(job)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(cfg)+"/v1/jobs", bytes.NewReader(raw))
	if err != nil {
		return bridge.Submission{}, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return bridge.Submission{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return bridge.Submission{}, fmt.Errorf("bridge returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var result bridge.Submission
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
		return bridge.Submission{}, err
	}
	if result.Decision == nil && result.Output == nil {
		return bridge.Submission{}, errors.New("bridge returned no output")
	}
	return result, nil
}

func readInkWallJob(dir string) (bridge.Job, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "payload.json"))
	if err != nil {
		return bridge.Job{}, err
	}
	var payload struct {
		ID      string `json:"id"`
		Content struct {
			Name    string `json:"name"`
			Message string `json:"message"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return bridge.Job{}, err
	}
	name := strings.TrimSpace(payload.Content.Name)
	message := strings.TrimSpace(payload.Content.Message)
	if name == "" {
		name = readText(filepath.Join(dir, "name.txt"))
	}
	if message == "" {
		message = readText(filepath.Join(dir, "message.txt"))
	}
	job := bridge.Job{
		ID:     payload.ID,
		Source: "inkwall",
		Route:  "inkwall",
		Kind:   "moderation",
		Prompt: "Review this name, message, and optional image for a public GitHub profile. Flag harassment, hate, sexual content, violence, self-harm, doxxing, spam, scams, unsafe advertising, and copyright or IP concerns. Use allow only when it is safe to publish; otherwise use review.",
		Text:   "Display name: " + name + "\nMessage: " + message,
		Metadata: map[string]interface{}{
			"inkwall_job_dir": dir,
		},
		Output: bridge.OutputSpec{Mode: "decision"},
	}
	images, _ := filepath.Glob(filepath.Join(dir, "image.*"))
	if len(images) > 0 {
		imageRaw, readErr := os.ReadFile(images[0])
		if readErr == nil && len(imageRaw) <= 8<<20 {
			job.ImageBase64 = base64.StdEncoding.EncodeToString(imageRaw)
			job.ImageMediaType = mime.TypeByExtension(filepath.Ext(images[0]))
			if job.ImageMediaType == "" {
				job.ImageMediaType = http.DetectContentType(imageRaw)
			}
		}
	}
	return job, nil
}

func readText(path string) string {
	raw, _ := os.ReadFile(path)
	return strings.TrimSpace(string(raw))
}

func baseURL(cfg config.Config) string {
	return "http://" + cfg.Server.Listen
}

func defaultConfigPath() string {
	if env := os.Getenv("CONTEXTBRIDGE_CONFIG"); env != "" {
		return env
	}
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
			return filepath.Join(appData, "ContextBridge", "config.yml")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "contextbridge", "config.yml")
}
