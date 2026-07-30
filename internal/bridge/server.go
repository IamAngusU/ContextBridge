package bridge

import (
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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/vectorstore"
)

type Server struct {
	cfg       config.Config
	store     *Store
	processor *Processor
	runtime   *RuntimeManager
	rag       vectorstore.Store
	logger    *log.Logger
}

func NewServer(cfg config.Config, logger *log.Logger) (*Server, error) {
	store, err := NewStore(cfg.Storage.Directory)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Storage.Inbox, 0700); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = log.New(os.Stderr, "", log.LstdFlags)
	}
	server := &Server{cfg: cfg, store: store, processor: NewProcessor(cfg, store), runtime: NewRuntimeManager(cfg, logger), logger: logger}
	if cfg.RAG.Enabled {
		ragStore, ragErr := vectorstore.NewLocal(cfg.RAG.Directory, cfg.RAG.MaxDocuments)
		if ragErr != nil {
			return nil, ragErr
		}
		server.rag = ragStore
	}
	return server, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/status", s.auth(s.handleStatus))
	mux.HandleFunc("/v1/jobs", s.auth(s.handleJobs))
	mux.HandleFunc("/v1/browser/jobs/next", s.auth(s.handleBrowserNext))
	mux.HandleFunc("/v1/browser/heartbeat", s.auth(s.handleBrowserHeartbeat))
	mux.HandleFunc("/v1/tunnel/heartbeat", s.auth(s.handleTunnelHeartbeat))
	mux.HandleFunc("/v1/browser/jobs/", s.auth(s.handleBrowserJobAction))
	mux.HandleFunc("/v1/browser/profiles", s.auth(s.handleProfiles))
	mux.Handle("/", dashboardHandler())
	return s.cors(mux)
}

func (s *Server) Run(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:              s.cfg.Server.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	go s.watchInbox(ctx)
	s.runtime.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdown)
	}()
	s.logger.Printf("listening on http://%s", s.cfg.Server.Listen)
	s.logger.Printf("dashboard: http://%s", s.cfg.Server.Listen)
	s.logger.Printf("folder inbox: %s", s.cfg.Storage.Inbox)
	err := httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Process(ctx context.Context, job Job) (Output, error) {
	prepareJob(&job)
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
	queued, completed := s.store.Stats()
	browser := s.store.BrowserStatus()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":        true,
		"service":   "contextbridge",
		"version":   Version,
		"queued":    queued,
		"completed": completed,
		"browser":   browser.Connected,
	})
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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":        true,
		"service":   "contextbridge",
		"version":   Version,
		"listen":    s.cfg.Server.Listen,
		"queued":    queued,
		"completed": completed,
		"browser":   s.store.BrowserStatus(),
		"tunnel":    s.store.TunnelStatus(),
		"runtime":   runtimeStatus,
		"metrics":   s.store.Metrics(),
		"rag": map[string]interface{}{
			"enabled": s.rag != nil, "backend": s.cfg.RAG.Backend,
			"documents": ragCount(s.rag), "embedding_route": s.cfg.RAG.EmbeddingRoute,
		},
		"routes": routes,
		"providers": map[string]interface{}{
			"ollama": map[string]interface{}{
				"url": s.cfg.Providers.Ollama.URL, "model": s.cfg.Providers.Ollama.Model,
				"images": s.cfg.Providers.Ollama.Images,
			},
			"browser": map[string]interface{}{"lease_seconds": s.cfg.Providers.Browser.LeaseSeconds},
		},
		"storage":  map[string]string{"directory": s.cfg.Storage.Directory, "inbox": s.cfg.Storage.Inbox, "models": s.cfg.Storage.Models},
		"activity": s.store.Activity(),
	})
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
	if err := decodeJSON(r.Body, &job, 12<<20); err != nil {
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
		}
		writeJSON(w, status, map[string]string{"error": "job could not be reserved: " + err.Error()})
		return
	}
	submission := Submission{Job: job, Status: "completed"}
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
		s.logger.Printf("completed job %s: %s via %s", job.ID, status, output.Provider)
		s.store.AddActivity("output", "Output: "+status+" via "+output.Provider, job.ID)
	}
	writeJSON(w, http.StatusOK, submission)
}

