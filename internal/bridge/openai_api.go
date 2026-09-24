package bridge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type openAIChatRequest struct {
	Model               string          `json:"model"`
	Messages            []openAIMessage `json:"messages"`
	Stream              bool            `json:"stream,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	N                   int             `json:"n,omitempty"`
	ResponseFormat      *struct {
		Type string `json:"type"`
	} `json:"response_format,omitempty"`
	Tools      json.RawMessage `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
}

type openAIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL json.RawMessage `json:"image_url,omitempty"`
}

const (
	contextBridgeStreamModeHeader        = "X-ContextBridge-Stream-Mode"
	contextBridgeRequireStreamModeHeader = "X-ContextBridge-Require-Stream-Mode"
	contextBridgeFinalResultStreamMode   = "final-result"
	contextBridgeIncrementalStreamMode   = "incremental"
)

func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeOpenAIError(w, http.StatusMethodNotAllowed, "GET required", "invalid_request_error")
		return
	}
	names := make([]string, 0, len(s.cfg.Routes))
	for name := range s.cfg.Routes {
		names = append(names, name)
	}
	sort.Strings(names)
	created := time.Now().Unix()
	models := make([]map[string]interface{}, 0, len(names))
	for _, name := range names {
		models = append(models, map[string]interface{}{"id": "contextbridge:" + name, "object": "model", "created": created, "owned_by": "contextbridge"})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"object": "list", "data": models})
}

