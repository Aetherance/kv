package raft_storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage"
	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestSingleNodeWriteReadAndRestart(t *testing.T) {
	directory := t.TempDir()
	newConfig := func() *config.Config {
		cfg := config.NewDefaultConfig()
		cfg.StoreID = 1
		cfg.Peers = map[uint64]string{1: "127.0.0.1:1"}
		cfg.DBPath = directory
		cfg.RaftLogGcCountLimit = 2
		return cfg
	}

	first := NewRaftStorage(newConfig())
	if err := first.Start(); err != nil {
		t.Fatalf("start first instance: %v", err)
	}
	if err := first.Write(context.Background(), []storage.Modify{{Data: storage.Put{
		Cf: "default", Key: []byte("key"), Val: []byte("value"),
	}}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	assertStoredValue(t, first, "value")
	if first.state.snapshot.Metadata.Index == 0 {
		t.Fatal("expected low compaction threshold to create a snapshot")
	}
	if err := first.Stop(); err != nil {
		t.Fatalf("stop first instance: %v", err)
	}

	second := NewRaftStorage(newConfig())
	if err := second.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Stop(); err != nil {
			t.Errorf("stop restarted instance: %v", err)
		}
	})
	assertStoredValue(t, second, "value")
}

func TestLearnerMembershipPersistsAcrossRestart(t *testing.T) {
	directory := t.TempDir()
	newConfig := func() *config.Config {
		cfg := config.NewDefaultConfig()
		cfg.StoreID = 1
		cfg.ClusterID = 42
		cfg.Peers = map[uint64]string{1: "127.0.0.1:1"}
		cfg.DBPath = directory
		cfg.RaftBaseTickInterval = time.Hour
		cfg.RaftLogGcCountLimit = 1
		return cfg
	}

	first := NewRaftStorage(newConfig())
	if err := first.Start(); err != nil {
		t.Fatalf("start first instance: %v", err)
	}
	response, err := first.MemberAdd(context.Background(), &clusterpb.MemberAddRequest{
		Id: 2, RaftAddress: "127.0.0.1:2", Learner: true,
	})
	if err != nil {
		t.Fatalf("add learner: %v", err)
	}
	if response.Cluster.ConfRevision != 1 || len(response.Cluster.Members) != 2 {
		t.Fatalf("unexpected member add response: %+v", response.Cluster)
	}
	if first.state.snapshot.Metadata.Index == 0 {
		t.Fatal("adding a learner did not force a snapshot")
	}
	assertSnapshotCluster(t, first.state.snapshot, 42, 1, 2, nil)
	joinInfo, err := first.JoinInfo(context.Background(), &clusterpb.JoinInfoRequest{Id: 2, RaftAddress: "127.0.0.1:2"})
	if err != nil {
		t.Fatalf("join info: %v", err)
	}
	if joinInfo.ClusterId != 42 || len(joinInfo.Members) != 2 {
		t.Fatalf("unexpected join info: %+v", joinInfo)
	}
	// An exact retry is idempotent and does not append another configuration.
	response, err = first.MemberAdd(context.Background(), &clusterpb.MemberAddRequest{
		Id: 2, RaftAddress: "127.0.0.1:2", Learner: true,
	})
	if err != nil {
		t.Fatalf("retry learner add: %v", err)
	}
	if response.Cluster.ConfRevision != 1 {
		t.Fatalf("idempotent retry changed revision to %d", response.Cluster.ConfRevision)
	}
	if err := first.Stop(); err != nil {
		t.Fatalf("stop first instance: %v", err)
	}

	second := NewRaftStorage(newConfig())
	if err := second.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = second.Stop() })
	list, err := second.MemberList(context.Background(), &clusterpb.MemberListRequest{})
	if err != nil {
		t.Fatalf("list after restart: %v", err)
	}
	if list.ClusterId != 42 || list.ConfRevision != 1 || len(list.Members) != 2 {
		t.Fatalf("membership not restored: %+v", list)
	}
	if list.Members[1].Role != clusterpb.MemberRole_MemberRoleLearner {
		t.Fatalf("member 2 role = %s, want learner", list.Members[1].Role)
	}

	if _, err := second.MemberRemove(context.Background(), &clusterpb.MemberRemoveRequest{Id: 2}); err != nil {
		t.Fatalf("remove learner: %v", err)
	}
	assertSnapshotCluster(t, second.state.snapshot, 42, 2, 1, []uint64{2})
	_, err = second.MemberAdd(context.Background(), &clusterpb.MemberAddRequest{
		Id: 2, RaftAddress: "127.0.0.1:2", Learner: true,
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("reusing removed ID error = %v, want AlreadyExists", err)
	}
}