func (s *Server) handleBrowserNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	profile := strings.TrimSpace(r.URL.Query().Get("profile"))
	wait := 25 * time.Second
	if r.URL.Query().Get("wait") == "0" {
		wait = 0
	}
	deadline := time.Now().Add(wait)
	for {
		item := s.store.NextBrowserJob(profile, time.Duration(s.cfg.Providers.Browser.LeaseSeconds)*time.Second)
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
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/browser/jobs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "browser job endpoint not found"})
		return
	}
	if parts[1] == "lease" {
		lease := time.Duration(s.cfg.Providers.Browser.LeaseSeconds) * time.Second
		if !s.store.Renew(parts[0], lease) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing or expired"})
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
	if err := decodeJSON(r.Body, &raw, 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	spec, model, ok := s.store.BrowserCompletionContext(parts[0])
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing, expired, or already completed"})
		return
	}
	output := NormalizeOutput(raw, spec, "browser", model, 0)
	if !s.store.Complete(parts[0], output) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing, expired, or already completed"})
		return
	}
	status := output.Mode
	if output.Decision != nil {
		status = output.Decision.Verdict
	} else if output.Error != "" {
		status = output.Error
	}
	s.logger.Printf("browser completed job %s: %s", parts[0], status)
	writeJSON(w, http.StatusOK, output)
}

func (s *Server) handleBrowserHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var status BrowserClientStatus
	if err := decodeJSON(r.Body, &status, 32<<10); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status.State = limitedValue(status.State, 30)
	status.Origin = limitedValue(status.Origin, 300)
	status.TabTitle = limitedValue(status.TabTitle, 300)
	status.ProfileLabel = limitedValue(status.ProfileLabel, 100)
	status.ExtensionVersion = limitedValue(status.ExtensionVersion, 30)
	status.Browser = limitedValue(status.Browser, 30)
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

func (s *Server) watchInbox(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			matches, _ := filepath.Glob(filepath.Join(s.cfg.Storage.Inbox, "*.json"))
			for _, path := range matches {
				if strings.HasSuffix(path, ".result.json") || strings.HasSuffix(path, ".processing.json") {
					continue
				}
				processing := strings.TrimSuffix(path, ".json") + ".processing.json"
				if os.Rename(path, processing) != nil {
					continue
				}
				go s.processInboxFile(ctx, processing)
			}
		}
	}
}

func (s *Server) processInboxFile(ctx context.Context, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var job Job
	if json.Unmarshal(raw, &job) != nil || s.validateRoute(job.Route) != nil {
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
		s.logger.Printf("inbox job %s could not be processed: %v", filepath.Base(path), err)
		return
	}
	resultPath := strings.TrimSuffix(path, ".processing.json") + ".result.json"
	payload := interface{}(output)
	if output.Mode == "decision" && output.Decision != nil {
		payload = *output.Decision
	}
	result, _ := json.MarshalIndent(payload, "", "  ")
	os.WriteFile(resultPath, append(result, '\n'), 0600)
	os.Remove(path)
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
	for name, value := range map[string]string{"source": job.Source, "route": job.Route, "kind": job.Kind} {
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
		if _, err := base64.StdEncoding.DecodeString(job.ImageBase64); err != nil {
			return errors.New("image_base64 must contain valid standard base64")
		}
	}
	mode := outputMode(job.Output)
	if mode != "decision" && mode != "json" && mode != "text" && mode != "embedding" && mode != "rag" {
		return errors.New("output.mode must be decision, json, text, embedding, or rag")
	}
	if job.Output.MaxBytes != 0 && (job.Output.MaxBytes < 256 || job.Output.MaxBytes > 1<<20) {
		return errors.New("output.max_bytes must be between 256 and 1048576")
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
	decoder := json.NewDecoder(io.LimitReader(reader, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
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
