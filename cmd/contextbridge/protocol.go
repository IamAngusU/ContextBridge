package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func clusterProtocolCommand(args []string) error {
	flags := flag.NewFlagSet("cluster protocol", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "producer/observer/admin token")
	asJSON := flags.Bool("json", false, "print machine-readable protocol manifest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: contextbridge cluster protocol [--config PATH] [--token TOKEN] [--json]")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	*token = clusterClientToken(cfg, *token)
	if *token == "" {
		return errors.New("cluster protocol requires a producer, observer, or admin token")
	}
	var manifest cluster.ProtocolManifest
	if err := clusterGET(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/protocol", *token, &manifest); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(manifest)
	}
	fmt.Printf("Protocol  [wire v%d]  [contract %s]\n", manifest.WireProtocolVersion, strings.Join(manifest.JobContractVersions, ", "))
	fmt.Printf("Limits  [job %s configured / %s maximum]  [result %s]  [worker slots %d]\n",
		formatInt64Bytes(manifest.Limits.MaximumConfiguredJobPayloadBytes), formatInt64Bytes(manifest.Limits.MaximumJobPayloadBytes),
		formatInt64Bytes(manifest.Limits.MaximumJobResultBytes), manifest.Limits.MaximumWorkerConcurrency)
	fmt.Printf("Stable IDs  [%d admission]  [%d runtime failure]\n", len(manifest.AdmissionErrorCodes), len(manifest.RuntimeFailureCodes))
	return nil
}
