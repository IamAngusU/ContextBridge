package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Store struct {
	dir       string
	mu        sync.Mutex
	queued    map[string]*queuedJob
	completed map[string]Output
	browser   BrowserClientStatus
	tunnel    TunnelStatus
	activity  []Activity
	metrics   Metrics
}

type TunnelStatus struct {
	Connected  bool      `json:"connected"`
	State      string    `json:"state"`
	Target     string    `json:"target,omitempty"`
	Transport  string    `json:"transport,omitempty"`
	LocalPort  int       `json:"local_port,omitempty"`
	RemotePort int       `json:"remote_port,omitempty"`
	LastSeen   time.Time `json:"last_seen,omitempty"`
}

type Metrics struct {
	JobsTotal                 uint64            `json:"jobs_total"`
	JobsFailed                uint64            `json:"jobs_failed"`
	LatencyTotalMS            uint64            `json:"latency_total_ms"`
	ByRoute                   map[string]uint64 `json:"by_route"`
	ByTask                    map[string]uint64 `json:"by_task"`
	ByProvider                map[string]uint64 `json:"by_provider"`
	ByModel                   map[string]uint64 `json:"by_model"`
	ByFlag                    map[string]uint64 `json:"by_flag"`
	ProviderLatency           map[string]uint64 `json:"provider_latency_ms"`
	ProviderSamples           map[string]uint64 `json:"provider_latency_samples"`
	ProviderFailures          map[string]uint64 `json:"provider_failures"`
	ByAttemptedProvider       map[string]uint64 `json:"by_attempted_provider"`
	AttemptedProviderFailures map[string]uint64 `json:"attempted_provider_failures"`
	ByAttemptedModel          map[string]uint64 `json:"by_attempted_model"`
	ByReasoning               map[string]uint64 `json:"by_reasoning"`
	ReasoningFailures         map[string]uint64 `json:"reasoning_failures"`
	ModelFailures             map[string]uint64 `json:"model_failures"`
	BySelection               map[string]uint64 `json:"by_selection"`
	SelectionFailures         map[string]uint64 `json:"selection_failures"`
	EmbeddingVectors          uint64            `json:"embedding_vectors"`
	UpdatedAt                 time.Time         `json:"updated_at"`
}

type BrowserClientStatus struct {
	Connected        bool               `json:"connected"`
	State            string             `json:"state"`
	Origin           string             `json:"origin,omitempty"`
	TabTitle         string             `json:"tab_title,omitempty"`
	ProfileLabel     string             `json:"profile_label,omitempty"`
	SelectorsReady   bool               `json:"selectors_ready"`
	ExtensionVersion string             `json:"extension_version,omitempty"`
	Browser          string             `json:"browser,omitempty"`
	ActiveTabs       int                `json:"active_tabs,omitempty"`
	BusyTabs         int                `json:"busy_tabs,omitempty"`
	Tabs             []BrowserTabStatus `json:"tabs,omitempty"`
	LastSeen         time.Time          `json:"last_seen,omitempty"`
}

type BrowserTabStatus struct {
	ID               int                 `json:"id,omitempty"`
	Origin           string              `json:"origin,omitempty"`
	Title            string              `json:"title,omitempty"`
	Profile          string              `json:"profile,omitempty"`
	State            string              `json:"state,omitempty"`
	CurrentModel     string              `json:"current_model,omitempty"`
	CurrentReasoning string              `json:"current_reasoning,omitempty"`
	Models           []string            `json:"models,omitempty"`
	ReasoningLevels  []string            `json:"reasoning_levels,omitempty"`
	ModelScan        string              `json:"model_scan,omitempty"`
	ReasoningScan    string              `json:"reasoning_scan,omitempty"`
	LastFailure      *BrowserTabFailure  `json:"last_failure,omitempty"`
	DOM              *BrowserDOMSnapshot `json:"dom,omitempty"`
}

