package main

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/strictjson"
)

const (
	adapterConformanceSchema       = "contextbridge.adapter-conformance.v1"
	adapterConformanceProtocol     = "contextbridge.adapter.v2"
	adapterConformanceMaximumInput = 1 << 20
	adapterConformanceMaximumBody  = 20 << 20
)

type repeatedAdapterArgument []string

func (values *repeatedAdapterArgument) String() string { return strings.Join(*values, " ") }
func (values *repeatedAdapterArgument) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type adapterConformanceOptions struct {
	executable    string
	arguments     []string
	profile       string
	profileConfig map[string]interface{}
	job           map[string]interface{}
	timeout       time.Duration
	outputJSON    bool
	workingDir    string
}

type adapterConformanceCheck struct {
	ID       string `json:"id"`
	Passed   bool   `json:"passed"`
	Detail   string `json:"detail"`
	Requests int    `json:"requests"`
}

type adapterConformanceReport struct {
	Schema      string                    `json:"schema"`
	Protocol    string                    `json:"protocol"`
	Adapter     string                    `json:"adapter"`
	Profile     string                    `json:"profile"`
	StartedAt   time.Time                 `json:"started_at"`
	CompletedAt time.Time                 `json:"completed_at"`
	Passed      bool                      `json:"passed"`
	Checks      []adapterConformanceCheck `json:"checks"`
}

func adapterConformanceCommand(args []string) error {
	flags := flag.NewFlagSet("adapter conformance", flag.ContinueOnError)
	var adapterArguments repeatedAdapterArgument
	executable := flags.String("adapter", "", "exact adapter executable; launched directly without a shell")
	flags.Var(&adapterArguments, "arg", "one adapter argument; repeat in process argument order")
	profile := flags.String("profile", "conformance", "safe adapter profile ID")
	profileFile := flags.String("profile-file", "", "optional bounded JSON adapter profile object")
	jobFile := flags.String("job-file", "", "optional bounded JSON job object understood by the adapter")
	workingDir := flags.String("working-directory", "", "optional adapter working directory")
	timeoutSeconds := flags.Int("timeout-seconds", 20, "maximum seconds per isolated scenario")
	asJSON := flags.Bool("json", false, "print the machine-readable report")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*executable) == "" {
		return errors.New("usage: contextbridge adapter conformance --adapter PATH [--arg VALUE] [--profile ID] [--profile-file FILE] [--job-file FILE] [--json]")
	}
	if !safeAdapterConformanceID(*profile) {
		return errors.New("--profile must be 1-80 ASCII letters, digits, dots, underscores, or hyphens")
	}
	if *timeoutSeconds < 5 || *timeoutSeconds > 120 {
		return errors.New("--timeout-seconds must be between 5 and 120")
	}
	resolvedExecutable, err := exec.LookPath(strings.TrimSpace(*executable))
	if err != nil {
		return fmt.Errorf("resolve adapter executable: %w", err)
	}
	resolvedExecutable, err = filepath.Abs(filepath.Clean(resolvedExecutable))
	if err != nil {
		return fmt.Errorf("resolve adapter executable path: %w", err)
	}
	resolvedWorkingDir := strings.TrimSpace(*workingDir)
	if resolvedWorkingDir != "" {
		resolvedWorkingDir, err = filepath.Abs(filepath.Clean(resolvedWorkingDir))
		if err != nil {
			return fmt.Errorf("resolve adapter working directory: %w", err)
		}
		info, statErr := os.Stat(resolvedWorkingDir)
		if statErr != nil || !info.IsDir() {
			return errors.New("--working-directory must name an existing directory")
		}
	}
	profileConfig, err := readAdapterConformanceObject(*profileFile, adapterConformanceMaximumInput)
	if err != nil {
		return fmt.Errorf("read profile file: %w", err)
	}
	if profileConfig == nil {
		profileConfig = map[string]interface{}{
			"label": "Adapter conformance probe", "driver": "conformance", "options": map[string]interface{}{},
		}
	}
	job, err := readAdapterConformanceObject(*jobFile, adapterConformanceMaximumInput)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}
	if job == nil {
		job = map[string]interface{}{
			"prompt": "Complete the side-effect-free ContextBridge adapter conformance probe.",
			"output": map[string]interface{}{"mode": "text", "max_bytes": 65536},
		}
	}
	options := adapterConformanceOptions{
		executable: resolvedExecutable, arguments: append([]string(nil), adapterArguments...), profile: *profile,
		profileConfig: profileConfig, job: job, timeout: time.Duration(*timeoutSeconds) * time.Second,
		outputJSON: *asJSON, workingDir: resolvedWorkingDir,
	}
	report, err := runAdapterConformance(options)
	if err != nil {
		return err
	}
	if options.outputJSON {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Printf("Adapter conformance %s · %s\n", report.Protocol, passFail(report.Passed))
		for _, check := range report.Checks {
			fmt.Printf("  %-5s %-38s %s\n", passFail(check.Passed), check.ID, check.Detail)
		}
		fmt.Println("This is free self-run point-in-time evidence, not certification or endorsement.")
	}
	if !report.Passed {
		return errors.New("adapter conformance failed")
	}
	return nil
}

