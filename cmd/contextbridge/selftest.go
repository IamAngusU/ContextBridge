package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/terminalui"
)

const (
	selftestLocal   = "local"
	selftestChatGPT = "chatgpt"
	selftestGemini  = "gemini"
)

type selftestTarget struct {
	Kind        string
	NodeID      string
	NodeName    string
	Running     int
	Capacity    int
	Model       string
	ModelLoaded bool
	ModelSize   int64
}

type selftestPlan struct {
	Targets []selftestTarget
	Missing []string
}

func clusterSelftestCommand(args []string) error {
	overallStarted := time.Now()
	flags := flag.NewFlagSet("cluster selftest", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	providersRaw := flags.String("providers", "local,chatgpt,gemini", "comma-separated checks: local, chatgpt, gemini")
	localModel := flags.String("local-model", "", "specific local generation model; default chooses the smallest loaded compatible model")
	run := flags.Bool("run", false, "send one fixed, low-output text check per requested target")
	dryRun := flags.Bool("dry-run", false, "readiness check only (also the default without --run)")
	image := flags.Bool("image", false, "also request and verify one real ChatGPT image file (explicit provider-cost opt-in)")
	artifacts := flags.String("artifacts", "", "directory for --image output; default uses a temporary directory")
	keepArtifacts := flags.Bool("keep-artifacts", false, "keep the automatically created temporary artifact directory")
	timeout := flags.Duration("timeout", 5*time.Minute, "maximum time to wait for compatible capacity")
	jobTimeout := flags.Duration("job-timeout", 10*time.Minute, "maximum execution time for each opted-in live check")
	poll := flags.Duration("poll", 2*time.Second, "readiness polling interval")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("cluster selftest accepts flags only")
	}
	providers, err := parseSelftestProviders(*providersRaw)
	if err != nil {
		return err
	}
	if *run && *dryRun {
		return errors.New("--run and --dry-run cannot be combined")
	}
	if *image && !*run {
		return errors.New("--image requires --run; readiness checks never create provider work")
	}
	if *image && !containsSelftestProvider(providers, selftestChatGPT) {
		return errors.New("--image requires chatgpt in --providers")
	}
	if (*artifacts != "" || *keepArtifacts) && !*image {
		return errors.New("--artifacts and --keep-artifacts require --image")
	}
	if *timeout < time.Second || *timeout > 30*time.Minute {
		return errors.New("--timeout must be between 1s and 30m")
	}
	if *poll < 250*time.Millisecond || *poll > 30*time.Second {
		return errors.New("--poll must be between 250ms and 30s")
	}
	if *jobTimeout < 5*time.Second || *jobTimeout > 30*time.Minute {
		return errors.New("--job-timeout must be between 5s and 30m")
	}

	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	token := clusterClientToken(cfg, "")
	if token == "" {
		return errors.New("a producer or observer token is required; run cluster login or configure cluster.client_token")
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	readinessCtx, cancelReadiness := context.WithTimeout(rootCtx, *timeout)

	mode := "readiness only · no AI request will be sent"
	if *run {
		mode = "live checks · fixed bounded prompts"
		if containsSelftestProvider(providers, selftestChatGPT) || containsSelftestProvider(providers, selftestGemini) {
			mode += " · fresh ContextBridge browser chats only"
		}
	}
	printSelftestSection("CONTEXTBRIDGE SELF-TEST")
	fmt.Println("| mode   ·", mode)
	fmt.Println("| needs  ·", selftestProviderLabels(providers))
	if *run {
		fmt.Printf("| budget · %d fixed text request(s)", len(providers))
		if *image {
			fmt.Print(" + 1 ChatGPT image request")
		}
		fmt.Println()
	}
	printSelftestSection("READINESS")

	plan, err := waitForSelftestPlan(readinessCtx, cfg, token, providers, strings.TrimSpace(*localModel), *poll)
	cancelReadiness()
	if err != nil {
		return err
	}
	fmt.Printf("| ✓ Ready %d/%d · %s\n", len(plan.Targets), len(providers), formatSelftestPreciseDuration(time.Since(overallStarted)))
	printSelftestPlan(plan)
	if !*run {
		printSelftestSection("RESULT")
		fmt.Println("| ✓ No prompts sent · live text: --run · image: --run --image")
		return nil
	}

	printSelftestSection("CHECKS")
	runID := selftestRunID()
	passed := 0
	for _, target := range plan.Targets {
		marker := "CB-SELFTEST-" + strings.ToUpper(target.Kind) + "-" + runID
		fmt.Printf("| → %s\n", selftestTargetLabel(target.Kind))
		jobStarted := time.Now()
		jobCtx, cancelJob := context.WithTimeout(rootCtx, *jobTimeout)
		output, job, err := runSelftestJob(jobCtx, cfg, token, target, selftestJobSpec{
			Prompt: "Reply with exactly " + marker + " and no other characters.",
		})
		cancelJob()
		if err != nil {
			return fmt.Errorf("%s text check: %w", target.Kind, err)
		}
		if output == nil || strings.TrimSpace(output.Text) != marker {
			actual := "<empty>"
			if output != nil && strings.TrimSpace(output.Text) != "" {
				actual = truncateSelftestText(strings.TrimSpace(output.Text), 120)
			}
			return fmt.Errorf("%s text check returned %q instead of the exact marker", target.Kind, actual)
		}
		fmt.Printf("| ✓ exact marker · job %s · node %s\n", shortChatID(job.ID), shortChatID(job.AssignedNode))
		fmt.Printf("|   timing · %s\n", selftestTimingSummary(job, time.Since(jobStarted)))
		passed++
	}

	verifiedImages := 0
	artifactDirectory := strings.TrimSpace(*artifacts)
	removeTemporaryArtifacts := false
	if *image {
		if artifactDirectory == "" {
			artifactDirectory, err = os.MkdirTemp("", "contextbridge-selftest-")
			if err != nil {
				return fmt.Errorf("create temporary artifact directory: %w", err)
			}
			removeTemporaryArtifacts = !*keepArtifacts
		}
		if removeTemporaryArtifacts {
			defer os.RemoveAll(artifactDirectory)
		}
		chatGPT := selftestTarget{}
		for _, target := range plan.Targets {
			if target.Kind == selftestChatGPT {
				chatGPT = target
				break
			}
		}
		fmt.Println("| → ChatGPT image")
		jobStarted := time.Now()
		jobCtx, cancelJob := context.WithTimeout(rootCtx, *jobTimeout)
		output, job, err := runSelftestJob(jobCtx, cfg, token, chatGPT, selftestJobSpec{
			Prompt:       "Create exactly one small square image with a plain white background and the centered black text CB SELFTEST. Return only the image.",
			RequireImage: true,
		})
		cancelJob()
		if err != nil {
			return fmt.Errorf("chatgpt image check: %w", err)
		}
		for _, artifact := range output.Artifacts {
			if artifact.DataBase64 != "" && strings.HasPrefix(strings.ToLower(artifact.MediaType), "image/") {
				verifiedImages++
			}
		}
		if verifiedImages < 1 {
			return errors.New("chatgpt image check completed without a transferred image file")
		}
		paths, references, err := saveOutputArtifacts(output, artifactDirectory)
		if err != nil {
			return fmt.Errorf("save verified image: %w", err)
		}
		if len(paths) == 0 {
			return errors.New("chatgpt image check returned no savable image bytes")
		}
		fmt.Printf("| ✓ %d verified image file(s) · job %s\n", verifiedImages, shortChatID(job.ID))
		fmt.Printf("|   timing · %s\n", selftestTimingSummary(job, time.Since(jobStarted)))
		reportSavedArtifacts(paths, references)
		if removeTemporaryArtifacts {
			fmt.Println("|   temporary files will be removed; pass --keep-artifacts or --artifacts DIR to retain them")
		}
	}

	printSelftestSection("RESULT")
	fmt.Printf("| ✓ Self-test passed · %d/%d text checks", passed, len(plan.Targets))
	if *image {
		fmt.Printf(" · %d image file(s)", verifiedImages)
	}
	fmt.Printf(" · total %s\n", formatSelftestPreciseDuration(time.Since(overallStarted)))
	return nil
}

func printSelftestSection(label string) {
	fmt.Println("+-- " + label)
}

func parseSelftestProviders(raw string) ([]string, error) {
	wanted := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		switch strings.ToLower(strings.TrimSpace(item)) {
		case "local", "loc", "ollama":
			wanted[selftestLocal] = true
		case "chatgpt", "gpt":
			wanted[selftestChatGPT] = true
		case "gemini", "gem":
			wanted[selftestGemini] = true
		case "":
		default:
			return nil, fmt.Errorf("unsupported self-test provider %q; use local, chatgpt, or gemini", strings.TrimSpace(item))
		}
	}
	providers := make([]string, 0, 3)
	for _, provider := range []string{selftestLocal, selftestChatGPT, selftestGemini} {
		if wanted[provider] {
			providers = append(providers, provider)
		}
	}
	if len(providers) == 0 {
		return nil, errors.New("--providers must include local, chatgpt, or gemini")
	}
	return providers, nil
}

