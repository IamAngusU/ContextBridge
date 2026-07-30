package cluster

import (
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

	"github.com/coder/websocket"
)

type RelayConfig struct {
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
}

type Relay struct {
	cfg     RelayConfig
	store   *Store
	logger  *log.Logger
	mu      sync.RWMutex
	workers map[string]*workerConnection
	rateMu  sync.Mutex
	rate    map[string]*rateWindow
	wake    chan struct{}
}

type workerConnection struct {
	conn *websocket.Conn
	mu   sync.Mutex
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
		cfg.MaxJobBytes = 12 << 20
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
	store, err := OpenStore(cfg.Database)
	if err != nil {
		return nil, err
	}
	if err := store.EnsureToken(cfg.AdminToken, "admin", "relay-admin", nil); err != nil {
		store.Close()
		return nil, err
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Relay{cfg: cfg, store: store, logger: logger, workers: map[string]*workerConnection{}, rate: map[string]*rateWindow{}, wake: make(chan struct{}, 1)}, nil
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
	server := &http.Server{Addr: r.cfg.Listen, Handler: r.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	go r.dispatchLoop(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	r.logger.Printf("relay listening on http://%s", r.cfg.Listen)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (r *Relay) handleHealth(w http.ResponseWriter, _ *http.Request) {
	overview, err := r.store.Overview()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "service": "contextbridge-relay", "protocol": ProtocolVersion, "overview": overview})
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
	writeJSON(w, http.StatusOK, job)
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
	writeJSON(w, http.StatusOK, job)
}

func (r *Relay) handleSubmit(w http.ResponseWriter, req *http.Request) {
	if queued, _ := r.store.CountJobs(JobQueued); queued >= r.cfg.MaxQueuedJobs {
		writeError(w, http.StatusServiceUnavailable, errors.New("relay queue is full"))
		return
	}
	var input SubmitRequest
	if err := decodeJSON(req.Body, &input, r.cfg.MaxJobBytes); err != nil {
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
	if input.MaxAttempts == 0 {
		input.MaxAttempts = r.cfg.MaxAttempts
	}
	input.OwnerSubject = record.Subject
	var job Job
	var err error
	if input.AssignmentID != "" {
		job, err = r.store.ConsumeReservation(input.AssignmentID, input.AssignmentSecret, input.Sealed, input.Source, input.TenantID, input.OwnerSubject, input.Priority, input.MaxAttempts)
	} else {
		job, err = r.store.CreateJob(input)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	_ = r.store.AddEvent(Event{Kind: "job.queued", Message: "Job queued", JobID: job.ID})
	r.signalDispatch()
	writeJSON(w, http.StatusAccepted, job)
}

func (r *Relay) handleReserve(w http.ResponseWriter, req *http.Request) {
	var requirements Requirements
	if err := decodeJSON(req.Body, &requirements, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, _ := tokenRecord(req.Context())
	if err := scopeRequirements(&requirements, record); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if err := r.validateRequirements(requirements); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	nodes, _ := r.store.ListNodes()
	routingRequirements := requirements
	if routingRequirements.MinFreeVRAM == 0 {
		routingRequirements.MinFreeVRAM = r.store.EstimateVRAM(requirements)
	}
	candidates := Rank(nodes, routingRequirements)
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
	assignment := Assignment{ID: randomID("assignment"), JobID: randomID("job"), NodeID: node.ID, NodeName: node.Name, PublicKey: node.PublicKey, ExpiresAt: time.Now().UTC().Add(r.cfg.AssignmentTTL), Requirements: requirements}
	if err := r.store.CreateReservation(assignment, secret); err != nil {
		writeError(w, http.StatusInternalServerError, err)
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
	conn.SetReadLimit(r.cfg.MaxJobBytes + (4 << 20))
	defer conn.Close(websocket.StatusNormalClosure, "worker disconnected")
	ctx := req.Context()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return
	}
	var hello WireMessage
	if json.Unmarshal(raw, &hello) != nil || hello.Type != "hello" || hello.Node == nil || hello.Node.ID != record.Subject {
		conn.Close(websocket.StatusPolicyViolation, "invalid worker hello")
		return
	}
	node := *hello.Node
	if len(record.Groups) > 0 {
		node.Capabilities.Groups = intersectFold(node.Capabilities.Groups, record.Groups)
	}
	node.Connected = true
	node.State = "online"
	node.LastSeen = time.Now().UTC()
	if node.PublicKey == "" {
		if saved, loadErr := r.store.GetNode(node.ID); loadErr == nil {
			node.PublicKey = saved.PublicKey
		}
	}
	if err := r.store.UpsertNode(node); err != nil {
		conn.Close(websocket.StatusInternalError, "node could not be stored")
		return
	}
	worker := &workerConnection{conn: conn}
	r.mu.Lock()
	if previous := r.workers[node.ID]; previous != nil {
		previous.conn.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
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
			}
			node.Connected = true
			node.State = "online"
			node.LastSeen = time.Now().UTC()
			_ = r.store.UpsertNode(node)
		case "started":
			_, _ = r.store.MarkRunning(message.JobID, node.ID)
		case "result":
			usage := priceUsage(message.Usage, r.cfg.Pricing)
			job, completeErr := r.store.CompleteJob(message.JobID, message.Result, message.SealedResult, usage, message.Error)
			if completeErr == nil {
				kind := "job.completed"
				if job.Status == JobQueued {
					kind = "job.requeued"
				} else if job.Status == JobFailed {
					kind = "job.failed"
				}
				_ = r.store.AddEvent(Event{Kind: kind, Message: "Worker reported " + job.Status, JobID: job.ID, NodeID: node.ID})
				r.signalDispatch()
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
	if recovered, err := r.store.RecoverStaleJobs(time.Now().UTC(), r.cfg.AssignmentTTL, r.cfg.JobTimeout); err == nil {
		for _, job := range recovered {
			_ = r.store.AddEvent(Event{Kind: "job." + job.Status, Message: job.Error, JobID: job.ID, NodeID: job.AssignedNode})
		}
	}
	jobs, err := r.store.QueuedJobs(200)
	if err != nil || len(jobs) == 0 {
		return
	}
	nodes, err := r.store.ListNodes()
	if err != nil {
		return
	}
	for _, queued := range jobs {
		routingRequirements := queued.Requirements
		if routingRequirements.MinFreeVRAM == 0 {
			routingRequirements.MinFreeVRAM = r.store.EstimateVRAM(queued.Requirements)
		}
		candidates := Rank(nodes, routingRequirements)
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
			job, assignErr := r.store.AssignJob(queued.ID, candidate.Node.ID)
			if assignErr != nil {
				break
			}
			message := WireMessage{Version: ProtocolVersion, Type: "job", Job: &job}
			worker.mu.Lock()
			writeErr := worker.conn.Write(context.Background(), websocket.MessageText, mustJSON(message))
			worker.mu.Unlock()
			if writeErr != nil {
				_, _ = r.store.RequeueNode(candidate.Node.ID, "worker connection failed")
				r.disconnectNode(candidate.Node.ID, worker)
			} else {
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

func (r *Relay) disconnectNode(id string, worker *workerConnection) {
	r.mu.Lock()
	if r.workers[id] == worker {
		delete(r.workers, id)
	}
	r.mu.Unlock()
	_ = r.store.SetNodeConnected(id, false)
	requeued, _ := r.store.RequeueNode(id, "worker disconnected")
	_ = r.store.AddEvent(Event{Kind: "node.offline", Message: "Worker disconnected", NodeID: id, Data: map[string]interface{}{"affected_jobs": len(requeued)}})
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
	return nil
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
		host, _, _ := net.SplitHostPort(req.RemoteAddr)
		if host == "" {
			host = req.RemoteAddr
		}
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
	decoder := json.NewDecoder(io.LimitReader(body, limit+1))
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
