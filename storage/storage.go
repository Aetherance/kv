package storage

type Storage interface {
	Get(key string, ts uint64) ([]byte, bool)
	Set(key string, value []byte, commitTs uint64)
	Delete(key string, commitTs uint64)
}
