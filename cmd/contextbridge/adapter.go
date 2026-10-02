package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/strictjson"
)

func adapterCommand(args []string) error {
	if len(args) == 0 || helpFlag(args[0]) {
		fmt.Fprintln(os.Stdout, `Usage: contextbridge adapter list|details|doctor|setup|enable|disable|start|stop [options]

Adapters are optional out-of-tree components. list/details/doctor are read-only.
setup registers one bounded adapter profile. --classification records whether
its execution crosses a remote boundary; repeatable --option KEY=JSON stores
non-secret driver configuration. Credentials are created only when explicitly
requested.
enable/start and disable/stop change the relay's desired admission state; they
do not install packages, invent missing credentials, or make an unhealthy
adapter ready. Control accepts a stable adp_ UID or one unambiguous leased
adapter/instance ID.`)
		return nil
	}
	switch args[0] {
	case "list":
		return adapterListCommand(args[1:])
	case "details":
		return adapterDetailsCommand(args[1:])
	case "doctor":
		return adapterDoctorCommand(args[1:])
	case "setup":
		return adapterSetupCommand(args[1:])
	case "enable", "start":
		return adapterControlCommand("enable", args[1:])
	case "disable", "stop":
		return adapterControlCommand("disable", args[1:])
	default:
		return fmt.Errorf("unknown adapter command %q", args[0])
	}
}

