package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

const localStopPath = "/v1/system/stop"

func stopCommand(args []string) error {
	flags := flag.NewFlagSet("stop", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	force := flags.Bool("force", false, "stop even while local, relay, or worker jobs are active")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: contextbridge stop [--config path] [--force]")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	endpoint, err := localControlURL(cfg.Server.Listen)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 20 * time.Second}
	if err := requestContextBridgeStop(ctx, client, endpoint, cfg.Server.Token, *force); err != nil {
		return err
	}
	if *force {
		fmt.Println("ContextBridge forced stop requested.")
	} else {
		fmt.Println("ContextBridge stop requested.")
	}
	return nil
}

func localControlURL(address string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || port == "" {
		return "", fmt.Errorf("server.listen must be a local host:port address: %q", address)
	}
	switch {
	case host == "", host == "0.0.0.0", host == "::", strings.EqualFold(host, "localhost"):
		host = "127.0.0.1"
	default:
		plainHost := host
		if zone := strings.LastIndexByte(plainHost, '%'); zone >= 0 {
			plainHost = plainHost[:zone]
		}
		ip := net.ParseIP(plainHost)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("contextbridge stop requires a loopback server.listen address, got %q", address)
		}
	}
	return "http://" + net.JoinHostPort(host, port) + localStopPath, nil
}

func requestContextBridgeStop(ctx context.Context, client *http.Client, endpoint, token string, force bool) error {
	if client == nil {
		return errors.New("stop request has no HTTP client")
	}
	payload, err := json.Marshal(map[string]bool{"force": force})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("local ContextBridge service is not reachable: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("read stop response: %w", err)
	}
	var result struct {
		OK       bool   `json:"ok"`
		Stopping bool   `json:"stopping"`
		Error    string `json:"error"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &result); err != nil {
			return fmt.Errorf("local service returned an invalid stop response (HTTP %d)", response.StatusCode)
		}
	}
	if response.StatusCode != http.StatusOK {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		if message == "" {
			message = response.Status
		}
		return fmt.Errorf("stop rejected: %s", message)
	}
	if !result.OK || !result.Stopping {
		return errors.New("local service did not confirm shutdown")
	}
	return nil
}
