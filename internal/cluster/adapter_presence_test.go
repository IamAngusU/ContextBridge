package cluster

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdapterPresenceIsLeasedScopedAndOperatorControlled(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	producerA, _, err := relay.store.CreateTokenWithPolicies("producer", "source-a", nil, time.Hour,
		ProducerLimits{AllowedTenants: []string{"tenant-a"}}, ObserverLimits{})
	if err != nil {
		t.Fatal(err)
	}
	producerB, _, err := relay.store.CreateToken("producer", "source-b", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	observerA, _, err := relay.store.CreateTokenWithPolicies("observer", "observer-a", nil, time.Hour,
		ProducerLimits{}, ObserverLimits{AllowedSubjects: []string{"source-a"}, AllowedTenants: []string{"tenant-a"}})
	if err != nil {
		t.Fatal(err)
	}
	observerB, _, err := relay.store.CreateTokenWithPolicies("observer", "observer-b", nil, time.Hour,
		ProducerLimits{}, ObserverLimits{AllowedSubjects: []string{"source-b"}})
	if err != nil {
		t.Fatal(err)
	}

	heartbeat := AdapterHeartbeat{
		Schema: AdapterPresenceV1, AdapterID: "example.ingress", InstanceID: "host-a", DisplayName: "Example ingress",
		Kind: "ingress", Version: "1.2.3", State: "ready", Capabilities: []string{"message.receive", "message.send"},
		Capacity: 4, Active: 1, LeaseSeconds: 30,
	}
	response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/heartbeat", producerA, heartbeat)
	if response.Code != http.StatusOK {
		t.Fatalf("heartbeat returned %d: %s", response.Code, response.Body.String())
	}
	var lease AdapterPresenceLease
	if err := json.Unmarshal(response.Body.Bytes(), &lease); err != nil || !validAdapterUID(lease.AdapterUID) || !lease.Enabled || !lease.Available {
		t.Fatalf("invalid lease: %#v, %v", lease, err)
	}

	for _, check := range []struct {
		token string
		want  int
	}{{producerA, 1}, {producerB, 0}, {observerA, 1}, {observerB, 0}, {admin, 1}} {
		response = adapterPresenceRequest(t, relay, http.MethodGet, "/v1/cluster/adapters", check.token, nil)
		var list AdapterPresenceList
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &list) != nil || list.Total != check.want {
			t.Fatalf("visible adapters for token prefix %q = %d, status %d, body %s", check.token[:8], list.Total, response.Code, response.Body.String())
		}
		if check.want == 1 && (list.Adapters[0].OwnerSubject != "source-a" || len(list.Adapters[0].TenantIDs) != 1 || list.Adapters[0].TenantIDs[0] != "tenant-a") {
			t.Fatalf("relay-authenticated scope missing from presence: %#v", list.Adapters[0])
		}
	}

	response = adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/"+lease.AdapterUID+"/disable", producerA, map[string]interface{}{})
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("producer controlled adapter desired state: %d %s", response.Code, response.Body.String())
	}
	response = adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/"+lease.AdapterUID+"/disable", admin, map[string]interface{}{})
	if response.Code != http.StatusOK {
		t.Fatalf("admin disable returned %d: %s", response.Code, response.Body.String())
	}
	response = adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/heartbeat", producerA, heartbeat)
	if err := json.Unmarshal(response.Body.Bytes(), &lease); err != nil || lease.Enabled || lease.Available {
		t.Fatalf("disabled lease was advertised as usable: %#v, %v", lease, err)
	}
	response = adapterPresenceRequest(t, relay, http.MethodGet, "/v1/cluster/adapters", admin, nil)
	var disabled AdapterPresenceList
	if err := json.Unmarshal(response.Body.Bytes(), &disabled); err != nil || disabled.Disabled != 1 || disabled.Available != 0 {
		t.Fatalf("disabled summary = %#v, %v", disabled, err)
	}

	relay.adapterPresenceMu.Lock()
	for key, item := range relay.adapterPresences {
		item.LeaseExpiresAt = time.Now().Add(-time.Second)
		relay.adapterPresences[key] = item
	}
	relay.adapterPresenceMu.Unlock()
	response = adapterPresenceRequest(t, relay, http.MethodGet, "/v1/cluster/adapters", admin, nil)
	var expired AdapterPresenceList
	if err := json.Unmarshal(response.Body.Bytes(), &expired); err != nil || expired.Total != 0 {
		t.Fatalf("expired presence remained visible: %#v, %v", expired, err)
	}
}

