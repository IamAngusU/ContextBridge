package cluster

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
)

type RelayConfig struct {
	Version            string
	Listen             string
	PublicURL          string
	Database           string
	AdminToken         string
	AllowedOrigins     []string
	MaxJobBytes        int64
	MaxQueuedJobs      int
	PairingTTL         time.Duration
	AssignmentTTL      time.Duration
	DispatchEvery      time.Duration
	Pricing            Pricing
	AllowedTasks       []string
	MaxAttempts        int
	Pipelines          map[string]Pipeline
	MaxPipelineRuntime time.Duration
	JobTimeout         time.Duration
	RetentionMaxAge    time.Duration
	MaxTerminalJobs    int
	MaxEvents          int
	MaxTerminalRuns    int
	RetentionSweep     time.Duration
}

const (
	maxActiveReservationsPerOwner = 64
	maxQueuedJobsPerOwner         = 64
	maxActivePipelineRuns         = 64
	maxActivePipelineRunsPerOwner = 8
	maximumMaintenanceInterval    = 5 * time.Second
)

type Relay struct {
	cfg             RelayConfig
	store           *Store
	logger          *log.Logger
	mu              sync.RWMutex
	workers         map[string]*workerConnection
	rateMu          sync.Mutex
	rate            map[string]*rateWindow
	wake            chan struct{}
	maintenanceMu   sync.Mutex
	nextMaintenance time.Time
	retentionMu     sync.Mutex
	nextRetention   time.Time
	fairnessMu      sync.Mutex
	lastOwner       map[int]string
	queueScanOffset int
	lifecycleMu     sync.RWMutex
	lifecycleCtx    context.Context
	pipelineWG      sync.WaitGroup
	admissionMu     sync.RWMutex
	quiescing       bool
}

func (r *Relay) Idle() bool {
	overview, err := r.store.Overview()
	if err != nil {
		return false
	}
	return r.idleWithOverview(overview)
}

func (r *Relay) idleWithOverview(overview Overview) bool {
	if overview.JobsByState[JobQueued] != 0 || overview.JobsByState[JobAssigned] != 0 || overview.JobsByState[JobRunning] != 0 {
		return false
	}
	activePipelines, err := r.store.HasActivePipelineRuns()
	if err != nil || activePipelines {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, worker := range r.workers {
		if running, _ := worker.load(); running != 0 {
			return false
		}
	}
	return true
}

func (r *Relay) pipelineContext() context.Context {
	r.lifecycleMu.RLock()
	ctx := r.lifecycleCtx
	r.lifecycleMu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// QuiesceForStop closes relay admission at a linearization point shared with
// job and pipeline creation. If a concurrent request won first, the idle check
// observes it and this method reopens admission before returning false.
func (r *Relay) QuiesceForStop(force bool) bool {
	r.admissionMu.Lock()
	defer r.admissionMu.Unlock()
	if r.quiescing {
		return true
	}
	r.quiescing = true
	if !force && !r.Idle() {
		r.quiescing = false
		return false
	}
	return true
}

func (r *Relay) ResumeAfterRejectedStop() {
	r.admissionMu.Lock()
	r.quiescing = false
	r.admissionMu.Unlock()
}

func (r *Relay) beginAdmission() bool {
	r.admissionMu.RLock()
	if r.quiescing {
		r.admissionMu.RUnlock()
		return false
	}
	return true
}

func (r *Relay) endAdmission() {
	r.admissionMu.RUnlock()
}

type workerConnection struct {
	conn     *websocket.Conn
	writeMu  sync.Mutex
	stateMu  sync.Mutex
	inFlight map[string]workerReservation
	capacity int
}

// workerReservation deliberately outlives the persisted job's active state.
// A relay timeout proves only that the producer must stop waiting; it does not
// prove that a side-effecting worker execution stopped. Keep counting that slot
// until the matching result or connection teardown, while remembering that a
// terminalized job no longer requires another stale-jobs store scan.
type workerReservation struct {
	storeTerminal   bool
	dispatchStarted bool
	attempt         int
}

func newWorkerConnection(conn *websocket.Conn, capacity int) *workerConnection {
	capacity = boundedWorkerCapacity(capacity)
	return &workerConnection{conn: conn, capacity: capacity, inFlight: map[string]workerReservation{}}
}

func (w *workerConnection) reserve(jobID string) bool {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if _, exists := w.inFlight[jobID]; exists {
		return false
	}
	if len(w.inFlight) >= w.capacity {
		return false
	}
	w.inFlight[jobID] = workerReservation{}
	return true
}

func (w *workerConnection) markStoreTerminal(jobID string) bool {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	reservation, exists := w.inFlight[jobID]
	if exists {
		reservation.storeTerminal = true
		w.inFlight[jobID] = reservation
	}
	// A capacity reservation is created before the durable assignment. It is
	// not execution evidence until dispatchStarted is set. Returning false in
	// that pre-dispatch window prevents a phantom worker cancel; beginDispatch
	// will observe storeTerminal and suppress the job frame itself.
	return exists && reservation.dispatchStarted
}

func (w *workerConnection) beginDispatch(jobID string, attempt int) bool {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	reservation, exists := w.inFlight[jobID]
	if !exists || reservation.storeTerminal || attempt <= 0 {
		return false
	}
	reservation.dispatchStarted = true
	reservation.attempt = attempt
	w.inFlight[jobID] = reservation
	return true
}

func (w *workerConnection) matchesDispatch(jobID string, attempt int) bool {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	reservation, exists := w.inFlight[jobID]
	return exists && reservation.dispatchStarted && attempt > 0 && reservation.attempt == attempt
}

func (w *workerConnection) release(jobID string) {
	w.stateMu.Lock()
	delete(w.inFlight, jobID)
	w.stateMu.Unlock()
}

func (w *workerConnection) load() (running, capacity int) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	return len(w.inFlight), w.capacity
}

func (w *workerConnection) needsStaleRecovery() bool {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	for _, reservation := range w.inFlight {
		if !reservation.storeTerminal {
			return true
		}
	}
	return false
}

func (w *workerConnection) updateCapacity(capacity int) {
	capacity = boundedWorkerCapacity(capacity)
	w.stateMu.Lock()
	w.capacity = capacity
	w.stateMu.Unlock()
}

func boundedWorkerCapacity(capacity int) int {
	if capacity <= 0 {
		return 1
	}
	if capacity > MaximumWorkerConcurrency {
		return MaximumWorkerConcurrency
	}
	return capacity
}

func (w *workerConnection) write(ctx context.Context, message []byte) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return w.conn.Write(ctx, websocket.MessageText, message)
}

