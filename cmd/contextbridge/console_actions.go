package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/terminalui"
)

const (
	maximumConsoleFollowers   = 4
	maximumConsoleResultRunes = 3000
	consoleActionTimeout      = 8 * time.Second
	consoleFollowInterval     = 600 * time.Millisecond
)

// consoleActionCredential intentionally excludes relay.admin_token. Opening a
// console must never turn an observer/local-service credential into mutation
// authority. Operators opt in with an exact producer credential instead.
func consoleActionCredential(cfg config.Config, explicit string) (token, source string) {
	if value := strings.TrimSpace(explicit); value != "" {
		return value, "--token"
	}
	if value := strings.TrimSpace(os.Getenv("CONTEXTBRIDGE_CLUSTER_TOKEN")); value != "" {
		return value, "CONTEXTBRIDGE_CLUSTER_TOKEN"
	}
	if value := strings.TrimSpace(cfg.Cluster.ClientToken); value != "" {
		return value, "cluster.client_token"
	}
	return "", ""
}

func consoleCommandLimits(ctx context.Context, client *clusterAPIClient, cfg config.Config) terminalui.ConsoleCommandLimits {
	limits := terminalui.ConsoleCommandLimits{
		MaxPromptCharacters: cfg.Terminal.MaxPromptCharacters,
		SessionProvider:     "adapter",
	}
	request, err := buildClusterTextSubmitRequest(clusterTextJobOptions{
		Source: "console", Output: bridge.OutputSpec{Mode: "text", MaxBytes: 1 << 20}, MaxAttempts: 1,
	})
	if err == nil && len(request.Payload) >= 2 {
		// Prompt is a required JSON string field. Subtract the encoded empty
		// string ("") so the renderer can calculate the exact payload size for
		// every typed prompt, including JSON escaping and UTF-8.
		limits.PayloadOverheadBytes = len(request.Payload) - 2
	}
	if client != nil {
		if manifest, err := client.Protocol(ctx); err == nil && manifest.Schema == cluster.ProtocolManifestV1 {
			maximum := manifest.Limits.MaximumConfiguredJobPayloadBytes
			if maximum > 0 && maximum <= int64(^uint(0)>>1) {
				limits.MaxPayloadBytes = int(maximum)
			}
		}
	}
	return limits
}

func runConsoleActions(ctx context.Context, client *clusterAPIClient, intents <-chan terminalui.ConsoleIntent, session *terminalui.Session) {
	if client == nil || intents == nil || session == nil {
		return
	}
	followers := make(chan struct{}, maximumConsoleFollowers)
	for {
		select {
		case <-ctx.Done():
			return
		case intent, ok := <-intents:
			if !ok {
				return
			}
			handleConsoleIntent(ctx, client, intent, session, followers)
		}
	}
}

func handleConsoleIntent(parent context.Context, client *clusterAPIClient, intent terminalui.ConsoleIntent, session *terminalui.Session, followers chan struct{}) {
	ctx, cancel := context.WithTimeout(parent, consoleActionTimeout)
	defer cancel()
	switch intent.Action {
	case terminalui.ConsoleIntentSend:
		job, err := submitConsoleIntent(ctx, client, intent)
		if err != nil {
			session.SetCommandNotice("Send rejected · " + consoleActionError(err))
			return
		}
		select {
		case followers <- struct{}{}:
			session.SetCommandNotice(fmt.Sprintf("Job %s accepted · following authoritative relay events", job.ID))
			go func() {
				defer func() { <-followers }()
				followConsoleJob(parent, client, job.ID, session)
			}()
		default:
			session.SetCommandNotice(fmt.Sprintf("Job %s accepted · follower limit reached; use `job %s` or `result %s`", job.ID, job.ID, job.ID))
		}
	case terminalui.ConsoleIntentJobs:
		jobs, err := client.Jobs(ctx, 8)
		if err != nil {
			session.SetCommandNotice("Jobs unavailable · " + consoleActionError(err))
			return
		}
		session.SetCommandNotice(consoleJobsNotice(jobs))
	case terminalui.ConsoleIntentJob:
		job, err := client.Job(ctx, intent.Argument)
		if err != nil {
			session.SetCommandNotice("Job unavailable · " + consoleActionError(err))
			return
		}
		session.SetCommandNotice(consoleJobNotice(job))
	case terminalui.ConsoleIntentResult:
		job, err := client.Job(ctx, intent.Argument)
		if err != nil {
			session.SetCommandNotice("Result unavailable · " + consoleActionError(err))
			return
		}
		session.SetCommandNotice(consoleResultNotice(job))
	case terminalui.ConsoleIntentCancel:
		job, err := client.Cancel(ctx, intent.Argument)
		if err != nil {
			session.SetCommandNotice("Cancellation rejected · " + consoleActionError(err))
			return
		}
		session.SetCommandNotice(fmt.Sprintf("Job %s is %s · relay state is authoritative; cancellation does not prove provider execution stopped", job.ID, job.Status))
	}
}

