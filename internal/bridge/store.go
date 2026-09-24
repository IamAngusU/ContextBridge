package bridge

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Adapter heartbeats tolerate short scheduler stalls without hiding an
// explicit paused state.
const adapterHeartbeatGracePeriod = 90 * time.Second

const (
	maximumMetricsFileBytes   = 8 << 20
	maximumMetricDimensions   = 1024
	maximumMetricKeyBytes     = 256
	maximumRecentCompletions  = 4096
	localHistoryPruneInterval = 5 * time.Minute
)

type Store struct {
	dir              string
	mu               sync.Mutex
	historyMu        sync.Mutex
	queued           map[string]*queuedJob
	completed        map[string]struct{}
	completedOrder   []string
	completedTotal   int
	adapter          AdapterClientStatus
	adapters         map[string]AdapterClientStatus
	endpointCaps     map[adapterEndpointKey]adapterEndpointCapability
	tunnel           TunnelStatus
	activity         []Activity
	metrics          Metrics
	retention        localHistoryRetention
	nextHistoryPrune time.Time
}

type localHistoryRetention struct {
	maxAge     time.Duration
	maxRecords int
	maxBytes   int64
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

type AdapterClientStatus struct {
	Connected       bool                    `json:"connected"`
	State           string                  `json:"state"`
	ProfileLabel    string                  `json:"profile_label,omitempty"`
	Ready           bool                    `json:"ready"`
	AdapterVersion  string                  `json:"adapter_version,omitempty"`
	Adapter         string                  `json:"adapter,omitempty"`
	ActiveEndpoints int                     `json:"active_endpoints,omitempty"`
	BusyEndpoints   int                     `json:"busy_endpoints,omitempty"`
	Endpoints       []AdapterEndpointStatus `json:"endpoints,omitempty"`
	LastSeen        time.Time               `json:"last_seen,omitempty"`
}

type AdapterEndpointStatus struct {
	ID                  int                     `json:"id,omitempty"`
	Profile             string                  `json:"profile,omitempty"`
	Principal           string                  `json:"principal,omitempty"`
	EndpointCapability  string                  `json:"endpoint_capability,omitempty"`
	State               string                  `json:"state,omitempty"`
	SessionKey          string                  `json:"session_key,omitempty"`
	SessionKeySupported bool                    `json:"session_key_supported,omitempty"`
	CanCreateSession    bool                    `json:"can_create_session,omitempty"`
	DefaultNewSession   bool                    `json:"default_new_session,omitempty"`
	CurrentModel        string                  `json:"current_model,omitempty"`
	CurrentReasoning    string                  `json:"current_reasoning,omitempty"`
	Models              []string                `json:"models,omitempty"`
	ReasoningLevels     []string                `json:"reasoning_levels,omitempty"`
	ModelScan           string                  `json:"model_scan,omitempty"`
	ReasoningScan       string                  `json:"reasoning_scan,omitempty"`
	LastFailure         *AdapterEndpointFailure `json:"last_failure,omitempty"`
}

type AdapterEndpointFailure struct {
	Code        string    `json:"code"`
	Reason      string    `json:"reason,omitempty"`
	LeaseReason string    `json:"lease_reason,omitempty"`
	At          time.Time `json:"at"`
}

type adapterEndpointKey struct {
	principal string
	profile   string
	endpoint  int
}

type adapterEndpointCapability struct {
	current           [sha256.Size]byte
	previous          [sha256.Size]byte
	previousExpiresAt time.Time
	expiresAt         time.Time
}

type AdapterEndpointCapability struct {
	Profile            string    `json:"profile"`
	EndpointID         int       `json:"endpoint_id"`
	EndpointCapability string    `json:"endpoint_capability"`
	ExpiresAt          time.Time `json:"expires_at"`
}

type Activity struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	JobID   string    `json:"job_id,omitempty"`
}

type queuedJob struct {
	job             Job
	profile         interface{}
	deadline        time.Time
	leasedTil       time.Time
	leaseGeneration uint64
	actionUnknown   bool
	done            chan Output
	progress        *AdapterProgress
	leasePrincipal  string
	leaseProfile    string
	leaseEndpointID int
	leaseCapability [sha256.Size]byte
}

