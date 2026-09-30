package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	AdapterPresenceV1               = "contextbridge.adapter-presence.v1"
	AdapterPresenceLeaseV1          = "contextbridge.adapter-presence-lease.v1"
	AdapterPresenceListV1           = "contextbridge.adapter-presence-list.v1"
	minimumAdapterLeaseSeconds      = 15
	maximumAdapterLeaseSeconds      = 300
	defaultAdapterLeaseSeconds      = 60
	maximumAdapterPresences         = 512
	maximumAdapterPresencesPerOwner = 32
)

// AdapterHeartbeat is a provider-neutral liveness claim from an independently
// deployed ingress or control adapter. Every authority-bearing field is added
// by the relay from the authenticated producer credential, never trusted from
// this payload.
type AdapterHeartbeat struct {
	Schema        string   `json:"schema"`
	AdapterID     string   `json:"adapter_id"`
	InstanceID    string   `json:"instance_id"`
	DisplayName   string   `json:"display_name"`
	Kind          string   `json:"kind"`
	Version       string   `json:"version"`
	State         string   `json:"state"`
	Capabilities  []string `json:"capabilities,omitempty"`
	Active        int      `json:"active,omitempty"`
	Capacity      int      `json:"capacity,omitempty"`
	QueueDepth    int      `json:"queue_depth,omitempty"`
	LastErrorCode string   `json:"last_error_code,omitempty"`
	LeaseSeconds  int      `json:"lease_seconds,omitempty"`
}

