package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

var performanceConcurrencies = []int{1, 4, 16, 64}

type performanceReport struct {
	SchemaVersion int                             `json:"schema_version"`
	GeneratedAt   time.Time                       `json:"generated_at"`
	Version       string                          `json:"contextbridge_version"`
	Environment   performanceEnvironment          `json:"environment"`
	Settings      performanceSettings             `json:"settings"`
	Operations    []cluster.BridgeOperationMetric `json:"operations"`
	Resources     performanceResources            `json:"resources"`
	Unavailable   []performanceUnavailableMetric  `json:"unavailable_metrics"`
	Excludes      []string                        `json:"excludes"`
	Warnings      []string                        `json:"warnings,omitempty"`
}

type performanceUnavailableMetric struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type performanceEnvironment struct {
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	GoVersion string `json:"go_version"`
	CPUs      int    `json:"logical_cpus"`
}

type performanceSettings struct {
	Samples       int           `json:"samples_per_concurrency"`
	Warmup        int           `json:"warmup_per_operation"`
	Concurrencies []int         `json:"concurrencies"`
	DatabaseJobs  int           `json:"database_jobs"`
	IdleDuration  time.Duration `json:"idle_duration_ns"`
}

type performanceResources struct {
	Files     performanceFileSizes         `json:"files"`
	IdleRelay performanceIdleRelay         `json:"idle_relay"`
	Heartbeat []performanceHeartbeat       `json:"representative_heartbeats"`
	Database  cluster.DatabaseGrowthMetric `json:"database"`
}

type performanceFileSizes struct {
	BinaryPath  string `json:"binary_path,omitempty"`
	BinaryBytes int64  `json:"binary_bytes,omitempty"`
}

type performanceIdleRelay struct {
	DurationMilliseconds       int64    `json:"duration_ms"`
	ProcessCPUTimeMilliseconds float64  `json:"process_cpu_time_ms,omitempty"`
	CPUCorePercent             *float64 `json:"cpu_core_percent,omitempty"`
	CPUHostPercent             *float64 `json:"cpu_host_percent,omitempty"`
	ResidentBytes              uint64   `json:"resident_bytes,omitempty"`
	ResidentKind               string   `json:"resident_kind,omitempty"`
	GoHeapAllocBytes           uint64   `json:"go_heap_alloc_bytes"`
	GoHeapInUseBytes           uint64   `json:"go_heap_in_use_bytes"`
	GoMemorySysBytes           uint64   `json:"go_memory_sys_bytes"`
}

type performanceHeartbeat struct {
	Name                  string `json:"name"`
	PayloadBytes          int    `json:"payload_bytes"`
	IntervalMilliseconds  int64  `json:"interval_ms"`
	PayloadBytesPerMinute int64  `json:"payload_bytes_per_minute"`
	PayloadBytesPerHour   int64  `json:"payload_bytes_per_hour"`
	Scope                 string `json:"scope"`
}

