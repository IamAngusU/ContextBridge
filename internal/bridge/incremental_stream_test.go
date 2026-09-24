package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestIncrementalProviderRequiresDoneAndDoesNotPublishFinalOutput(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer provider.Close()
	processor := incrementalTestProcessor(provider.URL, nil)
	deltas := []string{}
	output := processor.ProcessIncremental(context.Background(), Job{Route: "default", Prompt: "test", Output: OutputSpec{Mode: "text"}}, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if len(deltas) != 1 || deltas[0] != "partial" {
		t.Fatalf("ordered partial delta was lost: %#v", deltas)
	}
	if output.Error != "providers_unavailable" || output.Text != "" {
		t.Fatalf("incomplete provider stream became authoritative: %#v", output)
	}
}

func TestIncrementalProviderStopsWhenDownstreamRejectsDelta(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	processor := incrementalTestProcessor(provider.URL, nil)
	calls := 0
	output := processor.ProcessIncremental(context.Background(), Job{Route: "default", Prompt: "test", Output: OutputSpec{Mode: "text"}}, func(string) error {
		calls++
		return errors.New("downstream disconnected")
	})
	if calls != 1 || output.Error != "providers_unavailable" || output.Text != "" {
		t.Fatalf("downstream backpressure/cancellation failed: calls=%d output=%#v", calls, output)
	}
}

func TestIncrementalProviderRejectsEventFloodAndFallbackRoutes(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for index := 0; index < 4097; index++ {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[]}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	processor := incrementalTestProcessor(provider.URL, []string{"backup"})
	job := Job{Route: "default", Prompt: "test", Output: OutputSpec{Mode: "text"}}
	if processor.SupportsIncremental(job) {
		t.Fatal("fallback route was advertised as safe after partial output")
	}
	processor = incrementalTestProcessor(provider.URL, nil)
	output := processor.ProcessIncremental(context.Background(), job, func(string) error { return nil })
	if output.Error != "providers_unavailable" {
		t.Fatalf("event flood escaped the bound: %#v", output)
	}
}

func TestIncrementalProviderFinalTextExactlyMatchesPublishedDeltas(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, text := range []string{"  hello", " ", "world", "  "} {
			raw, _ := json.Marshal(map[string]interface{}{"choices": []map[string]interface{}{{"delta": map[string]string{"content": text}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	processor := incrementalTestProcessor(provider.URL, nil)
	var published strings.Builder
	output := processor.ProcessIncremental(context.Background(), Job{Route: "default", Prompt: "test", Output: OutputSpec{Mode: "text"}}, func(delta string) error {
		published.WriteString(delta)
		return nil
	})
	if output.Error != "" || output.Text != "hello world" || published.String() != output.Text {
		t.Fatalf("published=%q final=%#v", published.String(), output)
	}
}

func TestOpenAISSEAcceptsCRLFAndRejectsOversizedEvent(t *testing.T) {
	seen := []string{}
	err := scanOpenAISSE(strings.NewReader("data: one\r\n\r\ndata: two\r\n\r\n"), func(data []byte) error {
		seen = append(seen, string(data))
		return nil
	})
	if err != nil || strings.Join(seen, ",") != "one,two" {
		t.Fatalf("CRLF SSE parse = %#v err=%v", seen, err)
	}
	oversized := "data: " + strings.Repeat("x", (1<<20)+1) + "\n\n"
	if err := scanOpenAISSE(strings.NewReader(oversized), func([]byte) error { return nil }); err == nil {
		t.Fatal("oversized SSE event was accepted")
	}
}

func incrementalTestProcessor(providerURL string, fallback []string) *Processor {
	return NewProcessor(config.Config{
		Routes: map[string]config.Route{"default": {Provider: "remote", Fallback: fallback}},
		Engines: map[string]config.Engine{
			"remote": {Type: "openai_compatible", URL: providerURL + "/v1", Model: "stream-model", Capabilities: []string{"text", "incremental_output"}, TimeoutSeconds: 5},
			"backup": {Type: "openai_compatible", URL: providerURL + "/v1", Model: "backup-model", Capabilities: []string{"text"}, TimeoutSeconds: 5},
		},
	}, nil)
}
