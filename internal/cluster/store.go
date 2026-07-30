package cluster

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketJobs         = []byte("jobs")
	bucketJobIndex     = []byte("job_index")
	bucketQueue        = []byte("queue")
	bucketNodes        = []byte("nodes")
	bucketTokens       = []byte("tokens")
	bucketPairings     = []byte("pairings")
	bucketPairCodes    = []byte("pair_codes")
	bucketAssignments  = []byte("assignments")
	bucketEvents       = []byte("events")
	bucketPipelineRuns = []byte("pipeline_runs")
)

type Store struct {
	db *bolt.DB
}

type reservation struct {
	Assignment Assignment `json:"assignment"`
	SecretHash string     `json:"secret_hash"`
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 3 * time.Second, NoGrowSync: false})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketJobs, bucketJobIndex, bucketQueue, bucketNodes, bucketTokens, bucketPairings, bucketPairCodes, bucketAssignments, bucketEvents, bucketPipelineRuns} {
			if _, createErr := tx.CreateBucketIfNotExists(name); createErr != nil {
				return createErr
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateToken(role, subject string, groups []string, lifetime time.Duration) (string, TokenRecord, error) {
	if role != "admin" && role != "producer" && role != "node" && role != "observer" {
		return "", TokenRecord{}, fmt.Errorf("unsupported token role %s", role)
	}
	token, err := randomToken("cb_" + role + "_")
	if err != nil {
		return "", TokenRecord{}, err
	}
	record := TokenRecord{ID: randomID("tok"), Role: role, Subject: cleanLabel(subject, 120), Groups: cleanList(groups, 32, 80), CreatedAt: time.Now().UTC()}
	if lifetime > 0 {
		record.ExpiresAt = record.CreatedAt.Add(lifetime)
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketTokens), tokenHash(token), record)
	})
	return token, record, err
}

func (s *Store) EnsureToken(token, role, subject string, groups []string) error {
	if len(token) < 32 {
		return errors.New("token must contain at least 32 characters")
	}
	if role != "admin" && role != "producer" && role != "node" && role != "observer" {
		return fmt.Errorf("unsupported token role %s", role)
	}
	record := TokenRecord{ID: randomID("tok"), Role: role, Subject: cleanLabel(subject, 120), Groups: cleanList(groups, 32, 80), CreatedAt: time.Now().UTC()}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketTokens)
		key := tokenHash(token)
		if bucket.Get([]byte(key)) != nil {
			return nil
		}
		return putJSON(bucket, key, record)
	})
}

func (s *Store) Authenticate(token string) (TokenRecord, bool) {
	if token == "" {
		return TokenRecord{}, false
	}
	var record TokenRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketTokens), tokenHash(token), &record)
	})
	if err != nil || record.Revoked || (!record.ExpiresAt.IsZero() && time.Now().After(record.ExpiresAt)) {
		return TokenRecord{}, false
	}
	return record, true
}

func (s *Store) RevokeToken(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketTokens)
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var record TokenRecord
			if json.Unmarshal(value, &record) == nil && record.ID == id {
				record.Revoked = true
				return putJSON(bucket, string(key), record)
			}
		}
		return os.ErrNotExist
	})
}

func (s *Store) CreatePairing(request PairRequest, verificationURI string, lifetime time.Duration) (PairResponse, error) {
	deviceCode, err := randomToken("dev_")
	if err != nil {
		return PairResponse{}, err
	}
	userCode, err := randomUserCode()
	if err != nil {
		return PairResponse{}, err
	}
	if lifetime <= 0 || lifetime > 30*time.Minute {
		lifetime = 10 * time.Minute
	}
	pairing := Pairing{
		DeviceCodeHash: tokenHash(deviceCode), UserCode: userCode, NodeName: cleanLabel(request.NodeName, 100),
		PublicKey: request.PublicKey, Groups: cleanList(request.Groups, 16, 80), ExpiresAt: time.Now().UTC().Add(lifetime),
	}
	if _, err := parsePublicKey(pairing.PublicKey); err != nil {
		return PairResponse{}, fmt.Errorf("invalid node public key: %w", err)
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := putJSON(tx.Bucket(bucketPairings), pairing.DeviceCodeHash, pairing); err != nil {
			return err
		}
		return tx.Bucket(bucketPairCodes).Put([]byte(pairing.UserCode), []byte(pairing.DeviceCodeHash))
	})
	return PairResponse{DeviceCode: deviceCode, UserCode: userCode, VerificationURI: verificationURI, ExpiresAt: pairing.ExpiresAt, IntervalSeconds: 5}, err
}

