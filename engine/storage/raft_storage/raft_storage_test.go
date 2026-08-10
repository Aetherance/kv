package raft_storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage"
	"github.com/Aetherance/kv/proto/pkg/raft_cmdpb"
	"github.com/Aetherance/kv/raft"
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
	lastIndexBeforeRead := first.state.lastIndex
	assertStoredValue(t, first, "value")
	if first.state.lastIndex != lastIndexBeforeRead {
		t.Fatalf("read grew raft log from %d to %d", lastIndexBeforeRead, first.state.lastIndex)
	}
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

	if _, err := store.Reader(context.Background()); err == nil {
		t.Fatal("reader without a known leader succeeded")
	} else if !errors.As(err, &notLeader) {
		t.Fatalf("reader error = %v, want NotLeaderError", err)
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

func TestReadIndexWaitHonorsContext(t *testing.T) {
	store := &RaftStorage{
		config:      &config.Config{StoreID: 1},
		inbox:       make(chan raftEvent),
		done:        make(chan struct{}),
		readPending: make(map[string]*pendingRead),
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
		_, err := store.Reader(ctx)
		result <- err
	}()

	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("read-index request was not accepted")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("reader error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not return after cancellation")
	}

	store.readPendingMu.Lock()
	pending := len(store.readPending)
	store.readPendingMu.Unlock()
	if pending != 0 {
		t.Fatalf("canceled read left %d pending requests", pending)
	}
}

func TestReadIndexWaitsForLocalApply(t *testing.T) {
	context := []byte("read-context")
	resultCh := make(chan error, 1)
	store := &RaftStorage{
		state: &raftStatePersistence{appliedIndex: 4},
		readPending: map[string]*pendingRead{
			string(context): {resultCh: resultCh, leaderID: 2},
		},
	}

	store.onReadState(raft.ReadState{Index: 5, RequestCtx: context})
	select {
	case err := <-resultCh:
		t.Fatalf("read completed before local apply: %v", err)
	default:
	}

	store.state.appliedIndex = 5
	store.completeAppliedReads()
	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("completed read error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not complete after local apply")
	}
}

func TestReadIndexFailsWhenLeaderChanges(t *testing.T) {
	resultCh := make(chan error, 1)
	store := &RaftStorage{
		readPending: map[string]*pendingRead{
			"request": {resultCh: resultCh, leaderID: 2},
		},
	}
	store.failReadsForLeaderChange(3)
	select {
	case err := <-resultCh:
		if !errors.Is(err, errLeadershipLost) {
			t.Fatalf("read error = %v, want leadership lost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("leader change did not fail pending read")
	}
}

func TestLegacyReadBarrierRemainsReplayable(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.StoreID = 1
	cfg.Peers = map[uint64]string{1: "127.0.0.1:1"}
	cfg.DBPath = t.TempDir()
	store := NewRaftStorage(cfg)
	if err := store.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})

	err := store.propose(context.Background(), &raft_cmdpb.RaftCmdRequest{
		Requests: []*raft_cmdpb.Request{{CmdType: raft_cmdpb.CmdType_ReadBarrier}},
	})
	if err != nil {
		t.Fatalf("apply legacy ReadBarrier: %v", err)
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