func adapterSetupCommand(args []string) error {
	flags := flag.NewFlagSet("adapter setup", flag.ContinueOnError)
	var rawOptions adapterSetupOptions
	path := flags.String("config", defaultConfigPath(), "config path")
	label := flags.String("label", "", "human-readable adapter profile label")
	driver := flags.String("driver", "", "bounded adapter driver identifier")
	routeName := flags.String("route", "", "local route name; defaults to the profile")
	task := flags.String("task", "", "job task handled by this adapter")
	model := flags.String("model", "", "optional exact model selector")
	timeoutSeconds := flags.Int("timeout-seconds", 180, "route timeout in seconds")
	principalID := flags.String("principal", "", "scoped principal ID; defaults to the profile")
	tokenFile := flags.String("token-file", "", "private adapter credential file")
	createToken := flags.Bool("create-token", false, "create the credential file if it is missing; never overwrite")
	classification := flags.String("classification", "", "execution classification: local or remote")
	flags.Var(&rawOptions, "option", "profile option as KEY=JSON; repeat for multiple bounded non-secret options")
	asJSON := flags.Bool("json", false, "print machine-readable redacted setup details")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: contextbridge adapter setup PROFILE --driver ID --task TASK --token-file FILE [--classification local|remote] [--option KEY=JSON] [--create-token]")
	}
	profile := strings.TrimSpace(flags.Arg(0))
	if strings.TrimSpace(*driver) == "" || strings.TrimSpace(*task) == "" || strings.TrimSpace(*tokenFile) == "" {
		return errors.New("--driver, --task, and --token-file are required")
	}
	if *timeoutSeconds < 1 || *timeoutSeconds > 3600 {
		return errors.New("--timeout-seconds must be between 1 and 3600")
	}
	class := strings.ToLower(strings.TrimSpace(*classification))
	if class != "" && class != "local" && class != "remote" {
		return errors.New("--classification must be local or remote")
	}
	profileOptions, err := rawOptions.values()
	if err != nil {
		return err
	}
	if strings.TrimSpace(*label) == "" {
		*label = profile
	}
	if strings.TrimSpace(*routeName) == "" {
		*routeName = profile
	}
	if strings.TrimSpace(*principalID) == "" {
		*principalID = profile
	}
	configPath, err := filepath.Abs(filepath.Clean(*path))
	if err != nil {
		return err
	}
	credentialPath := filepath.Clean(*tokenFile)
	if !filepath.IsAbs(credentialPath) {
		credentialPath = filepath.Join(filepath.Dir(configPath), credentialPath)
	}
	credentialPath, err = filepath.Abs(credentialPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	secret, created, err := ensureAdapterSetupToken(credentialPath, *createToken)
	if err != nil {
		return err
	}
	keepCredential := false
	defer func() {
		if created && !keepCredential {
			_ = os.Remove(credentialPath) // #nosec G703 -- exact O_EXCL file created by this command.
		}
	}()
	if cfg.AdapterProfiles == nil {
		cfg.AdapterProfiles = map[string]config.AdapterProfile{}
	}
	if cfg.Providers.Adapter.Principals == nil {
		cfg.Providers.Adapter.Principals = map[string]config.AdapterPrincipal{}
	}
	if cfg.Routes == nil {
		cfg.Routes = map[string]config.Route{}
	}
	profileConfig := config.AdapterProfile{Label: strings.TrimSpace(*label), Driver: strings.TrimSpace(*driver), Options: profileOptions}
	if current, exists := cfg.AdapterProfiles[profile]; exists && (current.Label != profileConfig.Label || current.Driver != profileConfig.Driver || !sameAdapterSetupOptions(current.Options, profileConfig.Options)) {
		return fmt.Errorf("adapter profile %s already exists with different settings", profile)
	}
	routeConfig := config.Route{Provider: "adapter", TimeoutSeconds: *timeoutSeconds, AdapterProfile: profile,
		Task: strings.TrimSpace(*task), Model: strings.TrimSpace(*model)}
	if current, exists := cfg.Routes[strings.TrimSpace(*routeName)]; exists && !sameAdapterSetupRoute(current, routeConfig) {
		return fmt.Errorf("route %s already exists with different settings", strings.TrimSpace(*routeName))
	}
	principalConfig := config.AdapterPrincipal{TokenFile: credentialPath, ResolvedToken: secret, AllowedProfiles: []string{profile}}
	if current, exists := cfg.Providers.Adapter.Principals[strings.TrimSpace(*principalID)]; exists &&
		(filepath.Clean(current.TokenFile) != credentialPath || len(current.AllowedProfiles) != 1 || current.AllowedProfiles[0] != profile) {
		return fmt.Errorf("adapter principal %s already exists with different settings", strings.TrimSpace(*principalID))
	}
	cfg.AdapterProfiles[profile] = profileConfig
	cfg.Routes[strings.TrimSpace(*routeName)] = routeConfig
	cfg.Providers.Adapter.Principals[strings.TrimSpace(*principalID)] = principalConfig
	if class != "" {
		if cfg.Cluster.Policies.Execution.AdapterProfileClassifications == nil {
			cfg.Cluster.Policies.Execution.AdapterProfileClassifications = map[string]string{}
		}
		classificationKey := profile
		for existingProfile, existingClass := range cfg.Cluster.Policies.Execution.AdapterProfileClassifications {
			if !strings.EqualFold(existingProfile, profile) {
				continue
			}
			if !strings.EqualFold(existingClass, class) {
				return fmt.Errorf("adapter profile %s already has a different execution classification", profile)
			}
			classificationKey = existingProfile
		}
		cfg.Cluster.Policies.Execution.AdapterProfileClassifications[classificationKey] = class
	}
	if cfg.Providers.Adapter.AuthMode == "" {
		cfg.Providers.Adapter.AuthMode = "scoped"
	}
	cfg.Cluster.Policies.AllowedTasks = appendUniqueFold(cfg.Cluster.Policies.AllowedTasks, strings.TrimSpace(*task))
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("adapter setup is invalid: %w", err)
	}
	if err := config.Save(configPath, cfg); err != nil {
		return fmt.Errorf("save adapter setup: %w", err)
	}
	keepCredential = true
	result := map[string]interface{}{"profile": profile, "route": strings.TrimSpace(*routeName), "principal": strings.TrimSpace(*principalID),
		"task": strings.TrimSpace(*task), "model": strings.TrimSpace(*model), "classification": class,
		"option_keys": sortedAdapterOptionKeys(profileOptions), "token_file": credentialPath, "credential_created": created}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Printf("Adapter profile %s is configured on route %s for task %s.\n", profile, strings.TrimSpace(*routeName), strings.TrimSpace(*task))
	if created {
		fmt.Printf("Created an independent private credential at %s.\n", credentialPath)
	} else {
		fmt.Printf("Reused the existing private credential at %s without displaying it.\n", credentialPath)
	}
	fmt.Println("Start the adapter process separately; ordinary ContextBridge routes remain independent if it is absent.")
	return nil
}

type adapterSetupOptions []string

func (values *adapterSetupOptions) String() string { return strings.Join(*values, ",") }

func (values *adapterSetupOptions) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func (values adapterSetupOptions) values() (map[string]interface{}, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > 64 {
		return nil, errors.New("--option accepts at most 64 entries")
	}
	result := make(map[string]interface{}, len(values))
	seen := map[string]string{}
	totalBytes := 0
	for _, raw := range values {
		totalBytes += len(raw)
		if totalBytes > 64*1024 {
			return nil, errors.New("--option data exceeds the 64 KiB setup limit")
		}
		key, encoded, found := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" || encoded == "" {
			return nil, errors.New("--option must use KEY=JSON")
		}
		if len(key) > 80 || !isAdapterOptionKey(key) {
			return nil, fmt.Errorf("--option key %q must be a bounded identifier", key)
		}
		folded := strings.ToLower(key)
		if existing, ok := seen[folded]; ok {
			return nil, fmt.Errorf("--option keys %q and %q are case-insensitively ambiguous", existing, key)
		}
		seen[folded] = key
		if err := strictjson.Validate([]byte(encoded)); err != nil {
			return nil, fmt.Errorf("--option %s must contain unambiguous JSON: %w", key, err)
		}
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		var decoded interface{}
		if err := decoder.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("--option %s must contain valid JSON: %w", key, err)
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return nil, fmt.Errorf("--option %s contains trailing JSON data", key)
		}
		decoded, err := normalizeAdapterOptionJSON(decoded, 0)
		if err != nil {
			return nil, fmt.Errorf("--option %s: %w", key, err)
		}
		result[key] = decoded
	}
	return result, nil
}

