package raft_storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
	"github.com/Aetherance/kv/raft"
)

func TestLearnerChangePersistsAndRemovedIDCannotReturn(t *testing.T) {
	directory := t.TempDir()
	newConfig := func() *config.Config {
		cfg := config.NewDefaultConfig()
		cfg.StoreID = 1
		cfg.ClusterID = 42
		cfg.Peers = map[uint64]string{1: "127.0.0.1:1"}
		cfg.DBPath = directory
		cfg.RaftBaseTickInterval = time.Hour
		return cfg
	}

	first := NewRaftStorage(newConfig())
	if err := first.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	member := &clusterpb.Member{Id: 2, RaftAddress: "127.0.0.1:2"}
	if err := first.proposeMembership(context.Background(), raftpb.ConfChangeType_AddLearnerNode, member); err != nil {
		t.Fatalf("add learner: %v", err)
	}
	assertMemberState(t, first, 1, []uint64{1}, []uint64{2}, []uint64{1, 2}, nil)

	// Exact retries are idempotent and do not advance the metadata revision.
	if err := first.proposeMembership(context.Background(), raftpb.ConfChangeType_AddLearnerNode, member); err != nil {
		t.Fatalf("retry learner add: %v", err)
	}
	assertMemberState(t, first, 1, []uint64{1}, []uint64{2}, []uint64{1, 2}, nil)
	if err := first.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	second := NewRaftStorage(newConfig())
	if err := second.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = second.Stop() })
	assertMemberState(t, second, 1, []uint64{1}, []uint64{2}, []uint64{1, 2}, nil)

	if err := second.proposeMembership(context.Background(), raftpb.ConfChangeType_RemoveNode, member); err != nil {
		t.Fatalf("remove learner: %v", err)
	}
	assertMemberState(t, second, 2, []uint64{1}, nil, []uint64{1}, []uint64{2})
	if err := second.proposeMembership(context.Background(), raftpb.ConfChangeType_AddLearnerNode, member); !errors.Is(err, errMemberRemoved) {
		t.Fatalf("re-add removed member error = %v, want %v", err, errMemberRemoved)
	}
}

func TestLearnerPromotionRequiresCatchUp(t *testing.T) {
	store := startMembershipStore(t)
	member := &clusterpb.Member{Id: 2, RaftAddress: "127.0.0.1:2"}
	if err := store.proposeMembership(context.Background(), raftpb.ConfChangeType_AddLearnerNode, member); err != nil {
		t.Fatalf("add learner: %v", err)
	}
	if err := store.proposeMembership(context.Background(), raftpb.ConfChangeType_AddNode, member); !errors.Is(err, errLearnerNotReady) {
		t.Fatalf("promote uncaught learner error = %v, want %v", err, errLearnerNotReady)
	}

	progress := store.node.Raft.Prs[2]
	progress.RecentActive = true
	progress.Match = store.node.CommitIndex()
	progress.PendingSnapshot = 0
	if err := store.proposeMembership(context.Background(), raftpb.ConfChangeType_AddNode, member); err != nil {
		t.Fatalf("promote caught-up learner: %v", err)
	}
	assertMemberState(t, store, 2, []uint64{1, 2}, nil, []uint64{1, 2}, nil)
}

func TestMembershipSafetyValidation(t *testing.T) {
	store := startMembershipStore(t)
	if err := store.proposeMembership(context.Background(), raftpb.ConfChangeType_RemoveNode, &clusterpb.Member{Id: 1}); !errors.Is(err, errLastVoter) {
		t.Fatalf("remove last voter error = %v, want %v", err, errLastVoter)
	}
	if err := store.proposeMembership(context.Background(), raftpb.ConfChangeType_AddLearnerNode, &clusterpb.Member{
		Id: 2, RaftAddress: "127.0.0.1:1",
	}); !errors.Is(err, errAddressAlreadyExists) {
		t.Fatalf("duplicate address error = %v, want %v", err, errAddressAlreadyExists)
	}
	if err := store.proposeMembership(context.Background(), raftpb.ConfChangeType_AddLearnerNode, &clusterpb.Member{
		Id: 2, RaftAddress: "127.0.0.1:2",
	}); err != nil {
		t.Fatalf("add learner: %v", err)
	}
	if err := store.proposeMembership(context.Background(), raftpb.ConfChangeType_AddLearnerNode, &clusterpb.Member{
		Id: 3, RaftAddress: "127.0.0.1:3",
	}); !errors.Is(err, errTooManyLearners) {
		t.Fatalf("second learner error = %v, want %v", err, errTooManyLearners)
	}
}

