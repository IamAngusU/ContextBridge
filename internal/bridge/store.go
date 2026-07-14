package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store struct {
	dir       string
	mu        sync.Mutex
	queued    map[string]*queuedJob
	completed map[string]Decision
}

type queuedJob struct {
	job       Job
	profile   interface{}
	deadline  time.Time
	leasedTil time.Time
	done      chan Decision
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0700); err != nil {
		return nil, err
	}
	return &Store{
		dir:       dir,
		queued:    map[string]*queuedJob{},
		completed: map[string]Decision{},
	}, nil
}

func (s *Store) SaveJob(job Job) error {
	raw, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "jobs", job.ID+".json"), append(raw, '\n'), 0600)
}

func (s *Store) SaveDecision(id string, decision Decision) error {
	raw, err := json.MarshalIndent(decision, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "jobs", id+".result.json"), append(raw, '\n'), 0600)
}

func (s *Store) Queue(job Job, profile interface{}, timeout time.Duration) <-chan Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan Decision, 1)
	s.queued[job.ID] = &queuedJob{
		job:      job,
		profile:  profile,
		deadline: time.Now().Add(timeout),
		done:     done,
	}
	return done
}

func (s *Store) NextBrowserJob(profile string, lease time.Duration) *browserJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, item := range s.queued {
		if now.After(item.deadline) {
			delete(s.queued, id)
			continue
		}
		if now.Before(item.leasedTil) {
			continue
		}
		if profile != "" {
			if p, ok := item.profile.(map[string]interface{}); ok {
				if name, _ := p["name"].(string); name != "" && name != profile {
					continue
				}
			}
		}
		item.leasedTil = now.Add(lease)
		return &browserJob{Job: item.job, Profile: item.profile, Deadline: item.deadline}
	}
	return nil
}

func (s *Store) Complete(id string, decision Decision) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.queued[id]
	if !ok {
		return false
	}
	delete(s.queued, id)
	s.completed[id] = decision
	item.done <- decision
	close(item.done)
	return true
}

func (s *Store) Stats() (queued, completed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queued), len(s.completed)
}
