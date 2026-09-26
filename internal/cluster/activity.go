package cluster

import (
	"sort"
	"time"
)

const (
	ActivityProjectionV1      = "contextbridge.activity.v1"
	MaximumActivityDetailRows = 8
)

type ActivityItem struct {
	ID          string    `json:"id"`
	JobID       string    `json:"job_id"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	AssignedAt  time.Time `json:"assigned_at,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	FailureCode string    `json:"failure_code,omitempty"`
}

type ActivitySummary struct {
	Active    int `json:"active"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
	Ambiguous int `json:"ambiguous"`
}

type ActivityProjection struct {
	Schema          string          `json:"schema"`
	GroupID         string          `json:"group_id"`
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	State           string          `json:"state"`
	CreatedAt       time.Time       `json:"created_at"`
	FinishedAt      time.Time       `json:"finished_at,omitempty"`
	Items           []ActivityItem  `json:"items"`
	Summary         ActivitySummary `json:"summary"`
	DetailOverflow  int             `json:"detail_overflow,omitempty"`
	HistoryComplete bool            `json:"history_complete"`
}

// ProjectPipelineActivity is a bounded, content-minimizing view over durable
// pipeline/child-job relationships. It never inspects prompt or result data,
// never groups by similar strings, and never turns worker advisory progress
// into a CB-owned child lifecycle.
func ProjectPipelineActivity(run PipelineRun, historyComplete bool) ActivityProjection {
	projection := ActivityProjection{
		Schema: ActivityProjectionV1, GroupID: run.ID, Kind: "pipeline", Name: run.Pipeline,
		State: run.Status, CreatedAt: run.CreatedAt, FinishedAt: run.FinishedAt,
		Items: []ActivityItem{}, HistoryComplete: historyComplete,
	}
	details := make([]ActivityItem, 0, len(run.Steps))
	for _, job := range run.Steps {
		state := activityJobState(job)
		item := ActivityItem{
			ID: job.Step, JobID: job.ID, State: state, FailureCode: job.FailureCode,
			CreatedAt: job.CreatedAt, AssignedAt: job.AssignedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
		}
		switch state {
		case JobQueued, JobAssigned, JobRunning:
			projection.Summary.Active++
			details = append(details, item)
		case JobCompleted:
			projection.Summary.Completed++
		case "ambiguous":
			projection.Summary.Ambiguous++
			details = append(details, item)
		case JobFailed:
			projection.Summary.Failed++
			details = append(details, item)
		case JobCancelled:
			projection.Summary.Cancelled++
			details = append(details, item)
		}
	}
	sort.Slice(details, func(i, j int) bool {
		left, right := activityStateRank(details[i].State), activityStateRank(details[j].State)
		if left != right {
			return left < right
		}
		leftTime, rightTime := activityItemTime(details[i]), activityItemTime(details[j])
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}
		if details[i].ID != details[j].ID {
			return details[i].ID < details[j].ID
		}
		return details[i].JobID < details[j].JobID
	})
	if len(details) > MaximumActivityDetailRows {
		projection.DetailOverflow = len(details) - MaximumActivityDetailRows
		details = details[:MaximumActivityDetailRows]
	}
	projection.Items = details
	return projection
}

func activityJobState(job Job) string {
	if job.FailureCode == FailureExecutionStateAmbiguous {
		return "ambiguous"
	}
	return job.Status
}

func activityStateRank(state string) int {
	switch state {
	case "ambiguous":
		return 0
	case JobFailed:
		return 1
	case JobCancelled:
		return 2
	case JobRunning:
		return 3
	case JobAssigned:
		return 4
	case JobQueued:
		return 5
	default:
		return 6
	}
}

func activityItemTime(item ActivityItem) time.Time {
	switch item.State {
	case JobRunning:
		if !item.StartedAt.IsZero() {
			return item.StartedAt
		}
	case JobAssigned:
		if !item.AssignedAt.IsZero() {
			return item.AssignedAt
		}
	case "ambiguous", JobFailed, JobCancelled:
		if !item.FinishedAt.IsZero() {
			return item.FinishedAt
		}
	}
	return item.CreatedAt
}
