package cluster

import (
	"errors"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// InteractiveCapacity is operator policy, not a producer-selected QoS class.
// The existing credential priority ceiling authorizes access to its threshold.
type InteractiveCapacity struct {
	MinPriority int                        `json:"min_priority" yaml:"min_priority"`
	Scopes      []InteractiveCapacityScope `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

type InteractiveCapacityScope struct {
	Group      string `json:"group" yaml:"group"`
	Task       string `json:"task" yaml:"task"`
	Provider   string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Slots      int    `json:"slots" yaml:"slots"`
	BorrowIdle bool   `json:"borrow_idle,omitempty" yaml:"borrow_idle,omitempty"`
}

func (p InteractiveCapacity) Validate() error {
	if len(p.Scopes) == 0 {
		return nil
	}
	if len(p.Scopes) > 32 || p.MinPriority < 1 || p.MinPriority > 100 {
		return errors.New("interactive_capacity requires min_priority 1..100 and at most 32 scopes")
	}
	for _, scope := range p.Scopes {
		if strings.TrimSpace(scope.Group) == "" || strings.TrimSpace(scope.Task) == "" || scope.Slots < 1 || scope.Slots > MaximumWorkerConcurrency {
			return errors.New("interactive_capacity scopes require group, task and slots 1..maximum worker concurrency")
		}
		for _, label := range []string{scope.Group, scope.Task} {
			if err := validatePolicyLabels("interactive_capacity scope", []string{label}); err != nil {
				return err
			}
		}
		if scope.Provider != "" {
			if err := validatePolicyLabels("interactive_capacity provider", []string{scope.Provider}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Overlaps never add hidden slots: use the largest matching reservation and
// permit borrowing only if every matching scope explicitly permits it.
func (p InteractiveCapacity) scopeFor(node Node) (int, bool) {
	slots, borrow := 0, true
	for _, scope := range p.Scopes {
		if !containsFold(node.Capabilities.Groups, scope.Group) ||
			!matchesNode(capacityDemandNode(node), Requirements{Group: scope.Group, Task: scope.Task, Provider: scope.Provider}) {
			continue
		}
		slots = max(slots, scope.Slots)
		borrow = borrow && scope.BorrowIdle
	}
	return min(slots, boundedWorkerCapacity(node.Capabilities.MaxConcurrent)), borrow
}

// Busy endpoints and currently occupied GPU memory must not make a reservation
// disappear precisely when saturation requires it. All identity, model, task,
// profile and principal constraints still use the normal compatibility gates.
func capacityDemandNode(node Node) Node {
	node.Capabilities.AdapterBusy = 0
	node.Capabilities.GPUs = append([]GPUCapability(nil), node.Capabilities.GPUs...)
	for i := range node.Capabilities.GPUs {
		node.Capabilities.GPUs[i].MemoryFree = node.Capabilities.GPUs[i].MemoryTotal
	}
	return node
}

const maximumInteractiveDemandScan = 4096

type interactiveDemand struct {
	pending map[string]bool
	unknown bool
	jobs    []Job
}

// Scan only the priority-ordered interactive prefix, without loading payloads.
// A truncated scan is never evidence of idle capacity: borrowing fails closed.
// Demand is a point-in-time snapshot; arrivals after it reclaim on the next
// dispatch pass. No current execution or encrypted binding is changed.
func (s *Store) interactiveDemandSnapshot(nodes []Node, policy InteractiveCapacity, afterOwner map[int]string) (interactiveDemand, error) {
	result := interactiveDemand{pending: map[string]bool{}}
	if len(policy.Scopes) == 0 {
		return result, nil
	}
	demandNodes := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		if slots, _ := policy.scopeFor(node); slots > 0 {
			demandNodes = append(demandNodes, capacityDemandNode(node))
		}
	}
	if len(demandNodes) == 0 {
		return result, nil
	}
	projections := make([]Job, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketQueue).Cursor()
		scanned := 0
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			entry, err := queueEntryFromValue(tx.Bucket(bucketJobs), value)
			if err != nil {
				return err
			}
			if entry.Priority < policy.MinPriority {
				break
			}
			if scanned == maximumInteractiveDemandScan {
				result.unknown = true
				break
			}
			scanned++
			matched := false
			for _, node := range demandNodes {
				// Pool workers reject unsigned jobs; an explicit binding must stay
				// on its worker. Other readiness gates may clear on a heartbeat,
				// so conservatively retain demand while a matching job is queued.
				if entry.AssignedNode != "" && entry.AssignedNode != node.ID {
					continue
				}
				if node.PoolCertificate != nil && !entry.PoolAuthorized {
					continue
				}
				if !matchesNode(node, entry.Requirements) {
					continue
				}
				result.pending[node.ID], matched = true, true
			}
			if matched {
				job := Job{ID: entry.JobID, OwnerSubject: entry.OwnerSubject, Priority: entry.Priority, Requirements: entry.Requirements, PolicyDecision: entry.PolicyDecision, AssignedNode: entry.AssignedNode}
				if entry.Sealed {
					job.SealedPayload = &SealedEnvelope{}
				}
				if entry.PoolAuthorized {
					job.PoolAuthorization = &PoolJobAuthorization{}
				}
				projections = append(projections, job)
			}
		}
		return nil
	})
	result.jobs = fairQueueWindow(projections, 200, afterOwner)
	return result, err
}

func mergeInteractiveQueue(jobs, interactive []Job, owners map[int]string) []Job {
	seen := make(map[string]bool, len(jobs)+len(interactive))
	merged := make([]Job, 0, len(jobs)+len(interactive))
	for _, list := range [][]Job{interactive, jobs} {
		for _, job := range list {
			if !seen[job.ID] {
				seen[job.ID] = true
				merged = append(merged, job)
			}
		}
	}
	return fairQueueWindow(merged, len(merged), owners)
}

func rankWithInteractiveCapacity(nodes []Node, requirements Requirements, estimate uint64, owner string, now time.Time, placement PlacementPolicy, policy InteractiveCapacity, priority int, demand interactiveDemand) ([]Candidate, RoutingDecision) {
	candidates, decision := rankWithDecisionForOwnerPolicy(nodes, requirements, estimate, owner, now, placement)
	if len(policy.Scopes) == 0 {
		return candidates, decision
	}
	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	allowed := make(map[string]bool, len(nodes))
	for i := range decision.Candidates {
		item := &decision.Candidates[i]
		node := byID[item.NodeID]
		slots, borrow := policy.scopeFor(node)
		if slots == 0 {
			allowed[node.ID] = item.Eligible
			continue
		}
		item.ReservedSlots = slots
		limit, outcome := interactiveCapacityLimit(node, policy, priority, demand, slots, borrow)
		item.ReservationOutcome = outcome
		if node.Capabilities.Running >= limit && priority < policy.MinPriority {
			item.Eligible = false
			item.RejectionReasons = appendUniqueReason(item.RejectionReasons, "interactive_capacity_reserved")
		}
		allowed[node.ID] = item.Eligible
	}
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if allowed[candidate.Node.ID] {
			filtered = append(filtered, candidate)
		}
	}
	applyRoutingNodeConstraints(&decision, "", "")
	return filtered, decision
}

func interactiveCapacityLimit(node Node, policy InteractiveCapacity, priority int, demand interactiveDemand, slots int, borrow bool) (int, string) {
	capacity := boundedWorkerCapacity(node.Capabilities.MaxConcurrent)
	if priority >= policy.MinPriority {
		return capacity, "interactive"
	}
	if borrow && !demand.unknown && !demand.pending[node.ID] {
		return capacity, "borrow_idle"
	}
	limit := min(capacity, capacity-slots+node.interactiveRunning)
	outcome := "protected"
	if borrow && demand.unknown {
		outcome = "demand_unknown"
	} else if borrow && demand.pending[node.ID] {
		outcome = "reclaim"
	}
	return limit, outcome
}

// interactiveLoad counts even terminalized executions until their slot release.
func (w *workerConnection) interactiveLoad() int {
	_, _, count := w.capacityLoad()
	return count
}

func (w *workerConnection) capacityLoad() (int, int, int) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	count := 0
	for _, reservation := range w.inFlight {
		if reservation.interactive {
			count++
		}
	}
	return len(w.inFlight), w.capacity, count
}

// RouteExplainRequest adds only the scheduling priority to placement preview.
// Encryption reservations retain their existing request and binding contract.
type RouteExplainRequest struct {
	AssignmentRequest
	Priority int `json:"priority,omitempty"`
}
