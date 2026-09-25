package cluster

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMetricsRequiresObserverAndUsesOnlyBoundedAggregateLabels(t *testing.T) {
	admin := "admin_012345678901234567890123456789012345"
	relay, err := NewRelay(RelayConfig{Database: filepath.Join(t.TempDir(), "relay.db"), AdminToken: admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	observer, _, err := relay.store.CreateToken("observer", "metrics-reader", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	producer, _, err := relay.store.CreateToken("producer", "secret-producer-subject", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	node := Node{ID: "sensitive-node-id", Name: "Sensitive Node Name", Connected: true, Draining: true, LastSeen: time.Now().UTC(), Capabilities: Capabilities{MaxConcurrent: 4, Running: 2}, RoutingHealth: []RoutingHealth{{Provider: "private-provider", Model: "private-model", CircuitOpenUntil: time.Now().Add(time.Minute)}}}
	if err := relay.store.UpsertNode(node); err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < routingFailureThreshold; i++ {
		job, err := relay.store.CreateJob(SubmitRequest{Requirements: Requirements{Task: "generation", Provider: "private-provider", Model: "private-model"}, Payload: json.RawMessage(`{}`), MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		job, err = relay.store.AssignJob(job.ID, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := relay.store.CompleteJobWithFailure(job.ID, node.ID, job.Attempt, nil, nil, Usage{}, "provider timeout", FailureAdapterTimeout); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := relay.store.SetNodeDraining(node.ID, true); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		token string
		want  int
	}{
		{name: "anonymous", want: http.StatusUnauthorized},
		{name: "producer", token: producer, want: http.StatusUnauthorized},
		{name: "observer", token: observer, want: http.StatusOK},
		{name: "admin", token: admin, want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			relay.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
			if test.want != http.StatusOK {
				return
			}
			body := response.Body.String()
			for _, secret := range []string{"sensitive-node-id", "Sensitive Node Name", "private-provider", "private-model", "secret-producer-subject"} {
				if strings.Contains(body, secret) {
					t.Fatalf("metrics exposed %q: %s", secret, body)
				}
			}
			for _, expected := range []string{
				"contextbridge_nodes{state=\"total\"} 1",
				"contextbridge_nodes{state=\"draining\"} 1",
				"contextbridge_worker_slots{state=\"total\"} 4",
				"contextbridge_worker_slots{state=\"busy\"} 2",
				"contextbridge_jobs{state=\"queued\"} 0",
				"contextbridge_jobs{state=\"failed\"} 3",
				"contextbridge_routing_circuits_open 1",
			} {
				if !strings.Contains(body, expected) {
					t.Fatalf("metrics missing %q: %s", expected, body)
				}
			}
			if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
				t.Fatalf("unexpected content type %q", contentType)
			}
		})
	}
}