func containsSelftestProvider(providers []string, wanted string) bool {
	for _, provider := range providers {
		if provider == wanted {
			return true
		}
	}
	return false
}

func selftestProviderLabels(providers []string) string {
	labels := make([]string, 0, len(providers))
	for _, provider := range providers {
		labels = append(labels, selftestTargetLabel(provider))
	}
	return strings.Join(labels, " · ")
}

func selftestTargetLabel(kind string) string {
	switch kind {
	case selftestLocal:
		return "▣LOC local generation"
	case selftestChatGPT:
		return "◉GPT attached ChatGPT tab"
	case selftestGemini:
		return "✦GEM attached Gemini tab"
	default:
		return kind
	}
}

func waitForSelftestPlan(ctx context.Context, cfg config.Config, token string, providers []string, localModel string, poll time.Duration) (selftestPlan, error) {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	started := time.Now()
	interactive := false
	if info, err := os.Stdout.Stat(); err == nil {
		interactive = info.Mode()&os.ModeCharDevice != 0
	}
	lastMessage := "checking relay and pool"
	lastPrint := time.Time{}
	clearInteractive := func() {
		if interactive {
			terminalui.ClearTransientStatus(os.Stdout)
		}
	}
	for {
		requestCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		var nodes []cluster.Node
		err := clusterGET(requestCtx, clusterBaseURL(cfg)+"/v1/cluster/nodes", token, &nodes)
		cancel()
		if ctx.Err() != nil {
			clearInteractive()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return selftestPlan{}, fmt.Errorf("readiness timeout: %s. Start/attach the listed targets, connect the extension, or change --providers", lastMessage)
			}
			return selftestPlan{}, ctx.Err()
		}
		ready := 0
		message := ""
		if err == nil {
			plan := buildSelftestPlan(nodes, providers, localModel)
			if len(plan.Missing) == 0 {
				clearInteractive()
				return plan, nil
			}
			ready = len(plan.Targets)
			message = strings.Join(plan.Missing, "; ")
		} else {
			message = "relay unavailable: " + err.Error()
		}
		remaining := time.Duration(0)
		if deadline, ok := ctx.Deadline(); ok {
			remaining = max(time.Duration(0), time.Until(deadline))
		}
		line := fmt.Sprintf("| ◇ [Ready %d/%d] [waiting %s] [timeout in %s] %s", ready, len(providers), formatSelftestDuration(time.Since(started)), formatSelftestDuration(remaining), message)
		if interactive {
			terminalui.DrawTransientStatus(os.Stdout, line)
		} else if message != lastMessage || lastPrint.IsZero() || time.Since(lastPrint) >= 30*time.Second {
			fmt.Println(line)
			lastPrint = time.Now()
		}
		lastMessage = message
		select {
		case <-ctx.Done():
			clearInteractive()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return selftestPlan{}, fmt.Errorf("readiness timeout: %s. Start/attach the listed targets, connect the extension, or change --providers", lastMessage)
			}
			return selftestPlan{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func formatSelftestDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	duration = duration.Round(time.Second)
	if duration < time.Minute {
		return fmt.Sprintf("%ds", int(duration.Seconds()))
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(duration/time.Minute), int(duration%time.Minute/time.Second))
	}
	return fmt.Sprintf("%dh%02dm", int(duration/time.Hour), int(duration%time.Hour/time.Minute))
}

func formatSelftestPreciseDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	if duration < time.Minute {
		return fmt.Sprintf("%.1fs", duration.Round(100*time.Millisecond).Seconds())
	}
	return formatSelftestDuration(duration)
}

func selftestTimingSummary(job cluster.Job, wall time.Duration) string {
	parts := []string{"wall " + formatSelftestPreciseDuration(wall)}
	if job.Usage.QueueMS > 0 {
		parts = append(parts, "queue "+formatSelftestPreciseDuration(time.Duration(job.Usage.QueueMS)*time.Millisecond))
	}
	if job.Usage.ComputeMS > 0 {
		parts = append(parts, "compute "+formatSelftestPreciseDuration(time.Duration(job.Usage.ComputeMS)*time.Millisecond))
	}
	return strings.Join(parts, " · ")
}

func buildSelftestPlan(nodes []cluster.Node, providers []string, requestedLocalModel string) selftestPlan {
	return buildSelftestPlanAt(time.Now(), nodes, providers, requestedLocalModel)
}

func buildSelftestPlanAt(now time.Time, nodes []cluster.Node, providers []string, requestedLocalModel string) selftestPlan {
	plan := selftestPlan{}
	for _, kind := range providers {
		var candidates []selftestTarget
		profileSeen, busySeen, providerSeen, slotSeen, staleSeen := false, false, false, false, false
		for _, node := range nodes {
			if !node.Connected || now.Sub(node.LastSeen) > cluster.NodeFreshnessWindow {
				if selftestNodeAdvertisesTarget(node, kind, requestedLocalModel) {
					staleSeen = true
				}
				continue
			}
			switch kind {
			case selftestLocal:
				if !containsFolded(node.Capabilities.Providers, "ollama") {
					continue
				}
				providerSeen = true
				models := compatibleSelftestModels(node.Capabilities.Models, requestedLocalModel)
				if len(models) == 0 {
					continue
				}
				capacity := max(1, node.Capabilities.MaxConcurrent)
				if node.Capabilities.Running >= capacity {
					slotSeen = true
					continue
				}
				model := models[0]
				if !selftestNodeCanSchedule(node, cluster.Requirements{Task: "generation", Provider: "ollama", Model: model.Name}) {
					continue
				}
				candidates = append(candidates, selftestTarget{Kind: kind, NodeID: node.ID, NodeName: node.Name, Running: node.Capabilities.Running, Capacity: capacity, Model: model.Name, ModelLoaded: model.Loaded, ModelSize: model.Size})
			case selftestChatGPT, selftestGemini:
				if !containsFolded(node.Capabilities.Providers, "browser") {
					continue
				}
				providerSeen = true
				for _, session := range node.Capabilities.BrowserSessions {
					if !strings.EqualFold(strings.TrimSpace(session.Profile), kind) {
						continue
					}
					profileSeen = true
					if !strings.EqualFold(strings.TrimSpace(session.State), "waiting") {
						busySeen = true
						continue
					}
					capacity := max(1, node.Capabilities.MaxConcurrent)
					if node.Capabilities.Running >= capacity {
						slotSeen = true
						continue
					}
					if !selftestNodeCanSchedule(node, cluster.Requirements{Task: "generation", Provider: "browser", BrowserProfile: kind}) {
						busySeen = true
						continue
					}
					candidates = append(candidates, selftestTarget{Kind: kind, NodeID: node.ID, NodeName: node.Name, Running: node.Capabilities.Running, Capacity: capacity})
				}
			}
		}
		if len(candidates) == 0 {
			switch {
			case slotSeen:
				plan.Missing = append(plan.Missing, "all compatible worker slots are busy for "+kind)
			case kind == selftestLocal && providerSeen:
				if requestedLocalModel != "" {
					plan.Missing = append(plan.Missing, fmt.Sprintf("local model %q is not a generation model", requestedLocalModel))
				} else {
					plan.Missing = append(plan.Missing, "Ollama is online but has no generation model")
				}
			case (kind == selftestChatGPT || kind == selftestGemini) && profileSeen && busySeen:
				plan.Missing = append(plan.Missing, kind+" tab is busy or rate-limited")
			case (kind == selftestChatGPT || kind == selftestGemini) && providerSeen:
				plan.Missing = append(plan.Missing, "attach an idle "+kind+" tab in the ContextBridge extension")
			case staleSeen:
				plan.Missing = append(plan.Missing, "compatible "+kind+" worker is stale or offline")
			default:
				plan.Missing = append(plan.Missing, "no online worker provides "+kind)
			}
			continue
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].ModelLoaded != candidates[j].ModelLoaded {
				return candidates[i].ModelLoaded
			}
			if candidates[i].ModelSize != candidates[j].ModelSize {
				left, right := candidates[i].ModelSize, candidates[j].ModelSize
				if left <= 0 {
					left = 1<<63 - 1
				}
				if right <= 0 {
					right = 1<<63 - 1
				}
				if left != right {
					return left < right
				}
			}
			if candidates[i].NodeName != candidates[j].NodeName {
				return candidates[i].NodeName < candidates[j].NodeName
			}
			return candidates[i].NodeID < candidates[j].NodeID
		})
		plan.Targets = append(plan.Targets, candidates[0])
	}
	return plan
}

