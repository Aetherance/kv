package kv

type KV struct {
	storage StorageEngine
	ts TimestampOracle
}

func (kv * KV) Get(key []byte) ([]byte,error)
func (kv * KV) Put(key,val []byte) error
func (kv * KV) BeginTxn() Transaction