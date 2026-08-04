package raftruntime

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	badger "github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/proto/pkg/raftpb"
	"github.com/Aetherance/kv/raft"
)

var _ raft.Storage = (*BadgerEngine)(nil)

// BadgerEngine is a namespace-bound durable Raft log and state-machine
// checkpoint. It has no knowledge of what the namespace represents.
type BadgerEngine struct {
	db          *badger.DB
	prefix      []byte
	snapshotter Snapshotter

	hardState    *raftpb.HardState
	confState    *raftpb.ConfState
	snapshot     *raftpb.Snapshot
	appliedIndex uint64
	lastIndex    uint64
}

var (
	initializedSuffix = []byte("meta/initialized")
	hardStateSuffix   = []byte("meta/hard-state")
	confStateSuffix   = []byte("meta/conf-state")
	snapshotSuffix    = []byte("meta/snapshot")
	appliedSuffix     = []byte("meta/applied-index")
	lastIndexSuffix   = []byte("meta/last-index")
	logSuffix         = []byte("log/")
)

func OpenBadgerEngine(db *badger.DB, prefix []byte, initialPeers []uint64, snapshotter Snapshotter) (*BadgerEngine, error) {
	if db == nil {
		return nil, errors.New("raftruntime: nil badger database")
	}
	engine := &BadgerEngine{
		db:          db,
		prefix:      append([]byte(nil), prefix...),
		snapshotter: snapshotter,
	}
	if err := engine.loadOrBootstrap(initialPeers); err != nil {
		return nil, err
	}
	if engine.appliedIndex > engine.hardState.Commit {
		return nil, fmt.Errorf("raftruntime: applied index %d exceeds commit index %d", engine.appliedIndex, engine.hardState.Commit)
	}
	if engine.hardState.Commit > engine.lastIndex {
		return nil, fmt.Errorf("raftruntime: commit index %d exceeds last index %d", engine.hardState.Commit, engine.lastIndex)
	}
	return engine, nil
}

func (e *BadgerEngine) InitialState() (*raftpb.HardState, *raftpb.ConfState, error) {
	return cloneHardState(e.hardState), cloneConfState(e.confState), nil
}

func (e *BadgerEngine) Entries(low, high uint64) ([]*raftpb.Entry, error) {
	if low > high {
		return nil, fmt.Errorf("raftruntime: entries low %d exceeds high %d", low, high)
	}
	if low <= e.snapshot.Metadata.Index {
		return nil, raft.ErrCompacted
	}
	if high > e.lastIndex+1 {
		return nil, fmt.Errorf("raftruntime: entries high %d exceeds last index %d", high, e.lastIndex)
	}
	if low == high {
		return nil, nil
	}

	entries := make([]*raftpb.Entry, 0, high-low)
	err := e.db.View(func(txn *badger.Txn) error {
		for index := low; index < high; index++ {
			item, err := txn.Get(e.logKey(index))
			if err == badger.ErrKeyNotFound {
				return raft.ErrUnavailable
			}
			if err != nil {
				return err
			}
			value, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			entry := new(raftpb.Entry)
			if err := proto.Unmarshal(value, entry); err != nil {
				return err
			}
			entries = append(entries, entry)
		}
		return nil
	})
	return entries, err
}

func (e *BadgerEngine) Term(index uint64) (uint64, error) {
	snapshotIndex := e.snapshot.Metadata.Index
	if index < snapshotIndex {
		return 0, raft.ErrCompacted
	}
	if index == snapshotIndex {
		return e.snapshot.Metadata.Term, nil
	}
	if index > e.lastIndex {
		return 0, raft.ErrUnavailable
	}
	entries, err := e.Entries(index, index+1)
	if err != nil {
		return 0, err
	}
	return entries[0].Term, nil
}

func (e *BadgerEngine) LastIndex() (uint64, error) { return e.lastIndex, nil }

func (e *BadgerEngine) FirstIndex() (uint64, error) {
	return e.snapshot.Metadata.Index + 1, nil
}

func (e *BadgerEngine) Snapshot() (*raftpb.Snapshot, error) {
	if raft.IsEmptySnap(e.snapshot) {
		return nil, raft.ErrSnapshotTemporarilyUnavailable
	}
	return cloneSnapshot(e.snapshot), nil
}

func (e *BadgerEngine) AppliedIndex() uint64 { return e.appliedIndex }

