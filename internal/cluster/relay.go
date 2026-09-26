package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
)

type RelayConfig struct {
	Version              string
	Listen               string
	PublicURL            string
	LANListen            string
	LANPublicURL         string
	LANTLSCertificate    string
	LANTLSPrivateKey     string
	Database             string
	AdminToken           string
	AllowedOrigins       []string
	MaxJobBytes          int64
	MaxQueuedJobs        int
	PairingTTL           time.Duration
	AssignmentTTL        time.Duration
	DispatchEvery        time.Duration
	Pricing              Pricing
	AllowedTasks         []string
	ExecutionPolicy      ExecutionPolicyConfig
	MaxAttempts          int
	Pipelines            map[string]Pipeline
	MaxPipelineRuntime   time.Duration
	JobTimeout           time.Duration
	RetentionMaxAge      time.Duration
	MaxTerminalJobs      int
	MaxEvents            int
	MaxTerminalRuns      int
	MaxSessionPlacements int
	RetentionSweep       time.Duration
	Placement            PlacementPolicy
}

const (
	maxActiveReservationsPerOwner = 64
	maxQueuedJobsPerOwner         = 64
	maxActivePipelineRuns         = 64
	maxActivePipelineRunsPerOwner = 8
	maximumMaintenanceInterval    = 5 * time.Second
	maximumRateLimitBuckets       = 10_000
	maximumWorkerHelloWireBytes   = 2 << 20
	maximumWorkerHeartbeatBytes   = 4 << 20
	maximumWorkerProgressBytes    = 1 << 20
	maximumWorkerControlBytes     = 64 << 10
	maximumJobHistoryPage         = 200
	jobHistoryWriteTimeout        = 10 * time.Second
	maximumExecutionEventStreams  = 64
	maximumEventStreamsPerSubject = 8
	maximumWorkerReconnects       = 12
	workerReconnectWindow         = time.Minute
	maximumHeartbeatBurst         = 1000
	maximumHeartbeatWindowBytes   = 32 << 20
	workerHeartbeatWindow         = 10 * time.Second
)

type Relay struct {
	cfg              RelayConfig
	store            *Store
	authority        RelayAuthority
	logger           *log.Logger
	mu               sync.RWMutex
	workers          map[string]*workerConnection
	rateMu           sync.Mutex
	rate             map[string]*rateWindow
	rateLastSweep    time.Time
	workerRateMu     sync.Mutex
	workerRate       map[string]*rateWindow
	workerRateSweep  time.Time
	wake             chan struct{}
	maintenanceMu    sync.Mutex
	nextMaintenance  time.Time
	retentionMu      sync.Mutex
	nextRetention    time.Time
	fairnessMu       sync.Mutex
	lastOwner        map[int]string
	queueScanAfter   []byte
	lifecycleMu      sync.RWMutex
	lifecycleCtx     context.Context
	pipelineWG       sync.WaitGroup
	admissionMu      sync.RWMutex
	quiescing        bool
	eventStreamSlots chan struct{}
	eventStreamMu    sync.Mutex
	eventStreams     map[string]int
}

type heartbeatRateWindow struct {
	rateWindow
	bytes int64
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
	fence           *AssignmentFence
}

type workerReservationTerminalState uint8

const (
	workerReservationMissing workerReservationTerminalState = iota
	workerReservationPreDispatch
	workerReservationDispatched
)

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
	return w.markStoreTerminalState(jobID) == workerReservationDispatched
}

func (w *workerConnection) markStoreTerminalState(jobID string) workerReservationTerminalState {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	reservation, exists := w.inFlight[jobID]
	if !exists {
		return workerReservationMissing
	}
	reservation.storeTerminal = true
	w.inFlight[jobID] = reservation
	// A capacity reservation is created before the durable assignment. It is
	// not execution evidence until dispatchStarted is set. Distinguishing that
	// state from a missing reservation lets cancellation release a route probe
	// only when the relay can prove no worker execution began.
	if reservation.dispatchStarted {
		return workerReservationDispatched
	}
	return workerReservationPreDispatch
}

func (w *workerConnection) beginDispatch(jobID string, attempt int, fences ...*AssignmentFence) bool {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	reservation, exists := w.inFlight[jobID]
	if !exists || reservation.storeTerminal || attempt <= 0 {
		return false
	}
	reservation.dispatchStarted = true
	reservation.attempt = attempt
	if len(fences) > 0 && fences[0] != nil {
		copy := *fences[0]
		reservation.fence = &copy
	}
	w.inFlight[jobID] = reservation
	return true
}

func (w *workerConnection) matchesDispatch(jobID string, attempt int, fences ...*AssignmentFence) bool {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	reservation, exists := w.inFlight[jobID]
	if !exists || !reservation.dispatchStarted || attempt <= 0 || reservation.attempt != attempt {
		return false
	}
	var reported *AssignmentFence
	if len(fences) > 0 {
		reported = fences[0]
	}
	return assignmentFencePointersEqual(reservation.fence, reported)
}

func assignmentFencePointersEqual(left, right *AssignmentFence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
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
	if strings.TrimSpace(cfg.PublicURL) == "" {
		cfg.PublicURL = "http://" + cfg.Listen
	}
	if err := ValidateRelayURL(cfg.PublicURL); err != nil {
		return nil, fmt.Errorf("relay public URL: %w", err)
	}
	if cfg.LANListen != "" {
		if cfg.LANPublicURL == "" || cfg.LANTLSCertificate == "" || cfg.LANTLSPrivateKey == "" {
			return nil, errors.New("LAN relay listener requires public URL, certificate, and private key")
		}
		if err := ValidateRelayURL(cfg.LANPublicURL); err != nil {
			return nil, fmt.Errorf("LAN relay public URL: %w", err)
		}
		if !strings.HasPrefix(strings.ToLower(cfg.LANPublicURL), "https://") {
			return nil, errors.New("LAN relay public URL must use HTTPS")
		}
		if err := ValidateLANListenerEndpoint(cfg.LANListen, cfg.LANPublicURL); err != nil {
			return nil, err
		}
		parsedLANURL, err := url.Parse(cfg.LANPublicURL)
		if err != nil {
			return nil, fmt.Errorf("LAN relay public URL: %w", err)
		}
		if _, err := LoadLANTLSIdentity(cfg.LANTLSCertificate, cfg.LANTLSPrivateKey, parsedLANURL.Hostname(), time.Now().UTC()); err != nil {
			return nil, fmt.Errorf("LAN relay TLS identity: %w", err)
		}
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
	if err := cfg.ExecutionPolicy.Validate(); err != nil {
		return nil, err
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
	authority, err := store.AcquireRelayAuthority()
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("acquire relay authority: %w", err)
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
	return &Relay{cfg: cfg, store: store, authority: authority, logger: logger, workers: map[string]*workerConnection{}, rate: map[string]*rateWindow{}, workerRate: map[string]*rateWindow{}, wake: make(chan struct{}, 1), nextRetention: now.Add(cfg.RetentionSweep), lastOwner: map[int]string{}, eventStreamSlots: make(chan struct{}, maximumExecutionEventStreams), eventStreams: map[string]int{}}, nil
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
	if cfg.MaxSessionPlacements == 0 {
		cfg.MaxSessionPlacements = DefaultMaxSessionPlacements
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
		MaxSessionPlacements:    cfg.MaxSessionPlacements,
	}
}

func (r *Relay) Close() error { return r.store.Close() }

func (r *Relay) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", r.handleDashboard)
	mux.HandleFunc("GET /health", r.handleHealth)
	mux.HandleFunc("GET /livez", r.handleLiveness)
	mux.HandleFunc("GET /readyz", r.handleReadiness)
	mux.HandleFunc("GET /leaderz", r.handleLeadership)
	mux.HandleFunc("GET /metrics", r.authorize("admin", "observer")(r.handleMetrics))
	mux.HandleFunc("GET /v1/cluster/lifecycle", r.authorize("admin")(r.handleLifecycle))
	mux.HandleFunc("POST /v1/pair/request", r.rateLimit(12, time.Minute, r.handlePairRequest))
	mux.HandleFunc("POST /v1/pair/token", r.rateLimit(30, time.Minute, r.handlePairPoll))
	mux.HandleFunc("GET /v1/pairings", r.authorize("admin")(r.handlePairings))
	mux.HandleFunc("POST /v1/pairings/{code}/{decision}", r.authorize("admin")(r.handlePairDecision))
	mux.HandleFunc("GET /v1/cluster/overview", r.authorize("admin", "observer", "producer")(r.handleOverview))
	mux.HandleFunc("GET /v1/cluster/protocol", r.authorize("admin", "observer", "producer", "node")(r.handleProtocolManifest))
	mux.HandleFunc("GET /v1/cluster/nodes", r.authorize("admin", "observer", "producer")(r.handleNodes))
	mux.HandleFunc("POST /v1/cluster/nodes/{id}/{action}", r.authorize("admin")(r.handleNodeAdmission))
	mux.HandleFunc("GET /v1/cluster/events", r.authorize("admin", "observer")(r.handleEvents))
	mux.HandleFunc("GET /v1/cluster/jobs", r.authorize("admin", "observer", "producer")(r.handleJobs))
	mux.HandleFunc("POST /v1/cluster/jobs", r.authorize("admin", "producer")(r.handleSubmit))
	mux.HandleFunc("POST /v1/cluster/contracts/validate", r.authorize("admin", "producer")(r.handleContractValidate))
	mux.HandleFunc("GET /v1/cluster/jobs/{id}", r.authorize("admin", "observer", "producer")(r.handleJob))
	mux.HandleFunc("GET /v1/cluster/jobs/{id}/events", r.authorize("admin", "observer", "producer")(r.handleJobEvents))
	mux.HandleFunc("GET /v1/cluster/jobs/{id}/events/stream", r.authorize("admin", "observer", "producer")(r.handleJobEventStream))
	mux.HandleFunc("GET /v1/cluster/jobs/{id}/route", r.authorize("admin", "observer", "producer")(r.handleJobRoute))
	mux.HandleFunc("DELETE /v1/cluster/jobs/{id}", r.authorize("admin", "producer")(r.handleCancel))
	mux.HandleFunc("POST /v1/cluster/assign", r.authorize("admin", "producer")(r.handleReserve))
	mux.HandleFunc("POST /v1/cluster/routes/explain", r.authorize("admin", "producer")(r.handleRouteExplain))
	mux.HandleFunc("GET /v1/cluster/workers/connect", r.authorize("node")(r.handleWorker))
	mux.HandleFunc("POST /v1/cluster/tokens", r.authorize("admin")(r.handleCreateToken))
	mux.HandleFunc("GET /v1/cluster/pipelines", r.authorize("admin", "observer", "producer")(r.handlePipelines))
	mux.HandleFunc("POST /v1/cluster/pipelines/{name}/run", r.authorize("admin", "producer")(r.handlePipelineRun))
	mux.HandleFunc("GET /v1/cluster/pipeline-runs/{id}", r.authorize("admin", "observer", "producer")(r.handlePipelineRunStatus))
	mux.HandleFunc("GET /v1/cluster/pipeline-runs/{id}/events", r.authorize("admin", "observer", "producer")(r.handlePipelineRunEvents))
	mux.HandleFunc("GET /v1/cluster/pipeline-runs/{id}/events/stream", r.authorize("admin", "observer", "producer")(r.handlePipelineRunEventStream))
	return secureHeaders(mux)
}

func (r *Relay) handleProtocolManifest(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, CurrentProtocolManifest(r.cfg.MaxJobBytes))
}

