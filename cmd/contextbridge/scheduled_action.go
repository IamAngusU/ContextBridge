package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/strictjson"
)

const maximumScheduledActionCLIFileBytes = 64 << 10

func clusterScheduledActionCommand(args []string) error {
	return clusterScheduledActionCommandWithOutput(args, os.Stdout)
}

func clusterScheduledActionCommandWithOutput(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: contextbridge cluster scheduled-action preview|confirm|list|show|cancel [options]")
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	flags := flag.NewFlagSet("cluster scheduled-action "+action, flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath(), "config path")
	account := flags.String("account", "", "named cluster account; defaults to cluster.active_account")
	token := flags.String("token", "", "producer token; prefer --token-file so it does not enter shell history")
	tokenFile := flags.String("token-file", "", "file containing the exact producer credential")
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	file := flags.String("file", "", "scheduled action request JSON file")
	status := flags.String("status", "", "optional status filter for list")
	limit := flags.Int("limit", 100, "scheduled actions to return (1-100)")
	if err := parseInterspersedFlags(flags, args[1:]); err != nil {
		return err
	}
	provided := map[string]bool{}
	flags.Visit(func(option *flag.Flag) { provided[option.Name] = true })

	positional := flags.Args()
	switch action {
	case "preview":
		if len(positional) != 0 || strings.TrimSpace(*file) == "" || provided["status"] || provided["limit"] {
			return errors.New("usage: contextbridge cluster scheduled-action preview --file ACTION.json [--token-file FILE]")
		}
	case "list":
		if len(positional) != 0 || strings.TrimSpace(*file) != "" {
			return errors.New("usage: contextbridge cluster scheduled-action list [--status STATUS] [--limit 1-100]")
		}
		if *limit < 1 || *limit > 100 {
			return errors.New("--limit must be between 1 and 100")
		}
	case "confirm", "show", "cancel":
		if len(positional) != 1 || strings.TrimSpace(*file) != "" || provided["status"] || provided["limit"] {
			return fmt.Errorf("usage: contextbridge cluster scheduled-action %s ACTION_ID", action)
		}
	default:
		return fmt.Errorf("unknown scheduled-action command %q", action)
	}

	cfg, credential, err := scheduledActionCLIConfig(*configPath, *account, *token, *tokenFile)
	if err != nil {
		return err
	}
	client := newClusterAPIClient(clusterClientBaseURL(cfg), credential)
	ctx := context.Background()

	var result interface{}
	switch action {
	case "preview":
		request, err := readScheduledActionRequest(*file)
		if err != nil {
			return err
		}
		created, err := client.PreviewScheduledAction(ctx, request)
		if err != nil {
			return err
		}
		result = created
	case "confirm":
		confirmed, err := client.ConfirmScheduledAction(ctx, positional[0])
		if err != nil {
			return err
		}
		result = confirmed
	case "show":
		stored, err := client.ScheduledAction(ctx, positional[0])
		if err != nil {
			return err
		}
		result = stored
	case "cancel":
		cancelled, err := client.CancelScheduledAction(ctx, positional[0])
		if err != nil {
			return err
		}
		result = cancelled
	case "list":
		items, err := client.ScheduledActions(ctx, *status, *limit)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(output).Encode(items)
		}
		return printScheduledActionList(output, items)
	}

	item := result.(cluster.ScheduledAction)
	if *asJSON {
		return json.NewEncoder(output).Encode(item)
	}
	return printScheduledAction(output, item, action == "preview")
}

