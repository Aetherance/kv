// Package raftruntime provides the smallest durable runtime around a RawNode.
// It deliberately has no concept of stores, regions, shards, or raft groups.
package raftruntime

import (
	"errors"
	"fmt"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/Aetherance/kv/proto/pkg/raftpb"
	"github.com/Aetherance/kv/raft"
)

// ApplyFunc applies one committed normal entry inside the transaction that also
// advances the durable applied index.
type ApplyFunc func(txn *badger.Txn, index uint64, data []byte) ([]byte, error)

// SendFunc hands already-persisted outbound messages to the network layer.
// Implementations should enqueue messages quickly; Raft will retry lost traffic.
type SendFunc func(messages []*raftpb.Message)

// AppliedFunc observes normal entries after their state-machine changes are
// durable. Request correlation belongs to the caller, not to the runtime.
type AppliedFunc func(entries []Applied)

// StateFunc observes volatile leadership changes.
type StateFunc func(state raft.SoftState)

// Snapshotter binds the application state to the same Badger transaction used
// for snapshot installation. Capture must return a consistent representation
// of the state visible through txn; Restore must replace that state.
type Snapshotter struct {
	Capture func(txn *badger.Txn) ([]byte, error)
	Restore func(txn *badger.Txn, data []byte) error
}

type Applied struct {
	Index  uint64
	Type   raftpb.EntryType
	Data   []byte
	Result []byte
}

type Config struct {
	CompactThreshold uint64
	Send             SendFunc
	Applied          AppliedFunc
	StateChanged     StateFunc
}

type NotLeaderError struct {
	LeaderID uint64
}

func (e *NotLeaderError) Error() string {
	if e.LeaderID == 0 {
		return "raftruntime: not leader"
	}
	return fmt.Sprintf("raftruntime: not leader; leader=%d", e.LeaderID)
}

// FatalError means durable state can no longer be advanced safely. A scheduler
// must stop the Runtime instead of processing later events.
type FatalError struct {
	Err error
}

func (e *FatalError) Error() string { return e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }

func IsFatal(err error) bool {
	var fatal *FatalError
	return errors.As(err, &fatal)
}
