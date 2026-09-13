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

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/llamaruntime"
	"github.com/IamAngusU/ContextBridge/internal/modelregistry"
	"github.com/IamAngusU/ContextBridge/internal/systeminfo"
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
	case "review":
		err = reviewCommand(os.Args[2:])
	case "health":
		err = healthCommand(os.Args[2:])
	case "dashboard":
		err = dashboardCommand(os.Args[2:])
	case "status":
		err = statusCommand(os.Args[2:])
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
		fmt.Fprintln(os.Stderr, "ContextBridge:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `ContextBridge routes trusted local jobs to Ollama or an explicitly paired browser tab.

Usage:
  contextbridge init [--config path]
  contextbridge serve [--config path]
  contextbridge run [--config path]
  contextbridge submit --file job.json [--config path]
  contextbridge review --job-dir path [--config path]
  contextbridge health [--config path]
  contextbridge dashboard [--config path] [--no-open]
  contextbridge status [--config path] [--json]
  contextbridge doctor [--config path] [--json]
  contextbridge hardware [--json]
  contextbridge models [--config path] [--json]
  contextbridge pull [--config path] MODEL
  contextbridge runtime install [--config path] llama.cpp
  contextbridge relay [--config path]
  contextbridge pair [--config path] [--relay URL] [--name NAME]
  contextbridge worker [--config path]
  contextbridge cluster status|submit|token|pairing [options]
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
	logger := log.New(os.Stdout, "ContextBridge  ", log.LstdFlags)
	server, err := bridge.NewServer(cfg, logger)
	if err != nil {
		return err
	}
	updateManager, err := updater.New(cfg.Updates, cfg.Storage.Directory, version)
	if err != nil {
		return err
	}
	server.SetUpdater(updateManager)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startUpdater(ctx, updateManager, logger)
	logger.Printf("version %s", version)
	logger.Printf("routes: %d, browser profiles: %d", len(cfg.Routes), len(cfg.BrowserProfiles))
	return server.Run(ctx)
}

func runCommand(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
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
	errorsCh := make(chan error, 3)
	logger := log.New(os.Stdout, "ContextBridge  ", log.LstdFlags)
	local, err := bridge.NewServer(cfg, logger)
	if err != nil {
		return err
	}
	updateManager, err := updater.New(cfg.Updates, cfg.Storage.Directory, version)
	if err != nil {
		return err
	}
	local.SetUpdater(updateManager)
	startUpdater(ctx, updateManager, logger)
	go func() { errorsCh <- local.Run(ctx) }()
	components := 1
	if cfg.Cluster.Relay.Enabled {
		relay, err := cluster.NewRelay(relayConfig(cfg), logger)
		if err != nil {
			return err
		}
		defer relay.Close()
		components++
		go func() { errorsCh <- relay.Run(ctx) }()
	}
	if cfg.Cluster.Worker.Enabled {
		worker, err := configuredWorker(cfg)
		if err != nil {
			return err
		}
		components++
		go func() { errorsCh <- worker.Run(ctx, logger.Printf) }()
	}
	logger.Printf("running %d component(s): local bridge%s%s", components, enabledLabel(cfg.Cluster.Relay.Enabled, ", relay"), enabledLabel(cfg.Cluster.Worker.Enabled, ", worker"))
	for i := 0; i < components; i++ {
		if err := <-errorsCh; err != nil {
			stop()
			return err
		}
	}
	return nil
}

