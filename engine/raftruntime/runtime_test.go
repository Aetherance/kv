package raftruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aetherance/kv/proto/pkg/raftpb"
	"github.com/Aetherance/kv/raft"
)

func TestSingleNodePersistsAndRestartsAtAppliedIndex(t *testing.T) {
	db := openTestDB(t)
	engine, err := OpenBadgerEngine(db, []byte("test/"), []uint64{1}, Snapshotter{})
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	node, err := raft.NewRawNode(&raft.Config{
		ID: 1, ElectionTick: 10, HeartbeatTick: 1,
		Storage: engine, Applied: engine.AppliedIndex(),
	})
	if err != nil {
		t.Fatalf("new raw node: %v", err)
	}

	var observed []Applied
	runtime := New(node, engine, func(txn *badger.Txn, _ uint64, data []byte) ([]byte, error) {
		if err := txn.Set([]byte("state"), data); err != nil {
			return nil, err
		}
		return append([]byte("applied:"), data...), nil
	}, Config{Applied: func(entries []Applied) {
		observed = append(observed, entries...)
	}})

	if err := runtime.Campaign(); err != nil {
		t.Fatalf("campaign: %v", err)
	}
	if err := runtime.Propose([]byte("value")); err != nil {
		t.Fatalf("propose: %v", err)
	}
	if len(observed) != 1 || string(observed[0].Result) != "applied:value" {
		t.Fatalf("unexpected applied events: %+v", observed)
	}
	if engine.AppliedIndex() != observed[0].Index {
		t.Fatalf("durable applied index %d, event index %d", engine.AppliedIndex(), observed[0].Index)
	}

	if err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("state"))
		if err != nil {
			return err
		}
		value, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		if string(value) != "value" {
			t.Fatalf("state value %q", value)
		}
		return nil
	}); err != nil {
		t.Fatalf("read state: %v", err)
	}

	reopened, err := OpenBadgerEngine(db, []byte("test/"), []uint64{99}, Snapshotter{})
	if err != nil {
		t.Fatalf("reopen engine: %v", err)
	}
	restarted, err := raft.NewRawNode(&raft.Config{
		ID: 1, ElectionTick: 10, HeartbeatTick: 1,
		Storage: reopened, Applied: reopened.AppliedIndex(),
	})
	if err != nil {
		t.Fatalf("restart raw node: %v", err)
	}
	if restarted.HasReady() {
		t.Fatal("restart exposed already-applied entries")
	}
	wantTerm, err := reopened.Term(reopened.AppliedIndex())
	if err != nil {
		t.Fatalf("durable applied term: %v", err)
	}
	gotTerm, err := restarted.Raft.RaftLog.Term(reopened.AppliedIndex())
	if err != nil {
		t.Fatalf("restarted applied term: %v", err)
	}
	if gotTerm != wantTerm {
		t.Fatalf("restarted applied term %d, expected %d", gotTerm, wantTerm)
	}
	_, confState, err := reopened.InitialState()
	if err != nil {
		t.Fatalf("initial state: %v", err)
	}
	if len(confState.Nodes) != 1 || confState.Nodes[0] != 1 {
		t.Fatalf("persisted configuration changed on reopen: %v", confState.Nodes)
	}
}

func TestRunnerKeepsRunningAfterRejectedProposal(t *testing.T) {
	db := openTestDB(t)
	engine, err := OpenBadgerEngine(db, []byte("runner/"), []uint64{1, 2}, Snapshotter{})
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	node, err := raft.NewRawNode(&raft.Config{
		ID: 1, ElectionTick: 10, HeartbeatTick: 1,
		Storage: engine, Applied: engine.AppliedIndex(),
	})
	if err != nil {
		t.Fatalf("new raw node: %v", err)
	}
	runner := NewRunner(New(node, engine, nil, Config{}), time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- runner.Run(ctx) }()

	err = runner.Propose(context.Background(), []byte("not-leader"))
	var notLeader *NotLeaderError
	if !errors.As(err, &notLeader) {
		t.Fatalf("expected NotLeaderError, got %v", err)
	}
	select {
	case <-runner.Done():
		t.Fatal("runner stopped after a non-fatal rejected proposal")
	default:
	}

	cancel()
	if err := <-runResult; err != nil {
		t.Fatalf("runner shutdown: %v", err)
	}
}

