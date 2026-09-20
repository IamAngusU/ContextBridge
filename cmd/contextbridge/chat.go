package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

func clusterChatCommand(args []string) error {
	flags := flag.NewFlagSet("cluster chat", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	token := flags.String("token", "", "producer token; defaults to client_token, environment, or local admin token")
	provider := flags.String("provider", "adapter", "adapter or another generation provider")
	group := flags.String("group", "", "worker group")
	model := flags.String("model", "", "specific model")
	profile := flags.String("profile", "", "operator-configured adapter profile ID")
	reasoning := flags.String("reasoning", "", "reasoning level such as instant, medium, high, xhigh, pro, or max")
	e2ee := flags.Bool("e2ee", false, "encrypt prompts and results end-to-end for the selected worker")
	sessionID := flags.String("session", "", "stable logical session ID")
	prompt := flags.String("prompt", "", "send one turn and exit")
	artifactDir := flags.String("artifacts", "auto", "artifact directory; auto uses local ContextBridge storage, off disables saving")
	minArtifacts := flags.Int("min-artifacts", 0, "require this many verified files in the adapter response (0-12)")
	requireImage := flags.Bool("image", false, "require a real returned image file; ask for the image in the prompt")
	minImages := flags.Int("min-images", 0, "require this many verified image files in one adapter response (0-12)")
	attachImage := flags.String("attach-image", "", "attach one local PNG, JPEG, WebP, or GIF image to each turn")
	newSession := flags.Bool("new-session", false, "open a fresh adapter endpoint session")
	newSessionPerJob := flags.Bool("new-session-per-job", false, "open a fresh adapter session for every turn")
	foregroundNewSession := flags.Bool("foreground-new-session", false, "ask the adapter to foreground a newly opened session")
	egress := flags.String("egress", "", "execution boundary: local_only or remote_allowed")
	maxCostUSD := flags.Float64("max-cost-usd", 0, "hard remote cost upper bound in USD; unknown pricing fails closed")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		extra := strings.Join(flags.Args(), " ")
		if strings.Trim(extra, `\\`) == "" {
			return errors.New(`unexpected "\\": backslash line continuation works in Bash only; use ^ in Windows CMD, a backtick in PowerShell, or paste the command on one line`)
		}
		return fmt.Errorf("unexpected cluster chat argument %q; pass the message with --prompt or start interactive chat without extra arguments", extra)
	}
	if *minArtifacts < 0 || *minArtifacts > 12 {
		return errors.New("--min-artifacts must be between 0 and 12")
	}
	if *minImages < 0 || *minImages > 12 {
		return errors.New("--min-images must be between 0 and 12")
	}
	if (*requireImage || *minImages > 0) && !strings.EqualFold(*provider, "adapter") {
		return errors.New("--image and --min-images require --provider adapter")
	}
	if (*newSession || *newSessionPerJob) && !strings.EqualFold(*provider, "adapter") {
		return errors.New("--new-session and --new-session-per-job require --provider adapter")
	}
	if strings.TrimSpace(*profile) != "" && !strings.EqualFold(*provider, "adapter") {
		return errors.New("--profile requires --provider adapter")
	}
	if *foregroundNewSession && (!strings.EqualFold(*provider, "adapter") || (!*newSession && !*newSessionPerJob)) {
		return errors.New("--foreground-new-session requires --provider adapter and --new-session or --new-session-per-job")
	}
	if *egress != "" && *egress != "local_only" && *egress != "remote_allowed" {
		return errors.New("--egress must be local_only or remote_allowed")
	}
	if math.IsNaN(*maxCostUSD) || math.IsInf(*maxCostUSD, 0) || *maxCostUSD < 0 || *maxCostUSD > 1_000_000 {
		return errors.New("--max-cost-usd must be between 0 and 1000000")
	}
	if *requireImage && *minImages < 1 {
		*minImages = 1
	}
	var imageBase64, imageMediaType string
	if *attachImage != "" {
		raw, mediaType, err := readChatImage(*attachImage)
		if err != nil {
			return err
		}
		imageMediaType = mediaType
		imageBase64 = base64.StdEncoding.EncodeToString(raw)
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	*token = clusterClientToken(cfg, *token)
	if *token == "" {
		return errors.New("a producer token is required; pass --token, set CONTEXTBRIDGE_CLUSTER_TOKEN, or configure cluster.client_token")
	}
	if *sessionID == "" {
		*sessionID = fmt.Sprintf("terminal-%d", time.Now().Unix())
	}
	if *artifactDir == "auto" {
		*artifactDir = filepath.Join(cfg.Storage.Directory, "artifacts")
	} else if *artifactDir == "off" {
		*artifactDir = ""
	}
	if (*minArtifacts > 0 || *minImages > 0) && *artifactDir == "" {
		return errors.New("--image, --min-images and --min-artifacts require artifact saving")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	state := &chatState{relayURL: clusterBaseURL(cfg), token: *token, provider: *provider, group: *group, model: *model, profile: *profile, reasoning: *reasoning, egress: *egress, maxCostUSD: *maxCostUSD, e2ee: *e2ee, sessionID: *sessionID, artifactDir: *artifactDir, minArtifacts: *minArtifacts, minImages: *minImages, requireImage: *minImages > 0, imageBase64: imageBase64, imageMediaType: imageMediaType, newSession: *newSession || *newSessionPerJob, newSessionPerJob: *newSessionPerJob, foregroundNewSession: *foregroundNewSession}

	if strings.TrimSpace(*prompt) != "" {
		return state.turn(ctx, strings.TrimSpace(*prompt))
	}
	fmt.Printf("\n  ContextBridge Chat · %s\n  session %s · follow-ups stay in the same adapter session unless --new-session-per-job is set\n  /model, /reasoning, /profile, /egress, /max-cost-usd, /image, /min-images, /min-artifacts and /e2ee change this session · /settings shows it · /exit closes it\n\n", *provider, *sessionID)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for {
		fmt.Print("you › ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			break
		}
		if handled, message := state.command(line); handled {
			fmt.Println(message)
			continue
		}
		if looksLikePastedChatFlag(line) {
			fmt.Fprintln(os.Stderr, `  ! this looks like a pasted command option, not a chat message; use /exit and paste the complete command on one line (Bash: \, CMD: ^, PowerShell: backtick)`)
			continue
		}
		if err := state.turn(ctx, line); err != nil {
			fmt.Fprintln(os.Stderr, "  !", err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	return scanner.Err()
}

func looksLikePastedChatFlag(line string) bool {
	first := strings.Fields(strings.TrimSpace(line))
	if len(first) == 0 {
		return false
	}
	if strings.Trim(first[0], `\\`) == "" {
		return true
	}
	for _, name := range []string{"--config", "--token", "--provider", "--group", "--model", "--profile", "--reasoning",
		"--e2ee", "--session", "--prompt", "--artifacts", "--min-artifacts", "--image", "--min-images",
		"--attach-image", "--new-session", "--new-session-per-job", "--foreground-new-session", "--egress", "--max-cost-usd"} {
		if first[0] == name || strings.HasPrefix(first[0], name+"=") {
			return true
		}
	}
	return false
}

func readChatImage(path string) ([]byte, string, error) {
	raw, err := readRegularFileBounded(path, 8<<20)
	if err != nil {
		return nil, "", fmt.Errorf("read attached image: %w", err)
	}
	if len(raw) == 0 {
		return nil, "", errors.New("--attach-image must be a non-empty file no larger than 8 MiB")
	}
	mediaType := http.DetectContentType(raw)
	if mediaType != "image/png" && mediaType != "image/jpeg" && mediaType != "image/webp" && mediaType != "image/gif" {
		return nil, "", errors.New("--attach-image must be a PNG, JPEG, WebP, or GIF file")
	}
	return raw, mediaType, nil
}

func (s *chatState) command(line string) (bool, string) {
	parts := strings.Fields(line)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return false, ""
	}
	value := strings.TrimSpace(strings.TrimPrefix(line, parts[0]))
	switch parts[0] {
	case "/model":
		s.model = value
		return true, "  ✓ model: " + emptyChatSetting(s.model)
	case "/reasoning":
		s.reasoning = value
		return true, "  ✓ reasoning: " + emptyChatSetting(s.reasoning)
	case "/profile":
		s.profile = value
		return true, "  ✓ adapter profile: " + emptyChatSetting(s.profile)
	case "/min-artifacts":
		count, err := strconv.Atoi(value)
		if err != nil || count < 0 || count > 12 {
			return true, "  ! use /min-artifacts 0 through 12"
		}
		if count > 0 && s.artifactDir == "" {
			return true, "  ! file requirements need artifact saving enabled"
		}
		s.minArtifacts = count
		return true, fmt.Sprintf("  ✓ required files: %d", count)
	case "/image":
		switch strings.ToLower(value) {
		case "on", "true", "1", "yes", "an", "ein":
			if s.artifactDir == "" {
				return true, "  ! image mode needs artifact saving enabled"
			}
			s.requireImage = true
			if s.minImages < 1 {
				s.minImages = 1
			}
		case "off", "false", "0", "no", "aus":
			s.requireImage = false
			s.minImages = 0
		default:
			return true, "  ! use /image on or /image off"
		}
		return true, fmt.Sprintf("  ✓ image file required: %t", s.requireImage)
	case "/min-images":
		count, err := strconv.Atoi(value)
		if err != nil || count < 0 || count > 12 {
			return true, "  ! use /min-images 0 through 12"
		}
		if count > 0 && (s.artifactDir == "" || !strings.EqualFold(s.provider, "adapter")) {
			return true, "  ! image series need adapter artifact saving enabled"
		}
		s.minImages = count
		s.requireImage = count > 0
		return true, fmt.Sprintf("  ✓ required images: %d", count)
	case "/e2ee":
		switch strings.ToLower(value) {
		case "on", "true", "1", "yes", "an", "ein":
			s.e2ee = true
		case "off", "false", "0", "no", "aus":
			s.e2ee = false
		case "":
		default:
			return true, "  ! use /e2ee on or /e2ee off"
		}
		return true, fmt.Sprintf("  ✓ E2EE: %t", s.e2ee)
	case "/egress":
		if value != "" && value != "local_only" && value != "remote_allowed" {
			return true, "  ! use /egress local_only, /egress remote_allowed, or /egress with no value"
		}
		s.egress = value
		return true, "  ✓ egress: " + emptyChatSetting(s.egress)
	case "/max-cost-usd":
		if value == "" || value == "off" {
			s.maxCostUSD = 0
			return true, "  ✓ cost budget: off"
		}
		budget, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(budget) || math.IsInf(budget, 0) || budget <= 0 || budget > 1_000_000 {
			return true, "  ! use /max-cost-usd VALUE from above 0 through 1000000, or off"
		}
		s.maxCostUSD = budget
		return true, fmt.Sprintf("  ✓ cost budget: %.6f USD", budget)
	case "/settings":
		return true, fmt.Sprintf("  session %s · provider %s · profile %s · model %s · reasoning %s · egress %s · max cost %.6f USD · required images %d · required files %d · fresh session %t · per job %t · E2EE %t", s.sessionID, s.provider, emptyChatSetting(s.profile), emptyChatSetting(s.model), emptyChatSetting(s.reasoning), emptyChatSetting(s.egress), s.maxCostUSD, s.minImages, s.minArtifacts, s.newSession, s.newSessionPerJob, s.e2ee)
	default:
		return false, ""
	}
}

func emptyChatSetting(value string) string {
	if strings.TrimSpace(value) == "" {
		return "auto"
	}
	return strings.TrimSpace(value)
}

type chatState struct {
	relayURL             string
	token                string
	provider             string
	group                string
	model                string
	profile              string
	reasoning            string
	egress               string
	maxCostUSD           float64
	e2ee                 bool
	sessionID            string
	artifactDir          string
	minArtifacts         int
	minImages            int
	requireImage         bool
	imageBase64          string
	imageMediaType       string
	newSession           bool
	newSessionPerJob     bool
	foregroundNewSession bool
	nodeID               string
}

func (s *chatState) jobMetadata() map[string]interface{} {
	metadata := map[string]interface{}{}
	if s.newSession || s.newSessionPerJob {
		metadata["contextbridge_new_session"] = true
	}
	if s.newSessionPerJob {
		metadata["contextbridge_new_session_per_job"] = true
	}
	if s.foregroundNewSession {
		metadata["contextbridge_foreground_new_session"] = true
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func (s *chatState) turn(ctx context.Context, prompt string) error {
	fmt.Printf("  → angefragt: %s", s.provider)
	if s.profile != "" {
		fmt.Printf(" / %s", s.profile)
	}
	if s.model != "" {
		fmt.Printf(" · Modell %s", s.model)
	}
	if s.reasoning != "" {
		fmt.Printf(" · Denkstufe %s", s.reasoning)
	}
	fmt.Println()
	minimum := s.minArtifacts
	if s.requireImage || s.minImages > 0 {
		minimum = max(minimum, max(1, s.minImages))
	}
	minimumImages := 0
	if s.requireImage {
		minimumImages = max(1, s.minImages)
	}
	payload, err := json.Marshal(bridge.Job{
		Source: "terminal-chat", Task: "generation", Prompt: prompt,
		SessionID: s.sessionID, AdapterProfile: s.profile, Model: s.model, Reasoning: s.reasoning,
		MaxCostUSD:  s.maxCostUSD,
		ImageBase64: s.imageBase64, ImageMediaType: s.imageMediaType,
		Metadata: s.jobMetadata(),
		Output:   bridge.OutputSpec{Mode: "text", MaxBytes: 1 << 20, Artifacts: s.artifactDir != "", MaxArtifactBytes: 12 << 20, MinArtifacts: minimum, MinImages: minimumImages},
	})
	if err != nil {
		return err
	}
	requirements := cluster.Requirements{Task: "generation", Provider: s.provider, AdapterProfile: s.profile, Group: s.group, SessionID: s.sessionID}
	requirements.Egress = s.egress
	requirements.MaxCostUSD = s.maxCostUSD
	requirements.Vision = s.imageBase64 != ""
	requirements.Model = s.model
	if strings.EqualFold(s.provider, "adapter") {
		requirements.Reasoning = s.reasoning
		requirements.AdapterFreshSession = s.newSession || s.newSessionPerJob
		requirements.AdapterEphemeralSession = s.newSessionPerJob
	}
	input := cluster.SubmitRequest{
		Source:       "terminal-chat",
		Requirements: requirements,
		Payload:      payload,
		MaxAttempts:  1,
	}
	shared := ""
	encryptionContext := cluster.EncryptionContext{}
	if s.e2ee {
		var reservation cluster.AssignmentResponse
		assignmentRequest := cluster.AssignmentRequest{TenantID: input.TenantID, Requirements: requirements}
		if err := clusterPOST(ctx, s.relayURL+"/v1/cluster/assign", s.token, assignmentRequest, &reservation); err != nil {
			return fmt.Errorf("reserve E2EE worker: %w", err)
		}
		encryptionContext, err = cluster.ValidateAssignmentResponse(assignmentRequest, reservation, time.Now().UTC())
		if err != nil {
			return err
		}
		envelope, sharedKey, err := cluster.SealFor(reservation.Assignment.PublicKey, payload, cluster.JobAAD(encryptionContext))
		if err != nil {
			return err
		}
		shared = sharedKey
		input.ID = reservation.Assignment.JobID
		input.TenantID = reservation.Assignment.TenantID
		input.Requirements = reservation.Assignment.Requirements
		input.Payload = nil
		input.Sealed = envelope
		input.AssignmentID = reservation.Assignment.ID
		input.AssignmentSecret = reservation.Secret
	}
	var job cluster.Job
	if err := clusterPOST(ctx, s.relayURL+"/v1/cluster/jobs?compact=1", s.token, input, &job); err != nil {
		return err
	}
	if s.e2ee {
		if err := cluster.ValidateEncryptedJobContext(encryptionContext, job); err != nil {
			return err
		}
	}
	spinner := newChatSpinner("Queued · waiting for a worker")
	if s.e2ee {
		spinner.update("encrypted", "E2EE · waiting for encrypted final result", 0)
	}
	defer spinner.stop()
	lastProgress := ""
	lastSequence := uint64(0)
	streamed := false
	started := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(450 * time.Millisecond):
		}
		if err := clusterGET(ctx, s.relayURL+"/v1/cluster/jobs/"+url.PathEscape(job.ID)+"?compact=1", s.token, &job); err != nil {
			return err
		}
		if s.e2ee {
			if err := cluster.ValidateEncryptedJobContext(encryptionContext, job); err != nil {
				return err
			}
		}
		if !s.e2ee && job.Progress != nil && job.Progress.Sequence > lastSequence {
			spinner.update(job.Progress.Phase, job.Progress.Detail, job.Progress.Percent)
			if !streamed {
				if strings.TrimSpace(job.Progress.Text) != "" {
					spinner.stop()
					fmt.Print("ai  › ")
				}
			}
			current := job.Progress.Text
			if strings.TrimSpace(current) == "" {
				lastSequence = job.Progress.Sequence
			} else if strings.HasPrefix(current, lastProgress) {
				fmt.Print(strings.TrimPrefix(current, lastProgress))
			} else {
				if streamed {
					fmt.Print("\n      ")
				}
				fmt.Print(current)
			}
			if strings.TrimSpace(current) != "" {
				lastProgress, lastSequence, streamed = current, job.Progress.Sequence, true
			}
		}
		switch job.Status {
		case cluster.JobCompleted:
			spinner.stop()
			rawResult := job.Result
			if s.e2ee {
				if job.SealedResult == nil {
					return errors.New("worker returned no encrypted result")
				}
				rawResult, err = cluster.OpenResponse(shared, job.SealedResult, cluster.ResultAAD(encryptionContext))
				if err != nil {
					return fmt.Errorf("decrypt result: %w", err)
				}
			}
			var submission bridge.Submission
			if err := json.Unmarshal(rawResult, &submission); err != nil {
				return fmt.Errorf("decode result: %w", err)
			}
			if submission.Output == nil {
				return errors.New("worker returned no text output")
			}
			if submission.Output.Error != "" {
				if streamed {
					fmt.Println()
				}
				return fmt.Errorf("adapter job failed: %s", submission.Output.Error)
			}
			verifiedFiles := 0
			for _, artifact := range submission.Output.Artifacts {
				if artifact.DataBase64 != "" {
					verifiedFiles++
				}
			}
			if verifiedFiles < minimum {
				if streamed {
					fmt.Println()
				}
				return fmt.Errorf("artifacts_missing: expected %d file(s), received %d", minimum, verifiedFiles)
			}
			if minimumImages > 0 {
				images := 0
				for _, artifact := range submission.Output.Artifacts {
					if artifact.DataBase64 != "" && strings.HasPrefix(artifact.MediaType, "image/") {
						images++
					}
				}
				if images < minimumImages {
					if streamed {
						fmt.Println()
					}
					return fmt.Errorf("images_missing: expected %d image(s), received %d", minimumImages, images)
				}
			}
			if !streamed || submission.Output.Text != lastProgress {
				if streamed {
					fmt.Print("\n      ")
				} else {
					fmt.Print("ai  › ")
				}
				fmt.Print(submission.Output.Text)
			}
			fmt.Println()
			if submission.Output.Truncated {
				fmt.Fprintln(os.Stderr, "! response reached output.max_bytes and is incomplete")
			}
			if submission.Output.SelectedModel != "" || submission.Output.SelectedReasoning != "" {
				fmt.Print("  ↳ Endpoint reports:")
				if submission.Output.SelectedModel != "" {
					fmt.Printf(" Modell %s", submission.Output.SelectedModel)
				}
				if submission.Output.SelectedReasoning != "" {
					fmt.Printf(" · Denkstufe: %s", submission.Output.SelectedReasoning)
				}
				fmt.Println()
			} else if !strings.EqualFold(s.provider, "adapter") && submission.Output.Model != "" {
				fmt.Printf("  ↳ verwendet: %s · %s\n", submission.Output.Provider, submission.Output.Model)
			}
			paths, references, err := saveOutputArtifacts(submission.Output, s.artifactDir)
			if err != nil {
				return err
			}
			reportSavedArtifacts(paths, references)
			for _, artifact := range submission.Output.Artifacts {
				if artifact.URL != "" {
					fmt.Printf("      ↳ %s · %s\n", artifact.Name, artifact.URL)
				}
			}
			if s.nodeID != "" && s.nodeID != job.AssignedNode {
				fmt.Printf("  ↻ session moved from %s to %s; adapter context may differ\n", shortChatID(s.nodeID), shortChatID(job.AssignedNode))
			}
			s.nodeID = job.AssignedNode
			fmt.Printf("  ✓ %.1fs · %s\n\n", time.Since(started).Seconds(), shortChatID(job.AssignedNode))
			return nil
		case cluster.JobFailed, cluster.JobCancelled:
			if streamed {
				fmt.Println()
			}
			return fmt.Errorf("job %s: %s", job.Status, job.Error)
		}
	}
}

func clusterClientToken(cfg config.Config, explicit string) string {
	for _, value := range []string{explicit, os.Getenv("CONTEXTBRIDGE_CLUSTER_TOKEN"), cfg.Cluster.ClientToken, cfg.Cluster.Relay.AdminToken} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type chatSpinner struct {
	mu          sync.Mutex
	done        chan struct{}
	stopped     chan struct{}
	interactive bool
	label       string
	percent     int
	once        sync.Once
}

func newChatSpinner(label string) *chatSpinner {
	interactive := false
	if info, err := os.Stderr.Stat(); err == nil {
		interactive = info.Mode()&os.ModeCharDevice != 0
	}
	spinner := &chatSpinner{done: make(chan struct{}), stopped: make(chan struct{}), interactive: interactive, label: label}
	if !interactive {
		fmt.Fprintln(os.Stderr, "  ·", label)
		close(spinner.stopped)
		return spinner
	}
	go func() {
		defer close(spinner.stopped)
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			select {
			case <-spinner.done:
				spinner.mu.Lock()
				fmt.Fprint(os.Stderr, "\r\x1b[2K")
				spinner.mu.Unlock()
				return
			case <-ticker.C:
				spinner.mu.Lock()
				bar := "━━━───────────"
				if spinner.percent > 0 {
					filled := spinner.percent * 14 / 100
					bar = strings.Repeat("━", filled) + strings.Repeat("─", 14-filled)
				}
				fmt.Fprintf(os.Stderr, "\r\x1b[2K  %s  [%s]  %s", frames[frame%len(frames)], bar, spinner.label)
				spinner.mu.Unlock()
				frame++
			}
		}
	}()
	return spinner
}

func (s *chatSpinner) update(phase, detail string, percent int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	label := strings.TrimSpace(detail)
	if label == "" {
		label = strings.ReplaceAll(strings.TrimSpace(phase), "_", " ")
	}
	if label != "" {
		label = strings.ToUpper(label[:1]) + label[1:]
		s.label = label
	}
	if percent >= 0 && percent <= 100 {
		s.percent = percent
	}
}

func (s *chatSpinner) stop() {
	s.once.Do(func() {
		if s.interactive {
			close(s.done)
			<-s.stopped
		}
	})
}

func shortChatID(value string) string {
	if len(value) <= 18 {
		return value
	}
	return value[:12] + "…"
}