func assertSnapshotCluster(t *testing.T, snapshot interface{ GetData() []byte }, clusterID, revision uint64, members int, removed []uint64) {
	t.Helper()
	data := new(rspb.RaftSnapshotData)
	if err := proto.Unmarshal(snapshot.GetData(), data); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if data.Cluster == nil || data.Cluster.ClusterId != clusterID || data.Cluster.ConfRevision != revision || len(data.Cluster.Members) != members {
		t.Fatalf("unexpected snapshot cluster metadata: %+v", data.Cluster)
	}
	if len(data.Cluster.RemovedMemberIds) != len(removed) {
		t.Fatalf("snapshot removed IDs = %v, want %v", data.Cluster.RemovedMemberIds, removed)
	}
	for index := range removed {
		if data.Cluster.RemovedMemberIds[index] != removed[index] {
			t.Fatalf("snapshot removed IDs = %v, want %v", data.Cluster.RemovedMemberIds, removed)
		}
	}
}

func TestRejectedProposalDoesNotStopEventLoop(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.StoreID = 1
	cfg.Peers = map[uint64]string{
		1: "127.0.0.1:1",
		2: "127.0.0.1:2",
	}
	cfg.DBPath = t.TempDir()
	cfg.RaftBaseTickInterval = time.Hour

	store := NewRaftStorage(cfg)
	if err := store.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})

	err := store.Write(context.Background(), []storage.Modify{{Data: storage.Put{
		Cf: "default", Key: []byte("key"), Val: []byte("value"),
	}}})
	var notLeader *NotLeaderError
	if !errors.As(err, &notLeader) {
		t.Fatalf("expected NotLeaderError, got %v", err)
	}
	select {
	case <-store.done:
		t.Fatal("event loop stopped after rejecting a proposal")
	default:
	}
	store.pendingMu.Lock()
	pending := len(store.pending)
	store.pendingMu.Unlock()
	if pending != 0 {
		t.Fatalf("rejected proposal left %d pending requests", pending)
	}
}

func TestProposalWaitHonorsContext(t *testing.T) {
	store := &RaftStorage{
		config:  &config.Config{StoreID: 1},
		inbox:   make(chan raftEvent),
		done:    make(chan struct{}),
		pending: make(map[uint64]chan error),
	}

	accepted := make(chan struct{})
	go func() {
		event := <-store.inbox
		event.done <- nil
		close(accepted)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- store.Write(ctx, []storage.Modify{{Data: storage.Put{
			Cf: "default", Key: []byte("key"), Val: []byte("value"),
		}}})
	}()

	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("proposal was not accepted")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write did not return after cancellation")
	}

	store.pendingMu.Lock()
	pending := len(store.pending)
	store.pendingMu.Unlock()
	if pending != 0 {
		t.Fatalf("canceled proposal left %d pending requests", pending)
	}
}

func assertStoredValue(t *testing.T, store *RaftStorage, expected string) {
	t.Helper()
	reader, err := store.Reader(context.Background())
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer reader.Close()
	value, err := reader.GetCF("default", []byte("key"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(value) != expected {
		t.Fatalf("value %q, expected %q", value, expected)
	}
}