func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeOpenAIError(w, http.StatusMethodNotAllowed, "POST required", "invalid_request_error")
		return
	}
	var input openAIChatRequest
	if err := decodeCompatibleJSON(r.Body, &input, maximumJobRequestBytes); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	requiredMode := ""
	if input.Stream {
		requiredMode = strings.ToLower(strings.TrimSpace(r.Header.Get(contextBridgeRequireStreamModeHeader)))
		if requiredMode != "" && requiredMode != contextBridgeFinalResultStreamMode && requiredMode != contextBridgeIncrementalStreamMode {
			w.Header().Set(contextBridgeStreamModeHeader, contextBridgeFinalResultStreamMode)
			writeOpenAIError(w, http.StatusUnprocessableEntity, "unsupported required stream mode", "unsupported_parameter")
			return
		}
	}
	if input.N > 1 {
		writeOpenAIError(w, http.StatusUnprocessableEntity, "ContextBridge currently supports n=1", "unsupported_parameter")
		return
	}
	if rawJSONPresent(input.Tools) || rawJSONPresent(input.ToolChoice) {
		writeOpenAIError(w, http.StatusUnprocessableEntity, "tool calls are not exposed through the compatibility endpoint; use ContextBridge MCP", "unsupported_parameter")
		return
	}
	route, err := s.openAIRoute(input.Model)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error(), "model_not_found")
		return
	}
	prompt, history, image, mediaType, err := openAIJobInput(input.Messages)
	if err != nil {
		writeOpenAIError(w, http.StatusUnprocessableEntity, err.Error(), "invalid_request_error")
		return
	}
	mode := "text"
	if input.ResponseFormat != nil {
		switch input.ResponseFormat.Type {
		case "", "text":
		case "json_object":
			mode = "json"
		default:
			writeOpenAIError(w, http.StatusUnprocessableEntity, "only response_format text and json_object are supported", "unsupported_parameter")
			return
		}
	}
	maxTokens := input.MaxCompletionTokens
	if maxTokens <= 0 {
		maxTokens = input.MaxTokens
	}
	maxBytes := 0
	if maxTokens > 0 {
		if maxTokens > 250000 {
			writeOpenAIError(w, http.StatusUnprocessableEntity, "max_tokens exceeds the compatibility limit", "invalid_request_error")
			return
		}
		maxBytes = maxTokens * 4
	}
	job := Job{Source: "openai-compatible-api", Route: route, Prompt: prompt, Text: history, ImageBase64: image, ImageMediaType: mediaType, Output: OutputSpec{Mode: mode, MaxBytes: maxBytes}}
	prepareJob(&job)
	job.SessionID = "openai-" + job.ID
	job.Metadata = map[string]interface{}{"contextbridge_new_session": true, "contextbridge_close_endpoint_after_job": true}
	if routeTask := strings.TrimSpace(s.cfg.Route(job.Route).Task); routeTask != "" {
		job.Task = routeTask
	}
	applyTaskOutput(&job, s.cfg.Route(job.Route).Task)
	if err := validateJob(job); err != nil {
		writeOpenAIError(w, http.StatusUnprocessableEntity, err.Error(), "invalid_request_error")
		return
	}
	responseID := "chatcmpl-" + job.ID
	created := time.Now().Unix()
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = "contextbridge:" + route
	}
	incremental := input.Stream && requiredMode != contextBridgeFinalResultStreamMode && s.SupportsIncremental(job)
	flusher, canFlush := w.(http.Flusher)
	if incremental && !canFlush {
		incremental = false
	}
	if input.Stream && requiredMode == contextBridgeIncrementalStreamMode && !incremental {
		w.Header().Set(contextBridgeStreamModeHeader, contextBridgeFinalResultStreamMode)
		writeOpenAIError(w, http.StatusConflict, "incremental streaming is not available for the selected route; no job was submitted", "stream_mode_unavailable")
		return
	}
	if incremental {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set(contextBridgeStreamModeHeader, contextBridgeIncrementalStreamMode)
		started := false
		writeChunk := func(delta map[string]string, finish interface{}) error {
			chunk := map[string]interface{}{
				"id": responseID, "object": "chat.completion.chunk", "created": created, "model": model,
				"choices": []map[string]interface{}{{"index": 0, "delta": delta, "finish_reason": finish}},
			}
			raw, _ := json.Marshal(chunk)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}
		output, err := s.ProcessIncremental(r.Context(), job, func(text string) error {
			if !started {
				if err := writeChunk(map[string]string{"role": "assistant"}, nil); err != nil {
					return err
				}
				started = true
			}
			return writeChunk(map[string]string{"content": text}, nil)
		})
		if err != nil {
			if !started {
				writeOpenAIError(w, http.StatusServiceUnavailable, err.Error(), "server_error")
			}
			return
		}
		if output.Error != "" {
			if !started {
				writeOpenAIError(w, http.StatusBadGateway, output.Error, "provider_error")
			}
			return
		}
		finish := "stop"
		if output.Truncated {
			finish = "length"
		}
		if err := writeChunk(map[string]string{}, finish); err != nil {
			return
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	output, err := s.Process(r.Context(), job)
	if err != nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, err.Error(), "server_error")
		return
	}
	if output.Error != "" {
		writeOpenAIError(w, http.StatusBadGateway, output.Error, "provider_error")
		return
	}
	content := output.Text
	if mode == "json" {
		content = string(output.JSON)
	}
	finish := "stop"
	if output.Truncated {
		finish = "length"
	}
	if input.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set(contextBridgeStreamModeHeader, contextBridgeFinalResultStreamMode)
		chunk := map[string]interface{}{"id": responseID, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []map[string]interface{}{{"index": 0, "delta": map[string]string{"role": "assistant", "content": content}, "finish_reason": finish}}}
		raw, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id": responseID, "object": "chat.completion", "created": created, "model": model,
		"choices": []map[string]interface{}{{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": finish}},
		"usage":   map[string]uint64{"prompt_tokens": output.InputTokens, "completion_tokens": output.OutputTokens, "total_tokens": output.TotalTokens},
	})
}

func (s *Server) openAIRoute(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" || strings.EqualFold(model, "contextbridge") {
		return "default", nil
	}
	route := model
	if strings.HasPrefix(strings.ToLower(route), "contextbridge:") {
		route = route[len("contextbridge:"):]
	}
	if _, ok := s.cfg.Routes[route]; !ok {
		return "", fmt.Errorf("unknown ContextBridge model %q; use GET /openai/v1/models", model)
	}
	return route, nil
}

