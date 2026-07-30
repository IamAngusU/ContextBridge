package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/llamaruntime"
	"github.com/IamAngusU/ContextBridge/internal/modelregistry"
	"github.com/IamAngusU/ContextBridge/internal/systeminfo"
)

type RuntimeStatus struct {
	Hardware systeminfo.Snapshot     `json:"hardware"`
	Engines  map[string]EngineStatus `json:"engines"`
	Models   []modelregistry.Entry   `json:"models"`
}

type EngineStatus struct {
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	State     string         `json:"state"`
	URL       string         `json:"url,omitempty"`
	Model     string         `json:"model,omitempty"`
	Affinity  string         `json:"affinity,omitempty"`
	Warning   string         `json:"warning,omitempty"`
	Version   string         `json:"version,omitempty"`
	Models    []RuntimeModel `json:"models,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
	Restarts  int            `json:"restarts"`
}

type RuntimeModel struct {
	Name      string `json:"name"`
	Size      int64  `json:"size_bytes,omitempty"`
	VRAM      int64  `json:"vram_bytes,omitempty"`
	Affinity  string `json:"affinity,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Loaded    bool   `json:"loaded"`
}

type RuntimeManager struct {
	cfg      config.Config
	logger   *log.Logger
	mu       sync.RWMutex
	hardware systeminfo.Snapshot
	engines  map[string]EngineStatus
}

func NewRuntimeManager(cfg config.Config, logger *log.Logger) *RuntimeManager {
	manager := &RuntimeManager{cfg: cfg, logger: logger, engines: map[string]EngineStatus{}}
	for name, engine := range cfg.Engines {
		manager.engines[name] = EngineStatus{Name: name, Type: engine.Type, State: "checking", URL: engineURL(engine), Model: engine.Model, UpdatedAt: time.Now().UTC()}
	}
	if _, ok := manager.engines["ollama"]; !ok {
		engine, _ := cfg.Engine("ollama")
		manager.engines["ollama"] = EngineStatus{Name: "ollama", Type: "ollama", State: "checking", URL: engine.URL, Model: engine.Model, UpdatedAt: time.Now().UTC()}
	}
	return manager
}

func (m *RuntimeManager) Run(ctx context.Context) {
	m.refreshHardware(ctx)
	go m.refreshEngineLoop(ctx)
	go func() {
		ticker := time.NewTicker(time.Duration(m.cfg.Runtime.HardwareRefreshSeconds) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.refreshHardware(ctx)
			}
		}
	}()
	for name, engine := range m.cfg.Engines {
		if engine.Type == "llama_cpp" && engine.AutoStart {
			go m.supervise(ctx, name, engine)
		}
	}
}

func (m *RuntimeManager) Snapshot(ctx context.Context) RuntimeStatus {
	_ = ctx
	engines := map[string]EngineStatus{}
	m.mu.RLock()
	hardware := m.hardware
	for name, status := range m.engines {
		engines[name] = status
	}
	m.mu.RUnlock()
	return RuntimeStatus{Hardware: hardware, Engines: engines, Models: modelregistry.List(m.cfg)}
}

func (m *RuntimeManager) refreshEngineLoop(ctx context.Context) {
	m.refreshExternalEngines(ctx)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refreshExternalEngines(ctx)
		}
	}
}

func (m *RuntimeManager) refreshExternalEngines(parent context.Context) {
	engines := m.cfg.Engines
	if _, ok := engines["ollama"]; !ok {
		engine, _ := m.cfg.Engine("ollama")
		engines = make(map[string]config.Engine, len(m.cfg.Engines)+1)
		for name, configured := range m.cfg.Engines {
			engines[name] = configured
		}
		engines["ollama"] = engine
	}
	for name, engine := range engines {
		if engine.Type == "ollama" {
			ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
			status := ollamaStatus(ctx, name, engine)
			cancel()
			m.setEngine(status)
			continue
		}
		if engine.Type == "llama_cpp" && !engine.AutoStart {
			status := EngineStatus{Name: name, Type: engine.Type, State: "stopped", URL: engineURL(engine), Model: engine.Model, UpdatedAt: time.Now().UTC()}
			ctx, cancel := context.WithTimeout(parent, time.Second)
			if healthy(ctx, status.URL) {
				status.State = "online"
				status.Affinity = "external"
			}
			cancel()
			m.setEngine(status)
		}
	}
}

