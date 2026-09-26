package cluster

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketJobs                = []byte("jobs")
	bucketJobIndex            = []byte("job_index")
	bucketJobOwnerIndex       = []byte("job_owner_index_v1")
	bucketStoreMeta           = []byte("store_meta")
	bucketQueue               = []byte("queue")
	bucketQueueJobIndex       = []byte("queue_job_index_v1")
	bucketQueueCounts         = []byte("queue_counts_v1")
	bucketNodes               = []byte("nodes")
	bucketTokens              = []byte("tokens")
	bucketPairings            = []byte("pairings")
	bucketPairCodes           = []byte("pair_codes")
	bucketAssignments         = []byte("assignments")
	bucketEvents              = []byte("events")
	bucketJobEvents           = []byte("job_events_v1")
	bucketPipelineEvents      = []byte("pipeline_events_v1")
	bucketPipelineRuns        = []byte("pipeline_runs")
	bucketSessionPlacements   = []byte("session_placements_v1")
	bucketAdapterSessionLocks = []byte("adapter_session_locks_v1")
	bucketJobIdempotency      = []byte("job_idempotency_v1")
	bucketJobIdempotencyByJob = []byte("job_idempotency_by_job_v1")
	bucketProducerRateWindows = []byte("producer_rate_windows_v1")
	keyJobOwnerIndexVersion   = []byte("job_owner_index_version")
	jobOwnerIndexVersion      = []byte("1")
	keyJobContractVersion     = []byte("job_contract_version")
	jobContractVersion        = []byte("1")
	keyQueueIndexVersion      = []byte("queue_index_version")
	queueIndexVersion         = []byte("3")
	keyClusterID              = []byte("cluster_id_v1")
	keyRelayEpoch             = []byte("relay_epoch_v1")
)

func requiredStoreBuckets() [][]byte {
	return [][]byte{
		bucketJobs, bucketJobIndex, bucketJobOwnerIndex, bucketStoreMeta,
		bucketQueue, bucketQueueJobIndex, bucketQueueCounts, bucketNodes, bucketTokens, bucketPairings, bucketPairCodes,
		bucketAssignments, bucketEvents, bucketJobEvents, bucketPipelineEvents, bucketPipelineRuns, bucketSessionPlacements,
		bucketAdapterSessionLocks, bucketJobIdempotency, bucketJobIdempotencyByJob, bucketProducerRateWindows,
		bucketHistoricalTotals,
	}
}

const maximumPendingPairings = 1000

type Store struct {
	db                      *bolt.DB
	savePipelineRunTestHook func(PipelineRun) error
	saveJobEventTestHook    func(JobEvent) error
}

var (
	ErrQueueFull                   = errors.New("relay queue is full")
	ErrOwnerQueueCapacity          = errors.New("producer queue capacity is full")
	ErrOwnerRateCapacity           = errors.New("producer hourly job capacity is full")
	ErrReservationCapacity         = errors.New("assignment reservation capacity is full")
	ErrOwnerReservationCapacity    = errors.New("producer assignment reservation capacity is full")
	ErrReservationOwnerMismatch    = errors.New("assignment belongs to another producer")
	ErrReservationContextMismatch  = errors.New("assignment tenant context does not match the reservation")
	ErrReservationInvalidOrExpired = errors.New("assignment is invalid or expired")
	ErrPipelineCapacity            = errors.New("active pipeline capacity is full")
	ErrOwnerPipelineCapacity       = errors.New("producer active pipeline capacity is full")
	ErrNodePublicKeyMismatch       = errors.New("node public key differs from paired identity")
	ErrNodeDraining                = errors.New("node is draining")
	ErrRouteProbeInFlight          = errors.New("route recovery probe is already in flight")
	ErrAssignmentFenceMismatch     = errors.New("assignment fence does not match the current durable assignment")
	ErrAdapterSessionBusy          = errors.New("adapter session already has an active job or reservation")
	ErrIdempotencyConflict         = errors.New("idempotency key was already used for a different request")
)

type reservation struct {
	Assignment   Assignment `json:"assignment"`
	SecretHash   string     `json:"secret_hash"`
	OwnerSubject string     `json:"owner_subject"`
}

type sessionPlacement struct {
	NodeID            string    `json:"node_id"`
	AdapterEndpointID int       `json:"adapter_endpoint_id,omitempty"`
	AdapterPrincipal  string    `json:"adapter_principal,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type jobIdempotencyRecord struct {
	JobID       string `json:"job_id"`
	RequestHash string `json:"request_hash"`
}

type producerRateWindow struct {
	StartedAt time.Time `json:"started_at"`
	Count     int       `json:"count"`
}

// queueEntry is deliberately tiny. Queue admission and fair scheduling must
// not deserialize multi-megabyte request bodies merely to identify an owner.
// The authoritative Job remains in bucketJobs and is loaded only for the
// bounded page that the dispatcher is about to consider.
type queueEntry struct {
	JobID          string         `json:"job_id"`
	OwnerSubject   string         `json:"owner_subject,omitempty"`
	Priority       int            `json:"priority"`
	Requirements   Requirements   `json:"requirements"`
	PolicyDecision PolicyDecision `json:"policy_decision"`
	AssignedNode   string         `json:"assigned_node,omitempty"`
	Sealed         bool           `json:"sealed,omitempty"`
}

type jobHistoryRecord struct {
	ID                        string           `json:"id"`
	ContractVersion           string           `json:"contract_version"`
	OwnerSubject              string           `json:"owner_subject"`
	TenantID                  string           `json:"tenant_id"`
	Source                    string           `json:"source"`
	Pipeline                  string           `json:"pipeline"`
	Step                      string           `json:"step"`
	ParentID                  string           `json:"parent_id"`
	Requirements              Requirements     `json:"requirements"`
	PolicyDecision            PolicyDecision   `json:"policy_decision"`
	Status                    string           `json:"status"`
	Priority                  int              `json:"priority"`
	Attempt                   int              `json:"attempt"`
	MaxAttempts               int              `json:"max_attempts"`
	AssignedNode              string           `json:"assigned_node"`
	AssignmentFence           *AssignmentFence `json:"assignment_fence"`
	ExecutedAdapterEndpointID int              `json:"executed_adapter_endpoint_id"`
	EphemeralAdapterEndpoint  bool             `json:"ephemeral_adapter_endpoint"`
	Error                     string           `json:"error"`
	FailureCode               string           `json:"failure_code"`
	Usage                     Usage            `json:"usage"`
	CreatedAt                 time.Time        `json:"created_at"`
	UpdatedAt                 time.Time        `json:"updated_at"`
	AssignedAt                time.Time        `json:"assigned_at"`
	StartedAt                 time.Time        `json:"started_at"`
	FinishedAt                time.Time        `json:"finished_at"`
}

func (record jobHistoryRecord) Job() Job {
	return Job{
		ID: record.ID, ContractVersion: record.ContractVersion, OwnerSubject: record.OwnerSubject, TenantID: record.TenantID,
		Source: record.Source, Pipeline: record.Pipeline, Step: record.Step, ParentID: record.ParentID,
		Requirements: record.Requirements, PolicyDecision: record.PolicyDecision, Status: record.Status, Priority: record.Priority,
		Attempt: record.Attempt, MaxAttempts: record.MaxAttempts, AssignedNode: record.AssignedNode, AssignmentFence: record.AssignmentFence,
		ExecutedAdapterEndpointID: record.ExecutedAdapterEndpointID, EphemeralAdapterEndpoint: record.EphemeralAdapterEndpoint,
		Error: record.Error, FailureCode: record.FailureCode, Usage: record.Usage,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, AssignedAt: record.AssignedAt, StartedAt: record.StartedAt, FinishedAt: record.FinishedAt,
	}
}

// adapterSessionLock serializes non-ephemeral jobs for one pseudonymous
// producer/session scope. Explicit profiles are independent; provider-less or
// profile-less routes use a wildcard scope that conflicts with every profile.
// Assignment locks expire with their E2EE reservation; job locks live until
// execution is proven terminal.
type adapterSessionLock struct {
	Kind      string    `json:"kind"`
	HolderID  string    `json:"holder_id"`
	Scope     string    `json:"scope"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
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
		for _, name := range requiredStoreBuckets() {
			if _, createErr := tx.CreateBucketIfNotExists(name); createErr != nil {
				return createErr
			}
		}
		if err := ensureJobOwnerIndex(tx); err != nil {
			return err
		}
		if err := ensureJobContractVersion(tx); err != nil {
			return err
		}
		if err := ensureQueueIndex(tx); err != nil {
			return err
		}
		return rebuildAdapterSessionLocks(tx, time.Now().UTC())
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ready performs a constant-cost durable-store probe. Health endpoints must
// not deserialize jobs, nodes, results, or other attacker-influenced records:
// their cost should remain independent of queue and history size. A Bolt view
// also fails after Close, which preserves the fail-closed readiness contract.
func (s *Store) Ready() error {
	return s.db.View(func(tx *bolt.Tx) error {
		for _, name := range requiredStoreBuckets() {
			if tx.Bucket(name) == nil {
				return fmt.Errorf("required store bucket %q is missing", name)
			}
		}
		return nil
	})
}

// AcquireRelayAuthority returns the stable identity of this durable store and
// advances its process epoch in the same transaction. Gaps are harmless (a
// later startup step may fail), while reuse or wraparound would make stale
// leadership indistinguishable and therefore fails closed.
func (s *Store) AcquireRelayAuthority() (RelayAuthority, error) {
	var authority RelayAuthority
	err := s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketStoreMeta)
		clusterID := string(meta.Get(keyClusterID))
		if clusterID == "" {
			generated, err := randomToken("cluster_")
			if err != nil {
				return fmt.Errorf("generate cluster identity: %w", err)
			}
			clusterID = generated
			if err := meta.Put(keyClusterID, []byte(clusterID)); err != nil {
				return err
			}
		}
		if !validRoutingLabel(clusterID, 120) {
			return errors.New("durable cluster identity is invalid")
		}
		rawEpoch := meta.Get(keyRelayEpoch)
		var previous uint64
		if len(rawEpoch) != 0 {
			if len(rawEpoch) != 8 {
				return errors.New("durable relay epoch is invalid")
			}
			previous = binary.BigEndian.Uint64(rawEpoch)
		}
		if previous == ^uint64(0) {
			return errors.New("durable relay epoch is exhausted")
		}
		next := previous + 1
		encoded := make([]byte, 8)
		binary.BigEndian.PutUint64(encoded, next)
		if err := meta.Put(keyRelayEpoch, encoded); err != nil {
			return err
		}
		authority = RelayAuthority{ClusterID: clusterID, Epoch: next}
		return nil
	})
	return authority, err
}

// ensureJobContractVersion upgrades retained pre-contract records exactly
// once. Empty means the compatibility baseline (V1); an explicit unknown
// version fails closed so an older binary never silently reinterprets future
// durable state.
func ensureJobContractVersion(tx *bolt.Tx) error {
	meta := tx.Bucket(bucketStoreMeta)
	if bytes.Equal(meta.Get(keyJobContractVersion), jobContractVersion) {
		return nil
	}
	type update struct {
		id  string
		job Job
	}
	updates := make([]update, 0)
	if err := tx.Bucket(bucketJobs).ForEach(func(key, value []byte) error {
		var job Job
		if err := json.Unmarshal(value, &job); err != nil {
			return fmt.Errorf("migrate job contract for %q: %w", key, err)
		}
		if job.ID == "" || job.ID != string(key) {
			return fmt.Errorf("migrate job contract: record key %q does not match id %q", key, job.ID)
		}
		version, err := NormalizeJobContractVersion(job.ContractVersion)
		if err != nil {
			return fmt.Errorf("migrate job contract for %q: %w", key, err)
		}
		if job.ContractVersion != version {
			job.ContractVersion = version
			updates = append(updates, update{id: string(key), job: job})
		}
		return nil
	}); err != nil {
		return err
	}
	for _, item := range updates {
		if err := putJSON(tx.Bucket(bucketJobs), item.id, item.job); err != nil {
			return err
		}
	}
	return meta.Put(keyJobContractVersion, jobContractVersion)
}

func (s *Store) CreateToken(role, subject string, groups []string, lifetime time.Duration) (string, TokenRecord, error) {
	return s.CreateTokenWithLimits(role, subject, groups, lifetime, ProducerLimits{})
}

func (s *Store) CreateTokenWithLimits(role, subject string, groups []string, lifetime time.Duration, limits ProducerLimits) (string, TokenRecord, error) {
	if role != "admin" && role != "producer" && role != "node" && role != "observer" {
		return "", TokenRecord{}, fmt.Errorf("unsupported token role %s", role)
	}
	if err := validateTokenIdentity(role, subject, groups); err != nil {
		return "", TokenRecord{}, err
	}
	if err := validateProducerLimits(role, limits); err != nil {
		return "", TokenRecord{}, err
	}
	if lifetime < 0 || lifetime > 10*365*24*time.Hour {
		return "", TokenRecord{}, errors.New("token lifetime must be zero or at most 10 years")
	}
	token, err := randomToken("cb_" + role + "_")
	if err != nil {
		return "", TokenRecord{}, err
	}
	record := TokenRecord{ID: randomID("tok"), Role: role, Subject: cleanLabel(subject, 120), Groups: cleanList(groups, 32, 80), ProducerLimits: normalizeProducerLimits(limits), CreatedAt: time.Now().UTC()}
	if lifetime > 0 {
		record.ExpiresAt = record.CreatedAt.Add(lifetime)
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketTokens), tokenHash(token), record)
	})
	return token, record, err
}

func validateProducerLimits(role string, limits ProducerLimits) error {
	if role != "producer" && (limits.MaxQueuedJobs != 0 || limits.MaxJobsPerHour != 0 || len(limits.Providers) != 0 || limits.Egress != "") {
		return errors.New("producer limits may only be assigned to producer tokens")
	}
	if limits.MaxQueuedJobs < 0 || limits.MaxQueuedJobs > maxQueuedJobsPerOwner {
		return fmt.Errorf("producer_limits.max_queued_jobs must be 0 to %d", maxQueuedJobsPerOwner)
	}
	if limits.MaxJobsPerHour < 0 || limits.MaxJobsPerHour > 1_000_000 {
		return errors.New("producer_limits.max_jobs_per_hour must be 0 to 1000000")
	}
	if len(limits.Providers) > 32 {
		return errors.New("producer_limits.providers accepts at most 32 providers")
	}
	seen := map[string]struct{}{}
	for _, provider := range limits.Providers {
		if !validRoutingLabel(provider, 80) || strings.Contains(provider, "..") {
			return errors.New("producer_limits.providers contains an invalid provider")
		}
		key := strings.ToLower(provider)
		if _, exists := seen[key]; exists {
			return errors.New("producer_limits.providers contains a duplicate provider")
		}
		seen[key] = struct{}{}
	}
	if limits.Egress != "" && limits.Egress != "local_only" {
		return errors.New("producer_limits.egress must be empty or local_only")
	}
	return nil
}

func normalizeProducerLimits(limits ProducerLimits) ProducerLimits {
	limits.Providers = cleanList(limits.Providers, 32, 80)
	limits.Egress = strings.ToLower(strings.TrimSpace(limits.Egress))
	return limits
}

