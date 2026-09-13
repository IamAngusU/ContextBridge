package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/systeminfo"
	"github.com/coder/websocket"
)

type WorkerConfig struct {
	RelayURL       string
	IdentityFile   string
	Name           string
	Groups         []string
	Tags           []string
	MaxConcurrent  int
	LocalURL       string
	LocalToken     string
	HeartbeatEvery time.Duration
	RequestTimeout time.Duration
	AllowedTasks   []string
}

type WorkerIdentity struct {
	NodeID     string `json:"node_id"`
	NodeToken  string `json:"node_token"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	RelayURL   string `json:"relay_url"`
}

type Worker struct {
	cfg        WorkerConfig
	identity   WorkerIdentity
	client     *http.Client
	sem        chan struct{}
	mu         sync.Mutex
	running    int
	hardwareMu sync.Mutex
	hardware   systeminfo.Snapshot
	hardwareAt time.Time
}

func LoadWorker(cfg WorkerConfig) (*Worker, error) {
	if cfg.RelayURL == "" || cfg.IdentityFile == "" {
		return nil, errors.New("relay URL and identity file are required")
	}
	raw, err := os.ReadFile(cfg.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("load worker identity: %w", err)
	}
	var identity WorkerIdentity
	if err := json.Unmarshal(raw, &identity); err != nil || identity.NodeID == "" || identity.NodeToken == "" || identity.PrivateKey == "" {
		return nil, errors.New("worker identity is incomplete; run contextbridge pair first")
	}
	if cfg.Name == "" {
		cfg.Name, _ = os.Hostname()
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 1
	}
	if cfg.LocalURL == "" {
		cfg.LocalURL = "http://127.0.0.1:32145"
	}
	if cfg.HeartbeatEvery <= 0 {
		cfg.HeartbeatEvery = 5 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Minute
	}
	return &Worker{cfg: cfg, identity: identity, client: &http.Client{Timeout: cfg.RequestTimeout}, sem: make(chan struct{}, cfg.MaxConcurrent)}, nil
}

func PairWorker(ctx context.Context, relayURL, name, identityFile string, groups []string, output func(PairResponse)) error {
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		return err
	}
	request := PairRequest{NodeName: name, PublicKey: publicKey, Groups: groups}
	var response PairResponse
	if err := postJSON(ctx, http.DefaultClient, endpoint(relayURL, "/v1/pair/request"), "", request, &response); err != nil {
		return err
	}
	if output != nil {
		output(response)
	}
	ticker := time.NewTicker(time.Duration(response.IntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var poll struct {
				State     string `json:"state"`
				NodeID    string `json:"node_id"`
				NodeToken string `json:"node_token"`
			}
			err := postJSON(ctx, http.DefaultClient, endpoint(relayURL, "/v1/pair/token"), "", map[string]string{"device_code": response.DeviceCode}, &poll)
			if err != nil {
				var statusErr *HTTPError
				if errors.As(err, &statusErr) && statusErr.Status == http.StatusAccepted {
					continue
				}
				return err
			}
			switch poll.State {
			case "authorization_pending":
				continue
			case "approved":
				identity := WorkerIdentity{NodeID: poll.NodeID, NodeToken: poll.NodeToken, PrivateKey: privateKey, PublicKey: publicKey, RelayURL: relayURL}
				return saveIdentity(identityFile, identity)
			case "access_denied", "expired_token":
				return fmt.Errorf("pairing ended with %s", poll.State)
			}
		}
	}
}

func BootstrapWorkerIdentity(database, relayURL, name, identityFile string, groups []string) error {
	store, err := OpenStore(database)
	if err != nil {
		return err
	}
	defer store.Close()
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		return err
	}
	nodeID := randomID("node")
	token, _, err := store.CreateToken("node", nodeID, groups, 0)
	if err != nil {
		return err
	}
	if err := store.UpsertNode(Node{ID: nodeID, Name: cleanLabel(name, 100), PublicKey: publicKey, State: "paired", LastSeen: time.Now().UTC()}); err != nil {
		return err
	}
	return saveIdentity(identityFile, WorkerIdentity{NodeID: nodeID, NodeToken: token, PrivateKey: privateKey, PublicKey: publicKey, RelayURL: relayURL})
}

func (w *Worker) Run(ctx context.Context, logger func(string, ...interface{})) error {
	if logger == nil {
		logger = func(string, ...interface{}) {}
	}
	backoff := time.Second
	for ctx.Err() == nil {
		connectedAt := time.Now()
		err := w.connect(ctx, logger)
		if ctx.Err() != nil {
			return nil
		}
		logger("worker connection ended: %v", err)
		if time.Since(connectedAt) >= 30*time.Second {
			backoff = time.Second
		}
		jitter := time.Duration(rand.Int63n(int64(backoff / 3)))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff + jitter):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func (w *Worker) connect(ctx context.Context, logger func(string, ...interface{})) error {
	capabilities := w.capabilities(ctx)
	node := Node{ID: w.identity.NodeID, Name: w.cfg.Name, PublicKey: w.identity.PublicKey, Capabilities: capabilities, State: "online", Connected: true, LastSeen: time.Now().UTC()}
	target, err := websocketURL(endpoint(w.cfg.RelayURL, "/v1/cluster/workers/connect"))
	if err != nil {
		return err
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+w.identity.NodeToken)
	conn, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return err
	}
	conn.SetReadLimit(20 << 20)
	defer conn.Close(websocket.StatusNormalClosure, "worker stopping")
	var writeMu sync.Mutex
	write := func(message WireMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		message.Version = ProtocolVersion
		return conn.Write(ctx, websocket.MessageText, mustJSON(message))
	}
	if err := write(WireMessage{Type: "hello", Node: &node}); err != nil {
		return err
	}
	logger("connected as %s with %d job slot(s)", node.Name, capabilities.MaxConcurrent)
	heartbeatCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(w.cfg.HeartbeatEvery)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				latest := w.capabilities(heartbeatCtx)
				_ = write(WireMessage{Type: "heartbeat", Capabilities: &latest})
			}
		}
	}()
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var message WireMessage
		if json.Unmarshal(raw, &message) != nil || message.Type != "job" || message.Job == nil {
			continue
		}
		job := *message.Job
		w.sem <- struct{}{}
		w.changeRunning(1)
		go func() {
			defer func() { <-w.sem; w.changeRunning(-1) }()
			_ = write(WireMessage{Type: "started", JobID: job.ID})
			logger("received %s for task %s", job.ID, job.Requirements.Task)
			result, sealed, usage, runErr := w.execute(ctx, job, func(progress JobProgress) {
				_ = write(WireMessage{Type: "progress", JobID: job.ID, Progress: &progress})
			})
			errorText := ""
			if runErr != nil {
				errorText = runErr.Error()
				logger("job %s failed: %v", job.ID, runErr)
			} else {
				logger("completed %s in %d ms", job.ID, usage.ComputeMS)
			}
			_ = write(WireMessage{Type: "result", JobID: job.ID, Result: result, SealedResult: sealed, Usage: usage, Error: errorText})
		}()
	}
}

func (w *Worker) execute(ctx context.Context, job Job, emitProgress func(JobProgress)) (json.RawMessage, *SealedEnvelope, Usage, error) {
	started := time.Now()
	payload := []byte(job.Payload)
	shared := ""
	if job.SealedPayload != nil {
		var err error
		payload, shared, err = OpenWith(w.identity.PrivateKey, job.SealedPayload, jobAAD(job.ID, w.identity.NodeID))
		if err != nil {
			return nil, nil, Usage{}, fmt.Errorf("decrypt job: %w", err)
		}
	}
	if !json.Valid(payload) {
		return nil, nil, Usage{}, errors.New("job payload must be valid JSON")
	}
	localJobID := localExecutionID(job)
	payload, err := prepareLocalPayload(payload, job.Requirements.Provider, localJobID)
	if err != nil {
		return nil, nil, Usage{}, err
	}
	progressCtx, stopProgress := context.WithCancel(ctx)
	var progressWG sync.WaitGroup
	if emitProgress != nil && job.SealedPayload == nil && strings.EqualFold(job.Requirements.Provider, "browser") {
		progressWG.Add(1)
		go func() {
			defer progressWG.Done()
			w.watchLocalBrowserProgress(progressCtx, localJobID, emitProgress)
		}()
	}
	defer progressWG.Wait()
	defer stopProgress()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(w.cfg.LocalURL, "/")+"/v1/jobs", bytes.NewReader(payload))
	if err != nil {
		return nil, nil, Usage{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+w.cfg.LocalToken)
	response, err := w.client.Do(request)
	if err != nil {
		return nil, nil, Usage{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, nil, Usage{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, Usage{}, fmt.Errorf("local bridge returned %s: %s", response.Status, truncate(string(raw), 500))
	}
	usage := extractUsage(raw)
	usage.ComputeMS = uint64(time.Since(started).Milliseconds())
	for _, gpu := range w.hardwareSnapshot(ctx, 2*time.Second).GPUs {
		used := uint64(0)
		if gpu.MemoryTotal >= gpu.MemoryFree {
			used = gpu.MemoryTotal - gpu.MemoryFree
		}
		if used > usage.PeakVRAMBytes {
			usage.PeakVRAMBytes = used
		}
	}
	if shared != "" {
		sealed, sealErr := SealResponse(shared, raw, resultAAD(job.ID, w.identity.NodeID))
		return nil, sealed, usage, sealErr
	}
	return json.RawMessage(raw), nil, usage, nil
}

func prepareLocalPayload(payload []byte, provider, localJobID string) ([]byte, error) {
	provider = strings.TrimSpace(provider)
	var job map[string]json.RawMessage
	if err := json.Unmarshal(payload, &job); err != nil || job == nil {
		return nil, errors.New("job payload must be a JSON object")
	}
	if provider != "" {
		rawProvider, _ := json.Marshal(provider)
		job["provider"] = rawProvider
	}
	rawID, _ := json.Marshal(localJobID)
	job["id"] = rawID
	return json.Marshal(job)
}

func localExecutionID(job Job) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", job.ID, job.Attempt)))
	return fmt.Sprintf("cluster-%x", sum[:16])
}

func (w *Worker) watchLocalBrowserProgress(ctx context.Context, jobID string, emit func(JobProgress)) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var sequence uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		request, _ := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(w.cfg.LocalURL, "/")+"/v1/browser/jobs/"+url.PathEscape(jobID)+"/progress", nil)
		request.Header.Set("Authorization", "Bearer "+w.cfg.LocalToken)
		response, err := w.client.Do(request)
		if err != nil {
			cancel()
			continue
		}
		var progress JobProgress
		if response.StatusCode == http.StatusOK {
			err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&progress)
		}
		response.Body.Close()
		cancel()
		if err == nil && progress.Sequence > sequence {
			sequence = progress.Sequence
			emit(progress)
		}
	}
}

func (w *Worker) capabilities(ctx context.Context) Capabilities {
	hardware := w.hardwareSnapshot(ctx, 2*time.Second)
	w.mu.Lock()
	running := w.running
	w.mu.Unlock()
	capability := Capabilities{OS: hardware.OS, Architecture: hardware.Architecture, CPU: hardware.CPU, CPUCores: hardware.CPUCores, MemoryTotal: hardware.MemoryTotal, MemoryFree: hardware.MemoryAvailable, Groups: cleanList(w.cfg.Groups, 16, 80), Tags: cleanList(w.cfg.Tags, 32, 80), MaxConcurrent: w.cfg.MaxConcurrent, Running: running}
	for _, gpu := range hardware.GPUs {
		capability.GPUs = append(capability.GPUs, GPUCapability{Name: gpu.Name, Backend: gpu.Backend, MemoryTotal: gpu.MemoryTotal, MemoryFree: gpu.MemoryFree})
	}
	statusCtx, statusCancel := context.WithTimeout(ctx, 4*time.Second)
	defer statusCancel()
	request, _ := http.NewRequestWithContext(statusCtx, http.MethodGet, strings.TrimRight(w.cfg.LocalURL, "/")+"/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+w.cfg.LocalToken)
	if response, err := w.client.Do(request); err == nil {
		defer response.Body.Close()
		var status struct {
			Queued  int `json:"queued"`
			Browser struct {
				Connected      bool `json:"connected"`
				SelectorsReady bool `json:"selectors_ready"`
			} `json:"browser"`
			Routes map[string]struct {
				Task     string   `json:"task"`
				Model    string   `json:"model"`
				Provider string   `json:"provider"`
				Fallback []string `json:"fallback"`
			} `json:"routes"`
			Runtime struct {
				Engines map[string]struct {
					State  string `json:"state"`
					Models []struct {
						Name   string `json:"name"`
						Size   int64  `json:"size"`
						VRAM   int64  `json:"vram"`
						Loaded bool   `json:"loaded"`
					} `json:"models"`
				} `json:"engines"`
			} `json:"runtime"`
		}
		if response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&status) == nil {
			capability.QueueDepth = status.Queued
			seenTasks := map[string]bool{}
			seenProviders := map[string]bool{}
			providerOnline := func(provider string) bool {
				if provider == "browser" {
					return status.Browser.Connected && status.Browser.SelectorsReady
				}
				engine, ok := status.Runtime.Engines[provider]
				return ok && engine.State == "online"
			}
			for _, route := range status.Routes {
				task := route.Task
				if task == "" {
					task = "generation"
				}
				providers := append([]string{route.Provider}, route.Fallback...)
				routeReady := false
				for _, provider := range providers {
					if provider != "" && providerOnline(provider) {
						routeReady = true
						if !seenProviders[provider] {
							capability.Providers = append(capability.Providers, provider)
							seenProviders[provider] = true
						}
					}
				}
				if routeReady && !seenTasks[task] && (len(w.cfg.AllowedTasks) == 0 || containsFold(w.cfg.AllowedTasks, task)) {
					capability.Tasks = append(capability.Tasks, task)
					seenTasks[task] = true
				}
				if route.Model != "" && providerOnline(route.Provider) {
					vision, embedding := modelFeatures(route.Model, task)
					capability.Models = append(capability.Models, ModelCapability{Name: route.Model, Tasks: modelTasks(task, vision, embedding), Provider: route.Provider, Vision: vision, Embedding: embedding})
				}
			}
			for provider, engine := range status.Runtime.Engines {
				if engine.State != "online" {
					continue
				}
				if !seenProviders[provider] {
					capability.Providers = append(capability.Providers, provider)
					seenProviders[provider] = true
				}
				for _, model := range engine.Models {
					vision, embedding := modelFeatures(model.Name, "generation")
					capability.Models = append(capability.Models, ModelCapability{Name: model.Name, Provider: provider, Size: model.Size, VRAM: model.VRAM, Loaded: model.Loaded, Vision: vision, Embedding: embedding, Tasks: modelTasks("generation", vision, embedding)})
				}
			}
		}
	}
	return capability
}

func (w *Worker) hardwareSnapshot(ctx context.Context, maxAge time.Duration) systeminfo.Snapshot {
	w.hardwareMu.Lock()
	defer w.hardwareMu.Unlock()
	if !w.hardwareAt.IsZero() && maxAge > 0 && time.Since(w.hardwareAt) < maxAge {
		return w.hardware
	}
	detectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	w.hardware = systeminfo.Detect(detectCtx)
	w.hardwareAt = time.Now()
	return w.hardware
}

func (w *Worker) changeRunning(delta int) {
	w.mu.Lock()
	w.running += delta
	w.mu.Unlock()
}

type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

func postJSON(ctx context.Context, client *http.Client, target, token string, input, output interface{}) error {
	raw, _ := json.Marshal(input)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPError{Status: response.StatusCode, Body: truncate(string(body), 500)}
	}
	if output != nil {
		return json.Unmarshal(body, output)
	}
	return nil
}

func saveIdentity(path string, identity WorkerIdentity) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(identity, "", "  ")
	return os.WriteFile(path, raw, 0600)
}

func websocketURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", errors.New("relay URL must use http or https")
	}
	return parsed.String(), nil
}

func endpoint(base, path string) string     { return strings.TrimRight(base, "/") + path }
func jobAAD(jobID, nodeID string) []byte    { return []byte("job:" + jobID + ":" + nodeID) }
func resultAAD(jobID, nodeID string) []byte { return []byte("result:" + jobID + ":" + nodeID) }
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func extractUsage(raw []byte) Usage {
	var value interface{}
	_ = json.Unmarshal(raw, &value)
	usage := Usage{}
	walkNumbers(value, func(key string, number float64) {
		switch strings.ToLower(key) {
		case "prompt_tokens", "input_tokens", "prompt_eval_count":
			usage.InputTokens = maxU64(usage.InputTokens, uint64(number))
		case "completion_tokens", "output_tokens", "eval_count":
			usage.OutputTokens = maxU64(usage.OutputTokens, uint64(number))
		case "total_tokens":
			usage.TotalTokens = maxU64(usage.TotalTokens, uint64(number))
		}
	})
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return usage
}

func walkNumbers(value interface{}, visit func(string, float64)) {
	switch current := value.(type) {
	case map[string]interface{}:
		for key, child := range current {
			if number, ok := child.(float64); ok {
				visit(key, number)
			}
			walkNumbers(child, visit)
		}
	case []interface{}:
		for _, child := range current {
			walkNumbers(child, visit)
		}
	}
}

func maxU64(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}

func modelFeatures(name, task string) (bool, bool) {
	lower := strings.ToLower(name)
	vision := task == "vision" || strings.Contains(lower, "vl") || strings.Contains(lower, "llava") || strings.Contains(lower, "gemma3") || strings.Contains(lower, "vision")
	embedding := task == "embedding" || strings.Contains(lower, "embed") || strings.Contains(lower, "jina") || strings.Contains(lower, "nomic") || strings.Contains(lower, "bge")
	return vision, embedding
}

func modelTasks(task string, vision, embedding bool) []string {
	tasks := []string{task}
	if vision && !containsFold(tasks, "vision") {
		tasks = append(tasks, "vision")
	}
	if embedding && !containsFold(tasks, "embedding") {
		tasks = append(tasks, "embedding")
	}
	return tasks
}

var _ = runtime.GOOS
