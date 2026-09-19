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
	"sort"
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
	quiescing  bool
	hardwareMu sync.Mutex
	hardware   systeminfo.Snapshot
	hardwareAt time.Time
}

func (w *Worker) Idle() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running == 0
}

// QuiesceForStop atomically prevents a newly received relay frame from
// starting after the composite process has been declared idle.
func (w *Worker) QuiesceForStop(force bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.quiescing {
		return true
	}
	w.quiescing = true
	if !force && w.running != 0 {
		w.quiescing = false
		return false
	}
	return true
}

func (w *Worker) ResumeAfterRejectedStop() {
	w.mu.Lock()
	w.quiescing = false
	w.mu.Unlock()
}

func (w *Worker) beginJob() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.quiescing {
		return false
	}
	w.running++
	return true
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
	connectionCtx, cancelConnection := context.WithCancel(ctx)
	var connectionWG sync.WaitGroup
	defer func() {
		cancelConnection()
		_ = conn.Close(websocket.StatusNormalClosure, "worker connection ended")
		connectionWG.Wait()
	}()
	var writeMu sync.Mutex
	var activeMu sync.Mutex
	activeJobs := map[string]context.CancelFunc{}
	// A cancellation can win the relay write race immediately before the
	// corresponding job frame. Remember a small, bounded set so that frame is
	// acknowledged but never executed. The relay is authenticated, nevertheless
	// keeping this bounded prevents a broken peer from growing memory forever.
	pendingCancels := map[string]time.Time{}
	const maximumPendingCancels = 256
	write := func(message WireMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		message.Version = ProtocolVersion
		return conn.Write(connectionCtx, websocket.MessageText, mustJSON(message))
	}
	if err := write(WireMessage{Type: "hello", Node: &node}); err != nil {
		return err
	}
	report(WorkerEvent{Kind: WorkerConnected, NodeID: node.ID, NodeName: node.Name, Slots: capabilities.MaxConcurrent, Capabilities: capabilities})
	heartbeatCtx := connectionCtx
	connectionWG.Add(1)
	go func() {
		defer connectionWG.Done()
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
		_, raw, err := conn.Read(connectionCtx)
		if err != nil {
			return err
		}
		var message WireMessage
		if json.Unmarshal(raw, &message) != nil || message.Version != ProtocolVersion {
			continue
		}
		if message.Type == "cancel" {
			activeMu.Lock()
			cancelJob := activeJobs[message.JobID]
			if cancelJob == nil && message.JobID != "" {
				now := time.Now()
				for jobID, recordedAt := range pendingCancels {
					if now.Sub(recordedAt) > time.Minute {
						delete(pendingCancels, jobID)
					}
				}
				if len(pendingCancels) < maximumPendingCancels {
					pendingCancels[message.JobID] = now
				}
			}
			activeMu.Unlock()
			if cancelJob != nil {
				cancelJob()
			}
			continue
		}
		if message.Type != "job" || message.Job == nil {
			continue
		}
		job := *message.Job
		select {
		case w.sem <- struct{}{}:
		default:
			_ = write(WireMessage{Type: "result", JobID: job.ID, Attempt: job.Attempt, Error: "worker capacity exceeded"})
			continue
		}
		jobCtx, cancelJob := context.WithCancel(connectionCtx)
		activeMu.Lock()
		_, cancelledBeforeDispatch := pendingCancels[job.ID]
		delete(pendingCancels, job.ID)
		if _, duplicate := activeJobs[job.ID]; duplicate {
			activeMu.Unlock()
			cancelJob()
			<-w.sem
			continue
		}
		activeJobs[job.ID] = cancelJob
		activeMu.Unlock()
		if !w.beginJob() {
			cancelJob()
			activeMu.Lock()
			delete(activeJobs, job.ID)
			activeMu.Unlock()
			<-w.sem
			_ = write(WireMessage{Type: "result", JobID: job.ID, Attempt: job.Attempt, Error: "worker is stopping"})
			continue
		}
		if cancelledBeforeDispatch {
			cancelJob()
		}
		connectionWG.Add(1)
		go func(job Job, jobCtx context.Context, cancelJob context.CancelFunc) {
			defer connectionWG.Done()
			defer func() {
				cancelJob()
				activeMu.Lock()
				delete(activeJobs, job.ID)
				activeMu.Unlock()
				<-w.sem
				w.changeRunning(-1)
			}()
			_ = write(WireMessage{Type: "started", JobID: job.ID, Attempt: job.Attempt})
			provider, profile, model, reasoning := jobRequestLabels(job)
			report(WorkerEvent{Kind: WorkerJobStarted, NodeName: node.Name, JobID: job.ID, Task: job.Requirements.Task, Provider: provider, Profile: profile, Model: model, Reasoning: reasoning})
			result, sealed, usage, execution, runErr := w.execute(jobCtx, job, func(progress JobProgress) {
				_ = write(WireMessage{Type: "progress", JobID: job.ID, Attempt: job.Attempt, Progress: &progress})
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
			_ = write(WireMessage{Type: "result", JobID: job.ID, Attempt: job.Attempt, Result: result, SealedResult: sealed, Usage: usage, Execution: execution, Error: errorText})
		}(job, jobCtx, cancelJob)
	}
}

func jobRequestLabels(job Job) (provider, profile, model, reasoning string) {
	provider, profile, model, reasoning = job.Requirements.Provider, job.Requirements.BrowserProfile, job.Requirements.Model, job.Requirements.Reasoning
	if job.SealedPayload != nil || len(job.Payload) == 0 {
		return provider, profile, model, reasoning
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
	if profile == "" {
		profile = payload.Profile
	}
	if reasoning == "" {
		reasoning = payload.Reasoning
	}
	return provider, profile, model, reasoning
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

func (w *Worker) execute(ctx context.Context, job Job, emitProgress func(JobProgress)) (result json.RawMessage, sealedResult *SealedEnvelope, usage Usage, execution *ExecutionMetadata, resultErr error) {
	started := time.Now()
	payload := []byte(job.Payload)
	shared := ""
	encryptionContext := EncryptionContext{}
	if job.SealedPayload != nil {
		var err error
		encryptionContext, err = job.EncryptionContextForNode(w.identity.NodeID)
		if err != nil {
			return nil, nil, Usage{}, nil, fmt.Errorf("validate encrypted job context: %w", err)
		}
		payload, shared, err = OpenWith(w.identity.PrivateKey, job.SealedPayload, JobAAD(encryptionContext))
		if err != nil {
			return nil, nil, Usage{}, nil, fmt.Errorf("decrypt job: %w", err)
		}
	}
	if !json.Valid(payload) {
		return nil, nil, Usage{}, nil, errors.New("job payload must be valid JSON")
	}
	requirements, err := w.applyPolicy(job.Requirements)
	if err != nil {
		return nil, nil, Usage{}, nil, err
	}
	localRoute := ""
	if provider := strings.TrimSpace(requirements.Provider); provider != "" && !strings.EqualFold(provider, "browser") {
		localRoute, err = w.resolveLocalPrimaryRoute(ctx, requirements)
		if err != nil {
			return nil, nil, Usage{}, nil, err
		}
	}
	localJobID := localExecutionID(job)
	payload, err = prepareLocalPayload(payload, requirements, localJobID, job.OwnerSubject, job.TenantID)
	if err != nil {
		return nil, nil, Usage{}, nil, err
	}
	if localRoute != "" {
		payload, err = bindLocalRoute(payload, localRoute)
		if err != nil {
			return nil, nil, Usage{}, nil, err
		}
	}
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
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(w.cfg.LocalURL, "/")+"/v1/jobs?compact=1", bytes.NewReader(payload))
	if err != nil {
		return nil, nil, Usage{}, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+w.cfg.LocalToken)
	// Bind the worker-approved task to the local execution boundary. The local
	// service rejects a payload route whose authoritative task differs, so a
	// producer cannot declare an allowed task while selecting a more privileged
	// local route inside the opaque payload.
	request.Header.Set("X-ContextBridge-Expected-Task", requirements.Task)
	response, err := w.client.Do(request)
	if err != nil {
		return nil, nil, Usage{}, nil, err
	}
	defer response.Body.Close()
	raw, err := readLocalSubmissionResponse(response.Body)
	if err != nil {
		return nil, nil, Usage{}, nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, Usage{}, nil, fmt.Errorf("local bridge returned %s: %s", response.Status, truncate(string(raw), 500))
	}
	usage = extractUsage(raw)
	usage.ComputeMS = uint64(time.Since(started).Milliseconds())
	execution = extractLocalExecutionMetadata(raw)
	if outputErr := localOutputError(raw); outputErr != "" {
		return nil, nil, usage, execution, errors.New(outputErr)
	}
	// The outer cluster job already stores the submitted payload. Do not send
	// large or sensitive request fields (especially image_base64) back across
	// the network a second time merely because the local bridge echoes its Job
	// in the Submission envelope.
	raw, err = compactLocalSubmission(raw)
	if err != nil {
		// Fail closed: a successful local response must never cause the original
		// prompt, documents, or image bytes to be echoed back to the relay merely
		// because response compaction failed.
		return nil, nil, usage, nil, fmt.Errorf("compact local result: %w", err)
	}
	// Do not fill per-job resource fields from node-wide RAM, VRAM, or GPU
	// utilization snapshots. Other processes and concurrent jobs share those
	// counters, so attributing their totals to this job would be misleading.
	// Peak resource fields remain available for engine-reported, attributable
	// measurements.
	if shared != "" {
		sealed, sealErr := SealResponse(shared, raw, ResultAAD(encryptionContext))
		return nil, sealed, usage, execution, sealErr
	}
	return json.RawMessage(raw), nil, usage, execution, nil
}

func (w *Worker) resolveLocalPrimaryRoute(parent context.Context, requirements Requirements) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(w.cfg.LocalURL, "/")+"/v1/status", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+w.cfg.LocalToken)
	response, err := w.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("resolve local route: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return "", fmt.Errorf("resolve local route: status returned %s: %s", response.Status, truncate(string(body), 300))
	}
	var status struct {
		Routes map[string]struct {
			Task     string `json:"task"`
			Model    string `json:"model"`
			Provider string `json:"provider"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&status); err != nil {
		return "", fmt.Errorf("resolve local route: %w", err)
	}
	provider := strings.TrimSpace(requirements.Provider)
	task := strings.TrimSpace(requirements.Task)
	if task == "" {
		task = "generation"
	}
	model := strings.TrimSpace(requirements.Model)
	candidates := make([]string, 0)
	for name, route := range status.Routes {
		routeTask := strings.TrimSpace(route.Task)
		if routeTask == "" {
			routeTask = "generation"
		}
		if !strings.EqualFold(strings.TrimSpace(route.Provider), provider) || !strings.EqualFold(routeTask, task) {
			continue
		}
		if routeModel := strings.TrimSpace(route.Model); model != "" && routeModel != "" && !strings.EqualFold(routeModel, model) {
			continue
		}
		candidates = append(candidates, name)
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("provider_not_bound_to_local_route: provider %s has no primary %s route", provider, task)
	}
	sort.Strings(candidates)
	for _, candidate := range candidates {
		if strings.EqualFold(candidate, provider) {
			return candidate, nil
		}
	}
	return candidates[0], nil
}

func bindLocalRoute(payload []byte, route string) ([]byte, error) {
	var job map[string]json.RawMessage
	if err := json.Unmarshal(payload, &job); err != nil || job == nil {
		return nil, errors.New("cluster job payload must be a JSON object")
	}
	rawRoute, _ := json.Marshal(route)
	job["route"] = rawRoute
	return json.Marshal(job)
}

func extractLocalExecutionMetadata(raw []byte) *ExecutionMetadata {
	var submission struct {
		BrowserTabID        int  `json:"contextbridge_browser_tab_id"`
		EphemeralBrowserTab bool `json:"contextbridge_ephemeral_browser_tab"`
	}
	if json.Unmarshal(raw, &submission) != nil || submission.BrowserTabID <= 0 {
		return nil
	}
	return &ExecutionMetadata{BrowserTabID: submission.BrowserTabID, EphemeralBrowserTab: submission.EphemeralBrowserTab}
}

const maximumLocalSubmissionBytes int64 = MaximumJobResultBytes

func readLocalSubmissionResponse(reader io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, maximumLocalSubmissionBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximumLocalSubmissionBytes {
		return nil, fmt.Errorf("local bridge response exceeds %d MiB", maximumLocalSubmissionBytes>>20)
	}
	return raw, nil
}

func compactLocalSubmission(raw []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	delete(envelope, "contextbridge_browser_tab_id")
	delete(envelope, "contextbridge_ephemeral_browser_tab")
	jobRaw, ok := envelope["job"]
	if !ok {
		return nil, errors.New("local bridge response is missing the required job object")
	}
	var job map[string]json.RawMessage
	if err := json.Unmarshal(jobRaw, &job); err != nil {
		return nil, err
	}
	for _, field := range []string{
		"prompt", "text", "texts", "documents", "query", "image_base64",
		"contextbridge_session_key", "contextbridge_browser_tab_id",
	} {
		delete(job, field)
	}
	compactJob, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	envelope["job"] = compactJob
	for _, name := range []string{"output", "decision"} {
		raw, ok := envelope[name]
		if !ok || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		delete(value, "contextbridge_browser_tab_id")
		delete(value, "contextbridge_ephemeral_browser_tab")
		cleaned, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		envelope[name] = cleaned
	}
	compact, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return compact, nil
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

func prepareLocalPayload(payload []byte, requirements Requirements, localJobID string, owner ...string) ([]byte, error) {
	provider := strings.TrimSpace(requirements.Provider)
	var job map[string]json.RawMessage
	if err := json.Unmarshal(payload, &job); err != nil || job == nil {
		return nil, errors.New("job payload must be a JSON object")
	}
	if provider != "" {
		rawProvider, _ := json.Marshal(provider)
		job["provider"] = rawProvider
	} else {
		delete(job, "provider")
	}
	if task := strings.TrimSpace(requirements.Task); task != "" {
		rawTask, _ := json.Marshal(task)
		job["task"] = rawTask
	}
	if model := strings.TrimSpace(requirements.Model); model != "" {
		rawModel, _ := json.Marshal(model)
		job["model"] = rawModel
	} else {
		delete(job, "model")
	}
	rawID, _ := json.Marshal(localJobID)
	job["id"] = rawID
	// These fields cross the browser lease/session boundary. Producer payload
	// hints are never authoritative; only authenticated requirements may add
	// them back below.
	delete(job, "contextbridge_browser_tab_id")
	delete(job, "contextbridge_session_key")
	session := canonicalSessionID(requirements.SessionID)
	rawSession, _ := json.Marshal(session)
	job["session_id"] = rawSession
	if profile := strings.TrimSpace(requirements.BrowserProfile); profile != "" && strings.EqualFold(provider, "browser") {
		rawProfile, _ := json.Marshal(profile)
		job["browser_profile"] = rawProfile
	} else {
		delete(job, "browser_profile")
	}
	if reasoning := strings.TrimSpace(requirements.Reasoning); reasoning != "" && strings.EqualFold(provider, "browser") {
		rawReasoning, _ := json.Marshal(reasoning)
		job["reasoning"] = rawReasoning
	} else {
		delete(job, "reasoning")
	}
	if requirements.BrowserTabID > 0 && strings.EqualFold(provider, "browser") {
		rawTabID, _ := json.Marshal(requirements.BrowserTabID)
		job["contextbridge_browser_tab_id"] = rawTabID
	}
	// A browser tab is a security boundary between producer conversations.
	// Derive its internal binding from the authenticated producer, never from a
	// producer-supplied scope field. Provider-less cluster jobs may resolve via
	// an operator-owned local route, so they receive the same safe binding in
	// case that route is a browser; explicit non-browser jobs do not need it.
	if provider == "" || strings.EqualFold(provider, "browser") {
		producer := "local"
		if len(owner) > 0 && owner[0] != "" {
			producer = owner[0]
		}
		sum := sha256.Sum256([]byte(producer + "\x00" + session))
		key, _ := json.Marshal(fmt.Sprintf("cb:%x", sum[:]))
		job["contextbridge_session_key"] = key
	}
	metadata := map[string]json.RawMessage{}
	if raw := bytes.TrimSpace(job["metadata"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
			return nil, errors.New("cluster job metadata must be a JSON object")
		}
	}
	delete(metadata, "contextbridge_new_chat")
	delete(metadata, "contextbridge_new_chat_per_job")
	// Recovery is granted only by the extension after it verifies a durable
	// sent-unknown claim and the exact owned turn. A producer must never be able
	// to turn an initial or provider-less job into observation-only page reads.
	for _, field := range []string{
		"contextbridge_resume_only",
		"contextbridge_baseline_text",
		"contextbridge_baseline_response_count",
		"contextbridge_baseline_response_identity",
		"contextbridge_baseline_text_digest",
		"contextbridge_model_fallbacks",
		"contextbridge_reasoning_fallbacks",
	} {
		delete(metadata, field)
	}
	if requirements.BrowserFreshChat || requirements.BrowserEphemeralChat {
		metadata["contextbridge_new_chat"] = json.RawMessage(`true`)
		if requirements.BrowserEphemeralChat {
			metadata["contextbridge_new_chat_per_job"] = json.RawMessage(`true`)
		}
	}
	rawMetadata, _ := json.Marshal(metadata)
	job["metadata"] = rawMetadata
	if strings.EqualFold(requirements.Task, "rag_ingest") || strings.EqualFold(requirements.Task, "rag_query") {
		logicalTenant := ""
		if len(owner) > 1 {
			logicalTenant = strings.TrimSpace(owner[1])
		}
		if logicalTenant == "" {
			_ = json.Unmarshal(job["tenant_id"], &logicalTenant)
			logicalTenant = strings.TrimSpace(logicalTenant)
		}
		if logicalTenant == "" {
			return nil, errors.New("cluster RAG jobs require tenant_id")
		}
		producer := "local"
		if len(owner) > 0 && strings.TrimSpace(owner[0]) != "" {
			producer = strings.TrimSpace(owner[0])
		}
		// The local vector store trusts tenant_id as its partition key. Namespace
		// it with the authenticated producer so two relay clients cannot select
		// each other's partition by supplying the same public tenant label.
		sum := sha256.Sum256([]byte(producer + "\x00" + logicalTenant))
		tenantKey, _ := json.Marshal(fmt.Sprintf("cbt:%x", sum[:]))
		job["tenant_id"] = tenantKey
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
					ID                  int      `json:"id"`
					Profile             string   `json:"profile"`
					State               string   `json:"state"`
					SessionKey          string   `json:"session_key"`
					SessionKeySupported bool     `json:"session_key_supported"`
					CanCreateFreshChat  bool     `json:"can_create_fresh_chat"`
					DefaultFreshChat    bool     `json:"default_fresh_chat"`
					CurrentModel        string   `json:"current_model"`
					CurrentReasoning    string   `json:"current_reasoning"`
					Models              []string `json:"models"`
					ReasoningLevels     []string `json:"reasoning_levels"`
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
						Name                 string   `json:"name"`
						Size                 int64    `json:"size_bytes"`
						VRAM                 int64    `json:"vram_bytes"`
						Available            bool     `json:"available"`
						Loaded               bool     `json:"loaded"`
						Capabilities         []string `json:"capabilities"`
						CapabilitiesVerified bool     `json:"capabilities_verified"`
						CapabilitySource     string   `json:"capability_source"`
					} `json:"models"`
				} `json:"engines"`
			} `json:"runtime"`
		}
		if response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&status) == nil {
			capability.QueueDepth = status.Queued
			// The map's presence tells v0.5.66+ schedulers that provider-specific
			// automatic route readiness is authoritative. This prevents a ready
			// browser route from making an incompatible local provider appear able
			// to execute the same global task.
			capability.AutomaticTasks = map[string][]string{}
			// Browser tabs are separate serial UI slots. They must not lower the
			// worker-wide limit for Ollama or other local-model jobs.
			if len(w.cfg.AllowedProviders) == 0 || containsFold(w.cfg.AllowedProviders, "browser") {
				capability.BrowserTabs = status.Browser.ActiveTabs
				capability.BrowserBusy = status.Browser.BusyTabs
				for _, tab := range status.Browser.Tabs {
					modelChoices := cleanList(tab.Models, MaximumBrowserModelChoices, MaximumBrowserChoiceBytes)
					reasoningLevels := cleanList(tab.ReasoningLevels, MaximumBrowserReasoningLevels, MaximumBrowserChoiceBytes)
					capability.BrowserSessions = append(capability.BrowserSessions, BrowserSessionCapability{
						TabID: tab.ID, Profile: truncate(tab.Profile, 40), State: truncate(tab.State, 40),
						SessionKey: truncate(tab.SessionKey, MaximumBrowserSessionKeyBytes), SessionKeySupported: tab.SessionKeySupported,
						CanCreateFreshChat: tab.CanCreateFreshChat, DefaultFreshChat: tab.DefaultFreshChat,
						CurrentModel: truncate(tab.CurrentModel, 100), CurrentReasoning: truncate(tab.CurrentReasoning, 100),
						ModelChoices: modelChoices, ReasoningLevels: reasoningLevels,
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
			runtimeModels := map[string]ModelCapability{}
			runtimeModelOrder := make([]string, 0)
			for provider, engine := range status.Runtime.Engines {
				if engine.State != "online" || !providerAllowed(provider) {
					continue
				}
				for _, model := range engine.Models {
					if strings.TrimSpace(model.Name) == "" {
						continue
					}
					tasks, vision, embedding := modelTasksFromCapabilities(model.Name, model.Capabilities)
					runtimeModel := ModelCapability{Name: model.Name, Provider: provider, Size: model.Size, VRAM: model.VRAM, Available: model.Available || model.Loaded, Loaded: model.Loaded, Vision: vision, Embedding: embedding, Tasks: allowedModelTasks(tasks), CapabilitiesVerified: model.CapabilitiesVerified, CapabilitySource: model.CapabilitySource}
					key := modelCapabilityKey(provider, model.Name)
					if existing, ok := runtimeModels[key]; ok {
						runtimeModels[key] = mergeModelCapability(existing, runtimeModel)
						continue
					}
					runtimeModels[key] = runtimeModel
					runtimeModelOrder = append(runtimeModelOrder, key)
				}
			}
			modelIndexes := map[string]int{}
			addModel := func(model ModelCapability) {
				if strings.TrimSpace(model.Name) == "" {
					return
				}
				key := modelCapabilityKey(model.Provider, model.Name)
				if index, ok := modelIndexes[key]; ok {
					capability.Models[index] = mergeModelCapability(capability.Models[index], model)
					return
				}
				modelIndexes[key] = len(capability.Models)
				capability.Models = append(capability.Models, model)
			}
			runtimeProviderSupportsTask := func(provider, task string) (bool, bool) {
				inventoryAvailable := false
				for _, runtimeModel := range runtimeModels {
					if !strings.EqualFold(runtimeModel.Provider, provider) {
						continue
					}
					if runtimeModel.Available {
						inventoryAvailable = true
					}
					if runtimeModel.Available && runtimeModel.CapabilitiesVerified && modelAllowed(runtimeModel.Name) && containsFold(runtimeModel.Tasks, task) {
						return true, true
					}
				}
				return false, inventoryAvailable
			}
			providerSupportsRouteTask := func(provider, routeModel, task string) bool {
				model := strings.TrimSpace(routeModel)
				if model == "" || strings.EqualFold(model, "auto") {
					// Ollama auto-selection is fail closed: a live daemon is not enough.
					// At least one currently available model must carry provider-verified
					// capability evidence for this task. Other engines retain their
					// historical route behavior until they publish equivalent evidence.
					if supports, inventoryAvailable := runtimeProviderSupportsTask(provider, task); inventoryAvailable || strings.EqualFold(provider, "ollama") {
						return supports
					}
					return true
				}
				if runtimeModel, knownByRuntime := runtimeModels[modelCapabilityKey(provider, model)]; knownByRuntime {
					if !runtimeModel.Available || !modelAllowed(runtimeModel.Name) {
						return false
					}
					if runtimeModel.CapabilitiesVerified || !strings.EqualFold(provider, "ollama") {
						return containsFold(runtimeModel.Tasks, task)
					}
					// An explicit configured model is an operator choice. Older
					// Ollama versions may prove that it is installed without
					// publishing capability metadata; keep that fixed route usable.
					// Name-inferred tasks are never used to make an automatic route
					// eligible, and verified incompatible evidence still rejects it.
					return true
				}
				if _, inventoryAvailable := runtimeProviderSupportsTask(provider, task); inventoryAvailable {
					// A non-empty inventory is authoritative: a configured fixed
					// model absent from it cannot be loaded by this provider.
					return false
				}
				return true
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
						if !seenProviders[provider] {
							capability.Providers = append(capability.Providers, provider)
							seenProviders[provider] = true
						}
						providerSupportsTask := providerSupportsRouteTask(provider, route.Model, task)
						if providerSupportsTask && (len(w.cfg.AllowedTasks) == 0 || containsFold(w.cfg.AllowedTasks, task)) {
							if !containsFold(capability.AutomaticTasks[provider], task) {
								capability.AutomaticTasks[provider] = append(capability.AutomaticTasks[provider], task)
							}
						}
						routeReady = routeReady || providerSupportsTask
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
				if route.Model != "" && providerOnline(route.Provider) && allowedRouteModel && providerSupportsRouteTask(route.Provider, route.Model, task) {
					// A runtime inventory entry for the same provider and model is
					// authoritative. Route defaults describe intent, not what the
					// model can execute, and therefore must never add generation or
					// vision to an embedding/image-generation-only model.
					if _, knownByRuntime := runtimeModels[modelCapabilityKey(route.Provider, route.Model)]; !knownByRuntime {
						vision, embedding := modelFeatures(route.Model, task)
						if tasks := allowedModelTasks(modelTasks(task, vision, embedding)); len(tasks) > 0 {
							addModel(ModelCapability{Name: route.Model, Tasks: tasks, Provider: route.Provider, Vision: vision, Embedding: embedding})
						}
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
			}
			for _, key := range runtimeModelOrder {
				model := runtimeModels[key]
				if modelAllowed(model.Name) {
					addModel(model)
				}
			}
			for _, tab := range capability.BrowserSessions {
				if !providerAllowed("browser") {
					break
				}
				models := append([]string{}, tab.ModelChoices...)
				if tab.CurrentModel != "" {
					models = append(models, tab.CurrentModel)
				}
				for _, model := range models {
					if model == "" || !browserModelAllowed(model) {
						continue
					}
					if tasks := allowedModelTasks([]string{"generation", "vision"}); len(tasks) > 0 {
						addModel(ModelCapability{Name: model, Provider: "browser", Vision: true, Tasks: tasks})
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

func endpoint(base, path string) string { return strings.TrimRight(base, "/") + path }
func truncate(value string, limit int) string {
	return truncateUTF8Bytes(value, limit)
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
		case "reserved_cost_usd":
			usage.ReservedCostUSD = maxFloat64(usage.ReservedCostUSD, number)
		case "estimated_cost_usd":
			usage.EstimatedCostUSD = maxFloat64(usage.EstimatedCostUSD, number)
		}
	})
	walkStrings(value, func(key, text string) {
		switch strings.ToLower(key) {
		case "cost_status":
			usage.CostStatus = strings.ToLower(strings.TrimSpace(text))
		case "cost_source":
			usage.CostSource = truncateUTF8Bytes(strings.TrimSpace(text), 200)
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

func walkStrings(value interface{}, visit func(string, string)) {
	switch current := value.(type) {
	case map[string]interface{}:
		for key, child := range current {
			if text, ok := child.(string); ok {
				visit(key, text)
			}
			walkStrings(child, visit)
		}
	case []interface{}:
		for _, child := range current {
			walkStrings(child, visit)
		}
	}
}

func maxU64(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}

func maxFloat64(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

func modelCapabilityKey(provider, name string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	name = strings.TrimSpace(name)
	if strings.EqualFold(provider, "browser") {
		name = strings.Join(strings.Fields(name), " ")
	}
	return provider + "\x00" + strings.ToLower(name)
}

func mergeModelCapability(current, incoming ModelCapability) ModelCapability {
	mergeCapabilities := current.CapabilitiesVerified == incoming.CapabilitiesVerified
	replaceCapabilities := incoming.CapabilitiesVerified && !current.CapabilitiesVerified
	if replaceCapabilities {
		// Provider evidence replaces name inference; never retain an inferred
		// generation/vision flag under a verified embedding-only label.
		current.Tasks = append([]string(nil), incoming.Tasks...)
		current.Vision = incoming.Vision
		current.Embedding = incoming.Embedding
	} else if mergeCapabilities {
		for _, task := range incoming.Tasks {
			if !containsFold(current.Tasks, task) {
				current.Tasks = append(current.Tasks, task)
			}
		}
		current.Vision = current.Vision || incoming.Vision
		current.Embedding = current.Embedding || incoming.Embedding
	}
	current.Available = current.Available || incoming.Available
	current.Loaded = current.Loaded || incoming.Loaded
	if incoming.CapabilitiesVerified {
		current.CapabilitiesVerified = true
		if incoming.CapabilitySource != "" {
			current.CapabilitySource = incoming.CapabilitySource
		}
	} else if current.CapabilitySource == "" {
		current.CapabilitySource = incoming.CapabilitySource
	}
	if incoming.Size > current.Size {
		current.Size = incoming.Size
	}
	if incoming.VRAM > current.VRAM {
		current.VRAM = incoming.VRAM
	}
	return current
}

func modelFeatures(name, task string) (bool, bool) {
	lower := strings.ToLower(name)
	vision := task == "vision" || strings.Contains(lower, "vl") || strings.Contains(lower, "llava") || strings.Contains(lower, "gemma3") || strings.Contains(lower, "vision")
	embedding := task == "embedding" || strings.Contains(lower, "embed") || strings.Contains(lower, "jina") || strings.Contains(lower, "nomic") || strings.Contains(lower, "bge")
	return vision, embedding
}

func modelFeaturesFromCapabilities(name, task string, capabilities []string) (bool, bool) {
	if len(capabilities) == 0 {
		return modelFeatures(name, task)
	}
	_, vision, embedding := modelTasksFromCapabilities(name, capabilities)
	return vision, embedding
}

// modelTasksFromCapabilities derives schedulable tasks from a runtime's
// authoritative model capabilities. In particular, an embedding-only model
// must never inherit generation merely because generation is the common case.
func modelTasksFromCapabilities(name string, capabilities []string) ([]string, bool, bool) {
	if len(capabilities) == 0 {
		vision, embedding := modelFeatures(name, "generation")
		return modelTasks("generation", vision, embedding), vision, embedding
	}
	tasks := make([]string, 0, 3)
	vision, embedding := false, false
	appendTask := func(task string) {
		if !containsFold(tasks, task) {
			tasks = append(tasks, task)
		}
	}
	for _, capability := range capabilities {
		switch strings.ToLower(strings.TrimSpace(capability)) {
		case "text", "completion", "generate", "generation":
			appendTask("generation")
		case "vision", "image_understanding", "image-analysis", "ocr":
			vision = true
			appendTask("vision")
		case "embedding", "embeddings", "embed":
			embedding = true
			appendTask("embedding")
		}
	}
	return tasks, vision, embedding
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
