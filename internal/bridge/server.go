package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/updater"
	"github.com/IamAngusU/ContextBridge/internal/vectorstore"
)

type Server struct {
	cfg                 config.Config
	store               *Store
	schedules           *scheduleStore
	processor           *Processor
	runtime             *RuntimeManager
	updates             *updater.Manager
	rag                 vectorstore.Store
	logger              *log.Logger
	activeJobs          atomic.Int64
	scheduleAdmissionMu sync.Mutex
	regularActiveJobs   int
	jobAdmissionLimit   int
	inboxSlots          chan struct{}
	lifecycleMu         sync.RWMutex
	lifecycleIdle       func() bool
	lifecycleQuiesce    func(bool) bool
	lifecycleStop       func()
	lifecycleStopOnce   sync.Once
	stopRequestMu       sync.Mutex
	lifecycleStopping   bool
	poolSummaryMu       sync.Mutex
	poolSummary         interface{}
	poolSummaryAt       time.Time
	adapterPrincipals   []adapterPrincipalIdentity
}

const (
	maximumJobRequestBytes     int64 = 12 << 20
	maximumInboxConcurrent           = 4
	maximumInboxScanBatch            = 256
	maximumControlRequestBytes       = 4 << 10
)

var errServiceStopping = errors.New("service is stopping")

// Idle reports whether replacing this process would interrupt local work.
func (s *Server) Idle() bool {
	queued, _ := s.store.Stats()
	return s.activeJobs.Load() == 0 && s.schedules.runningCount() == 0 && queued == 0 && s.store.AdapterStatus().BusyEndpoints == 0
}

// SetLifecycleControl connects this local HTTP service to the root lifecycle
// owned by serve or run. The idle callback may include relay and worker state;
// it is deliberately supplied by the owner because Server itself does not own
// those optional components. Configure it before exposing Handler or Run.
func (s *Server) SetLifecycleControl(idle func() bool, stop func()) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.lifecycleIdle = idle
	s.lifecycleStop = stop
}

// SetLifecycleQuiesce installs an optional atomic admission gate for sibling
// components owned by a composite run. It is called only by an accepted stop
// attempt; false means a concurrent job won and the stop must be rejected.
func (s *Server) SetLifecycleQuiesce(quiesce func(force bool) bool) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.lifecycleQuiesce = quiesce
}

func (s *Server) lifecycleIsIdle() bool {
	s.lifecycleMu.RLock()
	idle := s.lifecycleIdle
	s.lifecycleMu.RUnlock()
	if idle != nil {
		return idle()
	}
	return s.Idle()
}

func (s *Server) lifecycleStopAccepted() bool {
	s.scheduleAdmissionMu.Lock()
	defer s.scheduleAdmissionMu.Unlock()
	return s.lifecycleStopping
}

func (s *Server) SetUpdater(manager *updater.Manager) {
	s.updates = manager
}

func NewServer(cfg config.Config, logger *log.Logger) (*Server, error) {
	store, err := NewStore(cfg.Storage.Directory)
	if err != nil {
		return nil, err
	}
	if err := store.ConfigureHistoryRetention(time.Duration(cfg.Storage.JobRetentionDays)*24*time.Hour, cfg.Storage.MaxJobRecords, cfg.Storage.MaxJobStorageBytes); err != nil {
		return nil, err
	}
	admissionLimit := configuredJobAdmissionLimit(cfg)
	schedules, err := newScheduleStore(cfg.Storage.Directory, admissionLimit)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Storage.Inbox, 0700); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = log.New(os.Stderr, "", log.LstdFlags)
	}
	server := &Server{
		cfg: cfg, store: store, schedules: schedules, processor: NewProcessor(cfg, store),
		runtime: NewRuntimeManager(cfg, logger), logger: logger,
		jobAdmissionLimit: admissionLimit,
		inboxSlots:        make(chan struct{}, maximumInboxConcurrent),
		adapterPrincipals: configuredAdapterPrincipals(cfg),
	}
	if server.adapterAuthMode() == "dual" {
		logger.Printf("warning: providers.adapter.auth_mode=dual enables legacy v1 adapter access with the operator token; migrate to contextbridge.adapter.v2 and scoped mode")
	}
	if cfg.RAG.Enabled {
		ragStore, ragErr := vectorstore.NewLocal(cfg.RAG.Directory, cfg.RAG.MaxDocuments)
		if ragErr != nil {
			return nil, ragErr
		}
		server.rag = ragStore
	}
	return server, nil
}

// configuredJobAdmissionLimit is the shared local service capacity for
// ordinary and scheduled jobs. A running worker is the authority for its real
// slot count; service-only installations retain the established local default.
func configuredJobAdmissionLimit(cfg config.Config) int {
	if cfg.Cluster.Worker.Enabled && cfg.Cluster.Worker.MaxConcurrent > 0 {
		return min(cfg.Cluster.Worker.MaxConcurrent, cluster.MaximumWorkerConcurrency)
	}
	return defaultJobAdmissionLimit
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/status", s.auth(s.handleStatus))
	mux.HandleFunc("/v1/system/stop", s.localOnly(s.auth(s.handleSystemStop)))
	mux.HandleFunc("/v1/jobs", s.auth(s.handleJobs))
	mux.HandleFunc("/v1/jobs/", s.auth(s.handleJobResult))
	mux.HandleFunc("/v1/schedules", s.auth(s.handleSchedules))
	mux.HandleFunc("/v1/schedules/", s.auth(s.handleScheduleAction))
	mux.HandleFunc("/v1/adapter/jobs/next", s.legacyAdapterAuth(s.handleAdapterNext))
	mux.HandleFunc("/v1/adapter/heartbeat", s.legacyAdapterAuth(s.handleAdapterHeartbeat))
	mux.HandleFunc("/v1/tunnel/heartbeat", s.auth(s.handleTunnelHeartbeat))
	mux.HandleFunc("/v1/settings/updates", s.auth(s.handleUpdateSettings))
	mux.HandleFunc("/v1/adapter/jobs/", s.legacyAdapterAuth(s.handleAdapterJobAction))
	mux.HandleFunc("/v1/operator/adapter/jobs/", s.auth(s.handleOperatorAdapterProgress))
	mux.HandleFunc("/v1/adapter/profiles", s.legacyAdapterAuth(s.handleProfiles))
	mux.HandleFunc("/v2/adapter/status", s.adapterAuth(s.handleAdapterStatusV2))
	mux.HandleFunc("/v2/adapter/jobs/next", s.adapterAuth(s.handleAdapterNext))
	mux.HandleFunc("/v2/adapter/heartbeat", s.adapterAuth(s.handleAdapterHeartbeat))
	mux.HandleFunc("/v2/adapter/jobs/", s.adapterAuth(s.handleAdapterJobAction))
	mux.HandleFunc("/v2/adapter/profiles", s.adapterAuth(s.handleProfiles))
	mux.HandleFunc("/openai/v1/models", s.auth(s.handleOpenAIModels))
	mux.HandleFunc("/openai/v1/chat/completions", s.auth(s.handleOpenAIChat))
	mux.Handle("/", dashboardHandler())
	return s.cors(mux)
}