func scheduledActionCLIConfig(path, account, explicitToken, tokenFile string) (config.Config, string, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, "", err
	}
	if err := selectClusterAccount(&cfg, account); err != nil {
		return config.Config{}, "", err
	}
	explicitToken = strings.TrimSpace(explicitToken)
	tokenFile = strings.TrimSpace(tokenFile)
	if explicitToken != "" && tokenFile != "" {
		return config.Config{}, "", errors.New("use only one of --token or --token-file")
	}
	if tokenFile != "" {
		explicitToken, err = clusterCredentialFromFile(tokenFile)
		if err != nil {
			return config.Config{}, "", err
		}
	}
	credential := explicitToken
	if credential == "" {
		if _, selected, ok := selectedClusterAccount(cfg); ok {
			credential = strings.TrimSpace(selected.ClientToken)
		} else {
			credential = strings.TrimSpace(os.Getenv("CONTEXTBRIDGE_CLUSTER_TOKEN"))
			if credential == "" {
				credential = strings.TrimSpace(cfg.Cluster.ClientToken)
			}
		}
	}
	if credential == "" {
		return config.Config{}, "", errors.New("an exact producer credential is required; use --token-file or contextbridge cluster login")
	}
	return cfg, credential, nil
}

func readScheduledActionRequest(path string) (cluster.ScheduledActionRequest, error) {
	raw, err := readRegularFileBounded(path, maximumScheduledActionCLIFileBytes)
	if err != nil {
		return cluster.ScheduledActionRequest{}, fmt.Errorf("read scheduled action request: %w", err)
	}
	var request cluster.ScheduledActionRequest
	if err := strictjson.Decode(raw, &request); err != nil {
		return cluster.ScheduledActionRequest{}, fmt.Errorf("decode scheduled action request: %w", err)
	}
	return request, nil
}

func readScheduledActionPolicy(path string) (*cluster.ScheduledActionLimits, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	raw, err := readRegularFileBounded(path, maximumScheduledActionCLIFileBytes)
	if err != nil {
		return nil, fmt.Errorf("read scheduled action policy: %w", err)
	}
	var policy cluster.ScheduledActionLimits
	if err := strictjson.Decode(raw, &policy); err != nil {
		return nil, fmt.Errorf("decode scheduled action policy: %w", err)
	}
	return &policy, nil
}

func printScheduledAction(output io.Writer, action cluster.ScheduledAction, preview bool) error {
	if _, err := fmt.Fprintf(output, "Scheduled adapter action %s · %s\n", action.ID, action.Status); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "When       %s [%s]\n", action.LocalStart, action.Timezone); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "Target     %s · profile %s\n", action.AdapterUID, action.AdapterProfile); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "Action     %s · destination %s · payload %s\n", action.ActionKind, action.DestinationRef, action.PayloadRef); err != nil {
		return err
	}
	if action.RepeatEverySeconds > 0 {
		if _, err := fmt.Fprintf(output, "Repeat     every %s · %d occurrences\n", compactSeconds(action.RepeatEverySeconds), action.Occurrences); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(output, "Repeat     one time"); err != nil {
		return err
	}
	if preview {
		_, err := fmt.Fprintf(output, "Not active. Confirm the normalized preview before %s:\n  contextbridge cluster scheduled-action confirm %s\n", action.PreviewExpiresAt.UTC().Format(time.RFC3339), action.ID)
		return err
	}
	return nil
}

func printScheduledActionList(output io.Writer, list cluster.ScheduledActionList) error {
	if len(list.Actions) == 0 {
		_, err := fmt.Fprintln(output, "No scheduled adapter actions matched this credential and filter.")
		return err
	}
	for _, action := range list.Actions {
		when := action.LocalStart
		if when == "" {
			when = action.StartAt.UTC().Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(output, "%s  [%s]  %s  %s  %s\n", action.ID, action.Status, when, action.ActionKind, action.AdapterUID); err != nil {
			return err
		}
	}
	if len(list.Actions) < list.Total {
		_, err := fmt.Fprintf(output, "Showing %d of %d; raise --limit up to 100 or filter by --status.\n", len(list.Actions), list.Total)
		return err
	}
	return nil
}

func compactSeconds(value int64) string {
	if value <= 0 {
		return "0s"
	}
	return (time.Duration(value) * time.Second).String()
}