func (r *Relay) Run(ctx context.Context) error {
	r.lifecycleMu.Lock()
	r.lifecycleCtx = ctx
	r.lifecycleMu.Unlock()
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	servers := []*http.Server{r.httpServer(runCtx, r.cfg.Listen)}
	tlsServer := -1
	if r.cfg.LANListen != "" {
		tlsServer = len(servers)
		servers = append(servers, r.httpServer(runCtx, r.cfg.LANListen))
	}
	go r.dispatchLoop(runCtx)
	errorsCh := make(chan error, len(servers))
	r.logger.Printf("relay listening on http://%s", r.cfg.Listen)
	for index, server := range servers {
		index, server := index, server
		go func() {
			if index == tlsServer {
				r.logger.Printf("relay LAN listener on %s (%s)", r.cfg.LANPublicURL, r.cfg.LANListen)
				errorsCh <- server.ListenAndServeTLS(r.cfg.LANTLSCertificate, r.cfg.LANTLSPrivateKey)
				return
			}
			errorsCh <- server.ListenAndServe()
		}()
	}
	select {
	case <-ctx.Done():
	case err := <-errorsCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			cancelRun()
			shutdownRelayServers(servers)
			r.pipelineWG.Wait()
			return err
		}
	}
	cancelRun()
	shutdownErr := shutdownRelayServers(servers)
	r.pipelineWG.Wait()
	return shutdownErr
}

func (r *Relay) httpServer(ctx context.Context, address string) *http.Server {
	return &http.Server{Addr: address, Handler: r.Handler(), BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
}

func shutdownRelayServers(servers []*http.Server) error {
	// #nosec G118 -- shutdown must outlive the cancelled serving context.
	shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var result error
	for _, server := range servers {
		if err := server.Shutdown(shutdown); err != nil && result == nil {
			result = err
		}
	}
	return result
}

func (r *Relay) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "service": "contextbridge-relay", "version": r.cfg.Version, "protocol": ProtocolVersion})
}

// Liveness deliberately proves only that this process can answer HTTP. It
// does not claim that the durable store is usable or that the relay should
// receive mutations. Existing /health clients retain their historical shape;
// HA-aware operators use the explicit endpoints below.
func (r *Relay) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "live": true, "service": "contextbridge-relay",
		"version": r.cfg.Version, "protocol": ProtocolVersion,
	})
}

func (r *Relay) readinessError() error {
	r.admissionMu.RLock()
	quiescing := r.quiescing
	r.admissionMu.RUnlock()
	if quiescing {
		return errors.New("relay is quiescing")
	}
	return r.store.Ready()
}

func (r *Relay) handleReadiness(w http.ResponseWriter, _ *http.Request) {
	if err := r.readinessError(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"ok": false, "ready": false, "leader": true, "mode": "standalone",
			"service": "contextbridge-relay", "version": r.cfg.Version, "protocol": ProtocolVersion,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "ready": true, "leader": true, "mode": "standalone",
		"service": "contextbridge-relay", "version": r.cfg.Version, "protocol": ProtocolVersion,
	})
}

// Leadership is a separate contract from process liveness. A reverse proxy
// may route mutating traffic only when this endpoint is 200 and writable=true.
// The current public core has one authoritative relay, so it truthfully reports
// standalone leadership without pretending that consensus already exists.
func (r *Relay) handleLeadership(w http.ResponseWriter, _ *http.Request) {
	if err := r.readinessError(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"ok": false, "leader": true, "writable": false, "mode": "standalone",
			"cluster_id": r.authority.ClusterID, "leader_epoch": r.authority.Epoch,
			"service": "contextbridge-relay", "version": r.cfg.Version, "protocol": ProtocolVersion,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "leader": true, "writable": true, "mode": "standalone",
		"cluster_id": r.authority.ClusterID, "leader_epoch": r.authority.Epoch,
		"service": "contextbridge-relay", "version": r.cfg.Version, "protocol": ProtocolVersion,
	})
}

func (r *Relay) handleLifecycle(w http.ResponseWriter, _ *http.Request) {
	overview, err := r.store.Overview()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	idle := r.idleWithOverview(overview)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "service": "contextbridge-relay", "version": r.cfg.Version, "protocol": ProtocolVersion, "idle": idle})
}

func (r *Relay) handlePairRequest(w http.ResponseWriter, req *http.Request) {
	var input PairRequest
	if err := decodeJSON(req.Body, &input, 32<<10); err != nil || input.NodeName == "" {
		writeError(w, http.StatusBadRequest, errors.New("valid node_name and public_key are required"))
		return
	}
	publicURL := r.cfg.PublicURL
	if req.TLS != nil && r.cfg.LANPublicURL != "" {
		publicURL = r.cfg.LANPublicURL
	}
	uri := strings.TrimRight(publicURL, "/") + "/dashboard/#pair"
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
	// Opaque session selectors are routing-only evidence. They are not useful
	// to API clients and must not let one producer correlate another producer's
	// adapter session across node snapshots.
	redactNodeRoutingEvidence(nodes)
	writeJSON(w, http.StatusOK, nodes)
}

