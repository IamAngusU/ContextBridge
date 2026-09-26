package cluster

import "time"

// LifecycleTiming is derived only from relay-owned lifecycle timestamps. It
// is display evidence, not fencing authority, semantic progress, or an ETA.
type LifecycleTiming struct {
	Phase              string
	Elapsed            time.Duration
	ElapsedAvailable   bool
	Queue              time.Duration
	QueueAvailable     bool
	Execution          time.Duration
	ExecutionAvailable bool
	Terminal           bool
}

// AuthoritativeJobTimingAt reconstructs stable job timing after reconnects.
// Active phases use now only as the display endpoint; terminal execution time
// always uses FinishedAt and therefore cannot drift after completion.
func AuthoritativeJobTimingAt(job Job, now time.Time) LifecycleTiming {
	timing := LifecycleTiming{Phase: job.Status, Terminal: terminalJobStatus(job.Status)}
	if !job.CreatedAt.IsZero() && !job.AssignedAt.IsZero() {
		timing.Queue = nonNegativeWallDuration(job.AssignedAt.Sub(job.CreatedAt))
		timing.QueueAvailable = true
	}
	switch job.Status {
	case JobQueued:
		setElapsed(&timing, job.CreatedAt, now)
	case JobAssigned:
		setElapsed(&timing, job.AssignedAt, now)
	case JobRunning:
		setElapsed(&timing, job.StartedAt, now)
		if !job.StartedAt.IsZero() {
			timing.Execution = nonNegativeWallDuration(now.Sub(job.StartedAt))
			timing.ExecutionAvailable = true
		}
	default:
		if !job.StartedAt.IsZero() && !job.FinishedAt.IsZero() {
			timing.Execution = nonNegativeWallDuration(job.FinishedAt.Sub(job.StartedAt))
			timing.ExecutionAvailable = true
			timing.Elapsed = timing.Execution
			timing.ElapsedAvailable = true
		}
	}
	return timing
}

// AuthoritativePipelineTimingAt derives total pipeline wall time without
// inventing step progress. Individual CB-owned step timing comes from the
// child Job values in PipelineRun.Steps via AuthoritativeJobTimingAt.
func AuthoritativePipelineTimingAt(run PipelineRun, now time.Time) LifecycleTiming {
	timing := LifecycleTiming{Phase: run.Status, Terminal: terminalPipelineStatus(run.Status)}
	if run.CreatedAt.IsZero() {
		return timing
	}
	endpoint := now
	if timing.Terminal && !run.FinishedAt.IsZero() {
		endpoint = run.FinishedAt
	}
	timing.Elapsed = nonNegativeWallDuration(endpoint.Sub(run.CreatedAt))
	timing.ElapsedAvailable = true
	return timing
}

func setElapsed(timing *LifecycleTiming, start, now time.Time) {
	if start.IsZero() {
		return
	}
	timing.Elapsed = nonNegativeWallDuration(now.Sub(start))
	timing.ElapsedAvailable = true
}

func nonNegativeWallDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}