func (s *Store) UpdateAdapterProgress(id string, generation uint64, progress AdapterProgress) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || now.After(item.deadline) {
		if ok {
			delete(s.queued, id)
		}
		return false
	}
	if !validAdapterLease(item, generation, now) {
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

func (s *Store) AdapterProgress(id string) (AdapterProgress, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	if !ok || time.Now().After(item.deadline) {
		if ok {
			delete(s.queued, id)
		}
		return AdapterProgress{}, false, false
	}
	if item.progress == nil {
		return AdapterProgress{}, true, false
	}
	return *item.progress, true, true
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0700); err != nil {
		return nil, err
	}
	store := &Store{
		dir:          dir,
		queued:       map[string]*queuedJob{},
		completed:    map[string]struct{}{},
		adapters:     map[string]AdapterClientStatus{},
		endpointCaps: map[adapterEndpointKey]adapterEndpointCapability{},
		metrics: Metrics{
			ByRoute: map[string]uint64{}, ByTask: map[string]uint64{}, ByProvider: map[string]uint64{},
			ByModel: map[string]uint64{}, ByFlag: map[string]uint64{}, ProviderLatency: map[string]uint64{}, ProviderSamples: map[string]uint64{}, ProviderFailures: map[string]uint64{},
			ByAttemptedProvider: map[string]uint64{}, AttemptedProviderFailures: map[string]uint64{}, ByAttemptedModel: map[string]uint64{}, ByReasoning: map[string]uint64{}, ReasoningFailures: map[string]uint64{}, ModelFailures: map[string]uint64{}, BySelection: map[string]uint64{}, SelectionFailures: map[string]uint64{},
		},
	}
	store.retention = localHistoryRetention{maxAge: 30 * 24 * time.Hour, maxRecords: 1000, maxBytes: 4 << 30}
	raw, _ := readMetricsFile(filepath.Join(dir, "metrics.json"))
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &store.metrics)
	}
	store.metrics.normalizeDimensions()
	return store, nil
}

func (s *Store) ConfigureHistoryRetention(maxAge time.Duration, maxRecords int, maxBytes int64) error {
	if maxAge == 0 {
		maxAge = 30 * 24 * time.Hour
	}
	if maxRecords == 0 {
		maxRecords = 1000
	}
	if maxBytes == 0 {
		maxBytes = 4 << 30
	}
	if maxAge < 24*time.Hour || maxAge > 3650*24*time.Hour || maxRecords < 1 || maxRecords > 1_000_000 || maxBytes < 64<<20 || maxBytes > 1<<50 {
		return errors.New("local job history retention is outside its safe bounds")
	}
	s.historyMu.Lock()
	s.retention = localHistoryRetention{maxAge: maxAge, maxRecords: maxRecords, maxBytes: maxBytes}
	s.nextHistoryPrune = time.Time{}
	s.historyMu.Unlock()
	return s.pruneJobHistory(time.Now().UTC())
}

func (s *Store) SaveJob(job Job) error {
	raw, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(s.dir, "jobs", jobStorageStem(job.ID)+".job.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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
	// #nosec G703 -- storageID returns a validated safe ID or a fixed-length SHA-256-derived name.
	if err := os.WriteFile(filepath.Join(s.dir, "jobs", jobStorageStem(id)+".result.json"), append(raw, '\n'), 0600); err != nil {
		return err
	}
	return s.maybePruneJobHistory(time.Now().UTC())
}

type localHistoryRecord struct {
	jobPath    string
	resultPath string
	updated    time.Time
	bytes      int64
	complete   bool
}

func (s *Store) maybePruneJobHistory(now time.Time) error {
	s.historyMu.Lock()
	due := !now.Before(s.nextHistoryPrune)
	s.historyMu.Unlock()
	if !due {
		return nil
	}
	return s.pruneJobHistory(now)
}

func (s *Store) pruneJobHistory(now time.Time) error {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	directory := filepath.Join(s.dir, "jobs")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	records := map[string]*localHistoryRecord{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "record-") {
			continue
		}
		result := strings.HasSuffix(name, ".result.json")
		jobFile := strings.HasSuffix(name, ".job.json")
		if !result && !jobFile {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".result.json"), ".job.json")
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() < 0 {
			continue
		}
		record := records[base]
		if record == nil {
			record = &localHistoryRecord{}
			records[base] = record
		}
		path := filepath.Join(directory, name)
		if result {
			record.resultPath = path
			record.complete = true
		} else {
			record.jobPath = path
		}
		record.bytes += info.Size()
		if info.ModTime().After(record.updated) {
			record.updated = info.ModTime()
		}
	}
	completed := make([]*localHistoryRecord, 0, len(records))
	for _, record := range records {
		if record.complete {
			completed = append(completed, record)
		}
	}
	sort.Slice(completed, func(left, right int) bool { return completed[left].updated.After(completed[right].updated) })
	var retainedBytes int64
	for index, record := range completed {
		keep := index < s.retention.maxRecords && now.Sub(record.updated) <= s.retention.maxAge && record.bytes <= s.retention.maxBytes-retainedBytes
		if keep {
			retainedBytes += record.bytes
			continue
		}
		for _, path := range []string{record.jobPath, record.resultPath} {
			if path != "" {
				if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					return removeErr
				}
			}
		}
	}
	s.nextHistoryPrune = now.Add(localHistoryPruneInterval)
	return nil
}