func isAdapterOptionKey(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return value != ""
}

func normalizeAdapterOptionJSON(value interface{}, depth int) (interface{}, error) {
	if depth > 8 {
		return nil, errors.New("JSON nesting exceeds 8 levels")
	}
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer, nil
		}
		decimal, err := typed.Float64()
		if err != nil {
			return nil, errors.New("number is outside the supported JSON range")
		}
		return decimal, nil
	case []interface{}:
		if len(typed) > 256 {
			return nil, errors.New("JSON array exceeds 256 items")
		}
		for index := range typed {
			normalized, err := normalizeAdapterOptionJSON(typed[index], depth+1)
			if err != nil {
				return nil, err
			}
			typed[index] = normalized
		}
		return typed, nil
	case map[string]interface{}:
		if len(typed) > 64 {
			return nil, errors.New("JSON object exceeds 64 properties")
		}
		nestedSeen := map[string]string{}
		for key, item := range typed {
			folded := strings.ToLower(key)
			if existing, exists := nestedSeen[folded]; exists {
				return nil, fmt.Errorf("JSON properties %q and %q are case-insensitively ambiguous", existing, key)
			}
			nestedSeen[folded] = key
			normalized, err := normalizeAdapterOptionJSON(item, depth+1)
			if err != nil {
				return nil, err
			}
			typed[key] = normalized
		}
		return typed, nil
	case string:
		if len(typed) > 16*1024 {
			return nil, errors.New("JSON string exceeds 16 KiB")
		}
		return typed, nil
	default:
		return value, nil
	}
}

func sortedAdapterOptionKeys(options map[string]interface{}) []string {
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sameAdapterSetupOptions(left, right map[string]interface{}) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func ensureAdapterSetupToken(path string, create bool) (string, bool, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- explicit operator-selected credential path.
	if err == nil {
		secret := strings.TrimSpace(string(raw))
		if len(secret) < 32 || len(secret) > 4096 || strings.ContainsAny(secret, "\x00\r\n") {
			return "", false, errors.New("existing adapter credential must contain one 32-4096 character secret line")
		}
		return secret, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("read adapter credential: %w", err)
	}
	if !create {
		return "", false, errors.New("adapter credential is missing; pass --create-token to create it without overwriting any existing file")
	}
	bytes := make([]byte, 48)
	if _, err := cryptorand.Read(bytes); err != nil {
		return "", false, fmt.Errorf("generate adapter credential: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(bytes)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	// #nosec G304 -- explicit operator-selected path; O_EXCL forbids replacement.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", false, fmt.Errorf("create adapter credential without overwriting: %w", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path) // #nosec G703 -- exact O_EXCL file created above.
		}
	}()
	if _, err := file.WriteString(secret + "\n"); err != nil {
		return "", false, err
	}
	if err := file.Sync(); err != nil {
		return "", false, err
	}
	if err := file.Close(); err != nil {
		return "", false, err
	}
	remove = false
	return secret, true, nil
}

func sameAdapterSetupRoute(left, right config.Route) bool {
	return left.Provider == right.Provider && len(left.Fallback) == 0 && left.TimeoutSeconds == right.TimeoutSeconds &&
		left.AdapterProfile == right.AdapterProfile && left.Task == right.Task && left.Model == right.Model
}

func appendUniqueFold(values []string, value string) []string {
	for _, current := range values {
		if strings.EqualFold(current, value) {
			return values
		}
	}
	return append(values, value)
}

type adapterCLIOptions struct {
	configPath string
	token      string
	tokenFile  string
	account    string
	asJSON     bool
}

func parseAdapterCLIFlags(name string, args []string, jsonAllowed bool) (adapterCLIOptions, []string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "scoped relay credential; defaults to the active cluster account")
	tokenFile := flags.String("token-file", "", "file containing a scoped relay credential")
	account := flags.String("account", "", "named cluster account; defaults to cluster.active_account")
	var asJSON *bool
	if jsonAllowed {
		asJSON = flags.Bool("json", false, "print machine-readable JSON")
	}
	if err := parseInterspersedFlags(flags, args); err != nil {
		return adapterCLIOptions{}, nil, err
	}
	options := adapterCLIOptions{configPath: *path, token: strings.TrimSpace(*token), tokenFile: strings.TrimSpace(*tokenFile), account: strings.TrimSpace(*account)}
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
	if err := selectClusterAccount(&cfg, options.account); err != nil {
		return config.Config{}, "", err
	}
	if options.token != "" && options.tokenFile != "" {
		return config.Config{}, "", errors.New("use only one of --token or --token-file")
	}
	explicit := options.token
	if options.tokenFile != "" {
		explicit, err = adapterCredentialFromFile(options.tokenFile)
		if err != nil {
			return config.Config{}, "", err
		}
	}
	token := clusterClientToken(cfg, explicit)
	if token == "" {
		return config.Config{}, "", errors.New("a scoped cluster credential is required; use contextbridge cluster login first")
	}
	return cfg, token, nil
}

