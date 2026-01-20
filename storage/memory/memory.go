package memory

import (
	"sync"
)

type MemoryStorage struct {
	data map[string][]Version
	mu   sync.RWMutex
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		data: make(map[string][]Version),
	}
}

func (db *MemoryStorage) Get(key string, ts uint64) ([]byte, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	versions, ok := db.data[key]
	if !ok {
		return nil, false
	}

	for i := len(versions) - 1; i >= 0; i-- {
		version := versions[i]
		if version.CommitTs <= ts {
			if version.Deleted {
				return nil, false
			} else {
				return version.Value, true
			}
		}
	}
	return nil, false
}

func (db *MemoryStorage) Set(key string, value []byte, commitTs uint64) {
	db.mu.Lock()
	defer db.mu.Unlock()

	v := Version{
		CommitTs: commitTs,
		Value:    value,
		Deleted:  false,
	}
	db.data[key] = append(db.data[key], v)
}

func (db *MemoryStorage) Delete(key string, commitTs uint64) {
	db.mu.Lock()
	defer db.mu.Unlock()

	v := Version{
		CommitTs: commitTs,
		Deleted:  true,
	}
	db.data[key] = append(db.data[key], v)
}
