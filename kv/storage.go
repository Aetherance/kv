package kv

type StorageEngine interface {
	Get(key string, ts uint64) ([]byte,bool)
	Put(key string, value []byte, commitTs uint64)
	Delete(key string, commitTs uint64)
}