func TestMemberAddressUpdate(t *testing.T) {
	store := startMembershipStore(t)
	member := &clusterpb.Member{Id: 1, RaftAddress: " 127.0.0.1:9 "}
	if err := store.proposeMembership(context.Background(), raftpb.ConfChangeType_UpdateNode, member); err != nil {
		t.Fatalf("update member: %v", err)
	}
	if store.state.cluster.ConfRevision != 1 || store.state.cluster.Members[0].RaftAddress != "127.0.0.1:9" {
		t.Fatalf("unexpected metadata after update: %v", store.state.cluster)
	}
	if err := store.proposeMembership(context.Background(), raftpb.ConfChangeType_UpdateNode, member); err != nil {
		t.Fatalf("retry member update: %v", err)
	}
	if store.state.cluster.ConfRevision != 1 {
		t.Fatalf("idempotent update changed revision to %d", store.state.cluster.ConfRevision)
	}
}

func TestVoterRemovalRequiresPostChangeQuorum(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.StoreID = 1
	cfg.ClusterID = 42
	cfg.Peers = map[uint64]string{
		1: "127.0.0.1:1",
		2: "127.0.0.1:2",
		3: "127.0.0.1:3",
	}
	cfg.DBPath = t.TempDir()
	cfg.RaftBaseTickInterval = time.Hour
	store := NewRaftStorage(cfg)
	if err := store.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = store.Stop() })
	// validateConfChange is called only on a leader; mirror that precondition so
	// RawNode exposes its replication progress.
	store.node.Raft.State = raft.StateLeader
	store.node.Raft.Lead = 1

	change := membershipChange(t, 1, raftpb.ConfChangeType_RemoveNode, &clusterpb.Member{Id: 2})
	if err := store.validateConfChange(change); !errors.Is(err, errUnsafeReconfiguration) {
		t.Fatalf("inactive post-change quorum error = %v, want %v", err, errUnsafeReconfiguration)
	}
	store.node.Raft.Prs[3].RecentActive = true
	if err := store.validateConfChange(change); err != nil {
		t.Fatalf("active post-change quorum rejected: %v", err)
	}
}

func startMembershipStore(t *testing.T) *RaftStorage {
	t.Helper()
	cfg := config.NewDefaultConfig()
	cfg.StoreID = 1
	cfg.ClusterID = 42
	cfg.Peers = map[uint64]string{1: "127.0.0.1:1"}
	cfg.DBPath = t.TempDir()
	cfg.RaftBaseTickInterval = time.Hour
	store := NewRaftStorage(cfg)
	if err := store.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = store.Stop() })
	return store
}

func assertMemberState(t *testing.T, store *RaftStorage, revision uint64, voters, learners, members, removed []uint64) {
	t.Helper()
	if store.state.cluster.ConfRevision != revision {
		t.Fatalf("revision = %d, want %d", store.state.cluster.ConfRevision, revision)
	}
	confState := store.node.ConfState()
	assertIDs(t, "voters", confState.Voters, voters)
	assertIDs(t, "learners", confState.Learners, learners)
	memberIDs := make([]uint64, 0, len(store.state.cluster.Members))
	for _, member := range store.state.cluster.Members {
		memberIDs = append(memberIDs, member.Id)
	}
	assertIDs(t, "members", memberIDs, members)
	assertIDs(t, "removed members", store.state.cluster.RemovedMemberIds, removed)
}

func assertIDs(t *testing.T, name string, got, want []uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
}

func membershipChange(t *testing.T, sequence uint64, changeType raftpb.ConfChangeType, member *clusterpb.Member) *raftpb.ConfChange {
	t.Helper()
	context, err := proto.Marshal(&clusterpb.ConfChangeContext{
		ProposerId: 1,
		Sequence:   sequence,
		Member:     member,
	})
	if err != nil {
		t.Fatalf("encode change context: %v", err)
	}
	return &raftpb.ConfChange{ChangeType: changeType, NodeId: member.Id, Context: context}
}