// The self-test must agree with the scheduler's provider/model/profile gates.
// LastSeen was already compared against the caller-supplied clock, so normalize
// only that timestamp before asking Rank to validate the remaining capability
// contract. This keeps deterministic readiness tests independent of wall time.
func selftestNodeCanSchedule(node cluster.Node, requirements cluster.Requirements) bool {
	node.LastSeen = time.Now()
	return len(cluster.Rank([]cluster.Node{node}, requirements)) == 1
}

func selftestNodeAdvertisesTarget(node cluster.Node, kind, requestedLocalModel string) bool {
	switch kind {
	case selftestLocal:
		return containsFolded(node.Capabilities.Providers, "ollama") && len(compatibleSelftestModels(node.Capabilities.Models, requestedLocalModel)) > 0
	case selftestChatGPT, selftestGemini:
		if !containsFolded(node.Capabilities.Providers, "browser") {
			return false
		}
		for _, session := range node.Capabilities.BrowserSessions {
			if strings.EqualFold(strings.TrimSpace(session.Profile), kind) {
				return true
			}
		}
	}
	return false
}

func compatibleSelftestModels(models []cluster.ModelCapability, requested string) []cluster.ModelCapability {
	compatible := make([]cluster.ModelCapability, 0, len(models))
	for _, model := range models {
		if !strings.EqualFold(strings.TrimSpace(model.Provider), "ollama") || strings.TrimSpace(model.Name) == "" {
			continue
		}
		if requested != "" && !strings.EqualFold(strings.TrimSpace(model.Name), requested) {
			continue
		}
		if !containsFolded(model.Tasks, "generation") {
			continue
		}
		compatible = append(compatible, model)
	}
	sort.SliceStable(compatible, func(i, j int) bool {
		if compatible[i].Loaded != compatible[j].Loaded {
			return compatible[i].Loaded
		}
		left, right := compatible[i].Size, compatible[j].Size
		if left <= 0 {
			left = 1<<63 - 1
		}
		if right <= 0 {
			right = 1<<63 - 1
		}
		if left != right {
			return left < right
		}
		return strings.ToLower(compatible[i].Name) < strings.ToLower(compatible[j].Name)
	})
	return compatible
}

