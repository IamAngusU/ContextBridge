package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Previous model output is untrusted. The prompt only receives a reference;
// the actual bytes go into Job.Text's submitted_content data section.
func schedulePrompt(template string, previous Output) (string, string, error) {
	replacements := map[string]string{
		"{{previous.text}}":           previous.Text,
		"{{previous.json}}":           string(previous.JSON),
		"{{previous.artifact_names}}": artifactNames(previous.Artifacts),
	}
	context := map[string]string{}
	for marker, value := range replacements {
		if !strings.Contains(template, marker) {
			continue
		}
		if strings.TrimSpace(value) == "" {
			return "", "", fmt.Errorf("%s is empty; next step was not sent", marker)
		}
		field := strings.TrimSuffix(strings.TrimPrefix(marker, "{{previous."), "}}")
		context[field] = value
		template = strings.ReplaceAll(template, marker, "previous_result."+field+" in submitted_content")
	}
	if strings.Contains(template, "{{previous.") {
		return "", "", errors.New("unknown previous-result variable; next step was not sent")
	}
	if len(template) > 20000 {
		return "", "", errors.New("expanded prompt exceeds 20000 bytes; next step was not sent")
	}
	if len(context) == 0 {
		return template, "", nil
	}
	raw, err := json.Marshal(map[string]interface{}{"previous_result": context})
	if err != nil {
		return "", "", err
	}
	return template, string(raw), nil
}

func artifactNames(artifacts []Artifact) string {
	names := make([]string, 0, len(artifacts))
	for _, item := range artifacts {
		if item.DataBase64 != "" {
			names = append(names, item.Name)
		}
	}
	return strings.Join(names, ", ")
}

func verifiedPreviousArtifact(previous Output, kind string) (Artifact, error) {
	if kind != "image" && kind != "file" {
		return Artifact{}, errors.New("previous artifact kind must be image or file")
	}
	for _, item := range previous.Artifacts {
		mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
		if kind == "image" && !strings.HasPrefix(mediaType, "image/") {
			continue
		}
		if item.DataBase64 == "" {
			continue // A URL alone is not a transferable file.
		}
		decoded, err := base64.StdEncoding.DecodeString(item.DataBase64)
		if err != nil || len(decoded) == 0 || len(decoded) > 8<<20 || !artifactBytesMatchMediaType(decoded, mediaType) {
			continue
		}
		digest := sha256.Sum256(decoded)
		canonicalDigest := hex.EncodeToString(digest[:])
		if item.SHA256 == "" || !strings.EqualFold(item.SHA256, canonicalDigest) {
			continue
		}
		// The previous provider controls its JSON fields. Preserve only values
		// verified from the embedded bytes and normalize away an unrelated URL.
		item.MediaType = mediaType
		item.Size = len(decoded)
		item.SHA256 = canonicalDigest
		item.URL = ""
		return item, nil
	}
	return Artifact{}, fmt.Errorf("no verified %s bytes in the previous result; next step was not sent", kind)
}