// handleOperatorAdapterProgress exposes only local progress observation to the
// authenticated cluster worker. It is deliberately separate from the adapter
// action namespace so scoped mode can disable v1 adapter authority without
// breaking operator-owned progress forwarding.
func (s *Server) handleOperatorAdapterProgress(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/operator/adapter/jobs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if r.Method != http.MethodGet || len(parts) != 2 || parts[0] == "" || parts[1] != "progress" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "operator adapter progress endpoint not found"})
		return
	}
	progress, exists, ready := s.store.AdapterProgress(parts[0])
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "adapter job not found"})
		return
	}
	if !ready {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

func (s *Server) Run(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:              s.cfg.Server.Listen,
		Handler:           s.Handler(),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	go s.watchInbox(ctx)
	go s.runSchedules(ctx)
	s.runtime.Run(ctx)
	shutdownDone := make(chan error, 1)
	// #nosec G118 -- shutdown must derive a fresh grace context after the parent context has been cancelled.
	go func() {
		<-ctx.Done()
		// The stop response carries an exact Content-Length and closes its own
		// connection after the handler returns. The accepted-stop path can then
		// close other keep-alive connections without waiting out the grace period.
		if s.lifecycleStopAccepted() {
			shutdownDone <- httpServer.Close()
			return
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- httpServer.Shutdown(shutdown)
	}()
	s.logger.Printf("listening on http://%s", s.cfg.Server.Listen)
	s.logger.Printf("dashboard: http://%s", s.cfg.Server.Listen)
	s.logger.Printf("folder inbox: %s", s.cfg.Storage.Inbox)
	err := httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return <-shutdownDone
	}
	return err
}

func (s *Server) Process(ctx context.Context, job Job) (Output, error) {
	releaseAccounting, err := s.beginJobAccounting(ctx)
	if err != nil {
		return Output{}, err
	}
	defer releaseAccounting()
	prepareJob(&job)
	job.routeProvider = s.cfg.Route(job.Route).Provider
	if routeTask := strings.TrimSpace(s.cfg.Route(job.Route).Task); routeTask != "" {
		job.Task = routeTask
	}
	applyTaskOutput(&job, s.cfg.Route(job.Route).Task)
	if err := s.store.SaveJob(job); err != nil {
		return Output{}, err
	}
	output := s.processJob(ctx, job)
	if err := s.store.SaveOutput(job.ID, output); err != nil {
		s.logger.Printf("output %s could not be stored: %v", job.ID, err)
	}
	s.store.RecordCompleted(job, output)
	return output, nil
}

func (s *Server) SupportsIncremental(job Job) bool {
	return s.processor.SupportsIncremental(job)
}

// ProcessIncremental shares the ordinary admission, persistence and
// accounting path. Provider fragments are observations; the normalized Output
// saved after the stream closes remains the terminal authority.
func (s *Server) ProcessIncremental(ctx context.Context, job Job, emit func(string) error) (Output, error) {
	releaseAccounting, err := s.beginJobAccounting(ctx)
	if err != nil {
		return Output{}, err
	}
	defer releaseAccounting()
	prepareJob(&job)
	job.routeProvider = s.cfg.Route(job.Route).Provider
	if routeTask := strings.TrimSpace(s.cfg.Route(job.Route).Task); routeTask != "" {
		job.Task = routeTask
	}
	applyTaskOutput(&job, s.cfg.Route(job.Route).Task)
	if !s.processor.SupportsIncremental(job) {
		return Output{}, errors.New("incremental stream is not available for this route")
	}
	if err := s.store.SaveJob(job); err != nil {
		return Output{}, err
	}
	output := s.processor.ProcessIncremental(ctx, job, emit)
	if err := s.store.SaveOutput(job.ID, output); err != nil {
		s.logger.Printf("output %s could not be stored: %v", job.ID, err)
	}
	s.store.RecordCompleted(job, output)
	return output, nil
}

type scheduledExecutionContextKey struct{}

func withScheduledExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, scheduledExecutionContextKey{}, true)
}

func isScheduledExecution(ctx context.Context) bool {
	value, _ := ctx.Value(scheduledExecutionContextKey{}).(bool)
	return value
}

// beginJobAccounting serializes ordinary-job accounting with schedule claims.
// The marker is private context state: the public Job.Source field cannot be
// used by a caller to masquerade as an already-reserved scheduled run.
func (s *Server) beginJobAccounting(ctx context.Context) (func(), error) {
	s.scheduleAdmissionMu.Lock()
	if s.lifecycleStopping {
		s.scheduleAdmissionMu.Unlock()
		return nil, errServiceStopping
	}
	regular := !isScheduledExecution(ctx)
	if regular && s.regularActiveJobs+s.schedules.runningCount() >= s.jobAdmissionLimit {
		s.scheduleAdmissionMu.Unlock()
		return nil, errScheduleCapacity
	}
	s.activeJobs.Add(1)
	if regular {
		s.regularActiveJobs++
	}
	s.scheduleAdmissionMu.Unlock()
	return func() {
		s.scheduleAdmissionMu.Lock()
		if regular {
			s.regularActiveJobs--
		}
		s.activeJobs.Add(-1)
		s.scheduleAdmissionMu.Unlock()
	}, nil
}