// Persist saves all unstable Raft state before any dependent message is sent.
// Snapshot installation is included so a successful return is crash-recoverable.
func (e *BadgerEngine) Persist(ready *raft.Ready) error {
	if ready == nil {
		return errors.New("raftruntime: nil ready")
	}

	nextHard := cloneHardState(e.hardState)
	nextConf := cloneConfState(e.confState)
	nextSnapshot := cloneSnapshot(e.snapshot)
	nextApplied := e.appliedIndex
	nextLast := e.lastIndex

	err := e.db.Update(func(txn *badger.Txn) error {
		if !raft.IsEmptySnap(ready.Snapshot) {
			snapshot := ready.Snapshot
			if snapshot.Metadata.Index <= nextSnapshot.Metadata.Index {
				return raft.ErrSnapOutOfDate
			}
			if e.snapshotter.Restore == nil {
				return errors.New("raftruntime: received snapshot without a restore function")
			}
			if err := e.snapshotter.Restore(txn, snapshot.Data); err != nil {
				return err
			}
			if err := e.deleteAllLogs(txn); err != nil {
				return err
			}
			nextSnapshot = cloneSnapshot(snapshot)
			nextConf = cloneConfState(snapshot.Metadata.ConfState)
			nextApplied = snapshot.Metadata.Index
			nextLast = snapshot.Metadata.Index
			if err := setProto(txn, e.key(snapshotSuffix), nextSnapshot); err != nil {
				return err
			}
			if err := setProto(txn, e.key(confStateSuffix), nextConf); err != nil {
				return err
			}
			if err := setUint64(txn, e.key(appliedSuffix), nextApplied); err != nil {
				return err
			}
		}

		if len(ready.Entries) > 0 {
			firstNew := ready.Entries[0].Index
			for _, entry := range ready.Entries {
				if entry.Index <= nextSnapshot.Metadata.Index {
					continue
				}
				if err := setProto(txn, e.logKey(entry.Index), entry); err != nil {
					return err
				}
			}
			lastNew := ready.Entries[len(ready.Entries)-1].Index
			if lastNew > nextSnapshot.Metadata.Index {
				if firstNew <= nextLast && lastNew < nextLast {
					for index := lastNew + 1; index <= nextLast; index++ {
						if err := txn.Delete(e.logKey(index)); err != nil {
							return err
						}
					}
				}
				nextLast = lastNew
			}
		}

		if !raft.IsEmptyHardState(ready.HardState) {
			nextHard = cloneHardState(ready.HardState)
			if err := setProto(txn, e.key(hardStateSuffix), nextHard); err != nil {
				return err
			}
		}
		return setUint64(txn, e.key(lastIndexSuffix), nextLast)
	})
	if err != nil {
		return err
	}

	e.hardState = nextHard
	e.confState = nextConf
	e.snapshot = nextSnapshot
	e.appliedIndex = nextApplied
	e.lastIndex = nextLast
	return nil
}

func (e *BadgerEngine) Apply(index uint64, data []byte, apply ApplyFunc) ([]byte, error) {
	if err := e.checkNextApplied(index); err != nil {
		return nil, err
	}
	var result []byte
	err := e.db.Update(func(txn *badger.Txn) error {
		var err error
		result, err = apply(txn, index, data)
		if err != nil {
			return err
		}
		return setUint64(txn, e.key(appliedSuffix), index)
	})
	if err != nil {
		return nil, err
	}
	e.appliedIndex = index
	return result, nil
}

func (e *BadgerEngine) MarkApplied(index uint64) error {
	if err := e.checkNextApplied(index); err != nil {
		return err
	}
	if err := e.db.Update(func(txn *badger.Txn) error {
		return setUint64(txn, e.key(appliedSuffix), index)
	}); err != nil {
		return err
	}
	e.appliedIndex = index
	return nil
}

func (e *BadgerEngine) ApplyConfChange(index uint64, state *raftpb.ConfState) error {
	if err := e.checkNextApplied(index); err != nil {
		return err
	}
	state = cloneConfState(state)
	if err := e.db.Update(func(txn *badger.Txn) error {
		if err := setProto(txn, e.key(confStateSuffix), state); err != nil {
			return err
		}
		return setUint64(txn, e.key(appliedSuffix), index)
	}); err != nil {
		return err
	}
	e.confState = state
	e.appliedIndex = index
	return nil
}

