package engine

import (
	"github.com/Aetherance/kv/common"
	"github.com/Aetherance/kv/storage"
)

type KV struct {
	storage storage.Storage
	tso     *Tso
}

func New(s storage.Storage) *KV {
	return &KV{
		storage: s,
		tso:     NewTSO(),
	}
}

func (kv *KV) Get(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, common.ErrEngineEmptyKey
	}

	commitTs := kv.tso.GetNextTs()
	val, ok := kv.storage.Get(string(key), commitTs)

	if !ok {
		return nil, common.ErrEngineKeyNotFound
	} else {
		return val, nil
	}
}

func (kv *KV) Set(key, val []byte) error {
	if len(key) == 0 {
		return common.ErrEngineEmptyKey
	}

	commitTs := kv.tso.GetNextTs()
	kv.storage.Set(string(key), val, commitTs)

	return nil
}

func (kv *KV) Del(key []byte) error {
	if len(key) == 0 {
		return common.ErrEngineEmptyKey
	}

	commitTs := kv.tso.GetNextTs()
	kv.storage.Delete(string(key), commitTs)
	return nil
}

func (kv *KV) BeginTxn() Transaction {
	return nil
}