func (s *Server) processJob(ctx context.Context, job Job) Output {
	task := jobTask(job, s.cfg.Route(job.Route).Task)
	if task != "rag_ingest" && task != "rag_query" {
		return s.processor.Process(ctx, job)
	}
	if s.rag == nil {
		return OutputError("rag", "contextbridge", "rag", "rag_disabled", 0)
	}
	started := time.Now()
	if task == "rag_ingest" {
		texts := make([]string, len(job.Documents))
		for index, document := range job.Documents {
			texts[index] = document.Text
		}
		embed := Job{ID: job.ID + "-embedding", Source: job.Source, Route: s.cfg.RAG.EmbeddingRoute, Task: "embedding", Texts: texts, TenantID: job.TenantID, Metadata: map[string]interface{}{"embedding_role": "passage"}, Output: OutputSpec{Mode: "embedding"}}
		vectors := s.processor.Process(ctx, embed)
		if vectors.Error != "" || len(vectors.Embeddings) != len(job.Documents) {
			return OutputError("rag", vectors.Provider, vectors.Model, "embedding_failed", time.Since(started))
		}
		if err := s.rag.Upsert(ctx, job.TenantID, job.Documents, vectors.Embeddings); err != nil {
			return OutputError("rag", vectors.Provider, vectors.Model, err.Error(), time.Since(started))
		}
		return Output{Mode: "rag", Indexed: len(job.Documents), TenantID: job.TenantID, Provider: vectors.Provider, Model: vectors.Model, LatencyMS: time.Since(started).Milliseconds()}
	}
	query := strings.TrimSpace(job.Query)
	if query == "" {
		query = strings.TrimSpace(job.Text)
	}
	embed := Job{ID: job.ID + "-embedding", Source: job.Source, Route: s.cfg.RAG.EmbeddingRoute, Task: "embedding", Text: query, TenantID: job.TenantID, Metadata: map[string]interface{}{"embedding_role": "query"}, Output: OutputSpec{Mode: "embedding"}}
	vectors := s.processor.Process(ctx, embed)
	if vectors.Error != "" || len(vectors.Embeddings) != 1 {
		return OutputError("rag", vectors.Provider, vectors.Model, "embedding_failed", time.Since(started))
	}
	matches, err := s.rag.Search(ctx, job.TenantID, vectors.Embeddings[0], job.TopK)
	if err != nil {
		return OutputError("rag", vectors.Provider, vectors.Model, err.Error(), time.Since(started))
	}
	return Output{Mode: "rag", Matches: matches, TenantID: job.TenantID, Provider: vectors.Provider, Model: vectors.Model, LatencyMS: time.Since(started).Milliseconds()}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	serverNow := time.Now()
	queued, completed := s.store.Stats()
	adapter := s.store.AdapterStatus()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":          true,
		"service":     "contextbridge",
		"version":     Version,
		"queued":      queued,
		"active_jobs": s.activeJobs.Load(),
		"idle":        s.lifecycleIsIdle(),
		"completed":   completed,
		"adapter":     adapter.Connected,
		"server_time": serverNow.UTC(),
	})
}

func (s *Server) handleSystemStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maximumControlRequestBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read request"})
		return
	}
	if int64(len(raw)) > maximumControlRequestBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "control request is too large"})
		return
	}
	request := struct {
		Force bool `json:"force"`
	}{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := decodeJSON(bytes.NewReader(raw), &request, maximumControlRequestBytes); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	s.lifecycleMu.RLock()
	stop := s.lifecycleStop
	quiesce := s.lifecycleQuiesce
	s.lifecycleMu.RUnlock()
	if stop == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this process has no local stop controller"})
		return
	}
	s.stopRequestMu.Lock()
	defer s.stopRequestMu.Unlock()
	s.scheduleAdmissionMu.Lock()
	alreadyStopping := s.lifecycleStopping
	s.lifecycleStopping = true
	s.scheduleAdmissionMu.Unlock()
	if !alreadyStopping {
		if !request.Force && !s.lifecycleIsIdle() {
			s.scheduleAdmissionMu.Lock()
			s.lifecycleStopping = false
			s.scheduleAdmissionMu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "ContextBridge is busy; wait for active or queued jobs to finish, or retry with --force"})
			return
		}
		if quiesce != nil && !quiesce(request.Force) {
			s.scheduleAdmissionMu.Lock()
			s.lifecycleStopping = false
			s.scheduleAdmissionMu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "ContextBridge became busy while stopping; work was preserved, retry when idle or use --force"})
			return
		}
	}
	payload, _ := json.Marshal(map[string]bool{"ok": true, "stopping": true, "forced": request.Force})
	payload = append(payload, '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
	// Push the exact-length confirmation into the connection before arranging
	// process cancellation. Unlike a chunked body, it needs no later terminator.
	_ = http.NewResponseController(w).Flush()
	// Wait until net/http has observed ServeHTTP returning and has cancelled
	// this request context before cancelling the process root. Cancelling here,
	// while this handler is still counted as active, can make Shutdown wait on
	// the very request that initiated it (most visibly under the race detector).
	requestDone := r.Context().Done()
	go func() {
		<-requestDone
		s.lifecycleStopOnce.Do(stop)
	}()
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	queued, completed := s.store.Stats()
	runtimeStatus := s.runtime.Snapshot(r.Context())
	routes := make(map[string]interface{}, len(s.cfg.Routes))
	for name, route := range s.cfg.Routes {
		routes[name] = map[string]interface{}{
			"provider": route.Provider, "fallback": route.Fallback,
			"timeout_seconds": route.TimeoutSeconds, "adapter_profile": route.AdapterProfile,
			"task": route.Task, "model": route.Model,
		}
	}
	ollama, _ := s.cfg.Engine("ollama")
	serverNow := time.Now()
	_, utcOffset := serverNow.Zone()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":                        true,
		"service":                   "contextbridge",
		"version":                   Version,
		"server_time":               serverNow.UTC(),
		"server_utc_offset_seconds": utcOffset,
		"listen":                    s.cfg.Server.Listen,
		"queued":                    queued,
		"completed":                 completed,
		"adapter":                   s.store.AdapterStatus(),
		"tunnel":                    s.store.TunnelStatus(),
		"runtime":                   runtimeStatus,
		"pool":                      s.readPoolSummary(r.Context()),
		"metrics":                   s.store.Metrics(),
		"schedules":                 s.schedules.status(),
		"updates":                   updateStatus(s.updates),
		"rag": map[string]interface{}{
			"enabled": s.rag != nil, "backend": s.cfg.RAG.Backend,
			"documents": ragCount(s.rag), "embedding_route": s.cfg.RAG.EmbeddingRoute,
		},
		"routes": routes,
		"providers": map[string]interface{}{
			"ollama": map[string]interface{}{
				"url": ollama.URL, "model": s.cfg.Providers.Ollama.Model,
				"images": s.cfg.Providers.Ollama.Images,
			},
			"adapter": map[string]interface{}{"lease_seconds": s.cfg.Providers.Adapter.LeaseSeconds},
		},
		"storage":  map[string]string{"directory": s.cfg.Storage.Directory, "inbox": s.cfg.Storage.Inbox, "models": s.cfg.Storage.Models},
		"activity": s.store.Activity(),
	})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the update manager is unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.updates.LocalStatus())
	case http.MethodPut:
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&input); err != nil || input.Enabled == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enabled must be true or false"})
			return
		}
		status, err := s.updates.SetEnabled(*input.Enabled)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update preference could not be stored"})
			return
		}
		writeJSON(w, http.StatusOK, status)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or PUT required"})
	}
}

