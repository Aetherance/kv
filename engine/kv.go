package engine

import "github.com/Aetherance/kv/storage"

type KV struct {
	storage storage.StorageEngine
	tso     *TimestampOracle
}

func (kv *KV) Get(key []byte) ([]byte, error)
func (kv *KV) Set(key, val []byte) error
func (kv *KV) BeginTxn() Transaction
