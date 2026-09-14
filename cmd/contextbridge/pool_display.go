package main

import (
	"context"
	"errors"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/terminalui"
)

// The terminal reads a small pool view with the configured observer/producer
// credential. A paired worker's node token is intentionally not elevated to
// permission to list other nodes.
func readPoolDisplay(ctx context.Context, cfg config.Config) ([]terminalui.PoolNode, error) {
	token := clusterClientToken(cfg, "")
	if token == "" {
		return nil, errors.New("no pool read credential configured")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var nodes []cluster.Node
	if err := clusterGET(requestCtx, clusterBaseURL(cfg)+"/v1/cluster/nodes", token, &nodes); err != nil {
		return nil, err
	}
	result := make([]terminalui.PoolNode, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, terminalui.PoolNode{ID: node.ID, Name: node.Name, Connected: node.Connected,
			Running: node.Capabilities.Running, Slots: node.Capabilities.MaxConcurrent})
	}
	return result, nil
}

func watchPoolDisplay(ctx context.Context, cfg config.Config, session *terminalui.Session) {
	if !cfg.Cluster.Relay.Enabled && !cfg.Cluster.Worker.Enabled {
		return
	}
	session.SetRelayTarget(clusterBaseURL(cfg))
	refresh := func() {
		nodes, err := readPoolDisplay(ctx, cfg)
		if ctx.Err() == nil {
			session.ObservePool(nodes, err)
		}
	}
	refresh()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}
