package raft_storage

import (
	"encoding/binary"
	"errors"
	"fmt"

	badger "github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
	"github.com/Aetherance/kv/raft"
)

var _ raft.PersistentState = (*raftStatePersistence)(nil)

type applyFunc func(txn *badger.Txn, index uint64, data []byte) error

// raftStatePersistence keeps the durable state for this Raft-backed KV.
type raftStatePersistence struct {
	db *badger.DB

	hardState    *raftpb.HardState
	confState    *raftpb.ConfState
	cluster      *clusterpb.ClusterMetadata
	snapshot     *raftpb.Snapshot
	appliedIndex uint64
	lastIndex    uint64
}

var (
	initializedSuffix = []byte("meta/initialized")
	hardStateSuffix   = []byte("meta/hard-state")
	confStateSuffix   = []byte("meta/conf-state")
	clusterSuffix     = []byte("meta/cluster")
	snapshotSuffix    = []byte("meta/snapshot")
	appliedSuffix     = []byte("meta/applied-index")
	lastIndexSuffix   = []byte("meta/last-index")
	logSuffix         = []byte("log/")
)

func openRaftStatePersistence(db *badger.DB, initialCluster *clusterpb.ClusterMetadata) (*raftStatePersistence, error) {
	if db == nil {
		return nil, errors.New("raft storage: nil badger database")
	}
	state := &raftStatePersistence{db: db}
	if err := state.loadOrBootstrap(initialCluster); err != nil {
		return nil, err
	}
	if state.appliedIndex > state.hardState.Commit {
		return nil, fmt.Errorf("raft storage: applied index %d exceeds commit index %d", state.appliedIndex, state.hardState.Commit)
	}
	if state.hardState.Commit > state.lastIndex {
		return nil, fmt.Errorf("raft storage: commit index %d exceeds last index %d", state.hardState.Commit, state.lastIndex)
	}
	return state, nil
}

func (e *raftStatePersistence) InitialState() (*raftpb.HardState, *raftpb.ConfState, error) {
	return cloneHardState(e.hardState), cloneConfState(e.confState), nil
}