func performanceCommand(args []string) error {
	flags := flag.NewFlagSet("benchmark", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print the complete machine-readable report")
	samples := flags.Int("samples", 128, "measured operations at each concurrency")
	warmup := flags.Int("warmup", 8, "untimed warmup operations for each operation")
	databaseJobs := flags.Int("database-jobs", 1000, "fresh cancelled jobs used for Bolt file-growth measurement")
	idleDuration := flags.Duration("idle-duration", 2*time.Second, "benchmark-process CPU/RAM window while hosting one fresh idle relay")
	binaryPath := flags.String("binary", "", "binary to size; default is the running executable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("benchmark accepts flags only")
	}
	if *samples < 1 || *samples > 100000 {
		return errors.New("--samples must be between 1 and 100000")
	}
	if *warmup < 0 || *warmup > 10000 {
		return errors.New("--warmup must be between 0 and 10000")
	}
	if *databaseJobs < 1 || *databaseJobs > 100000 {
		return errors.New("--database-jobs must be between 1 and 100000")
	}
	if *idleDuration < 250*time.Millisecond || *idleDuration > time.Minute {
		return errors.New("--idle-duration must be between 250ms and 1m")
	}

	ctx, stop := signalContext()
	defer stop()
	concurrencies := effectivePerformanceConcurrencies(*samples)
	report := performanceReport{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().UTC(),
		Version:       version,
		Environment: performanceEnvironment{
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), CPUs: runtime.NumCPU(),
		},
		Settings: performanceSettings{
			Samples: *samples, Warmup: *warmup, Concurrencies: concurrencies,
			DatabaseJobs: *databaseJobs, IdleDuration: *idleDuration,
		},
		Excludes: []string{
			"model and provider inference",
			"adapter rendering and website latency",
			"Internet and cross-device network latency",
			"TLS, TCP, HTTP, and WebSocket framing from heartbeat byte estimates",
		},
		Unavailable: []performanceUnavailableMetric{
			{Name: "ram_per_active_inference_job", Reason: "the benchmark deliberately starts no provider, adapter, or model inference; process-wide adapter/model memory cannot be attributed honestly to one ContextBridge job"},
			{Name: "os_disk_write_bytes_per_job", Reason: "fresh Bolt allocated-file growth is reported instead; filesystem cache, journaling, write amplification, and device counters are platform-specific"},
			{Name: "wire_network_bytes_per_job", Reason: "job payloads and responses are caller-dependent and transport framing depends on HTTP/WebSocket/TLS; fixed typed heartbeat JSON is reported separately"},
		},
	}

	idle, warning, err := measureIdleRelay(ctx, *idleDuration)
	if err != nil {
		return err
	}
	report.Resources.IdleRelay = idle
	if warning != "" {
		report.Warnings = append(report.Warnings, warning)
	}

	measurement, err := cluster.MeasureBridgeOnly(ctx, cluster.BridgeMeasurementOptions{
		Samples: *samples, Warmup: *warmup, Concurrencies: concurrencies, DatabaseJobs: *databaseJobs,
	})
	if err != nil {
		return err
	}
	report.Operations = measurement.Operations
	report.Resources.Database = measurement.Database
	artifactMetrics, err := bridge.MeasureArtifactVerification(ctx, *samples, *warmup, concurrencies)
	if err != nil {
		return err
	}
	report.Operations = append(report.Operations, artifactMetrics...)

	files, warnings, err := measurePerformanceFileSizes(*binaryPath)
	if err != nil {
		return err
	}
	report.Resources.Files = files
	report.Warnings = append(report.Warnings, warnings...)
	heartbeats, err := representativeHeartbeatMetrics()
	if err != nil {
		return err
	}
	report.Resources.Heartbeat = heartbeats

	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	printPerformanceReport(os.Stdout, report)
	return nil
}

func effectivePerformanceConcurrencies(samples int) []int {
	concurrencies := make([]int, 0, len(performanceConcurrencies))
	for _, concurrency := range performanceConcurrencies {
		if concurrency <= samples {
			concurrencies = append(concurrencies, concurrency)
		}
	}
	return concurrencies
}

