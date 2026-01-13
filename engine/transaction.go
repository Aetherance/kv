package engine

type Transaction interface {
	Get(key []byte) ([]byte, error)
	Set(key []byte, val []byte)
	Commit() error
	Rollback() error
}