func updateStatus(manager *updater.Manager) interface{} {
	if manager == nil {
		return nil
	}
	return manager.LocalStatus()
}

func (s *Server) handleTunnelHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var status TunnelStatus
	if err := decodeJSON(r.Body, &status, 16<<10); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status.State = limitedValue(status.State, 30)
	status.Target = limitedValue(status.Target, 200)
	status.Transport = limitedValue(status.Transport, 80)
	if status.LocalPort < 0 || status.LocalPort > 65535 || status.RemotePort < 0 || status.RemotePort > 65535 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "ports must be between 0 and 65535"})
		return
	}
	s.store.RecordTunnelHeartbeat(status)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var job Job
	if err := decodeJSON(r.Body, &job, maximumJobRequestBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.validateRoute(job.Route); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if routeTask := strings.TrimSpace(s.cfg.Route(job.Route).Task); routeTask != "" {
		job.Task = routeTask
	}
	if expectedTask := strings.TrimSpace(r.Header.Get("X-ContextBridge-Expected-Task")); expectedTask != "" && !strings.EqualFold(jobTask(job, s.cfg.Route(job.Route).Task), expectedTask) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "worker-approved task does not match the selected local route"})
		return
	}
	applyTaskOutput(&job, s.cfg.Route(job.Route).Task)
	if err := validateJob(job); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	prepareJob(&job)
	s.logger.Printf("received job %s from %s via route %s", job.ID, job.Source, job.Route)
	s.store.AddActivity("received", "Job received from "+job.Source, job.ID)
	output, err := s.Process(r.Context(), job)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		} else if errors.Is(err, errScheduleCapacity) || errors.Is(err, errServiceStopping) {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]string{"error": "job could not be reserved: " + err.Error()})
		return
	}
	response := responseJob(job)
	if r.URL.Query().Get("compact") == "1" {
		response = compactResponseJob(job)
	}
	submission := Submission{Job: response, Status: "completed", ContextBridgeAdapterEndpointID: output.ContextBridgeAdapterEndpointID, ContextBridgeEphemeralAdapterEndpoint: output.ContextBridgeEphemeralAdapterEndpoint}
	if output.Mode == "decision" && output.Decision != nil {
		submission.Decision = output.Decision
		s.logger.Printf("completed job %s: %s via %s", job.ID, output.Decision.Verdict, output.Decision.Provider)
		s.store.AddActivity("decision", "Decision: "+output.Decision.Verdict+" via "+output.Decision.Provider, job.ID)
	} else {
		submission.Output = &output
		status := output.Mode
		if output.Error != "" {
			status = output.Error
		}
		files, references := artifactCounts(output.Artifacts)
		s.logger.Printf("completed job %s: %s via %s [%d file(s), %d reference(s)]", job.ID, status, output.Provider, files, references)
		s.store.AddActivity("output", "Output: "+status+" via "+output.Provider, job.ID)
	}
	writeJSON(w, http.StatusOK, submission)
}

func responseJob(job Job) Job {
	// Returning the base64 input is redundant and can make a legal 8 MiB
	// visual input plus a legal 12 MiB artifact exceed transport response
	// limits. Keep its media type and routing metadata, but not the bytes.
	job.ImageBase64 = ""
	// These values exist only between the relay, worker, and adapter. They
	// must not become producer-visible correlation or topology metadata.
	job.ContextBridgeSessionKey = ""
	job.ContextBridgeAdapterEndpointID = 0
	job.ContextBridgeAdapterPrincipal = ""
	return job
}

func compactResponseJob(job Job) Job {
	job = responseJob(job)
	// Cluster workers already retain the submitted payload in the relay job.
	// A compact local response therefore carries only routing/result metadata,
	// never a second copy of potentially sensitive or multi-megabyte inputs.
	job.Prompt = ""
	job.Text = ""
	job.Texts = nil
	job.Documents = nil
	job.Query = ""
	return job
}