func (r *Relay) handleNodeAdmission(w http.ResponseWriter, req *http.Request) {
	var input struct{}
	if err := decodeJSON(req.Body, &input, 1<<10); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id := strings.TrimSpace(req.PathValue("id"))
	action := strings.ToLower(strings.TrimSpace(req.PathValue("action")))
	if !validJobID(id) {
		writeError(w, http.StatusBadRequest, errors.New("valid node ID is required"))
		return
	}
	var draining bool
	switch action {
	case "drain":
		draining = true
	case "resume":
		draining = false
	default:
		writeError(w, http.StatusNotFound, errors.New("node action must be drain or resume"))
		return
	}
	node, err := r.store.SetNodeDraining(id, draining)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, errors.New("node not found"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	kind, message := "node.resumed", "Worker resumed admission"
	if draining {
		kind, message = "node.draining", "Worker is draining; existing jobs may finish"
	}
	_ = r.store.AddEvent(Event{Kind: kind, Message: message, NodeID: node.ID, Data: map[string]interface{}{"running": node.Capabilities.Running}})
	if !draining {
		r.signalDispatch()
	}
	writeJSON(w, http.StatusOK, node)
}

func redactNodeRoutingEvidence(nodes []Node) {
	for nodeIndex := range nodes {
		// Provider/model health is relay-owned placement evidence. Aggregate
		// operators can inspect its effect through route explanations and metrics;
		// node listings must not reveal another producer's route labels.
		nodes[nodeIndex].RoutingHealth = nil
		nodes[nodeIndex].RoutingPerformance = nil
		for sessionIndex := range nodes[nodeIndex].Capabilities.AdapterSessions {
			nodes[nodeIndex].Capabilities.AdapterSessions[sessionIndex].SessionKey = ""
			nodes[nodeIndex].Capabilities.AdapterSessions[sessionIndex].Principal = ""
		}
	}
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
	limit := queryLimit(req, 100, maximumJobHistoryPage)
	status := cleanLabel(req.URL.Query().Get("status"), 20)
	record, _ := tokenRecord(req.Context())
	owner := ""
	if record.Role == "producer" {
		owner = record.Subject
	}
	jobs, err := r.store.ListJobSummariesForOwner(limit, status, owner)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// History is an index, not a bulk artifact endpoint. A list response never
	// repeats request/result bodies or per-node candidate arrays; callers fetch
	// one exact job when they need its retained result.
	for index := range jobs {
		jobs[index] = jobHistoryResponse(jobs[index])
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(jobHistoryWriteTimeout))
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

func (r *Relay) handleJobEvents(w http.ResponseWriter, req *http.Request) {
	job, err := r.store.GetJob(req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("job not found"))
		return
	}
	if !canReadJob(req.Context(), job) {
		writeError(w, http.StatusForbidden, errors.New("job belongs to another producer"))
		return
	}
	after := uint64(0)
	if raw := strings.TrimSpace(req.URL.Query().Get("after")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("after must be an unsigned event sequence"))
			return
		}
		after = value
	}
	page, err := r.store.ListJobEvents(job.ID, after, queryLimit(req, 100, 500))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, page)
}

func (r *Relay) handleJobRoute(w http.ResponseWriter, req *http.Request) {
	job, err := r.store.GetJob(req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("job not found"))
		return
	}
	if !canReadJob(req.Context(), job) {
		writeError(w, http.StatusForbidden, errors.New("job belongs to another producer"))
		return
	}
	if job.RoutingDecision == nil {
		writeError(w, http.StatusConflict, errors.New("job has not been assigned; no durable routing decision exists yet"))
		return
	}
	writeJSON(w, http.StatusOK, job.RoutingDecision)
}

func (r *Relay) handleRouteExplain(w http.ResponseWriter, req *http.Request) {
	var input AssignmentRequest
	if err := decodeJSON(req.Body, &input, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, _ := tokenRecord(req.Context())
	if input.Requirements.AdapterEndpointID != 0 || input.Requirements.AdapterPrincipal != "" || input.Requirements.AdapterSessionRecovery {
		writeError(w, http.StatusUnprocessableEntity, errors.New("adapter endpoint and recovery requirements are relay-assigned"))
		return
	}
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
	policyDecision, err := EvaluateExecutionPolicy(r.cfg.ExecutionPolicy, input.TenantID, input.Requirements, time.Now().UTC())
	if err != nil {
		var violation *PolicyViolation
		if errors.As(err, &violation) {
			writeErrorCode(w, http.StatusForbidden, violation.Code(), err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	nodes, err := r.routingNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	routingRequirements, requiredSessionNode := r.withSessionAffinity(input.Requirements, record.Subject)
	_, decision := rankWithDecisionForOwnerPolicy(nodes, routingRequirements, r.store.EstimateVRAM(input.Requirements), record.Subject, time.Now().UTC(), r.cfg.Placement)
	decision.ID = randomID("route_preview")
	decision.Preview = true
	decision.PolicyDecision = &policyDecision
	applyRoutingNodeConstraints(&decision, requiredSessionNode, "")
	boundRoutingDecision(&decision)
	writeJSON(w, http.StatusOK, decision)
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
	reservationState := r.markWorkerReservationTerminalState(job.AssignedNode, job.ID)
	if reservationState == workerReservationDispatched {
		// AssignedNode can also be an E2EE queue binding that has never been
		// dispatched. Only a live reservation proves there is worker execution
		// to cancel; otherwise a phantom cancel could poison the worker's bounded
		// cancel-before-dispatch cache.
		r.cancelWorkerExecution(job)
	} else {
		// A pre-dispatch reservation or queued encrypted binding proves no provider
		// action began. A missing reservation for an already assigned job does not:
		// treat that case as ambiguous and reopen the route circuit fail-closed.
		failureCode := ""
		if reservationState == workerReservationMissing && existing.Status != JobQueued {
			failureCode = FailureExecutionStateAmbiguous
		}
		_, _ = r.store.ResolveRoutingRecoveryProbe(job.AssignedNode, job.ID, failureCode)
		_, _ = r.store.ReleaseAdapterSessionJobLock(job.ID)
	}
	writeJSON(w, http.StatusOK, job)
}

func (r *Relay) cancelWorkerExecution(job Job) {
	nodeID, jobID := job.AssignedNode, job.ID
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
	message := WireMessage{Version: ProtocolVersion, Type: "cancel", JobID: jobID, Attempt: job.Attempt, Fence: job.AssignmentFence}
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
		writeErrorCode(w, http.StatusBadRequest, AdmissionCodeRequestInvalidJSON, err)
		return
	}
	idempotencyKey, err := requestIdempotencyKey(req)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, AdmissionCodeIdempotencyInvalid, err)
		return
	}
	record, _ := tokenRecord(req.Context())
	input, _, err = r.prepareAdmission(input, record, admissionSubmit)
	if err != nil {
		writeAdmissionError(w, err)
		return
	}
	requestHash := ""
	if idempotencyKey != "" {
		encoded, encodeErr := json.Marshal(input)
		if encodeErr != nil {
			writeError(w, http.StatusBadRequest, encodeErr)
			return
		}
		digest := sha256.Sum256(encoded)
		requestHash = fmt.Sprintf("%x", digest[:])
	}
	if !r.beginAdmission() {
		writeErrorCode(w, http.StatusServiceUnavailable, AdmissionCodeServiceStopping, errors.New("relay is stopping"))
		return
	}
	var job Job
	var replayed bool
	limits := r.producerLimits(record)
	if input.AssignmentID != "" {
		if idempotencyKey != "" {
			job, replayed, err = r.store.ConsumeReservationAdmittedIdempotentGovernedWithPolicy(input.AssignmentID, input.AssignmentSecret, input.Sealed, input.Source, input.TenantID, input.OwnerSubject, input.Priority, input.MaxAttempts, r.cfg.MaxQueuedJobs, limits, idempotencyKey, requestHash, input.PolicyDecision)
		} else {
			job, err = r.store.ConsumeReservationAdmittedGovernedWithPolicy(input.AssignmentID, input.AssignmentSecret, input.Sealed, input.Source, input.TenantID, input.OwnerSubject, input.Priority, input.MaxAttempts, r.cfg.MaxQueuedJobs, limits, input.PolicyDecision)
		}
	} else {
		if idempotencyKey != "" {
			job, replayed, err = r.store.CreateJobAdmittedIdempotentGoverned(input, r.cfg.MaxQueuedJobs, limits, idempotencyKey, requestHash)
		} else {
			job, err = r.store.CreateJobAdmittedGoverned(input, r.cfg.MaxQueuedJobs, limits)
		}
	}
	r.endAdmission()
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, ErrQueueFull) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(err, ErrOwnerQueueCapacity) || errors.Is(err, ErrOwnerRateCapacity) {
			status = http.StatusTooManyRequests
		} else if errors.Is(err, ErrReservationOwnerMismatch) {
			status = http.StatusForbidden
		} else if errors.Is(err, ErrIdempotencyConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		writeStoreErrorCode(w, status, err, submissionStoreErrorCode(err))
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
		writeJSON(w, http.StatusOK, jobResponse(job, req.URL.Query().Get("compact") == "1"))
		return
	}
	_ = r.store.AddEvent(Event{Kind: "job.queued", Message: "Job queued", JobID: job.ID})
	r.signalDispatch()
	writeJSON(w, http.StatusAccepted, jobResponse(job, req.URL.Query().Get("compact") == "1"))
}

