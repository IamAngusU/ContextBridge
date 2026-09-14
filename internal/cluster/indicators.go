package cluster

import "strings"

// IndicatorSources reports only sources actually advertised by this worker.
// The order is shared by the terminal and relay dashboard.
func IndicatorSources(cap Capabilities) []string {
	seen := map[string]bool{}
	for _, session := range cap.BrowserSessions {
		switch strings.ToLower(strings.TrimSpace(session.Profile)) {
		case "chatgpt", "gemini":
			seen[strings.ToLower(session.Profile)] = true
		}
	}
	for _, provider := range cap.Providers {
		if provider != "" && !strings.EqualFold(provider, "browser") {
			seen["local"] = true
		}
	}
	for _, model := range cap.Models {
		if model.Provider != "" && !strings.EqualFold(model.Provider, "browser") {
			seen["local"] = true
		}
	}
	order := []string{"chatgpt", "gemini", "local"}
	result := make([]string, 0, len(order))
	for _, source := range order {
		if seen[source] {
			result = append(result, source)
		}
	}
	return result
}

// IndicatorModes maps advertised tasks and model flags to stable modality
// badges. An unreported mode stays gray in the UI; no browser feature is
// assumed from the provider name alone.
func IndicatorModes(cap Capabilities) []string {
	seen := map[string]bool{}
	markTask := func(value string) {
		task := strings.ToLower(strings.TrimSpace(value))
		switch task {
		case "generation", "text", "chat", "extraction", "moderation", "rag_query", "rag_ingest", "translation", "coding":
			seen["text"] = true
		case "vision", "image_understanding", "image_analysis", "ocr":
			seen["vision"] = true
		case "image", "images", "image_generation", "image_editing":
			seen["image"] = true
		case "audio", "speech", "transcription", "text_to_speech", "speech_to_text":
			seen["audio"] = true
		case "music", "music_generation":
			seen["music"] = true
		case "video", "video_generation", "video_editing":
			seen["video"] = true
		case "file", "files", "file_analysis":
			seen["files"] = true
		case "embedding", "embeddings":
			seen["embedding"] = true
		}
	}
	for _, task := range cap.Tasks {
		markTask(task)
	}
	for _, model := range cap.Models {
		for _, task := range model.Tasks {
			markTask(task)
		}
		if model.Vision {
			seen["vision"] = true
		}
		if model.Embedding {
			seen["embedding"] = true
		}
	}
	if len(cap.BrowserSessions) > 0 {
		seen["text"] = true
	}
	order := []string{"text", "vision", "image", "audio", "music", "video", "files", "embedding"}
	result := make([]string, 0, len(order))
	for _, mode := range order {
		if seen[mode] {
			result = append(result, mode)
		}
	}
	return result
}
