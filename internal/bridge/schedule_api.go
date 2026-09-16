package bridge

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{"schedules": s.schedules.list()})
	case http.MethodPost:
		var input struct {
			Name     string           `json:"name"`
			Job      Job              `json:"job"`
			Timing   ScheduleTiming   `json:"timing"`
			Fallback ScheduleFallback `json:"fallback"`
			Steps    []ScheduleStep   `json:"steps"`
			Enabled  *bool            `json:"enabled"`
		}
		if err := decodeJSON(r.Body, &input, 12<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if len(input.Name) > 100 || strings.TrimSpace(input.Name) == "" || strings.IndexFunc(input.Name, func(c rune) bool { return c < 32 }) >= 0 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "name is required, up to 100 bytes, without controls"})
			return
		}
		if input.Job.ID != "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "scheduled job.id must be empty; each run gets a unique ID"})
			return
		}
		for _, alternatives := range [][]string{input.Fallback.Models, input.Fallback.Reasoning} {
			if len(alternatives) > 4 {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "at most four explicit alternatives per choice"})
				return
			}
			for _, alternative := range alternatives {
				if strings.TrimSpace(alternative) == "" || len(alternative) > 100 || strings.IndexFunc(alternative, unicode.IsControl) >= 0 {
					writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid model or reasoning fallback"})
					return
				}
			}
		}
		if err := s.validateRoute(input.Job.Route); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		route := s.cfg.Route(input.Job.Route)
		if input.Job.Provider != "" {
			allowed := strings.EqualFold(input.Job.Provider, route.Provider)
			for _, candidate := range route.Fallback {
				allowed = allowed || strings.EqualFold(input.Job.Provider, candidate)
			}
			if !allowed {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "provider is not allowed for this route"})
				return
			}
		}
		if (len(input.Fallback.Models) > 0 && strings.TrimSpace(input.Job.Model) == "") || (len(input.Fallback.Reasoning) > 0 && strings.TrimSpace(input.Job.Reasoning) == "") {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "fallback alternatives require a primary model or reasoning choice"})
			return
		}
		if len(input.Fallback.Models) > 0 || len(input.Fallback.Reasoning) > 0 {
			providers := append([]string{route.Provider}, route.Fallback...)
			if input.Job.Provider != "" {
				providers = []string{input.Job.Provider}
			}
			browserAllowed := false
			for _, provider := range providers {
				if engine, ok := s.cfg.Engine(provider); ok && engine.Type == "browser" {
					browserAllowed = true
				}
			}
			if !browserAllowed {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "browser menu alternatives require a browser provider"})
				return
			}
		}
		if routeTask := strings.TrimSpace(s.cfg.Route(input.Job.Route).Task); routeTask != "" {
			input.Job.Task = routeTask
		}
		applyTaskOutput(&input.Job, s.cfg.Route(input.Job.Route).Task)
		if err := validateJob(input.Job); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		if strings.Contains(input.Job.Prompt, "{{previous.") {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "first step cannot use a previous-result variable"})
			return
		}
		if len(input.Steps) > 4 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "at most four follow-up steps"})
			return
		}
		baseRouteName := input.Job.Route
		if baseRouteName == "" {
			baseRouteName = "default"
		}
		for index := range input.Steps {
			step := &input.Steps[index]
			if step.Job.Route == "" {
				step.Job.Route = input.Job.Route
			}
			stepRouteName := step.Job.Route
			if stepRouteName == "" {
				stepRouteName = "default"
			}
			inheritSelection := stepRouteName == baseRouteName
			if inheritSelection && step.Job.Provider == "" {
				step.Job.Provider = input.Job.Provider
			}
			if inheritSelection && step.Job.Model == "" {
				step.Job.Model = input.Job.Model
			}
			if inheritSelection && step.Job.Reasoning == "" {
				step.Job.Reasoning = input.Job.Reasoning
			}
			if len(step.Name) > 100 || strings.IndexFunc(step.Name, unicode.IsControl) >= 0 || step.Job.ID != "" || step.Job.SessionID != "" {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid follow-up name, id, or session"})
				return
			}
			if step.UsePreviousArtifact != "" && step.UsePreviousArtifact != "image" && step.UsePreviousArtifact != "file" {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "use_previous_artifact must be image or file"})
				return
			}
			if err := s.validateRoute(step.Job.Route); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
				return
			}
			stepRoute := s.cfg.Route(step.Job.Route)
			if routeTask := strings.TrimSpace(stepRoute.Task); routeTask != "" {
				step.Job.Task = routeTask
			}
			applyTaskOutput(&step.Job, stepRoute.Task)
			if step.Job.Provider != "" {
				allowed := strings.EqualFold(step.Job.Provider, stepRoute.Provider)
				for _, candidate := range stepRoute.Fallback {
					allowed = allowed || strings.EqualFold(step.Job.Provider, candidate)
				}
				if !allowed {
					writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "follow-up provider is not allowed for its route"})
					return
				}
			}
			if step.UsePreviousArtifact != "" {
				previousOutput := input.Job.Output
				if index > 0 {
					previousOutput = input.Steps[index-1].Job.Output
				}
				if !previousOutput.Artifacts {
					writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "previous step must request output.artifacts for artifact handoff"})
					return
				}
				provider := stepRoute.Provider
				if step.Job.Provider != "" {
					provider = step.Job.Provider
				}
				engine, ok := s.cfg.Engine(provider)
				if !ok {
					writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "artifact handoff provider is unavailable"})
					return
				}
				if engine.Type != "browser" && (step.UsePreviousArtifact != "image" || (engine.Type != "ollama" && engine.Type != "llama_cpp")) {
					writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "local model artifact handoff supports verified images only"})
					return
				}
				if engine.Type == "ollama" && !s.cfg.Providers.Ollama.Images {
					writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "local Ollama image input is disabled"})
					return
				}
				// Persist the provider binding for an artifact-carrying step. A
				// fallback with a different upload contract must not report success
				// after silently dropping the verified bytes.
				step.Job.Provider = provider
			}
			if err := validateJob(step.Job); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "follow-up: " + err.Error()})
				return
			}
		}
		now := time.Now().UTC()
		input.Timing.Type = strings.ToLower(strings.TrimSpace(input.Timing.Type))
		if len(input.Timing.Type) > 16 || len(input.Timing.Timezone) > 100 || len(input.Timing.Time) > 5 || len(input.Timing.Cron) > 200 || len(input.Timing.Days) > 7 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "timing fields exceed their limits"})
			return
		}
		if input.Timing.Timezone == "" {
			input.Timing.Timezone = "Local"
		}
		if input.Timing.Type == "at" && !input.Timing.At.After(now) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "timing.at must be in the future"})
			return
		}
		next, err := input.Timing.next(now, now)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		var id Job
		prepareJob(&id)
		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		item := Schedule{ID: id.ID, Name: strings.TrimSpace(input.Name), Job: input.Job, Timing: input.Timing, Fallback: input.Fallback, Steps: input.Steps, Enabled: enabled, NextRun: next, CreatedAt: now, UpdatedAt: now}
		item, err = s.schedules.add(item)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "schedule could not be saved"})
			return
		}
		writeJSON(w, http.StatusCreated, item)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST required"})
	}
}

func (s *Server) handleScheduleAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/schedules/"), "/"), "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" || !jobIDPattern.MatchString(parts[0]) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "schedule not found"})
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			item, ok := s.schedules.get(id)
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "schedule not found"})
				return
			}
			writeJSON(w, http.StatusOK, item)
		case http.MethodDelete:
			err := s.schedules.remove(id)
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "schedule not found"})
				return
			}
			if err != nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or DELETE required"})
		}
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	switch parts[1] {
	case "pause", "resume":
		item, err := s.schedules.setEnabled(id, parts[1] == "resume")
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "schedule not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, item)
	case "run":
		item, ok := s.schedules.get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "schedule not found"})
			return
		}
		if reason := s.scheduleReadiness(item.Job); reason != "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": reason})
			return
		}
		_, job, err := s.claimSchedule(id, time.Now().UTC(), true)
		if errors.Is(err, errServiceStopping) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": errServiceStopping.Error()})
			return
		}
		if errors.Is(err, errScheduleCapacity) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "capacity unavailable"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			s.executeSchedule(ctx, id, job)
		}()
		writeJSON(w, http.StatusAccepted, map[string]interface{}{"run_id": job.ID, "status": "accepted"})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "schedule action not found"})
	}
}
