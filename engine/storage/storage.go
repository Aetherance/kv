package storage

import (
	"context"

	util "github.com/Aetherance/kv/engine/util"
)

type Storage interface {
	Start() error
	Stop() error
	// Write atomically applies batch. Returning a context error means the caller
	// stopped waiting; a Raft-backed write may still commit after cancellation.
	Write(ctx context.Context, batch []Modify) error
	// Reader returns a consistent read view. Raft-backed implementations may use
	// ctx while waiting for a linearizable read barrier.
	Reader(ctx context.Context) (StorageReader, error)
}

// StorageReader is the minimal read-side abstraction for storage backends.
// Concrete read methods will be added with the raw KV API.
type StorageReader interface {
	GetCF(cf string, key []byte) ([]byte, error)
	IterCF(cf string) util.Iterator
	Close()
}
