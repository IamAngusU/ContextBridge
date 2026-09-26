package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

type doctorReport struct {
	OK     bool          `json:"ok"`
	Checks []doctorCheck `json:"checks"`
}

type doctorStatus struct {
	OK      bool `json:"ok"`
	Adapter struct {
		Connected    bool   `json:"connected"`
		State        string `json:"state"`
		Ready        bool   `json:"ready"`
		ProfileLabel string `json:"profile_label"`
	} `json:"adapter"`
	Runtime struct {
		Engines map[string]struct {
			State    string `json:"state"`
			Warning  string `json:"warning"`
			Affinity string `json:"affinity"`
		} `json:"engines"`
	} `json:"runtime"`
}

func doctorCommand(args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	report := runDoctor(*path)
	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Println("ContextBridge doctor")
		for _, check := range report.Checks {
			fmt.Printf("[%s] %s: %s\n", check.Status, check.Name, check.Detail)
			if check.Fix != "" {
				fmt.Println("      Fix:", check.Fix)
			}
		}
	}
	if !report.OK {
		return errors.New("doctor found a blocking setup problem")
	}
	return nil
}

func runDoctor(path string) doctorReport {
	report := doctorReport{OK: true}
	add := func(status, name, detail, fix string) {
		report.Checks = append(report.Checks, doctorCheck{Name: name, Status: status, Detail: detail, Fix: fix})
		if status == "fail" {
			report.OK = false
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		add("fail", "configuration", err.Error(), "Run `contextbridge init --config \""+path+"\"` or correct the YAML file.")
		return report
	}
	add("ok", "configuration", path, "")

	client := &http.Client{Timeout: 4 * time.Second}
	localURL := "http://" + cfg.Server.Listen
	if err := doctorGET(client, localURL+"/health", "", nil); err != nil {
		add("fail", "local service", err.Error(), "Start `contextbridge run --config \""+path+"\"` and run doctor again.")
		checkClusterFiles(cfg, add)
		return report
	}
	add("ok", "local service", "online at "+localURL, "")

	var status doctorStatus
	if err := doctorGET(client, localURL+"/v1/status", cfg.Server.Token, &status); err != nil || !status.OK {
		if err == nil {
			err = errors.New("authenticated status response was not healthy")
		}
		add("fail", "local authentication", err.Error(), "Confirm that the running service uses this config file and token.")
	} else {
		add("ok", "local authentication", "token accepted", "")
		providers := append([]string{cfg.Routes["default"].Provider}, cfg.Routes["default"].Fallback...)
		ready := make([]string, 0, len(providers))
		details := make([]string, 0, len(providers))
		for _, provider := range providers {
			if doctorProviderReady(provider, status) {
				ready = append(ready, provider)
			}
			details = append(details, provider+"="+doctorProviderState(provider, status))
		}
		if len(ready) == 0 {
			add("fail", "default route", strings.Join(details, ", "), "Start Ollama or a managed engine, configure an API engine, or connect an optional out-of-tree adapter.")
		} else {
			add("ok", "default route", "ready via "+strings.Join(ready, ", ")+" ("+strings.Join(details, ", ")+")", "")
		}
	}
	checkClusterFiles(cfg, add)
	return report
}

func checkClusterFiles(cfg config.Config, add func(string, string, string, string)) {
	if cfg.Cluster.Relay.Enabled {
		client := &http.Client{Timeout: 4 * time.Second}
		target := "http://" + cfg.Cluster.Relay.Listen + "/health"
		if err := doctorGET(client, target, "", nil); err != nil {
			add("fail", "cluster relay", err.Error(), "Ensure the relay component is running and the configured localhost port is free.")
		} else {
			add("ok", "cluster relay", "online at "+target, "")
		}
		if cfg.Cluster.Relay.LAN.Enabled {
			if lanListenIsWildcard(cfg.Cluster.Relay.LAN.Listen) {
				add("warn", "offline LAN relay exposure", "listener "+cfg.Cluster.Relay.LAN.Listen+" accepts traffic on every matching interface", "Bind cluster.relay.lan.listen to the intended LAN IP, or retain the wildcard only as an explicit operator decision.")
			}
			parsed, parseErr := url.Parse(cfg.Cluster.Relay.LAN.PublicURL)
			if parseErr != nil {
				add("fail", "offline LAN relay", parseErr.Error(), "Run `contextbridge cluster lan init` again with an explicit LAN address.")
			} else {
				trust, trustErr := cluster.LoadLANTLSIdentity(cfg.Cluster.Relay.LAN.CertificateFile, cfg.Cluster.Relay.LAN.PrivateKeyFile, parsed.Hostname(), time.Now().UTC())
				if trustErr != nil {
					add("fail", "offline LAN relay identity", trustErr.Error(), "Restore the original LAN TLS identity or explicitly initialize a new pool identity.")
				} else if client, clientErr := cluster.NewRelayHTTPClient(cfg.Cluster.Relay.LAN.PublicURL, trust, 4*time.Second); clientErr != nil {
					add("fail", "offline LAN relay", clientErr.Error(), "Check the LAN listener, local firewall, address, and pinned certificate without disabling TLS verification.")
				} else if reachErr := doctorGET(client, strings.TrimRight(cfg.Cluster.Relay.LAN.PublicURL, "/")+"/health", "", nil); reachErr != nil {
					add("fail", "offline LAN relay", reachErr.Error(), "Check the LAN listener, local firewall, address, and pinned certificate without disabling TLS verification.")
				} else {
					add("ok", "offline LAN relay", "reachable with pinned TLS at "+cfg.Cluster.Relay.LAN.PublicURL, "")
				}
			}
		}
	} else {
		add("warn", "cluster relay", "disabled on this device", "Enable it only when this device should coordinate other workers.")
	}
	if cfg.Cluster.Worker.Enabled {
		raw, err := os.ReadFile(cfg.Cluster.Worker.IdentityFile)
		var identity cluster.WorkerIdentity
		if err != nil || json.Unmarshal(raw, &identity) != nil || identity.NodeID == "" || identity.NodeToken == "" || identity.PrivateKey == "" {
			add("fail", "cluster worker identity", "missing or incomplete", "Run `contextbridge pair --config <path>` after configuring worker mode.")
		} else {
			add("ok", "cluster worker identity", "paired as "+identity.NodeID, "")
		}
		trust, trustErr := cluster.LoadWorkerRelayTrust(cfg.Cluster.Worker.IdentityFile, cfg.Cluster.Worker.RelayURL)
		client, clientErr := cluster.NewRelayHTTPClient(cfg.Cluster.Worker.RelayURL, trust, 4*time.Second)
		if trustErr != nil || clientErr != nil {
			if trustErr != nil {
				clientErr = trustErr
			}
			add("fail", "worker relay identity", clientErr.Error(), "Restore the trusted join bundle/identity; never disable certificate verification to bypass a pin mismatch.")
		} else if err := doctorGET(client, strings.TrimRight(cfg.Cluster.Worker.RelayURL, "/")+"/health", "", nil); err != nil {
			add("fail", "worker relay connection", err.Error(), "Check relay_url, TLS, DNS, and whether the relay is running.")
		} else {
			detail := "relay is reachable"
			if trust.SPKISHA256 != "" {
				detail += " with pinned LAN TLS identity " + trust.SPKISHA256
			}
			add("ok", "worker relay connection", detail, "")
		}
	} else {
		add("warn", "cluster worker", "disabled on this device", "Run `contextbridge guide` in a terminal, or use `contextbridge cluster configure --mode worker|all` for deterministic automation.")
	}
}

func lanListenIsWildcard(value string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsUnspecified()
}

func doctorProviderReady(provider string, status doctorStatus) bool {
	if provider == "adapter" {
		return status.Adapter.Connected && status.Adapter.Ready
	}
	engine, ok := status.Runtime.Engines[provider]
	return ok && engine.State == "online"
}

func doctorProviderState(provider string, status doctorStatus) string {
	if provider == "adapter" {
		if !status.Adapter.Connected {
			return "disconnected"
		}
		if !status.Adapter.Ready {
			return "profile-not-ready"
		}
		return "online"
	}
	if engine, ok := status.Runtime.Engines[provider]; ok {
		if engine.Warning != "" {
			return engine.State + " (" + engine.Warning + ")"
		}
		return engine.State
	}
	return "not-reported"
}

func doctorGET(client *http.Client, target, token string, output interface{}) error {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %s: %s", target, resp.Status, strings.TrimSpace(string(body)))
	}
	if output != nil {
		return json.Unmarshal(body, output)
	}
	return nil
}
