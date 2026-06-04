package raftstore

import (
	"sync"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/raftstore/message"
	"github.com/Aetherance/kv/engine/raftstore/util"
	"github.com/Aetherance/kv/log"
	"github.com/Aetherance/kv/proto/pkg/metapb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
)

type StoreTick int

const (
	StoreTickSchedulerStoreHeartbeat StoreTick = 1
	StoreTickSnapGC                  StoreTick = 2
)

type storeState struct {
	id       uint64
	receiver <-chan message.Msg
	ticker   *ticker
}

func newStoreState(cfg *config.Config) (chan<- message.Msg, *storeState) {
	ch := make(chan message.Msg, 40960)
	state := &storeState{
		receiver: (<-chan message.Msg)(ch),
		ticker:   newStoreTicker(cfg),
	}
	return (chan<- message.Msg)(ch), state
}

// storeWorker handles store-level messages: routing inbound raft messages to
// peers (creating a peer on demand) and store ticks.
type storeWorker struct {
	*storeState
	ctx *GlobalContext
}

func newStoreWorker(ctx *GlobalContext, state *storeState) *storeWorker {
	return &storeWorker{
		storeState: state,
		ctx:        ctx,
	}
}

func (sw *storeWorker) run(closeCh <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		var msg message.Msg
		select {
		case <-closeCh:
			return
		case msg = <-sw.receiver:
		}
		sw.handleMsg(msg)
	}
}

func (d *storeWorker) handleMsg(msg message.Msg) {
	switch msg.Type {
	case message.MsgTypeStoreRaftMessage:
		if err := d.onRaftMessage(msg.Data.(*rspb.RaftMessage)); err != nil {
			log.Errorf("handle raft message failed storeID %d, %v", d.id, err)
		}
	case message.MsgTypeStoreTick:
		// Only SnapGC and scheduler heartbeat ticks reach here; both are no-ops
		// for the static, inline-snapshot cluster.
	case message.MsgTypeStoreStart:
		d.start(msg.Data.(*metapb.Store))
	}
}

func (d *storeWorker) start(store *metapb.Store) {
	d.id = store.Id
}

func (d *storeWorker) onRaftMessage(msg *rspb.RaftMessage) error {
	regionID := msg.RegionId
	if err := d.ctx.router.send(regionID, message.Msg{Type: message.MsgTypeRaftMessage, Data: msg}); err == nil {
		return nil
	}
	if msg.ToPeer.StoreId != d.ctx.store.Id {
		log.Warnf("store not match, ignore it. store_id:%d, to_store_id:%d, region_id:%d",
			d.ctx.store.Id, msg.ToPeer.StoreId, regionID)
		return nil
	}
	if msg.RegionEpoch == nil || msg.IsTombstone {
		return nil
	}
	created, err := d.maybeCreatePeer(regionID, msg)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	_ = d.ctx.router.send(regionID, message.Msg{Type: message.MsgTypeRaftMessage, Data: msg})
	return nil
}

// maybeCreatePeer lazily creates a peer for an incoming message. In the static
// cluster every node bootstraps its own region peer at startup, so this is only
// a safety net for messages that arrive before that registration is observed.
func (d *storeWorker) maybeCreatePeer(regionID uint64, msg *rspb.RaftMessage) (bool, error) {
	meta := d.ctx.storeMeta
	meta.Lock()
	defer meta.Unlock()
	if _, ok := meta.regions[regionID]; ok {
		return true, nil
	}
	if !util.IsInitialMsg(msg.Message) {
		return false, nil
	}
	peer, err := replicatePeer(d.ctx.store.Id, d.ctx.cfg, d.ctx.regionTaskSender, d.ctx.engine, regionID, msg.ToPeer)
	if err != nil {
		return false, err
	}
	meta.regions[regionID] = peer.Region()
	d.ctx.router.register(peer)
	_ = d.ctx.router.send(regionID, message.Msg{Type: message.MsgTypeStart})
	return true, nil
}
