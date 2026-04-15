package storage

import "github.com/Aetherance/kv/proto/pkg/kvrpcpb"

type Storage interface {
	Start() error
	Stop() error
	Write(ctx *kvrpcpb.Context, batch []Modify) error
	Read(ctx *kvrpcpb.Context) error
}