func (s *Store) ListPairings() ([]Pairing, error) {
	result := []Pairing{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPairings).ForEach(func(_, value []byte) error {
			var pairing Pairing
			if err := json.Unmarshal(value, &pairing); err != nil {
				return err
			}
			pairing.PendingToken = ""
			if time.Now().Before(pairing.ExpiresAt) {
				result = append(result, pairing)
			}
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool { return result[i].ExpiresAt.Before(result[j].ExpiresAt) })
	return result, err
}

func (s *Store) DecidePairing(userCode string, approve bool) (Pairing, error) {
	userCode = strings.ToUpper(strings.TrimSpace(userCode))
	var result Pairing
	err := s.db.Update(func(tx *bolt.Tx) error {
		hash := tx.Bucket(bucketPairCodes).Get([]byte(userCode))
		if len(hash) == 0 {
			return os.ErrNotExist
		}
		if err := getJSON(tx.Bucket(bucketPairings), string(hash), &result); err != nil {
			return err
		}
		if time.Now().After(result.ExpiresAt) {
			return errors.New("pairing code expired")
		}
		if !approve {
			result.Denied = true
			return putJSON(tx.Bucket(bucketPairings), result.DeviceCodeHash, result)
		}
		token, tokenErr := randomToken("cb_node_")
		if tokenErr != nil {
			return tokenErr
		}
		result.Approved = true
		result.NodeID = randomID("node")
		result.PendingToken = token
		record := TokenRecord{ID: randomID("tok"), Role: "node", Subject: result.NodeID, Groups: result.Groups, CreatedAt: time.Now().UTC()}
		if err := putJSON(tx.Bucket(bucketTokens), tokenHash(token), record); err != nil {
			return err
		}
		node := Node{ID: result.NodeID, Name: result.NodeName, PublicKey: result.PublicKey, State: "paired", LastSeen: time.Now().UTC()}
		if err := putJSON(tx.Bucket(bucketNodes), node.ID, node); err != nil {
			return err
		}
		return putJSON(tx.Bucket(bucketPairings), result.DeviceCodeHash, result)
	})
	result.PendingToken = ""
	return result, err
}

func (s *Store) PollPairing(deviceCode string) (string, Pairing, string, error) {
	hash := tokenHash(deviceCode)
	var pairing Pairing
	var token string
	state := "authorization_pending"
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketPairings), hash, &pairing); err != nil {
			return err
		}
		if time.Now().After(pairing.ExpiresAt) {
			state = "expired_token"
			return nil
		}
		if pairing.Denied {
			state = "access_denied"
			return nil
		}
		if !pairing.Approved || pairing.PendingToken == "" {
			return nil
		}
		state = "approved"
		token = pairing.PendingToken
		pairing.PendingToken = ""
		if err := putJSON(tx.Bucket(bucketPairings), hash, pairing); err != nil {
			return err
		}
		return tx.Bucket(bucketPairCodes).Delete([]byte(pairing.UserCode))
	})
	pairing.PendingToken = ""
	return state, pairing, token, err
}

func (s *Store) UpsertNode(node Node) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var existing Node
		_ = getJSON(tx.Bucket(bucketNodes), node.ID, &existing)
		node.JobsTotal = existing.JobsTotal
		node.JobsFailed = existing.JobsFailed
		node.ComputeMS = existing.ComputeMS
		node.CostUSD = existing.CostUSD
		if node.PublicKey == "" {
			node.PublicKey = existing.PublicKey
		}
		return putJSON(tx.Bucket(bucketNodes), node.ID, node)
	})
}

func (s *Store) GetNode(id string) (Node, error) {
	var node Node
	err := s.db.View(func(tx *bolt.Tx) error { return getJSON(tx.Bucket(bucketNodes), id, &node) })
	return node, err
}

func (s *Store) ListNodes() ([]Node, error) {
	nodes := []Node{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketNodes).ForEach(func(_, value []byte) error {
			var node Node
			if err := json.Unmarshal(value, &node); err != nil {
				return err
			}
			nodes = append(nodes, node)
			return nil
		})
	})
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, err
}