func (s *Server) handleAdapterNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	profile := strings.TrimSpace(r.URL.Query().Get("profile"))
	identity, scoped := adapterPrincipalFromRequest(r)
	if scoped && !identity.allowsProfile(profile) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "adapter principal is not allowed to use this profile"})
		return
	}
	endpointID := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("endpoint_id")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint_id must be a positive adapter endpoint identifier"})
			return
		}
		endpointID = parsed
	}
	wait := 25 * time.Second
	if r.URL.Query().Get("wait") == "0" {
		wait = 0
	}
	deadline := time.Now().Add(wait)
	for {
		var item *adapterJob
		if scoped {
			if endpointID <= 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "v2 polling requires a positive endpoint_id"})
				return
			}
			var err error
			item, err = s.store.NextScopedAdapterJobForEndpoint(identity.id, profile, endpointID, r.Header.Get("X-ContextBridge-Endpoint-Capability"), time.Duration(s.cfg.Providers.Adapter.LeaseSeconds)*time.Second)
			if err != nil {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
				return
			}
		} else {
			item = s.store.NextAdapterJobForEndpoint(profile, endpointID, time.Duration(s.cfg.Providers.Adapter.LeaseSeconds)*time.Second)
		}
		if item != nil {
			writeJSON(w, http.StatusOK, item)
			return
		}
		if wait == 0 || time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Server) handleAdapterJobAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/adapter/jobs/")
	if strings.HasPrefix(r.URL.Path, "/v2/adapter/jobs/") {
		path = strings.TrimPrefix(r.URL.Path, "/v2/adapter/jobs/")
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "adapter job endpoint not found"})
		return
	}
	if parts[1] == "progress" {
		if r.Method == http.MethodGet {
			var progress AdapterProgress
			var exists, ready bool
			if identity, scoped := adapterPrincipalFromRequest(r); scoped {
				generation, capability, err := adapterLeaseCredentials(r)
				if err != nil {
					writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
					return
				}
				progress, exists, ready = s.store.AdapterProgressScoped(parts[0], generation, identity.id, capability)
			} else {
				progress, exists, ready = s.store.AdapterProgress(parts[0])
			}
			if !exists {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "job is missing or expired"})
				return
			}
			if !ready {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeJSON(w, http.StatusOK, progress)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST required"})
			return
		}
		generation, err := adapterLeaseGeneration(r)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		var progress AdapterProgress
		if err := decodeJSON(r.Body, &progress, 2<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if progress.Sequence == 0 || len(progress.Text) > 1<<20 || !utf8.ValidString(progress.Text) || len(progress.Detail) > 500 || !utf8.ValidString(progress.Detail) || progress.Percent < 0 || progress.Percent > 100 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "progress requires a sequence and at most 1 MB of UTF-8 text"})
			return
		}
		progress.Phase = strings.ToLower(strings.TrimSpace(progress.Phase))
		if progress.Phase != "generating" && progress.Phase != "stabilizing" && progress.Phase != "final" && progress.Phase != "submitting" && progress.Phase != "recovering" && progress.Phase != "rate_limited" {
			progress.Phase = "generating"
		}
		updated := false
		if identity, scoped := adapterPrincipalFromRequest(r); scoped {
			updated = s.store.UpdateAdapterProgressScoped(parts[0], generation, identity.id, r.Header.Get("X-ContextBridge-Lease-Capability"), progress)
		} else {
			updated = s.store.UpdateAdapterProgress(parts[0], generation, progress)
		}
		if !updated {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing or expired"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if parts[1] == "lease" {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST required"})
			return
		}
		generation, err := adapterLeaseGeneration(r)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		if r.Method == http.MethodGet {
			var expiresAt time.Time
			var active bool
			if identity, scoped := adapterPrincipalFromRequest(r); scoped {
				expiresAt, active = s.store.AdapterLeaseStatusScoped(parts[0], generation, identity.id, r.Header.Get("X-ContextBridge-Lease-Capability"))
			} else {
				expiresAt, active = s.store.AdapterLeaseStatus(parts[0], generation)
			}
			if !active {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing or expired"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "lease_expires_at": expiresAt})
			return
		}
		lease := time.Duration(s.cfg.Providers.Adapter.LeaseSeconds) * time.Second
		renewed := false
		if identity, scoped := adapterPrincipalFromRequest(r); scoped {
			renewed = s.store.RenewScoped(parts[0], generation, identity.id, r.Header.Get("X-ContextBridge-Lease-Capability"), lease)
		} else {
			renewed = s.store.Renew(parts[0], generation, lease)
		}
		if !renewed {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing or expired"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	if parts[1] == "claim" {
		generation, err := adapterLeaseGeneration(r)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		var claim struct {
			Action string `json:"action"`
		}
		if err := decodeJSON(r.Body, &claim, 1024); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		switch claim.Action {
		case "prepare", "mutate", "commit":
		default:
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "action must be prepare, mutate, or commit"})
			return
		}
		lease := time.Duration(s.cfg.Providers.Adapter.LeaseSeconds) * time.Second
		marked := false
		if identity, scoped := adapterPrincipalFromRequest(r); scoped {
			marked = s.store.MarkAdapterActionScoped(parts[0], generation, identity.id, r.Header.Get("X-ContextBridge-Lease-Capability"), lease)
		} else {
			marked = s.store.MarkAdapterAction(parts[0], generation, lease)
		}
		if !marked {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "job lease was lost"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "observation_only": true})
		return
	}
	if parts[1] == "release" {
		generation, err := adapterLeaseGeneration(r)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		released := false
		if identity, scoped := adapterPrincipalFromRequest(r); scoped {
			released = s.store.ReleaseAdapterLeaseScoped(parts[0], generation, identity.id, r.Header.Get("X-ContextBridge-Lease-Capability"))
		} else {
			released = s.store.ReleaseAdapterLease(parts[0], generation)
		}
		if !released {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "job lease was lost"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if parts[1] != "complete" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "adapter job endpoint not found"})
		return
	}
	identity, scoped := adapterPrincipalFromRequest(r)
	var generation uint64
	var capability string
	var err error
	if scoped {
		generation, capability, err = adapterLeaseCredentials(r)
	} else {
		generation, err = adapterLeaseGeneration(r)
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	var raw json.RawMessage
	if err := decodeJSON(r.Body, &raw, 20<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var spec OutputSpec
	var model string
	var ok bool
	if scoped {
		spec, model, ok = s.store.AdapterCompletionContextScoped(parts[0], generation, identity.id, capability)
	} else {
		spec, model, ok = s.store.AdapterCompletionContext(parts[0], generation)
	}
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing, expired, or already completed"})
		return
	}
	output := NormalizeOutput(raw, spec, "adapter", model, 0)
	completed := false
	if scoped {
		completed = s.store.CompleteScoped(parts[0], generation, identity.id, capability, output)
	} else {
		completed = s.store.Complete(parts[0], generation, output)
	}
	if !completed {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing, expired, or already completed"})
		return
	}
	status := output.Mode
	if output.Decision != nil {
		status = output.Decision.Verdict
	} else if output.Error != "" {
		status = output.Error
	}
	files, references := artifactCounts(output.Artifacts)
	s.logger.Printf("adapter completed job %s: %s [%d file(s), %d reference(s)]", parts[0], status, files, references)
	writeJSON(w, http.StatusOK, output)
}