func (s *Store) RecordCompleted(job Job, output Output) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCompletionLocked(job.ID)
	s.metrics.JobsTotal = saturatingMetricAdd(s.metrics.JobsTotal, 1)
	if output.Error != "" {
		s.metrics.JobsFailed = saturatingMetricAdd(s.metrics.JobsFailed, 1)
	}
	if output.LatencyMS > 0 {
		s.metrics.LatencyTotalMS = saturatingMetricAdd(s.metrics.LatencyTotalMS, uint64(output.LatencyMS))
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
	incrementMetric(s.metrics.ByRoute, route, 1)
	incrementMetric(s.metrics.ByTask, task, 1)
	incrementMetric(s.metrics.ByProvider, provider, 1)
	incrementMetric(s.metrics.ByModel, model, 1)
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
	incrementMetric(s.metrics.ByAttemptedProvider, attemptedProvider, 1)
	incrementMetric(s.metrics.ByAttemptedModel, attemptedModel, 1)
	incrementMetric(s.metrics.ByReasoning, reasoning, 1)
	incrementMetric(s.metrics.BySelection, selection, 1)
	if output.Error != "" {
		incrementMetric(s.metrics.AttemptedProviderFailures, attemptedProvider, 1)
		incrementMetric(s.metrics.ModelFailures, attemptedModel, 1)
		incrementMetric(s.metrics.ReasoningFailures, reasoning, 1)
		incrementMetric(s.metrics.SelectionFailures, selection, 1)
	}
	if output.LatencyMS > 0 {
		incrementMetric(s.metrics.ProviderLatency, provider, uint64(output.LatencyMS))
		incrementMetric(s.metrics.ProviderSamples, provider, 1)
	}
	if output.Error != "" {
		incrementMetric(s.metrics.ProviderFailures, provider, 1)
	}
	if output.Decision != nil {
		for _, flag := range output.Decision.Flags {
			if flag != "" {
				incrementMetric(s.metrics.ByFlag, flag, 1)
			}
		}
	}
	s.metrics.EmbeddingVectors = saturatingMetricAdd(s.metrics.EmbeddingVectors, uint64(len(output.Embeddings)))
	s.metrics.UpdatedAt = time.Now().UTC()
	s.persistMetricsLocked()
}

func readMetricsFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximumMetricsFileBytes {
		return nil, errors.New("metrics file is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximumMetricsFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maximumMetricsFileBytes {
		return nil, errors.New("metrics file exceeds its size limit")
	}
	return raw, nil
}

