package bridge

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestBrowserJobRoundTrip(t *testing.T) {
	cfg := config.Config{
		Version: 1,
		Server:  config.Server{Listen: "127.0.0.1:32145", Token: "test-token-that-is-long-enough"},
		Storage: config.Storage{Directory: t.TempDir(), Inbox: t.TempDir()},
		Routes: map[string]config.Route{
			"default": {Provider: "browser", TimeoutSeconds: 5, BrowserProfile: "test"},
		},
		Providers: config.Providers{Browser: config.BrowserProvider{LeaseSeconds: 5}},
		BrowserProfiles: map[string]config.BrowserProfile{
			"test": {
				Label: "Test AI", MatchURL: "https://example.test/*",
				Selectors: config.Selectors{Input: []string{"textarea"}, Submit: []string{"button"}, Response: []string{".answer"}},
			},
		},
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	result := make(chan Submission, 1)
	go func() {
		jobRaw, _ := json.Marshal(Job{Prompt: "Review safely", Text: "hello"})
		req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/jobs", bytes.NewReader(jobRaw))
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			return
		}
		defer resp.Body.Close()
		var submission Submission
		json.NewDecoder(resp.Body).Decode(&submission)
		result <- submission
	}()

	var work browserJob
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/browser/jobs/next?wait=0&profile=test", nil)
		req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
		resp, requestErr := http.DefaultClient.Do(req)
		if requestErr == nil && resp.StatusCode == http.StatusOK {
			json.NewDecoder(resp.Body).Decode(&work)
			resp.Body.Close()
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	if work.Job.ID == "" {
		t.Fatal("browser job was not queued")
	}

	decisionRaw := []byte(`{"verdict":"allow","flags":[],"confidence":0.9,"model":"test-ai"}`)
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/browser/jobs/"+work.Job.ID+"/complete", bytes.NewReader(decisionRaw))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("completion returned %s", resp.Status)
	}

	select {
	case submission := <-result:
		if submission.Decision == nil || submission.Decision.Verdict != "allow" {
			t.Fatalf("unexpected submission: %#v", submission)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("submission did not complete")
	}
}