type rateWindow struct {
	started time.Time
	count   int
}

func NewRelay(cfg RelayConfig, logger *log.Logger) (*Relay, error) {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:32150"
	}
	if cfg.Database == "" {
		return nil, errors.New("relay database path is required")
	}
	if len(cfg.AdminToken) < 32 {
		return nil, errors.New("relay admin token must contain at least 32 characters")
	}
	if cfg.MaxJobBytes <= 0 {
		cfg.MaxJobBytes = MaximumJobPayloadBytes
	}
	if cfg.MaxJobBytes > MaximumJobPayloadBytes {
		return nil, fmt.Errorf("relay max job bytes must not exceed %d MiB", MaximumJobPayloadBytes>>20)
	}
	if cfg.MaxQueuedJobs <= 0 {
		cfg.MaxQueuedJobs = 10000
	}
	if cfg.PairingTTL <= 0 {
		cfg.PairingTTL = 10 * time.Minute
	}
	if cfg.AssignmentTTL <= 0 {
		cfg.AssignmentTTL = 2 * time.Minute
	}
	if cfg.DispatchEvery <= 0 {
		cfg.DispatchEvery = 250 * time.Millisecond
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = 15 * time.Minute
	}
	if err := applyRetentionDefaults(&cfg); err != nil {
		return nil, err
	}
	store, err := OpenStore(cfg.Database)
	if err != nil {
		return nil, err
	}
	if err := store.EnsureToken(cfg.AdminToken, "admin", "relay-admin", nil); err != nil {
		store.Close()
		return nil, err
	}
	if _, err := store.RecoverRelayRestart("relay restarted before worker completion"); err != nil {
		store.Close()
		return nil, err
	}
	if _, err := store.FailActivePipelineRuns("relay restarted before pipeline completion"); err != nil {
		store.Close()
		return nil, err
	}
	now := time.Now().UTC()
	if _, err := store.PruneRetention(now, relayRetentionPolicy(cfg)); err != nil {
		store.Close()
		return nil, fmt.Errorf("prune relay history: %w", err)
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Relay{cfg: cfg, store: store, logger: logger, workers: map[string]*workerConnection{}, rate: map[string]*rateWindow{}, wake: make(chan struct{}, 1), nextRetention: now.Add(cfg.RetentionSweep), lastOwner: map[int]string{}}, nil
}

func applyRetentionDefaults(cfg *RelayConfig) error {
	if cfg.RetentionMaxAge == 0 {
		cfg.RetentionMaxAge = time.Duration(DefaultRetentionDays) * 24 * time.Hour
	}
	if cfg.MaxTerminalJobs == 0 {
		cfg.MaxTerminalJobs = DefaultMaxTerminalJobs
	}
	if cfg.MaxEvents == 0 {
		cfg.MaxEvents = DefaultMaxEvents
	}
	if cfg.MaxTerminalRuns == 0 {
		cfg.MaxTerminalRuns = DefaultMaxTerminalPipelineRuns
	}
	if cfg.RetentionSweep == 0 {
		cfg.RetentionSweep = time.Duration(DefaultRetentionSweepSeconds) * time.Second
	}
	if err := relayRetentionPolicy(*cfg).Validate(); err != nil {
		return err
	}
	minimumSweep := time.Duration(MinimumRetentionSweepSeconds) * time.Second
	maximumSweep := time.Duration(MaximumRetentionSweepSeconds) * time.Second
	if cfg.RetentionSweep < minimumSweep || cfg.RetentionSweep > maximumSweep {
		return fmt.Errorf("retention sweep must be between %s and %s", minimumSweep, maximumSweep)
	}
	return nil
}

func relayRetentionPolicy(cfg RelayConfig) RetentionPolicy {
	return RetentionPolicy{
		MaxAge:                  cfg.RetentionMaxAge,
		MaxTerminalJobs:         cfg.MaxTerminalJobs,
		MaxEvents:               cfg.MaxEvents,
		MaxTerminalPipelineRuns: cfg.MaxTerminalRuns,
	}
}

func (r *Relay) Close() error { return r.store.Close() }

func (r *Relay) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", r.handleDashboard)
	mux.HandleFunc("GET /health", r.handleHealth)
	mux.HandleFunc("POST /v1/pair/request", r.rateLimit(12, time.Minute, r.handlePairRequest))
	mux.HandleFunc("POST /v1/pair/token", r.rateLimit(30, time.Minute, r.handlePairPoll))
	mux.HandleFunc("GET /v1/pairings", r.authorize("admin")(r.handlePairings))
	mux.HandleFunc("POST /v1/pairings/{code}/{decision}", r.authorize("admin")(r.handlePairDecision))
	mux.HandleFunc("GET /v1/cluster/overview", r.authorize("admin", "observer", "producer")(r.handleOverview))
	mux.HandleFunc("GET /v1/cluster/nodes", r.authorize("admin", "observer", "producer")(r.handleNodes))
	mux.HandleFunc("GET /v1/cluster/events", r.authorize("admin", "observer")(r.handleEvents))
	mux.HandleFunc("GET /v1/cluster/jobs", r.authorize("admin", "observer", "producer")(r.handleJobs))
	mux.HandleFunc("POST /v1/cluster/jobs", r.authorize("admin", "producer")(r.handleSubmit))
	mux.HandleFunc("GET /v1/cluster/jobs/{id}", r.authorize("admin", "observer", "producer")(r.handleJob))
	mux.HandleFunc("DELETE /v1/cluster/jobs/{id}", r.authorize("admin", "producer")(r.handleCancel))
	mux.HandleFunc("POST /v1/cluster/assign", r.authorize("admin", "producer")(r.handleReserve))
	mux.HandleFunc("GET /v1/cluster/workers/connect", r.authorize("node")(r.handleWorker))
	mux.HandleFunc("POST /v1/cluster/tokens", r.authorize("admin")(r.handleCreateToken))
	mux.HandleFunc("GET /v1/cluster/pipelines", r.authorize("admin", "observer", "producer")(r.handlePipelines))
	mux.HandleFunc("POST /v1/cluster/pipelines/{name}/run", r.authorize("admin", "producer")(r.handlePipelineRun))
	mux.HandleFunc("GET /v1/cluster/pipeline-runs/{id}", r.authorize("admin", "observer", "producer")(r.handlePipelineRunStatus))
	return secureHeaders(mux)
}