func (s *Store) EnsureToken(token, role, subject string, groups []string) error {
	if len(token) < 32 {
		return errors.New("token must contain at least 32 characters")
	}
	if role != "admin" && role != "producer" && role != "node" && role != "observer" {
		return fmt.Errorf("unsupported token role %s", role)
	}
	if err := validateTokenIdentity(role, subject, groups); err != nil {
		return err
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

func validateTokenIdentity(role, subject string, groups []string) error {
	if (role == "producer" || role == "node") && !validRoutingLabel(subject, 120) {
		return fmt.Errorf("%s token subject must be 1 to 120 safe UTF-8 bytes", role)
	}
	if subject != "" && !validRoutingLabel(subject, 120) {
		return errors.New("token subject must be at most 120 safe UTF-8 bytes")
	}
	if len(groups) > 32 {
		return errors.New("token may contain at most 32 groups")
	}
	for _, group := range groups {
		if !validRoutingLabel(group, 80) {
			return errors.New("token groups must contain 1 to 80 safe UTF-8 bytes")
		}
	}
	return nil
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
	if lifetime <= 0 || lifetime > 30*time.Minute {
		lifetime = 10 * time.Minute
	}
	pairing := Pairing{
		DeviceCodeHash: tokenHash(deviceCode), NodeName: cleanLabel(request.NodeName, 100),
		PublicKey: request.PublicKey, Groups: cleanList(request.Groups, 16, 80), ExpiresAt: time.Now().UTC().Add(lifetime),
	}
	publicKey, err := parsePublicKey(pairing.PublicKey)
	if err != nil {
		return PairResponse{}, fmt.Errorf("invalid node public key: %w", err)
	}
	pairing.PublicKeyFingerprint = publicKeyFingerprint(publicKey.Bytes())
	err = s.db.Update(func(tx *bolt.Tx) error {
		if _, err := garbageCollectPairings(tx, time.Now().UTC()); err != nil {
			return err
		}
		pairings := tx.Bucket(bucketPairings)
		codes := tx.Bucket(bucketPairCodes)
		if pairings.Stats().KeyN >= maximumPendingPairings {
			return errors.New("too many pending pairing requests")
		}
		for attempt := 0; attempt < 16; attempt++ {
			candidate, codeErr := randomUserCode()
			if codeErr != nil {
				return codeErr
			}
			if codes.Get([]byte(candidate)) == nil {
				pairing.UserCode = candidate
				break
			}
		}
		if pairing.UserCode == "" {
			return errors.New("could not allocate a unique pairing code")
		}
		if err := putJSON(pairings, pairing.DeviceCodeHash, pairing); err != nil {
			return err
		}
		return codes.Put([]byte(pairing.UserCode), []byte(pairing.DeviceCodeHash))
	})
	return PairResponse{DeviceCode: deviceCode, UserCode: pairing.UserCode, VerificationURI: verificationURI, ExpiresAt: pairing.ExpiresAt, IntervalSeconds: 5}, err
}

func (s *Store) ListPairings() ([]Pairing, error) {
	if _, err := s.GarbageCollectPairings(time.Now().UTC()); err != nil {
		return nil, err
	}
	result := []Pairing{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPairings).ForEach(func(_, value []byte) error {
			var pairing Pairing
			if err := json.Unmarshal(value, &pairing); err != nil {
				return err
			}
			if pairing.PublicKeyFingerprint == "" {
				if publicKey, parseErr := parsePublicKey(pairing.PublicKey); parseErr == nil {
					pairing.PublicKeyFingerprint = publicKeyFingerprint(publicKey.Bytes())
				}
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
	if _, err := s.GarbageCollectPairings(time.Now().UTC()); err != nil {
		return Pairing{}, err
	}
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
			if err := deletePairCodeForHash(tx.Bucket(bucketPairCodes), pairing.UserCode, []byte(hash)); err != nil {
				return err
			}
			return tx.Bucket(bucketPairings).Delete([]byte(hash))
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
		// Keep the token-free device record until its normal expiry so a repeat
		// poll preserves the protocol's authorization_pending response. The
		// short approval code is no longer useful and can be removed now.
		if err := putJSON(tx.Bucket(bucketPairings), hash, pairing); err != nil {
			return err
		}
		return deletePairCodeForHash(tx.Bucket(bucketPairCodes), pairing.UserCode, []byte(hash))
	})
	pairing.PendingToken = ""
	return state, pairing, token, err
}

// GarbageCollectPairings deletes only expired pairing delivery records and
// their matching short-code indexes. Node identities and job history are not
// part of this lifecycle.
func (s *Store) GarbageCollectPairings(now time.Time) (int, error) {
	removed := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		removed, err = garbageCollectPairings(tx, now)
		return err
	})
	return removed, err
}

func garbageCollectPairings(tx *bolt.Tx, now time.Time) (int, error) {
	pairings := tx.Bucket(bucketPairings)
	codes := tx.Bucket(bucketPairCodes)
	removed := 0
	cursor := pairings.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		var pairing Pairing
		if err := json.Unmarshal(value, &pairing); err != nil {
			return removed, err
		}
		if pairing.ExpiresAt.After(now) {
			continue
		}
		if err := deletePairCodeForHash(codes, pairing.UserCode, key); err != nil {
			return removed, err
		}
		if err := cursor.Delete(); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func deletePairCodeForHash(codes *bolt.Bucket, userCode string, expectedHash []byte) error {
	key := []byte(strings.ToUpper(strings.TrimSpace(userCode)))
	if current := codes.Get(key); !bytes.Equal(current, expectedHash) {
		return nil
	}
	return codes.Delete(key)
}

func (s *Store) UpsertNode(node Node) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var existing Node
		_ = getJSON(tx.Bucket(bucketNodes), node.ID, &existing)
		mergeStoredNodeState(&node, existing)
		return putJSON(tx.Bucket(bucketNodes), node.ID, node)
	})
}

// UpsertNodePinned stores live node state without allowing possession of the
// bearer token to rotate the public key established during pairing. A token
// created manually by an administrator may pin its key on first use; every
// later connection must present that exact key.
func (s *Store) UpsertNodePinned(node Node) error {
	if _, err := parsePublicKey(node.PublicKey); err != nil {
		return fmt.Errorf("invalid node public key: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		var existing Node
		err := getJSON(tx.Bucket(bucketNodes), node.ID, &existing)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if existing.PublicKey != "" && node.PublicKey != existing.PublicKey {
			return ErrNodePublicKeyMismatch
		}
		if existing.PublicKey != "" {
			node.PublicKey = existing.PublicKey
		}
		mergeStoredNodeState(&node, existing)
		return putJSON(tx.Bucket(bucketNodes), node.ID, node)
	})
}

func mergeStoredNodeState(node *Node, existing Node) {
	node.JobsTotal = existing.JobsTotal
	node.JobsFailed = existing.JobsFailed
	node.ComputeMS = existing.ComputeMS
	node.CostUSD = existing.CostUSD
	node.CostKnownJobs = existing.CostKnownJobs
	node.CostUnknownJobs = existing.CostUnknownJobs
	node.Draining = existing.Draining
	// Routing health is relay-owned. In particular, a reconnecting worker must
	// not be able to clear an open circuit or forge one for a different route.
	node.RoutingHealth = boundedRoutingHealth(existing.RoutingHealth)
	// Historical performance is also relay-owned and derived only from accepted
	// terminal jobs. Heartbeats cannot claim an artificially fast route.
	node.RoutingPerformance = boundedRoutingPerformance(existing.RoutingPerformance)
	if node.Draining && node.Connected {
		node.State = "draining"
	}
	if node.PublicKey == "" {
		node.PublicKey = existing.PublicKey
	}
}

func recordNodeRoutingOutcomeTx(tx *bolt.Tx, nodeID string, requirements Requirements, ownerSubject, admittedRouteKey, jobID string, success bool, failureCode string, now time.Time) error {
	if strings.TrimSpace(nodeID) == "" {
		return nil
	}
	var node Node
	if err := getJSON(tx.Bucket(bucketNodes), nodeID, &node); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	recordRoutingOutcomeForOwnerRoute(&node, requirements, ownerSubject, admittedRouteKey, jobID, success, failureCode, now)
	return putJSON(tx.Bucket(bucketNodes), node.ID, node)
}

// claimRoutingRecoveryProbeTx is the linearization point for half-open routes.
// Ranking is necessarily advisory; this durable transaction guarantees that
// only one queued job can become the recovery probe for each applicable global
// or producer-scoped route record.
func claimRoutingRecoveryProbeTx(tx *bolt.Tx, node *Node, requirements Requirements, ownerSubject, jobID, admittedRouteKey string, now time.Time) (bool, error) {
	if node == nil || node.ID == "" {
		return false, nil
	}
	routeKey, _, _ := routingHealthKey(requirements)
	if validRoutingHealthRouteKey(admittedRouteKey) {
		routeKey = admittedRouteKey
	}
	ownerScope := routingHealthOwnerScope(ownerSubject)
	probation := make([]int, 0, 2)
	for index := range node.RoutingHealth {
		health := &node.RoutingHealth[index]
		if health.RouteKey != routeKey || health.OwnerScope != "" && health.OwnerScope != ownerScope || routingHealthState(*health, now) != 2 {
			continue
		}
		if health.ProbeJobID != "" && health.ProbeJobID != jobID {
			var existing Job
			err := getJSON(tx.Bucket(bucketJobs), health.ProbeJobID, &existing)
			if err == nil {
				return false, ErrRouteProbeInFlight
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
			health.ProbeJobID = ""
			health.ProbeOwnerScope = ""
		}
		probation = append(probation, index)
	}
	if len(probation) == 0 {
		return false, nil
	}
	for _, index := range probation {
		node.RoutingHealth[index].ProbeJobID = jobID
		node.RoutingHealth[index].ProbeOwnerScope = ownerScope
	}
	return true, nil
}

func resolveRoutingRecoveryProbeTx(tx *bolt.Tx, nodeID, jobID, failureCode string, now time.Time) (bool, error) {
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(jobID) == "" {
		return false, nil
	}
	var node Node
	if err := getJSON(tx.Bucket(bucketNodes), nodeID, &node); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	resolved := false
	for index := range node.RoutingHealth {
		health := &node.RoutingHealth[index]
		if health.ProbeJobID != jobID {
			continue
		}
		health.ProbeJobID = ""
		health.ProbeOwnerScope = ""
		if trackRoutingFailure(failureCode) {
			applyRoutingProbeFailure(health, failureCode, now)
		}
		resolved = true
	}
	if !resolved {
		return false, nil
	}
	return true, putJSON(tx.Bucket(bucketNodes), node.ID, node)
}

func routingRecoveryProbeExistsTx(tx *bolt.Tx, nodeID, jobID string) (bool, error) {
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(jobID) == "" {
		return false, nil
	}
	var node Node
	if err := getJSON(tx.Bucket(bucketNodes), nodeID, &node); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for _, health := range node.RoutingHealth {
		if health.ProbeJobID == jobID {
			return true, nil
		}
	}
	return false, nil
}

// ResolveRoutingRecoveryProbe releases a probe only after the relay has proof
// that execution never began or has ended on the explicitly named node. Using
// that evidence node prevents a late result from an older assignment attempt
// from releasing a newer probe for the same job ID on another node. A tracked
// failure reopens the circuit; an empty failure leaves the route in probation.
func (s *Store) ResolveRoutingRecoveryProbe(nodeID, jobID, failureCode string) (bool, error) {
	resolved := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		resolved, err = resolveRoutingRecoveryProbeTx(tx, nodeID, jobID, failureCode, time.Now().UTC())
		return err
	})
	return resolved, err
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
			if node.Draining {
				node.State = "draining"
			} else {
				node.State = "online"
			}
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

// SetNodeDraining changes only relay-owned admission state. It deliberately
// leaves the connection and current execution counters intact so in-flight
// work can finish and remain attributable to the same worker.
func (s *Store) SetNodeDraining(id string, draining bool) (Node, error) {
	var node Node
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketNodes), id, &node); err != nil {
			return err
		}
		node.Draining = draining
		if node.Connected {
			if draining {
				node.State = "draining"
			} else {
				node.State = "online"
			}
		}
		return putJSON(tx.Bucket(bucketNodes), id, node)
	})
	return node, err
}

func (s *Store) CreateJob(request SubmitRequest) (Job, error) {
	job, _, err := s.createJob(request, 0, ProducerLimits{}, "", "")
	return job, err
}

// CreateJobAdmitted atomically checks and consumes queue capacity in the same
// write transaction that creates the job. maxOwner is optional to preserve the
// existing store API; a non-positive limit is unbounded.
func (s *Store) CreateJobAdmitted(request SubmitRequest, maxQueued int, maxOwner ...int) (Job, error) {
	ownerLimit := 0
	if len(maxOwner) > 0 {
		ownerLimit = maxOwner[0]
	}
	job, _, err := s.createJob(request, maxQueued, ProducerLimits{MaxQueuedJobs: ownerLimit}, "", "")
	return job, err
}

func (s *Store) CreateJobAdmittedGoverned(request SubmitRequest, maxQueued int, limits ProducerLimits) (Job, error) {
	job, _, err := s.createJob(request, maxQueued, limits, "", "")
	return job, err
}

// CreateJobAdmittedIdempotent atomically binds an authenticated producer's
// key to one admitted request. An exact retry returns the retained job without
// consuming queue capacity; a different request with the same key fails.
func (s *Store) CreateJobAdmittedIdempotent(request SubmitRequest, maxQueued, maxOwner int, idempotencyKey, requestHash string) (Job, bool, error) {
	return s.createJob(request, maxQueued, ProducerLimits{MaxQueuedJobs: maxOwner}, idempotencyKey, requestHash)
}

func (s *Store) CreateJobAdmittedIdempotentGoverned(request SubmitRequest, maxQueued int, limits ProducerLimits, idempotencyKey, requestHash string) (Job, bool, error) {
	return s.createJob(request, maxQueued, limits, idempotencyKey, requestHash)
}

func (s *Store) createJob(request SubmitRequest, maxQueued int, limits ProducerLimits, idempotencyKey, requestHash string) (Job, bool, error) {
	now := time.Now().UTC()
	job, err := prepareJob(request, idempotencyKey, requestHash, now)
	if err != nil {
		return Job{}, false, err
	}
	replayed := false
	err = s.db.Update(func(tx *bolt.Tx) error {
		var admitErr error
		replayed, admitErr = s.admitPreparedJobTx(tx, &job, maxQueued, limits, idempotencyKey, requestHash, now)
		return admitErr
	})
	return job, replayed, err
}

func prepareJob(request SubmitRequest, idempotencyKey, requestHash string, now time.Time) (Job, error) {
	if err := request.PolicyDecision.ValidateAllowed(); err != nil {
		return Job{}, err
	}
	contractVersion, err := NormalizeJobContractVersion(request.ContractVersion)
	if err != nil {
		return Job{}, err
	}
	job := Job{
		ID: request.ID, ContractVersion: contractVersion, OwnerSubject: cleanLabel(request.OwnerSubject, 120), TenantID: cleanLabel(request.TenantID, 200), Source: cleanLabel(request.Source, 120), Requirements: request.Requirements, PolicyDecision: request.PolicyDecision,
		Payload: request.Payload, SealedPayload: request.Sealed, Status: JobQueued, Priority: request.Priority,
		MaxAttempts: request.MaxAttempts, CreatedAt: now, UpdatedAt: now,
		Pipeline: cleanLabel(request.Pipeline, 128), Step: cleanLabel(request.Step, 128), ParentID: cleanLabel(request.ParentID, 128),
	}
	if job.ID == "" {
		job.ID = randomID("job")
	} else if !validJobID(job.ID) {
		return Job{}, errors.New("job id must use 1-128 safe ASCII characters and must not contain '..'")
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
	if idempotencyKey != "" {
		if job.OwnerSubject == "" {
			return Job{}, errors.New("authenticated producer is required for idempotency")
		}
		if err := ValidateIdempotencyKey(idempotencyKey); err != nil {
			return Job{}, err
		}
		if len(requestHash) != sha256.Size*2 {
			return Job{}, errors.New("idempotency request hash is invalid")
		}
	}
	return job, nil
}

func (s *Store) admitPreparedJobTx(tx *bolt.Tx, job *Job, maxQueued int, limits ProducerLimits, idempotencyKey, requestHash string, now time.Time) (bool, error) {
	if idempotencyKey != "" {
		existing, found, lookupErr := lookupIdempotentJobTx(tx, job.OwnerSubject, idempotencyKey, requestHash)
		if lookupErr != nil {
			return false, lookupErr
		}
		if found {
			*job = existing
			return true, nil
		}
	}
	if tx.Bucket(bucketJobs).Get([]byte(job.ID)) != nil {
		return false, os.ErrExist
	}
	if maxQueued > 0 || limits.MaxQueuedJobs > 0 {
		queued, owned, err := queueCounts(tx, job.OwnerSubject)
		if err != nil {
			return false, err
		}
		if maxQueued > 0 && queued >= maxQueued {
			return false, ErrQueueFull
		}
		if limits.MaxQueuedJobs > 0 && owned >= limits.MaxQueuedJobs {
			return false, ErrOwnerQueueCapacity
		}
	}
	if err := consumeProducerRateLimitTx(tx, job.OwnerSubject, limits.MaxJobsPerHour, now); err != nil {
		return false, err
	}
	if err := putJSON(tx.Bucket(bucketJobs), job.ID, *job); err != nil {
		return false, err
	}
	if err := tx.Bucket(bucketJobIndex).Put(jobIndexKey(*job), []byte(job.ID)); err != nil {
		return false, err
	}
	if err := putJobOwnerIndex(tx.Bucket(bucketJobOwnerIndex), *job); err != nil {
		return false, err
	}
	if err := putQueueEntry(tx, *job); err != nil {
		return false, err
	}
	if idempotencyKey != "" {
		if err := saveIdempotentJobTx(tx, job.OwnerSubject, idempotencyKey, requestHash, job.ID); err != nil {
			return false, err
		}
	}
	if err := appendAuthoritativeJobEventTx(tx, s, *job, "job.accepted"); err != nil {
		return false, err
	}
	if err := appendAuthoritativeJobEventTx(tx, s, *job, "job.queued"); err != nil {
		return false, err
	}
	if err := appendPipelineStepEventTx(tx, s, *job, "pipeline.step.queued"); err != nil {
		return false, err
	}
	return false, nil
}

// CreateDAGChildJobAdmittedGoverned atomically changes one ready DAG node to
// queued and admits its governed child job. A crash can therefore expose
// neither a queued child without its graph checkpoint nor a checkpoint without
// the corresponding job and authoritative events.
func (s *Store) CreateDAGChildJobAdmittedGoverned(runID, step string, request SubmitRequest, maxQueued int, limits ProducerLimits) (PipelineRun, Job, error) {
	now := time.Now().UTC()
	job, err := prepareJob(request, "", "", now)
	if err != nil {
		return PipelineRun{}, Job{}, err
	}
	var updated PipelineRun
	err = s.db.Update(func(tx *bolt.Tx) error {
		var run PipelineRun
		if err := getJSON(tx.Bucket(bucketPipelineRuns), runID, &run); err != nil {
			return err
		}
		if run.Status != "running" || run.Graph == nil {
			return errors.New("DAG run is not active")
		}
		if err := validatePipelineRunGraphState(run); err != nil {
			return err
		}
		if job.ParentID != run.ID || job.Pipeline != run.Pipeline || job.Step != step || job.OwnerSubject != run.OwnerSubject || job.TenantID != run.TenantID {
			return errors.New("DAG child job context does not match its run")
		}
		nodeIndex := -1
		active := 0
		for index, node := range run.NodeStates {
			if node.State == PipelineNodeQueued || node.State == PipelineNodeRunning {
				active++
			}
			if node.Step == step {
				nodeIndex = index
			}
		}
		if nodeIndex < 0 || run.NodeStates[nodeIndex].State != PipelineNodeReady || run.NodeStates[nodeIndex].JobID != "" {
			return errors.New("DAG step is not ready for admission")
		}
		if active >= run.Graph.MaxParallel {
			return errors.New("DAG max_parallel capacity is full")
		}
		if replayed, err := s.admitPreparedJobTx(tx, &job, maxQueued, limits, "", "", now); err != nil {
			return err
		} else if replayed {
			return errors.New("unexpected DAG child replay")
		}
		updated = run
		updated.NodeStates = clonePipelineNodeCheckpoints(run.NodeStates)
		updated.Steps = append(append([]Job(nil), run.Steps...), job)
		updated.NodeStates[nodeIndex].State = PipelineNodeQueued
		updated.NodeStates[nodeIndex].JobID = job.ID
		updated.NodeStates[nodeIndex].UpdatedAt = now
		if err := validatePipelineRunGraphState(updated); err != nil {
			return err
		}
		if err := validatePipelineRunGraphTransition(run, updated); err != nil {
			return err
		}
		return putJSON(tx.Bucket(bucketPipelineRuns), updated.ID, updated)
	})
	return updated, job, err
}

func clonePipelineNodeCheckpoints(nodes []PipelineNodeCheckpoint) []PipelineNodeCheckpoint {
	cloned := append([]PipelineNodeCheckpoint(nil), nodes...)
	for index := range cloned {
		cloned[index].DependsOn = append([]string(nil), nodes[index].DependsOn...)
	}
	return cloned
}

func consumeProducerRateLimitTx(tx *bolt.Tx, owner string, maximum int, now time.Time) error {
	if maximum <= 0 {
		return nil
	}
	if owner == "" {
		return errors.New("producer identity is required for an hourly job limit")
	}
	bucket := tx.Bucket(bucketProducerRateWindows)
	var window producerRateWindow
	err := getJSON(bucket, owner, &window)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) || window.StartedAt.IsZero() || !now.Before(window.StartedAt.Add(time.Hour)) {
		window = producerRateWindow{StartedAt: now, Count: 0}
	} else if now.Before(window.StartedAt) {
		// A backwards wall clock must not reset or silently widen a durable quota.
		return ErrOwnerRateCapacity
	}
	if window.Count >= maximum {
		return ErrOwnerRateCapacity
	}
	window.Count++
	return putJSON(bucket, owner, window)
}

