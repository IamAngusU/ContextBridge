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
	RelayURL         string
	IdentityFile     string
	Name             string
	Groups           []string
	Tags             []string
	MaxConcurrent    int
	LocalURL         string
	LocalToken       string
	HeartbeatEvery   time.Duration
	RequestTimeout   time.Duration
	AllowedTasks     []string
	AllowedProviders []string
	AllowedModels    []string
	Version          string
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

func (w *Worker) Idle() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running == 0
}

const (
	WorkerConnecting   = "connecting"
	WorkerRetrying     = "retrying"
	WorkerConnected    = "connected"
	WorkerCapabilities = "capabilities"
	WorkerJobStarted   = "job_started"
	WorkerJobProgress  = "job_progress"
	WorkerJobCompleted = "job_completed"
	WorkerJobFailed    = "job_failed"
)

type WorkerEvent struct {
	Kind              string
	NodeID            string
	NodeName          string
	Slots             int
	Attempt           int
	RetryIn           time.Duration
	Error             string
	JobID             string
	Task              string
	Provider          string
	Profile           string
	Model             string
	Reasoning         string
	ReportedProvider  string
	ReportedModel     string
	ReportedReasoning string
	Phase             string
	ComputeMS         uint64
	Percent           int
	Detail            string
	Sequence          uint64
	Text              string
	Capabilities      Capabilities
}

type WorkerReporter func(WorkerEvent)

func LoadWorker(cfg WorkerConfig) (*Worker, error) {
	if cfg.RelayURL == "" || cfg.IdentityFile == "" {
		return nil, errors.New("relay URL and identity file are required")
	}
	if !strings.HasPrefix(cfg.RelayURL, "https://") && !strings.HasPrefix(cfg.RelayURL, "http://127.0.0.1:") && !strings.HasPrefix(cfg.RelayURL, "http://localhost:") {
		return nil, errors.New("worker relay URL must use HTTPS or localhost")
	}
	raw, err := os.ReadFile(cfg.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("load worker identity: %w", err)
	}
	var identity WorkerIdentity
	if err := json.Unmarshal(raw, &identity); err != nil || identity.NodeID == "" || identity.NodeToken == "" || identity.PrivateKey == "" {
		return nil, errors.New("worker identity is incomplete; run contextbridge pair first")
	}
	if identity.RelayURL != "" && strings.TrimRight(identity.RelayURL, "/") != strings.TrimRight(cfg.RelayURL, "/") {
		return nil, errors.New("worker identity belongs to another relay; pair this identity with the selected server")
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
	return w.RunWithEvents(ctx, func(event WorkerEvent) {
		switch event.Kind {
		case WorkerRetrying:
			logger("worker connection ended: %s; retrying in %s", event.Error, event.RetryIn.Round(time.Millisecond))
		case WorkerConnected:
			logger("connected as %s with %d job slot(s)", event.NodeName, event.Slots)
		case WorkerJobStarted:
			logger("received %s for task %s", event.JobID, event.Task)
		case WorkerJobFailed:
			logger("job %s failed: %s", event.JobID, event.Error)
		case WorkerJobCompleted:
			logger("completed %s in %d ms", event.JobID, event.ComputeMS)
		}
	})
}

func (w *Worker) RunWithEvents(ctx context.Context, report WorkerReporter) error {
	if report == nil {
		report = func(WorkerEvent) {}
	}
	backoff := time.Second
	attempt := 0
	for ctx.Err() == nil {
		attempt++
		report(WorkerEvent{Kind: WorkerConnecting, NodeName: w.cfg.Name, Attempt: attempt})
		connectedAt := time.Now()
		err := w.connect(ctx, report)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(connectedAt) >= 30*time.Second {
			backoff = time.Second
			attempt = 1
		}
		jitter := time.Duration(rand.Int63n(int64(backoff / 3)))
		retryIn := backoff + jitter
		report(WorkerEvent{Kind: WorkerRetrying, NodeName: w.cfg.Name, Attempt: attempt, RetryIn: retryIn, Error: err.Error()})
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retryIn):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func (w *Worker) connect(ctx context.Context, report WorkerReporter) error {
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
	report(WorkerEvent{Kind: WorkerConnected, NodeID: node.ID, NodeName: node.Name, Slots: capabilities.MaxConcurrent, Capabilities: capabilities})
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
				if write(WireMessage{Type: "heartbeat", Capabilities: &latest}) == nil {
					report(WorkerEvent{Kind: WorkerCapabilities, NodeName: node.Name, Slots: latest.MaxConcurrent, Capabilities: latest})
				}
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
			provider, profile, model, reasoning := jobRequestLabels(job)
			report(WorkerEvent{Kind: WorkerJobStarted, NodeName: node.Name, JobID: job.ID, Task: job.Requirements.Task, Provider: provider, Profile: profile, Model: model, Reasoning: reasoning})
			result, sealed, usage, runErr := w.execute(ctx, job, func(progress JobProgress) {
				_ = write(WireMessage{Type: "progress", JobID: job.ID, Progress: &progress})
				report(WorkerEvent{Kind: WorkerJobProgress, NodeName: node.Name, JobID: job.ID, Task: job.Requirements.Task, Phase: progress.Phase, Percent: progress.Percent, Detail: progress.Detail, Sequence: progress.Sequence, Text: progress.Text})
			})
			errorText := ""
			if runErr != nil {
				errorText = runErr.Error()
				report(WorkerEvent{Kind: WorkerJobFailed, NodeName: node.Name, JobID: job.ID, Task: job.Requirements.Task, Error: errorText})
			} else {
				reportedProvider, reportedModel, reportedReasoning := localResultSelection(result)
				report(WorkerEvent{Kind: WorkerJobCompleted, NodeName: node.Name, JobID: job.ID, Task: job.Requirements.Task, ComputeMS: usage.ComputeMS, ReportedProvider: reportedProvider, ReportedModel: reportedModel, ReportedReasoning: reportedReasoning})
			}
			_ = write(WireMessage{Type: "result", JobID: job.ID, Result: result, SealedResult: sealed, Usage: usage, Error: errorText})
		}()
	}
}