func (r *Relay) Run(ctx context.Context) error {
	r.lifecycleMu.Lock()
	r.lifecycleCtx = ctx
	r.lifecycleMu.Unlock()
	server := &http.Server{Addr: r.cfg.Listen, Handler: r.Handler(), BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	go r.dispatchLoop(ctx)
	shutdownDone := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		shutdownDone <- server.Shutdown(shutdown)
	}()
	r.logger.Printf("relay listening on http://%s", r.cfg.Listen)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		shutdownErr := <-shutdownDone
		r.pipelineWG.Wait()
		return shutdownErr
	}
	return err
}

func (r *Relay) handleHealth(w http.ResponseWriter, _ *http.Request) {
	overview, err := r.store.Overview()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	idle := r.idleWithOverview(overview)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "service": "contextbridge-relay", "version": r.cfg.Version, "protocol": ProtocolVersion, "overview": overview, "idle": idle})
}

func (r *Relay) handlePairRequest(w http.ResponseWriter, req *http.Request) {
	var input PairRequest
	if err := decodeJSON(req.Body, &input, 32<<10); err != nil || input.NodeName == "" {
		writeError(w, http.StatusBadRequest, errors.New("valid node_name and public_key are required"))
		return
	}
	uri := strings.TrimRight(r.cfg.PublicURL, "/") + "/dashboard/#pair"
	response, err := r.store.CreatePairing(input, uri, r.cfg.PairingTTL)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	_ = r.store.AddEvent(Event{Kind: "pairing.requested", Message: "Pairing requested by " + cleanLabel(input.NodeName, 100)})
	writeJSON(w, http.StatusCreated, response)
}

func (r *Relay) handlePairPoll(w http.ResponseWriter, req *http.Request) {
	var input struct {
		DeviceCode string `json:"device_code"`
	}
	if err := decodeJSON(req.Body, &input, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	state, pairing, token, err := r.store.PollPairing(input.DeviceCode)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("pairing request not found"))
		return
	}
	status := http.StatusAccepted
	if state == "approved" {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]interface{}{"state": state, "node_id": pairing.NodeID, "node_token": token})
}

func (r *Relay) handlePairings(w http.ResponseWriter, _ *http.Request) {
	pairings, err := r.store.ListPairings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, pairings)
}

func (r *Relay) handlePairDecision(w http.ResponseWriter, req *http.Request) {
	decision := req.PathValue("decision")
	if decision != "approve" && decision != "deny" {
		writeError(w, http.StatusBadRequest, errors.New("decision must be approve or deny"))
		return
	}
	pairing, err := r.store.DecidePairing(req.PathValue("code"), decision == "approve")
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	_ = r.store.AddEvent(Event{Kind: "pairing." + decision + "d", Message: "Pairing " + decision + "d for " + pairing.NodeName, NodeID: pairing.NodeID})
	writeJSON(w, http.StatusOK, pairing)
}

