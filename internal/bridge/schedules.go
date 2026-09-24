package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultJobAdmissionLimit = 4

const maximumScheduleStoreBytes int64 = 64 << 20

var errScheduleCapacity = errors.New("schedule capacity unavailable")

// A schedule is local, durable, and at-most-once: a due occurrence is recorded
// before its job is handed to a provider. An interrupted adapter send is never
// repeated automatically after a service restart.
type Schedule struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Job           Job              `json:"job"`
	Timing        ScheduleTiming   `json:"timing"`
	Fallback      ScheduleFallback `json:"fallback,omitempty"`
	Steps         []ScheduleStep   `json:"steps,omitempty"`
	History       []ScheduleRun    `json:"history,omitempty"`
	Enabled       bool             `json:"enabled"`
	NextRun       time.Time        `json:"next_run,omitempty"`
	CurrentRunID  string           `json:"current_run_id,omitempty"`
	LastRunID     string           `json:"last_run_id,omitempty"`
	LastRun       time.Time        `json:"last_run,omitempty"`
	LastOutcome   string           `json:"last_outcome,omitempty"`
	LastError     string           `json:"last_error,omitempty"`
	WaitingReason string           `json:"waiting_reason,omitempty"`
	Runs          uint64           `json:"runs"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

type ScheduleTiming struct {
	Type            string    `json:"type"` // at, interval, daily, weekdays, weekly, cron
	At              time.Time `json:"at,omitempty"`
	IntervalSeconds int       `json:"interval_seconds,omitempty"`
	Time            string    `json:"time,omitempty"` // HH:MM in Timezone
	Days            []string  `json:"days,omitempty"` // weekly: mon ... sun
	Cron            string    `json:"cron,omitempty"` // five-field minute hour day month weekday
	Timezone        string    `json:"timezone,omitempty"`
}

// Alternatives are only tried when the provider menu reports a preference
// unavailable before the prompt is submitted.
type ScheduleFallback struct {
	Models    []string `json:"models,omitempty"`
	Reasoning []string `json:"reasoning,omitempty"`
}

// Steps run only after the preceding step has a saved, successful result.
// The base Schedule.Job is step 1; these are additional steps.
type ScheduleStep struct {
	Name                string `json:"name,omitempty"`
	Job                 Job    `json:"job"`
	UsePreviousArtifact string `json:"use_previous_artifact,omitempty"` // image or file
}

type ScheduleRun struct {
	ID        string            `json:"id"`
	StartedAt time.Time         `json:"started_at"`
	EndedAt   time.Time         `json:"ended_at,omitempty"`
	Outcome   string            `json:"outcome"`
	Error     string            `json:"error,omitempty"`
	Steps     []ScheduleStepRun `json:"steps,omitempty"`
}

type ScheduleStepRun struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Outcome   string    `json:"outcome"`
	Error     string    `json:"error,omitempty"`
}

type scheduleStore struct {
	mu             sync.Mutex
	path           string
	items          map[string]Schedule
	admissionLimit int
}

func newScheduleStore(dir string, admissionLimit int) (*scheduleStore, error) {
	if admissionLimit <= 0 {
		admissionLimit = defaultJobAdmissionLimit
	}
	ss := &scheduleStore{path: filepath.Join(dir, "schedules.json"), items: map[string]Schedule{}, admissionLimit: admissionLimit}
	raw, err := readScheduleStore(ss.path)
	if errors.Is(err, os.ErrNotExist) {
		return ss, nil
	}
	if err != nil {
		return nil, err
	}
	var saved []Schedule
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, fmt.Errorf("invalid schedules store: %w", err)
	}
	for _, item := range saved {
		if item.CurrentRunID != "" {
			for index := range item.History {
				if item.History[index].ID == item.CurrentRunID {
					item.History[index].Outcome = "interrupted"
					item.History[index].EndedAt = time.Now().UTC()
					for step := range item.History[index].Steps {
						if item.History[index].Steps[step].Outcome == "running" {
							item.History[index].Steps[step].Outcome = "interrupted"
							item.History[index].Steps[step].EndedAt = item.History[index].EndedAt
						}
					}
				}
			}
			item.LastRunID, item.LastOutcome, item.LastError = item.CurrentRunID, "interrupted", "service_restarted_during_run; inspect the provider endpoint before retrying"
			item.CurrentRunID = ""
		}
		ss.items[item.ID] = item
	}
	if err := ss.persistLocked(); err != nil {
		return nil, err
	}
	return ss, nil
}

func (ss *scheduleStore) persistLocked() error {
	items := make([]Schedule, 0, len(ss.items))
	for _, item := range ss.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(raw)) > maximumScheduleStoreBytes {
		return fmt.Errorf("schedule store exceeds %d MiB", maximumScheduleStoreBytes>>20)
	}
	return writeScheduleFileAtomic(ss.path, append(raw, '\n'))
}

func readScheduleStore(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximumScheduleStoreBytes {
		return nil, fmt.Errorf("schedule store must be a regular file no larger than %d MiB", maximumScheduleStoreBytes>>20)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximumScheduleStoreBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximumScheduleStoreBytes {
		return nil, fmt.Errorf("schedule store exceeds %d MiB", maximumScheduleStoreBytes>>20)
	}
	return raw, nil
}

func (ss *scheduleStore) list() []Schedule {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	items := make([]Schedule, 0, len(ss.items))
	for _, item := range ss.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items
}

func (ss *scheduleStore) get(id string) (Schedule, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	item, ok := ss.items[id]
	return item, ok
}

func (ss *scheduleStore) add(item Schedule) (Schedule, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if _, exists := ss.items[item.ID]; exists {
		return Schedule{}, os.ErrExist
	}
	if len(ss.items) >= 128 {
		return Schedule{}, errors.New("maximum of 128 schedules reached")
	}
	ss.items[item.ID] = item
	if err := ss.persistLocked(); err != nil {
		delete(ss.items, item.ID)
		return Schedule{}, err
	}
	return item, nil
}

func (ss *scheduleStore) setEnabled(id string, enabled bool) (Schedule, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	item, ok := ss.items[id]
	if !ok {
		return Schedule{}, os.ErrNotExist
	}
	previous := item
	item.Enabled, item.UpdatedAt = enabled, time.Now().UTC()
	if enabled && item.NextRun.IsZero() {
		return Schedule{}, errors.New("one-shot schedule has already run")
	}
	ss.items[id] = item
	if err := ss.persistLocked(); err != nil {
		ss.items[id] = previous
		return Schedule{}, err
	}
	return item, nil
}

func (ss *scheduleStore) remove(id string) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	item, ok := ss.items[id]
	if !ok {
		return os.ErrNotExist
	}
	if item.CurrentRunID != "" {
		return errors.New("schedule is running; pause it after completion")
	}
	delete(ss.items, id)
	if err := ss.persistLocked(); err != nil {
		ss.items[id] = item
		return err
	}
	return nil
}

func (ss *scheduleStore) claim(id string, now time.Time, manual bool) (Schedule, Job, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	item, ok := ss.items[id]
	if !ok {
		return Schedule{}, Job{}, os.ErrNotExist
	}
	if item.CurrentRunID != "" {
		return Schedule{}, Job{}, errors.New("schedule is already running")
	}
	running := 0
	for _, candidate := range ss.items {
		if candidate.CurrentRunID != "" {
			running++
		}
	}
	// Manual runs and the due dispatcher share this atomic bound. Checking a
	// separate counter before claim allowed concurrent requests to all observe
	// a free slot and over-admit runs.
	if running >= ss.admissionLimit {
		return Schedule{}, Job{}, errScheduleCapacity
	}
	if !manual && (!item.Enabled || item.NextRun.IsZero() || item.NextRun.After(now)) {
		return Schedule{}, Job{}, errors.New("schedule is not due")
	}
	original := item
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Schedule{}, Job{}, err
	}
	job := item.Job
	metadata := make(map[string]interface{}, len(job.Metadata)+4)
	for key, value := range job.Metadata {
		metadata[key] = value
	}
	if len(item.Fallback.Models) > 0 {
		metadata["contextbridge_model_fallbacks"] = item.Fallback.Models
	}
	if len(item.Fallback.Reasoning) > 0 {
		metadata["contextbridge_reasoning_fallbacks"] = item.Fallback.Reasoning
	}
	// A schedule must never claim an unrelated adapter session.
	metadata["contextbridge_new_session"] = true
	if _, specified := metadata["contextbridge_foreground_new_session"]; !specified {
		metadata["contextbridge_foreground_new_session"] = true
	}
	job.Metadata = metadata
	job.ID = "schedule-" + item.ID + "-" + hex.EncodeToString(nonce[:])
	job.Source = "schedule"
	job.CreatedAt = now.UTC()
	if job.Metadata["contextbridge_new_session_per_run"] == true {
		job.SessionID = job.ID
	}
	if job.SessionID == "" {
		job.SessionID = "schedule-" + item.ID
	}
	if !manual {
		next, err := item.Timing.next(now, item.CreatedAt)
		if err != nil {
			return Schedule{}, Job{}, err
		}
		item.NextRun = next
		if next.IsZero() {
			item.Enabled = false
		}
	}
	item.CurrentRunID, item.WaitingReason, item.UpdatedAt = job.ID, "", now.UTC()
	item.History = append(item.History, ScheduleRun{ID: job.ID, StartedAt: now.UTC(), Outcome: "running"})
	if len(item.History) > 50 {
		item.History = append([]ScheduleRun(nil), item.History[len(item.History)-50:]...)
	}
	ss.items[id] = item
	if err := ss.persistLocked(); err != nil {
		ss.items[id] = original
		return Schedule{}, Job{}, err
	}
	return item, job, nil
}

func (ss *scheduleStore) runningCount() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	count := 0
	for _, item := range ss.items {
		if item.CurrentRunID != "" {
			count++
		}
	}
	return count
}

// status omits prompts and metadata; the explicit /v1/schedules endpoint is
// used for detailed management by an authenticated local client.
func (ss *scheduleStore) status() []map[string]interface{} {
	items := ss.list()
	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		history := make([]map[string]interface{}, 0, min(len(item.History), 10))
		for i := len(item.History) - 1; i >= 0 && len(history) < 10; i-- {
			run := item.History[i]
			steps := make([]map[string]interface{}, 0, len(run.Steps))
			for _, step := range run.Steps {
				steps = append(steps, map[string]interface{}{"id": step.ID, "name": step.Name, "started_at": step.StartedAt, "ended_at": step.EndedAt, "outcome": step.Outcome})
			}
			history = append(history, map[string]interface{}{"id": run.ID, "started_at": run.StartedAt, "ended_at": run.EndedAt, "outcome": run.Outcome, "steps": steps})
		}
		result = append(result, map[string]interface{}{
			"id": item.ID, "name": item.Name, "timing": item.Timing, "enabled": item.Enabled,
			"next_run": item.NextRun, "current_run_id": item.CurrentRunID, "last_run_id": item.LastRunID,
			"last_run": item.LastRun, "last_outcome": item.LastOutcome,
			"waiting_reason": item.WaitingReason, "runs": item.Runs, "history": history, "step_count": len(item.Steps) + 1,
			"route": item.Job.Route, "provider": item.Job.Provider, "model": item.Job.Model, "reasoning": item.Job.Reasoning,
		})
	}
	return result
}

func (ss *scheduleStore) finish(id, runID, outcome, detail string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	item, ok := ss.items[id]
	if !ok || item.CurrentRunID != runID {
		return
	}
	item.CurrentRunID, item.LastRunID, item.LastRun = "", runID, time.Now().UTC()
	item.LastOutcome, item.LastError, item.UpdatedAt = outcome, detail, item.LastRun
	item.Runs = saturatingMetricAdd(item.Runs, 1)
	for index := range item.History {
		if item.History[index].ID == runID {
			item.History[index].Outcome = outcome
			item.History[index].EndedAt = item.LastRun
			item.History[index].Error = detail
		}
	}
	ss.items[id] = item
	_ = ss.persistLocked()
}

func (ss *scheduleStore) recordStep(id, runID string, step ScheduleStepRun) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	item, ok := ss.items[id]
	if !ok || item.CurrentRunID != runID {
		return
	}
	for i := range item.History {
		if item.History[i].ID != runID {
			continue
		}
		found := false
		for j := range item.History[i].Steps {
			if item.History[i].Steps[j].ID == step.ID {
				item.History[i].Steps[j] = step
				found = true
			}
		}
		if !found {
			item.History[i].Steps = append(item.History[i].Steps, step)
		}
	}
	ss.items[id] = item
	_ = ss.persistLocked()
}

func (ss *scheduleStore) waiting(id, reason string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	item, ok := ss.items[id]
	if !ok || item.WaitingReason == reason {
		return
	}
	item.WaitingReason = reason
	ss.items[id] = item
	_ = ss.persistLocked()
}

func (t ScheduleTiming) next(after, anchor time.Time) (time.Time, error) {
	switch t.Type {
	case "at":
		if t.At.After(after) {
			return t.At.UTC(), nil
		}
		return time.Time{}, nil
	case "interval":
		if t.IntervalSeconds < 60 || t.IntervalSeconds > 365*24*3600 {
			return time.Time{}, errors.New("interval_seconds must be 60 to 31536000")
		}
		if anchor.IsZero() {
			anchor = after
		}
		step := time.Duration(t.IntervalSeconds) * time.Second
		if after.Before(anchor) {
			return anchor.UTC(), nil
		}
		elapsed := after.Sub(anchor)
		cycles := elapsed/step + 1
		if cycles <= 0 || cycles > time.Duration(math.MaxInt64)/step {
			return time.Time{}, errors.New("interval schedule exceeds supported time range")
		}
		next := anchor.Add(cycles * step).UTC()
		if !next.After(after) {
			return time.Time{}, errors.New("interval schedule exceeds supported time range")
		}
		return next, nil
	case "daily", "weekdays", "weekly", "cron":
		location := time.Local
		if t.Timezone != "" {
			var err error
			location, err = time.LoadLocation(t.Timezone)
			if err != nil {
				return time.Time{}, fmt.Errorf("invalid timezone: %w", err)
			}
		}
		minute, hour := -1, -1
		if t.Type != "cron" {
			parts := strings.Split(t.Time, ":")
			if len(parts) != 2 {
				return time.Time{}, errors.New("time must be HH:MM")
			}
			var err error
			hour, err = strconv.Atoi(parts[0])
			if err != nil {
				return time.Time{}, errors.New("invalid hour")
			}
			minute, err = strconv.Atoi(parts[1])
			if err != nil {
				return time.Time{}, errors.New("invalid minute")
			}
			if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
				return time.Time{}, errors.New("time must be HH:MM")
			}
		}
		var cronFields [5]map[int]bool
		var cronWild [5]bool
		if t.Type == "cron" {
			fields := strings.Fields(t.Cron)
			if len(fields) != 5 {
				return time.Time{}, errors.New("cron needs five fields: minute hour day month weekday")
			}
			bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
			for i, field := range fields {
				values, wild, err := parseCronField(field, bounds[i][0], bounds[i][1])
				if err != nil {
					return time.Time{}, fmt.Errorf("cron field %d: %w", i+1, err)
				}
				cronFields[i], cronWild[i] = values, wild
			}
		}
		days := map[time.Weekday]bool{}
		for _, day := range t.Days {
			switch strings.ToLower(day) {
			case "sun":
				days[time.Sunday] = true
			case "mon":
				days[time.Monday] = true
			case "tue":
				days[time.Tuesday] = true
			case "wed":
				days[time.Wednesday] = true
			case "thu":
				days[time.Thursday] = true
			case "fri":
				days[time.Friday] = true
			case "sat":
				days[time.Saturday] = true
			default:
				return time.Time{}, fmt.Errorf("invalid weekday %q", day)
			}
		}
		if t.Type == "weekly" && len(days) == 0 {
			return time.Time{}, errors.New("weekly needs days")
		}
		candidate := after.Truncate(time.Minute).Add(time.Minute)
		for i := 0; i < 2*366*24*60; i++ {
			local := candidate.In(location)
			match := false
			switch t.Type {
			case "daily":
				match = local.Hour() == hour && local.Minute() == minute
			case "weekdays":
				match = local.Weekday() >= time.Monday && local.Weekday() <= time.Friday && local.Hour() == hour && local.Minute() == minute
			case "weekly":
				match = days[local.Weekday()] && local.Hour() == hour && local.Minute() == minute
			case "cron":
				weekday := int(local.Weekday())
				dayMatch := cronFields[2][local.Day()]
				weekMatch := cronFields[4][weekday] || (weekday == 0 && cronFields[4][7])
				if cronWild[2] {
					dayMatch = weekMatch
				} else if !cronWild[4] {
					dayMatch = dayMatch || weekMatch
				}
				match = cronFields[0][local.Minute()] && cronFields[1][local.Hour()] && dayMatch && cronFields[3][int(local.Month())]
			}
			if match {
				return candidate.UTC(), nil
			}
			candidate = candidate.Add(time.Minute)
		}
		return time.Time{}, errors.New("schedule has no occurrence in the next two years")
	default:
		return time.Time{}, errors.New("timing.type must be at, interval, daily, weekdays, weekly, or cron")
	}
}

func parseCronField(field string, minValue, maxValue int) (map[int]bool, bool, error) {
	values := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		base, step := part, 1
		if strings.Contains(part, "/") {
			pieces := strings.Split(part, "/")
			if len(pieces) != 2 {
				return nil, false, errors.New("invalid step")
			}
			base = pieces[0]
			var err error
			step, err = strconv.Atoi(pieces[1])
			if err != nil || step < 1 || step > maxValue-minValue+1 {
				return nil, false, errors.New("invalid step")
			}
		}
		start, end := minValue, maxValue
		if base != "*" {
			pieces := strings.Split(base, "-")
			if len(pieces) > 2 {
				return nil, false, errors.New("invalid range")
			}
			var err error
			start, err = strconv.Atoi(pieces[0])
			if err != nil {
				return nil, false, errors.New("invalid number")
			}
			end = start
			if len(pieces) == 2 {
				end, err = strconv.Atoi(pieces[1])
				if err != nil {
					return nil, false, errors.New("invalid number")
				}
			}
			if len(pieces) == 1 && strings.Contains(part, "/") {
				end = maxValue
			}
		}
		if start < minValue || end > maxValue || start > end {
			return nil, false, errors.New("value out of range")
		}
		for value := start; value <= end; value += step {
			values[value] = true
		}
	}
	return values, field == "*", nil
}

func (s *Server) dispatchSchedules(ctx context.Context) {
	now := time.Now().UTC()
	for _, item := range s.schedules.list() {
		if !item.Enabled || item.NextRun.IsZero() || item.NextRun.After(now) || item.CurrentRunID != "" {
			continue
		}
		if reason := s.scheduleReadiness(item.Job); reason != "" {
			s.schedules.waiting(item.ID, reason)
			continue
		}
		claimed, job, err := s.claimSchedule(item.ID, now, false)
		if errors.Is(err, errScheduleCapacity) {
			s.schedules.waiting(item.ID, "capacity")
			continue
		}
		if err != nil {
			s.logger.Printf("schedule %s claim failed: %v", item.ID, err)
			continue
		}
		s.store.AddActivity("scheduled", "Scheduled job started: "+claimed.Name, job.ID)
		s.logger.Printf("schedule %s started as job %s", claimed.ID, job.ID)
		go s.executeSchedule(ctx, claimed.ID, job)
	}
}

// claimSchedule makes the combined ordinary-job + scheduled-run limit one
// critical section. A schedule remains counted for its complete multi-step
// lifetime through CurrentRunID, including the gaps between provider calls.
func (s *Server) claimSchedule(id string, now time.Time, manual bool) (Schedule, Job, error) {
	s.scheduleAdmissionMu.Lock()
	defer s.scheduleAdmissionMu.Unlock()
	if s.lifecycleStopping {
		return Schedule{}, Job{}, errServiceStopping
	}
	if s.regularActiveJobs+s.schedules.runningCount() >= s.jobAdmissionLimit {
		return Schedule{}, Job{}, errScheduleCapacity
	}
	return s.schedules.claim(id, now, manual)
}

func (s *Server) scheduleReadiness(job Job) string {
	route := s.cfg.Route(job.Route)
	provider := route.Provider
	if job.Provider != "" {
		provider = job.Provider
	}
	engine, ok := s.cfg.Engine(provider)
	if !ok {
		return "provider_unavailable"
	}
	if engine.Type == "adapter" {
		status := s.store.AdapterStatus()
		if !status.Connected || status.ActiveEndpoints == 0 {
			return "adapter_disconnected"
		}
		profile := strings.TrimSpace(job.AdapterProfile)
		if profile == "" {
			profile = strings.TrimSpace(route.AdapterProfile)
		}
		if profile != "" {
			matching, available := 0, 0
			for _, endpoint := range status.Endpoints {
				if strings.EqualFold(endpoint.Profile, profile) {
					matching++
					if endpoint.State != "working" && endpoint.State != "busy" {
						available++
					}
				}
			}
			if matching == 0 {
				return "adapter_profile_unavailable"
			}
			if available == 0 {
				return "adapter_profile_busy"
			}
		}
		if status.BusyEndpoints >= status.ActiveEndpoints {
			return "adapter_endpoints_busy"
		}
	}
	return ""
}

func (s *Server) executeSchedule(ctx context.Context, id string, job Job) {
	defer s.recoverSchedulePanic(id, job.ID)
	item, ok := s.schedules.get(id)
	if !ok {
		return
	}
	outcome, detail := s.runScheduleSteps(withScheduledExecution(ctx), id, job, item.Steps)
	s.schedules.finish(id, job.ID, outcome, detail)
	s.store.AddActivity("scheduled", "Scheduled job "+outcome, job.ID)
}

func (s *Server) runSchedules(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatchSchedules(ctx)
		}
	}
}
