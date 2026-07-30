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
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/llamaruntime"
	"github.com/IamAngusU/ContextBridge/internal/modelregistry"
	"github.com/IamAngusU/ContextBridge/internal/systeminfo"
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
	case "hardware":
		err = hardwareCommand(os.Args[2:])
	case "models":
		err = modelsCommand(os.Args[2:])
	case "pull":
		err = pullCommand(os.Args[2:])
	case "runtime":
		err = runtimeCommand(os.Args[2:])
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
  contextbridge submit --file job.json [--config path]
  contextbridge review --job-dir path [--config path]
  contextbridge health [--config path]
  contextbridge dashboard [--config path] [--no-open]
  contextbridge status [--config path] [--json]
  contextbridge hardware [--json]
  contextbridge models [--config path] [--json]
  contextbridge pull [--config path] MODEL
  contextbridge runtime install [--config path] llama.cpp
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Printf("version %s", version)
	logger.Printf("routes: %d, browser profiles: %d", len(cfg.Routes), len(cfg.BrowserProfiles))
	return server.Run(ctx)
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