func (r *Relay) handleOverview(w http.ResponseWriter, _ *http.Request) {
	overview, err := r.store.Overview()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (r *Relay) handleNodes(w http.ResponseWriter, _ *http.Request) {
	nodes, err := r.store.ListNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

func (r *Relay) handleEvents(w http.ResponseWriter, req *http.Request) {
	events, err := r.store.ListEvents(queryLimit(req, 100, 500))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (r *Relay) handleJobs(w http.ResponseWriter, req *http.Request) {
	jobs, err := r.store.ListJobs(queryLimit(req, 100, 1000), cleanLabel(req.URL.Query().Get("status"), 20))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	record, _ := tokenRecord(req.Context())
	if record.Role == "producer" {
		filtered := jobs[:0]
		for _, job := range jobs {
			if job.OwnerSubject == record.Subject {
				filtered = append(filtered, job)
			}
		}
		jobs = filtered
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (r *Relay) handleJob(w http.ResponseWriter, req *http.Request) {
	job, err := r.store.GetJob(req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("job not found"))
		return
	}
	if !canReadJob(req.Context(), job) {
		writeError(w, http.StatusForbidden, errors.New("job belongs to another producer"))
		return
	}
	writeJSON(w, http.StatusOK, jobResponse(job, req.URL.Query().Get("compact") == "1"))
}

func (r *Relay) handleCancel(w http.ResponseWriter, req *http.Request) {
	existing, err := r.store.GetJob(req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("job not found"))
		return
	}
	if !canReadJob(req.Context(), existing) {
		writeError(w, http.StatusForbidden, errors.New("job belongs to another producer"))
		return
	}
	job, err := r.store.CancelJob(req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	// Cancellation makes the durable record terminal, but it does not prove a
	// side-effecting worker execution has stopped. Retain the occupied slot
	// until the matching result or disconnect while excluding this reservation
	// from future stale-record scans.
	if r.markWorkerReservationTerminal(job.AssignedNode, job.ID) {
		// AssignedNode can also be an E2EE queue binding that has never been
		// dispatched. Only a live reservation proves there is worker execution
		// to cancel; otherwise a phantom cancel could poison the worker's bounded
		// cancel-before-dispatch cache.
		r.cancelWorkerExecution(job.AssignedNode, job.ID)
	}
	writeJSON(w, http.StatusOK, job)
}

func (r *Relay) cancelWorkerExecution(nodeID, jobID string) {
	if nodeID == "" || jobID == "" {
		return
	}
	r.mu.RLock()
	worker := r.workers[nodeID]
	r.mu.RUnlock()
	if worker == nil || worker.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	message := WireMessage{Version: ProtocolVersion, Type: "cancel", JobID: jobID}
	if err := worker.write(ctx, mustJSON(message)); err != nil {
		r.logger.Printf("worker cancellation for %s could not be delivered: %v", jobID, err)
	}
}

func (r *Relay) handleSubmit(w http.ResponseWriter, req *http.Request) {
	var input SubmitRequest
	// MaxJobBytes is the cleartext payload budget. A sealed payload base64-
	// encodes that same payload and therefore needs a larger HTTP envelope.
	// Decode against the bounded wire budget, then enforce the cleartext budget
	// independently below so encryption never reduces the usable job size.
	if err := decodeJSON(req.Body, &input, sealedSubmitBodyLimit(r.cfg.MaxJobBytes)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateSubmitPayload(input, r.cfg.MaxJobBytes); err != nil {
		status := http.StatusBadRequest
		var limitErr *payloadLimitError
		if errors.As(err, &limitErr) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, err)
		return
	}
	record, _ := tokenRecord(req.Context())
	if err := scopeRequirements(&input.Requirements, record); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if err := r.validateRequirements(input.Requirements); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := validateTenantID(input.TenantID); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if input.MaxAttempts == 0 {
		input.MaxAttempts = r.cfg.MaxAttempts
	}
	input.OwnerSubject = record.Subject
	if !r.beginAdmission() {
		writeError(w, http.StatusServiceUnavailable, errors.New("relay is stopping"))
		return
	}
	var job Job
	var err error
	if input.AssignmentID != "" {
		job, err = r.store.ConsumeReservationAdmitted(input.AssignmentID, input.AssignmentSecret, input.Sealed, input.Source, input.TenantID, input.OwnerSubject, input.Priority, input.MaxAttempts, r.cfg.MaxQueuedJobs, r.ownerQueueLimit())
	} else {
		job, err = r.store.CreateJobAdmitted(input, r.cfg.MaxQueuedJobs, r.ownerQueueLimit())
	}
	r.endAdmission()
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, ErrQueueFull) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(err, ErrOwnerQueueCapacity) {
			status = http.StatusTooManyRequests
		} else if errors.Is(err, ErrReservationOwnerMismatch) {
			status = http.StatusForbidden
		}
		writeError(w, status, err)
		return
	}
	_ = r.store.AddEvent(Event{Kind: "job.queued", Message: "Job queued", JobID: job.ID})
	r.signalDispatch()
	writeJSON(w, http.StatusAccepted, jobResponse(job, req.URL.Query().Get("compact") == "1"))
}

type payloadLimitError struct {
	message string
}

func (e *payloadLimitError) Error() string { return e.message }

func sealedSubmitBodyLimit(cleartextLimit int64) int64 {
	if cleartextLimit <= 0 {
		cleartextLimit = MaximumJobPayloadBytes
	}
	// AES-GCM adds a 16-byte tag; RawURL base64 expands by at most 4/3.
	// Leave a small, fixed allowance for requirements and envelope metadata.
	ciphertextLimit := cleartextLimit + 16
	encodedCiphertextLimit := (ciphertextLimit*4 + 2) / 3
	return encodedCiphertextLimit + (64 << 10)
}

func validateSubmitPayload(input SubmitRequest, cleartextLimit int64) error {
	if len(input.Payload) > 0 && input.Sealed != nil {
		return errors.New("job must contain either payload or sealed_payload, not both")
	}
	if int64(len(input.Payload)) > cleartextLimit {
		return &payloadLimitError{message: fmt.Sprintf("job payload exceeds %d bytes", cleartextLimit)}
	}
	if input.Sealed == nil {
		return nil
	}
	if input.Sealed.Algorithm != sealedAlgorithm {
		return errors.New("sealed payload uses an unsupported algorithm")
	}
	ciphertext, err := decode(input.Sealed.Ciphertext)
	if err != nil {
		return errors.New("sealed payload ciphertext is invalid")
	}
	if len(ciphertext) < 16 || int64(len(ciphertext)) > cleartextLimit+16 {
		return &payloadLimitError{message: fmt.Sprintf("sealed job payload exceeds %d cleartext bytes", cleartextLimit)}
	}
	nonce, err := decode(input.Sealed.Nonce)
	if err != nil || len(nonce) != 12 {
		return errors.New("sealed payload nonce is invalid")
	}
	publicKey, err := decode(input.Sealed.EphemeralPublic)
	if err != nil || len(publicKey) != 32 {
		return errors.New("sealed payload public key is invalid")
	}
	return nil
}

func jobResponse(job Job, compact bool) Job {
	if compact {
		job.Payload = nil
		job.SealedPayload = nil
	}
	return job
}

func (r *Relay) handleReserve(w http.ResponseWriter, req *http.Request) {
	var input AssignmentRequest
	if err := decodeJSON(req.Body, &input, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, _ := tokenRecord(req.Context())
	if err := scopeRequirements(&input.Requirements, record); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if err := r.validateRequirements(input.Requirements); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := validateTenantID(input.TenantID); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	nodes, _ := r.store.ListNodes()
	routingRequirements := r.withSessionAffinity(input.Requirements, record.Subject)
	candidates := RankWithEstimate(nodes, routingRequirements, r.store.EstimateVRAM(input.Requirements))
	if len(candidates) == 0 {
		writeError(w, http.StatusServiceUnavailable, errors.New("no online node satisfies these requirements"))
		return
	}
	node := candidates[0].Node
	if node.PublicKey == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("selected node has no encryption key"))
		return
	}
	secret, _ := randomToken("as_")
	assignment := Assignment{
		ID: randomID("assignment"), JobID: randomID("job"), NodeID: node.ID, NodeName: node.Name,
		PublicKey: node.PublicKey, Attempt: 1, OwnerSubject: record.Subject, TenantID: input.TenantID,
		ExpiresAt: time.Now().UTC().Add(r.cfg.AssignmentTTL), Requirements: input.Requirements,
	}
	ownerLimit := maxActiveReservationsPerOwner
	if r.cfg.MaxQueuedJobs < ownerLimit {
		ownerLimit = r.cfg.MaxQueuedJobs
	}
	if !r.beginAdmission() {
		writeError(w, http.StatusServiceUnavailable, errors.New("relay is stopping"))
		return
	}
	err := r.store.CreateReservationAdmitted(assignment, secret, record.Subject, r.cfg.MaxQueuedJobs, ownerLimit)
	r.endAdmission()
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrOwnerReservationCapacity) {
			status = http.StatusTooManyRequests
		} else if errors.Is(err, ErrReservationCapacity) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, AssignmentResponse{Assignment: assignment, Secret: secret})
}