func passFail(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}

func readAdapterConformanceObject(path string, limit int64) (map[string]interface{}, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := readRegularFileBounded(path, limit)
	if err != nil {
		return nil, err
	}
	if err := strictjson.Validate(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("JSON document must be an object")
	}
	return value, nil
}

func safeAdapterConformanceID(value string) bool {
	if len(value) < 1 || len(value) > 80 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func runAdapterConformance(options adapterConformanceOptions) (adapterConformanceReport, error) {
	report := adapterConformanceReport{
		Schema: adapterConformanceSchema, Protocol: adapterConformanceProtocol,
		Adapter: filepath.Base(options.executable), Profile: options.profile, StartedAt: time.Now().UTC(),
	}
	checks := make([]adapterConformanceCheck, 0, 7)

	unauthorized, err := runAdapterConformanceScenario(options, adapterConformanceUnauthorized)
	if err != nil {
		return report, err
	}
	checks = append(checks, unauthorized.check("unauthorized_is_terminal", unauthorized.totalRequests == 1 && unauthorized.statusRequests == 1,
		"a scoped 401 response was not retried or downgraded"))

	serverFailure, err := runAdapterConformanceScenario(options, adapterConformanceHTTPFailure)
	if err != nil {
		return report, err
	}
	checks = append(checks, serverFailure.check("http_failure_not_retried", serverFailure.totalRequests == 1 && serverFailure.statusRequests == 1,
		"an explicit HTTP failure was observed once without automatic replay"))

	expiredLease, err := runAdapterConformanceScenario(options, adapterConformanceLeaseRejected)
	if err != nil {
		return report, err
	}
	checks = append(checks, expiredLease.check("expired_lease_is_terminal",
		expiredLease.claimRequests == 1 && expiredLease.progressRequests == 0 && expiredLease.completeRequests == 0 &&
			expiredLease.nextAfterLeaseRejection == 0 && expiredLease.violation == "",
		"an expired lease was not progressed, completed, replayed, or replaced with new work"))

	lifecycle, err := runAdapterConformanceScenario(options, adapterConformanceLifecycle)
	if err != nil {
		return report, err
	}
	checks = append(checks,
		lifecycle.check("scoped_lifecycle", lifecycle.completed && lifecycle.violation == "",
			"status, profile, heartbeat, lease and completion stayed inside the scoped v2 surface"),
		lifecycle.check("endpoint_capability_renewal", lifecycle.heartbeats >= 2 && lifecycle.echoedEndpointCapability,
			"a later heartbeat renewed the original endpoint capability"),
		lifecycle.check("claim_before_completion", lifecycle.claimed && lifecycle.claimSequence < lifecycle.completeSequence,
			"the adapter crossed the explicit claim boundary before completion"),
	)

	ambiguous, err := runAdapterConformanceScenario(options, adapterConformanceAmbiguousComplete)
	if err != nil {
		return report, err
	}
	checks = append(checks, ambiguous.check("ambiguous_completion_not_retried",
		ambiguous.completeRequests == 1 && ambiguous.nextAfterComplete == 0 && ambiguous.violation == "",
		"the connection was dropped after completion bytes arrived and the adapter did not replay or poll new work"))

	report.Checks = checks
	report.Passed = true
	for _, check := range checks {
		if !check.Passed {
			report.Passed = false
		}
	}
	report.CompletedAt = time.Now().UTC()
	return report, nil
}

type adapterConformanceScenario string

const (
	adapterConformanceUnauthorized      adapterConformanceScenario = "unauthorized"
	adapterConformanceHTTPFailure       adapterConformanceScenario = "http_failure"
	adapterConformanceLeaseRejected     adapterConformanceScenario = "lease_rejected"
	adapterConformanceLifecycle         adapterConformanceScenario = "lifecycle"
	adapterConformanceAmbiguousComplete adapterConformanceScenario = "ambiguous_complete"
)

type adapterConformanceObservation struct {
	totalRequests            int
	statusRequests           int
	heartbeats               int
	claimRequests            int
	progressRequests         int
	completeRequests         int
	nextAfterComplete        int
	nextAfterLeaseRejection  int
	claimSequence            int
	completeSequence         int
	claimed                  bool
	completed                bool
	echoedEndpointCapability bool
	violation                string
}

func (observation adapterConformanceObservation) check(id string, passed bool, success string) adapterConformanceCheck {
	detail := success
	if !passed {
		detail = observation.violation
		if detail == "" {
			detail = "required protocol evidence was not observed"
		}
	}
	return adapterConformanceCheck{ID: id, Passed: passed, Detail: detail, Requests: observation.totalRequests}
}

type adapterConformanceCore struct {
	mu                       sync.Mutex
	scenario                 adapterConformanceScenario
	token                    string
	processToken             string
	profile                  string
	profileConfig            map[string]interface{}
	job                      map[string]interface{}
	outputSpec               bridge.OutputSpec
	jobID                    string
	timeout                  time.Duration
	endpointCapability       string
	leaseCapability          string
	requestSequence          int
	totalRequests            int
	statusRequests           int
	heartbeats               int
	claimRequests            int
	progressRequests         int
	completeRequests         int
	nextAfterComplete        int
	nextAfterLeaseRejection  int
	claimSequence            int
	completeSequence         int
	progressSequence         uint64
	jobDelivered             bool
	claimed                  bool
	completed                bool
	leaseRejected            bool
	echoedEndpointCapability bool
	violation                string
	firstRequest             chan struct{}
	completionAttempt        chan struct{}
	leaseRejectionAttempt    chan struct{}
	firstRequestOnce         sync.Once
	completionAttemptOnce    sync.Once
	leaseRejectionOnce       sync.Once
}

func newAdapterConformanceCore(options adapterConformanceOptions, scenario adapterConformanceScenario) (*adapterConformanceCore, error) {
	token, err := adapterConformanceSecret()
	if err != nil {
		return nil, err
	}
	processToken := token
	if scenario == adapterConformanceUnauthorized {
		processToken, err = adapterConformanceSecret()
		if err != nil {
			return nil, err
		}
	}
	endpointCapability, err := adapterConformanceSecret()
	if err != nil {
		return nil, err
	}
	leaseCapability, err := adapterConformanceSecret()
	if err != nil {
		return nil, err
	}
	jobIDBytes := make([]byte, 12)
	if _, err := cryptorand.Read(jobIDBytes); err != nil {
		return nil, err
	}
	job := make(map[string]interface{}, len(options.job)+1)
	for key, value := range options.job {
		job[key] = value
	}
	jobID := "conformance-" + base64.RawURLEncoding.EncodeToString(jobIDBytes)
	job["id"] = jobID
	var outputSpec bridge.OutputSpec
	if configured, exists := job["output"]; exists {
		rawOutputSpec, marshalErr := json.Marshal(configured)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode conformance output specification: %w", marshalErr)
		}
		if decodeErr := strictjson.Decode(rawOutputSpec, &outputSpec); decodeErr != nil {
			return nil, fmt.Errorf("decode conformance output specification: %w", decodeErr)
		}
	}
	return &adapterConformanceCore{
		scenario: scenario, token: token, processToken: processToken, profile: options.profile,
		profileConfig: options.profileConfig, job: job, outputSpec: outputSpec, jobID: jobID,
		timeout:            options.timeout,
		endpointCapability: endpointCapability, leaseCapability: leaseCapability,
		firstRequest: make(chan struct{}), completionAttempt: make(chan struct{}), leaseRejectionAttempt: make(chan struct{}),
	}, nil
}

