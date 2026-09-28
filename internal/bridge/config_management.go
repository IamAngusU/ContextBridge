package bridge

import (
	"errors"
	"net/http"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

const maximumManagedConfigRequestBytes int64 = 9 << 20

type managedConfigRequest struct {
	YAML         string `json:"yaml"`
	BaseRevision string `json:"base_revision"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" || s.configPath == "." {
		writeManagementError(w, http.StatusServiceUnavailable, "config.management_unavailable", "config management is unavailable for this server")
		return
	}
	switch r.Method {
	case http.MethodGet:
		managed, err := config.ReadManagedConfig(s.configPath)
		if err != nil {
			writeManagementError(w, http.StatusInternalServerError, "config.read_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, managed)
	case http.MethodPost, http.MethodPut:
		var input managedConfigRequest
		if err := decodeJSON(r.Body, &input, maximumManagedConfigRequestBytes); err != nil {
			writeManagementError(w, http.StatusBadRequest, "request.invalid_json", err.Error())
			return
		}
		if strings.TrimSpace(input.BaseRevision) == "" {
			writeManagementError(w, http.StatusBadRequest, "config.base_revision_required", "base_revision is required")
			return
		}
		var result config.ManagedValidation
		var err error
		if r.Method == http.MethodPost {
			result, err = config.ValidateManagedConfig(s.configPath, []byte(input.YAML), input.BaseRevision)
		} else {
			result, err = config.ApplyManagedConfig(s.configPath, []byte(input.YAML), input.BaseRevision)
		}
		if err != nil {
			status, code := http.StatusUnprocessableEntity, "config.invalid"
			if errors.Is(err, config.ErrManagedConfigConflict) {
				status, code = http.StatusConflict, "config.revision_conflict"
			}
			writeManagementError(w, status, code, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	default:
		w.Header().Set("Allow", "GET, POST, PUT")
		writeManagementError(w, http.StatusMethodNotAllowed, "request.method_not_allowed", "GET, POST, or PUT required")
	}
}

func (s *Server) handleConfigSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeManagementError(w, http.StatusMethodNotAllowed, "request.method_not_allowed", "GET required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema":           "contextbridge.config-management-schema.v1",
		"config_version":   1,
		"format":           "yaml",
		"maximum_bytes":    4 << 20,
		"secret_marker":    config.ManagedSecretMarker,
		"read":             map[string]string{"method": "GET", "path": "/v1/config"},
		"validate":         map[string]string{"method": "POST", "path": "/v1/config"},
		"apply":            map[string]string{"method": "PUT", "path": "/v1/config"},
		"concurrency":      "Send the revision returned by GET as base_revision.",
		"secret_semantics": "An unchanged secret_marker preserves the exact current value or environment reference. New secrets may be submitted but are never returned.",
		"apply_semantics":  "A successful change is written atomically and reports restart_required=true; the running process is not silently reconfigured.",
		"constraints":      config.ManagedConfigConstraints(),
		"cross_field_rules": []string{
			"POST and PUT run the complete startup validator; this constraint catalog is for UI controls, not a replacement for server validation.",
			"Remote provider URLs require HTTPS, remote: true, and resolved credentials; loopback HTTP is allowed.",
			"Image limits require the vision capability, and max_total_image_bytes cannot be smaller than max_image_bytes.",
			"Pipeline and step limits are additionally bounded by the configured cluster policy ceilings.",
			"Every configured pipeline step is validated against the public job requirement bounds and cluster task allowlist before apply.",
			"Scoped adapter mode requires every adapter route profile to be covered by a distinct adapter principal.",
		},
	})
}

func writeManagementError(w http.ResponseWriter, status int, code, message string) {
	response := map[string]string{
		"schema": "contextbridge.error.v1", "code": code, "error": message, "message": message,
	}
	if requestID := w.Header().Get("X-Request-ID"); requestID != "" {
		response["request_id"] = requestID
	}
	writeJSON(w, status, response)
}