func (r *Relay) handleCreateToken(w http.ResponseWriter, req *http.Request) {
	var input struct {
		Role          string   `json:"role"`
		Subject       string   `json:"subject"`
		Groups        []string `json:"groups"`
		LifetimeHours int      `json:"lifetime_hours"`
	}
	if err := decodeJSON(req.Body, &input, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Validate before converting to time.Duration: multiplying an attacker-
	// controlled int first can wrap and accidentally create a different token
	// lifetime. Zero retains the existing non-expiring admin-token behavior.
	if input.LifetimeHours < 0 || input.LifetimeHours > 10*365*24 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("lifetime_hours must be 0 to 87600"))
		return
	}
	token, record, err := r.store.CreateToken(input.Role, input.Subject, input.Groups, time.Duration(input.LifetimeHours)*time.Hour)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"token": token, "record": record})
}

func (r *Relay) handleWorker(w http.ResponseWriter, req *http.Request) {
	record, ok := tokenRecord(req.Context())
	if !ok || record.Subject == "" {
		writeError(w, http.StatusUnauthorized, errors.New("node token required"))
		return
	}
	options := &websocket.AcceptOptions{OriginPatterns: r.cfg.AllowedOrigins}
	conn, err := websocket.Accept(w, req, options)
	if err != nil {
		return
	}
	conn.SetReadLimit(MaximumJobResultWireBytes)
	defer conn.Close(websocket.StatusNormalClosure, "worker disconnected")
	ctx := req.Context()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return
	}
	var hello WireMessage
	if json.Unmarshal(raw, &hello) != nil || hello.Version != ProtocolVersion || hello.Type != "hello" || hello.Node == nil || hello.Node.ID != record.Subject {
		conn.Close(websocket.StatusPolicyViolation, "invalid worker hello")
		return
	}
	node := *hello.Node
	node.Name = cleanLabel(node.Name, 100)
	if node.Name == "" {
		conn.Close(websocket.StatusPolicyViolation, "worker name is empty")
		return
	}
	scopeNodeCapabilities(&node.Capabilities, record)
	node.Capabilities.MaxConcurrent = boundedWorkerCapacity(node.Capabilities.MaxConcurrent)
	node.Connected = true
	node.State = "online"
	node.LastSeen = time.Now().UTC()
	if !node.Capabilities.ClockTime.IsZero() {
		node.ClockOffsetMS = node.Capabilities.ClockTime.Sub(node.LastSeen).Milliseconds()
	}
	if node.PublicKey == "" {
		if saved, loadErr := r.store.GetNode(node.ID); loadErr == nil {
			node.PublicKey = saved.PublicKey
		}
	}
	if err := r.store.UpsertNodePinned(node); err != nil {
		status := websocket.StatusInternalError
		message := "node could not be stored"
		if errors.Is(err, ErrNodePublicKeyMismatch) {
			status = websocket.StatusPolicyViolation
			message = "node public key differs from paired identity; re-pair to rotate it"
		}
		conn.Close(status, message)
		return
	}
	worker := newWorkerConnection(conn, node.Capabilities.MaxConcurrent)
	r.mu.Lock()
	if previous := r.workers[node.ID]; previous != nil {
		delete(r.workers, node.ID)
		previous.conn.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
		_, _ = r.store.RequeueNode(node.ID, "worker connection replaced")
	}
	r.workers[node.ID] = worker
	r.mu.Unlock()
	defer r.disconnectNode(node.ID, worker)
	_ = r.store.AddEvent(Event{Kind: "node.online", Message: node.Name + " connected", NodeID: node.ID})
	r.signalDispatch()
	for {
		_, raw, err = conn.Read(ctx)
		if err != nil {
			return
		}
		var message WireMessage
		if json.Unmarshal(raw, &message) != nil || message.Version != ProtocolVersion {
			continue
		}
		switch message.Type {
		case "heartbeat":
			if message.Capabilities != nil {
				node.Capabilities = *message.Capabilities
				scopeNodeCapabilities(&node.Capabilities, record)
				worker.updateCapacity(node.Capabilities.MaxConcurrent)
				running, capacity := worker.load()
				if node.Capabilities.Running < running {
					node.Capabilities.Running = running
				}
				node.Capabilities.MaxConcurrent = capacity
			}
			node.Connected = true
			node.State = "online"
			node.LastSeen = time.Now().UTC()
			if !node.Capabilities.ClockTime.IsZero() {
				node.ClockOffsetMS = node.Capabilities.ClockTime.Sub(node.LastSeen).Milliseconds()
			}
			_ = r.store.UpsertNode(node)
		case "started":
			_, _ = r.store.MarkRunning(message.JobID, node.ID, message.Attempt)
		case "progress":
			if message.Progress != nil {
				_, _ = r.store.UpdateJobProgress(message.JobID, node.ID, message.Attempt, *message.Progress)
			}
		case "result":
			usage := priceUsage(message.Usage, r.cfg.Pricing)
			job, completeErr := r.store.CompleteJob(message.JobID, node.ID, message.Attempt, message.Result, message.SealedResult, usage, message.Error)
			// Only the current assignment generation may release the worker slot.
			// A final job can still receive its matching late result after a producer
			// cancellation or relay timeout. That matching result proves execution
			// really ended, so the slot is safe to release even though the store
			// rejects the state transition. The timeout itself is not such proof.
			final := job.Status == JobCompleted || job.Status == JobFailed || job.Status == JobCancelled
			if completeErr == nil || (final && job.AssignedNode == node.ID && job.Attempt == message.Attempt) || worker.matchesDispatch(message.JobID, message.Attempt) {
				worker.release(message.JobID)
				r.signalDispatch()
			}
			if completeErr == nil {
				kind := "job.completed"
				if job.Status == JobFailed {
					kind = "job.failed"
				}
				_ = r.store.AddEvent(Event{Kind: kind, Message: "Worker reported " + job.Status, JobID: job.ID, NodeID: node.ID})
			}
		}
	}
}