func (metrics *Metrics) normalizeDimensions() {
	metrics.ByRoute = normalizeMetricMap(metrics.ByRoute)
	metrics.ByTask = normalizeMetricMap(metrics.ByTask)
	metrics.ByProvider = normalizeMetricMap(metrics.ByProvider)
	metrics.ByModel = normalizeMetricMap(metrics.ByModel)
	metrics.ByFlag = normalizeMetricMap(metrics.ByFlag)
	metrics.ProviderLatency = normalizeMetricMap(metrics.ProviderLatency)
	metrics.ProviderSamples = normalizeMetricMap(metrics.ProviderSamples)
	metrics.ProviderFailures = normalizeMetricMap(metrics.ProviderFailures)
	metrics.ByAttemptedProvider = normalizeMetricMap(metrics.ByAttemptedProvider)
	metrics.AttemptedProviderFailures = normalizeMetricMap(metrics.AttemptedProviderFailures)
	metrics.ByAttemptedModel = normalizeMetricMap(metrics.ByAttemptedModel)
	metrics.ByReasoning = normalizeMetricMap(metrics.ByReasoning)
	metrics.ReasoningFailures = normalizeMetricMap(metrics.ReasoningFailures)
	metrics.ModelFailures = normalizeMetricMap(metrics.ModelFailures)
	metrics.BySelection = normalizeMetricMap(metrics.BySelection)
	metrics.SelectionFailures = normalizeMetricMap(metrics.SelectionFailures)
}

func normalizeMetricMap(input map[string]uint64) map[string]uint64 {
	result := make(map[string]uint64, min(len(input), maximumMetricDimensions))
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		incrementMetric(result, key, input[key])
	}
	return result
}

func incrementMetric(values map[string]uint64, key string, delta uint64) {
	key = truncateUTF8(strings.TrimSpace(key), maximumMetricKeyBytes)
	if key == "" {
		key = "unknown"
	}
	if _, exists := values[key]; !exists && len(values) >= maximumMetricDimensions-1 {
		key = "other"
	}
	values[key] = saturatingMetricAdd(values[key], delta)
}

func saturatingMetricAdd(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}

func storageID(id string) string {
	if jobIDPattern.MatchString(id) && !strings.Contains(id, "..") {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "job-" + hex.EncodeToString(sum[:16])
}

func jobStorageStem(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "record-" + hex.EncodeToString(sum[:])
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
	s.addActivityLocked("queued", "Adapter job queued", job.ID)
	return done
}

func (s *Store) NextAdapterJob(profile string, lease time.Duration) *adapterJob {
	return s.NextAdapterJobForEndpoint(profile, 0, lease)
}

func (s *Store) NextAdapterJobForEndpoint(profile string, endpointID int, lease time.Duration) *adapterJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextAdapterJobLocked("", profile, endpointID, lease, "")
}

func (s *Store) NextScopedAdapterJobForEndpoint(principal, profile string, endpointID int, endpointCapability string, lease time.Duration) (*adapterJob, error) {
	leaseCapability, err := randomAdapterCapability()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validEndpointCapabilityLocked(principal, profile, endpointID, endpointCapability, time.Now()) {
		return nil, errors.New("adapter endpoint capability is invalid or expired")
	}
	return s.nextAdapterJobLocked(principal, profile, endpointID, lease, leaseCapability), nil
}

func (s *Store) nextAdapterJobLocked(principal, profile string, endpointID int, lease time.Duration, leaseCapability string) *adapterJob {
	now := time.Now()
	for id, item := range s.queued {
		if now.After(item.deadline) {
			delete(s.queued, id)
			continue
		}
		if now.Before(item.leasedTil) {
			continue
		}
		if item.job.ContextBridgeAdapterEndpointID > 0 && item.job.ContextBridgeAdapterEndpointID != endpointID {
			continue
		}
		if item.job.ContextBridgeAdapterPrincipal != "" && item.job.ContextBridgeAdapterPrincipal != principal {
			continue
		}
		if profile != "" {
			if p, ok := item.profile.(map[string]interface{}); ok {
				if name, _ := p["name"].(string); name != "" && name != profile {
					continue
				}
			}
		}
		item.leaseGeneration++
		if item.leaseGeneration == 0 {
			item.leaseGeneration = 1
		}
		item.leasedTil = now.Add(lease)
		item.leasePrincipal = principal
		item.leaseProfile = profile
		item.leaseEndpointID = endpointID
		item.leaseCapability = sha256.Sum256([]byte(leaseCapability))
		return &adapterJob{Job: item.job, Profile: item.profile, Deadline: item.deadline,
			LeaseGeneration: item.leaseGeneration, LeaseCapability: leaseCapability, LeaseExpiresAt: item.leasedTil, ObservationOnly: item.actionUnknown}
	}
	return nil
}

