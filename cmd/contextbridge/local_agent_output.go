package main

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/IamAngusU/ContextBridge/internal/localtools"
)

func printLocalToolOutcome(out io.Writer, result localtools.Result, err error) error {
	fmt.Fprintf(out, "  → tool: %s · local · no model or pool job\n", result.Tool)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "ai  › %s\n", result.Text)
	fmt.Fprintln(out, "  ↳ used: "+result.Tool+" · verified computation (not an LLM answer)")
	return nil
}

func agentTerminalText(value any, singleLine bool) string {
	text, _ := value.(string)
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
	if singleLine {
		text = strings.Join(strings.Fields(text), " ")
	}
	return text
}

func renderLocalAgentAnswer(out io.Writer, result map[string]any) {
	answer, _ := result["answer"].(map[string]any)
	fmt.Fprintln(out, "ai  › "+agentTerminalText(answer["answer"], false))
	method := agentTerminalText(answer["method"], true)
	label := "Herkunft ungeprüft · Antwort prüfen"
	switch method {
	case "local-math", "rational", "sympy", "static-code":
		label = "lokales Werkzeug"
	case "local-tool-diagnosis":
		label = "lokale Diagnosebelege"
	case "offline-model", "local-model":
		label = "lokale KI · Modellantwort prüfen"
	case "model":
		label = "Modellroute · Modellantwort prüfen"
	case "offline-evidence":
		label = "lokale Quellen · keine abgesicherte Antwort"
	case "local-math-rejected", "unavailable", "tools-only":
		label = "keine abgesicherte Antwort"
	}
	if method == "" {
		method = "unknown"
	}
	fmt.Fprintf(out, "  ↳ used: %s · %s\n", method, label)
	sources, _ := answer["sources"].([]any)
	for index, raw := range sources {
		if index >= 4 {
			break
		}
		source, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		title := agentTerminalText(source["title"], true)
		if title != "" {
			fmt.Fprintln(out, "  ↳ source: "+title)
		}
	}
	if result["requires_review"] == true {
		fmt.Fprintln(out, "  ! Prüfhinweis: Antwort ist nicht unabhängig bestätigt.")
	}
	if duration, ok := result["duration_ms"].(float64); ok && duration >= 0 {
		fmt.Fprintf(out, "  · %.2f s\n", duration/1000)
	}
}
