package raft_storage

import (
	"context"
	"path/filepath"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage"
	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
	"github.com/Aetherance/kv/raft"
)

func TestClusterMetadataSurvivesSnapshotAndRestart(t *testing.T) {
	directory := t.TempDir()
	newConfig := func(clusterID uint64, address string) *config.Config {
		cfg := config.NewDefaultConfig()
		cfg.StoreID = 1
		cfg.ClusterID = clusterID
		cfg.Peers = map[uint64]string{1: address}
		cfg.DBPath = directory
		cfg.RaftLogGcCountLimit = 1
		return cfg
	}

	first := NewRaftStorage(newConfig(42, "127.0.0.1:1"))
	if err := first.Start(); err != nil {
		t.Fatalf("start first instance: %v", err)
	}
	if err := first.Write(context.Background(), []storage.Modify{{Data: storage.Put{
		Cf: "default", Key: []byte("key"), Val: []byte("value"),
	}}}); err != nil {
		t.Fatalf("write: %v", err)
	}

	snapshotData := new(rspb.RaftSnapshotData)
	if err := proto.Unmarshal(first.state.snapshot.Data, snapshotData); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	assertClusterMetadata(t, snapshotData.Cluster, 42, map[uint64]string{1: "127.0.0.1:1"})
	if err := first.Stop(); err != nil {
		t.Fatalf("stop first instance: %v", err)
	}

	// Bootstrap values are deliberately different: persisted metadata must win
	// once the database has been initialized.
	second := NewRaftStorage(newConfig(99, "127.0.0.1:9999"))
	if err := second.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = second.Stop() })
	assertClusterMetadata(t, second.state.cluster, 42, map[uint64]string{1: "127.0.0.1:1"})
	second.transport.mu.RLock()
	routedAddress := second.transport.members[1]
	second.transport.mu.RUnlock()
	if routedAddress != "127.0.0.1:1" {
		t.Fatalf("transport used bootstrap address %q instead of persisted metadata", routedAddress)
	}
}

func TestApplyMembershipPersistsConfStateAndMetadata(t *testing.T) {
	db := openTestDB(t)
	initial := mustInitialCluster(t, 42, map[uint64]string{1: "127.0.0.1:1"})
	state, err := openRaftStatePersistence(db, initial)
	if err != nil {
		t.Fatalf("bootstrap state: %v", err)
	}

	ready := &raft.Ready{
		HardState: &raftpb.HardState{Term: 1, Commit: 1},
		Entries: []*raftpb.Entry{{
			EntryType: raftpb.EntryType_EntryConfChange,
			Term:      1,
			Index:     1,
		}},
	}
	if err := state.persist(ready); err != nil {
		t.Fatalf("persist committed entry: %v", err)
	}
	nextCluster := &clusterpb.ClusterMetadata{
		ClusterId:    42,
		ConfRevision: 1,
		Members: []*clusterpb.Member{
			{Id: 2, RaftAddress: "127.0.0.1:2"},
			{Id: 1, RaftAddress: "127.0.0.1:1"},
		},
	}
	nextConfState := &raftpb.ConfState{Voters: []uint64{1}, Learners: []uint64{2}}
	if err := state.applyMembership(1, nextConfState, nextCluster); err != nil {
		t.Fatalf("apply membership: %v", err)
	}

	loaded, err := openRaftStatePersistence(db, mustInitialCluster(t, 99, map[uint64]string{1: "ignored"}))
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	assertClusterMetadata(t, loaded.cluster, 42, map[uint64]string{
		1: "127.0.0.1:1",
		2: "127.0.0.1:2",
	})
	if !proto.Equal(loaded.confState, nextConfState) {
		t.Fatalf("conf state = %v, want %v", loaded.confState, nextConfState)
	}
	if loaded.appliedIndex != 1 {
		t.Fatalf("applied index = %d, want 1", loaded.appliedIndex)
	}
}

func TestLegacyStateMigratesClusterMetadata(t *testing.T) {
	db := openTestDB(t)
	legacy := &raftStatePersistence{db: db}
	confState := &raftpb.ConfState{Voters: []uint64{1}}
	if err := db.Update(func(txn *badger.Txn) error {
		if err := txn.Set(legacy.key(initializedSuffix), []byte{1}); err != nil {
			return err
		}
		if err := setProto(txn, legacy.key(hardStateSuffix), &raftpb.HardState{}); err != nil {
			return err
		}
		if err := setProto(txn, legacy.key(confStateSuffix), confState); err != nil {
			return err
		}
		if err := setProto(txn, legacy.key(snapshotSuffix), &raftpb.Snapshot{
			Metadata: &raftpb.SnapshotMetadata{ConfState: confState},
		}); err != nil {
			return err
		}
		if err := setUint64(txn, legacy.key(appliedSuffix), 0); err != nil {
			return err
		}
		return setUint64(txn, legacy.key(lastIndexSuffix), 0)
	}); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	initial := mustInitialCluster(t, 42, map[uint64]string{1: "127.0.0.1:1"})
	loaded, err := openRaftStatePersistence(db, initial)
	if err != nil {
		t.Fatalf("migrate state: %v", err)
	}
	assertClusterMetadata(t, loaded.cluster, 42, map[uint64]string{1: "127.0.0.1:1"})

	persisted := new(clusterpb.ClusterMetadata)
	if err := db.View(func(txn *badger.Txn) error {
		return getProto(txn, loaded.key(clusterSuffix), persisted)
	}); err != nil {
		t.Fatalf("read migrated metadata: %v", err)
	}
	if !proto.Equal(persisted, loaded.cluster) {
		t.Fatalf("persisted metadata = %v, want %v", persisted, loaded.cluster)
	}
}

func openTestDB(t *testing.T) *badger.DB {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustInitialCluster(t *testing.T, clusterID uint64, peers map[uint64]string) *clusterpb.ClusterMetadata {
	t.Helper()
	metadata, err := initialClusterMetadata(clusterID, peers)
	if err != nil {
		t.Fatalf("create initial cluster: %v", err)
	}
	return metadata
}

func assertClusterMetadata(t *testing.T, metadata *clusterpb.ClusterMetadata, clusterID uint64, members map[uint64]string) {
	t.Helper()
	if metadata == nil || metadata.ClusterId != clusterID {
		t.Fatalf("cluster metadata = %v, want cluster ID %d", metadata, clusterID)
	}
	if len(metadata.Members) != len(members) {
		t.Fatalf("members = %v, want %v", metadata.Members, members)
	}
	for _, member := range metadata.Members {
		if address, exists := members[member.Id]; !exists || address != member.RaftAddress {
			t.Fatalf("unexpected member %v; want %v", member, members)
		}
	}
}