func randomAdapterCapability() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Store) Complete(id string, generation uint64, output Output) bool {
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
	if !validAdapterLease(item, generation, time.Now()) {
		return false
	}
	delete(s.queued, id)
	completed := cloneOutput(output)
	s.recordCompletionLocked(id)
	message := "Adapter result received"
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

func (s *Store) recordCompletionLocked(id string) {
	if _, exists := s.completed[id]; exists {
		return
	}
	s.completed[id] = struct{}{}
	s.completedOrder = append(s.completedOrder, id)
	if s.completedTotal < int(^uint(0)>>1) {
		s.completedTotal++
	}
	if len(s.completedOrder) <= maximumRecentCompletions {
		return
	}
	oldest := s.completedOrder[0]
	s.completedOrder = s.completedOrder[1:]
	delete(s.completed, oldest)
}

func (s *Store) Cancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.queued, id)
}

func (s *Store) Renew(id string, generation uint64, lease time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || now.After(item.deadline) {
		if ok {
			delete(s.queued, id)
		}
		return false
	}
	if !validAdapterLease(item, generation, now) {
		return false
	}
	item.leasedTil = now.Add(lease)
	return true
}

// ReleaseAdapterLease returns an unprocessed lease to the adapter queue. It
// never clears actionUnknown: if an external action may already have happened,
// the next generation remains observation-only.
func (s *Store) ReleaseAdapterLease(id string, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || now.After(item.deadline) || !validAdapterLease(item, generation, now) {
		return false
	}
	item.leasedTil = time.Time{}
	return true
}

// AdapterLeaseActive reports whether generation still owns the current,
// unexpired adapter lease. Unlike Renew it never extends the lease. The
// adapter uses this after a process restart to reserve the endpoint before it
// starts polling for more work without keeping an
// abandoned job alive merely by checking it.
func (s *Store) AdapterLeaseStatus(id string, generation uint64) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || now.After(item.deadline) || !validAdapterLease(item, generation, now) {
		return time.Time{}, false
	}
	return item.leasedTil.UTC(), true
}

// MarkAdapterAction is the point of no automatic retry. It is called before
// the adapter crosses its external side-effect boundary. If this worker
// disappears afterward, the next lease is observation-only because the
// external system may already have accepted the action.
func (s *Store) MarkAdapterAction(id string, generation uint64, lease time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || now.After(item.deadline) {
		if ok {
			delete(s.queued, id)
		}
		return false
	}
	if !validAdapterLease(item, generation, now) {
		return false
	}
	item.actionUnknown = true
	item.leasedTil = now.Add(lease)
	return true
}

func validAdapterLease(item *queuedJob, generation uint64, now time.Time) bool {
	return generation != 0 && item.leaseGeneration == generation && now.Before(item.leasedTil)
}

func validScopedAdapterLease(item *queuedJob, generation uint64, principal, capability string, now time.Time) bool {
	if !validAdapterLease(item, generation, now) || principal == "" || capability == "" || item.leasePrincipal != principal {
		return false
	}
	digest := sha256.Sum256([]byte(capability))
	return subtle.ConstantTimeCompare(digest[:], item.leaseCapability[:]) == 1
}

func (s *Store) UpdateAdapterProgressScoped(id string, generation uint64, principal, capability string, progress AdapterProgress) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || now.After(item.deadline) || !validScopedAdapterLease(item, generation, principal, capability, now) {
		if ok && now.After(item.deadline) {
			delete(s.queued, id)
		}
		return false
	}
	if item.progress != nil && progress.Sequence <= item.progress.Sequence {
		return true
	}
	progress.UpdatedAt = now.UTC()
	copy := progress
	item.progress = &copy
	return true
}

func (s *Store) AdapterProgressScoped(id string, generation uint64, principal, capability string) (AdapterProgress, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || now.After(item.deadline) || !validScopedAdapterLease(item, generation, principal, capability, now) {
		if ok && now.After(item.deadline) {
			delete(s.queued, id)
		}
		return AdapterProgress{}, false, false
	}
	if item.progress == nil {
		return AdapterProgress{}, true, false
	}
	return *item.progress, true, true
}

