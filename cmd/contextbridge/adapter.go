package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func adapterCommand(args []string) error {
	if len(args) == 0 || helpFlag(args[0]) {
		fmt.Fprintln(os.Stdout, `Usage: contextbridge adapter list|details|doctor|enable|disable|start|stop [options]

Adapters are optional out-of-tree components. list/details/doctor are read-only.
enable/start and disable/stop change the relay's desired admission state; they
do not install packages, invent missing credentials, or make an unhealthy
adapter ready.`)
		return nil
	}
	switch args[0] {
	case "list":
		return adapterListCommand(args[1:])
	case "details":
		return adapterDetailsCommand(args[1:])
	case "doctor":
		return adapterDoctorCommand(args[1:])
	case "enable", "start":
		return adapterControlCommand("enable", args[1:])
	case "disable", "stop":
		return adapterControlCommand("disable", args[1:])
	default:
		return fmt.Errorf("unknown adapter command %q", args[0])
	}
}

type adapterCLIOptions struct {
	configPath string
	token      string
	asJSON     bool
}

func parseAdapterCLIFlags(name string, args []string, jsonAllowed bool) (adapterCLIOptions, []string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "scoped relay credential; defaults to the active cluster account")
	var asJSON *bool
	if jsonAllowed {
		asJSON = flags.Bool("json", false, "print machine-readable JSON")
	}
	if err := flags.Parse(args); err != nil {
		return adapterCLIOptions{}, nil, err
	}
	options := adapterCLIOptions{configPath: *path, token: strings.TrimSpace(*token)}
	if asJSON != nil {
		options.asJSON = *asJSON
	}
	return options, flags.Args(), nil
}

func adapterClientConfig(options adapterCLIOptions) (config.Config, string, error) {
	cfg, err := config.Load(options.configPath)
	if err != nil {
		return config.Config{}, "", err
	}
	token := clusterClientToken(cfg, options.token)
	if token == "" {
		return config.Config{}, "", errors.New("a scoped cluster credential is required; use contextbridge cluster login first")
	}
	return cfg, token, nil
}

func adapterListCommand(args []string) error {
	options, positional, err := parseAdapterCLIFlags("adapter list", args, true)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("usage: contextbridge adapter list [--config PATH] [--json]")
	}
	items, err := fetchAdapterPresences(options, "")
	if err != nil {
		return err
	}
	return printAdapterPresences(items, options.asJSON)
}

func adapterDetailsCommand(args []string) error {
	options, positional, err := parseAdapterCLIFlags("adapter details", args, true)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: contextbridge adapter details [--config PATH] [--json] ADAPTER_UID")
	}
	items, err := fetchAdapterPresences(options, positional[0])
	if err != nil {
		return err
	}
	return printAdapterPresences(items, options.asJSON)
}

func adapterDoctorCommand(args []string) error {
	options, positional, err := parseAdapterCLIFlags("adapter doctor", args, true)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("usage: contextbridge adapter doctor [--config PATH] [--json]")
	}
	items, err := fetchAdapterPresences(options, "")
	if err != nil {
		return err
	}
	if options.asJSON {
		return json.NewEncoder(os.Stdout).Encode(items)
	}
	if items.Total == 0 {
		fmt.Println("Adapters  [none leased]  Optional adapters do not affect ordinary ContextBridge work.")
		return nil
	}
	fmt.Printf("Adapters  [%d leased]  [%d available]  [%d setup]  [%d degraded]  [%d disabled]\n",
		items.Total, items.Available, items.SetupRequired, items.Degraded, items.Disabled)
	for _, item := range items.Adapters {
		assessment := "OK"
		detail := "fresh ready lease"
		switch {
		case !item.Enabled:
			assessment, detail = "OFF", "disabled by relay policy"
		case item.State == "setup_required":
			assessment, detail = "SETUP", "adapter reports incomplete setup"
		case item.State == "degraded":
			assessment, detail = "WARN", "adapter reports degraded health"
		case !item.Available:
			assessment, detail = "WAIT", "adapter is not ready"
		}
		fmt.Printf("  %-5s %s  %s  [%s]\n", assessment, item.AdapterUID, item.DisplayName, detail)
	}
	return nil
}

func adapterControlCommand(action string, args []string) error {
	options, positional, err := parseAdapterCLIFlags("adapter "+action, args, true)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: contextbridge adapter %s [--config PATH] [--json] ADAPTER_UID", action)
	}
	cfg, token, err := adapterClientConfig(options)
	if err != nil {
		return err
	}
	uid := strings.TrimSpace(positional[0])
	var control cluster.AdapterControl
	target := clusterClientBaseURL(cfg) + "/v1/cluster/adapters/" + url.PathEscape(uid) + "/" + action
	if err := clusterPOST(context.Background(), target, token, map[string]interface{}{}, &control); err != nil {
		return err
	}
	if options.asJSON {
		return json.NewEncoder(os.Stdout).Encode(control)
	}
	state := "disabled"
	if control.Enabled {
		state = "enabled"
	}
	fmt.Printf("Adapter %s is %s. A fresh ready lease is still required before it is available.\n", control.AdapterUID, state)
	return nil
}

func fetchAdapterPresences(options adapterCLIOptions, uid string) (cluster.AdapterPresenceList, error) {
	cfg, token, err := adapterClientConfig(options)
	if err != nil {
		return cluster.AdapterPresenceList{}, err
	}
	target := clusterClientBaseURL(cfg) + "/v1/cluster/adapters"
	if strings.TrimSpace(uid) != "" {
		target += "/" + url.PathEscape(strings.TrimSpace(uid))
	}
	var result cluster.AdapterPresenceList
	err = clusterGET(context.Background(), target, token, &result)
	return result, err
}

func printAdapterPresences(items cluster.AdapterPresenceList, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(items)
	}
	if items.Total == 0 {
		fmt.Println("No external adapters are currently leased. ContextBridge itself remains available.")
		return nil
	}
	sort.Slice(items.Adapters, func(i, j int) bool {
		if items.Adapters[i].AdapterUID != items.Adapters[j].AdapterUID {
			return items.Adapters[i].AdapterUID < items.Adapters[j].AdapterUID
		}
		return items.Adapters[i].InstanceID < items.Adapters[j].InstanceID
	})
	for _, item := range items.Adapters {
		state := item.State
		if !item.Enabled {
			state = "disabled"
		}
		load := ""
		if item.Capacity > 0 {
			load = fmt.Sprintf(" · %d/%d active", item.Active, item.Capacity)
		}
		if item.QueueDepth > 0 {
			load += fmt.Sprintf(" · %d queued", item.QueueDepth)
		}
		fmt.Printf("%s  %-14s  %s · %s%s\n", item.AdapterUID, state, item.DisplayName, item.Version, load)
		fmt.Printf("  instance %s · %s · owner %s · lease until %s\n", item.InstanceID, item.Kind, item.OwnerSubject, item.LeaseExpiresAt.Local().Format("15:04:05"))
		if len(item.Capabilities) > 0 {
			fmt.Printf("  claims   %s (descriptive only; permissions are enforced separately)\n", strings.Join(item.Capabilities, ", "))
		}
		if item.LastErrorCode != "" {
			fmt.Printf("  error    %s\n", item.LastErrorCode)
		}
	}
	return nil
}
