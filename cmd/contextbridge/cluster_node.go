package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func clusterNodeCommand(args []string) error {
	if len(args) == 0 || (args[0] != "drain" && args[0] != "resume") {
		return errors.New("usage: contextbridge cluster node drain|resume NODE_ID [--config path] [--token token] [--json]")
	}
	action := args[0]
	flags := flag.NewFlagSet("cluster node "+action, flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "admin token; defaults to the configured relay admin token")
	asJSON := flags.Bool("json", false, "print machine-readable node state")
	if err := parseInterspersedFlags(flags, args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("exactly one node ID is required")
	}
	nodeID := strings.TrimSpace(flags.Arg(0))
	if nodeID == "" {
		return errors.New("node ID is required")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*token) == "" {
		*token = strings.TrimSpace(cfg.Cluster.Relay.AdminToken)
	}
	if strings.TrimSpace(*token) == "" {
		return errors.New("relay admin token is required; set it in the config or pass --token")
	}
	var node cluster.Node
	target := clusterBaseURL(cfg) + "/v1/cluster/nodes/" + url.PathEscape(nodeID) + "/" + action
	if err := clusterPOST(context.Background(), target, *token, map[string]interface{}{}, &node); err != nil {
		return err
	}
	if *asJSON {
		return jsonEncode(os.Stdout, node)
	}
	state := "resumed"
	if node.Draining {
		state = "draining"
	}
	fmt.Printf("Node %s  [%s]  [%d/%d jobs]\n", emptyLabel(node.Name, node.ID), state, node.Capabilities.Running, max(1, node.Capabilities.MaxConcurrent))
	if node.Draining && node.Capabilities.Running > 0 {
		fmt.Println("Existing jobs may finish; no new assignments will be created on this node.")
	}
	return nil
}

func jsonEncode(output *os.File, value interface{}) error {
	return json.NewEncoder(output).Encode(value)
}