func adapterLeaseGeneration(r *http.Request) (uint64, error) {
	value := strings.TrimSpace(r.Header.Get("X-ContextBridge-Lease-Generation"))
	generation, err := strconv.ParseUint(value, 10, 64)
	if err != nil || generation == 0 {
		return 0, errors.New("a valid adapter lease generation is required")
	}
	return generation, nil
}

func adapterLeaseCredentials(r *http.Request) (uint64, string, error) {
	generation, err := adapterLeaseGeneration(r)
	if err != nil {
		return 0, "", err
	}
	capability := strings.TrimSpace(r.Header.Get("X-ContextBridge-Lease-Capability"))
	if capability == "" || len(capability) > 256 {
		return 0, "", errors.New("a valid adapter lease capability is required")
	}
	return generation, capability, nil
}

func artifactCounts(artifacts []Artifact) (files, references int) {
	for _, artifact := range artifacts {
		if artifact.DataBase64 != "" {
			files++
		} else if artifact.URL != "" {
			references++
		}
	}
	return files, references
}

func (s *Server) handleAdapterHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var status AdapterClientStatus
	if err := decodeJSON(r.Body, &status, 128<<10); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status.State = limitedValue(status.State, 30)
	status.ProfileLabel = limitedValue(status.ProfileLabel, 100)
	status.AdapterVersion = limitedValue(status.AdapterVersion, 30)
	status.Adapter = limitedValue(status.Adapter, 30)
	if status.ActiveEndpoints < 0 {
		status.ActiveEndpoints = 0
	}
	if status.ActiveEndpoints > 16 {
		status.ActiveEndpoints = 16
	}
	if status.BusyEndpoints < 0 {
		status.BusyEndpoints = 0
	}
	if status.BusyEndpoints > status.ActiveEndpoints {
		status.BusyEndpoints = status.ActiveEndpoints
	}
	if len(status.Endpoints) > 16 {
		status.Endpoints = status.Endpoints[:16]
	}
	for index := range status.Endpoints {
		status.Endpoints[index].Principal = ""
		status.Endpoints[index].EndpointCapability = limitedValue(status.Endpoints[index].EndpointCapability, 128)
		status.Endpoints[index].Profile = limitedValue(status.Endpoints[index].Profile, 80)
		status.Endpoints[index].State = limitedValue(status.Endpoints[index].State, 30)
		status.Endpoints[index].CurrentModel = limitedValue(status.Endpoints[index].CurrentModel, 100)
		status.Endpoints[index].CurrentReasoning = limitedValue(status.Endpoints[index].CurrentReasoning, 100)
		status.Endpoints[index].Models = limitedStrings(status.Endpoints[index].Models, 50, 100)
		status.Endpoints[index].ReasoningLevels = limitedStrings(status.Endpoints[index].ReasoningLevels, 20, 100)
		status.Endpoints[index].ModelScan = limitedValue(status.Endpoints[index].ModelScan, 120)
		status.Endpoints[index].ReasoningScan = limitedValue(status.Endpoints[index].ReasoningScan, 120)
		if failure := status.Endpoints[index].LastFailure; failure != nil {
			failure.Code = limitedValue(failure.Code, 80)
			if !strings.HasPrefix(failure.Code, "adapter_") {
				status.Endpoints[index].LastFailure = nil
			} else {
				failure.Reason = limitedValue(failure.Reason, 120)
				failure.LeaseReason = limitedValue(failure.LeaseReason, 80)
			}
		}
	}
	if status.State == "" {
		status.State = "waiting"
	}
	if identity, scoped := adapterPrincipalFromRequest(r); scoped {
		seen := map[adapterEndpointKey]bool{}
		for index := range status.Endpoints {
			endpoint := &status.Endpoints[index]
			if endpoint.ID <= 0 || !identity.allowsProfile(endpoint.Profile) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "heartbeat endpoint is outside the adapter principal scope"})
				return
			}
			endpoint.Principal = identity.id
			key := adapterEndpointKey{principal: identity.id, profile: endpoint.Profile, endpoint: endpoint.ID}
			if seen[key] {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "heartbeat repeats an endpoint identity"})
				return
			}
			seen[key] = true
		}
		capabilities, err := s.store.RecordScopedAdapterHeartbeat(identity.id, status)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "protocol": adapterProtocolV2, "endpoints": capabilities})
		return
	}
	for index := range status.Endpoints {
		status.Endpoints[index].EndpointCapability = ""
	}
	s.store.RecordAdapterHeartbeat(status)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	if identity, scoped := adapterPrincipalFromRequest(r); scoped {
		profiles := make(map[string]config.AdapterProfile, len(identity.profiles))
		for profile := range identity.profiles {
			if configured, exists := s.cfg.AdapterProfiles[profile]; exists {
				profiles[profile] = configured
			}
		}
		writeJSON(w, http.StatusOK, profiles)
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.AdapterProfiles)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := bearerToken(r)
		expected := s.cfg.Server.Token
		if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "valid bearer token required"})
			return
		}
		next(w, r)
	}
}

func (s *Server) localOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "local access required"})
			return
		}
		if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
			host = host[:zone]
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "local access required"})
			return
		}
		next(w, r)
	}
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func limitedValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	return truncateUTF8(value, limit)
}

func limitedStrings(values []string, count, width int) []string {
	result := make([]string, 0, min(len(values), count))
	seen := map[string]bool{}
	for _, value := range values {
		value = limitedValue(value, width)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
		if len(result) == count {
			break
		}
	}
	return result
}

