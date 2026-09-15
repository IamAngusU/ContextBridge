package cluster

import "testing"

func TestWorkerQuiesceLinearizesWithJobStart(t *testing.T) {
	worker := &Worker{}
	if !worker.QuiesceForStop(false) {
		t.Fatal("idle worker could not quiesce")
	}
	if worker.beginJob() {
		t.Fatal("quiesced worker started a new job")
	}
	worker.ResumeAfterRejectedStop()
	if !worker.beginJob() {
		t.Fatal("resumed worker did not admit a job")
	}
	if worker.QuiesceForStop(false) {
		t.Fatal("busy worker quiesced without --force")
	}
	if !worker.beginJob() {
		t.Fatal("rejected quiesce did not reopen worker admission")
	}
	worker.changeRunning(-2)
	if !worker.Idle() {
		t.Fatal("test worker running count did not return to zero")
	}
	if !worker.beginJob() {
		t.Fatal("worker could not begin force-stop test job")
	}
	if !worker.QuiesceForStop(true) {
		t.Fatal("--force did not quiesce a busy worker")
	}
	if worker.beginJob() {
		t.Fatal("forced quiesce admitted another worker job")
	}
	worker.changeRunning(-1)
}
