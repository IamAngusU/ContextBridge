package bridge

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/vectorstore"
)

type Job struct {
	ID       string `json:"id,omitempty"`
	Source   string `json:"source,omitempty"`
	Route    string `json:"route,omitempty"`
	Provider string `json:"provider,omitempty"`
	// SessionID keeps browser follow-ups on the same selected conversation.
	SessionID string `json:"session_id,omitempty"`
	// ContextBridgeSessionKey is worker-derived for clustered browser jobs.
	// It survives the local bridge queue so the extension can isolate producers.
	ContextBridgeSessionKey string                 `json:"contextbridge_session_key,omitempty"`
	BrowserProfile          string                 `json:"browser_profile,omitempty"`
	Model                   string                 `json:"model,omitempty"`
	Reasoning               string                 `json:"reasoning,omitempty"`
	Kind                    string                 `json:"kind,omitempty"`
	Task                    string                 `json:"task,omitempty"`
	Prompt                  string                 `json:"prompt"`
	Text                    string                 `json:"text,omitempty"`
	Texts                   []string               `json:"texts,omitempty"`
	TenantID                string                 `json:"tenant_id,omitempty"`
	Documents               []vectorstore.Document `json:"documents,omitempty"`
	Query                   string                 `json:"query,omitempty"`
	TopK                    int                    `json:"top_k,omitempty"`
	ImageBase64             string                 `json:"image_base64,omitempty"`
	ImageMediaType          string                 `json:"image_media_type,omitempty"`
	Metadata                map[string]interface{} `json:"metadata,omitempty"`
	Output                  OutputSpec             `json:"output,omitempty"`
	CreatedAt               time.Time              `json:"created_at,omitempty"`
}

type OutputSpec struct {
	Mode             string   `json:"mode,omitempty"`
	RequiredKeys     []string `json:"required_keys,omitempty"`
	MaxBytes         int      `json:"max_bytes,omitempty"`
	Artifacts        bool     `json:"artifacts,omitempty"`
	MaxArtifactBytes int      `json:"max_artifact_bytes,omitempty"`
	MinArtifacts     int      `json:"min_artifacts,omitempty"`
	MinImages        int      `json:"min_images,omitempty"`
	MinMedia         int      `json:"min_media,omitempty"`
}

// Artifact is a file or image found in the final browser response. DataBase64
// is present when the browser can read the resource directly. URL is retained
// as a fallback for provider-hosted files that require the user's browser
// session or are too large to embed.
type Artifact struct {
	Name       string `json:"name"`
	MediaType  string `json:"media_type,omitempty"`
	Size       int    `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
	URL        string `json:"url,omitempty"`
}

type Decision struct {
	Verdict    string   `json:"verdict"`
	Flags      []string `json:"flags"`
	Confidence float64  `json:"confidence"`
	Model      string   `json:"model"`
	Provider   string   `json:"provider,omitempty"`
	LatencyMS  int64    `json:"latency_ms,omitempty"`
}

type Submission struct {
	Job      Job       `json:"job"`
	Decision *Decision `json:"decision,omitempty"`
	Output   *Output   `json:"output,omitempty"`
	Status   string    `json:"status"`
}

type Output struct {
	Mode              string              `json:"mode"`
	JSON              json.RawMessage     `json:"json,omitempty"`
	Text              string              `json:"text,omitempty"`
	Embeddings        [][]float32         `json:"embeddings,omitempty"`
	Dimensions        int                 `json:"dimensions,omitempty"`
	TenantID          string              `json:"tenant_id,omitempty"`
	Matches           []vectorstore.Match `json:"matches,omitempty"`
	Indexed           int                 `json:"indexed,omitempty"`
	Decision          *Decision           `json:"decision,omitempty"`
	Model             string              `json:"model,omitempty"`
	SelectedModel     string              `json:"selected_model,omitempty"`
	SelectedReasoning string              `json:"selected_reasoning,omitempty"`
	Provider          string              `json:"provider,omitempty"`
	LatencyMS         int64               `json:"latency_ms,omitempty"`
	InputTokens       uint64              `json:"input_tokens,omitempty"`
	OutputTokens      uint64              `json:"output_tokens,omitempty"`
	TotalTokens       uint64              `json:"total_tokens,omitempty"`
	Artifacts         []Artifact          `json:"artifacts,omitempty"`
	Error             string              `json:"error,omitempty"`
}

type browserJob struct {
	Job      Job         `json:"job"`
	Profile  interface{} `json:"profile"`
	Deadline time.Time   `json:"deadline"`
}

type BrowserProgress struct {
	Sequence  uint64    `json:"sequence"`
	Text      string    `json:"text"`
	Phase     string    `json:"phase,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Percent   int       `json:"percent,omitempty"`
	Busy      bool      `json:"busy"`
	UpdatedAt time.Time `json:"updated_at"`
}

