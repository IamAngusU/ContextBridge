package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

func TestChatDeadlineCancelsAcknowledgedJobWithSameScopedCredential(t *testing.T) {
	var submits, cancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer producer-only" {
			t.Error("cancellation changed or dropped the scoped credential")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			submits.Add(1)
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_deadline", Status: cluster.JobQueued})
		case http.MethodDelete:
			cancels.Add(1)
			if r.URL.Path != "/v1/cluster/jobs/job_deadline" {
				t.Errorf("wrong cancellation target: %s", r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_deadline", Status: cluster.JobCancelled})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	state := chatState{relayURL: server.URL, token: "producer-only", provider: "ollama", turnTimeout: 100 * time.Millisecond}
	if err := state.turn(context.Background(), "Explain RGB"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline cause lost: %v", err)
	}
	if submits.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("request replayed or cancellation missing: submit=%d cancel=%d", submits.Load(), cancels.Load())
	}
}

func TestChatInterruptionDuringPollRetainsAcknowledgedID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_interrupt", Status: cluster.JobQueued})
		case http.MethodGet:
			cancel()
			<-r.Context().Done()
		case http.MethodDelete:
			cancels.Add(1)
			if r.URL.Path != "/v1/cluster/jobs/job_interrupt" {
				t.Errorf("poll failure discarded cancellation ID: %s", r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_interrupt", Status: cluster.JobCancelled})
		}
	}))
	defer server.Close()
	state := chatState{relayURL: server.URL, token: "producer", provider: "ollama"}
	if err := state.turn(ctx, "Explain RGB"); !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption cause lost: %v", err)
	}
	if cancels.Load() != 1 {
		t.Fatal("interrupted poll did not cancel the original acknowledged job")
	}
}

func TestChatTerminalJobsAreNotCancelled(t *testing.T) {
	for _, status := range []string{cluster.JobCompleted, cluster.JobFailed, cluster.JobCancelled} {
		t.Run(status, func(t *testing.T) {
			var cancels atomic.Int32
			result, _ := json.Marshal(bridge.Submission{Output: &bridge.Output{Text: "done"}})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodDelete {
					cancels.Add(1)
				}
				if r.Method == http.MethodPost {
					_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_terminal", Status: cluster.JobQueued})
				} else {
					_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_terminal", Status: status, Result: result})
				}
			}))
			defer server.Close()
			state := chatState{relayURL: server.URL, token: "producer", provider: "ollama", turnTimeout: 2 * time.Second}
			err := state.turn(context.Background(), "Explain RGB")
			if status == cluster.JobCompleted && err != nil {
				t.Fatal(err)
			}
			if status != cluster.JobCompleted && err == nil {
				t.Fatal("terminal failure was hidden")
			}
			if cancels.Load() != 0 {
				t.Fatal("terminal job triggered cancellation")
			}
		})
	}
}

func TestChatCancellationFailurePreservesDeadlineAndDoesNotReplay(t *testing.T) {
	var submits, cancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			cancels.Add(1)
			http.Error(w, "not authorized", http.StatusForbidden)
			return
		}
		submits.Add(1)
		_ = json.NewEncoder(w).Encode(cluster.Job{ID: "job_denied", Status: cluster.JobQueued})
	}))
	defer server.Close()
	state := chatState{relayURL: server.URL, token: "producer", turnTimeout: 100 * time.Millisecond}
	if err := state.turn(context.Background(), "Explain RGB"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if submits.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("unexpected retry: submit=%d cancel=%d", submits.Load(), cancels.Load())
	}
}

func TestChatAmbiguousSubmissionNeverReplaysOrCancelsUnknownID(t *testing.T) {
	var submits, other atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			other.Add(1)
			return
		}
		submits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	state := chatState{relayURL: server.URL, token: "producer", turnTimeout: 100 * time.Millisecond}
	if err := state.turn(context.Background(), "Explain RGB"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if submits.Load() != 1 || other.Load() != 0 {
		t.Fatal("ambiguous submission replayed or fabricated cancellation ID")
	}
}

func TestChatTimeoutValidationAndHelpParsing(t *testing.T) {
	for _, value := range []string{"-1s", "25h"} {
		err := clusterChatCommandWithDefaults([]string{"--timeout", value}, "do", "ollama", "", "")
		if err == nil || !strings.Contains(err.Error(), "--timeout") {
			t.Fatalf("invalid timeout %s was accepted: %v", value, err)
		}
	}
	if !chatHelpRequested([]string{"--timeout", "20s", "--help"}) || chatHelpRequested([]string{"--timeout", "--help"}) {
		t.Fatal("timeout values confused help argument parsing")
	}
	if !looksLikePastedChatFlag("--timeout 20s") {
		t.Fatal("pasted timeout flag would be sent as prompt")
	}
}

func TestChatTimeoutKeepsLocalToolsNetworkFree(t *testing.T) {
	state := chatState{localTools: true, relayURL: "http://127.0.0.1:1", turnTimeout: time.Second}
	if err := state.turn(context.Background(), "10 * 3"); err != nil {
		t.Fatal(err)
	}
}
