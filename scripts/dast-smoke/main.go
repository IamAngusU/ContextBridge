package main

import (
	"bytes"
	"context"
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
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

const (
	adminToken = "dast_admin_012345678901234567890123456789"
	localToken = "dast_local_012345678901234567890123456789"
	maxBody    = 1 << 20
)

type response struct {
	status int
	header http.Header
	body   []byte
}

type suite struct {
	baseURL string
	client  *http.Client
	secrets []string
	passed  int
	failed  int
}

func main() {
	binary := flag.String("binary", "", "path to a freshly built ContextBridge binary")
	flag.Parse()
	if strings.TrimSpace(*binary) == "" {
		fmt.Fprintln(os.Stderr, "dast-smoke: --binary is required")
		os.Exit(2)
	}
	if err := run(*binary); err != nil {
		fmt.Fprintln(os.Stderr, "dast-smoke:", err)
		os.Exit(1)
	}
}

func run(binary string) error {
	absoluteBinary, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(absoluteBinary); statErr != nil || info.IsDir() {
		return fmt.Errorf("binary is not a regular file: %s", absoluteBinary)
	}
	root, err := os.MkdirTemp("", "contextbridge-dast-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	localAddress, err := unusedLoopbackAddress()
	if err != nil {
		return err
	}
	relayAddress, err := unusedLoopbackAddress()
	if err != nil {
		return err
	}
	configPath := filepath.Join(root, "config.yml")
	if err := config.Default(configPath); err != nil {
		return fmt.Errorf("create disposable config: %w", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load disposable config: %w", err)
	}
	cfg.Server.Listen = localAddress
	cfg.Server.Token = localToken
	cfg.Storage.Directory = filepath.Join(root, "data")
	cfg.Storage.Inbox = filepath.Join(root, "inbox")
	cfg.Storage.Models = filepath.Join(root, "models")
	cfg.Cluster.Relay.Enabled = true
	cfg.Cluster.Relay.Listen = relayAddress
	cfg.Cluster.Relay.PublicURL = "http://" + relayAddress
	cfg.Cluster.Relay.Database = filepath.Join(root, "relay.db")
	cfg.Cluster.Relay.AdminToken = adminToken
	cfg.Cluster.Relay.MaxJobBytes = 4096
	cfg.Cluster.Relay.MaxQueue = 32
	cfg.Cluster.Worker.Enabled = false
	if err := config.Save(configPath, cfg); err != nil {
		return fmt.Errorf("save disposable config: %w", err)
	}

	processContext, stopProcess := context.WithCancel(context.Background())
	command := exec.CommandContext(processContext, absoluteBinary, "relay", "--config", configPath) // #nosec G204 -- operator-selected test binary, never user request data.
	command.Env = append(os.Environ(), "CONTEXTBRIDGE_UPDATES_EXTERNAL=1")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		stopProcess()
		return fmt.Errorf("start disposable relay: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		stopProcess()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}()

	baseURL := "http://" + relayAddress
	client := &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	if err := waitForRelay(client, baseURL, done); err != nil {
		return fmt.Errorf("%w\nstdout: %s\nstderr: %s", err, redact(stdout.String()), redact(stderr.String()))
	}

	test := &suite{baseURL: baseURL, client: client, secrets: []string{adminToken, localToken}}
	health := test.request(http.MethodGet, "/health", "", nil, nil)
	test.check("public health endpoint", health.status == http.StatusOK)
	test.check("defensive response headers", health.header.Get("X-Content-Type-Options") == "nosniff" && health.header.Get("Cache-Control") == "no-store" && strings.Contains(health.header.Get("Content-Security-Policy"), "frame-ancestors 'none'"))
	test.check("health omits credentials", test.noSecrets(health.body))

	missing := test.request(http.MethodGet, "/v1/cluster/overview", "", nil, nil)
	wrong := test.request(http.MethodGet, "/v1/cluster/overview", "not-a-valid-token", nil, nil)
	test.check("missing credential rejected", missing.status == http.StatusUnauthorized && test.noSecrets(missing.body))
	test.check("invalid credential rejected", wrong.status == http.StatusUnauthorized && test.noSecrets(wrong.body))
	adminOverview := test.request(http.MethodGet, "/v1/cluster/overview", adminToken, nil, nil)
	test.check("admin credential accepted", adminOverview.status == http.StatusOK && test.noSecrets(adminOverview.body))

	producerA, err := test.createToken("producer", "dast-producer-a")
	if err != nil {
		return err
	}
	producerB, err := test.createToken("producer", "dast-producer-b")
	if err != nil {
		return err
	}
	observer, err := test.createToken("observer", "dast-observer")
	if err != nil {
		return err
	}
	priorityCapped, err := test.createPriorityCappedProducerToken("dast-priority-capped", 20)
	if err != nil {
		return err
	}
	test.secrets = append(test.secrets, producerA, producerB, observer, priorityCapped)

	producerOverview := test.request(http.MethodGet, "/v1/cluster/overview", producerA, nil, nil)
	producerMetrics := test.request(http.MethodGet, "/metrics", producerA, nil, nil)
	observerMetrics := test.request(http.MethodGet, "/metrics", observer, nil, nil)
	producerTokenMint := test.request(http.MethodPost, "/v1/cluster/tokens", producerA, []byte(`{"role":"producer","subject":"forbidden"}`), nil)
	test.check("producer has bounded read access", producerOverview.status == http.StatusOK)
	test.check("producer cannot read operator metrics", producerMetrics.status == http.StatusUnauthorized && test.noSecrets(producerMetrics.body))
	test.check("observer can read metrics", observerMetrics.status == http.StatusOK && test.noSecrets(observerMetrics.body))
	test.check("producer cannot mint credentials", producerTokenMint.status == http.StatusUnauthorized && test.noSecrets(producerTokenMint.body))

	priorityDenied := test.request(http.MethodPost, "/v1/cluster/jobs", priorityCapped, []byte(`{"priority":21,"requirements":{"task":"generation","provider":"ollama"},"payload":{"prompt":"must not queue"}}`), nil)
	priorityAllowed := test.request(http.MethodPost, "/v1/cluster/jobs", priorityCapped, []byte(`{"priority":20,"requirements":{"task":"generation","provider":"ollama"},"payload":{"prompt":"exact ceiling"}}`), nil)
	var priorityJob struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(priorityAllowed.body, &priorityJob)
	test.check("producer priority self-promotion rejected", priorityDenied.status == http.StatusForbidden && bytes.Contains(priorityDenied.body, []byte(`"scope.priority_forbidden"`)) && test.noSecrets(priorityDenied.body))
	test.check("exact producer priority ceiling accepted", priorityAllowed.status == http.StatusAccepted && priorityJob.ID != "" && test.noSecrets(priorityAllowed.body))
	if priorityJob.ID != "" {
		_ = test.request(http.MethodDelete, "/v1/cluster/jobs/"+priorityJob.ID, priorityCapped, nil, nil)
	}

	malformed := test.request(http.MethodPost, "/v1/cluster/jobs", producerA, []byte(`{"requirements":`), nil)
	duplicate := test.request(http.MethodPost, "/v1/cluster/jobs", producerA, []byte(`{"requirements":{"task":"generation"},"requirements":{"task":"generation"},"payload":{}}`), nil)
	caseAlias := test.request(http.MethodPost, "/v1/cluster/jobs", producerA, []byte(`{"requirements":{"task":"generation"},"Requirements":{"task":"moderation"},"payload":{}}`), nil)
	unknown := test.request(http.MethodPost, "/v1/cluster/jobs", producerA, []byte(`{"requirements":{"task":"generation"},"payload":{},"unexpected":true}`), nil)
	invalidUTF8 := test.request(http.MethodPost, "/v1/cluster/jobs", producerA, []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, nil)
	integerOverflow := test.request(http.MethodPost, "/v1/cluster/jobs", producerA, []byte(`{"priority":9223372036854775808,"requirements":{"task":"generation"},"payload":{}}`), nil)
	floatOverflow := test.request(http.MethodPost, "/v1/cluster/jobs", producerA, []byte(`{"requirements":{"task":"generation","max_cost_usd":1e309},"payload":{}}`), nil)
	negativeUnsigned := test.request(http.MethodPost, "/v1/cluster/jobs", producerA, []byte(`{"requirements":{"task":"generation","min_free_vram_bytes":-1},"payload":{}}`), nil)
	oversized := test.request(http.MethodPost, "/v1/cluster/jobs", producerA, []byte(`{"requirements":{"task":"generation"},"payload":{"prompt":"`+strings.Repeat("x", 20<<10)+`"}}`), nil)
	test.check("malformed JSON rejected", malformed.status == http.StatusBadRequest && test.noSecrets(malformed.body))
	test.check("duplicate JSON key rejected", duplicate.status == http.StatusBadRequest && test.noSecrets(duplicate.body))
	test.check("case-aliased JSON key rejected", caseAlias.status == http.StatusBadRequest && test.noSecrets(caseAlias.body))
	test.check("unknown JSON field rejected", unknown.status == http.StatusBadRequest && test.noSecrets(unknown.body))
	test.check("invalid UTF-8 rejected", invalidUTF8.status == http.StatusBadRequest && test.noSecrets(invalidUTF8.body))
	test.check("integer overflow rejected", integerOverflow.status == http.StatusBadRequest && test.noSecrets(integerOverflow.body))
	test.check("float overflow rejected", floatOverflow.status == http.StatusBadRequest && test.noSecrets(floatOverflow.body))
	test.check("negative unsigned value rejected", negativeUnsigned.status == http.StatusBadRequest && test.noSecrets(negativeUnsigned.body))
	test.check("oversized body rejected", oversized.status >= 400 && oversized.status < 500 && len(oversized.body) < 4096 && test.noSecrets(oversized.body))

	wrongMethod := test.request(http.MethodPut, "/v1/cluster/jobs", producerA, []byte(`{}`), nil)
	hostileOrigin := test.request(http.MethodGet, "/v1/cluster/overview", producerA, nil, map[string]string{"Origin": "https://attacker.invalid"})
	test.check("unsupported method rejected", wrongMethod.status == http.StatusMethodNotAllowed)
	test.check("untrusted origin not reflected", hostileOrigin.header.Get("Access-Control-Allow-Origin") == "")

	validJob := []byte(`{"requirements":{"task":"generation","provider":"ollama"},"payload":{"prompt":"bounded DAST job"}}`)
	created := test.request(http.MethodPost, "/v1/cluster/jobs", producerA, validJob, map[string]string{"Idempotency-Key": "dast-owner-isolation"})
	var createdJob struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(created.body, &createdJob)
	test.check("producer can submit bounded work", created.status == http.StatusAccepted && createdJob.ID != "" && test.noSecrets(created.body))
	if createdJob.ID != "" {
		own := test.request(http.MethodGet, "/v1/cluster/jobs/"+createdJob.ID, producerA, nil, nil)
		foreign := test.request(http.MethodGet, "/v1/cluster/jobs/"+createdJob.ID, producerB, nil, nil)
		notFound := test.request(http.MethodGet, "/v1/cluster/jobs/dast-missing-job", producerB, nil, nil)
		test.check("owner can read own job", own.status == http.StatusOK && test.noSecrets(own.body))
		test.check("foreign job is indistinguishable from missing", foreign.status == http.StatusNotFound && notFound.status == http.StatusNotFound && bytes.Equal(foreign.body, notFound.body) && test.noSecrets(foreign.body))
		foreignCancel := test.request(http.MethodDelete, "/v1/cluster/jobs/"+createdJob.ID, producerB, nil, nil)
		ownerCancel := test.request(http.MethodDelete, "/v1/cluster/jobs/"+createdJob.ID, producerA, nil, nil)
		test.check("foreign producer cannot cancel job", foreignCancel.status == http.StatusNotFound && test.noSecrets(foreignCancel.body))
		test.check("owner can cancel queued job", ownerCancel.status == http.StatusOK && test.noSecrets(ownerCancel.body))
	}

	lastPair := response{}
	for index := 0; index < 13; index++ {
		lastPair = test.request(http.MethodPost, "/v1/pair/request", "", []byte(`{}`), nil)
	}
	test.check("pairing endpoint is rate limited", lastPair.status == http.StatusTooManyRequests && lastPair.header.Get("Retry-After") != "" && test.noSecrets(lastPair.body))
	test.check("oversized request headers are rejected", test.oversizedHeaderRejected())
	finalHealth := test.request(http.MethodGet, "/health", "", nil, nil)
	test.check("relay remains healthy after hostile input", finalHealth.status == http.StatusOK && test.noSecrets(finalHealth.body))

	fmt.Printf("\nBlack-box result: %d passed, %d failed\n", test.passed, test.failed)
	if test.failed != 0 {
		return fmt.Errorf("%d black-box security checks failed", test.failed)
	}
	return nil
}

func unusedLoopbackAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return address, nil
}

func waitForRelay(client *http.Client, baseURL string, done <-chan error) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			if err == nil {
				err = errors.New("relay exited without an error")
			}
			return fmt.Errorf("relay exited before readiness: %w", err)
		default:
		}
		request, _ := http.NewRequest(http.MethodGet, baseURL+"/readyz", nil)
		result, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(result.Body, 4096))
			_ = result.Body.Close()
			if result.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("relay did not become ready within 15 seconds")
}