func submitConsoleText(ctx context.Context, client *clusterAPIClient, prompt string) (cluster.Job, error) {
	return submitConsoleIntent(ctx, client, terminalui.ConsoleIntent{Action: terminalui.ConsoleIntentSend, Argument: prompt})
}

func submitConsoleIntent(ctx context.Context, client *clusterAPIClient, intent terminalui.ConsoleIntent) (cluster.Job, error) {
	idempotencyKey, err := consoleIdempotencyKey()
	if err != nil {
		return cluster.Job{}, err
	}
	request, err := buildClusterTextSubmitRequest(clusterTextJobOptions{
		Source: "console", Prompt: intent.Argument,
		Provider: intent.Submit.Provider, Group: intent.Submit.Group, Model: intent.Submit.Model,
		SessionID: intent.Submit.SessionID, AdapterProfile: intent.Submit.Profile, Reasoning: intent.Submit.Reasoning,
		Egress: intent.Submit.Egress, MaxCostUSD: intent.Submit.MaxCostUSD,
		AdapterFreshSession: intent.Submit.FreshSession, AdapterEphemeralSession: intent.Submit.EphemeralSession,
		Output: bridge.OutputSpec{Mode: "text", MaxBytes: 1 << 20}, MaxAttempts: 1,
	})
	if err != nil {
		return cluster.Job{}, err
	}
	job, err := client.Submit(ctx, request, idempotencyKey)
	if err == nil || ctx.Err() != nil {
		return job, err
	}
	var responseError *clusterAPIError
	if errors.As(err, &responseError) {
		// The relay definitely returned a response. Admission failures and 5xx
		// responses stay visible instead of being replayed speculatively.
		return cluster.Job{}, err
	}
	// A transport loss can occur after durable admission but before the client
	// sees the response. Repeating the exact plaintext request once with the
	// same producer-scoped idempotency key recovers that one logical send; the
	// relay returns the original job or rejects a conflicting body.
	return client.Submit(ctx, request, idempotencyKey)
}

func consoleIdempotencyKey() (string, error) {
	random := make([]byte, 16)
	if _, err := cryptorand.Read(random); err != nil {
		return "", fmt.Errorf("cannot create idempotency key: %w", err)
	}
	return "console-" + hex.EncodeToString(random), nil
}

