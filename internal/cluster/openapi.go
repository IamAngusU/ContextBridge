package cluster

import "net/http"

func (r *Relay) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, relayOpenAPI())
}

func relayOpenAPI() map[string]interface{} {
	jsonResponse := func(description, schema string) map[string]interface{} {
		response := map[string]interface{}{"description": description}
		if schema != "" {
			response["content"] = map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"$ref": "#/components/schemas/" + schema}}}
		}
		return response
	}
	operation := func(summary string, roles []string, responseSchema string, successStatus ...string) map[string]interface{} {
		status := "200"
		if len(successStatus) > 0 {
			status = successStatus[0]
		}
		return map[string]interface{}{
			"summary":               summary,
			"x-contextbridge-roles": roles,
			"responses": map[string]interface{}{
				status: jsonResponse("Success", responseSchema),
				"400":  jsonResponse("Invalid request", "Error"),
				"401":  jsonResponse("Invalid credential", "Error"),
				"403":  jsonResponse("Credential scope forbids this operation", "Error"),
				"404":  jsonResponse("Resource not found or not visible", "Error"),
				"409":  jsonResponse("Resource state conflict", "Error"),
				"422":  jsonResponse("Semantically invalid request", "Error"),
				"429":  jsonResponse("Bounded capacity or rate limit reached", "Error"),
				"503":  jsonResponse("Relay temporarily unavailable", "Error"),
			},
		}
	}
	streamOperation := func(summary string) map[string]interface{} {
		result := operation(summary, []string{"admin", "observer", "producer"}, "Object")
		result["responses"].(map[string]interface{})["200"] = map[string]interface{}{
			"description": "Authoritative SSE stream",
			"headers":     map[string]interface{}{"X-ContextBridge-Event-Stream": map[string]interface{}{"schema": map[string]interface{}{"type": "string", "const": "authoritative-events-v1"}}},
			"content":     map[string]interface{}{"text/event-stream": map[string]interface{}{"schema": map[string]string{"type": "string"}}},
		}
		return result
	}
	pathItem := func(parameter string, operations map[string]interface{}) map[string]interface{} {
		if parameter != "" {
			operations["parameters"] = []map[string]interface{}{{"name": parameter, "in": "path", "required": true, "schema": map[string]string{"type": "string"}}}
		}
		return operations
	}
	jobHistoryOperation := operation("List job history; add page=1 for cursor pagination", []string{"admin", "observer", "producer"}, "JobHistoryResponse")
	jobHistoryOperation["parameters"] = []map[string]interface{}{
		{"name": "page", "in": "query", "schema": map[string]interface{}{"type": "string", "enum": []string{"1"}}},
		{"name": "limit", "in": "query", "schema": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": maximumJobHistoryPage, "default": 100}},
		{"name": "status", "in": "query", "schema": map[string]interface{}{"type": "string", "enum": []string{JobReserved, JobQueued, JobAssigned, JobRunning, JobCompleted, JobFailed, JobCancelled}}},
		{"name": "owner_subject", "in": "query", "schema": map[string]interface{}{"type": "string", "maxLength": 120}},
		{"name": "tenant_id", "in": "query", "schema": map[string]interface{}{"type": "string", "maxLength": 200}},
		{"name": "cursor", "in": "query", "schema": map[string]interface{}{"type": "string", "maxLength": 2048}},
	}
	jobSubmitOperation := operation("Submit a job", []string{"admin", "producer"}, "Object", "202")
	jobSubmitOperation["responses"].(map[string]interface{})["200"] = jsonResponse("Idempotent replay", "Object")
	jobSubmitOperation["requestBody"] = map[string]interface{}{"required": true, "content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]string{"$ref": "#/components/schemas/SubmitRequest"}}}}
	assignmentOperation := operation("Reserve an E2EE worker assignment", []string{"admin", "producer"}, "AssignmentResponse", "201")
	assignmentOperation["requestBody"] = map[string]interface{}{"required": true, "content": map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]interface{}{"type": "object", "required": []string{"requirements"}, "properties": map[string]interface{}{"tenant_id": map[string]string{"type": "string"}, "pool_id": map[string]string{"type": "string"}, "pool_authority_public_key": map[string]string{"type": "string"}, "requirements": map[string]string{"type": "object"}}}}}}
	tokenCreationOperation := operation("Create a scoped credential; bearer is returned once", []string{"admin"}, "TokenCreation", "201")
	tokenCreationOperation["requestBody"] = map[string]interface{}{
		"required": true,
		"content":  map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]string{"$ref": "#/components/schemas/TokenCreateRequest"}}},
	}
	scheduledPreviewOperation := operation("Preview a credential-scoped scheduled adapter action", []string{"producer"}, "ScheduledAction", "201")
	scheduledPreviewOperation["requestBody"] = map[string]interface{}{
		"required": true,
		"content":  map[string]interface{}{"application/json": map[string]interface{}{"schema": map[string]string{"$ref": "#/components/schemas/ScheduledActionRequest"}}},
	}
	scheduledListOperation := operation("List scheduled adapter actions created by this exact credential", []string{"producer"}, "ScheduledActionList")
	scheduledListOperation["parameters"] = []map[string]interface{}{
		{"name": "status", "in": "query", "schema": map[string]interface{}{"type": "string", "enum": []string{ScheduledActionPreview, ScheduledActionActive, ScheduledActionCompleted, ScheduledActionFailed, ScheduledActionUnknown, ScheduledActionCancelled, ScheduledActionExpired}}},
		{"name": "limit", "in": "query", "schema": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": maximumScheduledActionList, "default": maximumScheduledActionList}},
	}
	paths := map[string]interface{}{
		"/v1/cluster/openapi.json": map[string]interface{}{"get": operation("Get this OpenAPI contract", []string{"admin", "observer", "producer", "node"}, "OpenAPI")},
		"/v1/cluster/protocol":     map[string]interface{}{"get": operation("Get protocol capabilities and limits", []string{"admin", "observer", "producer", "node"}, "Object")},
		"/v1/cluster/whoami":       map[string]interface{}{"get": operation("Get the authenticated credential identity and scope", []string{"admin", "observer", "producer", "node"}, "Identity")},
		"/v1/cluster/overview":     map[string]interface{}{"get": operation("Get a content-minimized cluster overview", []string{"admin", "observer", "producer"}, "Object")},
		"/v1/cluster/nodes":        map[string]interface{}{"get": operation("List worker nodes", []string{"admin", "observer", "producer"}, "Array")},
		"/v1/cluster/events":       map[string]interface{}{"get": operation("List global operator events; unavailable to scoped observers", []string{"admin", "observer"}, "Array")},
		"/v1/cluster/jobs": map[string]interface{}{
			"get":  jobHistoryOperation,
			"post": jobSubmitOperation,
		},
		"/v1/cluster/jobs/{id}": pathItem("id", map[string]interface{}{
			"get":    operation("Get one visible job", []string{"admin", "observer", "producer"}, "Object"),
			"delete": operation("Cancel one owned job", []string{"admin", "producer"}, "Object"),
		}),
		"/v1/cluster/jobs/{id}/events":        pathItem("id", map[string]interface{}{"get": operation("Get authoritative job events", []string{"admin", "observer", "producer"}, "Object")}),
		"/v1/cluster/jobs/{id}/events/stream": pathItem("id", map[string]interface{}{"get": streamOperation("Stream authoritative job events as SSE")}),
		"/v1/cluster/jobs/{id}/route":         pathItem("id", map[string]interface{}{"get": operation("Get the durable routing decision", []string{"admin", "observer", "producer"}, "Object")}),
		"/v1/cluster/jobs/{id}/estimate":      pathItem("id", map[string]interface{}{"get": operation("Get a historical runtime estimate", []string{"admin", "observer", "producer"}, "Object")}),
		"/v1/cluster/contracts/validate":      map[string]interface{}{"post": operation("Validate a job contract without admission", []string{"admin", "producer"}, "Object")},
		"/v1/cluster/assign":                  map[string]interface{}{"post": assignmentOperation},
		"/v1/cluster/routes/explain":          map[string]interface{}{"post": operation("Explain placement without admission", []string{"admin", "producer"}, "Object")},
		"/v1/cluster/adapters":                map[string]interface{}{"get": operation("List visible leased external adapters", []string{"admin", "observer", "producer"}, "AdapterPresenceList")},
		"/v1/cluster/adapters/heartbeat":      map[string]interface{}{"post": operation("Register or renew an owned external adapter lease", []string{"producer"}, "Object")},
		"/v1/cluster/adapters/{id}":           pathItem("id", map[string]interface{}{"get": operation("Inspect one visible leased external adapter", []string{"admin", "observer", "producer"}, "AdapterPresenceList")}),
		"/v1/cluster/adapters/{id}/{action}": map[string]interface{}{
			"parameters": []map[string]interface{}{
				{"name": "id", "in": "path", "required": true, "schema": map[string]string{"type": "string"}},
				{"name": "action", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string", "enum": []string{"enable", "disable"}}},
			},
			"post": operation("Enable or disable one external adapter", []string{"admin"}, "Object"),
		},
		"/v1/cluster/scheduled-actions/preview": map[string]interface{}{"post": scheduledPreviewOperation},
		"/v1/cluster/scheduled-actions":         map[string]interface{}{"get": scheduledListOperation},
		"/v1/cluster/scheduled-actions/{id}": pathItem("id", map[string]interface{}{
			"get":    operation("Get one scheduled adapter action created by this exact credential", []string{"producer"}, "ScheduledAction"),
			"delete": operation("Cancel one scheduled adapter action created by this exact credential", []string{"producer"}, "ScheduledAction"),
		}),
		"/v1/cluster/scheduled-actions/{id}/confirm": pathItem("id", map[string]interface{}{
			"post": operation("Confirm an unexpired scheduled adapter action preview", []string{"producer"}, "ScheduledAction"),
		}),
		"/v1/cluster/tokens": map[string]interface{}{
			"get":  operation("List credential metadata without bearer values", []string{"admin"}, "Object"),
			"post": tokenCreationOperation,
		},
		"/v1/cluster/tokens/{id}":                      pathItem("id", map[string]interface{}{"delete": operation("Revoke a credential", []string{"admin"}, "Object")}),
		"/v1/cluster/pipelines":                        map[string]interface{}{"get": operation("List configured pipeline contracts", []string{"admin", "observer", "producer"}, "Object")},
		"/v1/cluster/pipelines/{name}/run":             pathItem("name", map[string]interface{}{"post": operation("Start a pipeline run", []string{"admin", "producer"}, "Object", "202")}),
		"/v1/cluster/pipeline-runs/{id}":               pathItem("id", map[string]interface{}{"get": operation("Get one visible pipeline run", []string{"admin", "observer", "producer"}, "Object")}),
		"/v1/cluster/pipeline-runs/{id}/activity":      pathItem("id", map[string]interface{}{"get": operation("Get content-minimized pipeline activity", []string{"admin", "observer", "producer"}, "Object")}),
		"/v1/cluster/pipeline-runs/{id}/events":        pathItem("id", map[string]interface{}{"get": operation("Get authoritative pipeline events", []string{"admin", "observer", "producer"}, "Object")}),
		"/v1/cluster/pipeline-runs/{id}/events/stream": pathItem("id", map[string]interface{}{"get": streamOperation("Stream authoritative pipeline events as SSE")}),
	}
	return map[string]interface{}{
		"openapi":  "3.1.0",
		"info":     map[string]interface{}{"title": "ContextBridge Relay API", "version": "1.0.0", "description": "Authenticated API for backends and operator UIs. Browser applications should keep bearer credentials in their backend-for-frontend."},
		"servers":  []map[string]string{{"url": "/"}},
		"security": []map[string][]string{{"bearerAuth": {}}},
		"paths":    paths,
		"components": map[string]interface{}{
			"securitySchemes": map[string]interface{}{"bearerAuth": map[string]string{"type": "http", "scheme": "bearer"}},
			"schemas": map[string]interface{}{
				"OpenAPI": map[string]string{"type": "object"},
				"Object":  map[string]string{"type": "object"},
				"Array":   map[string]interface{}{"type": "array", "items": map[string]string{"type": "object"}},
				"AdapterPresenceList": map[string]interface{}{
					"type": "object", "required": []string{"schema", "adapters", "total", "available", "setup_required", "degraded", "disabled"},
					"properties": map[string]interface{}{
						"schema":   map[string]interface{}{"type": "string", "const": AdapterPresenceListV1},
						"adapters": map[string]interface{}{"type": "array", "items": map[string]string{"type": "object"}},
						"total":    map[string]string{"type": "integer"}, "available": map[string]string{"type": "integer"},
						"setup_required": map[string]string{"type": "integer"}, "degraded": map[string]string{"type": "integer"}, "disabled": map[string]string{"type": "integer"},
					},
				},
				"Error": map[string]interface{}{
					"type": "object", "required": []string{"schema", "code", "message"},
					"properties": map[string]interface{}{
						"schema": map[string]interface{}{"type": "string", "const": "contextbridge.error.v1"}, "code": map[string]string{"type": "string"},
						"message": map[string]string{"type": "string"}, "error": map[string]string{"type": "string"}, "request_id": map[string]string{"type": "string"},
					},
				},
				"Identity": map[string]interface{}{
					"type": "object", "required": []string{"schema", "id", "role", "subject", "permissions"},
					"properties": map[string]interface{}{"schema": map[string]interface{}{"type": "string", "const": "contextbridge.identity.v1"}, "id": map[string]string{"type": "string"}, "role": map[string]interface{}{"type": "string", "enum": []string{"admin", "producer", "node", "observer"}}, "subject": map[string]string{"type": "string"}, "permissions": map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}}, "producer_limits": map[string]string{"type": "object"}, "observer_limits": map[string]string{"type": "object"}},
				},
				"JobHistoryPage": map[string]interface{}{
					"type": "object", "required": []string{"schema", "jobs", "has_more", "scanned"},
					"properties": map[string]interface{}{"schema": map[string]interface{}{"type": "string", "const": "contextbridge.job-history-page.v1"}, "jobs": map[string]interface{}{"type": "array", "items": map[string]string{"type": "object"}}, "next_cursor": map[string]string{"type": "string"}, "has_more": map[string]string{"type": "boolean"}, "scanned": map[string]string{"type": "integer"}},
				},
				"JobHistoryResponse": map[string]interface{}{"oneOf": []map[string]string{{"$ref": "#/components/schemas/Array"}, {"$ref": "#/components/schemas/JobHistoryPage"}}},
				"PoolWorkerCertificate": map[string]interface{}{
					"type": "object", "additionalProperties": false, "required": []string{"contract_version", "pool_id", "authority_public_key", "worker_public_key", "issued_at", "signature"},
					"properties": map[string]interface{}{"contract_version": map[string]interface{}{"type": "string", "const": PoolWorkerCertificateV1}, "pool_id": map[string]string{"type": "string"}, "authority_public_key": map[string]string{"type": "string"}, "worker_public_key": map[string]string{"type": "string"}, "issued_at": map[string]interface{}{"type": "string", "format": "date-time"}, "signature": map[string]string{"type": "string"}},
				},
				"PoolJobAuthorization": map[string]interface{}{
					"type": "object", "additionalProperties": false, "required": []string{"contract_version", "pool_id", "expires_at", "signature"},
					"properties": map[string]interface{}{"contract_version": map[string]interface{}{"type": "string", "const": PoolJobAuthorizationV1}, "pool_id": map[string]string{"type": "string"}, "expires_at": map[string]interface{}{"type": "string", "format": "date-time"}, "signature": map[string]string{"type": "string"}},
				},
				"AssignmentResponse": map[string]interface{}{
					"type": "object", "required": []string{"assignment", "assignment_secret"},
					"properties": map[string]interface{}{"assignment": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"id": map[string]string{"type": "string"}, "job_id": map[string]string{"type": "string"}, "node_id": map[string]string{"type": "string"}, "public_key": map[string]string{"type": "string"}, "pool_certificate": map[string]string{"$ref": "#/components/schemas/PoolWorkerCertificate"}}}, "assignment_secret": map[string]interface{}{"type": "string", "writeOnly": true}},
				},
				"SubmitRequest": map[string]interface{}{
					"type": "object", "properties": map[string]interface{}{"id": map[string]string{"type": "string"}, "tenant_id": map[string]string{"type": "string"}, "source": map[string]string{"type": "string"}, "requirements": map[string]string{"type": "object"}, "payload": map[string]interface{}{}, "sealed_payload": map[string]string{"type": "object"}, "pool_authorization": map[string]string{"$ref": "#/components/schemas/PoolJobAuthorization"}, "assignment_id": map[string]string{"type": "string"}, "assignment_secret": map[string]interface{}{"type": "string", "writeOnly": true}, "priority": map[string]interface{}{"type": "integer", "minimum": -100, "maximum": 100}, "max_attempts": map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 10}},
				},
				"ScheduledActionTarget": map[string]interface{}{
					"type": "object", "additionalProperties": false, "required": []string{"adapter_uid", "adapter_profile", "adapter_principal", "action_kinds", "destination_refs"},
					"properties": map[string]interface{}{
						"adapter_uid":       map[string]interface{}{"type": "string", "pattern": "^adp_[0-9a-f]{32}$"},
						"adapter_profile":   map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 80},
						"adapter_principal": map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 80},
						"action_kinds":      map[string]interface{}{"type": "array", "minItems": 1, "maxItems": maximumScheduledActionKindsPerTarget, "uniqueItems": true, "items": map[string]interface{}{"type": "string", "pattern": "^[a-z0-9][a-z0-9._:-]{0,79}$"}},
						"destination_refs":  map[string]interface{}{"type": "array", "minItems": 1, "maxItems": maximumScheduledDestinationsPerTarget, "uniqueItems": true, "items": map[string]interface{}{"type": "string", "pattern": "^dst_[0-9a-f]{32}$"}},
					},
				},
				"ScheduledActionLimits": map[string]interface{}{
					"type": "object", "additionalProperties": false, "required": []string{"schema", "targets"},
					"properties": map[string]interface{}{
						"schema":                      map[string]interface{}{"type": "string", "const": ScheduledActionPolicyV1},
						"targets":                     map[string]interface{}{"type": "array", "minItems": 1, "maxItems": maximumScheduledActionTargets, "items": map[string]string{"$ref": "#/components/schemas/ScheduledActionTarget"}},
						"max_active":                  map[string]interface{}{"type": "integer", "minimum": 0, "maximum": maximumScheduledMaxActive},
						"max_horizon_seconds":         map[string]interface{}{"type": "integer", "minimum": 0, "maximum": maximumScheduledHorizonSeconds},
						"min_interval_seconds":        map[string]interface{}{"anyOf": []map[string]interface{}{{"const": 0}, {"type": "integer", "minimum": minimumScheduledIntervalSeconds, "maximum": maximumScheduledHorizonSeconds}}},
						"max_occurrences":             map[string]interface{}{"type": "integer", "minimum": 0, "maximum": maximumScheduledOccurrences},
						"max_delivery_window_seconds": map[string]interface{}{"anyOf": []map[string]interface{}{{"const": 0}, {"type": "integer", "minimum": minimumScheduledDeliveryWindowSeconds, "maximum": maximumScheduledDeliveryWindowSeconds}}},
					},
				},
				"ScheduledActionRequest": map[string]interface{}{
					"type": "object", "additionalProperties": false,
					"required": []string{"schema", "adapter_uid", "action_kind", "destination_ref", "payload_ref", "start_at", "timezone"},
					"properties": map[string]interface{}{
						"schema":                  map[string]interface{}{"type": "string", "const": ScheduledActionRequestV1},
						"tenant_id":               map[string]interface{}{"type": "string", "maxLength": 200},
						"adapter_uid":             map[string]interface{}{"type": "string", "pattern": "^adp_[0-9a-f]{32}$"},
						"action_kind":             map[string]interface{}{"type": "string", "pattern": "^[a-z0-9][a-z0-9._:-]{0,79}$"},
						"destination_ref":         map[string]interface{}{"type": "string", "pattern": "^dst_[0-9a-f]{32}$"},
						"payload_ref":             map[string]interface{}{"type": "string", "pattern": "^ref_[0-9a-f]{32}$"},
						"start_at":                map[string]interface{}{"type": "string", "format": "date-time"},
						"timezone":                map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 80},
						"repeat_every_seconds":    map[string]interface{}{"anyOf": []map[string]interface{}{{"const": 0}, {"type": "integer", "minimum": minimumScheduledIntervalSeconds, "maximum": maximumScheduledHorizonSeconds}}},
						"occurrences":             map[string]interface{}{"type": "integer", "minimum": 0, "maximum": maximumScheduledOccurrences},
						"delivery_window_seconds": map[string]interface{}{"anyOf": []map[string]interface{}{{"const": 0}, {"type": "integer", "minimum": minimumScheduledDeliveryWindowSeconds, "maximum": maximumScheduledDeliveryWindowSeconds}}},
						"priority":                map[string]interface{}{"type": "integer", "minimum": -100, "maximum": 100},
					},
				},
				"ScheduledAction": map[string]interface{}{
					"type": "object", "description": "Content-minimized durable action. The execution principal and credential hash are never returned.",
					"required": []string{"schema", "id", "status", "owner_subject", "credential_id", "adapter_uid", "adapter_profile", "action_kind", "destination_ref", "payload_ref", "start_at", "local_start", "timezone", "occurrences", "delivery_window_seconds", "completed_occurrences", "skipped_occurrences", "created_at", "updated_at"},
					"properties": map[string]interface{}{
						"schema": map[string]interface{}{"type": "string", "const": ScheduledActionV1}, "id": map[string]string{"type": "string"},
						"status":        map[string]interface{}{"type": "string", "enum": []string{ScheduledActionPreview, ScheduledActionActive, ScheduledActionCompleted, ScheduledActionFailed, ScheduledActionUnknown, ScheduledActionCancelled, ScheduledActionExpired}},
						"owner_subject": map[string]string{"type": "string"}, "tenant_id": map[string]string{"type": "string"}, "credential_id": map[string]string{"type": "string"},
						"adapter_uid": map[string]string{"type": "string"}, "adapter_profile": map[string]string{"type": "string"}, "action_kind": map[string]string{"type": "string"},
						"destination_ref": map[string]string{"type": "string"}, "payload_ref": map[string]string{"type": "string"}, "start_at": map[string]interface{}{"type": "string", "format": "date-time"},
						"local_start": map[string]string{"type": "string"}, "timezone": map[string]string{"type": "string"}, "repeat_every_seconds": map[string]string{"type": "integer"},
						"occurrences": map[string]string{"type": "integer"}, "delivery_window_seconds": map[string]string{"type": "integer"}, "priority": map[string]string{"type": "integer"},
						"completed_occurrences": map[string]string{"type": "integer"}, "skipped_occurrences": map[string]string{"type": "integer"}, "current_occurrence": map[string]string{"type": "integer"},
						"current_due_at": map[string]interface{}{"type": "string", "format": "date-time"}, "current_expires_at": map[string]interface{}{"type": "string", "format": "date-time"},
						"current_job_id": map[string]string{"type": "string"}, "last_job_id": map[string]string{"type": "string"}, "last_outcome": map[string]string{"type": "string"},
						"failure_code": map[string]string{"type": "string"}, "waiting_reason": map[string]string{"type": "string"}, "next_run_at": map[string]interface{}{"type": "string", "format": "date-time"},
						"next_check_at": map[string]interface{}{"type": "string", "format": "date-time"}, "preview_expires_at": map[string]interface{}{"type": "string", "format": "date-time"},
						"confirmed_at": map[string]interface{}{"type": "string", "format": "date-time"}, "created_at": map[string]interface{}{"type": "string", "format": "date-time"}, "updated_at": map[string]interface{}{"type": "string", "format": "date-time"},
					},
				},
				"ScheduledActionList": map[string]interface{}{
					"type": "object", "required": []string{"schema", "actions", "total"},
					"properties": map[string]interface{}{"schema": map[string]interface{}{"type": "string", "const": ScheduledActionListV1}, "actions": map[string]interface{}{"type": "array", "items": map[string]string{"$ref": "#/components/schemas/ScheduledAction"}}, "total": map[string]string{"type": "integer"}},
				},
				"ObserverLimits": map[string]interface{}{
					"type": "object", "additionalProperties": false,
					"properties": map[string]interface{}{
						"allowed_subjects": map[string]interface{}{"type": "array", "maxItems": 32, "uniqueItems": true, "items": map[string]interface{}{"type": "string", "maxLength": 120}},
						"allowed_tenants":  map[string]interface{}{"type": "array", "maxItems": 32, "uniqueItems": true, "items": map[string]interface{}{"type": "string", "maxLength": 200}},
					},
				},
				"ProducerLimits": map[string]interface{}{
					"type": "object", "additionalProperties": false,
					"properties": map[string]interface{}{
						"max_queued_jobs":   map[string]interface{}{"type": "integer", "minimum": 0, "maximum": maxQueuedJobsPerOwner},
						"max_jobs_per_hour": map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 1000000},
						"max_priority":      map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 100, "description": "Maximum queue priority this producer may select; omission preserves the historical maximum of 100."},
						"providers":         map[string]interface{}{"type": "array", "maxItems": 32, "uniqueItems": true, "items": map[string]interface{}{"type": "string", "maxLength": 80}},
						"allowed_tenants":   map[string]interface{}{"type": "array", "maxItems": 32, "uniqueItems": true, "items": map[string]interface{}{"type": "string", "maxLength": 200}},
						"egress":            map[string]interface{}{"type": "string", "enum": []string{"", "local_only"}},
						"require_e2ee":      map[string]string{"type": "boolean"},
						"scheduled_actions": map[string]string{"$ref": "#/components/schemas/ScheduledActionLimits"},
					},
				},
				"TokenCreateRequest": map[string]interface{}{
					"type": "object", "additionalProperties": false, "required": []string{"role", "subject"},
					"properties": map[string]interface{}{
						"role":            map[string]interface{}{"type": "string", "enum": []string{"admin", "producer", "node", "observer"}},
						"subject":         map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 120},
						"groups":          map[string]interface{}{"type": "array", "maxItems": 32, "uniqueItems": true, "items": map[string]interface{}{"type": "string", "maxLength": 80}},
						"lifetime_hours":  map[string]interface{}{"type": "integer", "minimum": 0, "maximum": 87600},
						"producer_limits": map[string]string{"$ref": "#/components/schemas/ProducerLimits"},
						"observer_limits": map[string]string{"$ref": "#/components/schemas/ObserverLimits"},
					},
				},
				"TokenCreation": map[string]interface{}{
					"type": "object", "description": "The bearer token is returned only by this successful creation response.", "required": []string{"token", "record"},
					"properties": map[string]interface{}{"token": map[string]interface{}{"type": "string", "writeOnly": true}, "record": map[string]string{"type": "object"}},
				},
			},
		},
	}
}
