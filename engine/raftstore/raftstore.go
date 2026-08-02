package raftstore

import (
	"bytes"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/raftstore/message"
	"github.com/Aetherance/kv/engine/raftstore/meta"
	"github.com/Aetherance/kv/engine/raftstore/runner"
	"github.com/Aetherance/kv/engine/raftstore/snap"
	engine_util "github.com/Aetherance/kv/engine/util"
	"github.com/Aetherance/kv/engine/util/worker"
	"github.com/Aetherance/kv/log"
	"github.com/Aetherance/kv/proto/pkg/metapb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
)

// Transport sends RaftMessages to other stores.
type Transport interface {
	Send(msg *rspb.RaftMessage) error
}

// storeMeta keeps the in-memory region metadata for this store. With a static
// single-region cluster the map holds exactly one region, but the structure is
// kept generic so the existing peer_msg_handler logic works unchanged.
type storeMeta struct {
	sync.RWMutex
	// region_id -> region
	regions map[uint64]*metapb.Region
}

func newStoreMeta() *storeMeta {
	return &storeMeta{
		regions: map[uint64]*metapb.Region{},
	}
}

// getOverlapRegions returns the regions whose key range overlaps the given one.
func (m *storeMeta) getOverlapRegions(region *metapb.Region) []*metapb.Region {
	var overlaps []*metapb.Region
	for _, r := range m.regions {
		if rangesOverlap(r, region) {
			overlaps = append(overlaps, r)
		}
	}
	return overlaps
}

// rangesOverlap reports whether two regions' [StartKey, EndKey) ranges intersect.
// An empty EndKey means the range extends to the end of the keyspace.
func rangesOverlap(a, b *metapb.Region) bool {
	return !engine_util.ExceedEndKey(b.StartKey, a.EndKey) &&
		!engine_util.ExceedEndKey(a.StartKey, b.EndKey)
}

// GlobalContext bundles the shared per-store dependencies.
type GlobalContext struct {
	cfg                 *config.Config
	engine              *engine_util.Engines
	store               *metapb.Store
	storeMeta           *storeMeta
	snapMgr             *snap.SnapManager
	router              *router
	trans               Transport
	regionTaskSender    chan<- worker.Task
	raftLogGCTaskSender chan<- worker.Task
	tickDriverSender    chan uint64
}

type workers struct {
	raftLogGCWorker *worker.Worker
	regionWorker    *worker.Worker
	wg              *sync.WaitGroup
}

type Raftstore struct {
	ctx        *GlobalContext
	storeState *storeState
	router     *router
	workers    *workers
	tickDriver *tickDriver
	closeCh    chan struct{}
	wg         *sync.WaitGroup
}

// loadPeers scans the kv engine for region states and creates a peer for each
// non-tombstone region belonging to this store.
func (bs *Raftstore) loadPeers() ([]*peer, error) {
	startKey := meta.RegionMetaMinKey
	endKey := meta.RegionMetaMaxKey
	ctx := bs.ctx
	kvEngine := ctx.engine.Kv
	storeID := ctx.store.Id

	var regionPeers []*peer
	t := time.Now()
	err := kvEngine.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek(startKey); it.Valid(); it.Next() {
			item := it.Item()
			if bytes.Compare(item.Key(), endKey) >= 0 {
				break
			}
			_, suffix, err := meta.DecodeRegionMetaKey(item.Key())
			if err != nil {
				return err
			}
			if suffix != meta.RegionStateSuffix {
				continue
			}
			val, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			localState := new(rspb.RegionLocalState)
			if err := proto.Unmarshal(val, localState); err != nil {
				return err
			}
			if localState.State == rspb.PeerState_Tombstone {
				continue
			}
			region := localState.Region
			peer, err := createPeer(storeID, ctx.cfg, ctx.regionTaskSender, ctx.engine, region)
			if err != nil {
				return err
			}
			ctx.storeMeta.regions[region.Id] = region
			regionPeers = append(regionPeers, peer)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	log.Infof("start store %d, region_count %d, takes %v", storeID, len(regionPeers), time.Since(t))
	return regionPeers, nil
}

func (bs *Raftstore) start(
	store *metapb.Store,
	cfg *config.Config,
	engines *engine_util.Engines,
	trans Transport,
	snapMgr *snap.SnapManager) error {
	wg := new(sync.WaitGroup)
	bs.workers = &workers{
		regionWorker:    worker.NewWorker("snapshot-worker", wg),
		raftLogGCWorker: worker.NewWorker("raft-gc-worker", wg),
		wg:              wg,
	}
	bs.ctx = &GlobalContext{
		cfg:                 cfg,
		engine:              engines,
		store:               store,
		storeMeta:           newStoreMeta(),
		snapMgr:             snapMgr,
		router:              bs.router,
		trans:               trans,
		regionTaskSender:    bs.workers.regionWorker.Sender(),
		raftLogGCTaskSender: bs.workers.raftLogGCWorker.Sender(),
		tickDriverSender:    bs.tickDriver.newRegionCh,
	}
	regionPeers, err := bs.loadPeers()
	if err != nil {
		return err
	}
	for _, peer := range regionPeers {
		bs.router.register(peer)
	}
	bs.startWorkers(regionPeers)
	return nil
}

func (bs *Raftstore) startWorkers(peers []*peer) {
	ctx := bs.ctx
	workers := bs.workers
	router := bs.router
	bs.wg.Add(2) // raftWorker, storeWorker
	rw := newRaftWorker(ctx, router)
	go rw.run(bs.closeCh, bs.wg)
	sw := newStoreWorker(ctx, bs.storeState)
	go sw.run(bs.closeCh, bs.wg)
	router.sendStore(message.Msg{Type: message.MsgTypeStoreStart, Data: ctx.store})
	for i := 0; i < len(peers); i++ {
		regionID := peers[i].regionId
		_ = router.send(regionID, message.Msg{RegionID: regionID, Type: message.MsgTypeStart})
	}
	engines := ctx.engine
	workers.regionWorker.Start(runner.NewRegionTaskHandler(engines))
	workers.raftLogGCWorker.Start(runner.NewRaftLogGCTaskHandler())
	go bs.tickDriver.run()
}

func (bs *Raftstore) shutDown() {
	close(bs.closeCh)
	bs.wg.Wait()
	bs.tickDriver.stop()
	if bs.workers == nil {
		return
	}
	workers := bs.workers
	bs.workers = nil
	workers.regionWorker.Stop()
	workers.raftLogGCWorker.Stop()
	workers.wg.Wait()
}

func CreateRaftstore(cfg *config.Config) (*RaftstoreRouter, *Raftstore) {
	storeSender, storeState := newStoreState()
	router := newRouter(storeSender)
	raftstore := &Raftstore{
		router:     router,
		storeState: storeState,
		tickDriver: newTickDriver(cfg.RaftBaseTickInterval, router),
		closeCh:    make(chan struct{}),
		wg:         new(sync.WaitGroup),
	}
	return NewRaftstoreRouter(router), raftstore
}