func validJobID(value string) bool {
	if len(value) < 1 || len(value) > 128 || strings.Contains(value, "..") {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		alphaNumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if index == 0 {
			if !alphaNumeric {
				return false
			}
			continue
		}
		if !alphaNumeric && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

// ValidateIdempotencyKey accepts a deliberately small HTTP-safe alphabet.
// Spaces and control characters are excluded so intermediaries cannot
// reinterpret producer keys. The relay stores only a producer-scoped hash.
func ValidateIdempotencyKey(value string) error {
	if len(value) < 1 || len(value) > 200 {
		return errors.New("idempotency key must contain 1-200 visible ASCII characters")
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return errors.New("idempotency key must contain 1-200 visible ASCII characters")
		}
	}
	return nil
}

func jobIdempotencyKey(owner, key string) []byte {
	sum := sha256.Sum256([]byte(cleanLabel(owner, 120) + "\x00" + key + "\x00job-admission-v1"))
	return []byte(hex.EncodeToString(sum[:]))
}

func lookupIdempotentJobTx(tx *bolt.Tx, owner, key, requestHash string) (Job, bool, error) {
	indexKey := jobIdempotencyKey(owner, key)
	raw := tx.Bucket(bucketJobIdempotency).Get(indexKey)
	if raw == nil {
		return Job{}, false, nil
	}
	var record jobIdempotencyRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return Job{}, false, fmt.Errorf("decode idempotency record: %w", err)
	}
	if record.RequestHash != requestHash {
		return Job{}, false, ErrIdempotencyConflict
	}
	var job Job
	if err := getJSON(tx.Bucket(bucketJobs), record.JobID, &job); err != nil {
		return Job{}, false, fmt.Errorf("idempotency record references unavailable job: %w", err)
	}
	if job.OwnerSubject != cleanLabel(owner, 120) {
		return Job{}, false, errors.New("idempotency record owner mismatch")
	}
	return job, true, nil
}

func saveIdempotentJobTx(tx *bolt.Tx, owner, key, requestHash, jobID string) error {
	indexKey := jobIdempotencyKey(owner, key)
	if tx.Bucket(bucketJobIdempotency).Get(indexKey) != nil {
		return ErrIdempotencyConflict
	}
	if err := putJSON(tx.Bucket(bucketJobIdempotency), string(indexKey), jobIdempotencyRecord{JobID: jobID, RequestHash: requestHash}); err != nil {
		return err
	}
	return tx.Bucket(bucketJobIdempotencyByJob).Put([]byte(jobID), indexKey)
}

// CreateReservationAdmitted garbage-collects expired reservations and admits a
// new one under global and per-owner bounds in one write transaction. The
// owner is later required when the reservation is consumed.
func (s *Store) CreateReservationAdmitted(assignment Assignment, secret, owner string, maxGlobal, maxOwner int) error {
	owner = cleanLabel(owner, 120)
	if owner == "" {
		return errors.New("assignment owner is required")
	}
	assignment.OwnerSubject = owner
	if assignment.Attempt == 0 {
		assignment.Attempt = 1
	}
	if assignment.Attempt != 1 {
		return errors.New("reserved assignments must start at attempt 1")
	}
	if err := validateTenantID(assignment.TenantID); err != nil {
		return err
	}
	if err := assignment.PolicyDecision.ValidateAllowed(); err != nil {
		return err
	}
	if assignment.ID == "" || !validJobID(assignment.ID) || assignment.JobID == "" || !validJobID(assignment.JobID) {
		return errors.New("assignment and job ids must use safe ASCII characters")
	}
	if secret == "" {
		return errors.New("assignment secret is required")
	}
	if !assignment.ExpiresAt.After(time.Now().UTC()) {
		return ErrReservationInvalidOrExpired
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		now := time.Now().UTC()
		assignments := tx.Bucket(bucketAssignments)
		var node Node
		if err := getJSON(tx.Bucket(bucketNodes), assignment.NodeID, &node); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if node.Draining {
			return ErrNodeDraining
		}
		if err := garbageCollectReservations(assignments, now); err != nil {
			return err
		}
		if _, err := garbageCollectAdapterSessionLocks(tx, now); err != nil {
			return err
		}
		if assignments.Get([]byte(assignment.ID)) != nil || tx.Bucket(bucketJobs).Get([]byte(assignment.JobID)) != nil {
			return os.ErrExist
		}
		global, owned, err := reservationCounts(assignments, owner)
		if err != nil {
			return err
		}
		if maxGlobal > 0 && global >= maxGlobal {
			return ErrReservationCapacity
		}
		if maxOwner > 0 && owned >= maxOwner {
			return ErrOwnerReservationCapacity
		}
		if err := acquireAdapterSessionLockTx(tx, owner, assignment.Requirements, adapterSessionLockAssignment, assignment.ID, assignment.ExpiresAt, now, adapterSessionNeedsLockTx(tx, assignment.Requirements, assignment.NodeID)); err != nil {
			return err
		}
		return putJSON(assignments, assignment.ID, reservation{Assignment: assignment, SecretHash: tokenHash(secret), OwnerSubject: owner})
	})
}

// ConsumeReservationAdmitted verifies producer ownership and atomically moves
// the reservation into the bounded job queue. If the queue is full the
// reservation remains available until its original expiry.
func (s *Store) ConsumeReservationAdmitted(id, secret string, sealed *SealedEnvelope, source, tenant, owner string, priority, attempts, maxQueued int, maxOwner ...int) (Job, error) {
	ownerLimit := 0
	if len(maxOwner) > 0 {
		ownerLimit = maxOwner[0]
	}
	job, _, err := s.consumeReservationAdmitted(id, secret, sealed, source, tenant, owner, priority, attempts, maxQueued, ProducerLimits{MaxQueuedJobs: ownerLimit}, "", "", nil)
	return job, err
}

// ConsumeReservationAdmittedWithPolicy additionally proves that the relay's
// current policy grants the same authorization as the one authenticated in
// the sealed reservation. It prevents a stale reservation from surviving a
// policy or execution-classification change.
func (s *Store) ConsumeReservationAdmittedWithPolicy(id, secret string, sealed *SealedEnvelope, source, tenant, owner string, priority, attempts, maxQueued int, policy PolicyDecision, maxOwner ...int) (Job, error) {
	ownerLimit := 0
	if len(maxOwner) > 0 {
		ownerLimit = maxOwner[0]
	}
	job, _, err := s.consumeReservationAdmitted(id, secret, sealed, source, tenant, owner, priority, attempts, maxQueued, ProducerLimits{MaxQueuedJobs: ownerLimit}, "", "", &policy)
	return job, err
}

func (s *Store) ConsumeReservationAdmittedGovernedWithPolicy(id, secret string, sealed *SealedEnvelope, source, tenant, owner string, priority, attempts, maxQueued int, limits ProducerLimits, policy PolicyDecision) (Job, error) {
	job, _, err := s.consumeReservationAdmitted(id, secret, sealed, source, tenant, owner, priority, attempts, maxQueued, limits, "", "", &policy)
	return job, err
}

// ConsumeReservationAdmittedIdempotent gives the one-time E2EE promotion the
// same retry semantics as ordinary admission. A replay is resolved before the
// consumed reservation is read, but only when the exact sealed request hash
// matches the producer-scoped key.
func (s *Store) ConsumeReservationAdmittedIdempotent(id, secret string, sealed *SealedEnvelope, source, tenant, owner string, priority, attempts, maxQueued, maxOwner int, idempotencyKey, requestHash string) (Job, bool, error) {
	return s.consumeReservationAdmitted(id, secret, sealed, source, tenant, owner, priority, attempts, maxQueued, ProducerLimits{MaxQueuedJobs: maxOwner}, idempotencyKey, requestHash, nil)
}

func (s *Store) ConsumeReservationAdmittedIdempotentWithPolicy(id, secret string, sealed *SealedEnvelope, source, tenant, owner string, priority, attempts, maxQueued, maxOwner int, idempotencyKey, requestHash string, policy PolicyDecision) (Job, bool, error) {
	return s.consumeReservationAdmitted(id, secret, sealed, source, tenant, owner, priority, attempts, maxQueued, ProducerLimits{MaxQueuedJobs: maxOwner}, idempotencyKey, requestHash, &policy)
}

func (s *Store) ConsumeReservationAdmittedIdempotentGovernedWithPolicy(id, secret string, sealed *SealedEnvelope, source, tenant, owner string, priority, attempts, maxQueued int, limits ProducerLimits, idempotencyKey, requestHash string, policy PolicyDecision) (Job, bool, error) {
	return s.consumeReservationAdmitted(id, secret, sealed, source, tenant, owner, priority, attempts, maxQueued, limits, idempotencyKey, requestHash, &policy)
}

func (s *Store) consumeReservationAdmitted(id, secret string, sealed *SealedEnvelope, source, tenant, owner string, priority, attempts, maxQueued int, limits ProducerLimits, idempotencyKey, requestHash string, expectedPolicy *PolicyDecision) (Job, bool, error) {
	var job Job
	replayed := false
	owner = cleanLabel(owner, 120)
	if owner == "" {
		return Job{}, false, ErrReservationOwnerMismatch
	}
	if sealed == nil {
		return Job{}, false, errors.New("reserved assignments require a sealed payload")
	}
	if idempotencyKey != "" {
		if err := ValidateIdempotencyKey(idempotencyKey); err != nil {
			return Job{}, false, err
		}
		if len(requestHash) != sha256.Size*2 {
			return Job{}, false, errors.New("idempotency request hash is invalid")
		}
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		if idempotencyKey != "" {
			existing, found, lookupErr := lookupIdempotentJobTx(tx, owner, idempotencyKey, requestHash)
			if lookupErr != nil {
				return lookupErr
			}
			if found {
				job = existing
				replayed = true
				return nil
			}
		}
		var saved reservation
		if err := getJSON(tx.Bucket(bucketAssignments), id, &saved); err != nil {
			return err
		}
		now := time.Now().UTC()
		if !saved.Assignment.ExpiresAt.After(now) {
			_ = tx.Bucket(bucketAssignments).Delete([]byte(id))
			_ = releaseAdapterSessionLockTx(tx, saved.OwnerSubject, saved.Assignment.Requirements, adapterSessionLockAssignment, saved.Assignment.ID)
			return ErrReservationInvalidOrExpired
		}
		if !constantEqual(saved.SecretHash, tokenHash(secret)) {
			return ErrReservationInvalidOrExpired
		}
		if saved.OwnerSubject == "" || saved.OwnerSubject != owner || saved.Assignment.OwnerSubject != owner {
			return ErrReservationOwnerMismatch
		}
		if tenant != saved.Assignment.TenantID {
			return ErrReservationContextMismatch
		}
		if expectedPolicy != nil && !saved.Assignment.PolicyDecision.EquivalentAuthorization(*expectedPolicy) {
			return ErrReservationContextMismatch
		}
		if saved.Assignment.Attempt != 1 {
			return ErrReservationContextMismatch
		}
		if tx.Bucket(bucketJobs).Get([]byte(saved.Assignment.JobID)) != nil {
			return os.ErrExist
		}
		if maxQueued > 0 || limits.MaxQueuedJobs > 0 {
			queued, owned, err := queueCounts(tx, owner)
			if err != nil {
				return err
			}
			if maxQueued > 0 && queued >= maxQueued {
				return ErrQueueFull
			}
			if limits.MaxQueuedJobs > 0 && owned >= limits.MaxQueuedJobs {
				return ErrOwnerQueueCapacity
			}
		}
		if err := consumeProducerRateLimitTx(tx, owner, limits.MaxJobsPerHour, now); err != nil {
			return err
		}
		job = Job{ID: saved.Assignment.JobID, ContractVersion: JobContractV1, OwnerSubject: saved.Assignment.OwnerSubject, Source: cleanLabel(source, 120), TenantID: saved.Assignment.TenantID, Requirements: saved.Assignment.Requirements, PolicyDecision: saved.Assignment.PolicyDecision, SealedPayload: sealed, Status: JobQueued, AssignedNode: saved.Assignment.NodeID, Priority: priority, MaxAttempts: attempts, CreatedAt: now, UpdatedAt: now}
		if job.MaxAttempts <= 0 {
			job.MaxAttempts = 1
		}
		if err := promoteAdapterSessionLockTx(tx, saved, job, now); err != nil {
			return err
		}
		if err := putJSON(tx.Bucket(bucketJobs), job.ID, job); err != nil {
			return err
		}
		if err := tx.Bucket(bucketJobIndex).Put(jobIndexKey(job), []byte(job.ID)); err != nil {
			return err
		}
		if err := putJobOwnerIndex(tx.Bucket(bucketJobOwnerIndex), job); err != nil {
			return err
		}
		if err := putQueueEntry(tx, job); err != nil {
			return err
		}
		if idempotencyKey != "" {
			if err := saveIdempotentJobTx(tx, owner, idempotencyKey, requestHash, job.ID); err != nil {
				return err
			}
		}
		if err := appendAuthoritativeJobEventTx(tx, s, job, "job.accepted"); err != nil {
			return err
		}
		if err := appendAuthoritativeJobEventTx(tx, s, job, "job.queued"); err != nil {
			return err
		}
		if err := appendPipelineStepEventTx(tx, s, job, "pipeline.step.queued"); err != nil {
			return err
		}
		return tx.Bucket(bucketAssignments).Delete([]byte(id))
	})
	return job, replayed, err
}

// GarbageCollectReservations removes expired E2EE assignment reservations.
// It is safe to call from the relay's periodic dispatch loop.
func (s *Store) GarbageCollectReservations(now time.Time) (int, error) {
	removed := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		removed, err = garbageCollectReservationsCount(tx.Bucket(bucketAssignments), now)
		if err != nil {
			return err
		}
		_, err = garbageCollectAdapterSessionLocks(tx, now)
		return err
	})
	return removed, err
}