func (s *Store) SetNodeConnected(id string, connected bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var node Node
		if err := getJSON(tx.Bucket(bucketNodes), id, &node); err != nil {
			return err
		}
		node.Connected = connected
		node.LastSeen = time.Now().UTC()
		if connected {
			node.State = "online"
			if node.ConnectedAt.IsZero() {
				node.ConnectedAt = node.LastSeen
			}
		} else {
			node.State = "offline"
			node.Capabilities.Running = 0
		}
		return putJSON(tx.Bucket(bucketNodes), id, node)
	})
}

func (s *Store) CreateJob(request SubmitRequest) (Job, error) {
	now := time.Now().UTC()
	job := Job{
		ID: request.ID, OwnerSubject: cleanLabel(request.OwnerSubject, 120), TenantID: cleanLabel(request.TenantID, 200), Source: cleanLabel(request.Source, 120), Requirements: request.Requirements,
		Payload: request.Payload, SealedPayload: request.Sealed, Status: JobQueued, Priority: request.Priority,
		MaxAttempts: request.MaxAttempts, CreatedAt: now, UpdatedAt: now,
	}
	if job.ID == "" {
		job.ID = randomID("job")
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = 3
	}
	if job.MaxAttempts > 10 {
		job.MaxAttempts = 10
	}
	if job.Priority < -100 || job.Priority > 100 {
		return Job{}, errors.New("priority must be between -100 and 100")
	}
	if len(job.Payload) == 0 && job.SealedPayload == nil {
		return Job{}, errors.New("payload or sealed_payload is required")
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketJobs).Get([]byte(job.ID)) != nil {
			return os.ErrExist
		}
		if err := putJSON(tx.Bucket(bucketJobs), job.ID, job); err != nil {
			return err
		}
		if err := tx.Bucket(bucketJobIndex).Put(jobIndexKey(job), []byte(job.ID)); err != nil {
			return err
		}
		return tx.Bucket(bucketQueue).Put(queueKey(job), []byte(job.ID))
	})
	return job, err
}

func (s *Store) CreateReservation(assignment Assignment, secret string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketAssignments), assignment.ID, reservation{Assignment: assignment, SecretHash: tokenHash(secret)})
	})
}

func (s *Store) ConsumeReservation(id, secret string, sealed *SealedEnvelope, source, tenant, owner string, priority, attempts int) (Job, error) {
	var job Job
	err := s.db.Update(func(tx *bolt.Tx) error {
		var saved reservation
		if err := getJSON(tx.Bucket(bucketAssignments), id, &saved); err != nil {
			return err
		}
		if time.Now().After(saved.Assignment.ExpiresAt) || !constantEqual(saved.SecretHash, tokenHash(secret)) {
			return errors.New("assignment is invalid or expired")
		}
		if sealed == nil {
			return errors.New("reserved assignments require a sealed payload")
		}
		now := time.Now().UTC()
		job = Job{ID: saved.Assignment.JobID, OwnerSubject: cleanLabel(owner, 120), Source: cleanLabel(source, 120), TenantID: cleanLabel(tenant, 200), Requirements: saved.Assignment.Requirements, SealedPayload: sealed, Status: JobQueued, AssignedNode: saved.Assignment.NodeID, Priority: priority, MaxAttempts: attempts, CreatedAt: now, UpdatedAt: now}
		if job.MaxAttempts <= 0 {
			job.MaxAttempts = 1
		}
		if err := putJSON(tx.Bucket(bucketJobs), job.ID, job); err != nil {
			return err
		}
		if err := tx.Bucket(bucketJobIndex).Put(jobIndexKey(job), []byte(job.ID)); err != nil {
			return err
		}
		if err := tx.Bucket(bucketQueue).Put(queueKey(job), []byte(job.ID)); err != nil {
			return err
		}
		return tx.Bucket(bucketAssignments).Delete([]byte(id))
	})
	return job, err
}

func (s *Store) GetJob(id string) (Job, error) {
	var job Job
	err := s.db.View(func(tx *bolt.Tx) error { return getJSON(tx.Bucket(bucketJobs), id, &job) })
	return job, err
}

func (s *Store) SaveJob(job Job) error {
	job.UpdatedAt = time.Now().UTC()
	return s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bucketJobs), job.ID, job) })
}