func containsFolded(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}

func printSelftestPlan(plan selftestPlan) {
	printSelftestSection("PLAN")
	for _, target := range plan.Targets {
		fmt.Printf("| ✓ %-7s ready on %s\n", strings.ToUpper(target.Kind), target.NodeName)
		if target.Capacity > 0 {
			fmt.Printf("|   capacity · %d/%d slots busy\n", target.Running, target.Capacity)
		}
		if target.Model != "" {
			state := "available; loads on demand"
			if target.ModelLoaded {
				state = "loaded"
			}
			fmt.Printf("|   model · %s · %s\n", target.Model, state)
		}
	}
}

type selftestJobSpec struct {
	Prompt       string
	RequireImage bool
}

func runSelftestJob(ctx context.Context, cfg config.Config, token string, target selftestTarget, spec selftestJobSpec) (*bridge.Output, cluster.Job, error) {
	request, err := buildSelftestRequest(target, spec, selftestRunID())
	if err != nil {
		return nil, cluster.Job{}, err
	}
	var job cluster.Job
	if err := clusterPOST(ctx, clusterBaseURL(cfg)+"/v1/cluster/jobs?compact=1", token, request, &job); err != nil {
		return nil, job, err
	}
	terminal := false
	defer func() {
		if terminal || job.ID == "" {
			return
		}
		if err := cancelSelftestJob(clusterBaseURL(cfg), token, job.ID); err != nil {
			fmt.Fprintf(os.Stderr, "  ! could not cancel unfinished self-test job %s: %v\n", shortChatID(job.ID), err)
		}
	}()
	ticker := time.NewTicker(450 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, job, ctx.Err()
		case <-ticker.C:
		}
		if err := clusterGET(ctx, clusterBaseURL(cfg)+"/v1/cluster/jobs/"+url.PathEscape(job.ID)+"?compact=1", token, &job); err != nil {
			return nil, job, err
		}
		switch job.Status {
		case cluster.JobCompleted:
			terminal = true
			var submission bridge.Submission
			if err := json.Unmarshal(job.Result, &submission); err != nil {
				return nil, job, fmt.Errorf("decode worker result: %w", err)
			}
			if submission.Output == nil {
				return nil, job, errors.New("worker returned no output")
			}
			if submission.Output.Error != "" {
				return nil, job, errors.New(submission.Output.Error)
			}
			return submission.Output, job, nil
		case cluster.JobFailed, cluster.JobCancelled:
			terminal = true
			if strings.TrimSpace(job.Error) == "" {
				job.Error = job.Status
			}
			return nil, job, errors.New(job.Error)
		}
	}
}