func (s *Store) CompleteScoped(id string, generation uint64, principal, capability string, output Output) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || now.After(item.deadline) || !validScopedAdapterLease(item, generation, principal, capability, now) {
		if ok && now.After(item.deadline) {
			delete(s.queued, id)
		}
		return false
	}
	delete(s.queued, id)
	completed := cloneOutput(output)
	s.recordCompletionLocked(id)
	message := "Adapter result received"
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

func (s *Store) RenewScoped(id string, generation uint64, principal, capability string, lease time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || now.After(item.deadline) || !validScopedAdapterLease(item, generation, principal, capability, now) {
		if ok && now.After(item.deadline) {
			delete(s.queued, id)
		}
		return false
	}
	item.leasedTil = now.Add(lease)
	return true
}

func (s *Store) ReleaseAdapterLeaseScoped(id string, generation uint64, principal, capability string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || !validScopedAdapterLease(item, generation, principal, capability, now) {
		return false
	}
	item.leasedTil = time.Time{}
	item.leaseCapability = [sha256.Size]byte{}
	item.leasePrincipal = ""
	item.leaseProfile = ""
	item.leaseEndpointID = 0
	return true
}

func (s *Store) AdapterLeaseStatusScoped(id string, generation uint64, principal, capability string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	if !ok || !validScopedAdapterLease(item, generation, principal, capability, time.Now()) {
		return time.Time{}, false
	}
	return item.leasedTil.UTC(), true
}

func (s *Store) MarkAdapterActionScoped(id string, generation uint64, principal, capability string, lease time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	now := time.Now()
	if !ok || now.After(item.deadline) || !validScopedAdapterLease(item, generation, principal, capability, now) {
		if ok && now.After(item.deadline) {
			delete(s.queued, id)
		}
		return false
	}
	item.actionUnknown = true
	item.leasedTil = now.Add(lease)
	return true
}

func (s *Store) AdapterCompletionContextScoped(id string, generation uint64, principal, capability string) (OutputSpec, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	if !ok || !validScopedAdapterLease(item, generation, principal, capability, time.Now()) {
		return OutputSpec{}, "", false
	}
	return adapterCompletionContext(item)
}

func (s *Store) AdapterCompletionContext(id string, generation uint64) (OutputSpec, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	if !ok || !validAdapterLease(item, generation, time.Now()) {
		return OutputSpec{}, "", false
	}
	return adapterCompletionContext(item)
}

func adapterCompletionContext(item *queuedJob) (OutputSpec, string, bool) {
	model := "adapter-endpoint"
	if requested := strings.TrimSpace(item.job.Model); requested != "" {
		model = "adapter:" + requested
	}
	if profile, profileOK := item.profile.(map[string]interface{}); profileOK {
		if name, _ := profile["name"].(string); name != "" && item.job.Model == "" {
			model = "adapter:" + name
		}
	}
	return item.job.Output, model, true
}

func (s *Store) Stats() (queued, completed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queued), s.completedTotal
}

func (s *Store) RecordAdapterHeartbeat(status AdapterClientStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status.Connected = status.State != "paused"
	status.LastSeen = time.Now().UTC()
	wasConnected := s.adapter.Connected && time.Since(s.adapter.LastSeen) < adapterHeartbeatGracePeriod
	s.adapter = status
	if status.Connected && !wasConnected {
		s.addActivityLocked("adapter", "Adapter process connected", "")
	}
}

