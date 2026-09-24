package cluster

import (
	"bytes"
	"fmt"
	"testing"

	raft "go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

// TestSelectedConsensusCoreRejectsMinorityWrites is a CB-specific dependency
// gate. It is not a claim that the relay HA integration is complete: it proves
// that the exact selected Raft core keeps a partitioned former leader's
// proposal out of the committed command stream while the majority elects a
// replacement and commits a different command.
func TestSelectedConsensusCoreRejectsMinorityWrites(t *testing.T) {
	sim := newConsensusSimulation(t, 1, 2, 3)
	sim.campaign(t, 1)
	if leader := sim.singleLeader(t, 1, 2, 3); leader != 1 {
		t.Fatalf("initial leader = %d, want 1", leader)
	}

	sim.propose(t, 1, []byte("job:admit:stable"))
	sim.requireApplied(t, []byte("job:admit:stable"))

	// Isolate the old leader in both directions. It may still believe it is
	// leader temporarily, but it cannot commit without a quorum.
	sim.partition(1, 2)
	sim.partition(1, 3)
	if err := sim.nodes[2].raw.ForgetLeader(); err != nil {
		t.Fatalf("forget old leader on node 2: %v", err)
	}
	if err := sim.nodes[3].raw.ForgetLeader(); err != nil {
		t.Fatalf("forget old leader on node 3: %v", err)
	}
	sim.campaign(t, 2)
	if leader := sim.singleLeader(t, 2, 3); leader != 2 {
		t.Fatalf("majority leader = %d, want 2", leader)
	}

	sim.propose(t, 1, []byte("job:dispatch:stale-minority"))
	sim.propose(t, 2, []byte("job:dispatch:majority"))
	if sim.appliedContains(1, []byte("job:dispatch:stale-minority")) {
		t.Fatal("partitioned former leader committed a minority proposal")
	}
	for _, id := range []uint64{2, 3} {
		if !sim.appliedContains(id, []byte("job:dispatch:majority")) {
			t.Fatalf("node %d did not apply the majority proposal", id)
		}
	}

	sim.heal()
	for i := 0; i < 12; i++ {
		for _, node := range sim.nodes {
			node.raw.Tick()
		}
		sim.settle(t)
	}

	for _, id := range []uint64{1, 2, 3} {
		if sim.appliedContains(id, []byte("job:dispatch:stale-minority")) {
			t.Fatalf("node %d applied the stale minority proposal after healing", id)
		}
		if !sim.appliedContains(id, []byte("job:dispatch:majority")) {
			t.Fatalf("node %d did not converge on the committed majority proposal", id)
		}
	}
}

type consensusSimulation struct {
	nodes   map[uint64]*consensusSimulationNode
	blocked map[[2]uint64]bool
}

type consensusSimulationNode struct {
	raw     *raft.RawNode
	storage *raft.MemoryStorage
	applied [][]byte
}

func newConsensusSimulation(t *testing.T, ids ...uint64) *consensusSimulation {
	t.Helper()
	peers := make([]raft.Peer, 0, len(ids))
	for _, id := range ids {
		peers = append(peers, raft.Peer{ID: id})
	}
	sim := &consensusSimulation{nodes: map[uint64]*consensusSimulationNode{}, blocked: map[[2]uint64]bool{}}
	for _, id := range ids {
		storage := raft.NewMemoryStorage()
		raw, err := raft.NewRawNode(&raft.Config{
			ID:                        id,
			ElectionTick:              10,
			HeartbeatTick:             1,
			Storage:                   storage,
			MaxSizePerMsg:             1 << 20,
			MaxInflightMsgs:           64,
			CheckQuorum:               true,
			PreVote:                   true,
			DisableProposalForwarding: true,
			Logger:                    consensusTestLogger{},
		})
		if err != nil {
			t.Fatalf("create raft node %d: %v", id, err)
		}
		if err := raw.Bootstrap(peers); err != nil {
			t.Fatalf("bootstrap raft node %d: %v", id, err)
		}
		sim.nodes[id] = &consensusSimulationNode{raw: raw, storage: storage}
	}
	sim.settle(t)
	return sim
}

func (sim *consensusSimulation) campaign(t *testing.T, id uint64) {
	t.Helper()
	node := sim.nodes[id]
	if node == nil {
		t.Fatalf("campaign node %d is missing", id)
	}
	if err := node.raw.Campaign(); err != nil {
		t.Fatalf("campaign node %d: %v", id, err)
	}
	sim.settle(t)
}

func (sim *consensusSimulation) propose(t *testing.T, id uint64, command []byte) {
	t.Helper()
	node := sim.nodes[id]
	if node == nil {
		t.Fatalf("proposal node %d is missing", id)
	}
	if err := node.raw.Propose(command); err != nil {
		t.Fatalf("propose through node %d: %v", id, err)
	}
	sim.settle(t)
}

func (sim *consensusSimulation) partition(a, b uint64) {
	sim.blocked[[2]uint64{a, b}] = true
	sim.blocked[[2]uint64{b, a}] = true
}

func (sim *consensusSimulation) heal() { sim.blocked = map[[2]uint64]bool{} }

func (sim *consensusSimulation) settle(t *testing.T) {
	t.Helper()
	for round := 0; round < 10000; round++ {
		progress := false
		messages := []*pb.Message{}
		for id, node := range sim.nodes {
			if !node.raw.HasReady() {
				continue
			}
			progress = true
			ready := node.raw.Ready()
			if ready.Snapshot != nil && !raft.IsEmptySnap(ready.Snapshot) {
				if err := node.storage.ApplySnapshot(ready.Snapshot); err != nil {
					t.Fatalf("node %d apply snapshot: %v", id, err)
				}
			}
			if ready.HardState != nil && !raft.IsEmptyHardState(ready.HardState) {
				if err := node.storage.SetHardState(ready.HardState); err != nil {
					t.Fatalf("node %d persist hard state: %v", id, err)
				}
			}
			if err := node.storage.Append(ready.Entries); err != nil {
				t.Fatalf("node %d append entries: %v", id, err)
			}
			for _, entry := range ready.CommittedEntries {
				switch entry.GetType() {
				case pb.EntryConfChange:
					change := &pb.ConfChange{}
					if err := proto.Unmarshal(entry.GetData(), change); err != nil {
						t.Fatalf("node %d decode config change: %v", id, err)
					}
					node.raw.ApplyConfChange(change)
				case pb.EntryNormal:
					if len(entry.GetData()) > 0 {
						node.applied = append(node.applied, bytes.Clone(entry.GetData()))
					}
				}
			}
			messages = append(messages, ready.Messages...)
			node.raw.Advance(ready)
		}
		for _, message := range messages {
			if sim.blocked[[2]uint64{message.GetFrom(), message.GetTo()}] {
				continue
			}
			recipient := sim.nodes[message.GetTo()]
			if recipient == nil {
				t.Fatalf("message addressed to unknown node %d", message.GetTo())
			}
			if err := recipient.raw.Step(message); err != nil {
				t.Fatalf("deliver %s %d -> %d: %v", message.GetType(), message.GetFrom(), message.GetTo(), err)
			}
			progress = true
		}
		if !progress {
			return
		}
	}
	t.Fatal("consensus simulation did not settle")
}

func (sim *consensusSimulation) singleLeader(t *testing.T, ids ...uint64) uint64 {
	t.Helper()
	leader := uint64(0)
	for _, id := range ids {
		status := sim.nodes[id].raw.BasicStatus()
		if status.RaftState != raft.StateLeader {
			continue
		}
		if leader != 0 {
			t.Fatalf("nodes %d and %d both report leader in the same visible partition", leader, id)
		}
		leader = id
	}
	if leader == 0 {
		t.Fatalf("no leader among %v", ids)
	}
	return leader
}

func (sim *consensusSimulation) requireApplied(t *testing.T, command []byte) {
	t.Helper()
	for id := range sim.nodes {
		if !sim.appliedContains(id, command) {
			t.Fatalf("node %d did not apply %q; applied=%s", id, command, formatApplied(sim.nodes[id].applied))
		}
	}
}

func (sim *consensusSimulation) appliedContains(id uint64, command []byte) bool {
	for _, applied := range sim.nodes[id].applied {
		if bytes.Equal(applied, command) {
			return true
		}
	}
	return false
}

func formatApplied(commands [][]byte) string {
	return fmt.Sprintf("%q", commands)
}

type consensusTestLogger struct{}

func (consensusTestLogger) Debug(...interface{})            {}
func (consensusTestLogger) Debugf(string, ...interface{})   {}
func (consensusTestLogger) Error(...interface{})            {}
func (consensusTestLogger) Errorf(string, ...interface{})   {}
func (consensusTestLogger) Info(...interface{})             {}
func (consensusTestLogger) Infof(string, ...interface{})    {}
func (consensusTestLogger) Warning(...interface{})          {}
func (consensusTestLogger) Warningf(string, ...interface{}) {}
func (consensusTestLogger) Fatal(v ...interface{})          { panic(fmt.Sprint(v...)) }
func (consensusTestLogger) Fatalf(f string, v ...interface{}) {
	panic(fmt.Sprintf(f, v...))
}
func (consensusTestLogger) Panic(v ...interface{}) { panic(fmt.Sprint(v...)) }
func (consensusTestLogger) Panicf(f string, v ...interface{}) {
	panic(fmt.Sprintf(f, v...))
}
