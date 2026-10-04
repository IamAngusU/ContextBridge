package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/config"
	"github.com/IamAngusU/ContextBridge/internal/localtools"
)

// Confirmation is an additional human gate, not an authorization tier. Neither
// a model-supplied action name nor its self-reported risk can skip this gate.
func validateAgentAsk(mode string) error {
	if mode != "all" && mode != "critical" && mode != "none" {
		return errors.New("--ask must be all, critical, or none")
	}
	return nil
}

func agentNeedsConfirmation(mode string, cfg config.Config, step agentStep) bool {
	if mode == "none" {
		return false
	}
	if mode == "all" {
		return true
	}
	if mode != "critical" {
		return true
	}
	// An adapter may create/replace files or trigger external side effects.
	// Until there is a separately verified adapter effect contract, even an
	// action called 'read' is conservatively treated as critical here.
	if step.Provider == "adapter" {
		return true
	}
	classification, _ := agentProviderPolicy(cfg, step.Provider)
	return classification != "local"
}

type agentConfirmer struct {
	scanner *bufio.Scanner
	output  io.Writer
}

func newAgentConfirmer(input io.Reader, output io.Writer) *agentConfirmer {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), 4096)
	return &agentConfirmer{scanner: scanner, output: output}
}

func (c *agentConfirmer) confirm(ctx context.Context, step agentStep, prompt, text string) error {
	// These are the resolved bytes used for submission, including a dynamic
	// previous-json handoff, not just the original high-level plan instruction.
	body, err := json.Marshal(struct {
		Provider string `json:"provider"`
		Profile  string `json:"profile,omitempty"`
		Prompt   string `json:"prompt"`
		Text     string `json:"text"`
	}{step.Provider, step.Profile, prompt, text})
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	challenge := "yes " + digest[:12]
	fmt.Fprintf(c.output, "\nReview step %s · %s/%s\nPrompt:\n%s\nSubmitted content:\n%s\nRequest sha256:%s\nType %q to submit; anything else stops the run: ",
		step.ID, step.Provider, step.Profile, localtools.TerminalSafe(prompt), localtools.TerminalSafe(text), digest, challenge)
	if err := ctx.Err(); err != nil {
		return err
	}
	type answer struct {
		value string
		err   error
	}
	ready := make(chan answer, 1)
	go func() {
		if !c.scanner.Scan() {
			err := c.scanner.Err()
			if err == nil {
				err = io.EOF
			}
			ready <- answer{err: err}
			return
		}
		ready <- answer{value: strings.TrimSpace(c.scanner.Text())}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case response := <-ready:
		if response.err != nil || response.value != challenge {
			return errors.New("agent step was not confirmed; no job submitted for this step")
		}
		return nil
	}
}
