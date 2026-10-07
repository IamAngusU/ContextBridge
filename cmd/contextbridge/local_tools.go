package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/strictjson"
)

// This proxy does not implement a second execution engine or carry cluster secrets.
func localToolsCommand(args []string) error {
	cfg, err := config.Load(chatConfigPath(args))
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("tools", flag.ContinueOnError)
	flags.String("config", defaultConfigPath(), "config path")
	argumentFile := flags.String("arguments", "", "explicit JSON arguments file (256 KiB maximum)")
	rawArgs := flags.String("args", "{}", "JSON tool arguments; flags precede list/call")
	var fields imagePathListFlag
	flags.Var(&fields, "set", "FIELD=VALUE; repeat for small fields; JSON values or literal strings, avoids Windows JSON quoting")
	if err = flags.Parse(args); err != nil {
		return err
	}
	rest := flags.Args()
	path, method := "/api/agent/tools", http.MethodGet
	var body any
	if len(rest) == 1 && rest[0] == "list" {
		if *argumentFile != "" || *rawArgs != "{}" || len(fields) > 0 {
			return errors.New("arguments require tools call")
		}
	} else if len(rest) == 2 && rest[0] == "call" {
		raw := []byte(*rawArgs)
		if len(fields) > 0 {
			if *argumentFile != "" || *rawArgs != "{}" || len(fields) > 64 {
				return errors.New("choose --set, --args or --arguments; at most 64 fields")
			}
			values := map[string]any{}
			seen := map[string]bool{}
			for _, field := range fields {
				key, value, found := strings.Cut(field, "=")
				if !found || !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,47}$`).MatchString(key) || seen[strings.ToLower(key)] || len(value) > 256*1024 {
					return errors.New("invalid or duplicate --set field")
				}
				seen[strings.ToLower(key)] = true
				var parsed any
				if strictjson.Decode([]byte(value), &parsed) != nil {
					parsed = value
				}
				values[key] = parsed
			}
			raw, err = json.Marshal(values)
			if err != nil {
				return errors.New("invalid --set values")
			}
		}
		if *argumentFile != "" {
			if *rawArgs != "{}" {
				return errors.New("use only --args or --arguments")
			}
			raw, err = readRegularFileBounded(*argumentFile, 256*1024)
			if err != nil {
				return err
			}
		}
		if len(raw) > 256*1024 {
			return errors.New("tool arguments exceed 256 KiB")
		}
		var value map[string]any
		if strictjson.Decode(raw, &value) != nil || value == nil {
			return errors.New("arguments must be one strict JSON object")
		}
		body = map[string]any{"name": rest[1], "arguments": value}
		path += "/call"
		method = http.MethodPost
	} else {
		return errors.New("use cb tools list, cb tools --set port=8770 call network.listeners, or --arguments args.json")
	}
	result, err := (agentClient{api: cfg.AgentAPI}).call(context.Background(), method, path, body, "")
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
