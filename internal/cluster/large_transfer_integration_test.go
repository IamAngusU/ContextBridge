package cluster

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestMaximumVisualInputAndArtifactResultRoundTrip(t *testing.T) {
	inputImage := bytes.Repeat([]byte{0x5a}, 8<<20)
	encodedImage := base64.StdEncoding.EncodeToString(inputImage)
	firstArtifact := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 6<<20))
	secondArtifact := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, 6<<20))

	localErrors := make(chan error, 4)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/status":
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"queued": 0,
				"routes": map[string]interface{}{
					"default": map[string]interface{}{"task": "generation", "model": "vision-test", "provider": "ollama"},
				},
				"runtime": map[string]interface{}{"engines": map[string]interface{}{
					"ollama": map[string]interface{}{
						"state": "online",
						"models": []interface{}{map[string]interface{}{
							"name": "vision-test", "loaded": true, "capabilities": []string{"text", "vision"},
						}},
					},
				}},
			})
		case "/v1/jobs":
			if req.URL.Query().Get("compact") != "1" {
				localErrors <- fmt.Errorf("worker did not request a compact local response")
				http.Error(w, "compact response required", http.StatusBadRequest)
				return
			}
			if got := req.Header.Get("X-ContextBridge-Expected-Task"); got != "generation" {
				localErrors <- &transferTaskError{got: got}
				http.Error(w, "missing worker task binding", http.StatusUnprocessableEntity)
				return
			}
			var submitted map[string]json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&submitted); err != nil {
				localErrors <- err
				http.Error(w, "invalid job", http.StatusBadRequest)
				return
			}
			var image string
			if err := json.Unmarshal(submitted["image_base64"], &image); err != nil {
				localErrors <- err
			}
			decoded, err := base64.StdEncoding.DecodeString(image)
			if err != nil {
				localErrors <- err
			} else if len(decoded) != 8<<20 {
				localErrors <- &transferSizeError{kind: "visual input", got: len(decoded), want: 8 << 20}
			}
			// The local bridge deliberately does not echo image_base64 in its
			// response. The worker then removes the remaining known inputs.
			delete(submitted, "image_base64")
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"status": "completed",
				"job":    submitted,
				"output": map[string]interface{}{
					"mode": "text", "text": "large-transfer-ok", "provider": "ollama", "model": "vision-test",
					"artifacts": []interface{}{
						map[string]interface{}{"name": "first.bin", "media_type": "application/octet-stream", "size": 6 << 20, "data_base64": firstArtifact},
						map[string]interface{}{"name": "second.bin", "media_type": "application/octet-stream", "size": 6 << 20, "data_base64": secondArtifact},
					},
				},
			})
		default:
			http.NotFound(w, req)
		}
	}))
	defer local.Close()

	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:    admin,
		AllowedTasks:  []string{"generation"},
		MaxJobBytes:   12 << 20,
		DispatchEvery: 10 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	dispatchCtx, stopDispatch := context.WithCancel(context.Background())
	defer stopDispatch()
	go relay.dispatchLoop(dispatchCtx)
	relayHTTP := httptest.NewServer(relay.Handler())
	defer relayHTTP.Close()

	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "large-transfer-worker"
	nodeToken, _, err := relay.store.CreateToken("node", nodeID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	identityFile := filepath.Join(t.TempDir(), "identity.json")
	if err := saveIdentity(identityFile, WorkerIdentity{
		NodeID: nodeID, NodeToken: nodeToken, PrivateKey: privateKey, PublicKey: publicKey, RelayURL: relayHTTP.URL,
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := LoadWorker(WorkerConfig{
		RelayURL: relayHTTP.URL, IdentityFile: identityFile, Name: nodeID,
		MaxConcurrent: 1, LocalURL: local.URL, LocalToken: "local-test-token",
		HeartbeatEvery: 20 * time.Millisecond, RequestTimeout: 45 * time.Second,
		AllowedTasks: []string{"generation"}, AllowedProviders: []string{"ollama"},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	go func() { _ = worker.Run(workerCtx, nil) }()
	waitFor(t, 4*time.Second, func() bool {
		node, loadErr := relay.store.GetNode(nodeID)
		if loadErr != nil || !node.Connected {
			return false
		}
		for _, model := range node.Capabilities.Models {
			if model.Name == "vision-test" && model.Vision {
				return true
			}
		}
		return false
	}, "large-transfer worker did not advertise its vision model")

	producer, _, err := relay.store.CreateToken("producer", "large-transfer-producer", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]interface{}{
		"route": "default", "prompt": "never echo this prompt", "image_base64": encodedImage, "image_media_type": "image/png",
		"output": map[string]interface{}{"mode": "text", "artifacts": true, "max_artifact_bytes": 12 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 12<<20 {
		t.Fatalf("maximum visual payload no longer fits the relay request boundary: %d bytes", len(payload))
	}
	requirements := Requirements{Task: "generation", Provider: "ollama", Model: "vision-test", Vision: true}

	for _, encrypted := range []bool{false, true} {
		name := "plaintext"
		if encrypted {
			name = "e2ee"
		}
		t.Run(name, func(t *testing.T) {
			waitFor(t, 15*time.Second, func() bool {
				node, loadErr := relay.store.GetNode(nodeID)
				return loadErr == nil && node.Connected && node.Capabilities.Running == 0
			}, "large-transfer worker did not return to idle")
			input := SubmitRequest{Requirements: requirements, Payload: payload, MaxAttempts: 1}
			var shared string
			encryptionContext := EncryptionContext{}
			if encrypted {
				var assignment AssignmentResponse
				assignmentRequest := AssignmentRequest{Requirements: requirements}
				postTest(t, relayHTTP.URL+"/v1/cluster/assign", producer, assignmentRequest, &assignment)
				var contextErr error
				encryptionContext, contextErr = ValidateAssignmentResponse(assignmentRequest, assignment, time.Now().UTC())
				if contextErr != nil {
					t.Fatal(contextErr)
				}
				sealed, derived, sealErr := SealFor(assignment.Assignment.PublicKey, payload, JobAAD(encryptionContext))
				if sealErr != nil {
					t.Fatal(sealErr)
				}
				shared = derived
				input.Payload = nil
				input.Sealed = sealed
				input.AssignmentID = assignment.Assignment.ID
				input.AssignmentSecret = assignment.Secret
			}

			var submitted Job
			postTest(t, relayHTTP.URL+"/v1/cluster/jobs?compact=1", producer, input, &submitted)
			if len(submitted.Payload) != 0 || submitted.SealedPayload != nil {
				t.Fatal("compact submit response echoed the maximum-size input")
			}

			var completed Job
			// Race instrumentation and slower hosted runners can spend minutes
			// encoding and copying the intentional 8 MiB in / 12 MiB out boundary
			// fixture. This is a test-observation deadline, not a product execution
			// timeout.
			deadline := time.Now().Add(180 * time.Second)
			for time.Now().Before(deadline) {
				req, _ := http.NewRequest(http.MethodGet, relayHTTP.URL+"/v1/cluster/jobs/"+submitted.ID+"?compact=1", nil)
				req.Header.Set("Authorization", "Bearer "+producer)
				response, getErr := http.DefaultClient.Do(req)
				if getErr != nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				decodeErr := json.NewDecoder(response.Body).Decode(&completed)
				response.Body.Close()
				if response.StatusCode != http.StatusOK || decodeErr != nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				if completed.Status == JobFailed || completed.Status == JobCancelled {
					t.Fatalf("maximum-size cluster round trip ended as %s: %s", completed.Status, completed.Error)
				}
				if completed.Status == JobCompleted {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if completed.Status != JobCompleted {
				t.Fatalf("maximum-size cluster round trip did not complete: status=%q attempt=%d node=%q error=%q", completed.Status, completed.Attempt, completed.AssignedNode, completed.Error)
			}
			if len(completed.Payload) != 0 || completed.SealedPayload != nil {
				t.Fatal("compact poll response echoed the maximum-size input")
			}

			result := completed.Result
			if encrypted {
				if completed.SealedResult == nil || len(completed.Result) != 0 {
					t.Fatal("encrypted result did not stay sealed at the relay")
				}
				if contextErr := ValidateEncryptedJobContext(encryptionContext, completed); contextErr != nil {
					t.Fatal(contextErr)
				}
				result, err = OpenResponse(shared, completed.SealedResult, ResultAAD(encryptionContext))
				if err != nil {
					t.Fatal(err)
				}
			}
			assertMaximumArtifactResult(t, result)
		})
	}

	close(localErrors)
	for localErr := range localErrors {
		if localErr != nil {
			t.Fatal(localErr)
		}
	}
}

func TestEncryptedSubmitKeepsTheFullCleartextBudget(t *testing.T) {
	const limit = MaximumJobPayloadBytes
	_, publicKey, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	exactPlaintext := bytes.Repeat([]byte{0x61}, int(limit))
	exact, _, err := SealFor(publicKey, exactPlaintext, []byte("exact"))
	if err != nil {
		t.Fatal(err)
	}
	request := SubmitRequest{Requirements: Requirements{Task: "generation"}, Sealed: exact}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(rawRequest)) > sealedSubmitBodyLimit(limit) {
		t.Fatalf("legal encrypted request needs %d bytes, wire budget is %d", len(rawRequest), sealedSubmitBodyLimit(limit))
	}
	if err := validateSubmitPayload(request, limit); err != nil {
		t.Fatalf("exact encrypted cleartext budget was rejected: %v", err)
	}

	over, _, err := SealFor(publicKey, append(exactPlaintext, 0x62), []byte("over"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSubmitPayload(SubmitRequest{Sealed: over}, limit); err == nil {
		t.Fatal("encrypted payload one byte beyond the cleartext budget was accepted")
	}
	if err := validateSubmitPayload(SubmitRequest{Payload: json.RawMessage(`{}`), Sealed: exact}, limit); err == nil {
		t.Fatal("ambiguous plaintext plus encrypted payload was accepted")
	}
}

func TestRelayRejectsUnsupportedJobPayloadLimit(t *testing.T) {
	_, err := NewRelay(RelayConfig{
		Database:    filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:  "admin_012345678901234567890123456789012345",
		MaxJobBytes: MaximumJobPayloadBytes + 1,
	}, nil)
	if err == nil {
		t.Fatal("relay accepted a payload size that its worker transport cannot guarantee")
	}
}

func TestSubmitHTTPEnforcesCleartextPayloadBoundary(t *testing.T) {
	const limit = int64(512)
	relay, err := NewRelay(RelayConfig{
		Database:     filepath.Join(t.TempDir(), "relay.db"),
		AdminToken:   "admin_012345678901234567890123456789012345",
		AllowedTasks: []string{"generation"},
		MaxJobBytes:  limit,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	producer, _, err := relay.store.CreateToken("producer", "boundary-producer", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	payloadOfSize := func(size int64) json.RawMessage {
		if size < 2 {
			t.Fatal("JSON string fixture needs at least two bytes")
		}
		return json.RawMessage(append(append([]byte{'"'}, bytes.Repeat([]byte{'x'}, int(size-2))...), '"'))
	}
	if status := submitStatus(t, server.URL, producer, SubmitRequest{
		Requirements: Requirements{Task: "generation"}, Payload: payloadOfSize(limit),
	}); status != http.StatusAccepted {
		t.Fatalf("exact cleartext payload boundary returned HTTP %d", status)
	}
	if status := submitStatus(t, server.URL, producer, SubmitRequest{
		Requirements: Requirements{Task: "generation"}, Payload: payloadOfSize(limit + 1),
	}); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("cleartext payload one byte over the boundary returned HTTP %d", status)
	}
}

func submitStatus(t *testing.T, baseURL, token string, input SubmitRequest) int {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/cluster/jobs?compact=1", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

type transferSizeError struct {
	kind      string
	got, want int
}

type transferTaskError struct{ got string }

func (e *transferTaskError) Error() string {
	return "worker task binding was missing or unexpected"
}

func (e *transferSizeError) Error() string {
	return e.kind + " had an unexpected decoded size"
}

func assertMaximumArtifactResult(t *testing.T, raw []byte) {
	t.Helper()
	var result struct {
		Job    map[string]json.RawMessage `json:"job"`
		Output struct {
			Text      string `json:"text"`
			Artifacts []struct {
				DataBase64 string `json:"data_base64"`
			} `json:"artifacts"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Output.Text != "large-transfer-ok" || len(result.Output.Artifacts) != 2 {
		t.Fatalf("maximum artifact result was incomplete: text=%q artifacts=%d", result.Output.Text, len(result.Output.Artifacts))
	}
	if _, ok := result.Job["prompt"]; ok {
		t.Fatal("compacted result retained the source prompt")
	}
	if _, ok := result.Job["image_base64"]; ok {
		t.Fatal("compacted result retained the source image")
	}
	total := 0
	for _, artifact := range result.Output.Artifacts {
		decoded, err := base64.StdEncoding.DecodeString(artifact.DataBase64)
		if err != nil {
			t.Fatal(err)
		}
		total += len(decoded)
	}
	if total != 12<<20 {
		t.Fatalf("decoded result artifacts total %d bytes, want %d", total, 12<<20)
	}
}