func (s *Store) AssignJob(id, nodeID string) (Job, error) {
	var job Job
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketJobs), id, &job); err != nil {
			return err
		}
		if job.Status != JobQueued {
			return errors.New("job is no longer queued")
		}
		if job.SealedPayload != nil && job.AssignedNode != "" && job.AssignedNode != nodeID {
			return errors.New("sealed job is bound to another node")
		}
		job.Status = JobAssigned
		job.AssignedNode = nodeID
		job.Attempt++
		job.AssignedAt = time.Now().UTC()
		job.UpdatedAt = job.AssignedAt
		if err := putJSON(tx.Bucket(bucketJobs), id, job); err != nil {
			return err
		}
		return deleteQueueEntry(tx.Bucket(bucketQueue), id)
	})
	return job, err
}

func (s *Store) MarkRunning(id, nodeID string) (Job, error) {
	var job Job
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketJobs), id, &job); err != nil {
			return err
		}
		if job.Status != JobAssigned || job.AssignedNode != nodeID {
			return errors.New("job is not assigned to this node")
		}
		job.Status = JobRunning
		job.StartedAt = time.Now().UTC()
		job.UpdatedAt = job.StartedAt
		return putJSON(tx.Bucket(bucketJobs), id, job)
	})
	return job, err
}

func (s *Store) CancelJob(id string) (Job, error) {
	var job Job
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketJobs), id, &job); err != nil {
			return err
		}
		if job.Status == JobCompleted || job.Status == JobFailed || job.Status == JobCancelled {
			return errors.New("job is already final")
		}
		job.Status = JobCancelled
		job.FinishedAt = time.Now().UTC()
		job.UpdatedAt = job.FinishedAt
		if err := deleteQueueEntry(tx.Bucket(bucketQueue), id); err != nil {
			return err
		}
		return putJSON(tx.Bucket(bucketJobs), id, job)
	})
	return job, err
}

func (s *Store) CompleteJob(id string, result json.RawMessage, sealed *SealedEnvelope, usage Usage, jobError string) (Job, error) {
	var job Job
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketJobs), id, &job); err != nil {
			return err
		}
		if job.Status != JobAssigned && job.Status != JobRunning {
			return errors.New("job is not assigned")
		}
		job.Result = result
		job.SealedResult = sealed
		if !job.AssignedAt.IsZero() && job.AssignedAt.After(job.CreatedAt) {
			usage.QueueMS = uint64(job.AssignedAt.Sub(job.CreatedAt).Milliseconds())
		}
		job.Usage = usage
		job.Error = cleanLabel(jobError, 500)
		job.FinishedAt = time.Now().UTC()
		job.UpdatedAt = job.FinishedAt
		if jobError == "" {
			job.Status = JobCompleted
		} else if job.Attempt < job.MaxAttempts && job.SealedPayload == nil {
			job.Status = JobQueued
			job.AssignedNode = ""
			job.Result = nil
			job.SealedResult = nil
			if err := tx.Bucket(bucketQueue).Put(queueKey(job), []byte(job.ID)); err != nil {
				return err
			}
		} else {
			job.Status = JobFailed
		}
		if err := putJSON(tx.Bucket(bucketJobs), id, job); err != nil {
			return err
		}
		if job.Status == JobCompleted || job.Status == JobFailed {
			var node Node
			if getJSON(tx.Bucket(bucketNodes), job.AssignedNode, &node) == nil {
				node.JobsTotal++
				if job.Status == JobFailed {
					node.JobsFailed++
				}
				node.ComputeMS += usage.ComputeMS
				node.CostUSD += usage.EstimatedCostUSD
				_ = putJSON(tx.Bucket(bucketNodes), node.ID, node)
			}
		}
		return nil
	})
	return job, err
}

func (s *Store) RequeueNode(nodeID, reason string) ([]Job, error) {
	updated := []Job{}
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketJobs)
		type pending struct {
			key []byte
			job Job
		}
		changes := []pending{}
		if err := bucket.ForEach(func(key, value []byte) error {
			var job Job
			if json.Unmarshal(value, &job) != nil || job.AssignedNode != nodeID || (job.Status != JobAssigned && job.Status != JobRunning) {
				return nil
			}
			job.Error = cleanLabel(reason, 500)
			job.UpdatedAt = time.Now().UTC()
			if job.SealedPayload == nil && job.Attempt < job.MaxAttempts {
				job.Status = JobQueued
				job.AssignedNode = ""
				if err := tx.Bucket(bucketQueue).Put(queueKey(job), []byte(job.ID)); err != nil {
					return err
				}
			} else {
				job.Status = JobFailed
				job.FinishedAt = job.UpdatedAt
			}
			changes = append(changes, pending{key: append([]byte(nil), key...), job: job})
			updated = append(updated, job)
			return nil
		}); err != nil {
			return err
		}
		for _, change := range changes {
			encoded, err := json.Marshal(change.job)
			if err != nil {
				return err
			}
			if err := bucket.Put(change.key, encoded); err != nil {
				return err
			}
		}
		return nil
	})
	return updated, err
}

