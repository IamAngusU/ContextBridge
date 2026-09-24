package bridge

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestOpenAICompatibilityAPIListsRoutesAndCompletes(t *testing.T) {
	providerCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, request)
			return
		}
		var payload map[string]interface{}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(payload)
		providerCalls++
		if providerCalls == 1 && (!bytes.Contains(raw, []byte("Keep this terse")) || !bytes.Contains(raw, []byte("previous answer")) || !bytes.Contains(raw, []byte("Say API-OK"))) {
			t.Fatalf("OpenAI messages were not preserved across trust/history boundaries: %s", raw)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"content": "API-OK"}}},
			"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 2, "total_tokens": 9},
		})
	}))
	defer provider.Close()
	directory := t.TempDir()
	cfg := config.Config{
		Version: 1, Server: config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: directory, Inbox: directory},
		Routes:  map[string]config.Route{"default": {Provider: "remote", TimeoutSeconds: 5}, "fast": {Provider: "remote", TimeoutSeconds: 5}},
		Engines: map[string]config.Engine{"remote": {Type: "openai_compatible", URL: provider.URL + "/v1", Model: "mock-model", Capabilities: []string{"text"}, TimeoutSeconds: 5}},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	models := authorizedRequest(t, http.MethodGet, httpServer.URL+"/openai/v1/models", cfg.Server.Token, nil)
	defer models.Body.Close()
	modelBody, _ := io.ReadAll(models.Body)
	if models.StatusCode != http.StatusOK || !bytes.Contains(modelBody, []byte("contextbridge:default")) || !bytes.Contains(modelBody, []byte("contextbridge:fast")) {
		t.Fatalf("route models missing: HTTP %d %s", models.StatusCode, modelBody)
	}

	requestBody := []byte(`{"model":"contextbridge:fast","messages":[{"role":"system","content":"Keep this terse"},{"role":"user","content":"old question"},{"role":"assistant","content":"previous answer"},{"role":"user","content":"Say API-OK"}],"stream":false}`)
	response := authorizedRequest(t, http.MethodPost, httpServer.URL+"/openai/v1/chat/completions", cfg.Server.Token, requestBody)
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte(`"content":"API-OK"`)) || !bytes.Contains(raw, []byte(`"total_tokens":9`)) {
		t.Fatalf("compatibility completion failed: HTTP %d %s", response.StatusCode, raw)
	}

	streamBody := []byte(`{"model":"contextbridge:default","messages":[{"role":"user","content":"Say API-OK"}],"stream":true}`)
	stream := authorizedRequest(t, http.MethodPost, httpServer.URL+"/openai/v1/chat/completions", cfg.Server.Token, streamBody)
	defer stream.Body.Close()
	streamRaw, _ := io.ReadAll(stream.Body)
	if stream.StatusCode != http.StatusOK || stream.Header.Get("Content-Type") != "text/event-stream" || stream.Header.Get(contextBridgeStreamModeHeader) != contextBridgeFinalResultStreamMode || stream.Header.Get(contextBridgeStreamResumeHeader) != contextBridgeStreamResumeUnsupported || !bytes.Contains(streamRaw, []byte("data: [DONE]")) {
		t.Fatalf("bounded SSE response failed: HTTP %d %q %s", stream.StatusCode, stream.Header.Get("Content-Type"), streamRaw)
	}

	requiredRequest, err := http.NewRequest(http.MethodPost, httpServer.URL+"/openai/v1/chat/completions", bytes.NewReader(streamBody))
	if err != nil {
		t.Fatal(err)
	}
	requiredRequest.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	requiredRequest.Header.Set("Content-Type", "application/json")
	requiredRequest.Header.Set(contextBridgeRequireStreamModeHeader, "incremental")
	requiredResponse, err := http.DefaultClient.Do(requiredRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer requiredResponse.Body.Close()
	requiredRaw, _ := io.ReadAll(requiredResponse.Body)
	if requiredResponse.StatusCode != http.StatusConflict || requiredResponse.Header.Get(contextBridgeStreamModeHeader) != contextBridgeFinalResultStreamMode || requiredResponse.Header.Get(contextBridgeStreamResumeHeader) != contextBridgeStreamResumeUnsupported || !bytes.Contains(requiredRaw, []byte("stream_mode_unavailable")) {
		t.Fatalf("required incremental mode did not fail closed: HTTP %d %q %s", requiredResponse.StatusCode, requiredResponse.Header.Get(contextBridgeStreamModeHeader), requiredRaw)
	}
	if providerCalls != 2 {
		t.Fatalf("unavailable required stream mode submitted work: provider calls = %d, want 2", providerCalls)
	}
}