// handleContractValidate applies the exact pure admission path used by a real
// submit but deliberately performs no reservation, queue write, routing, or AI
// request. One-time E2EE reservations are excluded because only atomic
// consumption can prove their secret and current ownership.
func (r *Relay) handleContractValidate(w http.ResponseWriter, req *http.Request) {
	var input SubmitRequest
	if err := decodeJSON(req.Body, &input, sealedSubmitBodyLimit(r.cfg.MaxJobBytes)); err != nil {
		writeErrorCode(w, http.StatusBadRequest, AdmissionCodeRequestInvalidJSON, err)
		return
	}
	record, _ := tokenRecord(req.Context())
	_, validation, err := r.prepareAdmission(input, record, admissionValidateOnly)
	if err != nil {
		writeAdmissionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, validation)
}

func requestIdempotencyKey(req *http.Request) (string, error) {
	values := req.Header.Values("Idempotency-Key")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", errors.New("exactly one Idempotency-Key header is allowed")
	}
	if err := ValidateIdempotencyKey(values[0]); err != nil {
		return "", err
	}
	return values[0], nil
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

func jobHistoryResponse(job Job) Job {
	job.Payload = nil
	job.SealedPayload = nil
	job.Result = nil
	job.SealedResult = nil
	if job.Progress != nil {
		progress := *job.Progress
		progress.Text = ""
		progress.Detail = ""
		job.Progress = &progress
	}
	if job.RoutingDecision != nil {
		decision := *job.RoutingDecision
		decision.Candidates = nil
		job.RoutingDecision = &decision
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
	if input.Requirements.AdapterEndpointID != 0 || input.Requirements.AdapterPrincipal != "" || input.Requirements.AdapterSessionRecovery {
		writeError(w, http.StatusUnprocessableEntity, errors.New("adapter endpoint and recovery requirements are relay-assigned"))
		return
	}
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
	policyDecision, err := EvaluateExecutionPolicy(r.cfg.ExecutionPolicy, input.TenantID, input.Requirements, time.Now().UTC())
	if err != nil {
		var violation *PolicyViolation
		if errors.As(err, &violation) {
			writeErrorCode(w, http.StatusForbidden, violation.Code(), err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	nodes, err := r.routingNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	routingRequirements, requiredSessionNode := r.withSessionAffinity(input.Requirements, record.Subject)
	candidates, _ := rankWithDecisionForOwnerPolicy(nodes, routingRequirements, r.store.EstimateVRAM(input.Requirements), record.Subject, time.Now().UTC(), r.cfg.Placement)
	node, found := firstSessionCandidate(candidates, requiredSessionNode)
	if !found {
		writeError(w, http.StatusServiceUnavailable, errors.New("no online node satisfies these requirements"))
		return
	}
	if node.PublicKey == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("selected node has no encryption key"))
		return
	}
	secret, _ := randomToken("as_")
	assignedRequirements := input.Requirements
	assignedRequirements.AdapterSessionRecovery = routingRequirements.AdapterSessionRecovery
	if strings.EqualFold(input.Requirements.Provider, "adapter") && len(node.Capabilities.AdapterSessions) > 0 {
		session, ok := selectReadyAdapterSession(node.Capabilities.AdapterSessions, routingRequirements)
		if !ok || session.EndpointID <= 0 {
			writeError(w, http.StatusServiceUnavailable, errors.New("selected node no longer has the required adapter endpoint"))
			return
		}
		assignedRequirements.AdapterEndpointID = selectedAdapterEndpointBinding(routingRequirements, session)
		assignedRequirements.AdapterPrincipal = session.Principal
	}
	assignment := Assignment{
		ID: randomID("assignment"), JobID: randomID("job"), NodeID: node.ID, NodeName: node.Name,
		PublicKey: node.PublicKey, Attempt: 1, OwnerSubject: record.Subject, TenantID: input.TenantID,
		ExpiresAt: time.Now().UTC().Add(r.cfg.AssignmentTTL), Requirements: assignedRequirements,
		PolicyDecision: policyDecision,
	}
	ownerLimit := maxActiveReservationsPerOwner
	if r.cfg.MaxQueuedJobs < ownerLimit {
		ownerLimit = r.cfg.MaxQueuedJobs
	}
	if !r.beginAdmission() {
		writeErrorCode(w, http.StatusServiceUnavailable, AdmissionCodeServiceStopping, errors.New("relay is stopping"))
		return
	}
	err = r.store.CreateReservationAdmitted(assignment, secret, record.Subject, r.cfg.MaxQueuedJobs, ownerLimit)
	r.endAdmission()
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrOwnerReservationCapacity) {
			status = http.StatusTooManyRequests
		} else if errors.Is(err, ErrReservationCapacity) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(err, ErrNodeDraining) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(err, ErrAdapterSessionBusy) {
			status = http.StatusConflict
		} else if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		writeStoreErrorCode(w, status, err, reservationStoreErrorCode(err))
		return
	}
	writeJSON(w, http.StatusCreated, AssignmentResponse{Assignment: assignment, Secret: secret})
}