func (r *Relay) dispatchLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.DispatchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
		r.dispatch()
	}
}

func (r *Relay) dispatch() {
	now := time.Now().UTC()
	if r.maintenanceDue(now) {
		r.runMaintenance(now)
	}
	r.pruneRetentionIfDue(now)
	owners, offset := r.dispatchScanSnapshot()
	jobs, total, nextOffset, err := r.store.QueuedJobsFairPage(200, offset, owners)
	if err != nil || len(jobs) == 0 {
		return
	}
	r.recordQueueScan(nextOffset, total)
	nodes, err := r.store.ListNodes()
	if err != nil {
		return
	}
	r.mu.RLock()
	for index := range nodes {
		worker := r.workers[nodes[index].ID]
		if worker == nil {
			continue
		}
		running, capacity := worker.load()
		nodes[index].Connected = true
		nodes[index].Capabilities.Running = running
		nodes[index].Capabilities.MaxConcurrent = capacity
	}
	r.mu.RUnlock()
	for _, queued := range jobs {
		routingRequirements := r.withSessionAffinity(queued.Requirements, queued.OwnerSubject)
		estimatedVRAM := r.store.EstimateVRAM(queued.Requirements)
		candidates := RankWithEstimate(nodes, routingRequirements, estimatedVRAM)
		for _, candidate := range candidates {
			if queued.AssignedNode != "" && queued.AssignedNode != candidate.Node.ID {
				continue
			}
			r.mu.RLock()
			worker := r.workers[candidate.Node.ID]
			r.mu.RUnlock()
			if worker == nil {
				continue
			}
			if !r.beginAdmission() {
				return
			}
			if !worker.reserve(queued.ID) {
				r.endAdmission()
				continue
			}
			job, assignErr := r.store.AssignJob(queued.ID, candidate.Node.ID)
			if assignErr != nil {
				worker.release(queued.ID)
				r.endAdmission()
				break
			}
			// Cancellation may win after the slot reservation but before the
			// durable assignment. In that case markStoreTerminal records the win
			// without emitting a phantom cancel, and this gate ensures the already
			// cancelled job is never written to the worker afterward.
			if !worker.beginDispatch(job.ID, job.Attempt) {
				worker.release(job.ID)
				r.endAdmission()
				break
			}
			message := WireMessage{Version: ProtocolVersion, Type: "job", Job: &job}
			writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			writeErr := worker.write(writeCtx, mustJSON(message))
			cancel()
			r.endAdmission()
			if writeErr != nil {
				worker.release(queued.ID)
				_, _ = r.store.RequeueNode(candidate.Node.ID, "worker connection failed")
				r.disconnectNode(candidate.Node.ID, worker)
			} else {
				r.recordDispatchedOwner(job.Priority, job.OwnerSubject)
				_ = r.store.AddEvent(Event{Kind: "job.assigned", Message: "Job assigned to " + candidate.Node.Name, JobID: job.ID, NodeID: candidate.Node.ID})
				candidate.Node.Capabilities.Running++
				for index := range nodes {
					if nodes[index].ID == candidate.Node.ID {
						nodes[index] = candidate.Node
					}
				}
			}
			break
		}
	}
}

func (r *Relay) runMaintenance(now time.Time) {
	hasReservations, hasQueuedJobs, err := r.store.maintenanceCandidates()
	if err != nil {
		// Preserve the fail-safe behavior on an unhealthy store. The operations
		// below will surface their more specific errors through the existing log.
		hasReservations = true
		hasQueuedJobs = true
		r.logger.Printf("relay maintenance preflight failed: %v", err)
	}
	if hasReservations {
		if _, err := r.store.GarbageCollectReservations(now); err != nil {
			r.logger.Printf("assignment reservation cleanup failed: %v", err)
		}
	}
	// Assigned/running jobs are represented by a reserved worker slot. Queued
	// encrypted reservations remain in the queue. If neither exists, a full
	// jobs-bucket scan cannot recover anything and would only decode retained
	// payloads/results (which may include large artifacts). Relay startup still
	// performs the unconditional crash-recovery scan. A stored timeout keeps its
	// capacity reservation but is no longer a stale-recovery candidate.
	if !hasQueuedJobs && !r.hasStaleRecoveryCandidates() {
		return
	}
	if recovered, err := r.store.RecoverStaleJobs(now, r.cfg.AssignmentTTL, r.cfg.JobTimeout); err == nil {
		for _, job := range recovered {
			if r.markWorkerReservationTerminal(job.AssignedNode, job.ID) {
				// A live reservation proves this was dispatched execution, not merely
				// an expired encrypted queue binding. Best-effort cancellation bounds
				// hung provider work; the slot remains occupied until the matching
				// result or connection teardown proves execution has actually ended.
				r.cancelWorkerExecution(job.AssignedNode, job.ID)
			}
			_ = r.store.AddEvent(Event{Kind: "job." + job.Status, Message: job.Error, JobID: job.ID, NodeID: job.AssignedNode})
		}
	} else {
		r.logger.Printf("stale job recovery failed: %v", err)
	}
}