func TestLaggingReplicaRestoresSnapshot(t *testing.T) {
	type queuedMessage struct {
		message *raftpb.Message
	}
	var queue []queuedMessage
	runtimes := make(map[uint64]*Runtime)
	engines := make(map[uint64]*BadgerEngine)

	snapshotter := Snapshotter{
		Capture: func(txn *badger.Txn) ([]byte, error) {
			item, err := txn.Get([]byte("state"))
			if err == badger.ErrKeyNotFound {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			return item.ValueCopy(nil)
		},
		Restore: func(txn *badger.Txn, data []byte) error {
			if len(data) == 0 {
				return txn.Delete([]byte("state"))
			}
			return txn.Set([]byte("state"), data)
		},
	}

	for id := uint64(1); id <= 3; id++ {
		db := openTestDB(t)
		engine, err := OpenBadgerEngine(db, []byte("snapshot/"), []uint64{1, 2, 3}, snapshotter)
		if err != nil {
			t.Fatalf("open engine %d: %v", id, err)
		}
		node, err := raft.NewRawNode(&raft.Config{
			ID: id, ElectionTick: 10, HeartbeatTick: 1,
			Storage: engine, Applied: engine.AppliedIndex(),
		})
		if err != nil {
			t.Fatalf("new node %d: %v", id, err)
		}
		engines[id] = engine
		runtimes[id] = New(node, engine, func(txn *badger.Txn, _ uint64, data []byte) ([]byte, error) {
			return nil, txn.Set([]byte("state"), data)
		}, Config{
			CompactThreshold: 1,
			Send: func(messages []*raftpb.Message) {
				for _, message := range messages {
					queue = append(queue, queuedMessage{message: message})
				}
			},
		})
	}

	pump := func(dropNode3 bool) {
		t.Helper()
		for steps := 0; len(queue) > 0; steps++ {
			if steps > 1000 {
				t.Fatal("raft message pump did not quiesce")
			}
			message := queue[0].message
			queue = queue[1:]
			if dropNode3 && (message.To == 3 || message.From == 3) {
				continue
			}
			if err := runtimes[message.To].Step(message); err != nil {
				t.Fatalf("deliver %s from %d to %d: %v", message.MsgType, message.From, message.To, err)
			}
		}
	}

	if err := runtimes[1].Campaign(); err != nil {
		t.Fatalf("campaign: %v", err)
	}
	pump(true)
	if err := runtimes[1].Propose([]byte("snapshot-value")); err != nil {
		t.Fatalf("propose: %v", err)
	}
	pump(true)

	if engines[3].AppliedIndex() != 0 {
		t.Fatalf("isolated replica unexpectedly advanced to %d", engines[3].AppliedIndex())
	}
	if err := runtimes[1].Tick(); err != nil {
		t.Fatalf("leader tick: %v", err)
	}
	pump(false)

	if engines[3].AppliedIndex() != engines[1].AppliedIndex() {
		t.Fatalf("snapshot applied index %d, leader index %d", engines[3].AppliedIndex(), engines[1].AppliedIndex())
	}
	if err := engines[3].db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("state"))
		if err != nil {
			return err
		}
		value, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		if string(value) != "snapshot-value" {
			t.Fatalf("restored state %q", value)
		}
		return nil
	}); err != nil {
		t.Fatalf("read restored state: %v", err)
	}
}

func openTestDB(t *testing.T) *badger.DB {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions(t.TempDir()).WithLogger(nil).WithSyncWrites(true))
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close badger: %v", err)
		}
	})
	return db
}
