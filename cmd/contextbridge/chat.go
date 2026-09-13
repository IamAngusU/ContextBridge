package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
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
	sessionID := flags.String("session", "", "stable conversation ID")
	prompt := flags.String("prompt", "", "send one turn and exit")
	artifactDir := flags.String("artifacts", "auto", "artifact directory; auto uses local ContextBridge storage, off disables saving")
	if err := flags.Parse(args); err != nil {
		return err
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	state := &chatState{relayURL: clusterBaseURL(cfg), token: *token, provider: *provider, group: *group, model: *model, sessionID: *sessionID, artifactDir: *artifactDir}

	if strings.TrimSpace(*prompt) != "" {
		return state.turn(ctx, strings.TrimSpace(*prompt))
	}
	fmt.Printf("\n  ContextBridge Chat · %s\n  session %s · follow-ups stay in the same browser conversation\n  /exit closes this terminal session\n\n", *provider, *sessionID)
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
		if err := state.turn(ctx, line); err != nil {
			fmt.Fprintln(os.Stderr, "  !", err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	return scanner.Err()
}

type chatState struct {
	relayURL    string
	token       string
	provider    string
	group       string
	model       string
	sessionID   string
	artifactDir string
	nodeID      string
}

func (s *chatState) turn(ctx context.Context, prompt string) error {
	payload, err := json.Marshal(bridge.Job{
		Source: "terminal-chat", Task: "generation", Prompt: prompt,
		Output: bridge.OutputSpec{Mode: "text", MaxBytes: 1 << 20, Artifacts: true, MaxArtifactBytes: 6 << 20},
	})
	if err != nil {
		return err
	}
	input := cluster.SubmitRequest{
		Source:       "terminal-chat",
		Requirements: cluster.Requirements{Task: "generation", Provider: s.provider, Group: s.group, Model: s.model, SessionID: s.sessionID},
		Payload:      payload,
	}
	var job cluster.Job
	if err := clusterPOST(ctx, s.relayURL+"/v1/cluster/jobs", s.token, input, &job); err != nil {
		return err
	}
	spinner := newChatSpinner("Queued · waiting for a worker")
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
		if err := clusterGET(ctx, s.relayURL+"/v1/cluster/jobs/"+url.PathEscape(job.ID), s.token, &job); err != nil {
			return err
		}
		if job.Progress != nil && job.Progress.Sequence > lastSequence {
			if !streamed {
				spinner.stop()
				fmt.Print("ai  › ")
			}
			current := job.Progress.Text
			if strings.HasPrefix(current, lastProgress) {
				fmt.Print(strings.TrimPrefix(current, lastProgress))
			} else {
				if streamed {
					fmt.Print("\n      ")
				}
				fmt.Print(current)
			}
			lastProgress, lastSequence, streamed = current, job.Progress.Sequence, true
		}
		switch job.Status {
		case cluster.JobCompleted:
			spinner.stop()
			var submission bridge.Submission
			if err := json.Unmarshal(job.Result, &submission); err != nil {
				return fmt.Errorf("decode result: %w", err)
			}
			if submission.Output == nil {
				return errors.New("worker returned no text output")
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
	once        sync.Once
}

func newChatSpinner(label string) *chatSpinner {
	interactive := false
	if info, err := os.Stderr.Stat(); err == nil {
		interactive = info.Mode()&os.ModeCharDevice != 0
	}
	spinner := &chatSpinner{done: make(chan struct{}), stopped: make(chan struct{}), interactive: interactive}
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
				fmt.Fprintf(os.Stderr, "\r\x1b[2K  %s  [━━━───────────]  %s", frames[frame%len(frames)], label)
				spinner.mu.Unlock()
				frame++
			}
		}
	}()
	return spinner
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
