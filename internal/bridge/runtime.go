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
	"github.com/IamAngusU/ContextBridge/internal/resourcepacks"
	"github.com/IamAngusU/ContextBridge/internal/systeminfo"
)

type RuntimeStatus struct {
	Hardware systeminfo.Snapshot     `json:"hardware"`
	Engines  map[string]EngineStatus `json:"engines"`
	Models   []modelregistry.Entry   `json:"models"`
	Packs    []resourcepacks.Pack    `json:"resource_packs,omitempty"`
}

type EngineStatus struct {
	Name           string         `json:"name"`
	Type           string         `json:"type"`
	Remote         bool           `json:"remote,omitempty"`
	State          string         `json:"state"`
	URL            string         `json:"url,omitempty"`
	Model          string         `json:"model,omitempty"`
	Affinity       string         `json:"affinity,omitempty"`
	LifecycleOwner string         `json:"lifecycle_owner,omitempty"`
	Warning        string         `json:"warning,omitempty"`
	Version        string         `json:"version,omitempty"`
	Models         []RuntimeModel `json:"models,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at"`
	Restarts       int            `json:"restarts"`
}

type RuntimeModel struct {
	Name                 string   `json:"name"`
	Size                 int64    `json:"size_bytes,omitempty"`
	VRAM                 int64    `json:"vram_bytes,omitempty"`
	Affinity             string   `json:"affinity,omitempty"`
	ExpiresAt            string   `json:"expires_at,omitempty"`
	Available            bool     `json:"available"`
	Loaded               bool     `json:"loaded"`
	Capabilities         []string `json:"capabilities,omitempty"`
	CapabilitiesVerified bool     `json:"capabilities_verified"`
	CapabilitySource     string   `json:"capability_source,omitempty"`
	ContextWindowTokens  int      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens      int      `json:"max_output_tokens,omitempty"`
	MaxInputImages       int      `json:"max_input_images,omitempty"`
	MaxImageBytes        int64    `json:"max_image_bytes,omitempty"`
	MaxTotalImageBytes   int64    `json:"max_total_image_bytes,omitempty"`
	ImageMediaTypes      []string `json:"image_media_types,omitempty"`
	LimitsVerified       bool     `json:"limits_verified"`
	LimitSource          string   `json:"limit_source,omitempty"`
}

type RuntimeManager struct {
	cfg      config.Config
	logger   *log.Logger
	mu       sync.RWMutex
	hardware systeminfo.Snapshot
	engines  map[string]EngineStatus
	packs    []resourcepacks.Pack
}

