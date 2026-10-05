package cluster

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/resourceactivity"
)

// JobResourceActivity deliberately is not a run/subagent tree. Only the relay's
// persisted relationships may create groups; source labels and text may not.
type JobResourceActivity struct {
	Schema         string                         `json:"schema"`
	JobID          string                         `json:"job_id"`
	State          string                         `json:"state"`
	NodeID         string                         `json:"node_id,omitempty"`
	CreatedAt      time.Time                      `json:"created_at"`
	FinishedAt     time.Time                      `json:"finished_at,omitempty"`
	EvidenceStatus string                         `json:"evidence_status"`
	EvidenceSource string                         `json:"evidence_source,omitempty"`
	Resources      []resourceactivity.ResourceUse `json:"resources"`
	Counts         map[string]int                 `json:"counts"`
	Truncated      bool                           `json:"truncated,omitempty"`
	EndpointID     int                            `json:"executed_adapter_endpoint_id,omitempty"`
}

func ProjectJobResourceActivity(job Job) JobResourceActivity {
	view := JobResourceActivity{
		Schema: "contextbridge.job-activity.v1", JobID: job.ID, State: activityJobState(job),
		NodeID: job.AssignedNode, CreatedAt: job.CreatedAt, FinishedAt: job.FinishedAt,
		EvidenceStatus: "not_requested", Resources: []resourceactivity.ResourceUse{}, Counts: map[string]int{},
		EndpointID: job.ExecutedAdapterEndpointID,
	}
	// Even a malformed worker response cannot expose plaintext resource evidence
	// beside a sealed input/result. The relay cannot inspect encrypted contents.
	if job.SealedPayload != nil || job.SealedResult != nil {
		view.EvidenceStatus = "encrypted"
		return view
	}
	var input struct {
		Output struct {
			Activity bool `json:"activity"`
		} `json:"output"`
	}
	if json.Unmarshal(job.Payload, &input) != nil || !input.Output.Activity {
		return view
	}
	view.EvidenceStatus = "pending"
	if job.Status != JobCompleted && job.Status != JobFailed && job.Status != JobCancelled {
		return view
	}
	view.EvidenceStatus = "not_reported"
	var result struct {
		Output *struct {
			Provider       string          `json:"provider"`
			Error          string          `json:"error"`
			Activity       json.RawMessage `json:"activity"`
			ActivityStatus string          `json:"activity_status"`
		} `json:"output"`
	}
	if json.Unmarshal(job.Result, &result) != nil || result.Output == nil || result.Output.Error != "" {
		return view
	}
	output := result.Output
	// Model text, input image counts, a filename inventory and artifact extensions
	// are not evidence that the model opened/read/saw a resource.
	if output.Provider != "adapter" || job.Requirements.Provider != "adapter" {
		return view
	}
	if len(output.Activity) == 0 {
		if output.ActivityStatus == "invalid" {
			view.EvidenceStatus = "invalid"
		}
		return view
	}
	activity, err := resourceactivity.DecodeResourceActivity(output.Activity)
	if err != nil {
		view.EvidenceStatus = "invalid"
		return view
	}
	view.EvidenceStatus, view.EvidenceSource = "reported", "adapter_reported"
	view.Resources, view.Truncated = activity.Items, activity.Truncated
	for _, item := range activity.Items {
		view.Counts[item.Kind+"."+item.Action]++
	}
	return view
}

func (r *Relay) handleJobActivity(w http.ResponseWriter, req *http.Request) {
	job, err := r.visibleJob(req.Context(), req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("job not found"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, ProjectJobResourceActivity(job))
}

func jobResourceActivitySchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object", "additionalProperties": false,
		"required": []string{"schema", "job_id", "state", "created_at", "evidence_status", "resources", "counts"},
		"properties": map[string]interface{}{
			"schema": map[string]interface{}{"type": "string", "const": "contextbridge.job-activity.v1"},
			"job_id": map[string]string{"type": "string"}, "state": map[string]string{"type": "string"},
			"node_id":                      map[string]string{"type": "string"},
			"created_at":                   map[string]string{"type": "string", "format": "date-time"},
			"finished_at":                  map[string]string{"type": "string", "format": "date-time"},
			"executed_adapter_endpoint_id": map[string]string{"type": "integer"},
			"evidence_status":              map[string]interface{}{"type": "string", "enum": []string{"not_requested", "pending", "not_reported", "invalid", "encrypted", "reported"}},
			"evidence_source":              map[string]interface{}{"type": "string", "const": "adapter_reported"},
			"truncated":                    map[string]string{"type": "boolean"},
			"counts":                       map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "integer", "minimum": 0, "maximum": resourceactivity.MaximumActivityItems}},
			"resources": map[string]interface{}{
				"type": "array", "maxItems": resourceactivity.MaximumActivityItems,
				"description": "Untrusted adapter-reported evidence, not independently verified reads. Labels are plain text, refs are not download capabilities. No automatic URL fetching.",
				"items": map[string]interface{}{
					"type": "object", "additionalProperties": false, "required": []string{"id", "kind", "action", "label"},
					"properties": map[string]interface{}{
						"id":     map[string]interface{}{"type": "string", "pattern": `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`},
						"kind":   map[string]interface{}{"type": "string", "enum": []string{"file", "image", "web", "artifact", "tool"}},
						"action": map[string]interface{}{"type": "string", "enum": []string{"read", "inspected", "cited", "created", "updated", "reused", "executed"}},
						"label":  map[string]interface{}{"type": "string", "maxLength": 256, "description": "At most 256 UTF-8 bytes; non-web labels cannot be paths."},
						"ref":    map[string]interface{}{"type": "string", "pattern": `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`},
						"url":    map[string]interface{}{"type": "string", "maxLength": 2048, "description": "Only web items: HTTP(S), no userinfo, query or fragment. Never fetched by Core."},
						"sha256": map[string]interface{}{"type": "string", "pattern": `^[a-f0-9]{64}$`},
					},
				},
			},
		},
	}
}
