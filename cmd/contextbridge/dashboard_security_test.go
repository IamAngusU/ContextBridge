package main

import (
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestDashboardLaunchTargetsNeverContainCredentials(t *testing.T) {
	cfg := config.Config{
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "local-sentinel-secret"},
		Cluster: config.Cluster{Relay: config.ClusterRelay{PublicURL: "https://relay.example.test", AdminToken: "admin-sentinel-secret"}},
	}
	for name, target := range map[string]string{"local": localDashboardTarget(cfg), "cluster": clusterDashboardTarget(cfg)} {
		if strings.Contains(target, "sentinel-secret") || strings.Contains(target, "token=") || strings.Contains(target, "#") {
			t.Fatalf("%s dashboard target leaked a credential: %q", name, target)
		}
	}
}