func openAIJobInput(messages []openAIMessage) (prompt, history, image, mediaType string, err error) {
	if len(messages) == 0 || len(messages) > 128 {
		return "", "", "", "", errors.New("messages must contain between 1 and 128 entries")
	}
	lastUser := -1
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(messages[index].Role), "user") {
			lastUser = index
			break
		}
	}
	if lastUser < 0 {
		return "", "", "", "", errors.New("a user message is required")
	}
	trusted := make([]string, 0)
	historyParts := make([]string, 0)
	for index, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return "", "", "", "", fmt.Errorf("unsupported message role %q", message.Role)
		}
		text, data, mime, parseErr := parseOpenAIContent(message.Content)
		if parseErr != nil {
			return "", "", "", "", parseErr
		}
		if data != "" {
			if index != lastUser {
				return "", "", "", "", errors.New("only the final user message may contain one image")
			}
			if image != "" {
				return "", "", "", "", errors.New("only one image is supported per request")
			}
			image, mediaType = data, mime
		}
		if role == "system" || role == "developer" {
			if strings.TrimSpace(text) != "" {
				trusted = append(trusted, strings.ToUpper(role)+":\n"+text)
			}
			continue
		}
		if index == lastUser {
			trusted = append(trusted, "USER REQUEST:\n"+text)
			continue
		}
		if strings.TrimSpace(text) != "" {
			historyParts = append(historyParts, fmt.Sprintf("<message role=%q>\n%s\n</message>", role, text))
		}
	}
	prompt = strings.TrimSpace(strings.Join(trusted, "\n\n"))
	history = strings.Join(historyParts, "\n")
	if prompt == "" {
		return "", "", "", "", errors.New("the final user request is empty")
	}
	if len(prompt) > 20000 || len(history) > 200000 {
		return "", "", "", "", errors.New("messages exceed ContextBridge prompt/history limits")
	}
	return prompt, history, image, mediaType, nil
}

func parseOpenAIContent(raw json.RawMessage) (text, image, mediaType string, err error) {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain, "", "", nil
	}
	var parts []openAIContentPart
	if json.Unmarshal(raw, &parts) != nil || len(parts) > 128 {
		return "", "", "", errors.New("message content must be text or a bounded content-part array")
	}
	texts := make([]string, 0)
	for _, part := range parts {
		switch strings.ToLower(strings.TrimSpace(part.Type)) {
		case "text", "input_text":
			texts = append(texts, part.Text)
		case "image_url", "input_image":
			if image != "" {
				return "", "", "", errors.New("only one image is supported per request")
			}
			var value string
			if json.Unmarshal(part.ImageURL, &value) != nil {
				var object struct {
					URL string `json:"url"`
				}
				if json.Unmarshal(part.ImageURL, &object) != nil {
					return "", "", "", errors.New("image_url must contain a data URL")
				}
				value = object.URL
			}
			mime, data, decodeErr := decodeImageDataURL(value)
			if decodeErr != nil {
				return "", "", "", decodeErr
			}
			image, mediaType = data, mime
		default:
			return "", "", "", fmt.Errorf("unsupported content part %q", part.Type)
		}
	}
	return strings.Join(texts, "\n"), image, mediaType, nil
}

func decodeImageDataURL(value string) (string, string, error) {
	prefix := "data:"
	if !strings.HasPrefix(strings.ToLower(value), prefix) {
		return "", "", errors.New("remote image URLs are not fetched; use an image data URL")
	}
	separator := strings.IndexByte(value, ',')
	if separator < 0 {
		return "", "", errors.New("invalid image data URL")
	}
	header, encoded := value[len(prefix):separator], value[separator+1:]
	parts := strings.Split(header, ";")
	mediaType := strings.ToLower(strings.TrimSpace(parts[0]))
	if !strings.HasPrefix(mediaType, "image/") || len(parts) < 2 || !strings.EqualFold(parts[len(parts)-1], "base64") {
		return "", "", errors.New("image data URL must use an image MIME type and base64")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(8<<20) {
		return "", "", errors.New("image exceeds the 8 MiB decoded limit")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 || len(decoded) > 8<<20 {
		return "", "", errors.New("image data URL is invalid or exceeds 8 MiB")
	}
	return mediaType, base64.StdEncoding.EncodeToString(decoded), nil
}

func rawJSONPresent(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return value != "" && value != "null" && value != "[]" && value != `"none"`
}

func decodeCompatibleJSON(reader io.Reader, target interface{}, limit int64) error {
	limited := io.LimitReader(reader, limit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return errors.New("request body exceeds the compatibility limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}

func writeOpenAIError(w http.ResponseWriter, status int, message, kind string) {
	writeJSON(w, status, map[string]interface{}{"error": map[string]interface{}{"message": message, "type": kind, "param": nil, "code": nil}})
}