// maintenanceCandidates reports whether the relay has any reservation or
// queued-job records that can require periodic cleanup. It deliberately reads
// only the first key in each small index bucket: terminal Job values may embed
// multi-megabyte artifacts and must not be decoded merely to prove that an
// otherwise idle relay has no stale work.
func (s *Store) maintenanceCandidates() (reservations, queued bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		assignmentKey, _ := tx.Bucket(bucketAssignments).Cursor().First()
		queueKey, _ := tx.Bucket(bucketQueue).Cursor().First()
		reservations = assignmentKey != nil
		queued = queueKey != nil
		return nil
	})
	return reservations, queued, err
}

func garbageCollectReservations(bucket *bolt.Bucket, now time.Time) error {
	_, err := garbageCollectReservationsCount(bucket, now)
	return err
}

func garbageCollectReservationsCount(bucket *bolt.Bucket, now time.Time) (int, error) {
	removed := 0
	cursor := bucket.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		var saved reservation
		if err := json.Unmarshal(value, &saved); err != nil {
			return removed, err
		}
		if saved.Assignment.ExpiresAt.After(now) {
			continue
		}
		if err := cursor.Delete(); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func reservationCounts(bucket *bolt.Bucket, owner string) (global, owned int, err error) {
	err = bucket.ForEach(func(_, value []byte) error {
		var saved reservation
		if decodeErr := json.Unmarshal(value, &saved); decodeErr != nil {
			return decodeErr
		}
		global++
		if saved.OwnerSubject == owner {
			owned++
		}
		return nil
	})
	return global, owned, err
}

func countAndCleanQueue(tx *bolt.Tx) (int, error) {
	total, _, err := queueCounts(tx, "")
	return total, err
}

func queueCounts(tx *bolt.Tx, owner string) (total, owned int, err error) {
	counts := tx.Bucket(bucketQueueCounts)
	total, err = queueCounter(counts, queueTotalCounterKey())
	if err != nil {
		return 0, 0, err
	}
	if owner != "" {
		owned, err = queueCounter(counts, queueOwnerCounterKey(owner))
	}
	return total, owned, err
}

func (s *Store) GetJob(id string) (Job, error) {
	var job Job
	err := s.db.View(func(tx *bolt.Tx) error { return getJSON(tx.Bucket(bucketJobs), id, &job) })
	return job, err
}

// GetJobSummary decodes only bounded lifecycle/routing metadata. Activity and
// status projections must not allocate multi-megabyte prompt/result bodies
// merely to render a child state.
func (s *Store) GetJobSummary(id string) (Job, error) {
	var record jobHistoryRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketJobs).Get([]byte(id))
		if raw == nil {
			return os.ErrNotExist
		}
		return json.Unmarshal(raw, &record)
	})
	return record.Job(), err
}

// RecentSessionNode returns the worker most recently used by this producer's
// logical session. A session is only an affinity hint: the relay can still use
// another compatible worker when the previous one is offline.
func (s *Store) RecentSessionNode(owner, session string) (string, bool) {
	node, _, ok := s.RecentSessionPlacement(owner, Requirements{SessionID: session})
	return node, ok
}

// RecentSessionPlacement keeps an adapter session on the concrete endpoint
// chosen for its previous turn. Adapter endpoint identifiers are meaningful only
// together with their worker node, so both values come from the same job.
func (s *Store) RecentSessionPlacement(owner string, requirements Requirements) (node string, adapterEndpointID int, ok bool) {
	node, adapterEndpointID, _, ok = s.RecentSessionPlacementBinding(owner, requirements)
	return node, adapterEndpointID, ok
}

// RecentSessionPlacementBinding additionally returns the scoped adapter
// identity that owned the endpoint. This prevents a later endpoint-number
// collision from moving a durable session across adapter principals.
func (s *Store) RecentSessionPlacementBinding(owner string, requirements Requirements) (node string, adapterEndpointID int, adapterPrincipal string, ok bool) {
	owner = cleanLabel(owner, 120)
	session := canonicalSessionID(requirements.SessionID)
	key := sessionPlacementKey(owner, requirements)
	var placement sessionPlacement
	if err := s.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketSessionPlacements), key, &placement)
	}); err == nil && placement.NodeID != "" {
		return placement.NodeID, placement.AdapterEndpointID, placement.AdapterPrincipal, true
	}
	var selected Job
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobs).ForEach(func(_, value []byte) error {
			var job Job
			if json.Unmarshal(value, &job) != nil || job.OwnerSubject != owner ||
				canonicalSessionID(job.Requirements.SessionID) != session || job.AssignedNode == "" || !terminalJobStatus(job.Status) ||
				!sameSessionPlacementScope(job.Requirements, requirements) {
				return nil
			}
			if strings.EqualFold(strings.TrimSpace(requirements.Provider), "adapter") && (job.ExecutedAdapterEndpointID <= 0 || job.EphemeralAdapterEndpoint) {
				return nil
			}
			if selected.ID == "" || job.UpdatedAt.After(selected.UpdatedAt) {
				selected = job
			}
			return nil
		})
	})
	if err != nil || selected.AssignedNode == "" {
		return "", 0, "", false
	}
	endpointID := selected.ExecutedAdapterEndpointID
	placement = sessionPlacement{NodeID: selected.AssignedNode, AdapterEndpointID: endpointID, AdapterPrincipal: selected.Requirements.AdapterPrincipal, UpdatedAt: selected.UpdatedAt}
	_ = s.db.Update(func(tx *bolt.Tx) error { return putSessionPlacement(tx, key, placement) })
	return selected.AssignedNode, endpointID, selected.Requirements.AdapterPrincipal, true
}

func canonicalSessionID(session string) string {
	session = strings.TrimSpace(session)
	if session == "" {
		return "default"
	}
	return session
}

func sameSessionPlacementScope(previous, current Requirements) bool {
	previousAdapter := strings.EqualFold(strings.TrimSpace(previous.Provider), "adapter")
	currentAdapter := strings.EqualFold(strings.TrimSpace(current.Provider), "adapter")
	if previousAdapter || currentAdapter {
		return previousAdapter && currentAdapter &&
			strings.EqualFold(strings.TrimSpace(previous.AdapterProfile), strings.TrimSpace(current.AdapterProfile))
	}
	return true
}

func sessionPlacementKey(owner string, requirements Requirements) string {
	scope := "node"
	if strings.EqualFold(strings.TrimSpace(requirements.Provider), "adapter") {
		scope = "adapter\x00" + strings.ToLower(strings.TrimSpace(requirements.AdapterProfile))
	}
	sum := sha256.Sum256([]byte(owner + "\x00" + canonicalSessionID(requirements.SessionID) + "\x00" + scope))
	return hex.EncodeToString(sum[:])
}

const (
	adapterSessionLockAssignment = "assignment"
	adapterSessionLockJob        = "job"
)

func adapterSessionLockEligible(requirements Requirements) bool {
	provider := strings.TrimSpace(requirements.Provider)
	return !requirements.AdapterEphemeralSession && (strings.EqualFold(provider, "adapter") || provider == "")
}

// adapterSessionNeedsLockTx identifies jobs that can reach the adapter
// session boundary. Every provider-less job qualifies because an
// operator-owned route may switch/fallback to a adapter after the last node
// heartbeat; negative or stale capability telemetry is not safety evidence.
func adapterSessionNeedsLockTx(tx *bolt.Tx, requirements Requirements, nodeID string) bool {
	return adapterSessionLockEligible(requirements)
}

func adapterSessionLockBase(owner string, requirements Requirements) string {
	// The worker injects one producer/session key for explicit adapter and
	// provider-less fallback jobs. The profile is a separate selector because
	// the adapter scopes one logical session independently per provider UI.
	sum := sha256.Sum256([]byte(cleanLabel(owner, 120) + "\x00" + canonicalSessionID(requirements.SessionID) + "\x00adapter-session-lock-v1"))
	return hex.EncodeToString(sum[:])
}

func adapterSessionLockScope(requirements Requirements) string {
	if strings.EqualFold(strings.TrimSpace(requirements.Provider), "adapter") {
		if profile := strings.ToLower(strings.TrimSpace(requirements.AdapterProfile)); profile != "" {
			return "profile:" + profile
		}
	}
	return "*"
}

func adapterSessionLockPrefix(owner string, requirements Requirements) string {
	return adapterSessionLockBase(owner, requirements) + "\x00" + adapterSessionLockScope(requirements) + "\x00"
}

func adapterSessionLockKey(owner string, requirements Requirements, kind, holderID string) string {
	return adapterSessionLockPrefix(owner, requirements) + kind + "\x00" + holderID
}

func adapterSessionLockActive(lock adapterSessionLock, now time.Time) bool {
	return lock.ExpiresAt.IsZero() || lock.ExpiresAt.After(now)
}

func adapterSessionBusyTx(tx *bolt.Tx, owner string, requirements Requirements, excludeKind, excludeID string, now time.Time) (bool, error) {
	if !adapterSessionLockEligible(requirements) {
		return false, nil
	}
	bucket := tx.Bucket(bucketAdapterSessionLocks)
	prefix := []byte(adapterSessionLockBase(owner, requirements) + "\x00")
	wantedScope := adapterSessionLockScope(requirements)
	cursor := bucket.Cursor()
	for key, value := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, value = cursor.Next() {
		var lock adapterSessionLock
		if err := json.Unmarshal(value, &lock); err != nil {
			return false, err
		}
		if !adapterSessionLockActive(lock, now) {
			if err := cursor.Delete(); err != nil {
				return false, err
			}
			continue
		}
		if wantedScope != "*" && lock.Scope != "*" && lock.Scope != wantedScope {
			continue
		}
		if lock.Kind == excludeKind && lock.HolderID == excludeID {
			continue
		}
		return true, nil
	}
	return false, nil
}

func acquireAdapterSessionLockTx(tx *bolt.Tx, owner string, requirements Requirements, kind, holderID string, expiresAt, now time.Time, required bool) error {
	if !required {
		return nil
	}
	busy, err := adapterSessionBusyTx(tx, owner, requirements, kind, holderID, now)
	if err != nil {
		return err
	}
	if busy {
		return ErrAdapterSessionBusy
	}
	return putJSON(tx.Bucket(bucketAdapterSessionLocks), adapterSessionLockKey(owner, requirements, kind, holderID), adapterSessionLock{
		Kind: kind, HolderID: holderID, Scope: adapterSessionLockScope(requirements), ExpiresAt: expiresAt,
	})
}

func releaseAdapterSessionLockTx(tx *bolt.Tx, owner string, requirements Requirements, kind, holderID string) error {
	if !adapterSessionLockEligible(requirements) {
		return nil
	}
	return tx.Bucket(bucketAdapterSessionLocks).Delete([]byte(adapterSessionLockKey(owner, requirements, kind, holderID)))
}

func promoteAdapterSessionLockTx(tx *bolt.Tx, saved reservation, job Job, now time.Time) error {
	assignmentKey := []byte(adapterSessionLockKey(saved.OwnerSubject, saved.Assignment.Requirements, adapterSessionLockAssignment, saved.Assignment.ID))
	locked := tx.Bucket(bucketAdapterSessionLocks).Get(assignmentKey) != nil
	if !locked && !adapterSessionNeedsLockTx(tx, job.Requirements, job.AssignedNode) {
		return nil
	}
	if err := releaseAdapterSessionLockTx(tx, saved.OwnerSubject, saved.Assignment.Requirements, adapterSessionLockAssignment, saved.Assignment.ID); err != nil {
		return err
	}
	return acquireAdapterSessionLockTx(tx, job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID, time.Time{}, now, true)
}

func garbageCollectAdapterSessionLocks(tx *bolt.Tx, now time.Time) (int, error) {
	removed := 0
	cursor := tx.Bucket(bucketAdapterSessionLocks).Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		var lock adapterSessionLock
		if err := json.Unmarshal(value, &lock); err != nil {
			return removed, err
		}
		if adapterSessionLockActive(lock, now) {
			continue
		}
		if err := cursor.Delete(); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// rebuildAdapterSessionLocks makes the lock table derivable rather than a
// second source of truth. This both repairs upgrades from older databases and
// reconstructs reservations/active executions after a relay process crash.
func rebuildAdapterSessionLocks(tx *bolt.Tx, now time.Time) error {
	locks := tx.Bucket(bucketAdapterSessionLocks)
	for cursor, key, _ := locks.Cursor(), []byte(nil), []byte(nil); ; {
		if key == nil {
			key, _ = cursor.First()
		} else {
			key, _ = cursor.Next()
		}
		if key == nil {
			break
		}
		if err := cursor.Delete(); err != nil {
			return err
		}
	}
	if err := tx.Bucket(bucketAssignments).ForEach(func(_, value []byte) error {
		var saved reservation
		if err := json.Unmarshal(value, &saved); err != nil {
			return err
		}
		if !saved.Assignment.ExpiresAt.After(now) || !adapterSessionNeedsLockTx(tx, saved.Assignment.Requirements, saved.Assignment.NodeID) {
			return nil
		}
		return putJSON(locks, adapterSessionLockKey(saved.OwnerSubject, saved.Assignment.Requirements, adapterSessionLockAssignment, saved.Assignment.ID), adapterSessionLock{
			Kind: adapterSessionLockAssignment, HolderID: saved.Assignment.ID, Scope: adapterSessionLockScope(saved.Assignment.Requirements), ExpiresAt: saved.Assignment.ExpiresAt,
		})
	}); err != nil {
		return err
	}
	return tx.Bucket(bucketJobs).ForEach(func(_, value []byte) error {
		var job Job
		if err := json.Unmarshal(value, &job); err != nil {
			return err
		}
		active := job.Status == JobAssigned || job.Status == JobRunning ||
			(job.Status == JobQueued && job.SealedPayload != nil && job.AssignedNode != "")
		if !active || !adapterSessionNeedsLockTx(tx, job.Requirements, job.AssignedNode) {
			return nil
		}
		return putJSON(locks, adapterSessionLockKey(job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID), adapterSessionLock{
			Kind: adapterSessionLockJob, HolderID: job.ID, Scope: adapterSessionLockScope(job.Requirements),
		})
	})
}

// AdapterSessionBusy is a cheap scheduler hint. The write-transaction checks
// in reservation creation and assignment remain authoritative against races.
func (s *Store) AdapterSessionBusy(owner string, requirements Requirements, excludeJobID string) (bool, error) {
	if !adapterSessionLockEligible(requirements) {
		return false, nil
	}
	var busy bool
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		busy, err = adapterSessionBusyTx(tx, owner, requirements, adapterSessionLockJob, excludeJobID, time.Now().UTC())
		return err
	})
	return busy, err
}

// ReleaseAdapterSessionJobLock is used only when the relay has proof that a
// cancelled/terminalized worker execution ended (matching result or teardown).
func (s *Store) ReleaseAdapterSessionJobLock(jobID string) (bool, error) {
	released := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		var job Job
		if err := getJSON(tx.Bucket(bucketJobs), jobID, &job); err != nil {
			return err
		}
		if !adapterSessionLockEligible(job.Requirements) {
			return nil
		}
		key := []byte(adapterSessionLockKey(job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID))
		if tx.Bucket(bucketAdapterSessionLocks).Get(key) == nil {
			return nil
		}
		if err := tx.Bucket(bucketAdapterSessionLocks).Delete(key); err != nil {
			return err
		}
		released = true
		return nil
	})
	return released, err
}

func adapterSessionJobLockExistsTx(tx *bolt.Tx, job Job) bool {
	return adapterSessionLockEligible(job.Requirements) && tx.Bucket(bucketAdapterSessionLocks).Get([]byte(adapterSessionLockKey(job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID))) != nil
}

func putSessionPlacement(tx *bolt.Tx, key string, placement sessionPlacement) error {
	if placement.NodeID == "" || placement.UpdatedAt.IsZero() {
		return nil
	}
	bucket := tx.Bucket(bucketSessionPlacements)
	var current sessionPlacement
	if getJSON(bucket, key, &current) == nil && !placement.UpdatedAt.After(current.UpdatedAt) {
		return nil
	}
	return putJSON(bucket, key, placement)
}

func (s *Store) SaveJob(job Job) error {
	job.UpdatedAt = time.Now().UTC()
	return s.db.Update(func(tx *bolt.Tx) error {
		var previous Job
		previousExists := getJSON(tx.Bucket(bucketJobs), job.ID, &previous) == nil
		if err := putJSON(tx.Bucket(bucketJobs), job.ID, job); err != nil {
			return err
		}
		if previousExists && !bytes.Equal(jobIndexKey(previous), jobIndexKey(job)) {
			if err := tx.Bucket(bucketJobIndex).Delete(jobIndexKey(previous)); err != nil {
				return err
			}
		}
		if !previousExists || !bytes.Equal(jobIndexKey(previous), jobIndexKey(job)) {
			if err := tx.Bucket(bucketJobIndex).Put(jobIndexKey(job), []byte(job.ID)); err != nil {
				return err
			}
		}
		if previousExists && !bytes.Equal(jobOwnerIndexKey(previous), jobOwnerIndexKey(job)) {
			if err := deleteJobOwnerIndex(tx.Bucket(bucketJobOwnerIndex), previous); err != nil {
				return err
			}
		}
		return putJobOwnerIndex(tx.Bucket(bucketJobOwnerIndex), job)
	})
}

