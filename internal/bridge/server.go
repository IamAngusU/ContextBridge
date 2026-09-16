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
	draftHistoryMu      sync.Mutex
	draftHistoryDir     string
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
}

const (
	maximumJobRequestBytes     int64 = 12 << 20
	maximumInboxConcurrent           = 4
	maximumInboxScanBatch            = 256
	maximumControlRequestBytes       = 4 << 10
)

var browserScanDiagnosticPattern = regexp.MustCompile(`^(?:no trigger \([0-9]{1,3} composer menus\)|(?:composer|other) trigger, expanded=(?:true|false), submenu=(?:true|false), [0-9]{1,4} candidates(?:, open=(?:already|pointer|mouse|click))?)$`)
var browserModeControlTextPattern = regexp.MustCompile(`(?i)^(?:(?:gemini|gpt)[ ._-]*)?(?:[0-9]+(?:\.[0-9]+)?[ ._-]*)?(?:flash|pro|advanced|erweitert|schnell|fast|thinking|nachdenken|auto)(?:[ ._-]*(?:lite|flash|pro|advanced|erweitert|preview))?$`)
var browserModeControlIDPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,99}$`)
var errServiceStopping = errors.New("service is stopping")

// Idle reports whether replacing this process would interrupt local work.
func (s *Server) Idle() bool {
	queued, _ := s.store.Stats()
	return s.activeJobs.Load() == 0 && s.schedules.runningCount() == 0 && queued == 0 && s.store.BrowserStatus().BusyTabs == 0
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
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		server.draftHistoryDir = filepath.Join(home, ".contextbridge")
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
	mux.HandleFunc("/v1/browser/jobs/next", s.auth(s.handleBrowserNext))
	mux.HandleFunc("/v1/browser/heartbeat", s.auth(s.handleBrowserHeartbeat))
	mux.HandleFunc("/v1/browser/drafts", s.auth(s.handleBrowserDrafts))
	mux.HandleFunc("/v1/tunnel/heartbeat", s.auth(s.handleTunnelHeartbeat))
	mux.HandleFunc("/v1/settings/updates", s.auth(s.handleUpdateSettings))
	mux.HandleFunc("/v1/browser/jobs/", s.auth(s.handleBrowserJobAction))
	mux.HandleFunc("/v1/browser/profiles", s.auth(s.handleProfiles))
	mux.Handle("/", dashboardHandler())
	return s.cors(mux)
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
	go func() {
		<-ctx.Done()
		// An accepted local stop has already closed admission and waited for its
		// response handler to leave net/http. Closing remaining keep-alive
		// connections directly avoids a Shutdown polling race in which an idle
		// connection can otherwise consume the entire grace period. External
		// cancellation still receives the normal graceful shutdown path below.
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
	browser := s.store.BrowserStatus()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":          true,
		"service":     "contextbridge",
		"version":     Version,
		"queued":      queued,
		"active_jobs": s.activeJobs.Load(),
		"idle":        s.lifecycleIsIdle(),
		"completed":   completed,
		"browser":     browser.Connected,
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
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "stopping": true, "forced": request.Force})
	// Push the confirmation into the connection before arranging process
	// cancellation. This lets the client receive the complete JSON response
	// even though the accepted stop closes remaining keep-alive connections.
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
			"timeout_seconds": route.TimeoutSeconds, "browser_profile": route.BrowserProfile,
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
		"browser":                   s.store.BrowserStatus(),
		"tunnel":                    s.store.TunnelStatus(),
		"runtime":                   runtimeStatus,
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
			"browser": map[string]interface{}{"lease_seconds": s.cfg.Providers.Browser.LeaseSeconds},
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
	submission := Submission{Job: response, Status: "completed", ContextBridgeBrowserTabID: output.ContextBridgeBrowserTabID, ContextBridgeEphemeralBrowserTab: output.ContextBridgeEphemeralBrowserTab}
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
	// These values exist only between the relay, worker, and extension. They
	// must not become producer-visible correlation or topology metadata.
	job.ContextBridgeSessionKey = ""
	job.ContextBridgeBrowserTabID = 0
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

func (s *Server) handleBrowserNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	profile := strings.TrimSpace(r.URL.Query().Get("profile"))
	tabID := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("tab_id")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tab_id must be a positive browser tab identifier"})
			return
		}
		tabID = parsed
	}
	wait := 25 * time.Second
	if r.URL.Query().Get("wait") == "0" {
		wait = 0
	}
	deadline := time.Now().Add(wait)
	for {
		item := s.store.NextBrowserJobForTab(profile, tabID, time.Duration(s.cfg.Providers.Browser.LeaseSeconds)*time.Second)
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

func (s *Server) handleBrowserJobAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/browser/jobs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "browser job endpoint not found"})
		return
	}
	if parts[1] == "progress" {
		if r.Method == http.MethodGet {
			progress, exists, ready := s.store.BrowserProgress(parts[0])
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
		generation, err := browserLeaseGeneration(r)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		var progress BrowserProgress
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
		if !s.store.UpdateBrowserProgress(parts[0], generation, progress) {
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
		generation, err := browserLeaseGeneration(r)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		if r.Method == http.MethodGet {
			expiresAt, active := s.store.BrowserLeaseStatus(parts[0], generation)
			if !active {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing or expired"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "lease_expires_at": expiresAt})
			return
		}
		lease := time.Duration(s.cfg.Providers.Browser.LeaseSeconds) * time.Second
		if !s.store.Renew(parts[0], generation, lease) {
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
		generation, err := browserLeaseGeneration(r)
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
		case "upload", "edit", "send":
		default:
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "action must be upload, edit, or send"})
			return
		}
		lease := time.Duration(s.cfg.Providers.Browser.LeaseSeconds) * time.Second
		if !s.store.MarkBrowserAction(parts[0], generation, lease) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "job lease was lost"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "sent_unknown": true})
		return
	}
	if parts[1] == "release" {
		generation, err := browserLeaseGeneration(r)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		if !s.store.ReleaseBrowserLease(parts[0], generation) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "job lease was lost"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if parts[1] != "complete" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "browser job endpoint not found"})
		return
	}
	var raw json.RawMessage
	if err := decodeJSON(r.Body, &raw, 20<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	generation, err := browserLeaseGeneration(r)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	spec, model, ok := s.store.BrowserCompletionContext(parts[0], generation)
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing, expired, or already completed"})
		return
	}
	output := NormalizeOutput(raw, spec, "browser", model, 0)
	if !s.store.Complete(parts[0], generation, output) {
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
	s.logger.Printf("browser completed job %s: %s [%d file(s), %d reference(s)]", parts[0], status, files, references)
	writeJSON(w, http.StatusOK, output)
}

func browserLeaseGeneration(r *http.Request) (uint64, error) {
	value := strings.TrimSpace(r.Header.Get("X-ContextBridge-Lease-Generation"))
	generation, err := strconv.ParseUint(value, 10, 64)
	if err != nil || generation == 0 {
		return 0, errors.New("a valid browser lease generation is required")
	}
	return generation, nil
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

func (s *Server) handleBrowserHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var status BrowserClientStatus
	if err := decodeJSON(r.Body, &status, 128<<10); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status.State = limitedValue(status.State, 30)
	status.Origin = limitedValue(status.Origin, 300)
	status.TabTitle = limitedValue(status.TabTitle, 300)
	status.ProfileLabel = limitedValue(status.ProfileLabel, 100)
	status.ExtensionVersion = limitedValue(status.ExtensionVersion, 30)
	status.Browser = limitedValue(status.Browser, 30)
	if status.ActiveTabs < 0 {
		status.ActiveTabs = 0
	}
	if status.ActiveTabs > 16 {
		status.ActiveTabs = 16
	}
	if status.BusyTabs < 0 {
		status.BusyTabs = 0
	}
	if status.BusyTabs > status.ActiveTabs {
		status.BusyTabs = status.ActiveTabs
	}
	if len(status.Tabs) > 16 {
		status.Tabs = status.Tabs[:16]
	}
	for index := range status.Tabs {
		status.Tabs[index].Origin = limitedValue(status.Tabs[index].Origin, 300)
		status.Tabs[index].Title = limitedValue(status.Tabs[index].Title, 300)
		status.Tabs[index].Profile = limitedValue(status.Tabs[index].Profile, 100)
		status.Tabs[index].State = limitedValue(status.Tabs[index].State, 30)
		status.Tabs[index].CurrentModel = limitedValue(status.Tabs[index].CurrentModel, 100)
		status.Tabs[index].CurrentReasoning = limitedValue(status.Tabs[index].CurrentReasoning, 100)
		status.Tabs[index].Models = limitedStrings(status.Tabs[index].Models, 50, 100)
		status.Tabs[index].ReasoningLevels = limitedStrings(status.Tabs[index].ReasoningLevels, 20, 100)
		// Diagnostics are generated from fixed labels and counts only. Never
		// accept arbitrary page text in the browser status feed.
		if !browserScanDiagnosticPattern.MatchString(status.Tabs[index].ModelScan) {
			status.Tabs[index].ModelScan = ""
		}
		if !browserScanDiagnosticPattern.MatchString(status.Tabs[index].ReasoningScan) {
			status.Tabs[index].ReasoningScan = ""
		}
		switch status.Tabs[index].DOMStatus {
		case "ready", "pending", "timeout", "unavailable", "error":
		default:
			status.Tabs[index].DOMStatus = ""
		}
		if failure := status.Tabs[index].LastFailure; failure != nil {
			failure.Code = limitedValue(failure.Code, 80)
			if !strings.HasPrefix(failure.Code, "browser_") {
				status.Tabs[index].LastFailure = nil
			} else {
				switch failure.Reason {
				case "prompt_not_retained", "send_disabled", "send_missing", "composer_draft", "incompatible_tool", "provider_busy", "model_selector_missing", "model_candidates_empty", "model_candidates_empty_pill", "model_candidates_empty_form", "model_choices_empty", "model_choice_missing", "model_choice_disabled", "model_not_retained", "recovery_turn_unverified", "recovery_turn_mismatch", "recovery_draft", "recovery_attachment", "recovery_answer_unfinished", "recovery_input_missing", "recovery_editor_open", "recovery_image_busy", "recovery_response_changed", "upload_input_missing", "upload_preview_missing", "attachment_busy", "submitted_prompt_unverified", "local_bridge_unavailable", "other":
				default:
					failure.Reason = "other"
				}
			}
		}
		if dom := status.Tabs[index].DOM; dom != nil {
			switch dom.PageVisibility {
			case "visible", "hidden", "prerender", "unknown":
			default:
				dom.PageVisibility = ""
			}
			dom.Inputs = limitedDOMControls(dom.Inputs, 8)
			dom.Submit = limitedDOMControls(dom.Submit, 8)
			dom.FileInputs = limitedDOMControls(dom.FileInputs, 12)
			dom.Tools = limitedDOMControls(dom.Tools, 32)
			dom.ModelControls = limitedModelControls(dom.ModelControls, 12)
			dom.AssistantTurns = max(0, min(dom.AssistantTurns, 10000))
			dom.GeminiUserTurns = max(0, min(dom.GeminiUserTurns, 10000))
			dom.GeminiLastUserTurnCharacters = max(0, min(dom.GeminiLastUserTurnCharacters, 100000))
			dom.InputCharacters = max(0, min(dom.InputCharacters, 100000))
			dom.LastResponseCharacters = max(0, min(dom.LastResponseCharacters, 100000))
			allowedBusy := map[string]bool{"aria_busy": true, "streaming_attribute": true, "streaming_class": true, "image_loading": true, "stop_button": true}
			safeBusy := make([]string, 0, 5)
			for _, indicator := range dom.BusyIndicators {
				if !allowedBusy[indicator] {
					continue
				}
				seen := false
				for _, previous := range safeBusy {
					if previous == indicator {
						seen = true
						break
					}
				}
				if !seen {
					safeBusy = append(safeBusy, indicator)
				}
			}
			dom.BusyIndicators = safeBusy
			dom.LastResponseImages = max(0, min(dom.LastResponseImages, 100))
			dom.LastResponseLoadedImages = max(0, min(dom.LastResponseLoadedImages, 100))
			dom.ImageProgress = max(0, min(dom.ImageProgress, 100))
		}
	}
	if status.State == "" {
		status.State = "waiting"
	}
	s.store.RecordBrowserHeartbeat(status)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.BrowserProfiles)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
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
		origin := r.Header.Get("Origin")
		if strings.HasPrefix(origin, "chrome-extension://") || strings.HasPrefix(origin, "edge-extension://") || strings.HasPrefix(origin, "moz-extension://") {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
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

func limitedDOMControls(values []BrowserDOMControl, count int) []BrowserDOMControl {
	if len(values) > count {
		values = values[:count]
	}
	for index := range values {
		item := &values[index]
		item.Tag = limitedValue(item.Tag, 20)
		item.ID = limitedValue(item.ID, 100)
		item.TestID = limitedValue(item.TestID, 100)
		item.Role = limitedValue(item.Role, 40)
		item.AriaLabel = limitedValue(item.AriaLabel, 120)
		item.Text = limitedValue(item.Text, 120)
		item.Type = limitedValue(item.Type, 40)
		item.Accept = limitedValue(item.Accept, 120)
		item.HasPopup = limitedValue(item.HasPopup, 20)
		item.Expanded = limitedValue(item.Expanded, 10)
	}
	return values
}

// Model-picker diagnostics are deliberately narrower than generic tool
// diagnostics: no arbitrary page label, prompt, or response can pass through.
func limitedModelControls(values []BrowserDOMControl, count int) []BrowserDOMControl {
	values = limitedDOMControls(values, count)
	for index := range values {
		item := &values[index]
		if !browserModeControlIDPattern.MatchString(item.ID) {
			item.ID = ""
		}
		if !browserModeControlIDPattern.MatchString(item.TestID) {
			item.TestID = ""
		}
		if !browserModeControlTextPattern.MatchString(item.Text) {
			item.Text = ""
		}
		item.AriaLabel = ""
		item.Type = ""
		item.Accept = ""
	}
	return values
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
	for name, value := range map[string]string{"source": job.Source, "route": job.Route, "provider": job.Provider, "kind": job.Kind, "session_id": job.SessionID, "contextbridge_session_key": job.ContextBridgeSessionKey, "browser_profile": job.BrowserProfile, "model": job.Model, "reasoning": job.Reasoning} {
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
	if len(job.ImageBase64) > base64.StdEncoding.EncodedLen(8<<20) {
		return errors.New("image exceeds the 8 MB decoded limit")
	}
	if job.ImageBase64 != "" {
		if len(job.ImageMediaType) > 100 || (job.ImageMediaType != "" && !strings.HasPrefix(strings.ToLower(job.ImageMediaType), "image/")) {
			return errors.New("image_media_type must be an image MIME type")
		}
		decoded, err := base64.StdEncoding.DecodeString(job.ImageBase64)
		if err != nil {
			return errors.New("image_base64 must contain valid standard base64")
		}
		if len(decoded) > 8<<20 {
			return errors.New("image exceeds the 8 MB decoded limit")
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

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

var Version = "dev"

var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
