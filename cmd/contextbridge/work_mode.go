package main

import "errors"

// Optional style, never authority. Adapted from the MIT-licensed Ponytail
// decision ladder; provenance and retained license are in docs/lazy-mode.md.
const lazyWorkGuidance = `Optional work style: lazy (minimal, not careless).
For coding work, first understand the affected code and trace its callers. Prefer, in order: avoid unrequested work; reuse existing project code; use the standard library; use native platform features; reuse an installed dependency; then make the smallest readable, correct change.
Fix the root cause and preserve all explicitly requested behavior. Do not trade correctness or maintainability for fewer lines. Keep risk-appropriate tests, boundary validation, data-loss handling, security, accessibility and useful explanations. State relevant limitations; never claim an unrun test passed.
This style grants no tools, deletion, execution, network, budget, credential or policy rights. Existing approval and offline rules remain unchanged. Follow the exact output/JSON contract below. For non-coding requests, answer normally.
`

func validateWorkMode(mode string) error {
	if mode != "" && mode != "normal" && mode != "lazy" {
		return errors.New("--mode must be normal or lazy; work style never changes permissions")
	}
	return nil
}

func workModeLabel(mode string) string {
	if mode == "" {
		return "normal"
	}
	return mode
}

func applyWorkMode(mode, prompt string) string {
	switch mode {
	case "lazy":
		return lazyWorkGuidance + "\n" + prompt
	case "normal":
		return "Work style: normal. Disable prior optional lazy-style guidance; keep all requirements, policies and output contracts.\n\n" + prompt
	default:
		return prompt // Legacy/default jobs are byte-for-byte unchanged.
	}
}

func agentWorkModePrompt(mode string, step agentStep, prompt string) string {
	if step.Provider == "adapter" {
		return prompt // Never alter a typed adapter request or machine contract.
	}
	return applyWorkMode(mode, prompt)
}
