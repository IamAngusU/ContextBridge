package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	provider := flags.String("provider", "browser", "browser or another generation provider")
	group := flags.String("group", "", "worker group")
	model := flags.String("model", "", "specific model")
	profile := flags.String("profile", "", "browser profile such as chatgpt or gemini")
	reasoning := flags.String("reasoning", "", "reasoning level such as instant, medium, high, xhigh, pro, or max")
	e2ee := flags.Bool("e2ee", false, "encrypt prompts and results end-to-end for the selected worker")
	sessionID := flags.String("session", "", "stable conversation ID")
	prompt := flags.String("prompt", "", "send one turn and exit")
	artifactDir := flags.String("artifacts", "auto", "artifact directory; auto uses local ContextBridge storage, off disables saving")
	minArtifacts := flags.Int("min-artifacts", 0, "require this many verified files in the browser response (0-12)")
	requireImage := flags.Bool("image", false, "require a real returned image file; ask for the image in the prompt")
	minImages := flags.Int("min-images", 0, "require this many verified image files in one browser response (0-12)")
	requireMusic := flags.Bool("music", false, "select Gemini's music tool and require a verified audio/video file")
	attachImage := flags.String("attach-image", "", "attach one local PNG, JPEG, WebP, or GIF image to each turn")
	newChat := flags.Bool("new-chat", false, "open a fresh browser chat for this session")
	newChatPerJob := flags.Bool("new-chat-per-job", false, "open a fresh browser chat for every turn")
	foregroundNewChat := flags.Bool("foreground-new-chat", false, "show a newly opened browser chat while starting this turn")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *minArtifacts < 0 || *minArtifacts > 12 {
		return errors.New("--min-artifacts must be between 0 and 12")
	}
	if *minImages < 0 || *minImages > 12 {
		return errors.New("--min-images must be between 0 and 12")
	}
	if (*requireImage || *minImages > 0) && !strings.EqualFold(*provider, "browser") {
		return errors.New("--image and --min-images require --provider browser")
	}
	if *requireMusic && (!strings.EqualFold(*provider, "browser") || (*profile != "" && !strings.EqualFold(*profile, "gemini"))) {
		return errors.New("--music requires --provider browser and --profile gemini")
	}
	if *requireMusic && (*requireImage || *minImages > 0) {
		return errors.New("--music cannot be combined with --image or --min-images")
	}
	if (*newChat || *newChatPerJob) && !strings.EqualFold(*provider, "browser") {
		return errors.New("--new-chat and --new-chat-per-job require --provider browser")
	}
	if *foregroundNewChat && (!strings.EqualFold(*provider, "browser") || (!*newChat && !*newChatPerJob)) {
		return errors.New("--foreground-new-chat requires --provider browser and --new-chat or --new-chat-per-job")
	}
	if *requireImage && *minImages < 1 {
		*minImages = 1
	}
	if *requireMusic && *profile == "" {
		*profile = "gemini"
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
	if (*minArtifacts > 0 || *minImages > 0 || *requireMusic) && *artifactDir == "" {
		return errors.New("--image, --min-images, --music and --min-artifacts require artifact saving")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	state := &chatState{relayURL: clusterBaseURL(cfg), token: *token, provider: *provider, group: *group, model: *model, profile: *profile, reasoning: *reasoning, e2ee: *e2ee, sessionID: *sessionID, artifactDir: *artifactDir, minArtifacts: *minArtifacts, minImages: *minImages, requireImage: *minImages > 0, requireMusic: *requireMusic, imageBase64: imageBase64, imageMediaType: imageMediaType, newChat: *newChat || *newChatPerJob, newChatPerJob: *newChatPerJob, foregroundNewChat: *foregroundNewChat}

	if strings.TrimSpace(*prompt) != "" {
		return state.turn(ctx, strings.TrimSpace(*prompt))
	}
	fmt.Printf("\n  ContextBridge Chat · %s\n  session %s · follow-ups stay in the same browser conversation unless --new-chat-per-job is set\n  /model, /reasoning, /profile, /image, /min-images, /music, /min-artifacts and /e2ee change this session · /settings shows it · /exit closes it\n\n", *provider, *sessionID)
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
		if err := state.turn(ctx, line); err != nil {
			fmt.Fprintln(os.Stderr, "  !", err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	return scanner.Err()
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
		if s.requireMusic && !strings.EqualFold(value, "gemini") {
			return true, "  ! turn off /music before selecting another browser profile"
		}
		s.profile = value
		return true, "  ✓ browser profile: " + emptyChatSetting(s.profile)
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
			s.requireMusic = false
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
		if count > 0 && (s.artifactDir == "" || !strings.EqualFold(s.provider, "browser")) {
			return true, "  ! image series need browser artifact saving enabled"
		}
		s.minImages = count
		s.requireImage = count > 0
		if count > 0 {
			s.requireMusic = false
		}
		return true, fmt.Sprintf("  ✓ required images: %d", count)
	case "/music":
		switch strings.ToLower(value) {
		case "on", "true", "1", "yes", "an", "ein":
			if s.artifactDir == "" {
				return true, "  ! music mode needs artifact saving enabled"
			}
			if !strings.EqualFold(s.provider, "browser") || (s.profile != "" && !strings.EqualFold(s.profile, "gemini")) {
				return true, "  ! music mode needs the Gemini browser profile"
			}
			s.profile = "gemini"
			s.requireMusic = true
			s.requireImage = false
			s.minImages = 0
		case "off", "false", "0", "no", "aus":
			s.requireMusic = false
		default:
			return true, "  ! use /music on or /music off"
		}
		return true, fmt.Sprintf("  ✓ music file required: %t", s.requireMusic)
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
	case "/settings":
		return true, fmt.Sprintf("  session %s · provider %s · profile %s · model %s · reasoning %s · required images %d · music required %t · required files %d · new chat %t · per job %t · E2EE %t", s.sessionID, s.provider, emptyChatSetting(s.profile), emptyChatSetting(s.model), emptyChatSetting(s.reasoning), s.minImages, s.requireMusic, s.minArtifacts, s.newChat, s.newChatPerJob, s.e2ee)
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
	relayURL          string
	token             string
	provider          string
	group             string
	model             string
	profile           string
	reasoning         string
	e2ee              bool
	sessionID         string
	artifactDir       string
	minArtifacts      int
	minImages         int
	requireImage      bool
	requireMusic      bool
	imageBase64       string
	imageMediaType    string
	newChat           bool
	newChatPerJob     bool
	foregroundNewChat bool
	nodeID            string
}

func (s *chatState) jobMetadata() map[string]interface{} {
	metadata := map[string]interface{}{}
	if s.requireMusic {
		metadata["contextbridge_music_tool"] = true
	}
	if s.newChat || s.newChatPerJob {
		metadata["contextbridge_new_chat"] = true
	}
	if s.newChatPerJob {
		metadata["contextbridge_new_chat_per_job"] = true
	}
	if s.foregroundNewChat {
		metadata["contextbridge_foreground_new_chat"] = true
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func (s *chatState) turn(ctx context.Context, prompt string) error {
	if s.requireMusic && (!strings.EqualFold(s.provider, "browser") || !strings.EqualFold(s.profile, "gemini")) {
		return errors.New("music mode needs the Gemini browser profile")
	}
	if s.requireMusic && (s.requireImage || s.minImages > 0) {
		return errors.New("image and music modes cannot be combined in one turn")
	}
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
	if s.requireMusic {
		minimum = max(minimum, 1)
	}
	minimumImages := 0
	if s.requireImage {
		minimumImages = max(1, s.minImages)
	}
	minimumMedia := 0
	if s.requireMusic {
		minimumMedia = 1
	}
	payload, err := json.Marshal(bridge.Job{
		Source: "terminal-chat", Task: "generation", Prompt: prompt,
		SessionID: s.sessionID, BrowserProfile: s.profile, Model: s.model, Reasoning: s.reasoning,
		ImageBase64: s.imageBase64, ImageMediaType: s.imageMediaType,
		Metadata: s.jobMetadata(),
		Output:   bridge.OutputSpec{Mode: "text", MaxBytes: 1 << 20, Artifacts: s.artifactDir != "", MaxArtifactBytes: 12 << 20, MinArtifacts: minimum, MinImages: minimumImages, MinMedia: minimumMedia},
	})
	if err != nil {
		return err
	}
	requirements := cluster.Requirements{Task: "generation", Provider: s.provider, Group: s.group, SessionID: s.sessionID}
	requirements.Vision = s.imageBase64 != ""
	if !strings.EqualFold(s.provider, "browser") {
		requirements.Model = s.model
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
				return fmt.Errorf("browser job failed: %s", submission.Output.Error)
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
			if s.requireMusic {
				media := 0
				for _, artifact := range submission.Output.Artifacts {
					if artifact.DataBase64 != "" && (strings.HasPrefix(artifact.MediaType, "audio/") || strings.HasPrefix(artifact.MediaType, "video/")) {
						media++
					}
				}
				if media == 0 {
					return errors.New("media_missing: expected 1 audio/video file, received 0")
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
				fmt.Print("  ↳ Tab meldet:")
				if submission.Output.SelectedModel != "" {
					fmt.Printf(" Modell %s", submission.Output.SelectedModel)
				}
				if submission.Output.SelectedReasoning != "" {
					fmt.Printf(" · Denkstufe: %s", submission.Output.SelectedReasoning)
				}
				fmt.Println()
			} else if !strings.EqualFold(s.provider, "browser") && submission.Output.Model != "" {
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
				fmt.Printf("  ↻ session moved from %s to %s; browser context may differ\n", shortChatID(s.nodeID), shortChatID(job.AssignedNode))
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