func (r *Relay) hasStaleRecoveryCandidates() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, worker := range r.workers {
		if worker.needsStaleRecovery() {
			return true
		}
	}
	return false
}

func (r *Relay) markWorkerReservationTerminal(nodeID, jobID string) bool {
	if nodeID == "" || jobID == "" {
		return false
	}
	r.mu.RLock()
	worker := r.workers[nodeID]
	r.mu.RUnlock()
	if worker != nil {
		return worker.markStoreTerminal(jobID)
	}
	return false
}

func (r *Relay) maintenanceDue(now time.Time) bool {
	r.maintenanceMu.Lock()
	defer r.maintenanceMu.Unlock()
	if !r.nextMaintenance.IsZero() && now.Before(r.nextMaintenance) {
		return false
	}
	r.nextMaintenance = now.Add(relayMaintenanceInterval(r.cfg))
	return true
}

func relayMaintenanceInterval(cfg RelayConfig) time.Duration {
	interval := maximumMaintenanceInterval
	for _, deadline := range []time.Duration{cfg.AssignmentTTL, cfg.JobTimeout} {
		if candidate := deadline / 4; candidate > 0 && candidate < interval {
			interval = candidate
		}
	}
	if interval < cfg.DispatchEvery {
		interval = cfg.DispatchEvery
	}
	if interval <= 0 {
		return 250 * time.Millisecond
	}
	return interval
}

func (r *Relay) fairnessSnapshot() map[int]string {
	result, _ := r.dispatchScanSnapshot()
	return result
}

func (r *Relay) dispatchScanSnapshot() (map[int]string, int) {
	r.fairnessMu.Lock()
	defer r.fairnessMu.Unlock()
	result := make(map[int]string, len(r.lastOwner))
	for priority, owner := range r.lastOwner {
		result[priority] = owner
	}
	return result, r.queueScanOffset
}

func (r *Relay) recordQueueScan(next, total int) {
	r.fairnessMu.Lock()
	if total <= 0 {
		r.queueScanOffset = 0
	} else {
		r.queueScanOffset = next % total
	}
	r.fairnessMu.Unlock()
}

func (r *Relay) recordDispatchedOwner(priority int, owner string) {
	if owner == "" {
		return
	}
	r.fairnessMu.Lock()
	r.lastOwner[priority] = owner
	r.fairnessMu.Unlock()
}

func (r *Relay) pruneRetentionIfDue(now time.Time) {
	r.retentionMu.Lock()
	defer r.retentionMu.Unlock()
	if now.Before(r.nextRetention) {
		return
	}
	r.nextRetention = now.Add(r.cfg.RetentionSweep)
	pruned, err := r.store.PruneRetention(now, relayRetentionPolicy(r.cfg))
	if err != nil {
		r.logger.Printf("relay history retention failed: %v", err)
		return
	}
	if pruned.Jobs > 0 || pruned.Events > 0 || pruned.PipelineRuns > 0 {
		r.logger.Printf("relay history retention removed %d terminal jobs, %d events, and %d terminal pipeline runs", pruned.Jobs, pruned.Events, pruned.PipelineRuns)
	}
}

func (r *Relay) ownerQueueLimit() int {
	if r.cfg.MaxQueuedJobs <= 0 || r.cfg.MaxQueuedJobs > maxQueuedJobsPerOwner {
		return maxQueuedJobsPerOwner
	}
	return r.cfg.MaxQueuedJobs
}

func scopeNodeCapabilities(capabilities *Capabilities, record TokenRecord) {
	if len(record.Groups) > 0 {
		capabilities.Groups = intersectFold(capabilities.Groups, record.Groups)
	}
}

func (r *Relay) disconnectNode(id string, worker *workerConnection) {
	removed := false
	r.mu.Lock()
	if r.workers[id] == worker {
		delete(r.workers, id)
		removed = true
	}
	r.mu.Unlock()
	if !removed {
		return
	}
	_ = r.store.SetNodeConnected(id, false)
	affected, _ := r.store.RequeueNode(id, "worker disconnected")
	_ = r.store.AddEvent(Event{Kind: "node.offline", Message: "Worker disconnected", NodeID: id, Data: map[string]interface{}{"affected_jobs": len(affected)}})
	r.signalDispatch()
}

func (r *Relay) validateRequirements(requirements Requirements) error {
	if requirements.Task == "" {
		return errors.New("requirements.task is required")
	}
	if len(r.cfg.AllowedTasks) > 0 && !containsFold(r.cfg.AllowedTasks, requirements.Task) {
		return fmt.Errorf("task %s is not allowed by relay policy", requirements.Task)
	}
	if len(requirements.RequiredTags) > 32 || len(requirements.PreferredNodes) > 32 {
		return errors.New("too many routing selectors")
	}
	for name, value := range map[string]string{
		"task":            requirements.Task,
		"model":           requirements.Model,
		"group":           requirements.Group,
		"browser_profile": requirements.BrowserProfile,
	} {
		if name != "task" && value == "" {
			continue
		}
		if !validRoutingLabel(value, 160) {
			return fmt.Errorf("requirements.%s must be at most 160 bytes without surrounding whitespace or control characters", name)
		}
	}
	for _, tag := range requirements.RequiredTags {
		if !validRoutingLabel(tag, 80) {
			return errors.New("requirements.required_tags entries must be 1 to 80 bytes without surrounding whitespace or control characters")
		}
	}
	for _, node := range requirements.PreferredNodes {
		if !validRoutingLabel(node, 160) {
			return errors.New("requirements.preferred_nodes entries must be 1 to 160 bytes without surrounding whitespace or control characters")
		}
	}
	if requirements.SessionID != "" && !validRoutingLabel(requirements.SessionID, 128) {
		return errors.New("requirements.session_id must be at most 128 bytes without surrounding whitespace or control characters")
	}
	if requirements.Provider != "" && (!validRoutingLabel(requirements.Provider, 80) || strings.Contains(requirements.Provider, "..")) {
		return errors.New("requirements.provider is invalid")
	}
	if requirements.BrowserProfile != "" {
		if !strings.EqualFold(requirements.Provider, "browser") {
			return errors.New("requirements.browser_profile requires provider browser")
		}
		if !validRoutingLabel(requirements.BrowserProfile, 80) {
			return errors.New("requirements.browser_profile must be at most 80 bytes without surrounding whitespace or control characters")
		}
	}
	return nil
}