func (s *Store) AssignJob(id, nodeID string, selectedAdapterEndpoint ...int) (Job, error) {
	if len(selectedAdapterEndpoint) > 1 {
		return Job{}, errors.New("only one adapter endpoint may be selected")
	}
	selection := (*adapterAssignment)(nil)
	if len(selectedAdapterEndpoint) == 1 {
		selection = &adapterAssignment{EndpointID: selectedAdapterEndpoint[0]}
	}
	return s.assignJob(id, nodeID, selection, nil, nil)
}

func (s *Store) AssignJobWithDecision(id, nodeID string, decision RoutingDecision) (Job, error) {
	return s.assignJob(id, nodeID, nil, &decision, nil)
}

func (s *Store) AssignJobFencedWithDecision(id, nodeID string, decision RoutingDecision, authority RelayAuthority) (Job, error) {
	if !authority.Valid() {
		return Job{}, errors.New("valid relay authority is required for fenced assignment")
	}
	return s.assignJob(id, nodeID, nil, &decision, &authority)
}

type adapterAssignment struct {
	EndpointID      int
	Principal       string
	SessionRecovery bool
}

func (s *Store) AssignAdapterJob(id, nodeID string, endpointID int, sessionRecovery bool) (Job, error) {
	return s.assignJob(id, nodeID, &adapterAssignment{EndpointID: endpointID, SessionRecovery: sessionRecovery}, nil, nil)
}

func (s *Store) AssignAdapterJobWithDecision(id, nodeID string, endpointID int, sessionRecovery bool, decision RoutingDecision) (Job, error) {
	return s.assignJob(id, nodeID, &adapterAssignment{EndpointID: endpointID, SessionRecovery: sessionRecovery}, &decision, nil)
}

func (s *Store) AssignAdapterJobFencedWithDecision(id, nodeID string, endpointID int, principal string, sessionRecovery bool, decision RoutingDecision, authority RelayAuthority) (Job, error) {
	if !authority.Valid() {
		return Job{}, errors.New("valid relay authority is required for fenced assignment")
	}
	return s.assignJob(id, nodeID, &adapterAssignment{EndpointID: endpointID, Principal: principal, SessionRecovery: sessionRecovery}, &decision, &authority)
}

func (s *Store) assignJob(id, nodeID string, adapter *adapterAssignment, decision *RoutingDecision, authority *RelayAuthority) (Job, error) {
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
		// This check shares the assignment transaction with the queue transition.
		// It is the drain linearization point: even a dispatcher holding a stale
		// pre-drain routing snapshot cannot create a new assignment afterward.
		var node Node
		if err := getJSON(tx.Bucket(bucketNodes), nodeID, &node); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if node.Draining {
			return ErrNodeDraining
		}
		now := time.Now().UTC()
		if decision != nil && decision.RouteKey != "" {
			expectedRouteKey, _, _ := routingHealthKey(decision.Requirements)
			if !validRoutingHealthRouteKey(decision.RouteKey) || decision.RouteKey != expectedRouteKey {
				return errors.New("routing decision route identity is invalid")
			}
		}
		if adapter != nil {
			if !strings.EqualFold(job.Requirements.Provider, "adapter") {
				return errors.New("adapter endpoint selection requires provider adapter")
			}
			if adapter.EndpointID < 0 {
				return errors.New("adapter endpoint selection is invalid")
			}
			if job.SealedPayload != nil && (adapter.EndpointID != job.Requirements.AdapterEndpointID || adapter.Principal != job.Requirements.AdapterPrincipal || adapter.SessionRecovery != job.Requirements.AdapterSessionRecovery) {
				return errors.New("sealed adapter assignment cannot change authenticated endpoint requirements")
			}
			if adapter.EndpointID > 0 && job.Requirements.AdapterEndpointID > 0 && job.Requirements.AdapterEndpointID != adapter.EndpointID {
				return errors.New("adapter job is bound to another endpoint")
			}
			job.Requirements.AdapterEndpointID = adapter.EndpointID
			job.Requirements.AdapterPrincipal = adapter.Principal
			job.Requirements.AdapterSessionRecovery = adapter.SessionRecovery
		}
		admittedRouteKey := ""
		if decision != nil {
			admittedRouteKey = decision.RouteKey
		}
		probeClaimed, err := claimRoutingRecoveryProbeTx(tx, &node, job.Requirements, job.OwnerSubject, job.ID, admittedRouteKey, now)
		if err != nil {
			return err
		}
		if probeClaimed {
			if err := putJSON(tx.Bucket(bucketNodes), node.ID, node); err != nil {
				return err
			}
		}
		if err := acquireAdapterSessionLockTx(tx, job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID, time.Time{}, now, adapterSessionNeedsLockTx(tx, job.Requirements, nodeID)); err != nil {
			return err
		}
		job.Status = JobAssigned
		job.AssignedNode = nodeID
		job.Attempt++
		if job.Attempt <= 0 {
			return errors.New("assignment generation overflow")
		}
		if authority != nil {
			job.AssignmentFence = &AssignmentFence{ClusterID: authority.ClusterID, RelayEpoch: authority.Epoch, Generation: uint64(job.Attempt)}
		} else {
			job.AssignmentFence = nil
		}
		job.Progress = nil
		job.AssignedAt = now
		job.UpdatedAt = job.AssignedAt
		if decision != nil {
			if decision.SelectedNodeID != "" && decision.SelectedNodeID != nodeID {
				return errors.New("routing decision selects another node")
			}
			selectedCandidateFound := false
			for _, candidate := range decision.Candidates {
				if candidate.NodeID == nodeID && candidate.Eligible {
					selectedCandidateFound = true
					break
				}
			}
			if !selectedCandidateFound {
				return errors.New("routing decision does not contain the selected eligible node")
			}
			decisionCopy := *decision
			decisionCopy.Preview = false
			decisionCopy.JobID = job.ID
			decisionCopy.SelectedNodeID = nodeID
			decisionCopy.Requirements = job.Requirements
			if decisionCopy.ID == "" {
				decisionCopy.ID = randomID("route")
			}
			if decisionCopy.CreatedAt.IsZero() {
				decisionCopy.CreatedAt = job.AssignedAt
			}
			job.RoutingDecision = &decisionCopy
		}
		if err := putJSON(tx.Bucket(bucketJobs), id, job); err != nil {
			return err
		}
		if err := deleteQueueEntry(tx, id); err != nil {
			return err
		}
		if job.RoutingDecision != nil {
			if err := appendAuthoritativeJobEventTx(tx, s, job, "route.selected"); err != nil {
				return err
			}
		}
		return appendAuthoritativeJobEventTx(tx, s, job, "worker.assigned")
	})
	return job, err
}

func (s *Store) MarkRunning(id, nodeID string, attempt int) (Job, error) {
	return s.markRunning(id, nodeID, attempt, nil)
}

func (s *Store) MarkRunningFenced(id, nodeID string, attempt int, fence *AssignmentFence) (Job, error) {
	return s.markRunning(id, nodeID, attempt, fence)
}

func (s *Store) markRunning(id, nodeID string, attempt int, fence *AssignmentFence) (Job, error) {
	var job Job
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketJobs), id, &job); err != nil {
			return err
		}
		if job.Status != JobAssigned || job.AssignedNode != nodeID || attempt <= 0 || job.Attempt != attempt {
			return errors.New("job is not assigned to this node and attempt")
		}
		if err := validateAssignmentFence(job, fence); err != nil {
			return err
		}
		job.Status = JobRunning
		job.StartedAt = time.Now().UTC()
		job.UpdatedAt = job.StartedAt
		if err := putJSON(tx.Bucket(bucketJobs), id, job); err != nil {
			return err
		}
		if err := appendAuthoritativeJobEventTx(tx, s, job, "execution.started"); err != nil {
			return err
		}
		return appendPipelineStepEventTx(tx, s, job, "pipeline.step.started")
	})
	return job, err
}

func (s *Store) UpdateJobProgress(id, nodeID string, attempt int, progress JobProgress) (Job, error) {
	return s.updateJobProgress(id, nodeID, attempt, nil, progress)
}

func (s *Store) UpdateJobProgressFenced(id, nodeID string, attempt int, fence *AssignmentFence, progress JobProgress) (Job, error) {
	return s.updateJobProgress(id, nodeID, attempt, fence, progress)
}

func (s *Store) updateJobProgress(id, nodeID string, attempt int, fence *AssignmentFence, progress JobProgress) (Job, error) {
	var job Job
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketJobs), id, &job); err != nil {
			return err
		}
		if (job.Status != JobAssigned && job.Status != JobRunning) || job.AssignedNode != nodeID || attempt <= 0 || job.Attempt != attempt {
			return errors.New("job is not running on this node and attempt")
		}
		if err := validateAssignmentFence(job, fence); err != nil {
			return err
		}
		if job.SealedPayload != nil {
			return errors.New("plaintext progress is disabled for encrypted jobs")
		}
		if progress.Sequence == 0 || len(progress.Text) > 1<<20 || len(progress.Detail) > 500 || progress.Percent < 0 || progress.Percent > 100 {
			return errors.New("invalid job progress")
		}
		if job.Progress != nil && progress.Sequence <= job.Progress.Sequence {
			return nil
		}
		progress.Phase = cleanLabel(progress.Phase, 30)
		progress.Detail = cleanLabel(progress.Detail, 500)
		progress.UpdatedAt = time.Now().UTC()
		copy := progress
		job.Progress = &copy
		job.UpdatedAt = progress.UpdatedAt
		if err := putJSON(tx.Bucket(bucketJobs), id, job); err != nil {
			return err
		}
		return appendAdvisoryJobProgressEventTx(tx, s, job, progress)
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
		queued := job.Status == JobQueued
		job.Status = JobCancelled
		job.FinishedAt = time.Now().UTC()
		job.UpdatedAt = job.FinishedAt
		if queued {
			// No provider action can have begun for a queued job. Retain its
			// identity, ownership, policy and routing evidence, but discard the
			// attacker-controlled bulk body. Idempotency continues to resolve to
			// this terminal tombstone without retaining MiBs per cancel cycle.
			job.Payload = nil
			job.SealedPayload = nil
			job.Result = nil
			job.SealedResult = nil
			job.Progress = nil
		}
		if err := deleteQueueEntry(tx, id); err != nil {
			return err
		}
		if err := putJSON(tx.Bucket(bucketJobs), id, job); err != nil {
			return err
		}
		if err := appendAuthoritativeJobEventTx(tx, s, job, "job.cancelled"); err != nil {
			return err
		}
		return appendPipelineStepEventTx(tx, s, job, "pipeline.step.cancelled")
	})
	return job, err
}

func (s *Store) CompleteJob(id, nodeID string, attempt int, result json.RawMessage, sealed *SealedEnvelope, usage Usage, jobError string, execution ...*ExecutionMetadata) (Job, error) {
	return s.completeJobWithFailure(id, nodeID, attempt, nil, result, sealed, usage, jobError, "", execution...)
}

// CompleteJobWithFailure persists a bounded stable failure class while
// retaining the existing diagnostic text. Unknown worker-supplied classes are
// never trusted as new API identifiers; they collapse to a reviewed fallback.
func (s *Store) CompleteJobWithFailure(id, nodeID string, attempt int, result json.RawMessage, sealed *SealedEnvelope, usage Usage, jobError, failureCode string, execution ...*ExecutionMetadata) (Job, error) {
	return s.completeJobWithFailure(id, nodeID, attempt, nil, result, sealed, usage, jobError, failureCode, execution...)
}

func (s *Store) CompleteJobWithFailureFenced(id, nodeID string, attempt int, fence *AssignmentFence, result json.RawMessage, sealed *SealedEnvelope, usage Usage, jobError, failureCode string, execution ...*ExecutionMetadata) (Job, error) {
	return s.completeJobWithFailure(id, nodeID, attempt, fence, result, sealed, usage, jobError, failureCode, execution...)
}

func (s *Store) completeJobWithFailure(id, nodeID string, attempt int, fence *AssignmentFence, result json.RawMessage, sealed *SealedEnvelope, usage Usage, jobError, failureCode string, execution ...*ExecutionMetadata) (Job, error) {
	var job Job
	if len(execution) > 1 {
		return Job{}, errors.New("only one execution metadata record may be reported")
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := getJSON(tx.Bucket(bucketJobs), id, &job); err != nil {
			return err
		}
		if job.Status != JobAssigned && job.Status != JobRunning {
			return errors.New("job is not assigned")
		}
		if nodeID == "" || job.AssignedNode != nodeID {
			return errors.New("job is not assigned to this node")
		}
		if attempt <= 0 || job.Attempt != attempt {
			return errors.New("job result belongs to a stale assignment attempt")
		}
		if err := validateAssignmentFence(job, fence); err != nil {
			return err
		}
		// Treat the worker as a protocol peer, not as the E2EE trust boundary.
		// Even an old or modified worker must not persist provider-controlled
		// plaintext diagnostics for a sealed job at the relay.
		reportedFailureCode := failureCode
		if job.SealedPayload != nil && strings.TrimSpace(jobError) != "" {
			failureCode = normalizedWorkerFailureCode(failureCode, jobError)
			jobError = sealedJobFailureMessage(failureCode)
		}
		failureCode = normalizedWorkerFailureCode(failureCode, jobError)
		if preExecutionRetryAllowed(job, result, sealed, usage, jobError, reportedFailureCode, failureCode, execution) {
			// A proven pre-execution refusal may reroute this job and clears its
			// AssignedNode below. Release any recovery probe on the old node first;
			// the refusal is not evidence that the unhealthy route recovered.
			if _, err := resolveRoutingRecoveryProbeTx(tx, job.AssignedNode, job.ID, "", time.Now().UTC()); err != nil {
				return err
			}
			job.Status = JobQueued
			job.Error = ""
			job.FailureCode = ""
			job.Result = nil
			job.SealedResult = nil
			job.Usage = Usage{}
			job.Progress = nil
			job.RoutingDecision = nil
			job.AssignmentFence = nil
			job.ExecutedAdapterEndpointID = 0
			job.EphemeralAdapterEndpoint = false
			job.AssignedAt = time.Time{}
			job.StartedAt = time.Time{}
			job.FinishedAt = time.Time{}
			job.UpdatedAt = time.Now().UTC()
			// A sealed payload is authenticated to one reserved worker. Plaintext
			// jobs may be rerouted after a proven pre-execution refusal.
			if job.SealedPayload == nil {
				job.AssignedNode = ""
			}
			if err := putJSON(tx.Bucket(bucketJobs), id, job); err != nil {
				return err
			}
			if err := putQueueEntry(tx, job); err != nil {
				return err
			}
			if err := appendAuthoritativeJobEventTx(tx, s, job, "job.retrying"); err != nil {
				return err
			}
			return appendAuthoritativeJobEventTx(tx, s, job, "job.queued")
		}
		completionError := validateWorkerResult(job, result, sealed, jobError)
		var executedAdapterEndpointID int
		if len(execution) == 1 && execution[0] != nil && execution[0].AdapterEndpointID != 0 {
			executedAdapterEndpointID = execution[0].AdapterEndpointID
			provider := strings.TrimSpace(job.Requirements.Provider)
			if executedAdapterEndpointID < 1 || provider != "" && !strings.EqualFold(provider, "adapter") {
				completionError = errors.New("adapter execution metadata does not match this job")
			}
		}
		if completionError == nil {
			job.Result = result
			job.SealedResult = sealed
			// The authenticated request is authoritative. A worker may confirm
			// that a endpoint was ephemeral, but it cannot downgrade an explicitly
			// per-job adapter chat into durable session placement.
			job.EphemeralAdapterEndpoint = job.Requirements.AdapterEphemeralSession
			if executedAdapterEndpointID > 0 {
				job.ExecutedAdapterEndpointID = executedAdapterEndpointID
				job.EphemeralAdapterEndpoint = job.EphemeralAdapterEndpoint || execution[0].EphemeralAdapterEndpoint
			}
		} else {
			// A malformed completion from the assigned worker is terminal. Retrying
			// an execution whose side effects are unknown could submit a adapter
			// prompt twice; the producer must explicitly create a new job.
			job.Result = nil
			job.SealedResult = nil
			jobError = "worker result rejected: " + completionError.Error()
			failureCode = FailureWorkerResultRejected
		}
		if !job.AssignedAt.IsZero() && job.AssignedAt.After(job.CreatedAt) {
			usage.QueueMS = nonNegativeDurationMilliseconds(job.AssignedAt.Sub(job.CreatedAt))
		}
		job.Usage = usage
		job.Error = cleanLabel(jobError, 500)
		job.FailureCode = normalizedWorkerFailureCode(failureCode, jobError)
		job.FinishedAt = time.Now().UTC()
		job.UpdatedAt = job.FinishedAt
		if jobError == "" {
			job.FailureCode = ""
			if job.Progress != nil {
				job.Progress.Sequence = saturatingUint64Add(job.Progress.Sequence, 1)
				job.Progress.Phase = "final"
				job.Progress.Busy = false
				job.Progress.UpdatedAt = job.FinishedAt
			}
			job.Status = JobCompleted
		} else {
			job.Status = JobFailed
		}
		if err := putJSON(tx.Bucket(bucketJobs), id, job); err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(job.Requirements.Provider), "adapter") {
			if job.ExecutedAdapterEndpointID > 0 && !job.EphemeralAdapterEndpoint {
				if err := putSessionPlacement(tx, sessionPlacementKey(job.OwnerSubject, job.Requirements), sessionPlacement{
					NodeID: job.AssignedNode, AdapterEndpointID: job.ExecutedAdapterEndpointID, AdapterPrincipal: job.Requirements.AdapterPrincipal, UpdatedAt: job.UpdatedAt,
				}); err != nil {
					return err
				}
			}
		} else if err := putSessionPlacement(tx, sessionPlacementKey(job.OwnerSubject, job.Requirements), sessionPlacement{
			NodeID: job.AssignedNode, AdapterEndpointID: job.ExecutedAdapterEndpointID, UpdatedAt: job.UpdatedAt,
		}); err != nil {
			return err
		}
		if err := releaseAdapterSessionLockTx(tx, job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID); err != nil {
			return err
		}
		if job.Status == JobCompleted || job.Status == JobFailed {
			var node Node
			if getJSON(tx.Bucket(bucketNodes), job.AssignedNode, &node) == nil {
				node.JobsTotal = saturatingUint64Add(node.JobsTotal, 1)
				if job.Status == JobFailed {
					node.JobsFailed = saturatingUint64Add(node.JobsFailed, 1)
				}
				node.ComputeMS = saturatingUint64Add(node.ComputeMS, usage.ComputeMS)
				node.CostUSD = saturatingCostAdd(node.CostUSD, usage.EstimatedCostUSD)
				node.CostKnownJobs = saturatingUint64Add(node.CostKnownJobs, usage.CostKnownJobs)
				node.CostUnknownJobs = saturatingUint64Add(node.CostUnknownJobs, usage.CostUnknownJobs)
				recordRoutingOutcomeForOwnerRoute(&node, job.Requirements, job.OwnerSubject, jobRoutingHealthRouteKey(job), job.ID, job.Status == JobCompleted, job.FailureCode, job.FinishedAt)
				if job.Status == JobCompleted {
					recordRoutingPerformance(&node, job.Requirements, jobRoutingHealthRouteKey(job), routingObservedComputeMS(job), job.FinishedAt, jobRoutingPerformanceContext(job))
				}
				if err := putJSON(tx.Bucket(bucketNodes), node.ID, node); err != nil {
					return err
				}
			}
		}
		eventType := "job.completed"
		if job.Status == JobFailed {
			eventType = "job.failed"
		}
		if err := appendAuthoritativeJobEventTx(tx, s, job, eventType); err != nil {
			return err
		}
		stepType := "pipeline.step.completed"
		if job.Status == JobFailed {
			stepType = "pipeline.step.failed"
		}
		return appendPipelineStepEventTx(tx, s, job, stepType)
	})
	return job, err
}

