package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A schedule is local, durable, and at-most-once: a due occurrence is recorded
// before its job is handed to a provider. An interrupted browser send is never
// repeated automatically after a service restart.
type Schedule struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Job           Job              `json:"job"`
	Timing        ScheduleTiming   `json:"timing"`
	Fallback      ScheduleFallback `json:"fallback,omitempty"`
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

type scheduleStore struct {
	mu    sync.Mutex
	path  string
	items map[string]Schedule
}

func newScheduleStore(dir string) (*scheduleStore, error) {
	ss := &scheduleStore{path: filepath.Join(dir, "schedules.json"), items: map[string]Schedule{}}
	raw, err := os.ReadFile(ss.path)
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
			item.LastRunID, item.LastOutcome, item.LastError = item.CurrentRunID, "interrupted", "service_restarted_during_run; inspect the provider tab before retrying"
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
	tmp := ss.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, ss.path)
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
	// A schedule must never claim an unrelated attached conversation.
	metadata["contextbridge_new_chat"] = true
	if _, specified := metadata["contextbridge_foreground_new_chat"]; !specified {
		metadata["contextbridge_foreground_new_chat"] = true
	}
	job.Metadata = metadata
	job.ID = "schedule-" + item.ID + "-" + hex.EncodeToString(nonce[:])
	job.Source = "schedule"
	job.CreatedAt = now.UTC()
	if job.Metadata["contextbridge_new_chat_per_run"] == true {
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
		result = append(result, map[string]interface{}{
			"id": item.ID, "name": item.Name, "timing": item.Timing, "enabled": item.Enabled,
			"next_run": item.NextRun, "current_run_id": item.CurrentRunID, "last_run_id": item.LastRunID,
			"last_run": item.LastRun, "last_outcome": item.LastOutcome,
			"waiting_reason": item.WaitingReason, "runs": item.Runs,
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
	item.Runs++
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
		return anchor.Add((after.Sub(anchor)/step + 1) * step).UTC(), nil
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
	capacity := 4 - max(int(s.activeJobs.Load()), s.schedules.runningCount())
	for _, item := range s.schedules.list() {
		if !item.Enabled || item.NextRun.IsZero() || item.NextRun.After(now) || item.CurrentRunID != "" {
			continue
		}
		if capacity <= 0 {
			s.schedules.waiting(item.ID, "capacity")
			continue
		}
		if reason := s.scheduleReadiness(item.Job); reason != "" {
			s.schedules.waiting(item.ID, reason)
			continue
		}
		claimed, job, err := s.schedules.claim(item.ID, now, false)
		if err != nil {
			s.logger.Printf("schedule %s claim failed: %v", item.ID, err)
			continue
		}
		s.store.AddActivity("scheduled", "Scheduled job started: "+claimed.Name, job.ID)
		s.logger.Printf("schedule %s started as job %s", claimed.ID, job.ID)
		go s.executeSchedule(ctx, claimed.ID, job)
		capacity--
	}
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
	if engine.Type == "browser" {
		status := s.store.BrowserStatus()
		if !status.Connected || status.ActiveTabs == 0 {
			return "browser_disconnected"
		}
		profile := strings.TrimSpace(job.BrowserProfile)
		if profile == "" {
			profile = strings.TrimSpace(route.BrowserProfile)
		}
		if profile != "" {
			matching, available := 0, 0
			for _, tab := range status.Tabs {
				if strings.EqualFold(tab.Profile, profile) {
					matching++
					if tab.State != "working" && tab.State != "busy" {
						available++
					}
				}
			}
			if matching == 0 {
				return "browser_profile_unavailable"
			}
			if available == 0 {
				return "browser_profile_busy"
			}
		}
		if status.BusyTabs >= status.ActiveTabs {
			return "browser_tabs_busy"
		}
	}
	return ""
}

func (s *Server) executeSchedule(ctx context.Context, id string, job Job) {
	output, err := s.Process(ctx, job)
	if err != nil {
		s.schedules.finish(id, job.ID, "failed", err.Error())
		s.store.AddActivity("scheduled", "Scheduled job failed before completion", job.ID)
		return
	}
	if output.Error != "" {
		s.schedules.finish(id, job.ID, "failed", output.Error)
		s.store.AddActivity("scheduled", "Scheduled job failed", job.ID)
		return
	}
	if output.Decision != nil && output.Decision.Verdict == "review" {
		s.schedules.finish(id, job.ID, "review", strings.Join(output.Decision.Flags, ","))
		s.store.AddActivity("scheduled", "Scheduled review job needs attention", job.ID)
		return
	}
	s.schedules.finish(id, job.ID, "completed", "")
	s.store.AddActivity("scheduled", "Scheduled job completed", job.ID)
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