func (s *Server) watchInbox(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var directory *os.File
	defer func() {
		if directory != nil {
			_ = directory.Close()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if directory == nil {
				var err error
				directory, err = os.Open(s.cfg.Storage.Inbox)
				if err != nil {
					s.logger.Printf("inbox could not be scanned: %v", err)
					continue
				}
			}
			entries, err := directory.ReadDir(maximumInboxScanBatch)
			if errors.Is(err, io.EOF) {
				_ = directory.Close()
				directory = nil
			} else if err != nil {
				s.logger.Printf("inbox could not be scanned: %v", err)
				_ = directory.Close()
				directory = nil
				continue
			}
			for _, entry := range entries {
				name := entry.Name()
				if !strings.HasSuffix(strings.ToLower(name), ".json") || strings.HasSuffix(name, ".result.json") || strings.HasSuffix(name, ".processing.json") {
					continue
				}
				info, infoErr := entry.Info()
				if infoErr != nil || !info.Mode().IsRegular() {
					s.logger.Printf("ignored non-regular inbox entry %s", name)
					continue
				}
				select {
				case s.inboxSlots <- struct{}{}:
				default:
					continue
				}
				path := filepath.Join(s.cfg.Storage.Inbox, name)
				processing := strings.TrimSuffix(path, ".json") + ".processing.json"
				if os.Rename(path, processing) != nil {
					<-s.inboxSlots
					continue
				}
				go func() {
					defer func() { <-s.inboxSlots }()
					defer s.recoverInboxPanic(processing)
					s.processInboxFile(ctx, processing)
				}()
			}
		}
	}
}

func (s *Server) processInboxFile(ctx context.Context, path string) {
	var job Job
	if decodeInboxJob(path, &job) != nil || s.validateRoute(job.Route) != nil {
		s.logger.Printf("ignored invalid inbox job %s", filepath.Base(path))
		return
	}
	if routeTask := strings.TrimSpace(s.cfg.Route(job.Route).Task); routeTask != "" {
		job.Task = routeTask
	}
	applyTaskOutput(&job, s.cfg.Route(job.Route).Task)
	if validateJob(job) != nil {
		s.logger.Printf("ignored invalid inbox job %s", filepath.Base(path))
		return
	}
	output, err := s.Process(ctx, job)
	if err != nil {
		if errors.Is(err, errScheduleCapacity) {
			pending := strings.TrimSuffix(path, ".processing.json") + ".json"
			if renameErr := os.Rename(path, pending); renameErr != nil {
				s.logger.Printf("capacity-limited inbox job %s could not be returned to the queue: %v", filepath.Base(path), renameErr)
			}
			return
		}
		s.logger.Printf("inbox job %s could not be processed: %v", filepath.Base(path), err)
		return
	}
	resultPath := strings.TrimSuffix(path, ".processing.json") + ".result.json"
	payload := interface{}(output)
	if output.Mode == "decision" && output.Decision != nil {
		payload = *output.Decision
	}
	result, _ := json.MarshalIndent(payload, "", "  ")
	if err := writeInboxResult(resultPath, append(result, '\n')); err != nil {
		s.logger.Printf("inbox result %s could not be stored safely: %v", filepath.Base(resultPath), err)
		return
	}
	if err := os.Remove(path); err != nil {
		s.logger.Printf("processed inbox job %s could not be removed: %v", filepath.Base(path), err)
	}
}

