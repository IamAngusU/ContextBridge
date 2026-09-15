package bridge

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestBrowserLeaseGenerationMakesSentUnknownReclaimsObservationOnly(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "generation-job", Prompt: "send exactly once"}
	store.Queue(job, nil, time.Second)
	first := store.NextBrowserJob("", 5*time.Millisecond)
	if first == nil || first.LeaseGeneration == 0 || first.ObservationOnly {
		t.Fatalf("unexpected initial lease: %#v", first)
	}
	if !store.MarkBrowserAction(job.ID, first.LeaseGeneration, 5*time.Millisecond) {
		t.Fatal("current lease could not enter sent-unknown state")
	}
	time.Sleep(10 * time.Millisecond)
	second := store.NextBrowserJob("", time.Second)
	if second == nil || second.LeaseGeneration <= first.LeaseGeneration || !second.ObservationOnly {
		t.Fatalf("sent-unknown job was not reclaimed observation-only: first=%#v second=%#v", first, second)
	}
	if store.Renew(job.ID, first.LeaseGeneration, time.Second) {
		t.Fatal("stale worker renewed a replaced lease")
	}
	if store.MarkBrowserAction(job.ID, first.LeaseGeneration, time.Second) {
		t.Fatal("stale worker authorized another provider action")
	}
	stale := ReviewDecision("browser", "test", "stale", 0)
	if store.Complete(job.ID, first.LeaseGeneration, Output{Mode: "decision", Decision: &stale}) {
		t.Fatal("stale worker completed a replaced lease")
	}
	current := ReviewDecision("browser", "test", "observed", 0)
	if !store.Complete(job.ID, second.LeaseGeneration, Output{Mode: "decision", Decision: &current}) {
		t.Fatal("current observation-only lease could not complete")
	}
}

func TestBrowserCancellationWinsBeforePreSendClaim(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "cancel-before-claim", Prompt: "must not send"}
	store.Queue(job, nil, time.Second)
	lease := store.NextBrowserJob("", time.Second)
	if lease == nil || lease.LeaseGeneration == 0 {
		t.Fatalf("unexpected initial lease: %#v", lease)
	}

	// Cancel and action claims linearize under the same store mutex. Exercise
	// the security-sensitive ordering deterministically: once cancellation has
	// returned, the extension's pre-send claim must fail and cannot mark the job
	// sent-unknown or renew the removed lease.
	cancelled := make(chan struct{})
	go func() {
		store.Cancel(job.ID)
		close(cancelled)
	}()
	<-cancelled
	if store.MarkBrowserAction(job.ID, lease.LeaseGeneration, time.Second) {
		t.Fatal("a cancelled browser job acquired a pre-send action claim")
	}
	if store.Renew(job.ID, lease.LeaseGeneration, time.Second) {
		t.Fatal("a cancelled browser job renewed its lease")
	}
	if reclaimed := store.NextBrowserJob("", time.Second); reclaimed != nil {
		t.Fatalf("a cancelled browser job was redelivered: %#v", reclaimed)
	}
}

func TestBrowserCancelAndPreSendClaimRaceLinearizes(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 100; iteration++ {
		job := Job{ID: "cancel-claim-race-" + strconv.Itoa(iteration), Prompt: "at most one action"}
		store.Queue(job, nil, time.Second)
		lease := store.NextBrowserJob("", time.Second)
		if lease == nil {
			t.Fatal("browser job was not leased")
		}
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			store.Cancel(job.ID)
		}()
		go func() {
			defer wait.Done()
			<-start
			_ = store.MarkBrowserAction(job.ID, lease.LeaseGeneration, time.Second)
		}()
		close(start)
		wait.Wait()
		if store.Renew(job.ID, lease.LeaseGeneration, time.Second) {
			t.Fatal("cancelled browser job remained renewable after the claim race")
		}
		if reclaimed := store.NextBrowserJob("", time.Second); reclaimed != nil {
			t.Fatalf("cancelled browser job was redelivered after the claim race: %#v", reclaimed)
		}
	}
}
