package storage

import (
	util "github.com/Aetherance/kv/engine/util"
	"github.com/Aetherance/kv/proto/pkg/kvrpcpb"
)

type Storage interface {
	Start() error
	Stop() error
	Write(ctx *kvrpcpb.Context, batch []Modify) error
	Reader(ctx *kvrpcpb.Context) (StorageReader, error)
}

// StorageReader is the minimal read-side abstraction for storage backends.
// Concrete read methods will be added with the raw KV API.
type StorageReader interface {
	GetCF(cf string, key []byte) ([]byte, error)
	IterCF(cf string) util.Iterator
	Close()
}