func (r *Relay) handleCreateToken(w http.ResponseWriter, req *http.Request) {
	var input struct {
		Role           string         `json:"role"`
		Subject        string         `json:"subject"`
		Groups         []string       `json:"groups"`
		LifetimeHours  int            `json:"lifetime_hours"`
		ProducerLimits ProducerLimits `json:"producer_limits"`
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
	token, record, err := r.store.CreateTokenWithLimits(input.Role, input.Subject, input.Groups, time.Duration(input.LifetimeHours)*time.Hour, input.ProducerLimits)
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
	allowed, capacity := r.allowWorkerReconnect(record.Subject)
	if !allowed {
		w.Header().Set("Retry-After", fmt.Sprint(int(workerReconnectWindow.Seconds())))
		message := "worker reconnect rate limit exceeded"
		if capacity {
			message = "worker reconnect limiter is at capacity"
		}
		writeError(w, http.StatusTooManyRequests, errors.New(message))
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
	if len(raw) > maximumWorkerHelloWireBytes {
		conn.Close(websocket.StatusMessageTooBig, "worker hello exceeds its protocol limit")
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
	previous := r.workers[node.ID]
	r.workers[node.ID] = worker
	r.mu.Unlock()
	if previous != nil {
		// Do not wait for a close handshake from an unresponsive superseded peer;
		// the new connection must receive its authority fence before dispatch.
		previous.conn.CloseNow()
		_, _ = r.store.RequeueNode(node.ID, "worker connection replaced")
	}
	defer r.disconnectNode(node.ID, worker)
	authority := r.authority
	authorityCtx, cancelAuthority := context.WithTimeout(ctx, 10*time.Second)
	err = worker.write(authorityCtx, mustJSON(WireMessage{Version: ProtocolVersion, Type: "authority", Authority: &authority}))
	cancelAuthority()
	if err != nil {
		return
	}
	_ = r.store.AddEvent(Event{Kind: "node.online", Message: node.Name + " connected", NodeID: node.ID})
	r.signalDispatch()
	heartbeats := heartbeatRateWindow{rateWindow: rateWindow{started: time.Now()}}
	for {
		_, raw, err = conn.Read(ctx)
		if err != nil {
			return
		}
		var header struct {
			Version int    `json:"version"`
			Type    string `json:"type"`
		}
		if json.Unmarshal(raw, &header) != nil || header.Version != ProtocolVersion {
			continue
		}
		if int64(len(raw)) > workerMessageWireLimit(header.Type) {
			conn.Close(websocket.StatusMessageTooBig, "worker message exceeds its type-specific protocol limit")
			return
		}
		var message WireMessage
		if json.Unmarshal(raw, &message) != nil || message.Version != ProtocolVersion {
			continue
		}
		switch message.Type {
		case "heartbeat":
			now := time.Now()
			if !allowHeartbeatWindow(&heartbeats, len(raw), now) {
				conn.Close(websocket.StatusPolicyViolation, "worker heartbeat rate limit exceeded")
				return
			}
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
			node.LastSeen = now.UTC()
			if !node.Capabilities.ClockTime.IsZero() {
				node.ClockOffsetMS = node.Capabilities.ClockTime.Sub(node.LastSeen).Milliseconds()
			}
			_ = r.store.UpsertNode(node)
		case "started":
			_, _ = r.store.MarkRunningFenced(message.JobID, node.ID, message.Attempt, message.Fence)
		case "progress":
			if message.Progress != nil {
				_, _ = r.store.UpdateJobProgressFenced(message.JobID, node.ID, message.Attempt, message.Fence, *message.Progress)
			}
		case "result":
			usage := priceUsage(message.Usage, r.cfg.Pricing)
			job, completeErr := r.store.CompleteJobWithFailureFenced(message.JobID, node.ID, message.Attempt, message.Fence, message.Result, message.SealedResult, usage, message.Error, message.FailureCode, message.Execution)
			// Only the current assignment generation may release the worker slot.
			// A final job can still receive its matching late result after a producer
			// cancellation or relay timeout. That matching result proves execution
			// really ended, so the slot is safe to release even though the store
			// rejects the state transition. The timeout itself is not such proof.
			final := job.Status == JobCompleted || job.Status == JobFailed || job.Status == JobCancelled
			executionEnded := completeErr == nil || (final && job.AssignedNode == node.ID && job.Attempt == message.Attempt && assignmentFencePointersEqual(job.AssignmentFence, message.Fence)) || worker.matchesDispatch(message.JobID, message.Attempt, message.Fence)
			if executionEnded {
				// A producer cancellation is only a logical terminal state. The
				// matching fenced result is the first proof that worker execution
				// actually ended, so only now may its route probe be released.
				_, _ = r.store.ResolveRoutingRecoveryProbe(node.ID, message.JobID, "")
				_, _ = r.store.ReleaseAdapterSessionJobLock(message.JobID)
				worker.release(message.JobID)
				r.signalDispatch()
			}
			if completeErr == nil {
				kind := "job.completed"
				event := Event{Kind: kind, Message: "Worker reported " + job.Status, JobID: job.ID, NodeID: node.ID}
				if job.Status == JobQueued {
					kind = "job.retrying"
					event.Data = map[string]interface{}{
						"attempt": message.Attempt, "max_attempts": job.MaxAttempts,
						"failure_code": normalizedWorkerFailureCode(message.FailureCode, message.Error),
					}
				} else if job.Status == JobFailed {
					kind = "job.failed"
				}
				event.Kind = kind
				_ = r.store.AddEvent(event)
			}
		}
	}
}

func workerMessageWireLimit(messageType string) int64 {
	switch messageType {
	case "heartbeat":
		return maximumWorkerHeartbeatBytes
	case "progress":
		return maximumWorkerProgressBytes
	case "result":
		return MaximumJobResultWireBytes
	default:
		return maximumWorkerControlBytes
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
	owners, afterKey := r.dispatchScanSnapshot()
	jobs, nextKey, err := r.store.QueuedJobsFairWindow(200, afterKey, owners)
	if err != nil || len(jobs) == 0 {
		return
	}
	r.recordQueueScan(nextKey)
	nodes, err := r.routingNodes()
	if err != nil {
		return
	}
	for _, queued := range jobs {
		if busy, busyErr := r.store.AdapterSessionBusy(queued.OwnerSubject, queued.Requirements, queued.ID); busyErr != nil || busy {
			continue
		}
		routingRequirements := queued.Requirements
		requiredSessionNode := ""
		sealedAssignment := queued.SealedPayload != nil
		if sealedAssignment {
			requiredSessionNode = queued.AssignedNode
			if routingRequirements.AdapterEndpointID > 0 {
				// The reservation authenticated this exact endpoint in JobAAD. If it
				// disappears, wait for it instead of mutating encrypted context.
				routingRequirements.AdapterSessionRecovery = false
			}
		} else {
			routingRequirements, requiredSessionNode = r.withSessionAffinity(queued.Requirements, queued.OwnerSubject)
		}
		estimatedVRAM := r.store.EstimateVRAM(queued.Requirements)
		candidates, decision := rankWithDecisionForOwnerPolicy(nodes, routingRequirements, estimatedVRAM, queued.OwnerSubject, now, r.cfg.Placement)
		if queued.PolicyDecision.Schema != "" {
			policyDecision := queued.PolicyDecision
			decision.PolicyDecision = &policyDecision
		}
		applyRoutingNodeConstraints(&decision, requiredSessionNode, queued.AssignedNode)
		for _, candidate := range candidates {
			if requiredSessionNode != "" && candidate.Node.ID != requiredSessionNode {
				continue
			}
			if queued.AssignedNode != "" && queued.AssignedNode != candidate.Node.ID {
				continue
			}
			r.mu.RLock()
			worker := r.workers[candidate.Node.ID]
			r.mu.RUnlock()
			if worker == nil {
				rejectRoutingCandidate(&decision, candidate.Node.ID, "worker_not_connected")
				continue
			}
			adapterEndpointID := queued.Requirements.AdapterEndpointID
			adapterPrincipal := queued.Requirements.AdapterPrincipal
			adapterSessionRecovery := routingRequirements.AdapterSessionRecovery
			if sealedAssignment {
				adapterSessionRecovery = queued.Requirements.AdapterSessionRecovery
			}
			if strings.EqualFold(queued.Requirements.Provider, "adapter") && len(candidate.Node.Capabilities.AdapterSessions) > 0 {
				session, ok := selectReadyAdapterSession(candidate.Node.Capabilities.AdapterSessions, routingRequirements)
				if !ok || session.EndpointID <= 0 {
					continue
				}
				if !sealedAssignment {
					adapterEndpointID = selectedAdapterEndpointBinding(routingRequirements, session)
					adapterPrincipal = session.Principal
				}
			}
			if !r.beginAdmission() {
				return
			}
			if !worker.reserve(queued.ID) {
				r.endAdmission()
				rejectRoutingCandidate(&decision, candidate.Node.ID, "worker_at_capacity")
				continue
			}
			var job Job
			var assignErr error
			decision.SelectedNodeID = candidate.Node.ID
			decision.SelectedNodeName = candidate.Node.Name
			boundRoutingDecision(&decision)
			if strings.EqualFold(queued.Requirements.Provider, "adapter") {
				job, assignErr = r.store.AssignAdapterJobFencedWithDecision(queued.ID, candidate.Node.ID, adapterEndpointID, adapterPrincipal, adapterSessionRecovery, decision, r.authority)
			} else {
				job, assignErr = r.store.AssignJobFencedWithDecision(queued.ID, candidate.Node.ID, decision, r.authority)
			}
			if assignErr != nil {
				worker.release(queued.ID)
				r.endAdmission()
				if errors.Is(assignErr, ErrNodeDraining) {
					rejectRoutingCandidate(&decision, candidate.Node.ID, "worker_draining")
					continue
				}
				if errors.Is(assignErr, ErrRouteProbeInFlight) {
					rejectRoutingCandidate(&decision, candidate.Node.ID, "route_probe_in_flight")
					continue
				}
				break
			}
			// Cancellation may win after the slot reservation but before the
			// durable assignment. In that case markStoreTerminal records the win
			// without emitting a phantom cancel, and this gate ensures the already
			// cancelled job is never written to the worker afterward.
			if !worker.beginDispatch(job.ID, job.Attempt, job.AssignmentFence) {
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
				r.cancelWorkerExecution(job)
			} else {
				_, _ = r.store.ReleaseAdapterSessionJobLock(job.ID)
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
	return r.markWorkerReservationTerminalState(nodeID, jobID) == workerReservationDispatched
}

func (r *Relay) markWorkerReservationTerminalState(nodeID, jobID string) workerReservationTerminalState {
	if nodeID == "" || jobID == "" {
		return workerReservationMissing
	}
	r.mu.RLock()
	worker := r.workers[nodeID]
	r.mu.RUnlock()
	if worker != nil {
		return worker.markStoreTerminalState(jobID)
	}
	return workerReservationMissing
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

func (r *Relay) dispatchScanSnapshot() (map[int]string, []byte) {
	r.fairnessMu.Lock()
	defer r.fairnessMu.Unlock()
	result := make(map[int]string, len(r.lastOwner))
	for priority, owner := range r.lastOwner {
		result[priority] = owner
	}
	return result, append([]byte(nil), r.queueScanAfter...)
}

func (r *Relay) recordQueueScan(next []byte) {
	r.fairnessMu.Lock()
	r.queueScanAfter = append(r.queueScanAfter[:0], next...)
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
	if pruned.Jobs > 0 || pruned.Events > 0 || pruned.PipelineRuns > 0 || pruned.SessionPlacements > 0 {
		r.logger.Printf("relay history retention removed %d terminal jobs, %d events, %d terminal pipeline runs, and %d session placements", pruned.Jobs, pruned.Events, pruned.PipelineRuns, pruned.SessionPlacements)
	}
}

func (r *Relay) ownerQueueLimit() int {
	if r.cfg.MaxQueuedJobs <= 0 || r.cfg.MaxQueuedJobs > maxQueuedJobsPerOwner {
		return maxQueuedJobsPerOwner
	}
	return r.cfg.MaxQueuedJobs
}

func (r *Relay) producerLimits(record TokenRecord) ProducerLimits {
	limits := record.ProducerLimits
	defaultQueueLimit := r.ownerQueueLimit()
	if limits.MaxQueuedJobs <= 0 || limits.MaxQueuedJobs > defaultQueueLimit {
		limits.MaxQueuedJobs = defaultQueueLimit
	}
	return limits
}

func scopeNodeCapabilities(capabilities *Capabilities, record TokenRecord) {
	capabilities.OS = cleanLabel(capabilities.OS, 80)
	capabilities.OSVersion = cleanLabel(capabilities.OSVersion, 160)
	capabilities.Architecture = cleanLabel(capabilities.Architecture, 40)
	capabilities.CPU = cleanLabel(capabilities.CPU, 160)
	capabilities.MemoryType = cleanLabel(capabilities.MemoryType, 80)
	capabilities.AgentVersion = cleanLabel(capabilities.AgentVersion, 40)
	capabilities.CPUCores = boundedInt(capabilities.CPUCores, 0, 65536)
	capabilities.CPUFrequency = boundedInt(capabilities.CPUFrequency, 0, 10_000_000)
	capabilities.CPUUtilization = boundedInt(capabilities.CPUUtilization, 0, 100)
	capabilities.UTCOffsetSeconds = boundedInt(capabilities.UTCOffsetSeconds, -24*60*60, 24*60*60)
	capabilities.MemoryTotal = min(capabilities.MemoryTotal, MaximumNodeHardwareBytes)
	capabilities.MemoryFree = min(capabilities.MemoryFree, capabilities.MemoryTotal)
	if len(capabilities.GPUs) > MaximumGPUCapabilities {
		capabilities.GPUs = capabilities.GPUs[:MaximumGPUCapabilities]
	}
	for index := range capabilities.GPUs {
		gpu := &capabilities.GPUs[index]
		gpu.Name = cleanLabel(gpu.Name, 160)
		gpu.Backend = cleanLabel(gpu.Backend, 40)
		gpu.Driver = cleanLabel(gpu.Driver, 80)
		gpu.MemoryTotal = min(gpu.MemoryTotal, MaximumNodeHardwareBytes)
		gpu.MemoryFree = min(gpu.MemoryFree, gpu.MemoryTotal)
		gpu.Temperature = boundedInt(gpu.Temperature, -100, 1000)
		gpu.Utilization = boundedInt(gpu.Utilization, 0, 100)
	}
	if len(capabilities.Models) > MaximumModelCapabilities {
		capabilities.Models = capabilities.Models[:MaximumModelCapabilities]
	}
	models := capabilities.Models[:0]
	for index := range capabilities.Models {
		model := capabilities.Models[index]
		model.Name = cleanLabel(model.Name, 160)
		if model.Name == "" {
			continue
		}
		model.Provider = cleanLabel(model.Provider, 80)
		model.CapabilitySource = cleanLabel(model.CapabilitySource, 80)
		model.Tasks = cleanList(model.Tasks, 32, 80)
		if model.Size < 0 {
			model.Size = 0
		}
		if model.VRAM < 0 {
			model.VRAM = 0
		}
		models = append(models, model)
	}
	capabilities.Models = models
	capabilities.Providers = cleanList(capabilities.Providers, MaximumNodeListValues, 80)
	capabilities.Tasks = cleanList(capabilities.Tasks, MaximumNodeListValues, 80)
	capabilities.Tags = cleanList(capabilities.Tags, MaximumNodeListValues, 80)
	capabilities.Groups = cleanList(capabilities.Groups, MaximumNodeListValues, 80)
	capabilities.Sources = cleanList(capabilities.Sources, MaximumNodeListValues, 160)
	capabilities.Modes = cleanList(capabilities.Modes, MaximumNodeListValues, 80)
	capabilities.AutomaticTasks = cleanAutomaticTasks(capabilities.AutomaticTasks)
	if len(record.Groups) > 0 {
		capabilities.Groups = intersectFold(capabilities.Groups, record.Groups)
	}
	capabilities.MaxConcurrent = boundedWorkerCapacity(capabilities.MaxConcurrent)
	capabilities.Running = boundedInt(capabilities.Running, 0, capabilities.MaxConcurrent)
	capabilities.AdapterEndpoints = boundedInt(capabilities.AdapterEndpoints, 0, MaximumAdapterSessions)
	capabilities.AdapterBusy = boundedInt(capabilities.AdapterBusy, 0, capabilities.AdapterEndpoints)
	capabilities.QueueDepth = boundedInt(capabilities.QueueDepth, 0, 1_000_000)
	if len(capabilities.AdapterSessions) > MaximumAdapterSessions {
		capabilities.AdapterSessions = capabilities.AdapterSessions[:MaximumAdapterSessions]
	}
	for index := range capabilities.AdapterSessions {
		session := &capabilities.AdapterSessions[index]
		session.Profile = cleanLabel(session.Profile, 80)
		session.State = cleanLabel(session.State, 40)
		session.SessionKey = cleanAdapterSessionKey(session.SessionKey)
		// Session creation is an adapter capability, not a property of a
		// hard-coded profile name. Out-of-tree adapters may publish any valid,
		// operator-configured profile and must remain fully plug-compatible.
		if session.EndpointID <= 0 || session.Profile == "" || !validRoutingLabel(session.Profile, 80) {
			session.CanCreateSession = false
		}
		if !session.CanCreateSession {
			session.DefaultNewSession = false
		}
		session.CurrentModel = cleanLabel(session.CurrentModel, MaximumAdapterChoiceBytes)
		session.CurrentReasoning = cleanLabel(session.CurrentReasoning, MaximumAdapterChoiceBytes)
		session.ModelChoices = cleanList(session.ModelChoices, MaximumAdapterModelChoices, MaximumAdapterChoiceBytes)
		session.ReasoningLevels = cleanList(session.ReasoningLevels, MaximumAdapterReasoningLevels, MaximumAdapterChoiceBytes)
	}
}

func cleanAutomaticTasks(input map[string][]string) map[string][]string {
	if input == nil {
		return nil
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string][]string, min(len(keys), 64))
	for _, key := range keys {
		if len(result) >= 64 {
			break
		}
		cleanedKey := cleanLabel(key, 80)
		if cleanedKey == "" {
			continue
		}
		result[cleanedKey] = cleanList(input[key], 32, 80)
	}
	return result
}

func boundedInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
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
		"reasoning":       requirements.Reasoning,
		"group":           requirements.Group,
		"adapter_profile": requirements.AdapterProfile,
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
	if requirements.AdapterProfile != "" {
		if !strings.EqualFold(requirements.Provider, "adapter") {
			return errors.New("requirements.adapter_profile requires provider adapter")
		}
		if !validRoutingLabel(requirements.AdapterProfile, 80) {
			return errors.New("requirements.adapter_profile must be at most 80 bytes without surrounding whitespace or control characters")
		}
	}
	if requirements.Reasoning != "" && !strings.EqualFold(requirements.Provider, "adapter") {
		return errors.New("requirements.reasoning requires provider adapter")
	}
	if requirements.AdapterEndpointID < 0 {
		return errors.New("requirements.adapter_endpoint_id is invalid")
	}
	if requirements.AdapterEndpointID > 0 && !strings.EqualFold(requirements.Provider, "adapter") {
		return errors.New("requirements.adapter_endpoint_id requires provider adapter")
	}
	if requirements.AdapterPrincipal != "" && (!strings.EqualFold(requirements.Provider, "adapter") || !validRoutingLabel(requirements.AdapterPrincipal, 80)) {
		return errors.New("requirements.adapter_principal is invalid or requires provider adapter")
	}
	if (requirements.AdapterFreshSession || requirements.AdapterEphemeralSession) && !strings.EqualFold(requirements.Provider, "adapter") {
		return errors.New("adapter fresh-session requirements require provider adapter")
	}
	if requirements.AdapterEphemeralSession && !requirements.AdapterFreshSession {
		return errors.New("requirements.adapter_ephemeral_session requires adapter_fresh_session")
	}
	if requirements.InputImageCount < 0 || requirements.InputImageCount > 12 {
		return errors.New("requirements.input_image_count must be between 0 and 12")
	}
	if requirements.InputImageBytes < 0 || requirements.InputImageBytes > 8<<20 {
		return errors.New("requirements.input_image_bytes must be between 0 and 8388608")
	}
	if requirements.InputImageMaxBytes < 0 || requirements.InputImageMaxBytes > 8<<20 || requirements.InputImageMaxBytes > requirements.InputImageBytes {
		return errors.New("requirements.input_image_max_bytes must be between 0 and input_image_bytes")
	}
	if len(requirements.InputImageMediaTypes) > 4 {
		return errors.New("requirements.input_image_media_types accepts at most four entries")
	}
	for _, mediaType := range requirements.InputImageMediaTypes {
		switch strings.ToLower(strings.TrimSpace(mediaType)) {
		case "image/png", "image/jpeg", "image/webp", "image/gif":
		default:
			return fmt.Errorf("requirements.input_image_media_types contains unsupported value %q", mediaType)
		}
	}
	if requirements.InputImageCount > 0 && !requirements.Vision {
		return errors.New("requirements.input_image_count requires vision")
	}
	if requirements.InputImageCount == 0 && (requirements.InputImageBytes > 0 || requirements.InputImageMaxBytes > 0 || len(requirements.InputImageMediaTypes) > 0) {
		return errors.New("image byte and media requirements require input_image_count")
	}
	if err := validateExecutionPolicyRequirements(requirements); err != nil {
		return err
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

func (r *Relay) withSessionAffinity(requirements Requirements, owner string) (Requirements, string) {
	requiredNode := ""
	if strings.EqualFold(requirements.Provider, "adapter") && requirements.AdapterProfile != "" && !requirements.AdapterEphemeralSession {
		requirements.AdapterSessionKey = adapterSessionRoutingKey(owner, requirements)
	}
	// Per-job adapter sessions have no durable affinity. A per-session fresh-session request,
	// however, must reuse the placement created by its first completed turn even
	// before the next heartbeat advertises the new endpoint.
	if requirements.AdapterEphemeralSession {
		return requirements, requiredNode
	}
	if nodeID, endpointID, principal, ok := r.store.RecentSessionPlacementBinding(owner, requirements); ok {
		if !contains(requirements.PreferredNodes, nodeID) {
			requirements.PreferredNodes = append([]string{nodeID}, requirements.PreferredNodes...)
		}
		if strings.EqualFold(requirements.Provider, "adapter") && requirements.AdapterEndpointID == 0 && endpointID > 0 {
			requirements.AdapterEndpointID = endpointID
			requirements.AdapterPrincipal = principal
			requirements.AdapterSessionRecovery = true
			requiredNode = nodeID
		}
	}
	return requirements, requiredNode
}

func (r *Relay) routingNodes() ([]Node, error) {
	nodes, err := r.store.ListNodes()
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for index := range nodes {
		worker := r.workers[nodes[index].ID]
		if worker == nil {
			nodes[index].Connected = false
			continue
		}
		running, capacity := worker.load()
		nodes[index].Connected = true
		nodes[index].Capabilities.Running = running
		nodes[index].Capabilities.MaxConcurrent = capacity
	}
	return nodes, nil
}

func applyRoutingNodeConstraints(decision *RoutingDecision, requiredSessionNode, assignedNode string) {
	if decision == nil {
		return
	}
	decision.SelectedNodeID = ""
	decision.SelectedNodeName = ""
	for index := range decision.Candidates {
		candidate := &decision.Candidates[index]
		if !candidate.Eligible {
			continue
		}
		if requiredSessionNode != "" && candidate.NodeID != requiredSessionNode {
			candidate.Eligible = false
			candidate.RejectionReasons = appendUniqueReason(candidate.RejectionReasons, "session_affinity_node_mismatch")
		}
		if assignedNode != "" && candidate.NodeID != assignedNode {
			candidate.Eligible = false
			candidate.RejectionReasons = appendUniqueReason(candidate.RejectionReasons, "sealed_assignment_node_mismatch")
		}
	}
	sortRoutingCandidateDecisions(decision.Candidates)
	for _, candidate := range decision.Candidates {
		if candidate.Eligible {
			decision.SelectedNodeID = candidate.NodeID
			decision.SelectedNodeName = candidate.NodeName
			break
		}
	}
}

func rejectRoutingCandidate(decision *RoutingDecision, nodeID, reason string) {
	if decision == nil {
		return
	}
	for index := range decision.Candidates {
		candidate := &decision.Candidates[index]
		if candidate.NodeID != nodeID {
			continue
		}
		candidate.Eligible = false
		candidate.RejectionReasons = appendUniqueReason(candidate.RejectionReasons, reason)
		break
	}
	applyRoutingNodeConstraints(decision, "", "")
}

func firstSessionCandidate(candidates []Candidate, requiredNode string) (Node, bool) {
	for _, candidate := range candidates {
		if requiredNode == "" || candidate.Node.ID == requiredNode {
			return candidate.Node, true
		}
	}
	return Node{}, false
}

func selectedAdapterEndpointBinding(requirements Requirements, selected AdapterSessionCapability) int {
	if requirements.AdapterSessionRecovery && requirements.AdapterEndpointID > 0 && selected.EndpointID != requirements.AdapterEndpointID {
		if requirements.AdapterSessionKey != "" && selected.SessionKey == requirements.AdapterSessionKey {
			return selected.EndpointID
		}
		// The previous endpoint disappeared. Leave execution unpinned so the adapter
		// can prove the saved opaque session identity on any endpoint on this node;
		// its completion reports the actual replacement endpoint.
		return 0
	}
	return selected.EndpointID
}

func adapterSessionRoutingKey(owner string, requirements Requirements) string {
	owner = strings.TrimSpace(owner)
	if owner == "" || !strings.EqualFold(strings.TrimSpace(requirements.Provider), "adapter") || strings.TrimSpace(requirements.AdapterProfile) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(owner + "\x00" + canonicalSessionID(requirements.SessionID)))
	return fmt.Sprintf("cb:%x", sum[:])
}

func cleanAdapterSessionKey(value string) string {
	if len(value) != 67 || !strings.HasPrefix(value, "cb:") {
		return ""
	}
	for _, character := range value[3:] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return ""
		}
	}
	return value
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
		allowed, capacity := r.allowRate("client:"+rateLimitClientKey(req), max, window)
		if !allowed {
			w.Header().Set("Retry-After", fmt.Sprint(int(window.Seconds())))
			message := "rate limit exceeded"
			if capacity {
				message = "rate limiter is at capacity"
			}
			writeError(w, http.StatusTooManyRequests, errors.New(message))
			return
		}
		next(w, req)
	}
}

func (r *Relay) allowRate(key string, max int, window time.Duration) (allowed, capacity bool) {
	now := time.Now()
	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	entry := r.rate[key]
	if entry != nil && now.Sub(entry.started) >= window {
		delete(r.rate, key)
		entry = nil
	}
	sweepEvery := min(window, time.Minute)
	if sweepEvery <= 0 {
		sweepEvery = time.Second
	}
	if r.rateLastSweep.IsZero() || now.Sub(r.rateLastSweep) >= sweepEvery {
		for savedKey, value := range r.rate {
			if now.Sub(value.started) >= window {
				delete(r.rate, savedKey)
			}
		}
		r.rateLastSweep = now
	}
	if entry == nil && len(r.rate) >= maximumRateLimitBuckets {
		return false, true
	}
	if entry == nil {
		entry = &rateWindow{started: now}
		r.rate[key] = entry
	}
	entry.count++
	return entry.count <= max, false
}

func (r *Relay) allowWorkerReconnect(nodeID string) (allowed, capacity bool) {
	now := time.Now()
	r.workerRateMu.Lock()
	defer r.workerRateMu.Unlock()
	entry := r.workerRate[nodeID]
	if entry != nil && now.Sub(entry.started) >= workerReconnectWindow {
		delete(r.workerRate, nodeID)
		entry = nil
	}
	if r.workerRateSweep.IsZero() || now.Sub(r.workerRateSweep) >= workerReconnectWindow {
		for savedNode, value := range r.workerRate {
			if now.Sub(value.started) >= workerReconnectWindow {
				delete(r.workerRate, savedNode)
			}
		}
		r.workerRateSweep = now
	}
	if entry == nil && len(r.workerRate) >= maximumRateLimitBuckets {
		return false, true
	}
	if entry == nil {
		entry = &rateWindow{started: now}
		r.workerRate[nodeID] = entry
	}
	entry.count++
	return entry.count <= maximumWorkerReconnects, false
}

func allowHeartbeatWindow(window *heartbeatRateWindow, size int, now time.Time) bool {
	if window.started.IsZero() || now.Sub(window.started) >= workerHeartbeatWindow {
		window.started = now
		window.count = 0
		window.bytes = 0
	}
	if size < 0 || int64(size) > maximumHeartbeatWindowBytes-window.bytes {
		return false
	}
	window.count++
	window.bytes += int64(size)
	return window.count <= maximumHeartbeatBurst
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
	if record.Role != "producer" {
		return nil
	}
	if len(record.Groups) > 0 {
		if requirements.Group == "" {
			if len(record.Groups) == 1 {
				requirements.Group = record.Groups[0]
			} else {
				return errors.New("a group is required for this producer token")
			}
		} else if !containsFold(record.Groups, requirements.Group) {
			return errors.New("producer token is not allowed to use this group")
		}
	}
	if len(record.ProducerLimits.Providers) > 0 {
		if requirements.Provider == "" {
			if len(record.ProducerLimits.Providers) == 1 {
				requirements.Provider = record.ProducerLimits.Providers[0]
			} else {
				return errors.New("a provider is required for this producer token")
			}
		} else if !containsFold(record.ProducerLimits.Providers, requirements.Provider) {
			return errors.New("producer token is not allowed to use this provider")
		}
	}
	if record.ProducerLimits.Egress == "local_only" {
		if requirements.Egress == "" {
			requirements.Egress = "local_only"
		} else if requirements.Egress != "local_only" {
			return errors.New("producer token permits only local execution")
		}
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
	usage = normalizeReportedUsage(usage)
	if usage.TotalTokens == 0 {
		usage.TotalTokens = saturatingUint64Add(usage.InputTokens, usage.OutputTokens)
	}
	if !finiteBoundedCost(usage.EstimatedCostUSD) || !finiteBoundedCost(usage.ReservedCostUSD) {
		usage.CostStatus = CostUnknown
		usage.CostSource = ""
		usage.EstimatedCostUSD = 0
		usage.ReservedCostUSD = 0
	}
	usage.CostKnownJobs, usage.CostUnknownJobs = 0, 0
	switch usage.CostStatus {
	case CostEstimated, CostUpperBound, CostActual:
		if usage.CostSource == "" {
			usage.CostSource = "worker_reported"
		}
		usage.CostKnownJobs = 1
	default:
		configured := pricing.Mode != "" || pricing.ComputePerHourUSD > 0 || pricing.InputPerMillionUSD > 0 || pricing.OutputPerMillionUSD > 0
		if configured {
			usage.EstimatedCostUSD = float64(usage.ComputeMS)/3600000*pricing.ComputePerHourUSD + float64(usage.InputTokens)/1000000*pricing.InputPerMillionUSD + float64(usage.OutputTokens)/1000000*pricing.OutputPerMillionUSD
			usage.CostStatus = CostEstimated
			usage.CostSource = strings.TrimSpace(pricing.Source)
			if usage.CostSource == "" {
				usage.CostSource = "relay_config"
			}
			usage.CostKnownJobs = 1
		} else {
			usage.CostStatus = CostUnknown
			usage.CostSource = ""
			usage.ReservedCostUSD = 0
			usage.EstimatedCostUSD = 0
			usage.CostUnknownJobs = 1
		}
	}
	usage.EquivalentCostUSD = float64(usage.InputTokens)/1000000*pricing.EquivalentInputUSD + float64(usage.OutputTokens)/1000000*pricing.EquivalentOutputUSD
	if !finiteBoundedCost(usage.EquivalentCostUSD) {
		usage.EquivalentCostUSD = 1_000_000_000
	}
	usage.SavedCostUSD = usage.EquivalentCostUSD - usage.EstimatedCostUSD
	if usage.SavedCostUSD < 0 {
		usage.SavedCostUSD = 0
	}
	return usage
}

func normalizeReportedUsage(usage Usage) Usage {
	const (
		maximumReportedTokens  = uint64(1_000_000_000_000)
		maximumReportedCompute = uint64((7 * 24 * time.Hour) / time.Millisecond)
		maximumReportedMemory  = uint64(1 << 60)
	)
	invalidAccounting := usage.InputTokens > maximumReportedTokens || usage.OutputTokens > maximumReportedTokens || usage.TotalTokens > maximumReportedTokens || usage.ComputeMS > maximumReportedCompute
	if invalidAccounting {
		usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.ComputeMS = 0, 0, 0, 0
		usage.CostStatus, usage.CostSource = CostUnknown, ""
		usage.ReservedCostUSD, usage.EstimatedCostUSD = 0, 0
	}
	if usage.PeakRAMBytes > maximumReportedMemory {
		usage.PeakRAMBytes = 0
	}
	if usage.PeakVRAMBytes > maximumReportedMemory {
		usage.PeakVRAMBytes = 0
	}
	if usage.PeakGPUUtilization < 0 || usage.PeakGPUUtilization > 100 {
		usage.PeakGPUUtilization = 0
	}
	if usage.ResourceScope != "job" {
		usage.ResourceScope = ""
		usage.PeakRAMBytes, usage.PeakVRAMBytes, usage.PeakGPUUtilization = 0, 0, 0
	}
	return usage
}

func finiteBoundedCost(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1_000_000_000
}

func saturatingCostAdd(left, right float64) float64 {
	if !finiteBoundedCost(left) || !finiteBoundedCost(right) || left > 1_000_000_000-right {
		return 1_000_000_000
	}
	return left + right
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
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
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

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanUniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("request must contain one JSON value")
		}
		return err
	}
	return nil
}

func scanUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key must be a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON property %q", key)
			}
			seen[key] = struct{}{}
			if err := scanUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	response := map[string]string{"error": err.Error()}
	if coded, ok := err.(interface{ Code() string }); ok && coded.Code() != "" {
		response["code"] = coded.Code()
	}
	writeJSON(w, status, response)
}

type responseCodeError struct {
	code string
	err  error
}

func (e responseCodeError) Error() string { return e.err.Error() }
func (e responseCodeError) Unwrap() error { return e.err }
func (e responseCodeError) Code() string  { return e.code }

func writeErrorCode(w http.ResponseWriter, status int, code string, err error) {
	writeError(w, status, responseCodeError{code: code, err: err})
}

func writeStoreErrorCode(w http.ResponseWriter, status int, err error, code string) {
	if code != "" {
		writeErrorCode(w, status, code, err)
		return
	}
	writeError(w, status, err)
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src "+dashboardScriptPolicy+"; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, req)
	})
}

func constantEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