func NormalizeDecision(raw []byte, provider, model string, latency time.Duration) Decision {
	var parsed Decision
	clean := strings.TrimSpace(string(raw))
	if start := strings.Index(clean, "{"); start >= 0 {
		if end := strings.LastIndex(clean, "}"); end > start {
			clean = clean[start : end+1]
		}
	}
	if json.Unmarshal([]byte(clean), &parsed) != nil {
		return ReviewDecision(provider, model, "invalid_response", latency)
	}
	parsed.Verdict = strings.ToLower(strings.TrimSpace(parsed.Verdict))
	if parsed.Verdict != "allow" && parsed.Verdict != "review" {
		parsed.Verdict = "review"
		parsed.Flags = append(parsed.Flags, "invalid_verdict")
	}
	if parsed.Confidence < 0 || parsed.Confidence > 1 {
		parsed.Confidence = 0.5
	}
	parsed.Model = truncateUTF8(strings.TrimSpace(model), 80)
	parsed.Provider = provider
	parsed.LatencyMS = latency.Milliseconds()
	if parsed.Flags == nil {
		parsed.Flags = []string{}
	}
	if len(parsed.Flags) > 20 {
		parsed.Flags = parsed.Flags[:20]
	}
	cleanFlags := make([]string, 0, len(parsed.Flags))
	for _, flag := range parsed.Flags {
		flag = truncateUTF8(strings.TrimSpace(flag), 80)
		if flag != "" {
			cleanFlags = append(cleanFlags, flag)
		}
	}
	parsed.Flags = cleanFlags
	return parsed
}