func adapterCredentialFromFile(path string) (string, error) {
	raw, err := readRegularFileBounded(path, 32<<10)
	if err != nil {
		return "", fmt.Errorf("read adapter credential: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	var envelope struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Token != "" {
		token = strings.TrimSpace(envelope.Token)
	}
	if !strings.HasPrefix(token, "cb_") || len(token) < 24 || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return "", errors.New("adapter token file does not contain a valid ContextBridge credential")
	}
	return token, nil
}

func adapterListCommand(args []string) error {
	options, positional, err := parseAdapterCLIFlags("adapter list", args, true)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("usage: contextbridge adapter list [--config PATH] [--account NAME] [--token-file FILE] [--json]")
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
		return errors.New("usage: contextbridge adapter details [--config PATH] [--account NAME] [--token-file FILE] [--json] ADAPTER_UID_OR_ID")
	}
	uid, err := resolveAdapterUID(options, positional[0])
	if err != nil {
		return err
	}
	items, err := fetchAdapterPresences(options, uid)
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
		return errors.New("usage: contextbridge adapter doctor [--config PATH] [--account NAME] [--token-file FILE] [--json]")
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
		return fmt.Errorf("usage: contextbridge adapter %s [--config PATH] [--account NAME] [--token-file FILE] [--json] ADAPTER_UID_OR_ID", action)
	}
	cfg, token, err := adapterClientConfig(options)
	if err != nil {
		return err
	}
	uid, err := resolveAdapterUIDWithClient(cfg, token, positional[0])
	if err != nil {
		return err
	}
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

func resolveAdapterUID(options adapterCLIOptions, selector string) (string, error) {
	cfg, token, err := adapterClientConfig(options)
	if err != nil {
		return "", err
	}
	return resolveAdapterUIDWithClient(cfg, token, selector)
}

func resolveAdapterUIDWithClient(cfg config.Config, token, selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	if looksLikeAdapterUID(selector) {
		return selector, nil
	}
	if selector == "" {
		return "", errors.New("adapter selector is required")
	}
	items, err := fetchAdapterPresencesWithClient(cfg, token, "")
	if err != nil {
		return "", err
	}
	return resolveAdapterUIDFromItems(selector, items.Adapters)
}

func resolveAdapterUIDFromItems(selector string, items []cluster.AdapterPresence) (string, error) {
	selector = strings.TrimSpace(selector)
	matches := map[string]struct{}{}
	for _, item := range items {
		if strings.EqualFold(item.AdapterID, selector) || strings.EqualFold(item.InstanceID, selector) {
			matches[item.AdapterUID] = struct{}{}
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no visible leased adapter matches %q; run contextbridge adapter list or use its stable adp_ UID", selector)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("adapter selector %q is ambiguous; use the stable adp_ UID from contextbridge adapter list", selector)
	}
	for uid := range matches {
		return uid, nil
	}
	return "", errors.New("adapter selector resolution failed")
}

func looksLikeAdapterUID(value string) bool {
	if len(value) != 36 || !strings.HasPrefix(value, "adp_") {
		return false
	}
	_, err := hex.DecodeString(value[4:])
	return err == nil
}

func fetchAdapterPresences(options adapterCLIOptions, uid string) (cluster.AdapterPresenceList, error) {
	cfg, token, err := adapterClientConfig(options)
	if err != nil {
		return cluster.AdapterPresenceList{}, err
	}
	return fetchAdapterPresencesWithClient(cfg, token, uid)
}

func fetchAdapterPresencesWithClient(cfg config.Config, token, uid string) (cluster.AdapterPresenceList, error) {
	target := clusterClientBaseURL(cfg) + "/v1/cluster/adapters"
	if strings.TrimSpace(uid) != "" {
		target += "/" + url.PathEscape(strings.TrimSpace(uid))
	}
	var result cluster.AdapterPresenceList
	err := clusterGET(context.Background(), target, token, &result)
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