func (s *Store) RecordScopedAdapterHeartbeat(principal string, status AdapterClientStatus) ([]AdapterEndpointCapability, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adapters == nil {
		s.adapters = map[string]AdapterClientStatus{}
	}
	if s.endpointCaps == nil {
		s.endpointCaps = map[adapterEndpointKey]adapterEndpointCapability{}
	}
	status.Connected = status.State != "paused"
	status.LastSeen = now
	status.Adapter = principal
	for index := range status.Endpoints {
		status.Endpoints[index].Principal = principal
	}
	previousStatus := s.adapters[principal]
	wasConnected := previousStatus.Connected && now.Sub(previousStatus.LastSeen) < adapterHeartbeatGracePeriod
	capabilities := make([]AdapterEndpointCapability, 0, len(status.Endpoints))
	activeKeys := make(map[adapterEndpointKey]struct{}, len(status.Endpoints))
	type plannedEndpointCapability struct {
		key    adapterEndpointKey
		record adapterEndpointCapability
		raw    string
	}
	planned := make([]plannedEndpointCapability, 0, len(status.Endpoints))
	if status.Connected {
		for index := range status.Endpoints {
			endpoint := &status.Endpoints[index]
			key := adapterEndpointKey{principal: principal, profile: endpoint.Profile, endpoint: endpoint.ID}
			activeKeys[key] = struct{}{}
			provided := strings.TrimSpace(endpoint.EndpointCapability)
			record, exists := s.endpointCaps[key]
			if exists && now.Before(record.expiresAt) {
				if !endpointCapabilityMatches(record, provided, now) {
					return nil, errors.New("active adapter endpoint requires its current capability")
				}
				// A healthy endpoint keeps one stable capability. Heartbeats renew its
				// expiry without invalidating an in-flight long poll.
				record.expiresAt = now.Add(adapterHeartbeatGracePeriod)
				record.previous = [sha256.Size]byte{}
				record.previousExpiresAt = time.Time{}
				planned = append(planned, plannedEndpointCapability{key: key, record: record, raw: provided})
				endpoint.EndpointCapability = ""
				continue
			}
			raw, err := randomAdapterCapability()
			if err != nil {
				return nil, err
			}
			expiresAt := now.Add(adapterHeartbeatGracePeriod)
			planned = append(planned, plannedEndpointCapability{
				key: key, record: adapterEndpointCapability{current: sha256.Sum256([]byte(raw)), expiresAt: expiresAt}, raw: raw,
			})
			endpoint.EndpointCapability = ""
		}
	}
	for _, candidate := range planned {
		s.endpointCaps[candidate.key] = candidate.record
		capabilities = append(capabilities, AdapterEndpointCapability{
			Profile: candidate.key.profile, EndpointID: candidate.key.endpoint,
			EndpointCapability: candidate.raw, ExpiresAt: candidate.record.expiresAt,
		})
	}
	for key := range s.endpointCaps {
		if key.principal == principal {
			if _, active := activeKeys[key]; !active || !status.Connected {
				delete(s.endpointCaps, key)
			}
		}
	}
	s.adapters[principal] = status
	if status.Connected && !wasConnected {
		s.addActivityLocked("adapter", "Scoped adapter "+principal+" connected", "")
	}
	return capabilities, nil
}

func endpointCapabilityMatches(record adapterEndpointCapability, capability string, now time.Time) bool {
	if capability == "" {
		return false
	}
	digest := sha256.Sum256([]byte(capability))
	if subtle.ConstantTimeCompare(digest[:], record.current[:]) == 1 {
		return true
	}
	return now.Before(record.previousExpiresAt) && subtle.ConstantTimeCompare(digest[:], record.previous[:]) == 1
}

func (s *Store) validEndpointCapabilityLocked(principal, profile string, endpointID int, capability string, now time.Time) bool {
	if principal == "" || profile == "" || endpointID <= 0 || capability == "" {
		return false
	}
	record, ok := s.endpointCaps[adapterEndpointKey{principal: principal, profile: profile, endpoint: endpointID}]
	if !ok || !now.Before(record.expiresAt) {
		return false
	}
	return endpointCapabilityMatches(record, capability, now)
}

func (s *Store) AdapterStatus() AdapterClientStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.adapter
	status.Connected = status.Connected && time.Since(status.LastSeen) < adapterHeartbeatGracePeriod
	if !status.Connected {
		status = AdapterClientStatus{}
	}
	ids := make([]string, 0, len(s.adapters))
	for id := range s.adapters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		candidate := s.adapters[id]
		candidate.Connected = candidate.Connected && time.Since(candidate.LastSeen) < adapterHeartbeatGracePeriod
		if !candidate.Connected {
			continue
		}
		status.Connected = true
		status.Ready = status.Ready || candidate.Ready
		status.ActiveEndpoints += candidate.ActiveEndpoints
		status.BusyEndpoints += candidate.BusyEndpoints
		status.Endpoints = append(status.Endpoints, candidate.Endpoints...)
		if candidate.LastSeen.After(status.LastSeen) {
			status.LastSeen = candidate.LastSeen
		}
	}
	if status.Connected && status.State == "" {
		status.State = "waiting"
	}
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