func NormalizeOutput(raw []byte, spec OutputSpec, provider, model string, latency time.Duration) Output {
	mode := outputMode(spec)
	var artifacts []Artifact
	var envelope Output
	if json.Unmarshal(raw, &envelope) == nil {
		artifacts = NormalizeArtifacts(envelope.Artifacts, spec)
	}
	selectedModel, selectedReasoning := "", ""
	if provider == "browser" && envelope.Error == "" {
		selectedModel = truncateUTF8(strings.TrimSpace(envelope.SelectedModel), 100)
		selectedReasoning = truncateUTF8(strings.TrimSpace(envelope.SelectedReasoning), 100)
	}
	if mode == "decision" {
		decision := NormalizeDecision(raw, provider, model, latency)
		return Output{Mode: mode, Decision: &decision, Model: decision.Model, SelectedModel: selectedModel, SelectedReasoning: selectedReasoning, Provider: provider, LatencyMS: decision.LatencyMS, Artifacts: artifacts}
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Mode == mode {
		if envelope.Error != "" {
			return OutputError(mode, provider, model, envelope.Error, latency)
		}
		if mode == "json" && len(envelope.JSON) > 0 {
			raw = envelope.JSON
		} else if mode == "text" && envelope.Text != "" {
			raw = []byte(envelope.Text)
		}
	}
	if spec.MinArtifacts > 0 {
		verified := 0
		for _, artifact := range artifacts {
			if artifact.DataBase64 != "" {
				verified++
			}
		}
		if verified < spec.MinArtifacts {
			return OutputError(mode, provider, model, fmt.Sprintf("artifacts_missing: expected %d file(s), received %d", spec.MinArtifacts, verified), latency)
		}
	}
	if spec.MinImages > 0 {
		verified := 0
		for _, artifact := range artifacts {
			if artifact.DataBase64 != "" && strings.HasPrefix(artifact.MediaType, "image/") {
				verified++
			}
		}
		if verified < spec.MinImages {
			return OutputError(mode, provider, model, fmt.Sprintf("images_missing: expected %d image(s), received %d", spec.MinImages, verified), latency)
		}
	}
	if spec.MinMedia > 0 {
		verified := 0
		for _, artifact := range artifacts {
			if artifact.DataBase64 != "" && (strings.HasPrefix(artifact.MediaType, "audio/") || strings.HasPrefix(artifact.MediaType, "video/")) {
				verified++
			}
		}
		if verified < spec.MinMedia {
			return OutputError(mode, provider, model, fmt.Sprintf("media_missing: expected %d audio/video file(s), received %d", spec.MinMedia, verified), latency)
		}
	}

	limit := outputLimit(spec)
	clean := strings.TrimSpace(string(raw))
	if mode == "text" {
		if len(clean) > limit {
			clean = truncateUTF8(clean, limit)
		}
		if clean == "" {
			return OutputError(mode, provider, model, "empty_response", latency)
		}
		return Output{Mode: mode, Text: clean, Model: model, SelectedModel: selectedModel, SelectedReasoning: selectedReasoning, Provider: provider, LatencyMS: latency.Milliseconds(), Artifacts: artifacts}
	}

	if start := strings.IndexAny(clean, "[{"); start >= 0 {
		var end int
		if clean[start] == '[' {
			end = strings.LastIndex(clean, "]")
		} else {
			end = strings.LastIndex(clean, "}")
		}
		if end > start {
			clean = clean[start : end+1]
		}
	}
	if len(clean) > limit || !json.Valid([]byte(clean)) {
		return OutputError(mode, provider, model, "invalid_json", latency)
	}
	if len(spec.RequiredKeys) > 0 {
		var object map[string]interface{}
		if json.Unmarshal([]byte(clean), &object) != nil {
			return OutputError(mode, provider, model, "json_object_required", latency)
		}
		for _, key := range spec.RequiredKeys {
			if _, ok := object[key]; !ok {
				return OutputError(mode, provider, model, "missing_required_key:"+key, latency)
			}
		}
	}
	return Output{Mode: mode, JSON: json.RawMessage(clean), Model: model, SelectedModel: selectedModel, SelectedReasoning: selectedReasoning, Provider: provider, LatencyMS: latency.Milliseconds(), Artifacts: artifacts}
}

// NormalizeArtifacts applies the protocol's bounded, deterministic artifact
// rules. Invalid entries are omitted without discarding an otherwise useful
// model answer.
func NormalizeArtifacts(input []Artifact, spec OutputSpec) []Artifact {
	if !spec.Artifacts || len(input) == 0 {
		return nil
	}
	limit := spec.MaxArtifactBytes
	if limit <= 0 || limit > 12<<20 {
		limit = 12 << 20
	}
	result := make([]Artifact, 0, min(len(input), 12))
	total := 0
	for _, item := range input {
		if len(result) == 12 {
			break
		}
		item.Name = artifactName(item.Name, len(result)+1)
		item.MediaType = strings.ToLower(strings.TrimSpace(item.MediaType))
		if len(item.MediaType) > 100 || !allowedArtifactMediaType(item.MediaType) {
			continue
		}
		item.URL = safeArtifactURL(item.URL)
		if item.DataBase64 != "" {
			decoded, err := base64.StdEncoding.DecodeString(item.DataBase64)
			if err != nil || len(decoded) == 0 || total+len(decoded) > limit {
				item.DataBase64 = ""
				item.Size = 0
				item.SHA256 = ""
			} else {
				if !artifactBytesMatchMediaType(decoded, item.MediaType) {
					item.DataBase64 = ""
					item.Size = 0
					item.SHA256 = ""
					if item.URL == "" {
						continue
					}
					result = append(result, item)
					continue
				}
				total += len(decoded)
				item.Size = len(decoded)
				digest := sha256.Sum256(decoded)
				item.SHA256 = fmt.Sprintf("%x", digest[:])
			}
		}
		if item.DataBase64 == "" && item.URL == "" {
			continue
		}
		result = append(result, item)
	}
	return result
}

func artifactName(value string, index int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = filepath.Base(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, value)
	value = truncateUTF8(value, 180)
	if value == "" || value == "." {
		return fmt.Sprintf("artifact-%d", index)
	}
	return value
}

func safeArtifactURL(value string) string {
	if value == "" || len(value) > 4096 {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	parsed.Fragment = ""
	return parsed.String()
}

func allowedArtifactMediaType(value string) bool {
	if strings.HasPrefix(value, "image/") || strings.HasPrefix(value, "text/") {
		return true
	}
	switch value {
	case "video/mp4", "video/webm", "audio/mpeg", "audio/wave", "audio/wav", "audio/ogg", "audio/webm":
		return true
	case "application/pdf", "application/zip", "application/json", "application/octet-stream",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return true
	default:
		return false
	}
}

func artifactBytesMatchMediaType(data []byte, mediaType string) bool {
	actual := http.DetectContentType(data)
	if strings.HasPrefix(mediaType, "image/") {
		return actual == mediaType
	}
	switch mediaType {
	case "video/mp4":
		return actual == "video/mp4"
	case "video/webm", "audio/webm":
		return actual == "video/webm"
	case "audio/mpeg":
		return actual == "audio/mpeg"
	case "audio/wave", "audio/wav":
		return actual == "audio/wave"
	case "audio/ogg":
		return actual == "application/ogg"
	default:
		return true
	}
}

func OutputError(mode, provider, model, message string, latency time.Duration) Output {
	return Output{Mode: mode, Provider: provider, Model: model, LatencyMS: latency.Milliseconds(), Error: message}
}

func outputMode(spec OutputSpec) string {
	mode := strings.ToLower(strings.TrimSpace(spec.Mode))
	if mode == "" {
		return "decision"
	}
	return mode
}

func jobTask(job Job, routeTask string) string {
	task := strings.ToLower(strings.TrimSpace(routeTask))
	if task != "" {
		return task
	}
	task = strings.ToLower(strings.TrimSpace(job.Task))
	if task == "" {
		task = strings.ToLower(strings.TrimSpace(job.Kind))
	}
	if task == "" {
		task = "generation"
	}
	return task
}

func applyTaskOutput(job *Job, routeTask string) {
	switch jobTask(*job, routeTask) {
	case "moderation":
		job.Output.Mode = "decision"
	case "extraction":
		job.Output.Mode = "json"
	case "embedding":
		job.Output.Mode = "embedding"
	case "rag_ingest", "rag_query":
		job.Output.Mode = "rag"
	}
}

func outputLimit(spec OutputSpec) int {
	if spec.MaxBytes >= 256 && spec.MaxBytes <= 1<<20 {
		return spec.MaxBytes
	}
	return 64 << 10
}

func truncateUTF8(value string, limit int) string {
	if limit < 0 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func ReviewDecision(provider, model, flag string, latency time.Duration) Decision {
	return Decision{
		Verdict:    "review",
		Flags:      []string{flag},
		Confidence: 0.4,
		Model:      model,
		Provider:   provider,
		LatencyMS:  latency.Milliseconds(),
	}
}
