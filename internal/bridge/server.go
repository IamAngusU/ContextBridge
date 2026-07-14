package bridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

type Server struct {
	cfg       config.Config
	store     *Store
	processor *Processor
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
	return &Server{cfg: cfg, store: store, processor: NewProcessor(cfg, store), logger: logger}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/jobs", s.auth(s.handleJobs))
	mux.HandleFunc("/v1/browser/jobs/next", s.auth(s.handleBrowserNext))
	mux.HandleFunc("/v1/browser/jobs/", s.auth(s.handleBrowserComplete))
	mux.HandleFunc("/v1/browser/profiles", s.auth(s.handleProfiles))
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
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdown)
	}()
	s.logger.Printf("listening on http://%s", s.cfg.Server.Listen)
	s.logger.Printf("folder inbox: %s", s.cfg.Storage.Inbox)
	err := httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Process(ctx context.Context, job Job) Decision {
	prepareJob(&job)
	if err := s.store.SaveJob(job); err != nil {
		s.logger.Printf("job %s could not be stored: %v", job.ID, err)
	}
	decision := s.processor.Process(ctx, job)
	if err := s.store.SaveDecision(job.ID, decision); err != nil {
		s.logger.Printf("decision %s could not be stored: %v", job.ID, err)
	}
	return decision
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	queued, completed := s.store.Stats()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":        true,
		"service":   "contextbridge",
		"version":   Version,
		"queued":    queued,
		"completed": completed,
	})
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
	if err := validateJob(job); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	prepareJob(&job)
	s.logger.Printf("received job %s from %s via route %s", job.ID, job.Source, job.Route)
	decision := s.Process(r.Context(), job)
	s.logger.Printf("completed job %s: %s via %s", job.ID, decision.Verdict, decision.Provider)
	writeJSON(w, http.StatusOK, Submission{Job: job, Decision: &decision, Status: "completed"})
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

func (s *Server) handleBrowserComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/browser/jobs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[1] != "complete" || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job completion endpoint not found"})
		return
	}
	var raw json.RawMessage
	if err := decodeJSON(r.Body, &raw, 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	decision := NormalizeDecision(raw, "browser", "browser", 0)
	if !s.store.Complete(parts[0], decision) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "job is missing, expired, or already completed"})
		return
	}
	s.logger.Printf("browser completed job %s: %s", parts[0], decision.Verdict)
	writeJSON(w, http.StatusOK, decision)
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
		origin := r.Header.Get("Origin")
		if strings.HasPrefix(origin, "chrome-extension://") || strings.HasPrefix(origin, "edge-extension://") {
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
	if json.Unmarshal(raw, &job) != nil || validateJob(job) != nil {
		s.logger.Printf("ignored invalid inbox job %s", filepath.Base(path))
		return
	}
	decision := s.Process(ctx, job)
	resultPath := strings.TrimSuffix(path, ".processing.json") + ".result.json"
	result, _ := json.MarshalIndent(decision, "", "  ")
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
	if strings.TrimSpace(job.Prompt) == "" {
		return errors.New("prompt is required")
	}
	if len(job.Prompt) > 20000 || len(job.Text) > 200000 {
		return errors.New("job text exceeds configured protocol limits")
	}
	if len(job.ImageBase64) > 11<<20 {
		return errors.New("image exceeds the 8 MB decoded limit")
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