func TestAdapterControlPersistsButPresenceDoesNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	uid := "adp_" + strings.Repeat("a", 32)
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if enabled, err := store.AdapterEnabled(uid); err != nil || !enabled {
		t.Fatalf("default desired state = %v, %v", enabled, err)
	}
	if _, err := store.SetAdapterEnabled(uid, false, "operator-a", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if enabled, err := store.AdapterEnabled(uid); err != nil || enabled {
		t.Fatalf("disabled desired state did not survive restart: %v, %v", enabled, err)
	}
}

func TestAdapterPresenceProjectsTenantScopeAndRedactsAggregateTelemetry(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	producer, _, err := relay.store.CreateTokenWithPolicies("producer", "source-a", nil, time.Hour,
		ProducerLimits{AllowedTenants: []string{"tenant-a", "tenant-b"}}, ObserverLimits{})
	if err != nil {
		t.Fatal(err)
	}
	observer, _, err := relay.store.CreateTokenWithPolicies("observer", "observer-a", nil, time.Hour,
		ProducerLimits{}, ObserverLimits{AllowedSubjects: []string{"source-a"}, AllowedTenants: []string{"tenant-a"}})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := AdapterHeartbeat{
		Schema: AdapterPresenceV1, AdapterID: "example.ingress", InstanceID: "host-a", DisplayName: "Example ingress",
		Kind: "ingress", Version: "1.2.3", State: "ready", Capabilities: []string{"message.receive", "message.send"},
		Capacity: 4, Active: 2, QueueDepth: 3, LastErrorCode: "private-aggregate", LeaseSeconds: 30,
	}
	if response := adapterPresenceRequest(t, relay, http.MethodPost, "/v1/cluster/adapters/heartbeat", producer, heartbeat); response.Code != http.StatusOK {
		t.Fatalf("heartbeat returned %d: %s", response.Code, response.Body.String())
	}
	response := adapterPresenceRequest(t, relay, http.MethodGet, "/v1/cluster/adapters", observer, nil)
	var list AdapterPresenceList
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &list) != nil || list.Total != 1 {
		t.Fatalf("scoped observer response = %d %s", response.Code, response.Body.String())
	}
	item := list.Adapters[0]
	if len(item.TenantIDs) != 1 || item.TenantIDs[0] != "tenant-a" || item.State != "scope_redacted" {
		t.Fatalf("tenant projection failed: %#v", item)
	}
	if len(item.Capabilities) != 0 || item.Active != 0 || item.Capacity != 0 || item.QueueDepth != 0 || item.LastErrorCode != "" || item.Available || item.Enabled || !item.LastSeenAt.IsZero() || !item.LeaseExpiresAt.IsZero() {
		t.Fatalf("cross-tenant aggregate telemetry was disclosed: %#v", item)
	}
}

func TestAdapterHeartbeatRejectsAuthorityAndAmbiguousClaims(t *testing.T) {
	input := AdapterHeartbeat{Schema: AdapterPresenceV1, AdapterID: "example", InstanceID: "one", DisplayName: "Example", Kind: "ingress", Version: "1", State: "ready", Capabilities: []string{"chat", "chat"}}
	if _, err := normalizeAdapterHeartbeat(input); err == nil {
		t.Fatal("duplicate capability claims were accepted")
	}
	input.Capabilities = []string{"admin.grant"}
	if normalized, err := normalizeAdapterHeartbeat(input); err != nil || normalized.Capabilities[0] != "admin.grant" {
		t.Fatalf("descriptive capability was not accepted as bounded telemetry: %#v, %v", normalized, err)
	}
	// Acceptance as telemetry is intentionally not authority: the public type has
	// no role, scope, token, enabled, available, owner or tenant input fields.
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"owner_subject", "tenant_ids", "enabled", "available", "token", "role"} {
		if bytes.Contains(raw, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("heartbeat serialized authority-bearing field %q: %s", forbidden, raw)
		}
	}
}

func adapterPresenceRequest(t *testing.T, relay *Relay, method, path, token string, input interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body := bytes.NewReader(nil)
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer "+token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	relay.Handler().ServeHTTP(response, request)
	return response
}