// signalContext is a small seam kept separate so the measurement functions
// remain unit-testable without sending provider work or installing handlers.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func measureIdleRelay(parent context.Context, duration time.Duration) (performanceIdleRelay, string, error) {
	directory, err := os.MkdirTemp("", "contextbridge-measure-idle-")
	if err != nil {
		return performanceIdleRelay{}, "", err
	}
	defer os.RemoveAll(directory)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return performanceIdleRelay{}, "", err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return performanceIdleRelay{}, "", err
	}
	relay, err := cluster.NewRelay(cluster.RelayConfig{
		Listen: address, Database: filepath.Join(directory, "relay.db"),
		AdminToken:   "measure_idle_012345678901234567890123456789",
		AllowedTasks: []string{"generation"}, DispatchEvery: 250 * time.Millisecond,
	}, log.New(io.Discard, "", 0))
	if err != nil {
		return performanceIdleRelay{}, "", err
	}
	defer relay.Close()
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	if err := waitForMeasurementRelay(ctx, "http://"+address+"/health", done); err != nil {
		cancel()
		return performanceIdleRelay{}, "", err
	}
	runtime.GC()
	before, beforeErr := readProcessResourcePoint()
	started := time.Now()
	timer := time.NewTimer(duration)
	select {
	case <-parent.Done():
		timer.Stop()
		cancel()
		return performanceIdleRelay{}, "", parent.Err()
	case <-timer.C:
	}
	wall := time.Since(started)
	after, afterErr := readProcessResourcePoint()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			return performanceIdleRelay{}, "", runErr
		}
	case <-time.After(10 * time.Second):
		return performanceIdleRelay{}, "", errors.New("measurement relay did not stop")
	}
	result := performanceIdleRelay{
		DurationMilliseconds: wall.Milliseconds(),
		GoHeapAllocBytes:     memory.HeapAlloc, GoHeapInUseBytes: memory.HeapInuse, GoMemorySysBytes: memory.Sys,
	}
	if beforeErr != nil || afterErr != nil {
		failure := beforeErr
		if failure == nil {
			failure = afterErr
		}
		return result, "OS process CPU/RSS sample unavailable: " + failure.Error(), nil
	}
	if after.CPUTimeNanoseconds >= before.CPUTimeNanoseconds && wall > 0 {
		cpuDelta := after.CPUTimeNanoseconds - before.CPUTimeNanoseconds
		result.ProcessCPUTimeMilliseconds = float64(cpuDelta) / float64(time.Millisecond)
		corePercent := float64(cpuDelta) / float64(wall) * 100
		hostPercent := corePercent / float64(max(1, runtime.NumCPU()))
		result.CPUCorePercent = &corePercent
		result.CPUHostPercent = &hostPercent
	}
	result.ResidentBytes = after.ResidentBytes
	result.ResidentKind = after.ResidentKind
	return result, "", nil
}

