package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/localtools"
	"github.com/IamAngusU/ContextBridge/internal/strictjson"
)

func agentExplicitClusterRoute(args []string) bool {
	valueFlags := map[string]bool{"--prompt": true, "--config": true, "--policy": true, "--mode": true, "--task": true, "--timeout": true, "--use": true, "--design": true, "--rules": true, "--diagnostic-workspace": true, "--research": true, "--request-id": true, "--js-checks": true, "--workspace-context": true}
	for i := 0; i < len(args); i++ {
		name := strings.SplitN(args[i], "=", 2)[0]
		name = "--" + strings.TrimLeft(name, "-")
		switch name {
		case "--provider", "--model", "--profile", "--account", "--group", "--e2ee", "--egress", "--token":
			return true
		}
		if valueFlags[name] && !strings.Contains(args[i], "=") {
			i++
		}
	}
	return false
}

type agentClient struct{ api config.AgentAPI }

func (c agentClient) call(ctx context.Context, method, path string, body any, key string) (map[string]any, error) {
	if c.api.URL == "" {
		return nil, errors.New("configure agent_api.url to use the optional local agent service")
	}
	if err := c.api.Validate(); err != nil {
		return nil, err
	}
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.api.URL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // do not send local prompts through environment proxies
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("local agent unavailable; start the configured service (no cloud fallback was attempted)")
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil || len(raw) > 8*1024*1024 {
		return nil, errors.New("invalid or oversized local agent response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("local agent HTTP %d; check policy, job state and /api/agent/capabilities", response.StatusCode)
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, errors.New("invalid local agent response")
	}
	return result, nil
}

func localAgentCommand(api config.AgentAPI, args []string) error {
	flags := flag.NewFlagSet("do", flag.ContinueOnError)
	flags.String("config", defaultConfigPath(), "config path")
	policy := api.DefaultPolicy
	if policy == "" {
		policy = "offline"
	}
	flags.StringVar(&policy, "policy", policy, "local, offline, local-agent (web/no cloud), hybrid (cloud consent still required)")
	mode := flags.String("mode", "standard", "standard or separately granted daybreak-blue")
	task := flags.String("task", "solve", "solve, code or diagnose (offline read-only host tools)")
	prompt := flags.String("prompt", "", "task text")
	background := flags.Bool("background", false, "return durable job ID immediately")
	fresh := flags.Bool("fresh", false, "skip exact verified-code candidate reuse (code only)")
	budget := flags.Duration("timeout", 900*time.Second, "job attempt budget, e.g. 900s; service operator maximum applies")
	asJSON := flags.Bool("json", false, "print full JSON receipt")
	design := flags.String("design", "default", "design card ID, default or off (code only)")
	research := flags.String("research", "", "explicit public search query (code only)")
	key := flags.String("request-id", "", "stable idempotency key for background retries")
	checks := flags.String("js-checks", "", "explicit bounded pure-JS checks JSON (code only; no host IO)")
	workspace := flags.String("workspace-context", "", "explicit prepared workspace context JSON (code only; local sources by default)")
	diagnosticWorkspace := flags.String("diagnostic-workspace", "", "explicit registered workspace ID for diagnose")
	var components imagePathListFlag
	var rules imagePathListFlag
	flags.Var(&rules, "rules", "engineering rule profile ID[@revision], default or off (code only; repeat up to four)")
	flags.Var(&components, "use", "component ID or ID@revision (repeat up to four)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	text, err := chatPromptArgument("do", *prompt, flags.Args())
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("provide a task, e.g. cb do --policy offline \"Explain RGB\"; use cluster chat for interactive sessions")
	}
	if *task != "solve" && *task != "code" && *task != "diagnose" {
		return errors.New("--task must be solve, code or diagnose")
	}
	if *task == "diagnose" && policy != "offline" {
		return errors.New("diagnose is offline-only; host metadata is not sent to cloud providers")
	}
	if *mode != "standard" && *mode != "daybreak-blue" {
		return errors.New("--mode must be standard or daybreak-blue")
	}
	if *task == "diagnose" && *mode != "standard" {
		return errors.New("diagnose requires --mode standard")
	}
	if len(rules) > 0 && *task != "code" {
		return errors.New("--rules requires code")
	}
	if *diagnosticWorkspace != "" && *task != "diagnose" {
		return errors.New("--diagnostic-workspace requires diagnose")
	}
	if *budget < 10*time.Second || *budget > 24*time.Hour || *budget%time.Second != 0 {
		return errors.New("--timeout requires whole seconds between 10s and 24h")
	}
	switch policy {
	case "local", "offline", "local-agent", "hybrid":
	default:
		return errors.New("invalid --policy")
	}
	if *key != "" && !regexp.MustCompile(`^[a-zA-Z0-9_-]{16,80}$`).MatchString(*key) {
		return errors.New("--request-id requires 16 to 80 letters, digits, _ or -")
	}
	packet := map[string]any{"text": text, "task": *task, "policy": policy, "mode": *mode, "timeout_seconds": int(*budget / time.Second), "design": *design, "research_query": *research, "components": []string(components)}
	if len(rules) > 0 {
		packet["rules"] = []string(rules)
	}
	if *diagnosticWorkspace != "" {
		packet["diagnostic_workspace"] = *diagnosticWorkspace
	}
	if packet["components"].([]string) == nil {
		packet["components"] = []string{}
	}
	if *fresh {
		packet["reuse_verified_code"] = false
	}
	if *checks != "" {
		if *task != "code" {
			return errors.New("--js-checks requires --task code")
		}
		raw, err := readRegularFileBounded(*checks, 16000)
		if err != nil {
			return errors.New("--js-checks must be a regular JSON file up to 16000 bytes")
		}
		var value map[string]any
		if json.Unmarshal(raw, &value) != nil || value == nil {
			return errors.New("invalid check JSON")
		}
		packet["js_checks"] = value
	}
	if *workspace != "" {
		if *task != "code" {
			return errors.New("--workspace-context requires --task code")
		}
		raw, err := readRegularFileBounded(*workspace, 640*1024)
		if err != nil {
			return errors.New("--workspace-context must be a regular JSON file up to 640 KiB")
		}
		var value map[string]any
		if strictjson.Decode(raw, &value) != nil || value == nil || value["schema"] != "contextbridge.workspace-context.v1" {
			return errors.New("invalid workspace context; prepare an explicit snapshot first")
		}
		if policy == "hybrid" && value["cloud_allowed"] != true {
			return errors.New("workspace context forbids cloud inference; choose offline/local-agent or explicitly prepare a cloud-permitted snapshot")
		}
		// The local service validates hashes, path scopes and byte bounds. It
		// receives file contents, never authority to traverse caller-selected paths.
		packet["workspace_context"] = value
	}
	// Preserve the existing no-service calculator for ordinary foreground do.
	// Explicit durable/JSON/code/context requests keep their service contract.
	if *task == "solve" && !*background && !*asJSON && *key == "" && !*fresh &&
		len(components) == 0 && len(rules) == 0 && (*design == "default" || *design == "off") &&
		*research == "" && *checks == "" && *workspace == "" && *diagnosticWorkspace == "" {
		result, handled, err := localtools.Resolve(text)
		if handled {
			return printLocalToolOutcome(os.Stdout, result, err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := agentClient{api: api}
	if *workspace != "" {
		caps, err := c.call(ctx, http.MethodGet, "/api/agent/capabilities", nil, "")
		if err != nil {
			return err
		}
		code, _ := caps["code"].(map[string]any)
		if support, _ := code["workspace_context"].(string); support == "" {
			return errors.New("local service does not support workspace contexts; update/restart it before submitting private source")
		}
	}
	job, err := c.call(ctx, http.MethodPost, "/api/agent/jobs", packet, *key)
	if err != nil {
		return err
	}
	id, ok := job["id"].(string)
	if !ok || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return errors.New("agent returned an invalid job ID")
	}
	if *background {
		return json.NewEncoder(os.Stdout).Encode(job)
	}
	defer func() {
		if ctx.Err() != nil && (job == nil || job["state"] == "running") {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = c.call(cancelCtx, http.MethodPost, "/api/agent/jobs/"+id+"/cancel", map[string]any{}, "")
		}
	}()
	fmt.Fprintln(os.Stderr, "  local agent ·", *mode, "·", policy, "· job", id)
	for job["state"] == "running" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		job, err = c.call(ctx, http.MethodGet, "/api/agent/jobs/"+id, nil, "")
		if err != nil {
			return err
		}
	}
	result, _ := job["result"].(map[string]any)
	if *asJSON || *task == "code" {
		if err := json.NewEncoder(os.Stdout).Encode(job); err != nil {
			return err
		}
	} else if _, ok := result["answer"].(map[string]any); ok {
		renderLocalAgentAnswer(os.Stdout, result)
	} else {
		_ = json.NewEncoder(os.Stdout).Encode(job)
	}
	if job["state"] != "completed" || result["status"] == "unavailable" {
		return errors.New("agent could not complete the request; inspect cb jobs show " + id)
	}
	if artifact, ok := result["artifact"].(map[string]any); ok {
		if verification, ok := artifact["verification"].(map[string]any); ok && verification["tests_passed"] == false {
			return errors.New("code draft failed its supplied checks; do not apply it")
		}
	}
	return nil
}

func localAgentJobsCommand(args []string) error {
	cfg, err := config.Load(chatConfigPath(args))
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("jobs", flag.ContinueOnError)
	flags.String("config", defaultConfigPath(), "config path")
	output := flags.String("output", "", "save a show receipt to a new file (never overwrite)")
	version := flags.Int("version", 0, "explicit saved code version for show/resume/diff (1..64)")
	base := flags.String("base", "original", "diff base: original, previous or earlier version")
	filePath := flags.String("path", "", "diff one relative file identifier")
	patch := flags.Bool("patch", false, "include bounded unified patches in diff")
	if err = flags.Parse(args); err != nil {
		return err
	}
	rest := flags.Args()
	if len(rest) < 1 || len(rest) > 2 {
		return errors.New("use cb jobs list|show|versions|diff|activity|cancel|resume [latest|ID]")
	}
	op := rest[0]
	if *version < 0 || *version > 64 || (*version != 0 && op != "show" && op != "resume" && op != "diff") {
		return errors.New("--version requires show/resume/diff and a value from 1 to 64")
	}
	if *output != "" && op != "show" && op != "diff" {
		return errors.New("--output requires jobs show/diff")
	}
	if op != "diff" && (*base != "original" || *filePath != "" || *patch) {
		return errors.New("--base/--path/--patch require jobs diff")
	}
	id := "latest"
	if len(rest) == 2 {
		id = rest[1]
	}
	if id != "latest" && !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return errors.New("use latest or an actual 32-character ID, not <id>")
	}
	path, method := "/api/agent/jobs", http.MethodGet
	switch op {
	case "list":
		if len(rest) > 1 {
			return errors.New("jobs list takes no ID")
		}
	case "show":
		path += "/" + id
		if *version != 0 {
			path += fmt.Sprintf("/versions/%d", *version)
		}
	case "versions":
		path += "/" + id + "/versions"
	case "activity":
		path += "/" + id + "/activity"
	case "diff":
		query := url.Values{"base": {*base}, "include_patch": {fmt.Sprint(*patch)}}
		if *version != 0 {
			query.Set("version", fmt.Sprint(*version))
		}
		if *filePath != "" {
			query.Set("path", *filePath)
		}
		path += "/" + id + "/diff?" + query.Encode()
	case "cancel", "resume":
		path += "/" + id + "/" + op
		method = http.MethodPost
	default:
		return errors.New("use jobs list, show, versions, diff, activity, cancel or resume")
	}
	var body any
	if op == "resume" && *version != 0 {
		body = map[string]any{"version": *version}
	}
	result, err := (agentClient{api: cfg.AgentAPI}).call(context.Background(), method, path, body, "")
	if err != nil {
		return err
	}
	if *output != "" {
		file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(file).Encode(result); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
