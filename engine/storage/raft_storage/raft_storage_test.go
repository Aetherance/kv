package raft_storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage"
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