func startUpdater(ctx context.Context, manager *updater.Manager, logger *log.Logger) {
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
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	manager, err := updater.New(cfg.Updates, cfg.Storage.Directory, version)
	if err != nil {
		return err
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
		value, err = result, applyErr
	case "auto":
		result, autoErr := manager.Auto(ctx)
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
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
		Version   string               `json:"version"`
		Listen    string               `json:"listen"`
		Queued    int                  `json:"queued"`
		Completed int                  `json:"completed"`
		Tunnel    bridge.TunnelStatus  `json:"tunnel"`
		Runtime   bridge.RuntimeStatus `json:"runtime"`
		Metrics   bridge.Metrics       `json:"metrics"`
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
	fmt.Printf("Jobs: %d total, %d failed\n", status.Metrics.JobsTotal, status.Metrics.JobsFailed)
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
		fmt.Printf("GPU: %s, %s, %s free of %s\n", gpu.Name, gpu.Backend, formatBytes(gpu.MemoryFree), formatBytes(gpu.MemoryTotal))
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
	fmt.Printf("%s/%s, %d CPU cores\n", snapshot.OS, snapshot.Architecture, snapshot.CPUCores)
	fmt.Printf("CPU: %s\n", snapshot.CPU)
	fmt.Printf("Memory: %s available of %s\n", formatBytes(snapshot.MemoryAvailable), formatBytes(snapshot.MemoryTotal))
	if len(snapshot.GPUs) == 0 {
		fmt.Println("GPU: no supported telemetry tool detected")
	}
	for _, gpu := range snapshot.GPUs {
		fmt.Printf("GPU: %s, %s, %s free of %s\n", gpu.Name, gpu.Backend, formatBytes(gpu.MemoryFree), formatBytes(gpu.MemoryTotal))
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
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
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
	startUpdater(ctx, updateManager, logger)
	return relay.Run(ctx)
}

func pairCommand(args []string) error {
	flags := flag.NewFlagSet("pair", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	relayURL := flags.String("relay", "", "public relay URL")
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
	if *relayURL == "" {
		return errors.New("--relay or cluster.worker.relay_url is required")
	}
	if *name == "" || *name == "auto" {
		*name, _ = os.Hostname()
	}
	if cfg.Cluster.Relay.Enabled && *relayURL == "http://"+cfg.Cluster.Relay.Listen {
		if err := cluster.BootstrapWorkerIdentity(cfg.Cluster.Relay.Database, *relayURL, *name, cfg.Cluster.Worker.IdentityFile, cfg.Cluster.Worker.Groups); err != nil {
			return err
		}
		fmt.Println("Local worker paired directly with the relay database.")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cluster.PairWorker(ctx, *relayURL, *name, cfg.Cluster.Worker.IdentityFile, cfg.Cluster.Worker.Groups, func(pair cluster.PairResponse) {
		fmt.Println("Pair this worker")
		fmt.Println("  Code:", pair.UserCode)
		fmt.Println("  Open:", pair.VerificationURI)
		fmt.Println("Waiting for approval. The code expires at", pair.ExpiresAt.Local().Format(time.RFC1123))
	})
}

func workerCommand(args []string) error {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
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
	updateManager, err := updater.New(cfg.Updates, cfg.Storage.Directory, version)
	if err != nil {
		return err
	}
	startUpdater(ctx, updateManager, log.New(os.Stdout, "ContextBridge worker  ", log.LstdFlags))
	return worker.Run(ctx, func(format string, values ...interface{}) {
		fmt.Printf(time.Now().Format("15:04:05")+"  "+format+"\n", values...)
	})
}

func relayConfig(cfg config.Config) cluster.RelayConfig {
	return cluster.RelayConfig{Listen: cfg.Cluster.Relay.Listen, PublicURL: cfg.Cluster.Relay.PublicURL, Database: cfg.Cluster.Relay.Database, AdminToken: cfg.Cluster.Relay.AdminToken, AllowedOrigins: cfg.Cluster.Relay.AllowedOrigins, MaxJobBytes: cfg.Cluster.Relay.MaxJobBytes, MaxQueuedJobs: cfg.Cluster.Relay.MaxQueue, PairingTTL: time.Duration(cfg.Cluster.Relay.PairingTTLSeconds) * time.Second, Pricing: cfg.Cluster.Pricing, AllowedTasks: cfg.Cluster.Policies.AllowedTasks, MaxAttempts: cfg.Cluster.Policies.MaxAttempts, Pipelines: cfg.Cluster.Pipelines, MaxPipelineRuntime: time.Duration(cfg.Cluster.Policies.MaxRuntime) * time.Second, JobTimeout: time.Duration(cfg.Cluster.Policies.MaxJobRuntime) * time.Second}
}

func configuredWorker(cfg config.Config) (*cluster.Worker, error) {
	name := cfg.Cluster.Worker.NodeName
	if name == "" || name == "auto" {
		name, _ = os.Hostname()
	}
	return cluster.LoadWorker(cluster.WorkerConfig{RelayURL: cfg.Cluster.Worker.RelayURL, IdentityFile: cfg.Cluster.Worker.IdentityFile, Name: name, Groups: cfg.Cluster.Worker.Groups, Tags: cfg.Cluster.Worker.Tags, MaxConcurrent: cfg.Cluster.Worker.MaxConcurrent, LocalURL: cfg.Cluster.Worker.LocalURL, LocalToken: cfg.Cluster.Worker.LocalToken, HeartbeatEvery: time.Duration(cfg.Cluster.Worker.HeartbeatSeconds) * time.Second, AllowedTasks: cfg.Cluster.Policies.AllowedTasks})
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
		return errors.New("usage: contextbridge cluster status|submit|token|pairing")
	}
	switch args[0] {
	case "status":
		return clusterStatusCommand(args[1:])
	case "submit":
		return clusterSubmitCommand(args[1:])
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
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	var overview cluster.Overview
	if err := clusterGET(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/overview", cfg.Cluster.Relay.AdminToken, &overview); err != nil {
		return err
	}
	fmt.Printf("Nodes: %d online of %d\n", overview.NodesOnline, overview.NodesTotal)
	fmt.Printf("Jobs: %d queued, %d running, %d completed, %d failed\n", overview.JobsByState[cluster.JobQueued], overview.JobsByState[cluster.JobRunning]+overview.JobsByState[cluster.JobAssigned], overview.JobsByState[cluster.JobCompleted], overview.JobsByState[cluster.JobFailed])
	fmt.Printf("Usage: %d tokens, %.2f compute hours, $%.4f estimated savings\n", overview.Usage.TotalTokens, float64(overview.Usage.ComputeMS)/3600000, overview.Usage.SavedCostUSD)
	return nil
}

func clusterSubmitCommand(args []string) error {
	flags := flag.NewFlagSet("cluster submit", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	file := flags.String("file", "", "cluster job JSON file")
	token := flags.String("token", "", "producer token; defaults to local admin token")
	wait := flags.Bool("wait", true, "wait for a final result")
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
		*token = cfg.Cluster.Relay.AdminToken
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
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		if err := clusterGET(ctx, clusterBaseURL(cfg)+"/v1/cluster/jobs/"+url.PathEscape(job.ID), *token, &job); err != nil {
			return err
		}
		switch job.Status {
		case cluster.JobCompleted:
			if job.SealedResult != nil {
				raw, err := cluster.OpenResponse(shared, job.SealedResult, []byte("result:"+job.ID+":"+job.AssignedNode))
				if err != nil {
					return err
				}
				_, err = os.Stdout.Write(append(raw, '\n'))
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(job.Result)
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
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
