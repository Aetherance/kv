package coord

import (
	"context"

	"github.com/Aetherance/kv/common"
	"github.com/Aetherance/kv/engine"
)

type LocalCoordinator struct {
	kv *engine.KV
}

func NewLocal(kv *engine.KV) *LocalCoordinator {
	return &LocalCoordinator{
		kv: kv,
	}
}

func (lc *LocalCoordinator) Coordinator(ctx context.Context, req *common.Request) *common.Response {
	switch req.Op {
	case common.OpGet:
		val, err := lc.kv.Get(req.Key)
		return &common.Response{Data: val, Err: err}
	case common.OpSet:
		err := lc.kv.Set(req.Key, req.Value)
		return &common.Response{Err: err}
	case common.OpDel:
		err := lc.kv.Del(req.Key)
		return &common.Response{Err: err}
	default:
		return &common.Response{Err: common.ErrCoordErrUnknownOp}
	}
}