func NewRuntimeManager(cfg config.Config, logger *log.Logger) *RuntimeManager {
	manager := &RuntimeManager{cfg: cfg, logger: logger, engines: map[string]EngineStatus{}}
	for name, engine := range cfg.Engines {
		manager.engines[name] = EngineStatus{Name: name, Type: engine.Type, State: "checking", URL: engineURL(engine), Model: engine.Model, LifecycleOwner: configuredLifecycleOwner(engine), UpdatedAt: time.Now().UTC()}
	}
	if _, ok := manager.engines["ollama"]; !ok && needsImplicitOllama(cfg) {
		engine, _ := cfg.Engine("ollama")
		manager.engines["ollama"] = EngineStatus{Name: "ollama", Type: "ollama", State: "checking", URL: engine.URL, Model: engine.Model, LifecycleOwner: "external", UpdatedAt: time.Now().UTC()}
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
	packs := append([]resourcepacks.Pack(nil), m.packs...)
	m.mu.RUnlock()
	return RuntimeStatus{Hardware: hardware, Engines: engines, Models: modelregistry.List(m.cfg), Packs: packs}
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
	packs := discoverResourcePacks(m.cfg)
	m.mu.Lock()
	m.packs = append([]resourcepacks.Pack(nil), packs...)
	m.mu.Unlock()
	engines := m.cfg.Engines
	if _, ok := engines["ollama"]; !ok && needsImplicitOllama(m.cfg) {
		engine, _ := m.cfg.Engine("ollama")
		engines = make(map[string]config.Engine, len(m.cfg.Engines)+1)
		for name, configured := range m.cfg.Engines {
			engines[name] = configured
		}
		engines["ollama"] = engine
	}
	for name, engine := range engines {
		resolved, resolveErr := resolveResourceEngine(engine, packs)
		if resolveErr != nil {
			m.setEngine(EngineStatus{Name: name, Type: engine.Type, State: "unavailable", Model: engine.Model, LifecycleOwner: configuredLifecycleOwner(engine), Warning: resolveErr.Error(), UpdatedAt: time.Now().UTC()})
			continue
		}
		engine = resolved
		if engine.Type == "ollama" {
			status := ollamaStatus(parent, name, engine)
			m.setEngine(status)
			continue
		}
		if engine.Type == "openai_compatible" {
			m.setEngine(openAICompatibleStatus(parent, name, engine))
			continue
		}
		if engine.Type == "llama_cpp" && !engine.AutoStart {
			status := EngineStatus{Name: name, Type: engine.Type, State: "stopped", URL: engineURL(engine), Model: engine.Model, LifecycleOwner: "external", UpdatedAt: time.Now().UTC()}
			ctx, cancel := context.WithTimeout(parent, time.Second)
			if healthy(ctx, status.URL) {
				status.State = "online"
				status.Affinity = "external"
				status.Models = []RuntimeModel{configuredRuntimeModel(engine, true)}
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
			m.setEngine(EngineStatus{Name: name, Type: engine.Type, State: "online", URL: engineURL(engine), Model: engine.Model, Affinity: "external", LifecycleOwner: "external", Models: []RuntimeModel{configuredRuntimeModel(engine, true)}, UpdatedAt: time.Now().UTC()})
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
	m.setEngine(EngineStatus{Name: name, Type: engine.Type, State: "starting", URL: engineURL(engine), Model: engine.Model, Affinity: affinity, LifecycleOwner: "contextbridge", Warning: warning, UpdatedAt: time.Now().UTC()})
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
		status := EngineStatus{Name: name, Type: engine.Type, State: "online", URL: engineURL(engine), Model: engine.Model, Affinity: affinity, LifecycleOwner: "contextbridge", Warning: warning, Models: []RuntimeModel{configuredRuntimeModel(engine, true)}, UpdatedAt: time.Now().UTC()}
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
	status.LifecycleOwner = ""
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

func needsImplicitOllama(cfg config.Config) bool {
	for _, route := range cfg.Routes {
		for _, provider := range append([]string{route.Provider}, route.Fallback...) {
			if strings.EqualFold(strings.TrimSpace(provider), "ollama") {
				return true
			}
		}
	}
	return false
}

func configuredLifecycleOwner(engine config.Engine) string {
	if engine.Type == "llama_cpp" && engine.AutoStart {
		return ""
	}
	return "external"
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

func openAICompatibleStatus(parent context.Context, name string, engine config.Engine) EngineStatus {
	affinity := "loopback API"
	if engine.Remote {
		affinity = "remote API"
	}
	status := EngineStatus{Name: name, Type: engine.Type, Remote: engine.Remote, State: "offline", URL: engine.URL, Model: engine.Model, Affinity: affinity, LifecycleOwner: "external", UpdatedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(engine.URL, "/")+"/models", nil)
	if err != nil {
		status.Warning = "invalid provider endpoint"
		return status
	}
	applyOpenAIEngineAuth(request, engine)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return status
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status.Warning = "model inventory returned " + response.Status
		return status
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		status.Warning = "model inventory was not valid JSON"
		return status
	}
	status.State = "online"
	available := false
	for index, candidate := range payload.Data {
		if index >= 512 {
			break
		}
		if strings.EqualFold(strings.TrimSpace(candidate.ID), strings.TrimSpace(engine.Model)) {
			available = true
			break
		}
	}
	status.Models = []RuntimeModel{{
		Name: engine.Model, Available: available, Loaded: false,
		Capabilities: append([]string(nil), engine.Capabilities...), CapabilitiesVerified: true, CapabilitySource: "operator_config",
		ContextWindowTokens: engine.ContextWindowTokens, MaxOutputTokens: engine.MaxOutputTokens,
		MaxInputImages: engine.MaxInputImages, MaxImageBytes: engine.MaxImageBytes, MaxTotalImageBytes: engine.MaxTotalImageBytes,
		ImageMediaTypes: append([]string(nil), engine.ImageMediaTypes...), LimitsVerified: configuredEngineLimits(engine), LimitSource: configuredEngineLimitSource(engine),
	}}
	if !available {
		status.Warning = "configured model is absent from /models"
	}
	return status
}

func configuredEngineLimits(engine config.Engine) bool {
	return engine.ContextWindowTokens > 0 || engine.MaxOutputTokens > 0 || engine.MaxInputImages > 0 || engine.MaxImageBytes > 0 || engine.MaxTotalImageBytes > 0 || len(engine.ImageMediaTypes) > 0
}

func configuredEngineLimitSource(engine config.Engine) string {
	if configuredEngineLimits(engine) {
		return "operator_config"
	}
	return ""
}

func configuredRuntimeModel(engine config.Engine, loaded bool) RuntimeModel {
	capabilities := append([]string(nil), engine.Capabilities...)
	if len(capabilities) == 0 {
		switch strings.ToLower(strings.TrimSpace(engine.Mode)) {
		case "embedding":
			capabilities = []string{"embedding"}
		case "vision":
			capabilities = []string{"text", "vision"}
		default:
			capabilities = []string{"text"}
		}
	}
	return RuntimeModel{
		Name: engine.Model, Available: true, Loaded: loaded,
		Capabilities: capabilities, CapabilitiesVerified: len(engine.Capabilities) > 0, CapabilitySource: "operator_config",
		ContextWindowTokens: engine.ContextWindowTokens, MaxOutputTokens: engine.MaxOutputTokens,
		MaxInputImages: engine.MaxInputImages, MaxImageBytes: engine.MaxImageBytes, MaxTotalImageBytes: engine.MaxTotalImageBytes,
		ImageMediaTypes: append([]string(nil), engine.ImageMediaTypes...), LimitsVerified: configuredEngineLimits(engine), LimitSource: configuredEngineLimitSource(engine),
	}
}

func ollamaStatus(parent context.Context, name string, engine config.Engine) EngineStatus {
	status := EngineStatus{Name: name, Type: "ollama", State: "offline", URL: engine.URL, Model: engine.Model, LifecycleOwner: "external", UpdatedAt: time.Now().UTC()}
	base := strings.TrimRight(engine.URL, "/")
	var version struct {
		Version string `json:"version"`
	}
	versionCtx, versionCancel := context.WithTimeout(parent, 800*time.Millisecond)
	versionOK := getJSON(versionCtx, base+"/api/version", &version)
	versionCancel()
	if !versionOK {
		return status
	}
	status.State = "online"
	status.Version = version.Version
	type evidence struct {
		digest       string
		capabilities []string
		hint         string
	}
	evidenceByModel := map[string]evidence{}
	var cached struct {
		Models []struct {
			Name         string   `json:"name"`
			Digest       string   `json:"digest"`
			Size         int64    `json:"size"`
			Capabilities []string `json:"capabilities"`
		} `json:"models"`
	}
	tagsCtx, tagsCancel := context.WithTimeout(parent, time.Second)
	tagsOK := getJSON(tagsCtx, base+"/api/tags", &cached)
	tagsCancel()
	if tagsOK {
		for index, model := range cached.Models {
			if index >= 256 {
				break
			}
			status.Models = append(status.Models, RuntimeModel{Name: model.Name, Size: model.Size, Available: true})
			evidenceByModel[strings.ToLower(strings.TrimSpace(model.Name))] = evidence{digest: model.Digest, capabilities: model.Capabilities, hint: model.Name}
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
	// Loaded-state evidence is latency-sensitive and independent of /api/show.
	// Read it before capability probes so a slow model cannot erase GPU/CPU and
	// loaded-model telemetry for the whole status refresh.
	psCtx, psCancel := context.WithTimeout(parent, 800*time.Millisecond)
	psOK := getJSON(psCtx, base+"/api/ps", &running)
	psCancel()
	if psOK {
		for _, model := range running.Models {
			affinity := "CPU"
			if model.SizeVRAM > 0 && model.SizeVRAM >= model.Size {
				affinity = "GPU"
			} else if model.SizeVRAM > 0 {
				affinity = "GPU and CPU"
			}
			updated := false
			for index := range status.Models {
				if strings.EqualFold(status.Models[index].Name, model.Name) {
					status.Models[index].Size = model.Size
					status.Models[index].VRAM = model.SizeVRAM
					status.Models[index].Affinity = affinity
					status.Models[index].ExpiresAt = model.ExpiresAt
					status.Models[index].Available = true
					status.Models[index].Loaded = true
					updated = true
					break
				}
			}
			if !updated {
				status.Models = append(status.Models, RuntimeModel{Name: model.Name, Size: model.Size, VRAM: model.SizeVRAM, Affinity: affinity, ExpiresAt: model.ExpiresAt, Available: true, Loaded: true})
				evidenceByModel[strings.ToLower(strings.TrimSpace(model.Name))] = evidence{hint: model.Name}
			}
		}
	}
	capabilityCtx, capabilityCancel := context.WithTimeout(parent, 2*time.Second)
	defer capabilityCancel()
	for index := range status.Models {
		model := &status.Models[index]
		item := evidenceByModel[strings.ToLower(strings.TrimSpace(model.Name))]
		if item.hint == "" {
			item.hint = model.Name
		}
		if capabilityCtx.Err() != nil {
			model.Capabilities = modelregistry.OllamaCapabilities(item.capabilities, item.hint)
			model.CapabilitiesVerified = len(item.capabilities) > 0
			if model.CapabilitiesVerified {
				model.CapabilitySource = "ollama_tags"
			} else {
				model.CapabilitySource = "name_inference"
			}
			applyConfiguredRuntimeLimits(model, engine)
			continue
		}
		modelCtx, modelCancel := context.WithTimeout(capabilityCtx, 400*time.Millisecond)
		evidence := modelregistry.ResolveOllamaModelEvidence(modelCtx, http.DefaultClient, base, model.Name, item.digest, item.capabilities, item.hint)
		model.Capabilities, model.CapabilitiesVerified, model.CapabilitySource = evidence.Capabilities, evidence.CapabilitiesVerified, evidence.CapabilitySource
		model.ContextWindowTokens, model.LimitsVerified, model.LimitSource = evidence.ContextWindowTokens, evidence.LimitsVerified, evidence.LimitSource
		applyConfiguredRuntimeLimits(model, engine)
		modelCancel()
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

func applyConfiguredRuntimeLimits(model *RuntimeModel, engine config.Engine) {
	if model == nil || !configuredEngineLimits(engine) {
		return
	}
	if engine.ContextWindowTokens > 0 {
		model.ContextWindowTokens = engine.ContextWindowTokens
	}
	if engine.MaxOutputTokens > 0 {
		model.MaxOutputTokens = engine.MaxOutputTokens
	}
	if engine.MaxInputImages > 0 {
		model.MaxInputImages = engine.MaxInputImages
	}
	if engine.MaxImageBytes > 0 {
		model.MaxImageBytes = engine.MaxImageBytes
	}
	if engine.MaxTotalImageBytes > 0 {
		model.MaxTotalImageBytes = engine.MaxTotalImageBytes
	}
	if len(engine.ImageMediaTypes) > 0 {
		model.ImageMediaTypes = append([]string(nil), engine.ImageMediaTypes...)
	}
	model.LimitsVerified = true
	if model.LimitSource == "ollama_show" {
		model.LimitSource = "ollama_show+operator_config"
	} else {
		model.LimitSource = "operator_config"
	}
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