func validRoutingLabel(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, func(r rune) bool {
			return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
		}) < 0
}

func validateTenantID(tenantID string) error {
	if tenantID != "" && !validRoutingLabel(tenantID, 200) {
		return errors.New("tenant_id must be at most 200 bytes without surrounding whitespace or control characters")
	}
	return nil
}

func (r *Relay) withSessionAffinity(requirements Requirements, owner string) Requirements {
	if nodeID, ok := r.store.RecentSessionNode(owner, requirements.SessionID); ok && !contains(requirements.PreferredNodes, nodeID) {
		requirements.PreferredNodes = append([]string{nodeID}, requirements.PreferredNodes...)
	}
	return requirements
}

func (r *Relay) authorize(roles ...string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			token := strings.TrimSpace(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
			record, ok := r.store.Authenticate(token)
			if !ok || !contains(roles, record.Role) {
				writeError(w, http.StatusUnauthorized, errors.New("valid bearer token required"))
				return
			}
			next(w, req.WithContext(withTokenRecord(req.Context(), record)))
		}
	}
}

func (r *Relay) rateLimit(max int, window time.Duration, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		host := rateLimitClientKey(req)
		now := time.Now()
		r.rateMu.Lock()
		if len(r.rate) > 10000 {
			for key, value := range r.rate {
				if now.Sub(value.started) >= window {
					delete(r.rate, key)
				}
			}
		}
		entry := r.rate[host]
		if entry == nil || now.Sub(entry.started) >= window {
			entry = &rateWindow{started: now}
			r.rate[host] = entry
		}
		entry.count++
		allowed := entry.count <= max
		r.rateMu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", fmt.Sprint(int(window.Seconds())))
			writeError(w, http.StatusTooManyRequests, errors.New("rate limit exceeded"))
			return
		}
		next(w, req)
	}
}

func rateLimitClientKey(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil || host == "" {
		host = req.RemoteAddr
	}
	peer := net.ParseIP(strings.Trim(host, "[]"))
	// The supported public topology binds the relay to loopback and places a
	// TLS reverse proxy in front. Trust exactly one proxy-normalized IP header
	// only from that loopback peer; never trust an arbitrary X-Forwarded-For
	// chain from a directly connected client.
	if peer != nil && peer.IsLoopback() {
		forwarded := strings.TrimSpace(req.Header.Get("X-Real-IP"))
		if candidate := net.ParseIP(strings.Trim(forwarded, "[]")); candidate != nil {
			return candidate.String()
		}
	}
	if peer != nil {
		return peer.String()
	}
	return host
}

func (r *Relay) signalDispatch() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func scopeRequirements(requirements *Requirements, record TokenRecord) error {
	if record.Role != "producer" || len(record.Groups) == 0 {
		return nil
	}
	if requirements.Group == "" {
		if len(record.Groups) == 1 {
			requirements.Group = record.Groups[0]
			return nil
		}
		return errors.New("a group is required for this producer token")
	}
	if !containsFold(record.Groups, requirements.Group) {
		return errors.New("producer token is not allowed to use this group")
	}
	return nil
}

func canReadJob(ctx context.Context, job Job) bool {
	record, ok := tokenRecord(ctx)
	return !ok || record.Role != "producer" || job.OwnerSubject == record.Subject
}

func intersectFold(values, allowed []string) []string {
	result := []string{}
	for _, value := range values {
		if containsFold(allowed, value) {
			result = append(result, value)
		}
	}
	return result
}

type tokenContextKey struct{}

func withTokenRecord(ctx context.Context, record TokenRecord) context.Context {
	return context.WithValue(ctx, tokenContextKey{}, record)
}

func tokenRecord(ctx context.Context) (TokenRecord, bool) {
	record, ok := ctx.Value(tokenContextKey{}).(TokenRecord)
	return record, ok
}

func priceUsage(usage Usage, pricing Pricing) Usage {
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	usage.EstimatedCostUSD = float64(usage.ComputeMS)/3600000*pricing.ComputePerHourUSD + float64(usage.InputTokens)/1000000*pricing.InputPerMillionUSD + float64(usage.OutputTokens)/1000000*pricing.OutputPerMillionUSD
	usage.EquivalentCostUSD = float64(usage.InputTokens)/1000000*pricing.EquivalentInputUSD + float64(usage.OutputTokens)/1000000*pricing.EquivalentOutputUSD
	usage.SavedCostUSD = usage.EquivalentCostUSD - usage.EstimatedCostUSD
	if usage.SavedCostUSD < 0 {
		usage.SavedCostUSD = 0
	}
	return usage
}

func decodeJSON(body io.Reader, target interface{}, limit int64) error {
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	if int64(len(raw)) > limit {
		return fmt.Errorf("JSON body exceeds %d bytes", limit)
	}
	if !utf8.Valid(raw) {
		return errors.New("JSON body must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func mustJSON(value interface{}) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func queryLimit(req *http.Request, fallback, maximum int) int {
	value := fallback
	_, _ = fmt.Sscanf(req.URL.Query().Get("limit"), "%d", &value)
	if value <= 0 || value > maximum {
		return fallback
	}
	return value
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, req)
	})
}

func constantEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
