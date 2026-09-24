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
	"math"
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
	"github.com/IamAngusU/ContextBridge/internal/resourcepacks"
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
	case "stop":
		err = stopCommand(os.Args[2:])
	case "uninstall":
		err = uninstallCommand(os.Args[2:])
	case "console":
		err = consoleCommand(os.Args[2:])
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
	case "doctor":
		err = doctorCommand(os.Args[2:])
	case "hardware":
		err = hardwareCommand(os.Args[2:])
	case "models":
		err = modelsCommand(os.Args[2:])
	case "resources":
		err = resourcesCommand(os.Args[2:])
	case "pull":
		err = pullCommand(os.Args[2:])
	case "runtime":
		err = runtimeCommand(os.Args[2:])
	case "mcp":
		err = mcpCommand(os.Args[2:])
	case "integrate":
		err = integrateCommand(os.Args[2:])
	case "benchmark":
		err = performanceCommand(os.Args[2:])
	case "verification":
		err = verificationCommand(os.Args[2:])
	case "relay":
		err = relayCommand(os.Args[2:])
	case "pair":
		err = pairCommand(os.Args[2:])
	case "worker":
		err = workerCommand(os.Args[2:])
	case "cluster":
		err = clusterCommand(os.Args[2:])
	case "route":
		err = clusterRouteCommand(os.Args[2:])
	case "selftest":
		// Operator-friendly shortcut for the identical cluster command. It works
		// on a worker PC, relay VPS, or any configured producer.
		err = clusterSelftestCommand(os.Args[2:])
	case "update":
		err = updateCommand(os.Args[2:])
	case "completion":
		err = completionCommand(os.Args[2:])
	case "help", "--help", "-h":
		usage()
		return
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
	fmt.Fprintln(os.Stderr, `ContextBridge routes trusted jobs across local engines, API engines, and worker pools.

Usage:
  contextbridge init [--config path]
  contextbridge serve [--config path]
  contextbridge run [--config path] [--slots N] [--topmost]
  contextbridge stop [--config path] [--force]
  contextbridge uninstall [--config path] [--install-dir path] [--purge] [--force] [--yes] [--dry-run]
  contextbridge console [--config path] # read-only view; type exit + Enter to close
  contextbridge submit --file job.json [--config path]
  contextbridge schedule add --file schedule.json [--config path]
  contextbridge schedule list|show|pause|resume|run|delete [ID] [--config path]
  contextbridge result JOB_ID [--config path]
  contextbridge review --job-dir path [--config path]
  contextbridge health [--config path]
  contextbridge dashboard [--config path] [--no-open]
  contextbridge status [--config path] [--json]
  contextbridge doctor [--config path] [--json]
  contextbridge hardware [--json]
  contextbridge models [--config path] [--json]
  contextbridge resources [--config path] [--json]
  contextbridge pull [--config path] MODEL
  contextbridge runtime install [--config path] llama.cpp
  contextbridge mcp serve [--config path]
  contextbridge integrate openai|mcp|relay [--config path] [--json]
  contextbridge benchmark [--json] [--samples N] [--warmup N] [--database-jobs N] [--idle-duration D] [--binary path]
  contextbridge verification verify --file STATEMENT.json --trust-key KEY.json [--artifact FILE] [--require-artifact] [--evidence-dir DIR] [--require-evidence] [--json]
  contextbridge relay [--config path]
  contextbridge pair [--config path] [--relay URL] [--identity path] [--name NAME]
  contextbridge worker [--config path] [--relay URL] [--identity path] [--name NAME] [--slots N] [--providers LIST] [--models LIST] [--tasks LIST] [--topmost]
  contextbridge selftest [options]
	contextbridge cluster status|events|node|protocol|conformance|submit|chat|agent|selftest|route|contract|receipt|login|token|pairing [options]
	contextbridge cluster agent auto [--policy NAME] --goal TEXT [options]
	contextbridge cluster agent plan --goal TEXT --out PLAN.json [options]
	contextbridge cluster agent run --plan PLAN.json --approve sha256:HASH [options]
	contextbridge route explain (--file job.json | --job JOB_ID) [--json]
  contextbridge update status|check|apply|enable|disable|auto [options]
  contextbridge completion powershell|bash|zsh
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
	fmt.Println("The generated token stays local. Keep the config file private.")
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
	session := terminalui.NewWithStyle(os.Stdout, cfg.Terminal.Style)
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
	server.SetLifecycleControl(server.Idle, stop)
	startUpdater(ctx, updateManager, logger, func(context.Context) bool { return server.Idle() })
	logger.Printf("version %s", version)
	logger.Printf("routes: %d, adapter profiles: %d", len(cfg.Routes), len(cfg.AdapterProfiles))
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
	session := terminalui.NewWithStyle(os.Stdout, cfg.Terminal.Style)
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
	allIdle := func() bool {
		if !local.Idle() || (relay != nil && !relay.Idle()) || (worker != nil && !worker.Idle()) {
			return false
		}
		return true
	}
	local.SetLifecycleControl(allIdle, stop)
	local.SetLifecycleQuiesce(func(force bool) bool {
		relayQuiesced := false
		if relay != nil {
			if !relay.QuiesceForStop(force) {
				return false
			}
			relayQuiesced = true
		}
		if worker != nil && !worker.QuiesceForStop(force) {
			if relayQuiesced {
				relay.ResumeAfterRejectedStop()
			}
			return false
		}
		return true
	})
	startUpdater(ctx, updateManager, logger, func(context.Context) bool { return allIdle() })
	session.Banner(version, fmt.Sprintf("%d components · local bridge%s%s", components, enabledLabel(cfg.Cluster.Relay.Enabled, " · relay"), enabledLabel(cfg.Cluster.Worker.Enabled, " · worker")))
	if cfg.Cluster.Relay.Enabled || cfg.Cluster.Worker.Enabled {
		go watchPoolDisplay(ctx, cfg, session)
	}
	session.EnableServiceCommands()
	session.EnableServiceStopCommand()
	commands, restoreInput := foregroundServiceCommandInput(os.Stdin, session)
	defer restoreInput()
	go func() { errorsCh <- local.Run(ctx) }()
	if relay != nil {
		go func() { errorsCh <- relay.Run(ctx) }()
	}
	if worker != nil {
		go func() { errorsCh <- worker.RunWithEvents(ctx, session.HandleWorker) }()
	}
	return waitForForegroundComponents(stop, session, errorsCh, commands, components)
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
		logger.Printf("staged %s; restarting the managed service", result.TargetVersion)
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
			applyErr = finishManagedUpdate(ctx, manager, *managedService, result.TargetVersion)
			result.RestartRequired = applyErr != nil
			if applyErr == nil {
				markUpdateActive(&result)
			}
		}
		value, err = result, applyErr
	case "auto":
		manager.SetIdleCheck(func(ctx context.Context) bool { return installedServiceIdle(ctx, cfg, *relayOnly) })
		result, autoErr := manager.Auto(ctx)
		if autoErr == nil && result.Applied && *managedService != "" {
			autoErr = finishManagedUpdate(ctx, manager, *managedService, result.TargetVersion)
			result.RestartRequired = autoErr != nil
			if autoErr == nil {
				markUpdateActive(&result)
			}
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
	check := func(address, path, token string) bool {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return false
		}
		if host != "localhost" && host != "" && host != "0.0.0.0" && host != "::" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
			return false
		}
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		target := strings.TrimSuffix(localHealthURL(address), "/health") + path
		request, err := http.NewRequestWithContext(probe, http.MethodGet, target, nil)
		if err != nil {
			return false
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
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
		return cfg.Cluster.Relay.Enabled && check(cfg.Cluster.Relay.Listen, "/v1/cluster/lifecycle", cfg.Cluster.Relay.AdminToken)
	}
	if cfg.Cluster.Relay.Enabled && !check(cfg.Cluster.Relay.Listen, "/v1/cluster/lifecycle", cfg.Cluster.Relay.AdminToken) {
		return false
	}
	return check(cfg.Server.Listen, "/health", "")
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
		// #nosec G702 -- executable is fixed and service passed validManagedService's strict character/shape validation.
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
		Status          updater.Status `json:"status"`
		Applied         bool           `json:"applied"`
		RestartRequired bool           `json:"restart_required"`
		TargetVersion   string         `json:"target_version"`
	}
	if json.Unmarshal(raw, &result) == nil && result.Status.Repository != "" {
		fmt.Printf("Current: %s\n", result.Status.CurrentVersion)
		fmt.Printf("Available: %s\n", emptyLabel(result.Status.AvailableVersion, "not checked"))
		fmt.Printf("Automatic updates: %s\n", onOffLabel(result.Status.Enabled))
		if result.Applied {
			fmt.Println(updateAppliedMessage(result.RestartRequired, runtime.GOOS, result.TargetVersion))
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

func updateAppliedMessage(restartRequired bool, goos, target string) string {
	if strings.TrimSpace(target) == "" {
		target = "The verified release"
	}
	if !restartRequired {
		return target + " is installed and the managed service is running it."
	}
	if goos == "windows" {
		return target + " is staged. The Windows update helper is attempting activation; it is not reported as installed until the new executable actually runs."
	}
	return target + " is staged. Restart the running ContextBridge process or service to activate it, or pass --managed-service on Linux; it is not reported as installed before activation."
}

func markUpdateActive(result *updater.Result) {
	if result == nil || strings.TrimSpace(result.TargetVersion) == "" {
		return
	}
	result.Status.CurrentVersion = result.TargetVersion
	result.Status.LastInstalled = result.TargetVersion
	result.Status.PendingVersion = ""
	result.Status.UpdateAvailable = false
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
	const maxJobJSONBytes = 12 << 20
	var raw []byte
	var err error
	if *jobPath == "-" {
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, maxJobJSONBytes+1))
	} else {
		file, openErr := os.Open(*jobPath)
		if openErr != nil {
			return openErr
		}
		defer file.Close()
		raw, err = io.ReadAll(io.LimitReader(file, maxJobJSONBytes+1))
	}
	if err != nil {
		return err
	}
	if len(raw) > maxJobJSONBytes {
		return errors.New("job JSON exceeds the 12 MiB request limit")
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
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if *jobDir == "" && flags.NArg() > 0 {
		*jobDir = flags.Arg(0)
	}
	if flags.NArg() > 1 {
		return errors.New("review accepts only one job directory")
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
	noOpen := flags.Bool("no-open", false, "print the dashboard address without opening a adapter")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	base := localDashboardTarget(cfg)
	if *noOpen {
		fmt.Println(base)
		fmt.Println("Open the dashboard and enter the pairing token from your config.")
		return nil
	}
	if err := openAdapter(base); err != nil {
		fmt.Println(base)
		return fmt.Errorf("could not open the default adapter: %w", err)
	}
	fmt.Println("Opened the local ContextBridge dashboard. Enter the pairing token from your config.")
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
	clockRequestStarted := time.Now()
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("service is not reachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 24<<20))
	clockRequestEnded := time.Now()
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
		Version                string                     `json:"version"`
		Listen                 string                     `json:"listen"`
		ServerTime             time.Time                  `json:"server_time"`
		ServerUTCOffsetSeconds int                        `json:"server_utc_offset_seconds"`
		Queued                 int                        `json:"queued"`
		Completed              int                        `json:"completed"`
		Tunnel                 bridge.TunnelStatus        `json:"tunnel"`
		Adapter                bridge.AdapterClientStatus `json:"adapter"`
		Runtime                bridge.RuntimeStatus       `json:"runtime"`
		Metrics                bridge.Metrics             `json:"metrics"`
		Schedules              []struct {
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
	if !status.ServerTime.IsZero() {
		serviceZone := time.FixedZone("service", status.ServerUTCOffsetSeconds)
		midpoint := clockRequestStarted.Add(clockRequestEnded.Sub(clockRequestStarted) / 2)
		fmt.Printf("Clock: %s %s · %s vs this PC (approximately ±%s)\n", status.ServerTime.In(serviceZone).Format("15:04:05"), formatUTCOffset(status.ServerUTCOffsetSeconds), formatSignedClockDelta(status.ServerTime.Sub(midpoint)), formatClockDuration(clockRequestEnded.Sub(clockRequestStarted)/2))
	}
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
	if status.Adapter.Connected {
		fmt.Printf("Adapter: %d endpoint(s), %d busy, version %s\n", max(1, status.Adapter.ActiveEndpoints), status.Adapter.BusyEndpoints, status.Adapter.AdapterVersion)
		for _, endpoint := range status.Adapter.Endpoints {
			choice := endpoint.CurrentModel
			if endpoint.CurrentReasoning != "" {
				choice += " · " + endpoint.CurrentReasoning
			}
			fmt.Printf("  [%s] endpoint %d · %s", endpoint.State, endpoint.ID, endpoint.Profile)
			if choice != "" {
				fmt.Printf("  [%s]", choice)
			}
			fmt.Println()
		}
	} else {
		fmt.Println("Adapter: not connected")
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
					memory := fmt.Sprintf("%s model", formatInt64Bytes(model.Size))
					if model.VRAM > 0 {
						memory = fmt.Sprintf("%s VRAM", formatInt64Bytes(model.VRAM))
					}
					fmt.Printf("  Loaded: %s on %s, %s\n", model.Name, model.Affinity, memory)
				}
			}
			fmt.Printf("  Model cache: %d available, %d loaded\n", len(engine.Models), loaded)
		}
	}
	for _, pack := range status.Runtime.Packs {
		identity := pack.Name
		if pack.Version != "" {
			identity += " " + pack.Version
		}
		fmt.Printf("Resource pack %s: %s (%s)\n", pack.ID, identity, pack.Path)
		for _, endpoint := range pack.Endpoints {
			detail := endpoint.Type
			if endpoint.Model != "" {
				detail += " · " + endpoint.Model
			}
			if len(endpoint.Capabilities) > 0 {
				detail += " · " + strings.Join(endpoint.Capabilities, "+")
			}
			fmt.Printf("  Endpoint %s: %s\n", endpoint.ID, detail)
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
			fmt.Printf("%-28s  [%-6s]  [%-16s]  [%s RAM est.]  %s\n", model.Name, state, detail, formatInt64Bytes(model.MemoryEstimate), location)
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
			state = formatInt64Bytes(model.Size)
		}
		fmt.Printf("%-24s %-14s %s\n", model.Name, state, model.Repository+"/"+model.File)
	}
	return nil
}

func resourcesCommand(args []string) error {
	flags := flag.NewFlagSet("resources", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	enabled := cfg.Portable.Enabled != nil && *cfg.Portable.Enabled
	packs, err := resourcepacks.Discover(resourcepacks.Settings{
		Enabled:   enabled,
		ScanRoots: append([]string(nil), cfg.Portable.ScanRoots...),
		MaxPacks:  cfg.Portable.MaxPacks,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(packs)
	}
	if !enabled {
		fmt.Println("Portable resource discovery is disabled in the config.")
		return nil
	}
	if len(packs) == 0 {
		fmt.Printf("No portable resource packs found. Add %s to a selected root/direct child, or a JSON sidecar under %s at the volume root.\n", resourcepacks.MarkerName, resourcepacks.SidecarDirectory)
		return nil
	}
	for _, pack := range packs {
		identity := pack.Name
		if pack.Version != "" {
			identity += " " + pack.Version
		}
		if pack.Quarantined {
			identity += " · QUARANTINED"
		}
		fmt.Printf("%s  [%s]  %s\n", pack.ID, emptyLabel(pack.Kind, "resource-pack"), identity)
		fmt.Printf("  Path: %s\n", pack.Path)
		for _, endpoint := range pack.Endpoints {
			detail := endpoint.Type
			if endpoint.Model != "" {
				detail += " · " + endpoint.Model
			}
			if len(endpoint.Capabilities) > 0 {
				detail += " · " + strings.Join(endpoint.Capabilities, "+")
			}
			fmt.Printf("  %s: %s at %s\n", endpoint.ID, detail, endpoint.URL)
		}
		if pack.Warning != "" {
			fmt.Printf("  Warning: %s\n", pack.Warning)
		}
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
	if err := parseInterspersedFlags(flags, args); err != nil {
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
		fmt.Printf("Ready: %s (%s)\n", entry.Path, formatInt64Bytes(entry.Size))
	}
	return nil
}

func runtimeCommand(args []string) error {
	if len(args) == 0 || args[0] != "install" {
		return errors.New("usage: contextbridge runtime install [--config path] llama.cpp")
	}
	flags := flag.NewFlagSet("runtime install", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	if err := parseInterspersedFlags(flags, args[1:]); err != nil {
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
	session := terminalui.NewWithStyle(os.Stdout, cfg.Terminal.Style)
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
	session.EnableServiceCommands()
	commands, restoreInput := foregroundServiceCommandInput(os.Stdin, session)
	defer restoreInput()
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- worker.RunWithEvents(ctx, session.HandleWorker) }()
	return waitForForegroundComponents(stop, session, errorsCh, commands, 1)
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
	return cluster.RelayConfig{
		Version: version, Listen: cfg.Cluster.Relay.Listen, PublicURL: cfg.Cluster.Relay.PublicURL,
		Database: cfg.Cluster.Relay.Database, AdminToken: cfg.Cluster.Relay.AdminToken, AllowedOrigins: cfg.Cluster.Relay.AllowedOrigins,
		MaxJobBytes: cfg.Cluster.Relay.MaxJobBytes, MaxQueuedJobs: cfg.Cluster.Relay.MaxQueue,
		PairingTTL: time.Duration(cfg.Cluster.Relay.PairingTTLSeconds) * time.Second,
		Pricing:    cfg.Cluster.Pricing, AllowedTasks: cfg.Cluster.Policies.AllowedTasks,
		ExecutionPolicy: cfg.Cluster.Policies.Execution, MaxAttempts: cfg.Cluster.Policies.MaxAttempts,
		Pipelines: cfg.Cluster.Pipelines, MaxPipelineRuntime: time.Duration(cfg.Cluster.Policies.MaxRuntime) * time.Second,
		JobTimeout:      time.Duration(cfg.Cluster.Policies.MaxJobRuntime) * time.Second,
		RetentionMaxAge: time.Duration(cfg.Cluster.Relay.RetentionDays) * 24 * time.Hour,
		MaxTerminalJobs: cfg.Cluster.Relay.MaxTerminalJobs, MaxEvents: cfg.Cluster.Relay.MaxEvents,
		MaxTerminalRuns:      cfg.Cluster.Relay.MaxTerminalPipelineRuns,
		MaxSessionPlacements: cfg.Cluster.Relay.MaxSessionPlacements,
		RetentionSweep:       time.Duration(cfg.Cluster.Relay.RetentionSweepSeconds) * time.Second,
	}
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
		return errors.New("usage: contextbridge cluster status|events|node|protocol|conformance|submit|chat|agent|selftest|route|contract|receipt|login|token|pairing")
	}
	switch args[0] {
	case "status":
		return clusterStatusCommand(args[1:])
	case "events":
		return clusterEventsCommand(args[1:])
	case "node":
		return clusterNodeCommand(args[1:])
	case "protocol":
		return clusterProtocolCommand(args[1:])
	case "conformance":
		return clusterConformanceCommand(args[1:])
	case "submit":
		return clusterSubmitCommand(args[1:])
	case "chat":
		return clusterChatCommand(args[1:])
	case "agent":
		return clusterAgentCommand(args[1:])
	case "selftest":
		return clusterSelftestCommand(args[1:])
	case "route":
		return clusterRouteCommand(args[1:])
	case "contract":
		return clusterContractCommand(args[1:])
	case "receipt":
		return clusterReceiptCommand(args[1:])
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

func clusterEventsCommand(args []string) error {
	flags := flag.NewFlagSet("cluster events", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "producer, observer, or admin token; defaults to the configured client token")
	after := flags.Uint64("after", 0, "return events after this sequence")
	limit := flags.Int("limit", 100, "events per page; 1-500")
	asJSON := flags.Bool("json", false, "print the versioned event page as JSON")
	follow := flags.Bool("follow", false, "poll until a terminal lifecycle event is observed")
	pipeline := flags.Bool("pipeline", false, "read a pipeline-run event stream instead of a job stream")
	poll := flags.Duration("poll", 500*time.Millisecond, "follow polling interval; 100ms-30s")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 || strings.TrimSpace(flags.Arg(0)) == "" {
		return errors.New("usage: contextbridge cluster events JOB_OR_RUN_ID [--pipeline] [--after N] [--limit N] [--json] [--follow]")
	}
	if *limit < 1 || *limit > 500 {
		return errors.New("--limit must be between 1 and 500")
	}
	if *poll < 100*time.Millisecond || *poll > 30*time.Second {
		return errors.New("--poll must be between 100ms and 30s")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *token == "" {
		*token = clusterClientToken(cfg, "")
	}
	jobID := strings.TrimSpace(flags.Arg(0))
	cursor := *after
	for {
		var page cluster.JobEventPage
		kind := "jobs"
		if *pipeline {
			kind = "pipeline-runs"
		}
		target := fmt.Sprintf("%s/v1/cluster/%s/%s/events?after=%d&limit=%d", clusterBaseURL(cfg), kind, url.PathEscape(jobID), cursor, *limit)
		if err := clusterGET(context.Background(), target, *token, &page); err != nil {
			return err
		}
		if *asJSON {
			if err := json.NewEncoder(os.Stdout).Encode(page); err != nil {
				return err
			}
		} else {
			if page.Gap {
				fmt.Fprintf(os.Stderr, "Warning: event history before sequence %d is no longer retained; resume from %d.\n", page.OldestRetained, page.OldestRetained)
			}
			for _, event := range page.Events {
				fmt.Printf("%6d  %s  %-20s", event.Sequence, event.Time.UTC().Format(time.RFC3339), event.Type)
				if event.Attempt > 0 {
					fmt.Printf("  attempt %d", event.Attempt)
				}
				if event.NodeID != "" {
					fmt.Printf("  node %s", event.NodeID)
				}
				if event.StepID != "" {
					fmt.Printf("  step %s", event.StepID)
				}
				fmt.Printf("  [%s]\n", event.Authority)
			}
		}
		terminal := false
		for _, event := range page.Events {
			if terminalExecutionEvent(event.Type) {
				terminal = true
			}
		}
		if page.Next > cursor {
			cursor = page.Next
		}
		if !*follow || terminal {
			return nil
		}
		time.Sleep(*poll)
	}
}

func terminalExecutionEvent(eventType string) bool {
	switch eventType {
	case "job.completed", "job.failed", "job.cancelled", "job.ambiguous", "pipeline.completed", "pipeline.failed", "pipeline.cancelled":
		return true
	default:
		return false
	}
}

func clusterRouteCommand(args []string) error {
	if len(args) == 0 || args[0] != "explain" {
		return errors.New("usage: contextbridge cluster route explain (--file job.json | --job JOB_ID) [--json]")
	}
	flags := flag.NewFlagSet("cluster route explain", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	file := flags.String("file", "", "cluster job JSON file for a non-executing preview")
	jobID := flags.String("job", "", "assigned job ID with a durable routing decision")
	token := flags.String("token", "", "producer token; defaults to local admin token")
	asJSON := flags.Bool("json", false, "print machine-readable routing evidence")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if (*file == "") == (*jobID == "") {
		return errors.New("exactly one of --file or --job is required")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *token == "" {
		*token = clusterClientToken(cfg, "")
	}
	var decision cluster.RoutingDecision
	if *file != "" {
		const maximumClusterSubmissionFileBytes = ((cluster.MaximumJobPayloadBytes+16)*4+2)/3 + (64 << 10)
		raw, err := readRegularFileBounded(*file, maximumClusterSubmissionFileBytes)
		if err != nil {
			return fmt.Errorf("cluster job: %w", err)
		}
		var input cluster.SubmitRequest
		if err := json.Unmarshal(raw, &input); err != nil {
			return err
		}
		request := cluster.AssignmentRequest{TenantID: input.TenantID, Requirements: input.Requirements}
		if err := clusterPOST(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/routes/explain", *token, request, &decision); err != nil {
			return err
		}
	} else {
		if err := clusterGET(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/jobs/"+url.PathEscape(*jobID)+"/route", *token, &decision); err != nil {
			return err
		}
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(decision)
	}
	printRoutingDecision(decision)
	return nil
}

func printRoutingDecision(decision cluster.RoutingDecision) {
	kind := "Route decision"
	if decision.Preview {
		kind = "Route preview · no job was submitted"
	}
	fmt.Println(kind)
	if decision.SelectedNodeID == "" {
		fmt.Println("Selected  [none · no current candidate satisfies every requirement]")
	} else {
		fmt.Printf("Selected  [%s · %s]\n", emptyLabel(decision.SelectedNodeName, "unnamed node"), decision.SelectedNodeID)
	}
	fmt.Printf("Needs  [task %s", decision.Requirements.Task)
	if decision.Requirements.Provider != "" {
		fmt.Printf(" · provider %s", decision.Requirements.Provider)
	}
	if decision.Requirements.Model != "" {
		fmt.Printf(" · model %s", decision.Requirements.Model)
	}
	if decision.Requirements.Group != "" {
		fmt.Printf(" · group %s", decision.Requirements.Group)
	}
	fmt.Println("]")
	for _, candidate := range decision.Candidates {
		if candidate.Eligible {
			fmt.Printf("  ✓ %s  [score %.2f]  %s\n", emptyLabel(candidate.NodeName, candidate.NodeID), candidate.Score, routingScoreSummary(candidate.ScoreComponents))
			continue
		}
		fmt.Printf("  × %s  [%s]\n", emptyLabel(candidate.NodeName, candidate.NodeID), strings.Join(candidate.RejectionReasons, " · "))
	}
	if decision.CandidatesTruncated > 0 {
		fmt.Printf("  … %d additional candidates omitted\n", decision.CandidatesTruncated)
	}
}

func routingScoreSummary(components cluster.RoutingScoreComponents) string {
	parts := []string{}
	values := []struct {
		name  string
		value float64
	}{
		{"load", components.ActiveLoad}, {"queue", components.QueueDepth}, {"memory", components.MemoryPressure},
		{"cpu", components.CPUPressure}, {"gpu", components.GPUPressure}, {"vram", components.VRAMHeadroom},
		{"adapter", components.AdapterPressure}, {"loaded", components.LoadedModel}, {"fit", components.EstimatedVRAMFit},
		{"preferred", components.PreferredNode},
	}
	for _, value := range values {
		if value.value != 0 {
			parts = append(parts, fmt.Sprintf("%s %+.2f", value.name, value.value))
		}
	}
	if len(parts) == 0 {
		return "neutral evidence"
	}
	return strings.Join(parts, " · ")
}

func clusterConfigureCommand(args []string) error {
	flags := flag.NewFlagSet("cluster configure", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	mode := flags.String("mode", "local", "local, client/sender, relay, worker, or all")
	relayURL := flags.String("relay-url", "", "public relay URL for this worker")
	publicURL := flags.String("public-url", "", "public HTTPS URL of this relay")
	name := flags.String("name", "auto", "worker node name")
	listen := flags.String("listen", "", "relay listen address or auto")
	if err := flags.Parse(args); err != nil {
		return err
	}
	nameWasSet := false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "name" {
			nameWasSet = true
		}
	})
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	configuredMode := strings.ToLower(strings.TrimSpace(*mode))
	if configuredMode == "sender" {
		configuredMode = "client"
	}
	switch configuredMode {
	case "local":
		cfg.Cluster.Relay.Enabled, cfg.Cluster.Worker.Enabled = false, false
	case "client":
		cfg.Cluster.Relay.Enabled, cfg.Cluster.Worker.Enabled = false, false
	case "relay":
		cfg.Cluster.Relay.Enabled, cfg.Cluster.Worker.Enabled = true, false
	case "worker":
		cfg.Cluster.Relay.Enabled, cfg.Cluster.Worker.Enabled = false, true
	case "all":
		cfg.Cluster.Relay.Enabled, cfg.Cluster.Worker.Enabled = true, true
	default:
		return errors.New("--mode must be local, client (or sender), relay, worker, or all")
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
		normalizedRelayURL := strings.TrimRight(strings.TrimSpace(*relayURL), "/")
		if err := cluster.ValidateRelayURL(normalizedRelayURL); err != nil {
			return fmt.Errorf("--relay-url: %w", err)
		}
		cfg.Cluster.Worker.RelayURL = normalizedRelayURL
	}
	if configuredMode == "all" && cfg.Cluster.Worker.RelayURL == "" {
		cfg.Cluster.Worker.RelayURL = "http://" + cfg.Cluster.Relay.Listen
	}
	if configuredMode == "client" && cfg.Cluster.Worker.RelayURL == "" {
		return errors.New("--relay-url is required for client/sender mode unless a relay URL is already saved")
	}
	if cfg.Cluster.Worker.Enabled && cfg.Cluster.Worker.RelayURL == "" {
		return errors.New("--relay-url is required for worker mode")
	}
	if cfg.Cluster.Worker.Enabled && (nameWasSet || strings.TrimSpace(cfg.Cluster.Worker.NodeName) == "") {
		cfg.Cluster.Worker.NodeName = *name
	}
	if err := config.Save(*path, cfg); err != nil {
		return err
	}
	fmt.Printf("Cluster mode saved: %s\n", configuredMode)
	if configuredMode == "client" {
		fmt.Println("This device can submit and observe work but will not accept worker assignments after the service restarts.")
		fmt.Println("The saved worker identity was retained, so switching back to worker mode does not require re-pairing when the relay is unchanged.")
		if strings.TrimSpace(cfg.Cluster.ClientToken) == "" {
			fmt.Println("Next: create a scoped producer token on the relay, then run contextbridge cluster login --token-file <path>.")
		}
	}
	if cfg.Cluster.Worker.Enabled {
		if _, identityErr := configuredWorker(cfg); identityErr != nil {
			fmt.Println("Next: contextbridge pair --config", *path)
		} else {
			fmt.Println("Existing worker identity retained. Restart ContextBridge to rejoin the pool as a worker.")
		}
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
	target := clusterDashboardTarget(cfg)
	if *noOpen {
		fmt.Println(target)
		fmt.Println("Open the dashboard and enter the relay admin token from your config.")
		return nil
	}
	if err := openAdapter(target); err != nil {
		fmt.Println(target)
		return fmt.Errorf("could not open the cluster dashboard: %w", err)
	}
	fmt.Println("Opened the cluster dashboard. Enter the relay admin token from your config.")
	return nil
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
	raw, err := readRegularFileBounded(*file, cluster.MaximumJobPayloadBytes)
	if err != nil {
		return fmt.Errorf("pipeline input: %w", err)
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
	clockRequestStarted := time.Now()
	if err := clusterGET(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/overview", token, &overview); err != nil {
		return err
	}
	clockRequestEnded := time.Now()
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
	if !overview.GeneratedAt.IsZero() {
		zone := time.FixedZone("relay", overview.UTCOffsetSeconds)
		midpoint := clockRequestStarted.Add(clockRequestEnded.Sub(clockRequestStarted) / 2)
		fmt.Printf("Relay clock  [%s %s]  [%s vs this PC · approximately ±%s]\n", overview.GeneratedAt.In(zone).Format("15:04:05"), formatUTCOffset(overview.UTCOffsetSeconds), formatSignedClockDelta(overview.GeneratedAt.Sub(midpoint)), formatClockDuration(clockRequestEnded.Sub(clockRequestStarted)/2))
	}
	for _, node := range nodes {
		state := "offline"
		if node.Connected {
			state = "online"
		}
		if node.Draining && node.Connected {
			state = "draining"
		} else if node.Draining {
			state = "offline · draining"
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
		if !node.Capabilities.ClockTime.IsZero() {
			zone := time.FixedZone("node", node.Capabilities.UTCOffsetSeconds)
			fmt.Printf("      Clock  [%s %s]  [%s vs relay at last heartbeat · approximate]\n", node.Capabilities.ClockTime.In(zone).Format("15:04:05"), formatUTCOffset(node.Capabilities.UTCOffsetSeconds), formatSignedClockDelta(time.Duration(node.ClockOffsetMS)*time.Millisecond))
		}
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
	fmt.Printf("Jobs  [%d completed]  [%d failed]  [%.2f compute hours]  %s\n", overview.JobsByState[cluster.JobCompleted], overview.JobsByState[cluster.JobFailed], float64(overview.Usage.ComputeMS)/3600000, formatClusterCost(overview.Usage))
	return nil
}

func formatClusterCost(usage cluster.Usage) string {
	if usage.CostKnownJobs == 0 {
		return fmt.Sprintf("[cost unknown · %d jobs]", usage.CostUnknownJobs)
	}
	status := strings.TrimSpace(strings.ReplaceAll(usage.CostStatus, "_", " "))
	if status == "" || status == cluster.CostPartial {
		status = "mixed evidence"
	}
	reservation := ""
	if usage.ReservedCostUSD > 0 {
		reservation = fmt.Sprintf(" · reservation basis $%.6f", usage.ReservedCostUSD)
	}
	if usage.CostUnknownJobs > 0 {
		return fmt.Sprintf("[tracked cost $%.6f · %s · %d known / %d unknown jobs%s]", usage.EstimatedCostUSD, status, usage.CostKnownJobs, usage.CostUnknownJobs, reservation)
	}
	return fmt.Sprintf("[tracked cost $%.6f · %s · %d jobs%s]", usage.EstimatedCostUSD, status, usage.CostKnownJobs, reservation)
}

func formatUTCOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, seconds/3600, seconds%3600/60)
}

func formatClockDuration(duration time.Duration) string {
	if duration < 0 {
		duration = -duration
	}
	if duration < time.Second {
		return fmt.Sprintf("%d ms", duration.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", duration.Seconds())
}

func formatSignedClockDelta(duration time.Duration) string {
	sign := "+"
	if duration < 0 {
		sign = "-"
		duration = -duration
	}
	return sign + formatClockDuration(duration)
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
	stream := flags.Bool("stream", false, "print progressive adapter text to stderr while waiting")
	artifactDir := flags.String("artifacts", "", "save returned images and files in this directory")
	sealed := flags.Bool("e2ee", false, "encrypt payload for the selected worker")
	idempotencyKey := flags.String("idempotency-key", "", "deduplicate an exact producer submission retry")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("--file is required")
	}
	if *idempotencyKey != "" {
		if err := cluster.ValidateIdempotencyKey(*idempotencyKey); err != nil {
			return err
		}
		if *sealed {
			return errors.New("--idempotency-key cannot be combined with --e2ee in the native client because each invocation creates a fresh one-time reservation; exact prepared sealed retries remain available through the cluster API")
		}
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *token == "" {
		*token = clusterClientToken(cfg, "")
	}
	const maximumClusterSubmissionFileBytes = ((cluster.MaximumJobPayloadBytes+16)*4+2)/3 + (64 << 10)
	raw, err := readRegularFileBounded(*file, maximumClusterSubmissionFileBytes)
	if err != nil {
		return fmt.Errorf("cluster job: %w", err)
	}
	var input cluster.SubmitRequest
	if err := json.Unmarshal(raw, &input); err != nil {
		return err
	}
	shared := ""
	encryptionContext := cluster.EncryptionContext{}
	if *sealed {
		var reservation cluster.AssignmentResponse
		assignmentRequest := cluster.AssignmentRequest{TenantID: input.TenantID, Requirements: input.Requirements}
		if err := clusterPOST(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/assign", *token, assignmentRequest, &reservation); err != nil {
			return err
		}
		encryptionContext, err = cluster.ValidateAssignmentResponse(assignmentRequest, reservation, time.Now().UTC())
		if err != nil {
			return err
		}
		envelope, sharedKey, err := cluster.SealFor(reservation.Assignment.PublicKey, input.Payload, cluster.JobAAD(encryptionContext))
		if err != nil {
			return err
		}
		shared = sharedKey
		input.ID = reservation.Assignment.JobID
		input.TenantID = reservation.Assignment.TenantID
		input.Requirements = reservation.Assignment.Requirements
		input.Payload = nil
		input.Sealed = envelope
		input.AssignmentID = reservation.Assignment.ID
		input.AssignmentSecret = reservation.Secret
	}
	var job cluster.Job
	headers := http.Header{}
	if *idempotencyKey != "" {
		headers.Set("Idempotency-Key", *idempotencyKey)
	}
	responseHeaders, err := clusterPOSTHeaders(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/jobs?compact=1", *token, input, &job, headers)
	if err != nil {
		return err
	}
	if *sealed {
		if err := cluster.ValidateEncryptedJobContext(encryptionContext, job); err != nil {
			return err
		}
	}
	if responseHeaders.Get("Idempotency-Replayed") == "true" {
		fmt.Println("Queued:", job.ID, "(existing; duplicate submission suppressed)")
	} else {
		fmt.Println("Queued:", job.ID)
	}
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
		if err := clusterGET(ctx, clusterBaseURL(cfg)+"/v1/cluster/jobs/"+url.PathEscape(job.ID)+"?compact=1", *token, &job); err != nil {
			return err
		}
		if *sealed {
			if err := cluster.ValidateEncryptedJobContext(encryptionContext, job); err != nil {
				return err
			}
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
				raw, err := cluster.OpenResponse(shared, job.SealedResult, cluster.ResultAAD(encryptionContext))
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
	raw, err := readRegularFileBounded(*tokenFile, 32<<10)
	if err != nil {
		return err
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
	raw, err := readClusterAPIResponse(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, output)
}

func clusterPOST(ctx context.Context, target, token string, input, output interface{}) error {
	_, err := clusterPOSTHeaders(ctx, target, token, input, output, nil)
	return err
}

func clusterPOSTHeaders(ctx context.Context, target, token string, input, output interface{}, headers http.Header) (http.Header, error) {
	raw, _ := json.Marshal(input)
	// #nosec G704 -- target is the operator-configured relay URL plus a fixed
	// cluster API path; connecting to that remote relay is this CLI's purpose.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	// #nosec G704 -- see the operator-owned relay boundary above.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readClusterAPIResponse(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.Header.Clone(), fmt.Errorf("relay returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if output != nil {
		if err := json.Unmarshal(body, output); err != nil {
			return resp.Header.Clone(), err
		}
	}
	return resp.Header.Clone(), nil
}

const maximumClusterAPIResponseBytes = cluster.MaximumJobResultWireBytes + (2 << 20)

func readClusterAPIResponse(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maximumClusterAPIResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maximumClusterAPIResponseBytes {
		return nil, fmt.Errorf("relay response exceeds %d bytes", maximumClusterAPIResponseBytes)
	}
	return body, nil
}

func formatBytes(value uint64) string {
	if value == 0 {
		return "unknown"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}

func formatInt64Bytes(value int64) string {
	if value < 0 {
		return "unknown"
	}
	return formatBytes(uint64(value))
}

func formatFloat64Bytes(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value >= float64(math.MaxUint64) {
		return "unknown"
	}
	return formatBytes(uint64(value))
}

func durationFromUint64Milliseconds(value uint64) time.Duration {
	maximum := uint64(math.MaxInt64 / int64(time.Millisecond))
	if value > maximum {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(value) * time.Millisecond
}

func durationFromInt64Milliseconds(value int64) time.Duration {
	if value <= 0 {
		return 0
	}
	maximum := int64(math.MaxInt64 / int64(time.Millisecond))
	if value > maximum {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(value) * time.Millisecond
}

func openAdapter(target string) error {
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

func localDashboardTarget(cfg config.Config) string {
	return baseURL(cfg) + "/"
}

func clusterDashboardTarget(cfg config.Config) string {
	return clusterBaseURL(cfg) + "/dashboard/"
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
	raw, err := readRegularFileBounded(filepath.Join(dir, "payload.json"), 1<<20)
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
		name, err = readOptionalTextBounded(filepath.Join(dir, "name.txt"), 200000)
		if err != nil {
			return bridge.Job{}, err
		}
	}
	if message == "" {
		message, err = readOptionalTextBounded(filepath.Join(dir, "message.txt"), 200000)
		if err != nil {
			return bridge.Job{}, err
		}
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
	imagePath, findErr := firstRegularImageFile(dir)
	if findErr != nil {
		return bridge.Job{}, findErr
	}
	if imagePath != "" {
		imageRaw, readErr := readRegularFileBounded(imagePath, 8<<20)
		if readErr != nil {
			return bridge.Job{}, fmt.Errorf("review image: %w", readErr)
		}
		job.ImageBase64 = base64.StdEncoding.EncodeToString(imageRaw)
		job.ImageMediaType = mime.TypeByExtension(filepath.Ext(imagePath))
		if job.ImageMediaType == "" {
			job.ImageMediaType = http.DetectContentType(imageRaw)
		}
	}
	return job, nil
}

func readRegularFileBounded(path string, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, errors.New("file limit must not be negative")
	}
	// #nosec G703 -- path is an explicit operator-selected CLI input; it is size-bounded and must be a regular file below.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path must be a regular file")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return raw, nil
}

func readOptionalTextBounded(path string, limit int64) (string, error) {
	raw, err := readRegularFileBounded(path, limit)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func firstRegularImageFile(dir string) (string, error) {
	directory, err := os.Open(dir)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	for {
		entries, readErr := directory.ReadDir(64)
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(strings.ToLower(name), "image.") || strings.ContainsAny(name, `/\\`) {
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return "", infoErr
			}
			if info.Mode().IsRegular() {
				return filepath.Join(dir, name), nil
			}
		}
		if errors.Is(readErr, io.EOF) {
			return "", nil
		}
		if readErr != nil {
			return "", readErr
		}
	}
}

func baseURL(cfg config.Config) string {
	return "http://" + cfg.Server.Listen
}

func defaultConfigPath() string {
	if env := os.Getenv("CONTEXTBRIDGE_CONFIG"); env != "" {
		return env
	}
	if executable, err := os.Executable(); err == nil {
		if adjacent := adjacentConfigPath(executable); adjacent != "" {
			return adjacent
		}
	}
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
			return filepath.Join(appData, "ContextBridge", "config.yml")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "contextbridge", "config.yml")
}

func adjacentConfigPath(executable string) string {
	path := filepath.Join(filepath.Dir(executable), "config.yml")
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	return ""
}