func waitForMeasurementRelay(ctx context.Context, healthURL string, done <-chan error) error {
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err == nil {
				return errors.New("measurement relay stopped before measurement")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("measurement relay did not become ready")
		case <-ticker.C:
			response, err := client.Get(healthURL)
			if err == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

func measurePerformanceFileSizes(requestedBinary string) (performanceFileSizes, []string, error) {
	result := performanceFileSizes{}
	warnings := []string{}
	binary := strings.TrimSpace(requestedBinary)
	if binary == "" {
		var err error
		binary, err = os.Executable()
		if err != nil {
			return result, warnings, err
		}
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		return result, warnings, err
	}
	info, err := os.Stat(binary)
	if err != nil {
		return result, warnings, fmt.Errorf("measure binary size: %w", err)
	}
	if !info.Mode().IsRegular() {
		return result, warnings, errors.New("--binary must name a regular file")
	}
	result.BinaryPath, result.BinaryBytes = binary, info.Size()

	return result, warnings, nil
}

func representativeHeartbeatMetrics() ([]performanceHeartbeat, error) {
	fixedTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	adapterPayload, err := json.Marshal(bridge.AdapterClientStatus{
		Connected: true, State: "waiting", Ready: true, AdapterVersion: "measurement", Adapter: "test", ActiveEndpoints: 1,
		Endpoints: []bridge.AdapterEndpointStatus{{
			ID: 1, Profile: "test", State: "waiting", CurrentModel: "measured-model",
		}},
	})
	if err != nil {
		return nil, err
	}
	workerPayload, err := json.Marshal(cluster.WireMessage{
		Version: cluster.ProtocolVersion, Type: "heartbeat",
		Capabilities: &cluster.Capabilities{
			ClockTime: fixedTime, OS: "measurement-os", Architecture: "amd64", CPU: "measurement-cpu", CPUCores: 8,
			MemoryTotal: 32 << 30, MemoryFree: 16 << 30,
			GPUs:      []cluster.GPUCapability{{Name: "measurement-gpu", Backend: "cuda", MemoryTotal: 12 << 30, MemoryFree: 8 << 30}},
			Models:    []cluster.ModelCapability{{Name: "measurement-model", Provider: "ollama", Loaded: true, Tasks: []string{"generation"}}},
			Providers: []string{"ollama"}, Tasks: []string{"generation"}, AutomaticTasks: map[string][]string{"ollama": {"generation"}},
			MaxConcurrent: 4,
		},
	})
	if err != nil {
		return nil, err
	}
	const interval = 5 * time.Second
	metric := func(name, scope string, size int) performanceHeartbeat {
		return performanceHeartbeat{
			Name: name, Scope: scope, PayloadBytes: size, IntervalMilliseconds: interval.Milliseconds(),
			PayloadBytesPerMinute: int64(size) * int64(time.Minute/interval),
			PayloadBytesPerHour:   int64(size) * int64(time.Hour/interval),
		}
	}
	return []performanceHeartbeat{
		metric("adapter_one_idle_endpoint", "representative JSON body; actual size scales with advertised endpoints and bounded diagnostics", len(adapterPayload)),
		metric("worker_one_gpu_one_model", "representative WebSocket JSON message; actual size scales with capabilities", len(workerPayload)),
	}, nil
}

func printPerformanceReport(output io.Writer, report performanceReport) {
	fmt.Fprintf(output, "ContextBridge bridge-only measurement · %s · %s/%s · %d CPUs\n", report.Version, report.Environment.GOOS, report.Environment.GOARCH, report.Environment.CPUs)
	fmt.Fprintln(output, "Provider/model inference, adapter rendering, Internet latency, and transport framing are excluded.")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Latency and throughput")
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "OPERATION\tC\tSAMPLES\tBATCH\tP50\tP95\tP99\tOPS/S")
	for _, operation := range report.Operations {
		fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%.1f us\t%.1f us\t%.1f us\t%.1f\n",
			operation.Name, operation.Concurrency, operation.Samples, operation.OperationsPerSample,
			operation.P50Microseconds, operation.P95Microseconds, operation.P99Microseconds, operation.ThroughputPerSec)
	}
	writer.Flush()
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Resource footprint")
	fmt.Fprintf(output, "  binary                         %s\n", formatInt64Bytes(report.Resources.Files.BinaryBytes))
	idle := report.Resources.IdleRelay
	fmt.Fprintf(output, "  benchmark + idle relay window   %s\n", durationFromInt64Milliseconds(idle.DurationMilliseconds))
	if idle.CPUCorePercent != nil && idle.CPUHostPercent != nil {
		fmt.Fprintf(output, "  benchmark + idle relay CPU      %.3f%% of one core · %.3f%% of host capacity\n", *idle.CPUCorePercent, *idle.CPUHostPercent)
	}
	if idle.ResidentBytes > 0 {
		fmt.Fprintf(output, "  benchmark + idle relay memory   %s · %s\n", formatBytes(idle.ResidentBytes), idle.ResidentKind)
	}
	fmt.Fprintf(output, "  benchmark + idle relay heap/sys %s / %s\n", formatBytes(idle.GoHeapAllocBytes), formatBytes(idle.GoMemorySysBytes))
	database := report.Resources.Database
	fmt.Fprintf(output, "  fresh Bolt growth               %s for %d cancelled jobs · normalized %s/1000\n",
		formatInt64Bytes(database.GrowthBytes), database.Jobs, formatFloat64Bytes(database.GrowthPer1000JobBytes))
	for _, heartbeat := range report.Resources.Heartbeat {
		fmt.Fprintf(output, "  %-30s %d B/heartbeat · %d B/min · %s/hour at %.0fs\n",
			heartbeat.Name, heartbeat.PayloadBytes, heartbeat.PayloadBytesPerMinute,
			formatInt64Bytes(heartbeat.PayloadBytesPerHour), float64(heartbeat.IntervalMilliseconds)/1000)
	}
	if len(report.Unavailable) > 0 {
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Unavailable without inventing a workload")
		for _, metric := range report.Unavailable {
			fmt.Fprintf(output, "  %-30s %s\n", metric.Name, metric.Reason)
		}
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Warnings")
		for _, warning := range report.Warnings {
			fmt.Fprintln(output, "  -", warning)
		}
	}
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Use --json for the complete auditable report, paths, settings, scopes, and exclusions.")
}
