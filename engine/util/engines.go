package util

import (
	"os"

	"github.com/Aetherance/kv/log"
	badger "github.com/dgraph-io/badger/v4"
)

// Engines keeps references to and data for the engines used by the storage layer.
// All engines are badger key/value databases.
// The Path fields are the filesystem path to where the data is stored.
type Engines struct {
	// Data, including data which is committed (i.e., committed across other nodes) and un-committed (i.e., only present
	// locally).
	Kv     *badger.DB
	KvPath string
	// Metadata used by Raft.
	Raft     *badger.DB
	RaftPath string
}

func NewEngines(kvEngine, raftEngine *badger.DB, kvPath, raftPath string) *Engines {
	return &Engines{
		Kv:       kvEngine,
		KvPath:   kvPath,
		Raft:     raftEngine,
		RaftPath: raftPath,
	}
}

func (en *Engines) WriteKV(wb *WriteBatch) error {
	return wb.WriteToDB(en.Kv)
}

func (en *Engines) WriteRaft(wb *WriteBatch) error {
	return wb.WriteToDB(en.Raft)
}

func (en *Engines) Close() error {
	dbs := []*badger.DB{en.Kv, en.Raft}
	for _, db := range dbs {
		if db == nil {
			continue
		}
		if err := db.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (en *Engines) Destroy() error {
	if err := en.Close(); err != nil {
		return err
	}
	if err := os.RemoveAll(en.KvPath); err != nil {
		return err
	}
	if err := os.RemoveAll(en.RaftPath); err != nil {
		return err
	}
	return nil
}

// CreateDB creates a new Badger DB on disk at path.
func CreateDB(path string, raft bool) *badger.DB {
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