func (e *BadgerEngine) MaybeCompact(threshold uint64) error {
	if threshold == 0 || e.snapshotter.Capture == nil {
		return nil
	}
	if e.appliedIndex <= e.snapshot.Metadata.Index || e.appliedIndex-e.snapshot.Metadata.Index < threshold {
		return nil
	}

	index := e.appliedIndex
	term, err := e.Term(index)
	if err != nil {
		return err
	}
	var data []byte
	if err := e.db.View(func(txn *badger.Txn) error {
		var captureErr error
		data, captureErr = e.snapshotter.Capture(txn)
		return captureErr
	}); err != nil {
		return err
	}

	snapshot := &raftpb.Snapshot{
		Data: data,
		Metadata: &raftpb.SnapshotMetadata{
			Index:     index,
			Term:      term,
			ConfState: cloneConfState(e.confState),
		},
	}
	if err := e.db.Update(func(txn *badger.Txn) error {
		if err := setProto(txn, e.key(snapshotSuffix), snapshot); err != nil {
			return err
		}
		for logIndex := e.snapshot.Metadata.Index + 1; logIndex <= index; logIndex++ {
			if err := txn.Delete(e.logKey(logIndex)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	e.snapshot = snapshot
	return nil
}

func (e *BadgerEngine) checkNextApplied(index uint64) error {
	if index != e.appliedIndex+1 {
		return fmt.Errorf("raftruntime: apply index %d is not after durable index %d", index, e.appliedIndex)
	}
	return nil
}

func (e *BadgerEngine) loadOrBootstrap(initialPeers []uint64) error {
	err := e.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(e.key(initializedSuffix))
		return err
	})
	if err == badger.ErrKeyNotFound {
		peers := append([]uint64(nil), initialPeers...)
		sort.Slice(peers, func(i, j int) bool { return peers[i] < peers[j] })
		for i, peer := range peers {
			if peer == 0 || (i > 0 && peer == peers[i-1]) {
				return fmt.Errorf("raftruntime: invalid initial peers %v", initialPeers)
			}
		}
		if len(peers) == 0 {
			return errors.New("raftruntime: initial peers are required for a new namespace")
		}

		e.hardState = &raftpb.HardState{}
		e.confState = &raftpb.ConfState{Nodes: peers}
		e.snapshot = &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{ConfState: cloneConfState(e.confState)}}
		return e.db.Update(func(txn *badger.Txn) error {
			if err := txn.Set(e.key(initializedSuffix), []byte{1}); err != nil {
				return err
			}
			if err := setProto(txn, e.key(hardStateSuffix), e.hardState); err != nil {
				return err
			}
			if err := setProto(txn, e.key(confStateSuffix), e.confState); err != nil {
				return err
			}
			if err := setProto(txn, e.key(snapshotSuffix), e.snapshot); err != nil {
				return err
			}
			if err := setUint64(txn, e.key(appliedSuffix), 0); err != nil {
				return err
			}
			return setUint64(txn, e.key(lastIndexSuffix), 0)
		})
	}
	if err != nil {
		return err
	}

	return e.db.View(func(txn *badger.Txn) error {
		e.hardState = new(raftpb.HardState)
		if err := getProto(txn, e.key(hardStateSuffix), e.hardState); err != nil {
			return err
		}
		e.confState = new(raftpb.ConfState)
		if err := getProto(txn, e.key(confStateSuffix), e.confState); err != nil {
			return err
		}
		e.snapshot = new(raftpb.Snapshot)
		if err := getProto(txn, e.key(snapshotSuffix), e.snapshot); err != nil {
			return err
		}
		var err error
		if e.appliedIndex, err = getUint64(txn, e.key(appliedSuffix)); err != nil {
			return err
		}
		e.lastIndex, err = getUint64(txn, e.key(lastIndexSuffix))
		return err
	})
}

func (e *BadgerEngine) deleteAllLogs(txn *badger.Txn) error {
	prefix := e.key(logSuffix)
	iterator := txn.NewIterator(badger.DefaultIteratorOptions)
	defer iterator.Close()
	for iterator.Seek(prefix); iterator.ValidForPrefix(prefix); iterator.Next() {
		if err := txn.Delete(iterator.Item().KeyCopy(nil)); err != nil {
			return err
		}
	}
	return nil
}

func (e *BadgerEngine) key(suffix []byte) []byte {
	key := make([]byte, 0, len(e.prefix)+len(suffix))
	key = append(key, e.prefix...)
	return append(key, suffix...)
}

func (e *BadgerEngine) logKey(index uint64) []byte {
	key := e.key(logSuffix)
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], index)
	return append(key, encoded[:]...)
}

func setProto(txn *badger.Txn, key []byte, message proto.Message) error {
	value, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	return txn.Set(key, value)
}

func getProto(txn *badger.Txn, key []byte, message proto.Message) error {
	item, err := txn.Get(key)
	if err != nil {
		return err
	}
	value, err := item.ValueCopy(nil)
	if err != nil {
		return err
	}
	return proto.Unmarshal(value, message)
}

func setUint64(txn *badger.Txn, key []byte, value uint64) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	return txn.Set(key, encoded[:])
}

func getUint64(txn *badger.Txn, key []byte) (uint64, error) {
	item, err := txn.Get(key)
	if err != nil {
		return 0, err
	}
	value, err := item.ValueCopy(nil)
	if err != nil {
		return 0, err
	}
	if len(value) != 8 {
		return 0, fmt.Errorf("raftruntime: invalid uint64 value %x", value)
	}
	return binary.BigEndian.Uint64(value), nil
}

func cloneHardState(state *raftpb.HardState) *raftpb.HardState {
	if state == nil {
		return &raftpb.HardState{}
	}
	return proto.Clone(state).(*raftpb.HardState)
}

func cloneConfState(state *raftpb.ConfState) *raftpb.ConfState {
	if state == nil {
		return &raftpb.ConfState{}
	}
	return proto.Clone(state).(*raftpb.ConfState)
}

func cloneSnapshot(snapshot *raftpb.Snapshot) *raftpb.Snapshot {
	if snapshot == nil {
		return &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{ConfState: &raftpb.ConfState{}}}
	}
	cloned := proto.Clone(snapshot).(*raftpb.Snapshot)
	if cloned.Metadata == nil {
		cloned.Metadata = &raftpb.SnapshotMetadata{}
	}
	if cloned.Metadata.ConfState == nil {
		cloned.Metadata.ConfState = &raftpb.ConfState{}
	}
	return cloned
}
