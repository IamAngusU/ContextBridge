package cluster

import (
	"testing"
	"time"
)

func TestAuthoritativeJobTimingUsesPhaseTimestampAndStableTerminalDuration(t *testing.T) {
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		job  Job
		now  time.Time
		want time.Duration
	}{
		{"queued", Job{Status: JobQueued, CreatedAt: base}, base.Add(38 * time.Second), 38 * time.Second},
		{"assigned", Job{Status: JobAssigned, CreatedAt: base, AssignedAt: base.Add(3 * time.Second)}, base.Add(12 * time.Second), 9 * time.Second},
		{"running", Job{Status: JobRunning, CreatedAt: base, AssignedAt: base.Add(3 * time.Second), StartedAt: base.Add(5 * time.Second)}, base.Add(83 * time.Minute), 82*time.Minute + 55*time.Second},
		{"terminal", Job{Status: JobCompleted, StartedAt: base.Add(5 * time.Second), FinishedAt: base.Add(43 * time.Second)}, base.Add(24 * time.Hour), 38 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := AuthoritativeJobTimingAt(test.job, test.now)
			if !got.ElapsedAvailable || got.Elapsed != test.want {
				t.Fatalf("timing = %#v, want elapsed %s", got, test.want)
			}
		})
	}
}

func TestAuthoritativeJobTimingSeparatesQueueAndExecution(t *testing.T) {
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	job := Job{Status: JobCompleted, CreatedAt: base, AssignedAt: base.Add(4 * time.Second), StartedAt: base.Add(7 * time.Second), FinishedAt: base.Add(17 * time.Second)}
	timing := AuthoritativeJobTimingAt(job, base.Add(10*time.Hour))
	if !timing.QueueAvailable || timing.Queue != 4*time.Second || !timing.ExecutionAvailable || timing.Execution != 10*time.Second {
		t.Fatalf("timing did not preserve distinct queue/execution evidence: %#v", timing)
	}
}

func TestAuthoritativeTimingClampsBackwardClockAndPipelineTerminalIsStable(t *testing.T) {
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	jobTiming := AuthoritativeJobTimingAt(Job{Status: JobRunning, StartedAt: base}, base.Add(-time.Minute))
	if !jobTiming.ElapsedAvailable || jobTiming.Elapsed != 0 || jobTiming.Execution != 0 {
		t.Fatalf("negative job duration was not clamped: %#v", jobTiming)
	}
	run := PipelineRun{Status: "completed", CreatedAt: base, FinishedAt: base.Add(25*time.Hour + 44*time.Minute)}
	first := AuthoritativePipelineTimingAt(run, base.Add(30*time.Hour))
	second := AuthoritativePipelineTimingAt(run, base.Add(300*time.Hour))
	if !first.ElapsedAvailable || first.Elapsed != 25*time.Hour+44*time.Minute || second.Elapsed != first.Elapsed {
		t.Fatalf("terminal pipeline timing drifted: first=%#v second=%#v", first, second)
	}
}
