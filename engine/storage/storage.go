package storage

import (
	util "github.com/Aetherance/kv/engine/util"
)

type Storage interface {
	Start() error
	Stop() error
	Write(batch []Modify) error
	Reader() (StorageReader, error)
}

// StorageReader is the minimal read-side abstraction for storage backends.
// Concrete read methods will be added with the raw KV API.
type StorageReader interface {
	GetCF(cf string, key []byte) ([]byte, error)
	IterCF(cf string) util.Iterator
	Close()
}