func (m *RuntimeManager) refreshHardware(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	snapshot := systeminfo.Detect(ctx)
	m.mu.Lock()
	m.hardware = snapshot
	m.mu.Unlock()
}

func (m *RuntimeManager) supervise(ctx context.Context, name string, engine config.Engine) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if healthy(ctx, engineURL(engine)) {
			m.setEngine(EngineStatus{Name: name, Type: engine.Type, State: "online", URL: engineURL(engine), Model: engine.Model, Affinity: "external", UpdatedAt: time.Now().UTC()})
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				continue
			}
		}
		if err := m.runLlama(ctx, name, engine, false); err != nil && engine.GPU == "prefer" && ctx.Err() == nil {
			m.logger.Printf("engine %s GPU start failed, retrying on CPU: %v", name, err)
			if err = m.runLlama(ctx, name, engine, true); err != nil {
				m.markEngineError(name, engine, "GPU and CPU start failed: "+err.Error())
			}
		} else if err != nil && ctx.Err() == nil {
			m.markEngineError(name, engine, err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (m *RuntimeManager) runLlama(ctx context.Context, name string, engine config.Engine, forceCPU bool) error {
	executable, err := llamaExecutable(engine.Executable, filepath.Join(m.cfg.Storage.Directory, "runtime", "llama.cpp"))
	if err != nil {
		return err
	}
	modelPath, err := modelregistry.Path(m.cfg, engine.Model)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(engine.Listen)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	args := []string{"--host", host, "--port", port, "--model", modelPath}
	model := m.cfg.Models[engine.Model]
	if model.ProjectorFile != "" {
		projector := filepath.Join(filepath.Dir(modelPath), model.ProjectorFile)
		if _, statErr := os.Stat(projector); statErr == nil {
			args = append(args, "--mmproj", projector)
		}
	}
	if engine.Mode == "embedding" {
		args = append(args, "--embedding")
		pooling := engine.Pooling
		if pooling == "" {
			pooling = "mean"
		}
		args = append(args, "--pooling", pooling)
	}
	if forceCPU || engine.GPU == "off" {
		args = append(args, "--n-gpu-layers", "0")
	} else {
		args = append(args, "--n-gpu-layers", "all")
	}
	args = append(args, engine.Args...)
	logs := filepath.Join(m.cfg.Storage.Directory, "logs")
	if err := os.MkdirAll(logs, 0700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(logs, "engine-"+storageID(name)+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		return err
	}
	affinity := "GPU requested"
	warning := ""
	if forceCPU || engine.GPU == "off" {
		affinity = "CPU"
	}
	if forceCPU && engine.GPU == "prefer" {
		affinity = "CPU fallback"
		warning = "GPU startup failed. ContextBridge used the configured CPU fallback."
	}
	m.setEngine(EngineStatus{Name: name, Type: engine.Type, State: "starting", URL: engineURL(engine), Model: engine.Model, Affinity: affinity, Warning: warning, UpdatedAt: time.Now().UTC()})
	ready := make(chan bool, 1)
	go func() {
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if healthy(ctx, engineURL(engine)) {
				ready <- true
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		ready <- false
	}()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case ok := <-ready:
		if !ok {
			_ = command.Process.Kill()
			<-wait
			return fmt.Errorf("engine did not become healthy; see %s", logFile.Name())
		}
		status := EngineStatus{Name: name, Type: engine.Type, State: "online", URL: engineURL(engine), Model: engine.Model, Affinity: affinity, Warning: warning, UpdatedAt: time.Now().UTC()}
		m.mu.RLock()
		status.Restarts = m.engines[name].Restarts
		m.mu.RUnlock()
		m.setEngine(status)
		m.logger.Printf("engine %s is online at %s using %s", name, status.URL, affinity)
		return <-wait
	case err := <-wait:
		return fmt.Errorf("engine exited during startup: %w", err)
	case <-ctx.Done():
		_ = command.Process.Kill()
		return ctx.Err()
	}
}

func (m *RuntimeManager) markEngineError(name string, engine config.Engine, warning string) {
	m.mu.Lock()
	status := m.engines[name]
	status.Name = name
	status.Type = engine.Type
	status.State = "error"
	status.URL = engineURL(engine)
	status.Model = engine.Model
	status.Warning = warning
	status.UpdatedAt = time.Now().UTC()
	status.Restarts++
	m.engines[name] = status
	m.mu.Unlock()
}

func (m *RuntimeManager) setEngine(status EngineStatus) {
	m.mu.Lock()
	m.engines[status.Name] = status
	m.mu.Unlock()
}

func llamaExecutable(value, runtimeDirectory string) (string, error) {
	if value != "" && value != "auto" {
		if path, err := exec.LookPath(value); err == nil {
			return path, nil
		}
		if stat, err := os.Stat(value); err == nil && !stat.IsDir() {
			return value, nil
		}
		return "", fmt.Errorf("llama.cpp executable not found: %s", value)
	}
	for _, name := range []string{"llama-server", "llama-server.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	if path := llamaruntime.Current(runtimeDirectory); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("llama-server is not installed or on PATH")
}

func engineURL(engine config.Engine) string {
	if engine.URL != "" {
		return strings.TrimRight(engine.URL, "/")
	}
	if engine.Listen != "" {
		return "http://" + engine.Listen
	}
	return ""
}

func healthy(parent context.Context, base string) bool {
	if base == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, 800*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func ollamaStatus(parent context.Context, name string, engine config.Engine) EngineStatus {
	status := EngineStatus{Name: name, Type: "ollama", State: "offline", URL: engine.URL, Model: engine.Model, UpdatedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(parent, 900*time.Millisecond)
	defer cancel()
	base := strings.TrimRight(engine.URL, "/")
	var version struct {
		Version string `json:"version"`
	}
	if !getJSON(ctx, base+"/api/version", &version) {
		return status
	}
	status.State = "online"
	status.Version = version.Version
	var cached struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if getJSON(ctx, base+"/api/tags", &cached) {
		for _, model := range cached.Models {
			status.Models = append(status.Models, RuntimeModel{Name: model.Name, Size: model.Size})
		}
	}
	var running struct {
		Models []struct {
			Name      string `json:"name"`
			Size      int64  `json:"size"`
			SizeVRAM  int64  `json:"size_vram"`
			ExpiresAt string `json:"expires_at"`
		} `json:"models"`
	}
	if getJSON(ctx, base+"/api/ps", &running) {
		for _, model := range running.Models {
			affinity := "CPU"
			if model.SizeVRAM > 0 && model.SizeVRAM >= model.Size {
				affinity = "GPU"
			} else if model.SizeVRAM > 0 {
				affinity = "GPU and CPU"
			}
			updated := false
			for index := range status.Models {
				if status.Models[index].Name == model.Name {
					status.Models[index].Size = model.Size
					status.Models[index].VRAM = model.SizeVRAM
					status.Models[index].Affinity = affinity
					status.Models[index].ExpiresAt = model.ExpiresAt
					status.Models[index].Loaded = true
					updated = true
					break
				}
			}
			if !updated {
				status.Models = append(status.Models, RuntimeModel{Name: model.Name, Size: model.Size, VRAM: model.SizeVRAM, Affinity: affinity, ExpiresAt: model.ExpiresAt, Loaded: true})
			}
		}
	}
	status.Affinity = "idle"
	for _, model := range status.Models {
		if model.Loaded {
			status.Affinity = model.Affinity
			break
		}
	}
	return status
}

func getJSON(ctx context.Context, url string, target interface{}) bool {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(target) == nil
}