func jobRoutingHealthRouteKey(job Job) string {
	if job.RoutingDecision != nil && validRoutingHealthRouteKey(job.RoutingDecision.RouteKey) {
		return job.RoutingDecision.RouteKey
	}
	return ""
}

func jobRoutingPerformanceContext(job Job) string {
	if job.RoutingDecision == nil || job.RoutingDecision.SelectedNodeID == "" || job.RoutingDecision.SelectedNodeID != job.AssignedNode {
		return ""
	}
	for _, candidate := range job.RoutingDecision.Candidates {
		if candidate.NodeID == job.AssignedNode && candidate.Eligible && validRoutingPerformanceContext(candidate.PerformanceContext) {
			return candidate.PerformanceContext
		}
	}
	return ""
}

func validateAssignmentFence(job Job, reported *AssignmentFence) error {
	if job.AssignmentFence == nil {
		if reported == nil {
			return nil
		}
		return ErrAssignmentFenceMismatch
	}
	// Persisted state is not trusted merely because ordinary writers only create
	// positive attempts. Reject a corrupt negative/zero attempt before converting
	// it to uint64; otherwise -1 can alias math.MaxUint64 and match a forged fence.
	if job.Attempt <= 0 || reported == nil || !job.AssignmentFence.Valid() || !reported.Valid() || !job.AssignmentFence.Equal(*reported) || reported.Generation != uint64(job.Attempt) {
		return ErrAssignmentFenceMismatch
	}
	return nil
}

// preExecutionRetryAllowed recognizes only worker refusals emitted before the
// worker starts a provider execution. No timeout, disconnect, provider error,
// adapter failure, malformed completion, or other ambiguous state is eligible.
func preExecutionRetryAllowed(job Job, result json.RawMessage, sealed *SealedEnvelope, usage Usage, jobError, reportedFailureCode, failureCode string, execution []*ExecutionMetadata) bool {
	if job.Status != JobAssigned || job.Attempt <= 0 || job.Attempt >= job.MaxAttempts || strings.TrimSpace(jobError) == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(job.Requirements.Provider), "adapter") {
		return false
	}
	if reportedFailureCode != failureCode || failureCode != FailureWorkerCapacity && failureCode != FailureWorkerStopping {
		return false
	}
	if len(result) != 0 || sealed != nil || !usageHasNoExecutionEvidence(usage) {
		return false
	}
	return len(execution) == 0 || execution[0] == nil || *execution[0] == (ExecutionMetadata{})
}

func usageHasNoExecutionEvidence(usage Usage) bool {
	return usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 && usage.ComputeMS == 0 && usage.QueueMS == 0 &&
		usage.ReservedCostUSD == 0 && usage.EstimatedCostUSD == 0 && usage.EquivalentCostUSD == 0 && usage.SavedCostUSD == 0 &&
		usage.PeakVRAMBytes == 0 && usage.PeakRAMBytes == 0 && usage.PeakGPUUtilization == 0 && usage.ResourceScope == ""
}

func validateWorkerResult(job Job, result json.RawMessage, sealed *SealedEnvelope, jobError string) error {
	if strings.TrimSpace(jobError) != "" {
		if len(result) != 0 || sealed != nil {
			return errors.New("failed completion must not include a result")
		}
		return nil
	}
	if job.SealedPayload == nil {
		if sealed != nil {
			return errors.New("plaintext job returned an encrypted result")
		}
		if len(result) == 0 || int64(len(result)) > MaximumJobResultBytes || !utf8.Valid(result) || !json.Valid(result) {
			return fmt.Errorf("plaintext result must be valid JSON up to %d bytes", MaximumJobResultBytes)
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(result, &object) != nil || object == nil {
			return errors.New("plaintext result must be a JSON object")
		}
		return nil
	}
	if len(result) != 0 || sealed == nil {
		return errors.New("encrypted job must return only an encrypted result")
	}
	if sealed.Algorithm != sealedAlgorithm || sealed.EphemeralPublic != "" {
		return errors.New("encrypted result envelope is invalid")
	}
	nonce, err := decode(sealed.Nonce)
	if err != nil || len(nonce) != 12 {
		return errors.New("encrypted result nonce is invalid")
	}
	ciphertext, err := decode(sealed.Ciphertext)
	if err != nil || len(ciphertext) < 16 || int64(len(ciphertext)) > MaximumJobResultBytes+16 {
		return fmt.Errorf("encrypted result ciphertext must represent at most %d bytes", MaximumJobResultBytes)
	}
	return nil
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
			if json.Unmarshal(value, &job) != nil || job.AssignedNode != nodeID {
				return nil
			}
			if job.Status == JobCancelled {
				now := time.Now().UTC()
				activeProbe, err := routingRecoveryProbeExistsTx(tx, job.AssignedNode, job.ID)
				if err != nil {
					return err
				}
				if activeProbe {
					if err := recordNodeRoutingOutcomeTx(tx, job.AssignedNode, job.Requirements, "", jobRoutingHealthRouteKey(job), job.ID, false, FailureExecutionStateAmbiguous, now); err != nil {
						return err
					}
					if _, err := resolveRoutingRecoveryProbeTx(tx, job.AssignedNode, job.ID, FailureExecutionStateAmbiguous, now); err != nil {
						return err
					}
				}
				return releaseAdapterSessionLockTx(tx, job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID)
			}
			if job.Status != JobAssigned && job.Status != JobRunning {
				return nil
			}
			job.Error = cleanLabel(reason+"; execution state is ambiguous; explicit resubmission required", 500)
			job.FailureCode = FailureExecutionStateAmbiguous
			job.UpdatedAt = time.Now().UTC()
			job.Status = JobFailed
			job.FinishedAt = job.UpdatedAt
			if err := deleteQueueEntry(tx, job.ID); err != nil {
				return err
			}
			if err := releaseAdapterSessionLockTx(tx, job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID); err != nil {
				return err
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
			if err := appendAuthoritativeJobEventTx(tx, s, change.job, "job.ambiguous"); err != nil {
				return err
			}
			if err := appendPipelineStepEventTx(tx, s, change.job, "pipeline.step.ambiguous"); err != nil {
				return err
			}
			if err := recordNodeRoutingOutcomeTx(tx, change.job.AssignedNode, change.job.Requirements, "", jobRoutingHealthRouteKey(change.job), change.job.ID, false, change.job.FailureCode, change.job.FinishedAt); err != nil {
				return err
			}
			if _, err := resolveRoutingRecoveryProbeTx(tx, change.job.AssignedNode, change.job.ID, change.job.FailureCode, change.job.FinishedAt); err != nil {
				return err
			}
		}
		return nil
	})
	return updated, err
}