// AdapterPresence is safe operational metadata. Capabilities are descriptive
// claims only: they never grant a caller permission or execution authority.
type AdapterPresence struct {
	Schema         string    `json:"schema"`
	AdapterUID     string    `json:"adapter_uid"`
	AdapterID      string    `json:"adapter_id"`
	InstanceID     string    `json:"instance_id"`
	DisplayName    string    `json:"display_name"`
	Kind           string    `json:"kind"`
	Version        string    `json:"version"`
	State          string    `json:"state"`
	Capabilities   []string  `json:"capabilities,omitempty"`
	Active         int       `json:"active,omitempty"`
	Capacity       int       `json:"capacity,omitempty"`
	QueueDepth     int       `json:"queue_depth,omitempty"`
	LastErrorCode  string    `json:"last_error_code,omitempty"`
	OwnerSubject   string    `json:"owner_subject"`
	TenantIDs      []string  `json:"tenant_ids,omitempty"`
	Enabled        bool      `json:"enabled"`
	Available      bool      `json:"available"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

type AdapterPresenceLease struct {
	Schema                string    `json:"schema"`
	AdapterUID            string    `json:"adapter_uid"`
	Enabled               bool      `json:"enabled"`
	Available             bool      `json:"available"`
	LeaseExpiresAt        time.Time `json:"lease_expires_at"`
	HeartbeatAfterSeconds int       `json:"heartbeat_after_seconds"`
}

type AdapterPresenceList struct {
	Schema        string            `json:"schema"`
	Adapters      []AdapterPresence `json:"adapters"`
	Total         int               `json:"total"`
	Available     int               `json:"available"`
	SetupRequired int               `json:"setup_required"`
	Degraded      int               `json:"degraded"`
	Disabled      int               `json:"disabled"`
}

func validAdapterUID(value string) bool {
	if len(value) != 36 || !strings.HasPrefix(value, "adp_") {
		return false
	}
	_, err := hex.DecodeString(value[4:])
	return err == nil
}

func validAdapterID(value string, maximum int) bool {
	if value == "" || len(value) > maximum || value != strings.ToLower(value) {
		return false
	}
	for index, character := range []byte(value) {
		alphaNumeric := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if index == 0 && !alphaNumeric {
			return false
		}
		if index > 0 && !alphaNumeric && character != '.' && character != '_' && character != '-' && character != ':' {
			return false
		}
	}
	return true
}

func normalizeAdapterHeartbeat(input AdapterHeartbeat) (AdapterHeartbeat, error) {
	if input.Schema != AdapterPresenceV1 || !validAdapterID(input.AdapterID, 64) || !validAdapterID(input.InstanceID, 96) ||
		!validRoutingLabel(input.DisplayName, 100) || !validRoutingLabel(input.Version, 64) {
		return AdapterHeartbeat{}, errors.New("adapter presence identity is invalid")
	}
	switch input.Kind {
	case "ingress", "control", "hybrid":
	default:
		return AdapterHeartbeat{}, errors.New("adapter kind must be ingress, control, or hybrid")
	}
	switch input.State {
	case "setup_required", "starting", "ready", "degraded", "stopped":
	default:
		return AdapterHeartbeat{}, errors.New("adapter state is invalid")
	}
	if input.LeaseSeconds == 0 {
		input.LeaseSeconds = defaultAdapterLeaseSeconds
	}
	if input.LeaseSeconds < minimumAdapterLeaseSeconds || input.LeaseSeconds > maximumAdapterLeaseSeconds {
		return AdapterHeartbeat{}, errors.New("adapter lease must be 15 to 300 seconds")
	}
	if input.Active < 0 || input.Active > 1_000_000 || input.Capacity < 0 || input.Capacity > 1_000_000 ||
		input.QueueDepth < 0 || input.QueueDepth > 1_000_000 || (input.Capacity > 0 && input.Active > input.Capacity) {
		return AdapterHeartbeat{}, errors.New("adapter capacity telemetry is invalid")
	}
	if input.LastErrorCode != "" && !validAdapterID(input.LastErrorCode, 80) {
		return AdapterHeartbeat{}, errors.New("adapter error code is invalid")
	}
	if len(input.Capabilities) > 32 {
		return AdapterHeartbeat{}, errors.New("adapter presence accepts at most 32 capabilities")
	}
	seen := make(map[string]struct{}, len(input.Capabilities))
	capabilities := make([]string, 0, len(input.Capabilities))
	for _, capability := range input.Capabilities {
		if !validAdapterID(capability, 80) {
			return AdapterHeartbeat{}, errors.New("adapter capability is invalid")
		}
		if _, exists := seen[capability]; exists {
			return AdapterHeartbeat{}, errors.New("adapter capabilities contain a duplicate")
		}
		seen[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)
	input.Capabilities = capabilities
	return input, nil
}

func adapterPresenceUID(clusterID, owner, adapterID string) string {
	digest := sha256.Sum256([]byte("contextbridge.adapter-presence.v1\x00" + clusterID + "\x00" + owner + "\x00" + adapterID))
	return "adp_" + hex.EncodeToString(digest[:16])
}

func adapterPresenceKey(adapterUID, instanceID string) string {
	return adapterUID + "\x00" + instanceID
}

func (r *Relay) handleAdapterHeartbeat(w http.ResponseWriter, req *http.Request) {
	var input AdapterHeartbeat
	if err := decodeJSON(req.Body, &input, 32<<10); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "adapter.invalid_presence", errors.New("invalid adapter presence payload"))
		return
	}
	normalized, err := normalizeAdapterHeartbeat(input)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "adapter.invalid_presence", err)
		return
	}
	record, _ := tokenRecord(req.Context())
	now := time.Now().UTC()
	uid := adapterPresenceUID(r.authority.ClusterID, record.Subject, normalized.AdapterID)
	enabled, err := r.store.AdapterEnabled(uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	expires := now.Add(time.Duration(normalized.LeaseSeconds) * time.Second)
	presence := AdapterPresence{
		Schema: AdapterPresenceV1, AdapterUID: uid, AdapterID: normalized.AdapterID, InstanceID: normalized.InstanceID,
		DisplayName: normalized.DisplayName, Kind: normalized.Kind, Version: normalized.Version, State: normalized.State,
		Capabilities: append([]string(nil), normalized.Capabilities...), Active: normalized.Active, Capacity: normalized.Capacity,
		QueueDepth: normalized.QueueDepth, LastErrorCode: normalized.LastErrorCode, OwnerSubject: record.Subject,
		TenantIDs: append([]string(nil), record.ProducerLimits.AllowedTenants...), Enabled: enabled,
		Available: enabled && normalized.State == "ready", LastSeenAt: now, LeaseExpiresAt: expires,
	}
	key := adapterPresenceKey(uid, normalized.InstanceID)
	r.adapterPresenceMu.Lock()
	r.pruneAdapterPresencesLocked(now)
	_, existing := r.adapterPresences[key]
	if !existing && len(r.adapterPresences) >= maximumAdapterPresences {
		r.adapterPresenceMu.Unlock()
		writeErrorCode(w, http.StatusTooManyRequests, "adapter.presence_capacity", errors.New("adapter presence registry is at capacity"))
		return
	}
	if !existing {
		owned := 0
		for _, item := range r.adapterPresences {
			if item.OwnerSubject == record.Subject {
				owned++
			}
		}
		if owned >= maximumAdapterPresencesPerOwner {
			r.adapterPresenceMu.Unlock()
			writeErrorCode(w, http.StatusTooManyRequests, "adapter.owner_presence_capacity", errors.New("adapter owner presence limit reached"))
			return
		}
	}
	r.adapterPresences[key] = presence
	r.adapterPresenceMu.Unlock()
	writeJSON(w, http.StatusOK, AdapterPresenceLease{
		Schema: AdapterPresenceLeaseV1, AdapterUID: uid, Enabled: enabled, Available: presence.Available,
		LeaseExpiresAt: expires, HeartbeatAfterSeconds: max(minimumAdapterLeaseSeconds/3, normalized.LeaseSeconds/3),
	})
}

func (r *Relay) handleAdapters(w http.ResponseWriter, req *http.Request) {
	record, _ := tokenRecord(req.Context())
	items := r.visibleAdapterPresences(record, time.Now().UTC())
	writeJSON(w, http.StatusOK, summarizeAdapterPresences(items))
}

func (r *Relay) handleAdapter(w http.ResponseWriter, req *http.Request) {
	uid := strings.TrimSpace(req.PathValue("id"))
	if !validAdapterUID(uid) {
		writeErrorCode(w, http.StatusNotFound, "adapter.not_found", errors.New("adapter not found"))
		return
	}
	record, _ := tokenRecord(req.Context())
	all := r.visibleAdapterPresences(record, time.Now().UTC())
	items := make([]AdapterPresence, 0, 1)
	for _, item := range all {
		if item.AdapterUID == uid {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		writeErrorCode(w, http.StatusNotFound, "adapter.not_found", errors.New("adapter not found"))
		return
	}
	writeJSON(w, http.StatusOK, summarizeAdapterPresences(items))
}

func (r *Relay) handleAdapterControl(w http.ResponseWriter, req *http.Request) {
	uid := strings.TrimSpace(req.PathValue("id"))
	action := strings.TrimSpace(req.PathValue("action"))
	if !validAdapterUID(uid) || (action != "enable" && action != "disable") {
		writeErrorCode(w, http.StatusNotFound, "adapter.not_found", errors.New("adapter not found"))
		return
	}
	now := time.Now().UTC()
	r.adapterPresenceMu.Lock()
	r.pruneAdapterPresencesLocked(now)
	found := false
	for _, item := range r.adapterPresences {
		if item.AdapterUID == uid {
			found = true
			break
		}
	}
	r.adapterPresenceMu.Unlock()
	if !found {
		writeErrorCode(w, http.StatusNotFound, "adapter.not_found", errors.New("adapter not found"))
		return
	}
	record, _ := tokenRecord(req.Context())
	control, err := r.store.SetAdapterEnabled(uid, action == "enable", record.Subject, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	r.adapterPresenceMu.Lock()
	for key, item := range r.adapterPresences {
		if item.AdapterUID != uid {
			continue
		}
		item.Enabled = control.Enabled
		item.Available = control.Enabled && item.State == "ready"
		r.adapterPresences[key] = item
	}
	r.adapterPresenceMu.Unlock()
	_ = r.store.AddEvent(Event{Kind: "adapter.desired_state", Message: "External adapter desired state changed", Data: map[string]interface{}{"adapter_uid": uid, "enabled": control.Enabled}})
	writeJSON(w, http.StatusOK, control)
}

func (r *Relay) visibleAdapterPresences(record TokenRecord, now time.Time) []AdapterPresence {
	r.adapterPresenceMu.Lock()
	defer r.adapterPresenceMu.Unlock()
	r.pruneAdapterPresencesLocked(now)
	items := make([]AdapterPresence, 0, len(r.adapterPresences))
	for _, item := range r.adapterPresences {
		if adapterPresenceVisible(record, item) {
			copy := item
			copy.Capabilities = append([]string(nil), item.Capabilities...)
			copy.TenantIDs = append([]string(nil), item.TenantIDs...)
			items = append(items, copy)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].AdapterUID != items[j].AdapterUID {
			return items[i].AdapterUID < items[j].AdapterUID
		}
		return items[i].InstanceID < items[j].InstanceID
	})
	return items
}

func (r *Relay) pruneAdapterPresencesLocked(now time.Time) {
	for key, item := range r.adapterPresences {
		if !now.Before(item.LeaseExpiresAt) {
			delete(r.adapterPresences, key)
		}
	}
}

func adapterPresenceVisible(record TokenRecord, item AdapterPresence) bool {
	switch record.Role {
	case "admin":
		return true
	case "producer":
		return record.Subject == item.OwnerSubject
	case "observer":
		if len(record.ObserverLimits.AllowedSubjects) > 0 && !contains(record.ObserverLimits.AllowedSubjects, item.OwnerSubject) {
			return false
		}
		if len(record.ObserverLimits.AllowedTenants) == 0 {
			return true
		}
		if len(item.TenantIDs) == 0 {
			return false
		}
		for _, tenant := range item.TenantIDs {
			if contains(record.ObserverLimits.AllowedTenants, tenant) {
				return true
			}
		}
	}
	return false
}

func summarizeAdapterPresences(items []AdapterPresence) AdapterPresenceList {
	result := AdapterPresenceList{Schema: AdapterPresenceListV1, Adapters: items, Total: len(items)}
	for _, item := range items {
		if !item.Enabled {
			result.Disabled++
		}
		if item.Available {
			result.Available++
		}
		if item.State == "setup_required" {
			result.SetupRequired++
		}
		if item.State == "degraded" {
			result.Degraded++
		}
	}
	return result
}