func (s *suite) request(method, path, token string, body []byte, headers map[string]string) response {
	request, err := http.NewRequest(method, s.baseURL+path, bytes.NewReader(body))
	if err != nil {
		s.check(method+" "+path+" request construction", false)
		return response{}
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	result, err := s.client.Do(request)
	if err != nil {
		s.check(method+" "+path+" transport", false)
		return response{}
	}
	defer result.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(result.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		s.check(method+" "+path+" bounded response", false)
		return response{status: result.StatusCode, header: result.Header}
	}
	return response{status: result.StatusCode, header: result.Header.Clone(), body: raw}
}

func (s *suite) createToken(role, subject string) (string, error) {
	raw, _ := json.Marshal(map[string]interface{}{"role": role, "subject": subject, "lifetime_hours": 1})
	created := s.request(http.MethodPost, "/v1/cluster/tokens", adminToken, raw, nil)
	if created.status != http.StatusCreated {
		return "", fmt.Errorf("create %s token returned HTTP %d", role, created.status)
	}
	var output struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.body, &output); err != nil || output.Token == "" {
		return "", fmt.Errorf("create %s token returned an invalid response", role)
	}
	return output.Token, nil
}

func (s *suite) createPriorityCappedProducerToken(subject string, maxPriority int) (string, error) {
	raw, _ := json.Marshal(map[string]interface{}{
		"role": "producer", "subject": subject, "lifetime_hours": 1,
		"producer_limits": map[string]interface{}{"max_priority": maxPriority},
	})
	created := s.request(http.MethodPost, "/v1/cluster/tokens", adminToken, raw, nil)
	if created.status != http.StatusCreated {
		return "", fmt.Errorf("create priority-capped producer token returned HTTP %d", created.status)
	}
	var output struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.body, &output); err != nil || output.Token == "" {
		return "", errors.New("create priority-capped producer token returned an invalid response")
	}
	return output.Token, nil
}

func (s *suite) noSecrets(raw []byte) bool {
	text := string(raw)
	for _, secret := range s.secrets {
		if secret != "" && strings.Contains(text, secret) {
			return false
		}
	}
	return true
}

func (s *suite) oversizedHeaderRejected() bool {
	request, _ := http.NewRequest(http.MethodGet, s.baseURL+"/health", nil)
	request.Header.Set("X-ContextBridge-DAST", strings.Repeat("x", 128<<10))
	result, err := s.client.Do(request)
	if err != nil {
		return s.request(http.MethodGet, "/health", "", nil, nil).status == http.StatusOK
	}
	defer result.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(result.Body, 4096))
	return result.StatusCode == http.StatusRequestHeaderFieldsTooLarge || result.StatusCode == http.StatusBadRequest
}

func (s *suite) check(name string, ok bool) {
	if ok {
		s.passed++
		fmt.Println("  PASS", name)
		return
	}
	s.failed++
	fmt.Println("  FAIL", name)
}

func redact(value string) string {
	value = strings.ReplaceAll(value, adminToken, "[REDACTED]")
	return strings.ReplaceAll(value, localToken, "[REDACTED]")
}
