package bridge

import (
	"encoding/json"
	"strings"
	"time"
)

type Job struct {
	ID             string                 `json:"id,omitempty"`
	Source         string                 `json:"source,omitempty"`
	Route          string                 `json:"route,omitempty"`
	Kind           string                 `json:"kind,omitempty"`
	Prompt         string                 `json:"prompt"`
	Text           string                 `json:"text,omitempty"`
	ImageBase64    string                 `json:"image_base64,omitempty"`
	ImageMediaType string                 `json:"image_media_type,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt      time.Time              `json:"created_at,omitempty"`
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
	Status   string    `json:"status"`
}

type browserJob struct {
	Job      Job         `json:"job"`
	Profile  interface{} `json:"profile"`
	Deadline time.Time   `json:"deadline"`
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
	if parsed.Model == "" {
		parsed.Model = model
	}
	parsed.Provider = provider
	parsed.LatencyMS = latency.Milliseconds()
	if parsed.Flags == nil {
		parsed.Flags = []string{}
	}
	if len(parsed.Flags) > 20 {
		parsed.Flags = parsed.Flags[:20]
	}
	return parsed
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