// RecoverRelayRestart closes every execution whose worker connection belonged
// to the previous relay process and marks persisted nodes offline. An assigned
// or running provider action may already have happened, so these jobs must
// fail closed instead of becoming eligible for automatic re-execution.
func (s *Store) RecoverRelayRestart(reason string) ([]Job, error) {
	now := time.Now().UTC()
	reason = cleanLabel(reason+"; execution state is ambiguous; explicit resubmission required", 500)
	updated := []Job{}
	err := s.db.Update(func(tx *bolt.Tx) error {
		nodes := tx.Bucket(bucketNodes)
		if err := nodes.ForEach(func(key, value []byte) error {
			var node Node
			if json.Unmarshal(value, &node) != nil {
				return nil
			}
			node.Connected = false
			node.State = "offline"
			node.Capabilities.Running = 0
			encoded, err := json.Marshal(node)
			if err != nil {
				return err
			}
			return nodes.Put(key, encoded)
		}); err != nil {
			return err
		}

		jobs := tx.Bucket(bucketJobs)
		type pending struct {
			key []byte
			job Job
		}
		changes := []pending{}
		if err := jobs.ForEach(func(key, value []byte) error {
			var job Job
			if json.Unmarshal(value, &job) != nil {
				return nil
			}
			if job.Status == JobCancelled {
				activeProbe, err := routingRecoveryProbeExistsTx(tx, job.AssignedNode, job.ID)
				if err != nil {
					return err
				}
				if activeProbe {
					if err := recordNodeRoutingOutcomeTx(tx, job.AssignedNode, job.Requirements, "", jobRoutingHealthRouteKey(job), job.ID, false, FailureExecutionStateAmbiguous, now); err != nil {
						return err
					}
					if _, err := resolveRoutingRecoveryProbeTx(tx, job.AssignedNode, job.ID, FailureExecutionStateAmbiguous, now); err != nil {
						return err
					}
				}
				return releaseAdapterSessionLockTx(tx, job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID)
			}
			if job.Status != JobAssigned && job.Status != JobRunning {
				return nil
			}
			job.Error = reason
			job.FailureCode = FailureExecutionStateAmbiguous
			job.Status = JobFailed
			job.UpdatedAt = now
			job.FinishedAt = now
			job.Progress = nil
			if err := deleteQueueEntry(tx, job.ID); err != nil {
				return err
			}
			if err := releaseAdapterSessionLockTx(tx, job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID); err != nil {
				return err
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
			if err := jobs.Put(change.key, encoded); err != nil {
				return err
			}
			if err := appendAuthoritativeJobEventTx(tx, s, change.job, "job.ambiguous"); err != nil {
				return err
			}
			if err := appendPipelineStepEventTx(tx, s, change.job, "pipeline.step.ambiguous"); err != nil {
				return err
			}
			if err := recordNodeRoutingOutcomeTx(tx, change.job.AssignedNode, change.job.Requirements, "", jobRoutingHealthRouteKey(change.job), change.job.ID, false, change.job.FailureCode, change.job.FinishedAt); err != nil {
				return err
			}
			if _, err := resolveRoutingRecoveryProbeTx(tx, change.job.AssignedNode, change.job.ID, change.job.FailureCode, change.job.FinishedAt); err != nil {
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
			key       []byte
			job       Job
			eventType string
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
			if staleExecution {
				job.Status, job.Error, job.FinishedAt = JobFailed, "worker execution timed out; execution state is ambiguous; explicit resubmission required", now
				job.FailureCode = FailureExecutionTimeoutAmbiguous
			} else {
				job.Status, job.Error, job.FinishedAt = JobFailed, "encrypted worker reservation expired; explicit resubmission required", now
				job.FailureCode = FailureEncryptedReservationExpired
			}
			if err := deleteQueueEntry(tx, job.ID); err != nil {
				return err
			}
			if staleBound {
				if err := releaseAdapterSessionLockTx(tx, job.OwnerSubject, job.Requirements, adapterSessionLockJob, job.ID); err != nil {
					return err
				}
			}
			eventType := "job.failed"
			if staleExecution {
				eventType = "job.ambiguous"
			}
			changes = append(changes, change{key: append([]byte(nil), key...), job: job, eventType: eventType})
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
			if err := appendAuthoritativeJobEventTx(tx, s, item.job, item.eventType); err != nil {
				return err
			}
			stepType := "pipeline.step.failed"
			if item.eventType == "job.ambiguous" {
				stepType = "pipeline.step.ambiguous"
			}
			if err := appendPipelineStepEventTx(tx, s, item.job, stepType); err != nil {
				return err
			}
			if item.eventType == "job.ambiguous" {
				if err := recordNodeRoutingOutcomeTx(tx, item.job.AssignedNode, item.job.Requirements, item.job.OwnerSubject, jobRoutingHealthRouteKey(item.job), item.job.ID, false, item.job.FailureCode, item.job.FinishedAt); err != nil {
					return err
				}
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
		jobs := tx.Bucket(bucketJobs)
		cursor := tx.Bucket(bucketQueue).Cursor()
		for key, value := cursor.First(); key != nil && len(queued) < limit; key, value = cursor.Next() {
			entry, err := queueEntryFromValue(jobs, value)
			if err != nil {
				return err
			}
			var job Job
			if err := getJSON(jobs, entry.JobID, &job); err != nil {
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

// QueuedJobsFair preserves priority ordering while round-robining producers
// within each priority tier. One producer therefore cannot hide another's
// equally urgent work beyond the dispatch scan window.
func (s *Store) QueuedJobsFair(limit int) ([]Job, error) {
	return s.QueuedJobsFairAfter(limit, nil)
}

// QueuedJobsFairAfter starts each same-priority owner rotation immediately
// after the owner that most recently received a slot. The relay updates that
// cursor only after a successful dispatch, preventing a deep queue from
// winning every repeated one-slot scan.
func (s *Store) QueuedJobsFairAfter(limit int, afterOwner map[int]string) ([]Job, error) {
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	selectionLimit := boundedQueueScanSize(limit, -1)
	if selectionLimit > 5000 {
		selectionLimit = 5000
	}
	jobs, _, _, err := s.QueuedJobsFairPage(selectionLimit, 0, afterOwner)
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, err
}

// QueuedJobsFairPage returns a bounded rotating scan. Its working memory is
// proportional to the requested page, never to the complete durable queue.
// The integer offset API is retained for callers/tests; the relay hot path uses
// QueuedJobsFairWindow and an opaque Bolt key so it does not re-walk a prefix.
func (s *Store) QueuedJobsFairPage(limit, offset int, afterOwner map[int]string) ([]Job, int, int, error) {
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	var result []Job
	var total, scanned int
	err := s.db.View(func(tx *bolt.Tx) error {
		queue := tx.Bucket(bucketQueue)
		var err error
		total, err = queueCounter(tx.Bucket(bucketQueueCounts), queueTotalCounterKey())
		if err != nil {
			return err
		}
		if total == 0 {
			return nil
		}
		normalizedOffset := offset % total
		if normalizedOffset < 0 {
			normalizedOffset += total
		}
		cursor := queue.Cursor()
		key, value := cursor.First()
		for skipped := 0; skipped < normalizedOffset && key != nil; skipped++ {
			key, value = cursor.Next()
		}
		window, count, err := collectFairQueueWindow(tx, cursor, key, value, min(limit, total), nil)
		if err != nil {
			return err
		}
		scanned = count
		projections := make([]Job, 0, len(window))
		for _, item := range window {
			projections = append(projections, item.job)
		}
		result = fairQueueWindow(projections, limit, afterOwner)
		return nil
	})
	if err != nil {
		return nil, 0, 0, err
	}
	if total == 0 {
		return nil, 0, 0, nil
	}
	offset %= total
	if offset < 0 {
		offset += total
	}
	return result, total, (offset + scanned) % total, nil
}

// QueuedJobsFairWindow is the relay dispatch hot path. afterKey is the last
// queue key inspected by the previous call. The scan wraps once and stops at a
// strict bounded budget, so a million-job configured queue cannot create a
// million-entry allocation or an ever-growing prefix walk.
func (s *Store) QueuedJobsFairWindow(limit int, afterKey []byte, afterOwner map[int]string) ([]Job, []byte, error) {
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	var result []Job
	var next []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		queue := tx.Bucket(bucketQueue)
		cursor := queue.Cursor()
		key, value := queueCursorAfter(cursor, afterKey)
		// The resume key must correspond to the complete candidate set returned
		// to dispatch. Scanning a larger hidden suffix would skip candidates that
		// did not fit in this page.
		window, _, err := collectFairQueueWindow(tx, cursor, key, value, limit, afterKey)
		if err != nil {
			return err
		}
		if len(window) > 0 {
			next = append([]byte(nil), window[len(window)-1].key...)
		}
		projections := make([]Job, 0, len(window))
		for _, item := range window {
			projections = append(projections, item.job)
		}
		result = fairQueueWindow(projections, limit, afterOwner)
		return nil
	})
	return result, next, err
}

type fairQueueItem struct {
	key []byte
	job Job
}

func boundedQueueScanSize(limit, total int) int {
	scan := limit * 4
	if scan < 256 {
		scan = 256
	}
	if scan > 20_000 {
		scan = 20_000
	}
	if total >= 0 && scan > total {
		scan = total
	}
	return scan
}

func queueCursorAfter(cursor *bolt.Cursor, afterKey []byte) ([]byte, []byte) {
	if len(afterKey) == 0 {
		return cursor.First()
	}
	key, value := cursor.Seek(afterKey)
	if key != nil && bytes.Equal(key, afterKey) {
		key, value = cursor.Next()
	}
	if key == nil {
		return cursor.First()
	}
	return key, value
}

func collectFairQueueWindow(tx *bolt.Tx, cursor *bolt.Cursor, key, value []byte, scanLimit int, stopAfter []byte) ([]fairQueueItem, int, error) {
	if key == nil || scanLimit <= 0 {
		return nil, 0, nil
	}
	jobs := tx.Bucket(bucketJobs)
	start := append([]byte(nil), key...)
	window := make([]fairQueueItem, 0, scanLimit)
	wrapped := false
	for len(window) < scanLimit && key != nil {
		entry, err := queueEntryFromValue(jobs, value)
		if err != nil {
			return nil, len(window), err
		}
		projection := Job{ID: entry.JobID, OwnerSubject: entry.OwnerSubject, Priority: entry.Priority, Requirements: entry.Requirements, PolicyDecision: entry.PolicyDecision, AssignedNode: entry.AssignedNode}
		if entry.Sealed {
			projection.SealedPayload = &SealedEnvelope{}
		}
		window = append(window, fairQueueItem{key: append([]byte(nil), key...), job: projection})
		key, value = cursor.Next()
		if key == nil && !wrapped {
			key, value = cursor.First()
			wrapped = true
		}
		if key != nil && bytes.Equal(key, start) {
			break
		}
		if wrapped && len(stopAfter) > 0 && key != nil && bytes.Compare(key, stopAfter) > 0 {
			break
		}
	}
	return window, len(window), nil
}

func fairQueueWindow(window []Job, limit int, afterOwner map[int]string) []Job {
	if len(window) == 0 || limit <= 0 {
		return nil
	}
	priorities := make([]int, 0)
	byPriority := make(map[int][]Job)
	for _, job := range window {
		if _, exists := byPriority[job.Priority]; !exists {
			priorities = append(priorities, job.Priority)
		}
		byPriority[job.Priority] = append(byPriority[job.Priority], job)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(priorities)))
	result := make([]Job, 0, min(limit, len(window)))
	for _, priority := range priorities {
		appendFairPriorityTier(&result, byPriority[priority], limit, afterOwner[priority])
		if len(result) == limit {
			break
		}
	}
	return result
}

func appendFairPriorityTier(target *[]Job, tier []Job, limit int, afterOwner string) {
	owners := make([]string, 0)
	byOwner := make(map[string][]Job)
	for _, job := range tier {
		owner := job.OwnerSubject
		if _, exists := byOwner[owner]; !exists {
			owners = append(owners, owner)
		}
		byOwner[owner] = append(byOwner[owner], job)
	}
	if afterOwner != "" && len(owners) > 1 {
		for index, owner := range owners {
			if owner == afterOwner {
				next := index + 1
				owners = append(append([]string{}, owners[next:]...), owners[:next]...)
				break
			}
		}
	}
	for remaining := len(tier); remaining > 0 && len(*target) < limit; {
		for _, owner := range owners {
			jobs := byOwner[owner]
			if len(jobs) == 0 {
				continue
			}
			*target = append(*target, jobs[0])
			byOwner[owner] = jobs[1:]
			remaining--
			if len(*target) >= limit {
				return
			}
		}
	}
}

func (s *Store) ListJobs(limit int, status string) ([]Job, error) {
	return s.ListJobsForOwner(limit, status, "")
}

// ListJobsForOwner uses a durable owner+createdAt index for producer-scoped
// history. Old databases build the index once, atomically, during OpenStore;
// this hot path never decodes another producer's records.
func (s *Store) ListJobsForOwner(limit int, status, owner string) ([]Job, error) {
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	owner = cleanLabel(owner, 120)
	jobs := []Job{}
	err := s.db.View(func(tx *bolt.Tx) error {
		index := tx.Bucket(bucketJobIndex)
		var prefix []byte
		if owner != "" {
			index = tx.Bucket(bucketJobOwnerIndex)
			prefix = jobOwnerIndexPrefix(owner)
		}
		cursor := index.Cursor()
		key, id := cursor.Last()
		if len(prefix) > 0 {
			upper := prefixUpperBound(prefix)
			if upper != nil {
				key, id = cursor.Seek(upper)
				if key == nil {
					key, id = cursor.Last()
				} else {
					key, id = cursor.Prev()
				}
			}
		}
		for ; key != nil && len(jobs) < limit; key, id = cursor.Prev() {
			if len(prefix) > 0 && !bytes.HasPrefix(key, prefix) {
				break
			}
			var job Job
			if err := getJSON(tx.Bucket(bucketJobs), string(id), &job); err != nil {
				return err
			}
			if owner != "" && (job.OwnerSubject != owner || !bytes.Equal(key, jobOwnerIndexKey(job))) {
				return errors.New("job owner index does not match its authoritative record")
			}
			if (owner == "" || job.OwnerSubject == owner) && (status == "" || job.Status == status) {
				jobs = append(jobs, job)
			}
		}
		return nil
	})
	return jobs, err
}

// ListJobSummariesForOwner decodes only bounded lifecycle/routing metadata.
// Request and result bodies are skipped by encoding/json instead of first
// being materialized and discarded by the HTTP layer.
func (s *Store) ListJobSummariesForOwner(limit int, status, owner string) ([]Job, error) {
	if limit <= 0 || limit > maximumJobHistoryPage {
		limit = 100
	}
	owner = cleanLabel(owner, 120)
	jobs := []Job{}
	err := s.db.View(func(tx *bolt.Tx) error {
		index := tx.Bucket(bucketJobIndex)
		var prefix []byte
		if owner != "" {
			index = tx.Bucket(bucketJobOwnerIndex)
			prefix = jobOwnerIndexPrefix(owner)
		}
		cursor := index.Cursor()
		key, id := cursor.Last()
		if len(prefix) > 0 {
			upper := prefixUpperBound(prefix)
			if upper != nil {
				key, id = cursor.Seek(upper)
				if key == nil {
					key, id = cursor.Last()
				} else {
					key, id = cursor.Prev()
				}
			}
		}
		for ; key != nil && len(jobs) < limit; key, id = cursor.Prev() {
			if len(prefix) > 0 && !bytes.HasPrefix(key, prefix) {
				break
			}
			raw := tx.Bucket(bucketJobs).Get(id)
			if raw == nil {
				return os.ErrNotExist
			}
			var record jobHistoryRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return err
			}
			job := record.Job()
			if owner != "" && (job.OwnerSubject != owner || !bytes.Equal(key, jobOwnerIndexKey(job))) {
				return errors.New("job owner index does not match its authoritative record")
			}
			if (owner == "" || job.OwnerSubject == owner) && (status == "" || job.Status == status) {
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
			if getJSON(tx.Bucket(bucketJobs), string(id), &job) != nil || job.Status != JobCompleted || job.Usage.ResourceScope != "job" || job.Usage.PeakVRAMBytes == 0 {
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

const (
	maximumRetainedJobEvents            = 256
	maximumAdvisoryProgressEventsPerJob = 64
)

var (
	keyJobEventSequence      = []byte("_sequence")
	keyJobProgressEventCount = []byte("_progress_count")
	jobEventPrefix           = byte('e')
)

func jobEventKey(sequence uint64) []byte {
	key := make([]byte, 9)
	key[0] = jobEventPrefix
	binary.BigEndian.PutUint64(key[1:], sequence)
	return key
}

func appendAuthoritativeJobEventTx(tx *bolt.Tx, store *Store, job Job, eventType string) error {
	switch eventType {
	case "job.accepted", "job.queued", "route.selected", "worker.assigned", "execution.started", "job.retrying", "job.completed", "job.failed", "job.cancelled", "job.ambiguous":
	default:
		return fmt.Errorf("unsupported authoritative job event %q", eventType)
	}
	timestamp := job.UpdatedAt
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	event := JobEvent{
		Schema: JobEventSchemaV1, JobID: job.ID, Type: eventType,
		Source: "relay", Authority: "authoritative", Time: timestamp, Attempt: job.Attempt,
		NodeID: job.AssignedNode, StepID: job.Step,
	}
	return appendJobEventTx(tx, store, event)
}

func appendAdvisoryJobProgressEventTx(tx *bolt.Tx, store *Store, job Job, progress JobProgress) error {
	root := tx.Bucket(bucketJobEvents)
	bucket, err := root.CreateBucketIfNotExists([]byte(job.ID))
	if err != nil {
		return err
	}
	count := uint64(0)
	if raw := bucket.Get(keyJobProgressEventCount); len(raw) == 8 {
		count = binary.BigEndian.Uint64(raw)
	}
	if count >= maximumAdvisoryProgressEventsPerJob {
		return nil
	}
	event := JobEvent{
		Schema: JobEventSchemaV1, JobID: job.ID, Type: "execution.progress",
		Source: "worker", Authority: "advisory", Time: progress.UpdatedAt,
		Attempt: job.Attempt, NodeID: job.AssignedNode, StepID: job.Step,
		Progress: &JobEventProgress{
			ReportedSequence: progress.Sequence, Phase: progress.Phase,
			Percent: progress.Percent, Busy: progress.Busy,
		},
	}
	if err := appendExecutionEventTx(tx, store, bucketJobEvents, job.ID, event); err != nil {
		return err
	}
	encodedCount := make([]byte, 8)
	binary.BigEndian.PutUint64(encodedCount, count+1)
	return bucket.Put(keyJobProgressEventCount, encodedCount)
}

func appendJobEventTx(tx *bolt.Tx, store *Store, event JobEvent) error {
	return appendExecutionEventTx(tx, store, bucketJobEvents, event.JobID, event)
}

func appendPipelineEventTx(tx *bolt.Tx, store *Store, run PipelineRun, eventType string) error {
	switch eventType {
	case "pipeline.started", "pipeline.completed", "pipeline.failed", "pipeline.cancelled":
	default:
		return fmt.Errorf("unsupported authoritative pipeline event %q", eventType)
	}
	timestamp := run.CreatedAt
	if !run.FinishedAt.IsZero() {
		timestamp = run.FinishedAt
	}
	event := JobEvent{
		Schema: JobEventSchemaV1, RunID: run.ID, Type: eventType,
		Source: "relay", Authority: "authoritative", Time: timestamp,
	}
	return appendExecutionEventTx(tx, store, bucketPipelineEvents, run.ID, event)
}

func appendPipelineStepEventTx(tx *bolt.Tx, store *Store, job Job, eventType string) error {
	if job.ParentID == "" || job.Step == "" {
		return nil
	}
	switch eventType {
	case "pipeline.step.queued", "pipeline.step.started", "pipeline.step.completed", "pipeline.step.failed", "pipeline.step.cancelled", "pipeline.step.ambiguous":
	default:
		return fmt.Errorf("unsupported authoritative pipeline step event %q", eventType)
	}
	event := JobEvent{
		Schema: JobEventSchemaV1, RunID: job.ParentID, JobID: job.ID, Type: eventType,
		Source: "relay", Authority: "authoritative", Time: job.UpdatedAt,
		Attempt: job.Attempt, NodeID: job.AssignedNode, StepID: job.Step,
	}
	return appendExecutionEventTx(tx, store, bucketPipelineEvents, job.ParentID, event)
}

func appendExecutionEventTx(tx *bolt.Tx, store *Store, rootName []byte, scopeID string, event JobEvent) error {
	root := tx.Bucket(rootName)
	bucket, err := root.CreateBucketIfNotExists([]byte(scopeID))
	if err != nil {
		return err
	}
	sequence := uint64(0)
	if raw := bucket.Get(keyJobEventSequence); len(raw) == 8 {
		sequence = binary.BigEndian.Uint64(raw)
	}
	if sequence == ^uint64(0) {
		return errors.New("job event sequence exhausted")
	}
	sequence++
	event.Sequence = sequence
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if store != nil && store.saveJobEventTestHook != nil {
		if err := store.saveJobEventTestHook(event); err != nil {
			return err
		}
	}
	if err := putJSON(bucket, string(jobEventKey(sequence)), event); err != nil {
		return err
	}
	encodedSequence := make([]byte, 8)
	binary.BigEndian.PutUint64(encodedSequence, sequence)
	if err := bucket.Put(keyJobEventSequence, encodedSequence); err != nil {
		return err
	}
	if sequence > maximumRetainedJobEvents {
		if err := bucket.Delete(jobEventKey(sequence - maximumRetainedJobEvents)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListJobEvents(jobID string, after uint64, limit int) (JobEventPage, error) {
	return s.listExecutionEvents(bucketJobEvents, jobID, after, limit)
}

func (s *Store) ListPipelineEvents(runID string, after uint64, limit int) (JobEventPage, error) {
	return s.listExecutionEvents(bucketPipelineEvents, runID, after, limit)
}

func (s *Store) listExecutionEvents(rootName []byte, scopeID string, after uint64, limit int) (JobEventPage, error) {
	page := JobEventPage{Events: []JobEvent{}, After: after, Next: after}
	if !validJobID(scopeID) {
		return page, errors.New("invalid execution id")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		root := tx.Bucket(rootName)
		bucket := root.Bucket([]byte(scopeID))
		if bucket == nil {
			return nil
		}
		if raw := bucket.Get(keyJobEventSequence); len(raw) == 8 {
			page.Newest = binary.BigEndian.Uint64(raw)
		}
		cursor := bucket.Cursor()
		first, _ := cursor.Seek(jobEventKey(1))
		if len(first) == 9 && first[0] == jobEventPrefix {
			page.OldestRetained = binary.BigEndian.Uint64(first[1:])
			page.Gap = after != ^uint64(0) && after+1 < page.OldestRetained
		}
		if after == ^uint64(0) {
			return nil
		}
		for key, value := cursor.Seek(jobEventKey(after + 1)); key != nil && len(page.Events) < limit; key, value = cursor.Next() {
			if len(key) != 9 || key[0] != jobEventPrefix {
				continue
			}
			var event JobEvent
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			page.Events = append(page.Events, event)
			page.Next = event.Sequence
		}
		return nil
	})
	return page, err
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
	_, overview.UTCOffsetSeconds = time.Now().Zone()
	nodes, err := s.ListNodes()
	if err != nil {
		return overview, err
	}
	overview.NodesTotal = len(nodes)
	for _, node := range nodes {
		if node.Connected && time.Since(node.LastSeen) < 30*time.Second {
			overview.NodesOnline++
		}
		overview.Usage.ComputeMS = saturatingUint64Add(overview.Usage.ComputeMS, node.ComputeMS)
		overview.Usage.EstimatedCostUSD = saturatingCostAdd(overview.Usage.EstimatedCostUSD, node.CostUSD)
		overview.Usage.CostKnownJobs = saturatingUint64Add(overview.Usage.CostKnownJobs, node.CostKnownJobs)
		overview.Usage.CostUnknownJobs = saturatingUint64Add(overview.Usage.CostUnknownJobs, node.CostUnknownJobs)
		accounted := saturatingUint64Add(node.CostKnownJobs, node.CostUnknownJobs)
		if node.JobsTotal > accounted {
			overview.Usage.CostUnknownJobs = saturatingUint64Add(overview.Usage.CostUnknownJobs, node.JobsTotal-accounted)
		}
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		totals, err := readHistoricalJobTotals(tx.Bucket(bucketHistoricalTotals))
		if err != nil {
			return err
		}
		mergeHistoricalJobTotals(&overview, totals)
		return tx.Bucket(bucketJobs).ForEach(func(_, value []byte) error {
			var job Job
			if err := json.Unmarshal(value, &job); err != nil {
				return err
			}
			overview.JobsByState[job.Status] = saturatingUint64Add(overview.JobsByState[job.Status], 1)
			overview.Usage.InputTokens = saturatingUint64Add(overview.Usage.InputTokens, job.Usage.InputTokens)
			overview.Usage.OutputTokens = saturatingUint64Add(overview.Usage.OutputTokens, job.Usage.OutputTokens)
			overview.Usage.TotalTokens = saturatingUint64Add(overview.Usage.TotalTokens, job.Usage.TotalTokens)
			overview.Usage.EquivalentCostUSD = saturatingCostAdd(overview.Usage.EquivalentCostUSD, job.Usage.EquivalentCostUSD)
			overview.Usage.SavedCostUSD = saturatingCostAdd(overview.Usage.SavedCostUSD, job.Usage.SavedCostUSD)
			return nil
		})
	})
	overview.Usage.CostStatus = aggregateCostStatus(overview.Usage.CostKnownJobs, overview.Usage.CostUnknownJobs, overview.Usage.CostStatus, "")
	return overview, err
}

// retainedJobMetrics returns only detailed job records that still exist in
// the jobs bucket. Unlike Overview it deliberately excludes historical totals
// and lifetime node counters so Prometheus metrics named "retained" can fall
// after a retention sweep exactly as documented.
func (s *Store) retainedJobMetrics() (map[string]uint64, Usage, error) {
	states := map[string]uint64{}
	var usage Usage
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobs).ForEach(func(_, value []byte) error {
			var job Job
			if err := json.Unmarshal(value, &job); err != nil {
				return err
			}
			states[job.Status] = saturatingUint64Add(states[job.Status], 1)
			usage.InputTokens = saturatingUint64Add(usage.InputTokens, job.Usage.InputTokens)
			usage.OutputTokens = saturatingUint64Add(usage.OutputTokens, job.Usage.OutputTokens)
			usage.TotalTokens = saturatingUint64Add(usage.TotalTokens, job.Usage.TotalTokens)
			usage.ComputeMS = saturatingUint64Add(usage.ComputeMS, job.Usage.ComputeMS)
			return nil
		})
	})
	return states, usage, err
}

func (s *Store) SavePipelineRun(run PipelineRun) error {
	if err := validatePipelineRunGraphState(run); err != nil {
		return err
	}
	if s.savePipelineRunTestHook != nil {
		if err := s.savePipelineRunTestHook(run); err != nil {
			return err
		}
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketPipelineRuns)
		var previous PipelineRun
		previousErr := getJSON(bucket, run.ID, &previous)
		existed := previousErr == nil
		if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
			return fmt.Errorf("read previous pipeline run: %w", previousErr)
		}
		if existed {
			if err := validatePipelineRunGraphTransition(previous, run); err != nil {
				return err
			}
		}
		if err := putJSON(bucket, run.ID, run); err != nil {
			return err
		}
		if !existed && run.Status == "running" {
			return appendPipelineEventTx(tx, s, run, "pipeline.started")
		}
		if existed && previous.Status == run.Status {
			return nil
		}
		switch run.Status {
		case "completed":
			return appendPipelineEventTx(tx, s, run, "pipeline.completed")
		case "failed":
			return appendPipelineEventTx(tx, s, run, "pipeline.failed")
		case "cancelled":
			return appendPipelineEventTx(tx, s, run, "pipeline.cancelled")
		default:
			return nil
		}
	})
}

// CreatePipelineRunAdmitted atomically counts active runs and persists the new
// run in one Bolt write transaction. This prevents parallel HTTP requests from
// all passing a non-atomic count before starting their goroutines.
func (s *Store) CreatePipelineRunAdmitted(run PipelineRun, maxGlobal, maxOwner int) error {
	if run.ID == "" || !validJobID(run.ID) {
		return errors.New("pipeline run id must use safe ASCII characters")
	}
	if run.Status != "running" {
		return errors.New("an admitted pipeline run must start in running state")
	}
	if err := validatePipelineRunGraphState(run); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketPipelineRuns)
		if bucket.Get([]byte(run.ID)) != nil {
			return os.ErrExist
		}
		global, owned, err := activePipelineCounts(bucket, run.OwnerSubject)
		if err != nil {
			return err
		}
		if maxGlobal > 0 && global >= maxGlobal {
			return ErrPipelineCapacity
		}
		if maxOwner > 0 && owned >= maxOwner {
			return ErrOwnerPipelineCapacity
		}
		if err := putJSON(bucket, run.ID, run); err != nil {
			return err
		}
		return appendPipelineEventTx(tx, s, run, "pipeline.started")
	})
}

func activePipelineCounts(bucket *bolt.Bucket, owner string) (global, owned int, err error) {
	err = bucket.ForEach(func(_, value []byte) error {
		var run PipelineRun
		if decodeErr := json.Unmarshal(value, &run); decodeErr != nil {
			return decodeErr
		}
		if run.Status != "running" {
			return nil
		}
		global++
		if run.OwnerSubject == owner {
			owned++
		}
		return nil
	})
	return global, owned, err
}

// HasActivePipelineRuns scans the authoritative pipeline bucket without a
// presentation limit. ListPipelineRuns is intentionally capped and therefore
// cannot safely answer lifecycle or updater-idle decisions.
func (s *Store) HasActivePipelineRuns() (bool, error) {
	active := false
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketPipelineRuns)
		return bucket.ForEach(func(_, value []byte) error {
			var run PipelineRun
			if err := json.Unmarshal(value, &run); err != nil {
				return err
			}
			if run.Status == "running" {
				active = true
			}
			return nil
		})
	})
	return active, err
}

// FailActivePipelineRuns closes orphaned admission slots on relay startup.
// Pipeline goroutines are process-local and cannot survive a restart.
func (s *Store) FailActivePipelineRuns(reason string) (int, error) {
	reason = cleanLabel(reason, 500)
	if reason == "" {
		reason = "relay restarted before pipeline completion"
	}
	failed := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketPipelineRuns)
		updates := map[string]PipelineRun{}
		if err := bucket.ForEach(func(key, value []byte) error {
			var run PipelineRun
			if err := json.Unmarshal(value, &run); err != nil {
				return err
			}
			if run.Status != "running" {
				return nil
			}
			run.Status = "failed"
			run.Error = reason
			run.FinishedAt = time.Now().UTC()
			updates[string(key)] = run
			failed++
			return nil
		}); err != nil {
			return err
		}
		for key, run := range updates {
			if err := putJSON(bucket, key, run); err != nil {
				return err
			}
			if err := appendPipelineEventTx(tx, s, run, "pipeline.failed"); err != nil {
				return err
			}
		}
		return nil
	})
	return failed, err
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