func adapterConformanceSecret() (string, error) {
	raw := make([]byte, 48)
	if _, err := cryptorand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (core *adapterConformanceCore) observation() adapterConformanceObservation {
	core.mu.Lock()
	defer core.mu.Unlock()
	return adapterConformanceObservation{
		totalRequests: core.totalRequests, statusRequests: core.statusRequests, heartbeats: core.heartbeats,
		completeRequests: core.completeRequests, nextAfterComplete: core.nextAfterComplete,
		claimRequests: core.claimRequests, progressRequests: core.progressRequests,
		nextAfterLeaseRejection: core.nextAfterLeaseRejection,
		claimSequence:           core.claimSequence, completeSequence: core.completeSequence,
		claimed: core.claimed, completed: core.completed, echoedEndpointCapability: core.echoedEndpointCapability,
		violation: core.violation,
	}
}

func (core *adapterConformanceCore) recordViolation(message string) {
	if core.violation == "" {
		core.violation = message
	}
}

func (core *adapterConformanceCore) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	core.mu.Lock()
	core.requestSequence++
	sequence := core.requestSequence
	core.totalRequests++
	core.firstRequestOnce.Do(func() { close(core.firstRequest) })
	if core.completed && strings.Contains(request.URL.Path, "/jobs/next") {
		core.nextAfterComplete++
	}
	if core.leaseRejected && strings.Contains(request.URL.Path, "/jobs/next") {
		core.nextAfterLeaseRejection++
	}
	core.mu.Unlock()

	if request.Header.Get("Authorization") != "Bearer "+core.token {
		core.mu.Lock()
		if request.Method == http.MethodGet && request.URL.Path == "/v2/adapter/status" {
			core.statusRequests++
		} else {
			core.recordViolation("adapter did not probe the scoped v2 status endpoint before using the credential")
		}
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusUnauthorized, map[string]string{"error": "valid scoped adapter credential required"})
		return
	}
	if core.scenario == adapterConformanceHTTPFailure {
		core.mu.Lock()
		if request.Method == http.MethodGet && request.URL.Path == "/v2/adapter/status" {
			core.statusRequests++
		} else {
			core.recordViolation("adapter did not begin with the scoped v2 status probe")
		}
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "injected conformance failure"})
		return
	}

	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/v2/adapter/status":
		core.mu.Lock()
		core.statusRequests++
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true, "protocol": adapterConformanceProtocol, "principal_id": "conformance", "allowed_profiles": []string{core.profile},
		})
	case request.Method == http.MethodGet && request.URL.Path == "/v2/adapter/profiles":
		adapterConformanceJSON(w, http.StatusOK, map[string]interface{}{core.profile: core.profileConfig})
	case request.Method == http.MethodPost && request.URL.Path == "/v2/adapter/heartbeat":
		core.handleHeartbeat(w, request)
	case request.Method == http.MethodGet && request.URL.Path == "/v2/adapter/jobs/next":
		core.handleNext(w, request)
	case strings.HasPrefix(request.URL.Path, "/v2/adapter/jobs/"+core.jobID+"/"):
		core.handleLeaseAction(w, request, sequence)
	default:
		core.mu.Lock()
		core.recordViolation("adapter requested an endpoint outside the documented scoped v2 surface")
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (core *adapterConformanceCore) handleHeartbeat(w http.ResponseWriter, request *http.Request) {
	var payload bridge.AdapterClientStatus
	if err := adapterConformanceDecode(request, &payload, 128<<10); err != nil || len(payload.Endpoints) != 1 ||
		!payload.Connected || payload.ActiveEndpoints != 1 || payload.BusyEndpoints < 0 ||
		payload.BusyEndpoints > payload.ActiveEndpoints || payload.Endpoints[0].ID != 1 ||
		payload.Endpoints[0].Profile != core.profile {
		core.mu.Lock()
		core.recordViolation("heartbeat was malformed or escaped the configured profile/endpoint")
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid heartbeat"})
		return
	}
	core.mu.Lock()
	core.heartbeats++
	if core.heartbeats > 1 && payload.Endpoints[0].EndpointCapability == core.endpointCapability {
		core.echoedEndpointCapability = true
	}
	core.mu.Unlock()
	adapterConformanceJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "protocol": adapterConformanceProtocol,
		"endpoints": []map[string]interface{}{{"profile": core.profile, "endpoint_id": 1, "endpoint_capability": core.endpointCapability}},
	})
}