func TestOpenAICompatibilityNativeIncrementalArrivesBeforeProviderCompletion(t *testing.T) {
	releaseProvider := make(chan struct{})
	defer func() {
		select {
		case <-releaseProvider:
		default:
			close(releaseProvider)
		}
	}()
	var providerCompleted atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || !payload.Stream || request.Header.Get("Accept") != "text/event-stream" {
			http.Error(w, "native stream not requested", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"  STREAM-\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-releaseProvider
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK  \"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		providerCompleted.Store(true)
	}))
	defer provider.Close()
	directory := t.TempDir()
	cfg := config.Config{
		Version: 1, Server: config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: directory, Inbox: directory},
		Routes:  map[string]config.Route{"default": {Provider: "remote", TimeoutSeconds: 5}},
		Engines: map[string]config.Engine{"remote": {
			Type: "openai_compatible", URL: provider.URL + "/v1", Model: "mock-model",
			Capabilities: []string{"text", "incremental_output"}, TimeoutSeconds: 5,
		}},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	body := []byte(`{"model":"contextbridge:default","messages":[{"role":"user","content":"stream"}],"stream":true}`)
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/openai/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(contextBridgeRequireStreamModeHeader, contextBridgeIncrementalStreamMode)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get(contextBridgeStreamModeHeader) != contextBridgeIncrementalStreamMode || response.Header.Get(contextBridgeStreamResumeHeader) != contextBridgeStreamResumeUnsupported {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("native stream was not negotiated: HTTP %d mode=%q %s", response.StatusCode, response.Header.Get(contextBridgeStreamModeHeader), raw)
	}
	reader := bufio.NewReader(response.Body)
	prefix := ""
	for !strings.Contains(prefix, "STREAM-") {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("first native delta was not readable: %v (%s)", err, prefix)
		}
		prefix += line
	}
	if providerCompleted.Load() {
		t.Fatal("first client delta arrived only after provider completion")
	}
	close(releaseProvider)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	stream := prefix + string(rest)
	if !strings.Contains(stream, `"content":"STREAM-"`) || !strings.Contains(stream, `"content":"OK"`) || strings.Count(stream, "data: [DONE]") != 1 {
		t.Fatalf("native stream did not preserve ordered normalized deltas: %s", stream)
	}
	if !providerCompleted.Load() {
		t.Fatal("provider did not finish after stream release")
	}
}

func TestOpenAICompatibilityAPIRejectsToolsAndRemoteImages(t *testing.T) {
	request := openAIChatRequest{Tools: json.RawMessage(`[{"type":"function"}]`)}
	if !rawJSONPresent(request.Tools) {
		t.Fatal("tool request was not recognized")
	}
	_, _, _, _, err := openAIJobInput([]openAIMessage{{Role: "user", Content: json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.test/private.png"}}]`)}})
	if err == nil || !strings.Contains(err.Error(), "not fetched") {
		t.Fatalf("remote image fetch was not rejected: %v", err)
	}
}

func TestOpenAICompatibilityBoundaries(t *testing.T) {
	messages := make([]openAIMessage, 128)
	for index := range messages {
		messages[index] = openAIMessage{Role: "assistant", Content: json.RawMessage(`"history"`)}
	}
	messages[len(messages)-1] = openAIMessage{Role: "user", Content: json.RawMessage(`"final"`)}
	if _, _, _, _, err := openAIJobInput(messages); err != nil {
		t.Fatalf("documented 128-message boundary was rejected: %v", err)
	}
	messages = append(messages, openAIMessage{Role: "user", Content: json.RawMessage(`"overflow"`)})
	if _, _, _, _, err := openAIJobInput(messages); err == nil || !strings.Contains(err.Error(), "128") {
		t.Fatalf("129-message request escaped the boundary: %v", err)
	}

	trusted := strings.Repeat("x", 20_000-len("USER REQUEST:\n"))
	if _, _, _, _, err := openAIJobInput([]openAIMessage{{Role: "user", Content: mustRawJSONString(t, trusted)}}); err != nil {
		t.Fatalf("documented trusted-text boundary was rejected: %v", err)
	}
	trusted += "x"
	if _, _, _, _, err := openAIJobInput([]openAIMessage{{Role: "user", Content: mustRawJSONString(t, trusted)}}); err == nil {
		t.Fatal("oversized trusted instructions were accepted")
	}

	maximumImage := bytes.Repeat([]byte{0x5a}, 8<<20)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(maximumImage)
	if mime, decoded, err := decodeImageDataURL(dataURL); err != nil || mime != "image/png" || len(decoded) != base64.StdEncoding.EncodedLen(len(maximumImage)) {
		t.Fatalf("documented 8 MiB image boundary failed: mime=%q bytes=%d err=%v", mime, len(decoded), err)
	}
	tooLarge := append(maximumImage, 0x01)
	if _, _, err := decodeImageDataURL("data:image/png;base64," + base64.StdEncoding.EncodeToString(tooLarge)); err == nil {
		t.Fatal("image above 8 MiB was accepted")
	}
}

func mustRawJSONString(t *testing.T, value string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func authorizedRequest(t *testing.T, method, target, token string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
