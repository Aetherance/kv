package util

import (
	"os"

	"github.com/Aetherance/kv/log"
	badger "github.com/dgraph-io/badger/v4"
)

// Engines keeps the Badger databases used by the storage layer.
type Engines struct {
	// Data, including data which is committed (i.e., committed across other nodes) and un-committed (i.e., only present
	// locally).
	Kv *badger.DB
	// Metadata used by Raft.
	Raft *badger.DB
}

func NewEngines(kvEngine, raftEngine *badger.DB) *Engines {
	return &Engines{
		Kv:   kvEngine,
		Raft: raftEngine,
	}
}

func (en *Engines) WriteKV(wb *WriteBatch) error {
	return wb.WriteToDB(en.Kv)
}

func (en *Engines) WriteRaft(wb *WriteBatch) error {
	return wb.WriteToDB(en.Raft)
}

// CreateDB creates a new Badger DB on disk at path.
func CreateDB(path string) *badger.DB {
	opts := badger.DefaultOptions(path)
	// Sync writes to disk so acknowledged raft log / apply state survive a crash.
	// badger v4 defaults SyncWrites to false; the raftstore requires durability
	// (in particular appliedIndex must never exceed the persisted lastIndex).
	// Note: tinykv set ValueThreshold=0 for the raft engine, but under badger v4
	// that routes every value into the value log, which is not crash-durable here,
	// so we keep the default value threshold.
	opts.SyncWrites = true
	if err := os.MkdirAll(path, os.ModePerm); err != nil {
		log.Fatal(err)
	}
	db, err := badger.Open(opts)
	if err != nil {
		log.Fatal(err)
	}
	return db
}