func (s *Server) nextScheduleStep(base Job, step ScheduleStep, index int, previous Output) (Job, error) {
	job := step.Job
	job.ID = fmt.Sprintf("%s-step-%d", base.ID, index+1)
	job.Source = "schedule"
	job.CreatedAt = time.Now().UTC()
	if job.SessionID == "" {
		job.SessionID = base.SessionID
	}
	if routeTask := strings.TrimSpace(s.cfg.Route(job.Route).Task); routeTask != "" {
		job.Task = routeTask
	}
	applyTaskOutput(&job, s.cfg.Route(job.Route).Task)
	metadata := make(map[string]interface{}, len(job.Metadata)+3)
	for key, value := range job.Metadata {
		metadata[key] = value
	}
	metadata["contextbridge_new_session"] = true
	if _, set := metadata["contextbridge_foreground_new_session"]; !set {
		metadata["contextbridge_foreground_new_session"] = base.Metadata["contextbridge_foreground_new_session"]
	}
	job.Metadata = metadata
	var err error
	var previousContext string
	job.Prompt, previousContext, err = schedulePrompt(job.Prompt, previous)
	if err != nil {
		return Job{}, err
	}
	if previousContext != "" {
		if job.Text != "" {
			job.Text += "\n\n"
		}
		job.Text += previousContext
	}
	if step.UsePreviousArtifact != "" {
		artifact, err := verifiedPreviousArtifact(previous, step.UsePreviousArtifact)
		if err != nil {
			return Job{}, err
		}
		provider := s.cfg.Route(job.Route).Provider
		if job.Provider != "" {
			provider = job.Provider
		}
		engine, ok := s.cfg.Engine(provider)
		if !ok {
			return Job{}, errors.New("artifact handoff provider is unavailable; next step was not sent")
		}
		// Artifact contracts are provider-specific. Pin the verified provider so
		// a route fallback cannot succeed without receiving the carried bytes.
		job.Provider = provider
		sourceJobID := base.ID
		if index > 1 {
			sourceJobID = fmt.Sprintf("%s-step-%d", base.ID, index)
		}
		metadata["contextbridge_input_artifact"] = map[string]interface{}{
			"name": artifact.Name, "media_type": artifact.MediaType, "size": artifact.Size,
			"sha256": artifact.SHA256, "source_job_id": sourceJobID, "source": "schedule_previous_step",
		}
		switch engine.Type {
		case "adapter":
			if step.UsePreviousArtifact == "image" {
				job.ImageBase64, job.ImageMediaType = artifact.DataBase64, artifact.MediaType
			} else {
				metadata["contextbridge_input_file"] = map[string]string{
					"name": artifact.Name, "media_type": artifact.MediaType, "data_base64": artifact.DataBase64,
					"size": fmt.Sprint(artifact.Size), "sha256": artifact.SHA256, "source_job_id": sourceJobID,
				}
			}
		case "ollama", "llama_cpp":
			if step.UsePreviousArtifact != "image" {
				return Job{}, errors.New("local model artifact handoff supports verified images only; next step was not sent")
			}
			if engine.Type == "ollama" && !s.cfg.Providers.Ollama.Images {
				return Job{}, errors.New("local Ollama image input is disabled; next step was not sent")
			}
			job.ImageBase64, job.ImageMediaType = artifact.DataBase64, artifact.MediaType
		default:
			return Job{}, errors.New("artifact handoff provider cannot accept verified image bytes; next step was not sent")
		}
	}
	if err := validateJob(job); err != nil {
		return Job{}, fmt.Errorf("next step invalid: %w", err)
	}
	return job, nil
}

func (s *Server) runScheduleSteps(ctx context.Context, scheduleID string, first Job, steps []ScheduleStep) (string, string) {
	var previous Output
	for index := 0; index <= len(steps); index++ {
		job := first
		name := fmt.Sprintf("Step %d", index+1)
		if index > 0 {
			step := steps[index-1]
			if step.Name != "" {
				name = step.Name
			}
			var err error
			job, err = s.nextScheduleStep(first, step, index, previous)
			if err != nil {
				return "failed", err.Error()
			}
			if reason := s.scheduleReadiness(job); reason != "" {
				return "failed", "next step unavailable: " + reason
			}
		}
		entry := ScheduleStepRun{ID: job.ID, Name: name, StartedAt: time.Now().UTC(), Outcome: "running"}
		s.schedules.recordStep(scheduleID, first.ID, entry)
		output, err := s.Process(ctx, job)
		entry.EndedAt = time.Now().UTC()
		if err != nil || output.Error != "" {
			entry.Outcome = "failed"
			if err != nil {
				entry.Error = err.Error()
			} else {
				entry.Error = output.Error
			}
			s.schedules.recordStep(scheduleID, first.ID, entry)
			return "failed", entry.Error
		}
		if output.Decision != nil && output.Decision.Verdict == "review" {
			entry.Outcome = "review"
			entry.Error = strings.Join(output.Decision.Flags, ",")
			s.schedules.recordStep(scheduleID, first.ID, entry)
			return "review", entry.Error
		}
		entry.Outcome = "completed"
		s.schedules.recordStep(scheduleID, first.ID, entry)
		previous = output
	}
	return "completed", ""
}