type BrowserTabFailure struct {
	Code   string    `json:"code"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// BrowserDOMSnapshot contains selector diagnostics only. It deliberately never
// carries the prompt value, chat text, file contents, cookies, or full HTML.
type BrowserDOMSnapshot struct {
	CapturedAt               time.Time           `json:"captured_at"`
	Inputs                   []BrowserDOMControl `json:"inputs,omitempty"`
	InputHasText             bool                `json:"input_has_text,omitempty"`
	InputCharacters          int                 `json:"input_characters,omitempty"`
	Submit                   []BrowserDOMControl `json:"submit,omitempty"`
	FileInputs               []BrowserDOMControl `json:"file_inputs,omitempty"`
	Tools                    []BrowserDOMControl `json:"tools,omitempty"`
	AssistantTurns           int                 `json:"assistant_turns"`
	LastResponseCharacters   int                 `json:"last_response_characters,omitempty"`
	LastResponseBusy         bool                `json:"last_response_busy,omitempty"`
	BusyIndicators           []string            `json:"busy_indicators,omitempty"`
	StopButtonDisabled       bool                `json:"stop_button_disabled,omitempty"`
	StopButtonSpinning       bool                `json:"stop_button_spinning,omitempty"`
	LastResponseImages       int                 `json:"last_response_images"`
	LastResponseLoadedImages int                 `json:"last_response_loaded_images,omitempty"`
	ImageProgress            int                 `json:"image_progress,omitempty"`
}

type BrowserDOMControl struct {
	Tag       string `json:"tag,omitempty"`
	ID        string `json:"id,omitempty"`
	TestID    string `json:"test_id,omitempty"`
	Role      string `json:"role,omitempty"`
	AriaLabel string `json:"aria_label,omitempty"`
	Text      string `json:"text,omitempty"`
	Type      string `json:"type,omitempty"`
	Accept    string `json:"accept,omitempty"`
	HasPopup  string `json:"has_popup,omitempty"`
	Expanded  string `json:"expanded,omitempty"`
	Visible   bool   `json:"visible"`
	Disabled  bool   `json:"disabled,omitempty"`
	Multiple  bool   `json:"multiple,omitempty"`
	Directory bool   `json:"directory,omitempty"`
}

type Activity struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	JobID   string    `json:"job_id,omitempty"`
}

type queuedJob struct {
	job       Job
	profile   interface{}
	deadline  time.Time
	leasedTil time.Time
	done      chan Output
	progress  *BrowserProgress
}

func (s *Store) UpdateBrowserProgress(id string, progress BrowserProgress) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	if !ok || time.Now().After(item.deadline) {
		if ok {
			delete(s.queued, id)
		}
		return false
	}
	if item.progress != nil && progress.Sequence <= item.progress.Sequence {
		return true
	}
	progress.UpdatedAt = time.Now().UTC()
	copy := progress
	item.progress = &copy
	return true
}

func (s *Store) BrowserProgress(id string) (BrowserProgress, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	if !ok || time.Now().After(item.deadline) {
		if ok {
			delete(s.queued, id)
		}
		return BrowserProgress{}, false, false
	}
	if item.progress == nil {
		return BrowserProgress{}, true, false
	}
	return *item.progress, true, true
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0700); err != nil {
		return nil, err
	}
	store := &Store{
		dir:       dir,
		queued:    map[string]*queuedJob{},
		completed: map[string]Output{},
		metrics: Metrics{
			ByRoute: map[string]uint64{}, ByTask: map[string]uint64{}, ByProvider: map[string]uint64{},
			ByModel: map[string]uint64{}, ByFlag: map[string]uint64{}, ProviderLatency: map[string]uint64{}, ProviderSamples: map[string]uint64{}, ProviderFailures: map[string]uint64{},
			ByAttemptedProvider: map[string]uint64{}, AttemptedProviderFailures: map[string]uint64{}, ByAttemptedModel: map[string]uint64{}, ByReasoning: map[string]uint64{}, ReasoningFailures: map[string]uint64{}, ModelFailures: map[string]uint64{}, BySelection: map[string]uint64{}, SelectionFailures: map[string]uint64{},
		},
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "metrics.json"))
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &store.metrics)
		if store.metrics.ByRoute == nil {
			store.metrics.ByRoute = map[string]uint64{}
		}
		if store.metrics.ByTask == nil {
			store.metrics.ByTask = map[string]uint64{}
		}
		if store.metrics.ByProvider == nil {
			store.metrics.ByProvider = map[string]uint64{}
		}
		if store.metrics.ByModel == nil {
			store.metrics.ByModel = map[string]uint64{}
		}
		if store.metrics.ByFlag == nil {
			store.metrics.ByFlag = map[string]uint64{}
		}
		if store.metrics.ProviderLatency == nil {
			store.metrics.ProviderLatency = map[string]uint64{}
		}
		if store.metrics.ProviderSamples == nil {
			store.metrics.ProviderSamples = map[string]uint64{}
		}
		if store.metrics.ProviderFailures == nil {
			store.metrics.ProviderFailures = map[string]uint64{}
		}
		if store.metrics.ByAttemptedProvider == nil {
			store.metrics.ByAttemptedProvider = map[string]uint64{}
		}
		if store.metrics.AttemptedProviderFailures == nil {
			store.metrics.AttemptedProviderFailures = map[string]uint64{}
		}
		if store.metrics.ByAttemptedModel == nil {
			store.metrics.ByAttemptedModel = map[string]uint64{}
		}
		if store.metrics.ByReasoning == nil {
			store.metrics.ByReasoning = map[string]uint64{}
		}
		if store.metrics.ReasoningFailures == nil {
			store.metrics.ReasoningFailures = map[string]uint64{}
		}
		if store.metrics.ModelFailures == nil {
			store.metrics.ModelFailures = map[string]uint64{}
		}
		if store.metrics.BySelection == nil {
			store.metrics.BySelection = map[string]uint64{}
		}
		if store.metrics.SelectionFailures == nil {
			store.metrics.SelectionFailures = map[string]uint64{}
		}
	}
	return store, nil
}

func (s *Store) SaveJob(job Job) error {
	raw, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(s.dir, "jobs", storageID(job.ID)+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(raw, '\n'))
	return err
}

func (s *Store) SaveOutput(id string, output Output) error {
	payload := interface{}(output)
	if output.Mode == "decision" && output.Decision != nil {
		payload = *output.Decision
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "jobs", storageID(id)+".result.json"), append(raw, '\n'), 0600)
}

func (s *Store) RecordCompleted(job Job, output Output) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed[job.ID] = output
	s.metrics.JobsTotal++
	if output.Error != "" {
		s.metrics.JobsFailed++
	}
	if output.LatencyMS > 0 {
		s.metrics.LatencyTotalMS += uint64(output.LatencyMS)
	}
	route := job.Route
	if route == "" {
		route = "default"
	}
	task := job.Task
	if task == "" {
		task = job.Kind
	}
	if task == "" {
		task = output.Mode
	}
	provider := output.Provider
	model := output.Model
	if provider == "" && output.Decision != nil {
		provider = output.Decision.Provider
	}
	if model == "" && output.Decision != nil {
		model = output.Decision.Model
	}
	if provider == "" {
		provider = "unknown"
	}
	if model == "" {
		model = "unknown"
	}
	s.metrics.ByRoute[route]++
	s.metrics.ByTask[task]++
	s.metrics.ByProvider[provider]++
	s.metrics.ByModel[model]++
	attemptedProvider := strings.TrimSpace(job.Provider)
	if attemptedProvider == "" {
		attemptedProvider = strings.TrimSpace(job.routeProvider)
	}
	if attemptedProvider == "" {
		attemptedProvider = provider
	}
	attemptedModel := strings.TrimSpace(job.Model)
	if attemptedModel == "" {
		attemptedModel = strings.TrimSpace(output.SelectedModel)
	}
	if attemptedModel == "" {
		attemptedModel = model
	}
	reasoning := strings.TrimSpace(job.Reasoning)
	if reasoning == "" {
		reasoning = strings.TrimSpace(output.SelectedReasoning)
	}
	if reasoning == "" {
		reasoning = "unknown"
	}
	selection := attemptedProvider + " / " + attemptedModel + " / " + reasoning
	s.metrics.ByAttemptedProvider[attemptedProvider]++
	s.metrics.ByAttemptedModel[attemptedModel]++
	s.metrics.ByReasoning[reasoning]++
	s.metrics.BySelection[selection]++
	if output.Error != "" {
		s.metrics.AttemptedProviderFailures[attemptedProvider]++
		s.metrics.ModelFailures[attemptedModel]++
		s.metrics.ReasoningFailures[reasoning]++
		s.metrics.SelectionFailures[selection]++
	}
	if output.LatencyMS > 0 {
		s.metrics.ProviderLatency[provider] += uint64(output.LatencyMS)
		s.metrics.ProviderSamples[provider]++
	}
	if output.Error != "" {
		s.metrics.ProviderFailures[provider]++
	}
	if output.Decision != nil {
		for _, flag := range output.Decision.Flags {
			if flag != "" {
				s.metrics.ByFlag[flag]++
			}
		}
	}
	s.metrics.EmbeddingVectors += uint64(len(output.Embeddings))
	s.metrics.UpdatedAt = time.Now().UTC()
	s.persistMetricsLocked()
}

func storageID(id string) string {
	if jobIDPattern.MatchString(id) && !strings.Contains(id, "..") {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "job-" + hex.EncodeToString(sum[:16])
}

func (s *Store) Queue(job Job, profile interface{}, timeout time.Duration) <-chan Output {
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan Output, 1)
	s.queued[job.ID] = &queuedJob{
		job:      job,
		profile:  profile,
		deadline: time.Now().Add(timeout),
		done:     done,
	}
	s.addActivityLocked("queued", "Browser job queued", job.ID)
	return done
}

func (s *Store) NextBrowserJob(profile string, lease time.Duration) *browserJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, item := range s.queued {
		if now.After(item.deadline) {
			delete(s.queued, id)
			continue
		}
		if now.Before(item.leasedTil) {
			continue
		}
		if profile != "" {
			if p, ok := item.profile.(map[string]interface{}); ok {
				if name, _ := p["name"].(string); name != "" && name != profile {
					continue
				}
			}
		}
		item.leasedTil = now.Add(lease)
		return &browserJob{Job: item.job, Profile: item.profile, Deadline: item.deadline}
	}
	return nil
}

func (s *Store) Complete(id string, output Output) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	if !ok {
		return false
	}
	if time.Now().After(item.deadline) {
		delete(s.queued, id)
		return false
	}
	delete(s.queued, id)
	completed := cloneOutput(output)
	s.completed[id] = completed
	message := "Browser result received"
	if output.Decision != nil {
		message += ": " + output.Decision.Verdict
	} else if output.Error != "" {
		message += ": " + output.Error
	}
	s.addActivityLocked("completed", message, id)
	item.done <- completed
	close(item.done)
	return true
}

func cloneOutput(output Output) Output {
	clone := output
	clone.Artifacts = append([]Artifact{}, output.Artifacts...)
	if output.Decision != nil {
		decision := *output.Decision
		decision.Flags = append([]string{}, output.Decision.Flags...)
		clone.Decision = &decision
	}
	return clone
}

func (s *Store) Cancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.queued, id)
}

func (s *Store) Renew(id string, lease time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	if !ok || time.Now().After(item.deadline) {
		if ok {
			delete(s.queued, id)
		}
		return false
	}
	item.leasedTil = time.Now().Add(lease)
	return true
}

func (s *Store) BrowserCompletionContext(id string) (OutputSpec, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	if !ok {
		return OutputSpec{}, "", false
	}
	model := "browser-tab"
	if requested := strings.TrimSpace(item.job.Model); requested != "" {
		model = "browser:" + requested
	}
	if profile, profileOK := item.profile.(map[string]interface{}); profileOK {
		if name, _ := profile["name"].(string); name != "" && item.job.Model == "" {
			model = "browser:" + name
		}
	}
	return item.job.Output, model, true
}

func (s *Store) Stats() (queued, completed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queued), len(s.completed)
}

func (s *Store) RecordBrowserHeartbeat(status BrowserClientStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status.Connected = status.State != "paused"
	status.LastSeen = time.Now().UTC()
	wasConnected := s.browser.Connected && time.Since(s.browser.LastSeen) < 45*time.Second
	s.browser = status
	if status.Connected && !wasConnected {
		s.addActivityLocked("browser", "Browser extension connected", "")
	}
}

func (s *Store) BrowserStatus() BrowserClientStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.browser
	status.Connected = status.Connected && time.Since(status.LastSeen) < 45*time.Second
	return status
}

func (s *Store) RecordTunnelHeartbeat(status TunnelStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status.Connected = status.State == "connected"
	status.LastSeen = time.Now().UTC()
	wasConnected := s.tunnel.Connected && time.Since(s.tunnel.LastSeen) < 45*time.Second
	s.tunnel = status
	if status.Connected && !wasConnected {
		s.addActivityLocked("tunnel", "Secure tunnel connected", "")
	}
}

func (s *Store) TunnelStatus() TunnelStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.tunnel
	status.Connected = status.Connected && time.Since(status.LastSeen) < 45*time.Second
	if status.State == "" {
		status.State = "not configured"
	}
	if !status.Connected && status.State == "connected" {
		status.State = "stale"
	}
	return status
}

func (s *Store) Metrics() Metrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(s.metrics)
	var result Metrics
	_ = json.Unmarshal(raw, &result)
	return result
}

func (s *Store) Activity() []Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Activity, len(s.activity))
	copy(result, s.activity)
	return result
}

func (s *Store) AddActivity(kind, message, jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addActivityLocked(kind, message, jobID)
}

func (s *Store) addActivityLocked(kind, message, jobID string) {
	s.activity = append([]Activity{{
		Time: time.Now().UTC(), Kind: kind, Message: message, JobID: jobID,
	}}, s.activity...)
	if len(s.activity) > 60 {
		s.activity = s.activity[:60]
	}
}

func (s *Store) persistMetricsLocked() {
	raw, err := json.MarshalIndent(s.metrics, "", "  ")
	if err != nil {
		return
	}
	temporary := filepath.Join(s.dir, "metrics.json.tmp")
	if os.WriteFile(temporary, append(raw, '\n'), 0600) == nil {
		_ = os.Rename(temporary, filepath.Join(s.dir, "metrics.json"))
	}
}