func publicKeyFingerprint(publicKey []byte) string {
	digest := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func jobIndexKey(job Job) []byte {
	return []byte(fmt.Sprintf("%020d:%s", job.CreatedAt.UnixNano(), job.ID))
}

func jobOwnerIndexPrefix(owner string) []byte {
	digest := sha256.Sum256([]byte(owner))
	return append([]byte(nil), digest[:]...)
}

func jobOwnerIndexKey(job Job) []byte {
	if job.OwnerSubject == "" {
		return nil
	}
	return append(jobOwnerIndexPrefix(job.OwnerSubject), jobIndexKey(job)...)
}

func putJobOwnerIndex(bucket *bolt.Bucket, job Job) error {
	key := jobOwnerIndexKey(job)
	if len(key) == 0 {
		return nil
	}
	return bucket.Put(key, []byte(job.ID))
}

func deleteJobOwnerIndex(bucket *bolt.Bucket, job Job) error {
	key := jobOwnerIndexKey(job)
	if len(key) == 0 {
		return nil
	}
	value := bucket.Get(key)
	if value != nil && bytes.Equal(value, []byte(job.ID)) {
		return bucket.Delete(key)
	}
	return nil
}

func prefixUpperBound(prefix []byte) []byte {
	upper := append([]byte(nil), prefix...)
	for index := len(upper) - 1; index >= 0; index-- {
		if upper[index] != 0xff {
			upper[index]++
			return upper[:index+1]
		}
	}
	return nil
}

func ensureJobOwnerIndex(tx *bolt.Tx) error {
	meta := tx.Bucket(bucketStoreMeta)
	if bytes.Equal(meta.Get(keyJobOwnerIndexVersion), jobOwnerIndexVersion) {
		return nil
	}
	if err := tx.DeleteBucket(bucketJobOwnerIndex); err != nil && !errors.Is(err, bolt.ErrBucketNotFound) {
		return err
	}
	index, err := tx.CreateBucket(bucketJobOwnerIndex)
	if err != nil {
		return err
	}
	if err := tx.Bucket(bucketJobs).ForEach(func(key, value []byte) error {
		var job Job
		if err := json.Unmarshal(value, &job); err != nil {
			return fmt.Errorf("migrate job owner index for %q: %w", key, err)
		}
		if job.ID != string(key) {
			return fmt.Errorf("migrate job owner index: record key %q does not match id %q", key, job.ID)
		}
		return putJobOwnerIndex(index, job)
	}); err != nil {
		return err
	}
	return meta.Put(keyJobOwnerIndexVersion, jobOwnerIndexVersion)
}

func queueKey(job Job) []byte {
	return []byte(fmt.Sprintf("%03d:%020d:%s", 100-job.Priority, job.CreatedAt.UnixNano(), job.ID))
}

func putQueueEntry(tx *bolt.Tx, job Job) error {
	bucket := tx.Bucket(bucketQueue)
	index := tx.Bucket(bucketQueueJobIndex)
	counts := tx.Bucket(bucketQueueCounts)
	key := queueKey(job)
	value, err := json.Marshal(queueEntry{JobID: job.ID, OwnerSubject: job.OwnerSubject, Priority: job.Priority, Requirements: job.Requirements, PolicyDecision: job.PolicyDecision, AssignedNode: job.AssignedNode, Sealed: job.SealedPayload != nil})
	if err != nil {
		return err
	}
	if previousKey := index.Get([]byte(job.ID)); previousKey != nil {
		previousValue := bucket.Get(previousKey)
		if previousValue == nil {
			return errors.New("queue job index points to a missing entry")
		}
		previous, decodeErr := queueEntryFromValue(tx.Bucket(bucketJobs), previousValue)
		if decodeErr != nil {
			return decodeErr
		}
		if !bytes.Equal(previousKey, key) {
			if err := bucket.Delete(previousKey); err != nil {
				return err
			}
		}
		if previous.OwnerSubject != job.OwnerSubject {
			if err := adjustQueueCounter(counts, queueOwnerCounterKey(previous.OwnerSubject), -1); err != nil {
				return err
			}
			if err := adjustQueueCounter(counts, queueOwnerCounterKey(job.OwnerSubject), 1); err != nil {
				return err
			}
		}
	} else {
		if err := adjustQueueCounter(counts, queueTotalCounterKey(), 1); err != nil {
			return err
		}
		if err := adjustQueueCounter(counts, queueOwnerCounterKey(job.OwnerSubject), 1); err != nil {
			return err
		}
	}
	if err := bucket.Put(key, value); err != nil {
		return err
	}
	return index.Put([]byte(job.ID), key)
}

func queueEntryFromValue(jobs *bolt.Bucket, value []byte) (queueEntry, error) {
	var entry queueEntry
	if len(value) > 0 && value[0] == '{' {
		if err := json.Unmarshal(value, &entry); err != nil {
			return queueEntry{}, err
		}
	} else {
		raw := jobs.Get(value)
		if raw == nil {
			return queueEntry{JobID: string(value)}, nil
		}
		var err error
		entry, _, err = queueEntryFromJob(raw)
		if err != nil {
			return queueEntry{}, err
		}
	}
	if !validJobID(entry.JobID) || entry.Priority < -100 || entry.Priority > 100 {
		return queueEntry{}, errors.New("queue entry is invalid")
	}
	return entry, nil
}

func queueEntryFromJob(raw []byte) (queueEntry, string, error) {
	var projection struct {
		ID             string         `json:"id"`
		OwnerSubject   string         `json:"owner_subject"`
		Priority       int            `json:"priority"`
		Requirements   Requirements   `json:"requirements"`
		PolicyDecision PolicyDecision `json:"policy_decision"`
		AssignedNode   string         `json:"assigned_node"`
		SealedPayload  *struct{}      `json:"sealed_payload"`
		Status         string         `json:"status"`
	}
	if err := json.Unmarshal(raw, &projection); err != nil {
		return queueEntry{}, "", err
	}
	return queueEntry{JobID: projection.ID, OwnerSubject: projection.OwnerSubject, Priority: projection.Priority, Requirements: projection.Requirements, PolicyDecision: projection.PolicyDecision, AssignedNode: projection.AssignedNode, Sealed: projection.SealedPayload != nil}, projection.Status, nil
}

func ensureQueueIndex(tx *bolt.Tx) error {
	meta := tx.Bucket(bucketStoreMeta)
	if bytes.Equal(meta.Get(keyQueueIndexVersion), queueIndexVersion) {
		return nil
	}
	queue := tx.Bucket(bucketQueue)
	jobs := tx.Bucket(bucketJobs)
	type replacement struct{ key, value []byte }
	replacements := []replacement{}
	deletions := [][]byte{}
	cursor := queue.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		entryID := string(value)
		if len(value) > 0 && value[0] == '{' {
			var saved queueEntry
			if err := json.Unmarshal(value, &saved); err != nil {
				return fmt.Errorf("migrate queue entry %q: %w", key, err)
			}
			entryID = saved.JobID
		}
		raw := jobs.Get([]byte(entryID))
		if raw == nil {
			deletions = append(deletions, append([]byte(nil), key...))
			continue
		}
		entry, status, err := queueEntryFromJob(raw)
		if err != nil {
			return fmt.Errorf("migrate queue job %q: %w", entryID, err)
		}
		if status != JobQueued {
			deletions = append(deletions, append([]byte(nil), key...))
			continue
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		replacements = append(replacements, replacement{append([]byte(nil), key...), encoded})
	}
	for _, key := range deletions {
		if err := queue.Delete(key); err != nil {
			return err
		}
	}
	for _, item := range replacements {
		if err := queue.Put(item.key, item.value); err != nil {
			return err
		}
	}
	jobIndex := tx.Bucket(bucketQueueJobIndex)
	counts := tx.Bucket(bucketQueueCounts)
	if err := clearBucket(jobIndex); err != nil {
		return err
	}
	if err := clearBucket(counts); err != nil {
		return err
	}
	cursor = queue.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		entry, err := queueEntryFromValue(jobs, value)
		if err != nil {
			return err
		}
		if err := jobIndex.Put([]byte(entry.JobID), append([]byte(nil), key...)); err != nil {
			return err
		}
		if err := adjustQueueCounter(counts, queueTotalCounterKey(), 1); err != nil {
			return err
		}
		if err := adjustQueueCounter(counts, queueOwnerCounterKey(entry.OwnerSubject), 1); err != nil {
			return err
		}
	}
	return meta.Put(keyQueueIndexVersion, queueIndexVersion)
}

func deleteQueueEntry(tx *bolt.Tx, jobID string) error {
	bucket := tx.Bucket(bucketQueue)
	index := tx.Bucket(bucketQueueJobIndex)
	key := index.Get([]byte(jobID))
	if key == nil {
		return nil
	}
	entry, err := queueEntryFromValue(tx.Bucket(bucketJobs), bucket.Get(key))
	if err != nil {
		return err
	}
	if entry.JobID != jobID {
		return errors.New("queue job index does not match its entry")
	}
	if err := bucket.Delete(key); err != nil {
		return err
	}
	if err := index.Delete([]byte(jobID)); err != nil {
		return err
	}
	counts := tx.Bucket(bucketQueueCounts)
	if err := adjustQueueCounter(counts, queueTotalCounterKey(), -1); err != nil {
		return err
	}
	return adjustQueueCounter(counts, queueOwnerCounterKey(entry.OwnerSubject), -1)
}

func clearBucket(bucket *bolt.Bucket) error {
	cursor := bucket.Cursor()
	for key, _ := cursor.First(); key != nil; key, _ = cursor.Next() {
		if err := cursor.Delete(); err != nil {
			return err
		}
	}
	return nil
}

func queueTotalCounterKey() []byte { return []byte{0} }

func queueOwnerCounterKey(owner string) []byte {
	return append([]byte{1}, []byte(owner)...)
}

func queueCounter(bucket *bolt.Bucket, key []byte) (int, error) {
	raw := bucket.Get(key)
	if len(raw) == 0 {
		return 0, nil
	}
	if len(raw) != 8 {
		return 0, errors.New("durable queue counter is invalid")
	}
	count := binary.BigEndian.Uint64(raw)
	if count > 1_000_000 {
		return 0, errors.New("durable queue counter exceeds the supported maximum")
	}
	return int(count), nil
}

func adjustQueueCounter(bucket *bolt.Bucket, key []byte, delta int) error {
	current, err := queueCounter(bucket, key)
	if err != nil {
		return err
	}
	next := current + delta
	if next < 0 || next > 1_000_000 {
		return errors.New("durable queue counter would leave the supported range")
	}
	if next == 0 {
		return bucket.Delete(key)
	}
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, uint64(next))
	return bucket.Put(key, raw)
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
		value = truncateUTF8Bytes(value, limit)
	}
	return value
}

func truncateUTF8Bytes(value string, limit int) string {
	if limit < 0 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
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