func jobRequestLabels(job Job) (provider, profile, model, reasoning string) {
	provider, model = job.Requirements.Provider, job.Requirements.Model
	if job.SealedPayload != nil || len(job.Payload) == 0 {
		return provider, "", model, ""
	}
	var payload struct {
		Provider  string `json:"provider"`
		Profile   string `json:"browser_profile"`
		Model     string `json:"model"`
		Reasoning string `json:"reasoning"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil {
		return provider, "", model, ""
	}
	if provider == "" {
		provider = payload.Provider
	}
	if model == "" {
		model = payload.Model
	}
	return provider, payload.Profile, model, payload.Reasoning
}

func localResultSelection(result json.RawMessage) (provider, model, reasoning string) {
	var submission struct {
		Output struct {
			Provider          string `json:"provider"`
			Model             string `json:"model"`
			SelectedModel     string `json:"selected_model"`
			SelectedReasoning string `json:"selected_reasoning"`
		} `json:"output"`
	}
	if json.Unmarshal(result, &submission) == nil {
		if submission.Output.Provider == "browser" {
			return "browser", submission.Output.SelectedModel, submission.Output.SelectedReasoning
		}
		return submission.Output.Provider, submission.Output.Model, ""
	}
	return "", "", ""
}

func (w *Worker) execute(ctx context.Context, job Job, emitProgress func(JobProgress)) (result json.RawMessage, sealedResult *SealedEnvelope, usage Usage, resultErr error) {
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
	requirements, err := w.applyPolicy(job.Requirements)
	if err != nil {
		return nil, nil, Usage{}, err
	}
	localJobID := localExecutionID(job)
	payload, err = prepareLocalPayload(payload, requirements, localJobID, job.OwnerSubject)
	if err != nil {
		return nil, nil, Usage{}, err
	}
	stopResourceMonitor := w.monitorResources(ctx)
	defer func() {
		peaks := stopResourceMonitor()
		if peaks.PeakVRAMBytes > usage.PeakVRAMBytes {
			usage.PeakVRAMBytes = peaks.PeakVRAMBytes
		}
		if peaks.PeakRAMBytes > usage.PeakRAMBytes {
			usage.PeakRAMBytes = peaks.PeakRAMBytes
		}
		if peaks.PeakGPUUtilization > usage.PeakGPUUtilization {
			usage.PeakGPUUtilization = peaks.PeakGPUUtilization
		}
	}()
	progressCtx, stopProgress := context.WithCancel(ctx)
	var progressWG sync.WaitGroup
	if emitProgress != nil && job.SealedPayload == nil && strings.EqualFold(requirements.Provider, "browser") {
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
	raw, err := io.ReadAll(io.LimitReader(response.Body, 24<<20))
	if err != nil {
		return nil, nil, Usage{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, Usage{}, fmt.Errorf("local bridge returned %s: %s", response.Status, truncate(string(raw), 500))
	}
	usage = extractUsage(raw)
	usage.ComputeMS = uint64(time.Since(started).Milliseconds())
	if outputErr := localOutputError(raw); outputErr != "" {
		return nil, nil, usage, errors.New(outputErr)
	}
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

// applyPolicy is the worker-side boundary. A relay may suggest a job, but it
// cannot make this device use a provider, model, or task excluded by its owner.
func (w *Worker) applyPolicy(requirements Requirements) (Requirements, error) {
	if requirements.Task == "" {
		requirements.Task = "generation"
	}
	if len(w.cfg.AllowedTasks) > 0 && !containsFold(w.cfg.AllowedTasks, requirements.Task) {
		return Requirements{}, fmt.Errorf("worker policy rejects task %q", requirements.Task)
	}
	if len(w.cfg.AllowedProviders) > 0 {
		if requirements.Provider == "" {
			requirements.Provider = w.cfg.AllowedProviders[0]
		}
		if !containsFold(w.cfg.AllowedProviders, requirements.Provider) {
			return Requirements{}, fmt.Errorf("worker policy rejects provider %q", requirements.Provider)
		}
	}
	if len(w.cfg.AllowedModels) > 0 {
		if requirements.Model == "" {
			requirements.Model = w.cfg.AllowedModels[0]
		}
		allowed := containsFold(w.cfg.AllowedModels, requirements.Model)
		if strings.EqualFold(requirements.Provider, "browser") {
			allowed = containsBrowserModel(w.cfg.AllowedModels, requirements.Model)
		}
		if !allowed {
			return Requirements{}, fmt.Errorf("worker policy rejects model %q", requirements.Model)
		}
	}
	return requirements, nil
}

func (w *Worker) monitorResources(parent context.Context) func() Usage {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan Usage, 1)
	go func() {
		peaks := Usage{}
		sample := func() {
			hardware := w.hardwareSnapshot(ctx, 1500*time.Millisecond)
			if hardware.MemoryTotal >= hardware.MemoryAvailable {
				used := hardware.MemoryTotal - hardware.MemoryAvailable
				if used > peaks.PeakRAMBytes {
					peaks.PeakRAMBytes = used
				}
			}
			for _, gpu := range hardware.GPUs {
				used := uint64(0)
				if gpu.MemoryTotal >= gpu.MemoryFree {
					used = gpu.MemoryTotal - gpu.MemoryFree
				}
				if used > peaks.PeakVRAMBytes {
					peaks.PeakVRAMBytes = used
				}
				if gpu.Utilization > peaks.PeakGPUUtilization {
					peaks.PeakGPUUtilization = gpu.Utilization
				}
			}
		}
		sample()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				done <- peaks
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
	var once sync.Once
	peaks := Usage{}
	return func() Usage {
		once.Do(func() {
			cancel()
			peaks = <-done
		})
		return peaks
	}
}

func prepareLocalPayload(payload []byte, requirements Requirements, localJobID string, owner ...string) ([]byte, error) {
	provider := strings.TrimSpace(requirements.Provider)
	var job map[string]json.RawMessage
	if err := json.Unmarshal(payload, &job); err != nil || job == nil {
		return nil, errors.New("job payload must be a JSON object")
	}
	if provider != "" {
		rawProvider, _ := json.Marshal(provider)
		job["provider"] = rawProvider
	}
	if model := strings.TrimSpace(requirements.Model); model != "" {
		rawModel, _ := json.Marshal(model)
		job["model"] = rawModel
	}
	rawID, _ := json.Marshal(localJobID)
	job["id"] = rawID
	if session := strings.TrimSpace(requirements.SessionID); session != "" {
		rawSession, _ := json.Marshal(session)
		job["session_id"] = rawSession
	}
	// A browser tab is a security boundary between producer conversations.
	// Derive its internal binding from the authenticated producer, never from a
	// producer-supplied scope field. The public session_id remains unchanged.
	if strings.EqualFold(provider, "browser") {
		var session string
		_ = json.Unmarshal(job["session_id"], &session)
		if session == "" {
			session = "default"
		}
		producer := "local"
		if len(owner) > 0 && owner[0] != "" {
			producer = owner[0]
		}
		sum := sha256.Sum256([]byte(producer + "\x00" + session))
		key, _ := json.Marshal(fmt.Sprintf("cb:%x", sum[:]))
		job["contextbridge_session_key"] = key
	}
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
	// Hardware probes such as nvidia-smi and CIM are intentionally cached so an
	// idle worker remains effectively asleep between relay heartbeats.
	hardware := w.hardwareSnapshot(ctx, 10*time.Second)
	w.mu.Lock()
	running := w.running
	w.mu.Unlock()
	capability := Capabilities{OS: hardware.OS, OSVersion: hardware.OSVersion, Architecture: hardware.Architecture, CPU: hardware.CPU, CPUCores: hardware.CPUCores, CPUFrequency: hardware.CPUFrequencyMHz, CPUUtilization: hardware.CPUUtilization, UptimeSeconds: hardware.UptimeSeconds, AgentVersion: w.cfg.Version, MemoryTotal: hardware.MemoryTotal, MemoryFree: hardware.MemoryAvailable, MemoryType: hardware.MemoryType, Groups: cleanList(w.cfg.Groups, 16, 80), Tags: cleanList(w.cfg.Tags, 32, 80), MaxConcurrent: w.cfg.MaxConcurrent, Running: running}
	for _, gpu := range hardware.GPUs {
		capability.GPUs = append(capability.GPUs, GPUCapability{Name: gpu.Name, Backend: gpu.Backend, Driver: gpu.Driver, MemoryTotal: gpu.MemoryTotal, MemoryFree: gpu.MemoryFree, Temperature: gpu.Temperature, Utilization: gpu.Utilization})
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
				ActiveTabs     int  `json:"active_tabs"`
				BusyTabs       int  `json:"busy_tabs"`
				Tabs           []struct {
					ID               int      `json:"id"`
					Profile          string   `json:"profile"`
					State            string   `json:"state"`
					CurrentModel     string   `json:"current_model"`
					CurrentReasoning string   `json:"current_reasoning"`
					Models           []string `json:"models"`
				} `json:"tabs"`
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
			// Browser tabs are separate serial UI slots. They must not lower the
			// worker-wide limit for Ollama or other local-model jobs.
			if len(w.cfg.AllowedProviders) == 0 || containsFold(w.cfg.AllowedProviders, "browser") {
				capability.BrowserTabs = status.Browser.ActiveTabs
				capability.BrowserBusy = status.Browser.BusyTabs
				for _, tab := range status.Browser.Tabs {
					capability.BrowserSessions = append(capability.BrowserSessions, BrowserSessionCapability{
						TabID: tab.ID, Profile: truncate(tab.Profile, 40), State: truncate(tab.State, 40),
						CurrentModel: truncate(tab.CurrentModel, 100), CurrentReasoning: truncate(tab.CurrentReasoning, 100),
					})
				}
			}
			seenTasks := map[string]bool{}
			seenProviders := map[string]bool{}
			providerAllowed := func(provider string) bool {
				return len(w.cfg.AllowedProviders) == 0 || containsFold(w.cfg.AllowedProviders, provider)
			}
			modelAllowed := func(model string) bool {
				return len(w.cfg.AllowedModels) == 0 || containsFold(w.cfg.AllowedModels, model)
			}
			browserModelAllowed := func(model string) bool {
				return len(w.cfg.AllowedModels) == 0 || containsBrowserModel(w.cfg.AllowedModels, model)
			}
			allowedModelTasks := func(tasks []string) []string {
				if len(w.cfg.AllowedTasks) == 0 {
					return tasks
				}
				allowed := make([]string, 0, len(tasks))
				for _, task := range tasks {
					if containsFold(w.cfg.AllowedTasks, task) {
						allowed = append(allowed, task)
					}
				}
				return allowed
			}
			providerOnline := func(provider string) bool {
				if !providerAllowed(provider) {
					return false
				}
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
				allowedRouteModel := modelAllowed(route.Model)
				if strings.EqualFold(route.Provider, "browser") {
					allowedRouteModel = browserModelAllowed(route.Model)
				}
				if route.Model != "" && providerOnline(route.Provider) && allowedRouteModel {
					vision, embedding := modelFeatures(route.Model, task)
					if tasks := allowedModelTasks(modelTasks(task, vision, embedding)); len(tasks) > 0 {
						capability.Models = append(capability.Models, ModelCapability{Name: route.Model, Tasks: tasks, Provider: route.Provider, Vision: vision, Embedding: embedding})
					}
				}
			}
			for provider, engine := range status.Runtime.Engines {
				if engine.State != "online" || !providerAllowed(provider) {
					continue
				}
				if !seenProviders[provider] {
					capability.Providers = append(capability.Providers, provider)
					seenProviders[provider] = true
				}
				for _, model := range engine.Models {
					if !modelAllowed(model.Name) {
						continue
					}
					vision, embedding := modelFeatures(model.Name, "generation")
					if tasks := allowedModelTasks(modelTasks("generation", vision, embedding)); len(tasks) > 0 {
						capability.Models = append(capability.Models, ModelCapability{Name: model.Name, Provider: provider, Size: model.Size, VRAM: model.VRAM, Loaded: model.Loaded, Vision: vision, Embedding: embedding, Tasks: tasks})
					}
				}
			}
			seenBrowserModels := map[string]bool{}
			for _, tab := range status.Browser.Tabs {
				if !providerAllowed("browser") {
					break
				}
				models := append([]string{}, tab.Models...)
				if tab.CurrentModel != "" {
					models = append(models, tab.CurrentModel)
				}
				for _, model := range models {
					key := strings.ToLower(strings.TrimSpace(tab.Profile + ":" + model))
					if model == "" || seenBrowserModels[key] || !browserModelAllowed(model) {
						continue
					}
					seenBrowserModels[key] = true
					if tasks := allowedModelTasks([]string{"generation", "vision"}); len(tasks) > 0 {
						capability.Models = append(capability.Models, ModelCapability{Name: model, Provider: "browser", Vision: true, Tasks: tasks})
					}
				}
			}
		}
	}
	capability.Sources = IndicatorSources(capability)
	capability.Modes = IndicatorModes(capability)
	now := time.Now()
	_, capability.UTCOffsetSeconds = now.Zone()
	capability.ClockTime = now.UTC()
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

func localOutputError(raw []byte) string {
	var value struct {
		Error  string `json:"error"`
		Output *struct {
			Error string `json:"error"`
		} `json:"output"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	if value.Output != nil && strings.TrimSpace(value.Output.Error) != "" {
		return strings.TrimSpace(value.Output.Error)
	}
	return strings.TrimSpace(value.Error)
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
