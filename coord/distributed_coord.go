package coord

import (
	"context"
	"sync"
	"time"

	"github.com/Aetherance/kv/common"
	"github.com/Aetherance/kv/engine"
)

type DistributedConfig struct {
	NodeID      int
	PeerAddrs   []string
	ListenAddr  string
	PersistPath string
}

type DistributedCoordinator struct {
	kv      *engine.KV
	raft    *Raft
	applyCh chan ApplyMsg

	mu        sync.Mutex
	applyCond *sync.Cond
	applied   int
	pending   map[int]chan *common.Response
}

func NewDistributed(kv *engine.KV, cfg DistributedConfig) (*DistributedCoordinator, error) {
	applyCh := make(chan ApplyMsg, 256)
	rf, err := NewRaft(cfg.NodeID, cfg.PeerAddrs, cfg.ListenAddr, cfg.PersistPath, applyCh)
	if err != nil {
		return nil, err
	}
	dc := &DistributedCoordinator{
		kv:      kv,
		raft:    rf,
		applyCh: applyCh,
		applied: 0,
		pending: make(map[int]chan *common.Response),
	}
	dc.applyCond = sync.NewCond(&dc.mu)
	go dc.applyLoop()
	return dc, nil
}

func (dc *DistributedCoordinator) Coordinate(ctx context.Context, req *common.Request) *common.Response {
	switch req.Op {
	case common.OpGet:
		idx, isLeader, err := dc.raft.ReadIndex(ctx)
		if !isLeader {
			return &common.Response{Err: common.ErrCoordNotLeader}
		}
		if err != nil {
			return &common.Response{Err: err}
		}
		if werr := dc.waitApplied(ctx, idx, 3*time.Second); werr != nil {
			return &common.Response{Err: werr}
		}
		val, err := dc.kv.Get(req.Key)
		return &common.Response{Data: val, Err: err}
	case common.OpSet, common.OpDel:
		cmd := RaftCommand{
			Op:    int(req.Op),
			Key:   append([]byte(nil), req.Key...),
			Value: append([]byte(nil), req.Value...),
		}
		index, _, isLeader := dc.raft.Start(cmd)
		if !isLeader {
			return &common.Response{Err: common.ErrCoordNotLeader}
		}
		waitCh := make(chan *common.Response, 1)
		dc.mu.Lock()
		dc.pending[index] = waitCh
		dc.mu.Unlock()

		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			dc.removePending(index)
			return &common.Response{Err: ctx.Err()}
		case <-timer.C:
			dc.removePending(index)
			return &common.Response{Err: common.ErrCoordApplyTimeout}
		case resp := <-waitCh:
			return resp
		}
	case common.OpPing:
		return &common.Response{Data: "PONG"}
	case common.OpCommand:
		return &common.Response{Data: "Kv storage: You can use GET SET or DEL to operate KV"}
	default:
		return &common.Response{Err: common.ErrCoordErrUnknownOp}
	}
}

func (dc *DistributedCoordinator) applyLoop() {
	for msg := range dc.applyCh {
		if !msg.CommandValid {
			continue
		}
		cmd, ok := msg.Command.(RaftCommand)
		if !ok {
			continue
		}
		resp := dc.applyCommand(cmd)
		dc.mu.Lock()
		if msg.CommandIndex > dc.applied {
			dc.applied = msg.CommandIndex
		}
		dc.applyCond.Broadcast()
		if ch, exists := dc.pending[msg.CommandIndex]; exists {
			delete(dc.pending, msg.CommandIndex)
			ch <- resp
			close(ch)
		}
		dc.mu.Unlock()
	}
}

func (dc *DistributedCoordinator) waitApplied(ctx context.Context, idx int, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		dc.mu.Lock()
		done := dc.applied >= idx
		dc.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return common.ErrCoordApplyTimeout
		case <-tick.C:
		}
	}
}

func (dc *DistributedCoordinator) applyCommand(cmd RaftCommand) *common.Response {
	switch common.OpType(cmd.Op) {
	case common.OpSet:
		return &common.Response{Err: dc.kv.Set(cmd.Key, cmd.Value)}
	case common.OpDel:
		return &common.Response{Err: dc.kv.Del(cmd.Key)}
	default:
		return &common.Response{Err: common.ErrCoordErrUnknownOp}
	}
}

func (dc *DistributedCoordinator) removePending(index int) {
	dc.mu.Lock()
	if ch, exists := dc.pending[index]; exists {
		delete(dc.pending, index)
		close(ch)
	}
	dc.mu.Unlock()
}