func decodeInboxJob(path string, job *Job) error {
	file, err := openRegularNoFollow(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return decodeJSON(file, job, maximumJobRequestBytes)
}

// writeInboxResult never opens the operator-visible result pathname for
// writing. A unique regular file is completed first and then renamed, so an
// attacker-created symlink/reparse point at the final component is replaced
// as a directory entry on Unix or causes a safe failure on Windows rather than
// being followed to another file.
func writeInboxResult(path string, value []byte) error {
	if existing, err := os.Lstat(path); err == nil {
		return fmt.Errorf("result already exists (%s)", existing.Mode().Type())
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".contextbridge-result-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func prepareJob(job *Job) {
	if job.ID == "" {
		buf := make([]byte, 16)
		rand.Read(buf)
		job.ID = hex.EncodeToString(buf)
	}
	if job.Source == "" {
		job.Source = "api"
	}
	if job.Route == "" {
		job.Route = "default"
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
}

func validateJob(job Job) error {
	if job.ID != "" && (!jobIDPattern.MatchString(job.ID) || strings.Contains(job.ID, "..")) {
		return errors.New("id must use 1 to 128 letters, numbers, dots, underscores, or hyphens")
	}
	for name, value := range map[string]string{"source": job.Source, "route": job.Route, "provider": job.Provider, "kind": job.Kind, "session_id": job.SessionID, "contextbridge_session_key": job.ContextBridgeSessionKey, "contextbridge_adapter_principal": job.ContextBridgeAdapterPrincipal, "adapter_profile": job.AdapterProfile, "model": job.Model, "reasoning": job.Reasoning} {
		if len(value) > 100 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s must be at most 100 bytes without control characters", name)
		}
	}
	task := jobTask(job, "")
	if strings.TrimSpace(job.Prompt) == "" && task != "embedding" && task != "rag_ingest" && task != "rag_query" {
		return errors.New("prompt is required")
	}
	if len(job.Texts) > 256 {
		return errors.New("texts accepts at most 256 inputs")
	}
	for _, text := range job.Texts {
		if len(text) > 200000 {
			return errors.New("embedding input exceeds 200000 bytes")
		}
	}
	if len(job.TenantID) > 200 || strings.IndexFunc(job.TenantID, unicode.IsControl) >= 0 {
		return errors.New("tenant_id must be at most 200 bytes without control characters")
	}
	if len(job.Documents) > 256 {
		return errors.New("documents accepts at most 256 entries")
	}
	for _, document := range job.Documents {
		if document.ID == "" || len(document.ID) > 200 || len(document.Text) == 0 || len(document.Text) > 200000 {
			return errors.New("documents require an ID and text up to 200000 bytes")
		}
	}
	if len(job.Query) > 200000 {
		return errors.New("query exceeds 200000 bytes")
	}
	if task == "embedding" && len(job.Texts) == 0 && strings.TrimSpace(job.Text) == "" {
		return errors.New("embedding requires text or texts")
	}
	if (task == "rag_ingest" || task == "rag_query") && strings.TrimSpace(job.TenantID) == "" {
		return errors.New("RAG tasks require tenant_id")
	}
	if task == "rag_ingest" && len(job.Documents) == 0 {
		return errors.New("rag_ingest requires documents")
	}
	if task == "rag_query" && strings.TrimSpace(job.Query) == "" && strings.TrimSpace(job.Text) == "" {
		return errors.New("rag_query requires query or text")
	}
	if job.TopK < 0 || job.TopK > 50 {
		return errors.New("top_k must be between 0 and 50")
	}
	if len(job.Prompt) > 20000 || len(job.Text) > 200000 {
		return errors.New("job text exceeds configured protocol limits")
	}
	if math.IsNaN(job.MaxCostUSD) || math.IsInf(job.MaxCostUSD, 0) || job.MaxCostUSD < 0 || job.MaxCostUSD > 1_000_000 {
		return errors.New("max_cost_usd must be a finite value from 0 through 1000000")
	}
	if len(job.ImageBase64) > base64.StdEncoding.EncodedLen(8<<20) {
		return errors.New("image exceeds the 8 MB decoded limit")
	}
	if job.ImageBase64 != "" {
		mediaType := strings.ToLower(strings.TrimSpace(job.ImageMediaType))
		if mediaType != "image/png" && mediaType != "image/jpeg" && mediaType != "image/webp" && mediaType != "image/gif" {
			return errors.New("image_media_type must be image/png, image/jpeg, image/webp, or image/gif")
		}
		decoded, err := base64.StdEncoding.DecodeString(job.ImageBase64)
		if err != nil {
			return errors.New("image_base64 must contain valid standard base64")
		}
		if len(decoded) > 8<<20 {
			return errors.New("image exceeds the 8 MB decoded limit")
		}
		if !imageBytesMatchMediaType(mediaType, decoded) {
			return errors.New("image bytes do not match image_media_type")
		}
	}
	mode := outputMode(job.Output)
	if mode != "decision" && mode != "json" && mode != "text" && mode != "embedding" && mode != "rag" {
		return errors.New("output.mode must be decision, json, text, embedding, or rag")
	}
	if job.Output.MaxBytes != 0 && (job.Output.MaxBytes < 256 || job.Output.MaxBytes > 1<<20) {
		return errors.New("output.max_bytes must be between 256 and 1048576")
	}
	if job.Output.MaxArtifactBytes != 0 && (job.Output.MaxArtifactBytes < 1024 || job.Output.MaxArtifactBytes > 12<<20) {
		return errors.New("output.max_artifact_bytes must be between 1024 and 12582912")
	}
	if job.Output.MinArtifacts < 0 || job.Output.MinArtifacts > 12 {
		return errors.New("output.min_artifacts must be between 0 and 12")
	}
	if job.Output.MinArtifacts > 0 && !job.Output.Artifacts {
		return errors.New("output.min_artifacts requires output.artifacts")
	}
	if job.Output.MinArtifacts > 0 && mode != "text" && mode != "json" {
		return errors.New("output.min_artifacts requires text or json output")
	}
	if job.Output.MinImages < 0 || job.Output.MinImages > 12 {
		return errors.New("output.min_images must be between 0 and 12")
	}
	if job.Output.MinImages > 0 && !job.Output.Artifacts {
		return errors.New("output.min_images requires output.artifacts")
	}
	if job.Output.MinImages > 0 && mode != "text" && mode != "json" {
		return errors.New("output.min_images requires text or json output")
	}
	if job.Output.MinMedia < 0 || job.Output.MinMedia > 12 {
		return errors.New("output.min_media must be between 0 and 12")
	}
	if job.Output.MinMedia > 0 && !job.Output.Artifacts {
		return errors.New("output.min_media requires output.artifacts")
	}
	if job.Output.MinMedia > 0 && mode != "text" && mode != "json" {
		return errors.New("output.min_media requires text or json output")
	}
	// Images and audio/video are disjoint verified types, while one response
	// can contain at most twelve artifacts total. Reject an impossible mixed
	// requirement before dispatch instead of waiting for a provider response
	// that can never satisfy the contract. MinArtifacts is not added here: it
	// counts the same transferred files and may overlap either typed minimum.
	if job.Output.MinImages+job.Output.MinMedia > 12 {
		return errors.New("combined output.min_images and output.min_media must not exceed 12")
	}
	if len(job.Output.RequiredKeys) > 50 {
		return errors.New("output.required_keys accepts at most 50 keys")
	}
	for _, key := range job.Output.RequiredKeys {
		if key == "" || len(key) > 100 || strings.IndexFunc(key, unicode.IsControl) >= 0 {
			return errors.New("output.required_keys must contain non-empty keys up to 100 bytes")
		}
	}
	return nil
}

func imageBytesMatchMediaType(mediaType string, data []byte) bool {
	switch mediaType {
	case "image/png":
		return len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		return len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
	case "image/webp":
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
	case "image/gif":
		return len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a")))
	default:
		return false
	}
}

func ragCount(store vectorstore.Store) int {
	if store == nil {
		return 0
	}
	return store.Count()
}

func (s *Server) validateRoute(name string) error {
	if name == "" {
		name = "default"
	}
	if _, ok := s.cfg.Routes[name]; !ok {
		return fmt.Errorf("unknown route %s", name)
	}
	return nil
}

func decodeJSON(reader io.Reader, target interface{}, limit int64) error {
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	if int64(len(raw)) > limit {
		return fmt.Errorf("JSON body exceeds %d bytes", limit)
	}
	if !utf8.Valid(raw) {
		return errors.New("invalid JSON: input is not valid UTF-8")
	}
	// Go's encoding/json otherwise accepts the last value of a duplicate
	// property. That makes a signed, logged, or reviewed job ambiguous to a
	// different parser. Reject duplicates recursively at every local API and
	// inbox boundary just as the cluster relay does.
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
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

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

var Version = "dev"

var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
