package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func clusterContractCommand(args []string) error {
	if len(args) == 0 || args[0] != "validate" {
		return errors.New("usage: contextbridge cluster contract validate --file JOB.json [--json]")
	}
	flags := flag.NewFlagSet("cluster contract validate", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	file := flags.String("file", "", "cluster job JSON file")
	token := flags.String("token", "", "producer token; defaults to local admin token")
	asJSON := flags.Bool("json", false, "print machine-readable validation result")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*file) == "" {
		return errors.New("usage: contextbridge cluster contract validate --file JOB.json [--config PATH] [--token TOKEN] [--json]")
	}
	const maximumClusterSubmissionFileBytes = ((cluster.MaximumJobPayloadBytes+16)*4+2)/3 + (64 << 10)
	raw, err := readRegularFileBounded(*file, maximumClusterSubmissionFileBytes)
	if err != nil {
		return fmt.Errorf("cluster job: %w", err)
	}
	var input cluster.SubmitRequest
	if err := decodeStrictContractJSON(raw, &input); err != nil {
		return fmt.Errorf("cluster job: %w", err)
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	*token = clusterClientToken(cfg, *token)
	if *token == "" {
		return errors.New("cluster contract validation requires a producer or admin token")
	}
	var result cluster.ContractValidation
	if err := clusterPOST(context.Background(), clusterBaseURL(cfg)+"/v1/cluster/contracts/validate", *token, input, &result); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Printf("Contract valid · %s · %s payload · %d max attempts · no job submitted\n", result.ContractVersion, result.PayloadMode, result.MaxAttempts)
	return nil
}

func decodeStrictContractJSON(raw []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}