func cancelSelftestJob(relayURL, token, jobID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(relayURL, "/")+"/v1/cluster/jobs/"+url.PathEscape(jobID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := readClusterAPIResponse(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func buildSelftestRequest(target selftestTarget, spec selftestJobSpec, runID string) (cluster.SubmitRequest, error) {
	profile := ""
	provider := "ollama"
	model := target.Model
	metadata := map[string]interface{}{}
	if target.Kind == selftestChatGPT || target.Kind == selftestGemini {
		provider = "browser"
		profile = target.Kind
		model = ""
		// The self-test never edits a currently open personal conversation. Every
		// browser request asks the extension for a fresh, isolated per-job chat.
		metadata["contextbridge_new_chat"] = true
		metadata["contextbridge_new_chat_per_job"] = true
	}
	outputSpec := bridge.OutputSpec{Mode: "text", MaxBytes: 4096}
	if spec.RequireImage {
		metadata["contextbridge_image_tool"] = true
		outputSpec.Artifacts = true
		outputSpec.MaxArtifactBytes = 12 << 20
		outputSpec.MinArtifacts = 1
		outputSpec.MinImages = 1
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	sessionID := "selftest-" + runID + "-" + target.Kind
	payload, err := json.Marshal(bridge.Job{
		Source:         "cluster-selftest",
		Task:           "generation",
		Prompt:         spec.Prompt,
		SessionID:      sessionID,
		BrowserProfile: profile,
		Model:          model,
		Metadata:       metadata,
		Output:         outputSpec,
	})
	if err != nil {
		return cluster.SubmitRequest{}, err
	}
	requirements := cluster.Requirements{Task: "generation", Provider: provider, BrowserProfile: profile, Model: model, SessionID: sessionID, PreferredNodes: []string{target.NodeID}}
	return cluster.SubmitRequest{Source: "cluster-selftest", Requirements: requirements, Payload: payload, MaxAttempts: 1}, nil
}

func selftestRunID() string {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err == nil {
		return strings.ToUpper(hex.EncodeToString(raw))
	}
	return fmt.Sprintf("%X", time.Now().UnixNano())
}

func truncateSelftestText(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum]) + "…"
}