func (e *raftStatePersistence) Entries(low, high uint64) ([]*raftpb.Entry, error) {
	if low > high {
		return nil, fmt.Errorf("raft storage: entries low %d exceeds high %d", low, high)
	}
	if low <= e.snapshot.Metadata.Index {
		return nil, raft.ErrCompacted
	}
	if high > e.lastIndex+1 {
		return nil, fmt.Errorf("raft storage: entries high %d exceeds last index %d", high, e.lastIndex)
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

func (e *raftStatePersistence) Term(index uint64) (uint64, error) {
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

func (e *raftStatePersistence) LastIndex() (uint64, error) { return e.lastIndex, nil }

func (e *raftStatePersistence) FirstIndex() (uint64, error) {
	return e.snapshot.Metadata.Index + 1, nil
}

func (e *raftStatePersistence) Snapshot() (*raftpb.Snapshot, error) {
	if raft.IsEmptySnap(e.snapshot) {
		return nil, raft.ErrSnapshotTemporarilyUnavailable
	}
	return cloneSnapshot(e.snapshot), nil
}

func (e *raftStatePersistence) applied() uint64 { return e.appliedIndex }

// persist saves all unstable Raft state before any dependent message is sent.
// Snapshot installation is included so a successful return is crash-recoverable.
func (e *raftStatePersistence) persist(ready *raft.Ready) error {
	if ready == nil {
		return errors.New("raft storage: nil ready")
	}

	nextHard := cloneHardState(e.hardState)
	nextConf := cloneConfState(e.confState)
	nextCluster := cloneClusterMetadata(e.cluster)
	nextSnapshot := cloneSnapshot(e.snapshot)
	nextApplied := e.appliedIndex
	nextLast := e.lastIndex

	err := e.db.Update(func(txn *badger.Txn) error {
		if !raft.IsEmptySnap(ready.Snapshot) {
			snapshot := ready.Snapshot
			if snapshot.Metadata.Index <= nextSnapshot.Metadata.Index {
				return raft.ErrSnapOutOfDate
			}
			restoredCluster, err := restoreKVSnapshot(txn, snapshot.Data)
			if err != nil {
				return err
			}
			if err := validateClusterMetadata(restoredCluster); err != nil {
				return fmt.Errorf("raft storage: invalid snapshot cluster metadata: %w", err)
			}
			if err := e.deleteAllLogs(txn); err != nil {
				return err
			}
			nextSnapshot = cloneSnapshot(snapshot)
			nextConf = cloneConfState(snapshot.Metadata.ConfState)
			nextCluster = restoredCluster
			nextApplied = snapshot.Metadata.Index
			nextLast = snapshot.Metadata.Index
			if err := setProto(txn, e.key(snapshotSuffix), nextSnapshot); err != nil {
				return err
			}
			if err := setProto(txn, e.key(confStateSuffix), nextConf); err != nil {
				return err
			}
			if err := setProto(txn, e.key(clusterSuffix), nextCluster); err != nil {
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
	e.cluster = nextCluster
	e.snapshot = nextSnapshot
	e.appliedIndex = nextApplied
	e.lastIndex = nextLast
	return nil
}

func (e *raftStatePersistence) apply(index uint64, data []byte, apply applyFunc) error {
	if err := e.checkNextApplied(index); err != nil {
		return err
	}
	err := e.db.Update(func(txn *badger.Txn) error {
		if err := apply(txn, index, data); err != nil {
			return err
		}
		return setUint64(txn, e.key(appliedSuffix), index)
	})
	if err != nil {
		return err
	}
	e.appliedIndex = index
	return nil
}

func (e *raftStatePersistence) markApplied(index uint64) error {
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

func (e *raftStatePersistence) applyConfChange(index uint64, state *raftpb.ConfState) error {
	return e.applyMembership(index, state, e.cluster)
}

// applyMembership advances ConfState and its application-level member registry
// atomically at the same committed log index.
func (e *raftStatePersistence) applyMembership(index uint64, state *raftpb.ConfState, cluster *clusterpb.ClusterMetadata) error {
	if err := e.checkNextApplied(index); err != nil {
		return err
	}
	state = cloneConfState(state)
	cluster = cloneClusterMetadata(cluster)
	normalizeClusterMetadata(cluster)
	if err := validateClusterMetadata(cluster); err != nil {
		return err
	}
	if err := e.db.Update(func(txn *badger.Txn) error {
		if err := setProto(txn, e.key(confStateSuffix), state); err != nil {
			return err
		}
		if err := setProto(txn, e.key(clusterSuffix), cluster); err != nil {
			return err
		}
		return setUint64(txn, e.key(appliedSuffix), index)
	}); err != nil {
		return err
	}
	e.confState = state
	e.cluster = cluster
	e.appliedIndex = index
	return nil
}

func (e *raftStatePersistence) maybeCompact(threshold uint64) error {
	if threshold == 0 {
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
		data, captureErr = captureKVSnapshot(txn, e.cluster)
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

func (e *raftStatePersistence) checkNextApplied(index uint64) error {
	if index != e.appliedIndex+1 {
		return fmt.Errorf("raft storage: apply index %d is not after durable index %d", index, e.appliedIndex)
	}
	return nil
}

func (e *raftStatePersistence) loadOrBootstrap(initialCluster *clusterpb.ClusterMetadata) error {
	err := e.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(e.key(initializedSuffix))
		return err
	})
	if err == badger.ErrKeyNotFound {
		if err := validateClusterMetadata(initialCluster); err != nil {
			return err
		}

		e.hardState = &raftpb.HardState{}
		e.cluster = cloneClusterMetadata(initialCluster)
		e.confState = &raftpb.ConfState{}
		for _, member := range e.cluster.Members {
			e.confState.Voters = append(e.confState.Voters, member.Id)
		}
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
			if err := setProto(txn, e.key(clusterSuffix), e.cluster); err != nil {
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

	err = e.db.View(func(txn *badger.Txn) error {
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
	if err != nil {
		return err
	}

	// Migrate databases created before ClusterMetadata existed. ConfState stays
	// authoritative for voting roles; the bootstrap configuration supplies the
	// address registry exactly once.
	e.cluster = new(clusterpb.ClusterMetadata)
	err = e.db.View(func(txn *badger.Txn) error {
		return getProto(txn, e.key(clusterSuffix), e.cluster)
	})
	if err == badger.ErrKeyNotFound {
		if err := validateClusterMetadata(initialCluster); err != nil {
			return fmt.Errorf("raft storage: migrate cluster metadata: %w", err)
		}
		e.cluster = cloneClusterMetadata(initialCluster)
		return e.db.Update(func(txn *badger.Txn) error {
			return setProto(txn, e.key(clusterSuffix), e.cluster)
		})
	}
	if err != nil {
		return err
	}
	return validateClusterMetadata(e.cluster)
}

func (e *raftStatePersistence) deleteAllLogs(txn *badger.Txn) error {
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

func (e *raftStatePersistence) key(suffix []byte) []byte {
	key := make([]byte, 0, len(raftNamespace)+len(suffix))
	key = append(key, raftNamespace...)
	return append(key, suffix...)
}

func (e *raftStatePersistence) logKey(index uint64) []byte {
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
		return 0, fmt.Errorf("raft storage: invalid uint64 value %x", value)
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