func (core *adapterConformanceCore) handleNext(w http.ResponseWriter, request *http.Request) {
	if request.URL.Query().Get("profile") != core.profile || request.URL.Query().Get("endpoint_id") != "1" ||
		request.Header.Get("X-ContextBridge-Endpoint-Capability") != core.endpointCapability {
		core.mu.Lock()
		core.recordViolation("poll omitted or changed its profile-bound endpoint capability")
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusForbidden, map[string]string{"error": "endpoint capability required"})
		return
	}
	core.mu.Lock()
	if core.jobDelivered {
		core.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	core.jobDelivered = true
	job := core.job
	core.mu.Unlock()
	adapterConformanceJSON(w, http.StatusOK, map[string]interface{}{
		"job": job, "profile": core.profileConfig, "deadline": time.Now().Add(core.timeout).UTC(),
		"lease_generation": 1, "lease_capability": core.leaseCapability,
		"lease_expires_at": time.Now().Add(core.timeout).UTC(),
	})
}

func (core *adapterConformanceCore) handleLeaseAction(w http.ResponseWriter, request *http.Request, sequence int) {
	if request.Header.Get("X-ContextBridge-Lease-Generation") != "1" ||
		request.Header.Get("X-ContextBridge-Lease-Capability") != core.leaseCapability {
		core.mu.Lock()
		core.recordViolation("lease action omitted or changed its generation/capability fence")
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusConflict, map[string]string{"error": "valid lease credentials required"})
		return
	}
	action := strings.TrimPrefix(request.URL.Path, "/v2/adapter/jobs/"+core.jobID+"/")
	switch action {
	case "claim":
		var payload struct {
			Action string `json:"action"`
		}
		if request.Method != http.MethodPost || adapterConformanceDecode(request, &payload, 16<<10) != nil ||
			(payload.Action != "prepare" && payload.Action != "mutate" && payload.Action != "commit") {
			core.mu.Lock()
			core.recordViolation("adapter sent a malformed claim request")
			core.mu.Unlock()
			adapterConformanceJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid claim"})
			return
		}
		core.mu.Lock()
		core.claimRequests++
		if core.scenario == adapterConformanceLeaseRejected {
			core.leaseRejected = true
			core.leaseRejectionOnce.Do(func() { close(core.leaseRejectionAttempt) })
			core.mu.Unlock()
			adapterConformanceJSON(w, http.StatusConflict, map[string]string{"error": "job lease was lost"})
			return
		}
		core.claimed = true
		core.claimSequence = sequence
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "observation_only": true})
	case "progress":
		var payload struct {
			Sequence uint64 `json:"sequence"`
			Text     string `json:"text"`
			Phase    string `json:"phase,omitempty"`
			Percent  int    `json:"percent,omitempty"`
			Busy     bool   `json:"busy"`
		}
		if request.Method != http.MethodPost || adapterConformanceDecode(request, &payload, 64<<10) != nil || payload.Sequence == 0 ||
			len(payload.Text) > 2000 || payload.Percent < 0 || payload.Percent > 100 {
			core.mu.Lock()
			core.recordViolation("adapter sent malformed or unbounded progress")
			core.mu.Unlock()
			adapterConformanceJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid progress"})
			return
		}
		core.mu.Lock()
		core.progressRequests++
		if payload.Sequence <= core.progressSequence {
			core.recordViolation("progress sequence was not strictly monotonic")
		}
		core.progressSequence = payload.Sequence
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "lease":
		if request.Method != http.MethodGet && request.Method != http.MethodPost {
			core.mu.Lock()
			core.recordViolation("adapter used an unsupported lease method")
			core.mu.Unlock()
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		adapterConformanceJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "lease_expires_at": time.Now().Add(core.timeout).UTC()})
	case "release":
		if request.Method != http.MethodPost {
			core.mu.Lock()
			core.recordViolation("adapter used an unsupported release method")
			core.mu.Unlock()
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		core.mu.Lock()
		claimed := core.claimed
		core.mu.Unlock()
		if claimed {
			adapterConformanceJSON(w, http.StatusConflict, map[string]string{"error": "claimed work is observation-only"})
			return
		}
		adapterConformanceJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "complete":
		core.handleComplete(w, request, sequence)
	default:
		core.mu.Lock()
		core.recordViolation("adapter requested an unknown lease action")
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (core *adapterConformanceCore) handleComplete(w http.ResponseWriter, request *http.Request, sequence int) {
	var payload map[string]interface{}
	if request.Method != http.MethodPost || adapterConformanceDecode(request, &payload, adapterConformanceMaximumBody) != nil {
		core.mu.Lock()
		core.recordViolation("adapter sent a malformed or oversized completion")
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid completion"})
		return
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		core.mu.Lock()
		core.recordViolation("adapter completion could not be normalized")
		core.mu.Unlock()
		adapterConformanceJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid completion"})
		return
	}
	normalized := bridge.NormalizeOutput(rawPayload, core.outputSpec, "adapter", "conformance", 0)
	mode, modeOK := payload["mode"].(string)
	core.mu.Lock()
	core.completeRequests++
	core.completeSequence = sequence
	if !core.claimed {
		core.recordViolation("adapter completed work before crossing the claim boundary")
	}
	if !modeOK || strings.TrimSpace(mode) == "" {
		core.recordViolation("completion omitted its normalized output mode")
	}
	if rawError, exists := payload["error"]; exists && strings.TrimSpace(fmt.Sprint(rawError)) != "" {
		core.recordViolation("side-effect-free conformance job returned an adapter error")
	} else if normalized.Error != "" {
		core.recordViolation("completion failed the core's production output normalization")
	}
	core.completed = true
	core.completionAttemptOnce.Do(func() { close(core.completionAttempt) })
	core.mu.Unlock()
	if core.scenario == adapterConformanceAmbiguousComplete {
		if hijacker, ok := w.(http.Hijacker); ok {
			connection, _, err := hijacker.Hijack()
			if err == nil {
				_ = connection.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	adapterConformanceJSON(w, http.StatusOK, normalized)
}

func adapterConformanceDecode(request *http.Request, target interface{}, limit int64) error {
	raw, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return errors.New("request body exceeds conformance limit")
	}
	return strictjson.Decode(raw, target)
}

func adapterConformanceJSON(w http.ResponseWriter, status int, payload interface{}) {
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func runAdapterConformanceScenario(options adapterConformanceOptions, scenario adapterConformanceScenario) (adapterConformanceObservation, error) {
	core, err := newAdapterConformanceCore(options, scenario)
	if err != nil {
		return adapterConformanceObservation{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return adapterConformanceObservation{}, err
	}
	server := &http.Server{Handler: core, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
		_ = listener.Close()
		<-serverDone
	}()

	temporaryDirectory, err := os.MkdirTemp("", "contextbridge-adapter-conformance-")
	if err != nil {
		return adapterConformanceObservation{}, err
	}
	defer os.RemoveAll(temporaryDirectory)
	tokenFile := filepath.Join(temporaryDirectory, "adapter.token")
	if err := os.WriteFile(tokenFile, []byte(core.processToken+"\n"), 0o600); err != nil {
		return adapterConformanceObservation{}, err
	}

	processContext, cancelProcess := context.WithCancel(context.Background())
	defer cancelProcess()
	command := exec.CommandContext(processContext, options.executable, options.arguments...) // #nosec G204 -- explicit operator-selected executable and argv, never a shell.
	command.Dir = options.workingDir
	baseURL := "http://" + listener.Addr().String()
	command.Env = adapterConformanceEnvironment(os.Environ(), map[string]string{
		"CONTEXTBRIDGE_URL":                 baseURL,
		"CONTEXTBRIDGE_ADAPTER_TOKEN":       core.processToken,
		"CONTEXTBRIDGE_ADAPTER_TOKEN_FILE":  tokenFile,
		"CONTEXTBRIDGE_ADAPTER_PROFILE":     options.profile,
		"CONTEXTBRIDGE_ADAPTER_ENDPOINT_ID": "1",
		"CONTEXTBRIDGE_CONFORMANCE":         "1",
	})
	var stdout, stderr boundedAdapterConformanceBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		return adapterConformanceObservation{}, fmt.Errorf("start adapter: %w", err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- command.Wait() }()

	deadline := time.NewTimer(options.timeout)
	defer deadline.Stop()
	var signal <-chan struct{}
	switch scenario {
	case adapterConformanceUnauthorized, adapterConformanceHTTPFailure:
		signal = core.firstRequest
	case adapterConformanceLeaseRejected:
		signal = core.leaseRejectionAttempt
	case adapterConformanceLifecycle, adapterConformanceAmbiguousComplete:
		signal = core.completionAttempt
	}
	select {
	case <-signal:
	case processErr := <-processDone:
		observation := core.observation()
		if observation.violation == "" {
			observation.violation = "adapter exited before the scenario produced its required evidence: " + boundedAdapterConformanceExit(processErr)
		}
		return observation, nil
	case <-deadline.C:
		cancelProcess()
		<-processDone
		observation := core.observation()
		if observation.violation == "" {
			observation.violation = "adapter scenario timed out before required protocol evidence"
		}
		return observation, nil
	}

	processExited := false
	grace := time.NewTimer(600 * time.Millisecond)
	select {
	case <-grace.C:
	case <-processDone:
		processExited = true
		if !grace.Stop() {
			select {
			case <-grace.C:
			default:
			}
		}
	}
	if !processExited {
		cancelProcess()
		select {
		case <-processDone:
		case <-time.After(2 * time.Second):
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			<-processDone
		}
	}
	return core.observation(), nil
}

func adapterConformanceEnvironment(environment []string, overrides map[string]string) []string {
	result := make([]string, 0, len(environment)+len(overrides))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		replaced := false
		for override := range overrides {
			if strings.EqualFold(name, override) {
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, entry)
		}
	}
	for name, value := range overrides {
		result = append(result, name+"="+value)
	}
	return result
}

type boundedAdapterConformanceBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	truncated bool
}

func (buffer *boundedAdapterConformanceBuffer) Write(raw []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	const maximum = 64 << 10
	remaining := maximum - buffer.buffer.Len()
	if remaining > 0 {
		_, _ = buffer.buffer.Write(raw[:min(len(raw), remaining)])
	}
	if len(raw) > remaining {
		buffer.truncated = true
	}
	return len(raw), nil
}

func (buffer *boundedAdapterConformanceBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	value := strings.TrimSpace(buffer.buffer.String())
	if buffer.truncated {
		value += " [truncated]"
	}
	return value
}

func boundedAdapterConformanceExit(processErr error) string {
	result := "process exited"
	if processErr != nil {
		result = processErr.Error()
	}
	return result
}