func followConsoleJob(ctx context.Context, client *clusterAPIClient, jobID string, session *terminalui.Session) {
	cursor := uint64(0)
	consecutiveFailures := 0
	ticker := time.NewTicker(consoleFollowInterval)
	defer ticker.Stop()
	for {
		requestCtx, cancel := context.WithTimeout(ctx, consoleActionTimeout)
		page, err := client.Events(requestCtx, jobID, cursor, 100)
		cancel()
		if err != nil {
			consecutiveFailures++
			if consecutiveFailures >= 3 {
				session.SetCommandNotice(fmt.Sprintf("Job %s remains retained · event follow interrupted: %s · use `job %s`", jobID, consoleActionError(err), jobID))
				return
			}
		} else {
			consecutiveFailures = 0
			if page.Gap {
				session.SetCommandNotice(fmt.Sprintf("Job %s event history has a retention gap · fetching current authoritative state", jobID))
			}
			if page.Next > cursor {
				cursor = page.Next
			}
			for _, event := range page.Events {
				if event.Authority != "authoritative" {
					continue
				}
				session.SetCommandNotice(consoleEventNotice(jobID, event))
				if terminalConsoleEvent(event.Type) {
					requestCtx, cancel := context.WithTimeout(ctx, consoleActionTimeout)
					job, getErr := client.Job(requestCtx, jobID)
					cancel()
					if getErr != nil {
						session.SetCommandNotice(fmt.Sprintf("Job %s reached %s · retained result lookup failed: %s", jobID, event.Type, consoleActionError(getErr)))
						return
					}
					session.SetCommandNotice(consoleResultNotice(job))
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func terminalConsoleEvent(eventType string) bool {
	switch eventType {
	case "job.completed", "job.failed", "job.cancelled", "job.ambiguous":
		return true
	default:
		return false
	}
}

func consoleEventNotice(jobID string, event cluster.JobEvent) string {
	state := strings.ReplaceAll(strings.TrimPrefix(event.Type, "job."), ".", " ")
	state = strings.ReplaceAll(strings.TrimPrefix(state, "worker."), ".", " ")
	state = strings.ReplaceAll(strings.TrimPrefix(state, "execution."), ".", " ")
	if event.NodeID != "" {
		return fmt.Sprintf("Job %s · %s · node %s · authoritative relay event %d", jobID, state, event.NodeID, event.Sequence)
	}
	return fmt.Sprintf("Job %s · %s · authoritative relay event %d", jobID, state, event.Sequence)
}

func consoleJobsNotice(jobs []cluster.Job) string {
	return consoleJobsNoticeAt(jobs, time.Now().UTC())
}

func consoleJobsNoticeAt(jobs []cluster.Job, now time.Time) string {
	if len(jobs) == 0 {
		return "No jobs are visible to this producer credential."
	}
	lines := []string{"YOUR RECENT JOBS · request/result content omitted"}
	for _, job := range jobs {
		lines = append(lines, fmt.Sprintf("%s · %s%s · %s", job.ID, job.Status, consoleJobTimingSuffix(job, now), emptyLabel(job.Requirements.Task, "task unavailable")))
	}
	return strings.Join(lines, "\n")
}

func consoleJobNotice(job cluster.Job) string {
	return consoleJobNoticeAt(job, time.Now().UTC())
}

func consoleJobNoticeAt(job cluster.Job, now time.Time) string {
	timing := cluster.AuthoritativeJobTimingAt(job, now)
	lines := []string{fmt.Sprintf("JOB %s · %s%s", job.ID, job.Status, consoleJobTimingSuffix(job, now))}
	lines = append(lines, "task · "+emptyLabel(job.Requirements.Task, "unavailable"))
	if job.Pipeline != "" {
		lines = append(lines, "pipeline · "+job.Pipeline)
	}
	if job.Step != "" {
		lines = append(lines, "step · "+job.Step)
	}
	if job.AssignedNode != "" {
		lines = append(lines, "node · "+job.AssignedNode)
	}
	if timing.QueueAvailable {
		lines = append(lines, "queue · "+terminalui.CompactDuration(timing.Queue))
	}
	if timing.ExecutionAvailable {
		label := "execution"
		if !timing.Terminal {
			label += " elapsed"
		}
		lines = append(lines, label+" · "+terminalui.CompactDuration(timing.Execution))
	}
	if job.FailureCode != "" {
		lines = append(lines, "failure · "+job.FailureCode)
	}
	lines = append(lines, "content · hidden here; use `result "+job.ID+"` for the retained text result")
	return strings.Join(lines, "\n")
}

func consoleResultNotice(job cluster.Job) string {
	heading := fmt.Sprintf("JOB %s · %s%s", job.ID, job.Status, consoleJobTimingSuffix(job, time.Now().UTC()))
	if job.Status != cluster.JobCompleted {
		if job.FailureCode != "" {
			return heading + " · " + job.FailureCode
		}
		if job.Error != "" {
			return heading + " · " + boundedConsoleText(job.Error)
		}
		return heading + " · no completed result retained"
	}
	if job.SealedResult != nil {
		return heading + " · encrypted result retained; use the originating E2EE client to decrypt it"
	}
	var submission bridge.Submission
	if err := json.Unmarshal(job.Result, &submission); err != nil || submission.Output == nil {
		return heading + " · retained result is not a text submission"
	}
	if submission.Output.Error != "" {
		return heading + " · output error: " + boundedConsoleText(submission.Output.Error)
	}
	if strings.TrimSpace(submission.Output.Text) == "" {
		return heading + " · completed without text output"
	}
	text := boundedConsoleText(submission.Output.Text)
	if submission.Output.Truncated {
		text += "\n[worker marked this result truncated at output.max_bytes]"
	}
	return heading + "\n" + text
}

func consoleJobTimingSuffix(job cluster.Job, now time.Time) string {
	timing := cluster.AuthoritativeJobTimingAt(job, now)
	if !timing.ElapsedAvailable {
		return ""
	}
	label := "elapsed"
	if timing.Terminal {
		label = "execution"
	}
	return " · " + label + " " + terminalui.CompactDuration(timing.Elapsed)
}

func boundedConsoleText(value string) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maximumConsoleResultRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximumConsoleResultRunes]) + "…\n[console display truncated; retained result is unchanged]"
}

func consoleActionError(err error) string {
	if err == nil {
		return "unknown error"
	}
	return boundedConsoleText(err.Error())
}
