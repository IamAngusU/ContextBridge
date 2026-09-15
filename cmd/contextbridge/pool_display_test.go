package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestReadPoolDisplayUsesReadOnlyCredentialAndMapsSafeFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/cluster/nodes" || r.Header.Get("Authorization") != "Bearer observer-token" {
			t.Errorf("unexpected pool request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"node_abc","name":"workstation","connected":true,"public_key":"not-for-terminal","capabilities":{"running":1,"max_concurrent":4,"gpus":[{"name":"RTX","utilization_percent":20}],"models":[{"name":"vision-model","provider":"ollama","loaded":true,"vision":true}]}},{"id":"node_def","name":"server","connected":false,"capabilities":{"max_concurrent":2}}]`))
	}))
	defer server.Close()
	cfg := config.Config{Cluster: config.Cluster{ClientToken: "observer-token", Worker: config.ClusterWorker{RelayURL: server.URL}}}
	nodes, err := readPoolDisplay(context.Background(), cfg)
	if err != nil || len(nodes) != 2 || nodes[0].Name != "workstation" || !nodes[0].Connected || nodes[0].Running != 1 || nodes[1].Connected ||
		len(nodes[0].GPUs) != 1 || nodes[0].GPUs[0].Name != "RTX" || len(nodes[0].Models) != 1 || !nodes[0].Models[0].Vision {
		t.Fatalf("unexpected pool display: %+v, %v", nodes, err)
	}
	if _, err := readPoolDisplay(context.Background(), config.Config{Cluster: config.Cluster{Worker: config.ClusterWorker{RelayURL: server.URL}}}); err == nil {
		t.Fatal("pool display must not use the node identity as an observer credential")
	}
}