func (s *Store) RecoverStaleJobs(now time.Time, sealedWait, execution time.Duration) ([]Job, error) {
	if sealedWait <= 0 {
		sealedWait = 2 * time.Minute
	}
	if execution <= 0 {
		execution = 15 * time.Minute
	}
	updated := []Job{}
	err := s.db.Update(func(tx *bolt.Tx) error {
		jobs := tx.Bucket(bucketJobs)
		type change struct {
			key []byte
			job Job
		}
		changes := []change{}
		if err := jobs.ForEach(func(key, value []byte) error {
			var job Job
			if json.Unmarshal(value, &job) != nil {
				return nil
			}
			staleBound := job.Status == JobQueued && job.SealedPayload != nil && job.AssignedNode != "" && now.Sub(job.CreatedAt) > sealedWait
			staleExecution := (job.Status == JobAssigned || job.Status == JobRunning) && !job.AssignedAt.IsZero() && now.Sub(job.AssignedAt) > execution
			if !staleBound && !staleExecution {
				return nil
			}
			job.UpdatedAt = now
			if staleExecution && job.SealedPayload == nil && job.Attempt < job.MaxAttempts {
				job.Status, job.AssignedNode, job.Error = JobQueued, "", "worker execution timed out; job requeued"
				job.StartedAt, job.AssignedAt = time.Time{}, time.Time{}
				if err := tx.Bucket(bucketQueue).Put(queueKey(job), []byte(job.ID)); err != nil {
					return err
				}
			} else {
				job.Status, job.Error, job.FinishedAt = JobFailed, "encrypted worker reservation or execution expired", now
				if err := deleteQueueEntry(tx.Bucket(bucketQueue), job.ID); err != nil {
					return err
				}
			}
			changes = append(changes, change{key: append([]byte(nil), key...), job: job})
			updated = append(updated, job)
			return nil
		}); err != nil {
			return err
		}
		for _, item := range changes {
			raw, err := json.Marshal(item.job)
			if err != nil {
				return err
			}
			if err := jobs.Put(item.key, raw); err != nil {
				return err
			}
		}
		return nil
	})
	return updated, err
}

func (s *Store) QueuedJobs(limit int) ([]Job, error) {
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	queued := make([]Job, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketQueue).Cursor()
		for key, id := cursor.First(); key != nil && len(queued) < limit; key, id = cursor.Next() {
			var job Job
			if err := getJSON(tx.Bucket(bucketJobs), string(id), &job); err != nil {
				return err
			}
			if job.Status == JobQueued {
				queued = append(queued, job)
			}
		}
		return nil
	})
	return queued, err
}

func (s *Store) ListJobs(limit int, status string) ([]Job, error) {
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	jobs := []Job{}
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketJobIndex).Cursor()
		for key, id := cursor.Last(); key != nil && len(jobs) < limit; key, id = cursor.Prev() {
			var job Job
			if err := getJSON(tx.Bucket(bucketJobs), string(id), &job); err != nil {
				return err
			}
			if status == "" || job.Status == status {
				jobs = append(jobs, job)
			}
		}
		return nil
	})
	return jobs, err
}

func (s *Store) CountJobs(status string) (int, error) {
	count := 0
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobs).ForEach(func(_, value []byte) error {
			var job Job
			if err := json.Unmarshal(value, &job); err != nil {
				return err
			}
			if status == "" || job.Status == status {
				count++
			}
			return nil
		})
	})
	return count, err
}

func (s *Store) EstimateVRAM(requirements Requirements) uint64 {
	samples := []uint64{}
	_ = s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketJobIndex).Cursor()
		for key, id := cursor.Last(); key != nil && len(samples) < 500; key, id = cursor.Prev() {
			var job Job
			if getJSON(tx.Bucket(bucketJobs), string(id), &job) != nil || job.Status != JobCompleted || job.Usage.PeakVRAMBytes == 0 {
				continue
			}
			if requirements.Model != "" && !strings.EqualFold(job.Requirements.Model, requirements.Model) {
				continue
			}
			if requirements.Model == "" && !strings.EqualFold(job.Requirements.Task, requirements.Task) {
				continue
			}
			samples = append(samples, job.Usage.PeakVRAMBytes)
		}
		return nil
	})
	if len(samples) < 3 {
		return 0
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	estimate := samples[(len(samples)-1)*9/10]
	return estimate + estimate/10
}

func (s *Store) AddEvent(event Event) error {
	if event.ID == "" {
		event.ID = randomID("event")
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		key := fmt.Sprintf("%020d:%s", event.Time.UnixNano(), event.ID)
		return putJSON(tx.Bucket(bucketEvents), key, event)
	})
}

func (s *Store) ListEvents(limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	events := []Event{}
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketEvents).Cursor()
		for key, value := cursor.Last(); key != nil && len(events) < limit; key, value = cursor.Prev() {
			var event Event
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			events = append(events, event)
		}
		return nil
	})
	return events, err
}

func (s *Store) Overview() (Overview, error) {
	overview := Overview{JobsByState: map[string]uint64{}, GeneratedAt: time.Now().UTC()}
	nodes, err := s.ListNodes()
	if err != nil {
		return overview, err
	}
	overview.NodesTotal = len(nodes)
	for _, node := range nodes {
		if node.Connected && time.Since(node.LastSeen) < 30*time.Second {
			overview.NodesOnline++
		}
		overview.Usage.ComputeMS += node.ComputeMS
		overview.Usage.EstimatedCostUSD += node.CostUSD
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobs).ForEach(func(_, value []byte) error {
			var job Job
			if err := json.Unmarshal(value, &job); err != nil {
				return err
			}
			overview.JobsByState[job.Status]++
			overview.Usage.InputTokens += job.Usage.InputTokens
			overview.Usage.OutputTokens += job.Usage.OutputTokens
			overview.Usage.TotalTokens += job.Usage.TotalTokens
			overview.Usage.EquivalentCostUSD += job.Usage.EquivalentCostUSD
			overview.Usage.SavedCostUSD += job.Usage.SavedCostUSD
			return nil
		})
	})
	return overview, err
}

func (s *Store) SavePipelineRun(run PipelineRun) error {
	return s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(bucketPipelineRuns), run.ID, run) })
}

func (s *Store) GetPipelineRun(id string) (PipelineRun, error) {
	var run PipelineRun
	err := s.db.View(func(tx *bolt.Tx) error { return getJSON(tx.Bucket(bucketPipelineRuns), id, &run) })
	return run, err
}

func (s *Store) ListPipelineRuns(limit int) ([]PipelineRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	runs := []PipelineRun{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPipelineRuns).ForEach(func(_, value []byte) error {
			var run PipelineRun
			if err := json.Unmarshal(value, &run); err != nil {
				return err
			}
			runs = append(runs, run)
			return nil
		})
	})
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, err
}

func putJSON(bucket *bolt.Bucket, key string, value interface{}) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put([]byte(key), encoded)
}

func getJSON(bucket *bolt.Bucket, key string, target interface{}) error {
	value := bucket.Get([]byte(key))
	if value == nil {
		return os.ErrNotExist
	}
	return json.Unmarshal(value, target)
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken(prefix string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + encode(raw), nil
}

func randomID(prefix string) string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(raw)
}

func randomUserCode() (string, error) {
	raw := make([]byte, 5)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	return code[:4] + "-" + code[4:8], nil
}

func jobIndexKey(job Job) []byte {
	return []byte(fmt.Sprintf("%020d:%s", job.CreatedAt.UnixNano(), job.ID))
}

func queueKey(job Job) []byte {
	return []byte(fmt.Sprintf("%03d:%020d:%s", 100-job.Priority, job.CreatedAt.UnixNano(), job.ID))
}

func deleteQueueEntry(bucket *bolt.Bucket, jobID string) error {
	cursor := bucket.Cursor()
	for key, id := cursor.First(); key != nil; key, id = cursor.Next() {
		if string(id) == jobID {
			return bucket.Delete(key)
		}
	}
	return nil
}

func cleanLabel(value string, limit int) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func cleanList(values []string, maxItems, maxLength int) []string {
	if len(values) > maxItems {
		values = values[:maxItems]
	}
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = cleanLabel(value, maxLength)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
