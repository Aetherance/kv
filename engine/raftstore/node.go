package raftstore

import (
	"fmt"

	"github.com/dgraph-io/badger/v4"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/raftstore/meta"
	engine_util "github.com/Aetherance/kv/engine/util"
	"github.com/Aetherance/kv/log"
	"github.com/Aetherance/kv/proto/pkg/metapb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
)

// ClusterID and RegionID are fixed for the static, single-region cluster.
const (
	ClusterID uint64 = 1
	RegionID  uint64 = 1
)

// Node bootstraps and runs the raftstore for a single store in a static cluster.
// There is no scheduler: store id, region id and the full peer set all come from
// the config, and every node independently bootstraps the same fixed region.
type Node struct {
	clusterID uint64
	store     *metapb.Store
	cfg       *config.Config
	system    *Raftstore
}

func NewNode(system *Raftstore, cfg *config.Config) *Node {
	return &Node{
		clusterID: ClusterID,
		store: &metapb.Store{
			Id: cfg.StoreID,
		},
		cfg:    cfg,
		system: system,
	}
}

func (n *Node) Start(engines *engine_util.Engines, trans Transport) error {
	storeID, err := n.checkStore(engines)
	if err != nil {
		return err
	}
	if storeID == 0 {
		// Fresh store: persist the identity from config.
		if err := BootstrapStore(engines, n.clusterID, n.cfg.StoreID); err != nil {
			return err
		}
		storeID = n.cfg.StoreID
	}
	n.store.Id = storeID

	// Bootstrap the fixed region on first start. On restart it is already
	// persisted and will be loaded by Raftstore.loadPeers.
	bootstrapped, err := regionBootstrapped(engines, RegionID)
	if err != nil {
		return err
	}
	if !bootstrapped {
		region, err := PrepareBootstrap(engines, RegionID, n.cfg.Peers)
		if err != nil {
			return err
		}
		log.Infof("bootstrap region %d on store %d: %s", RegionID, storeID, region)
	}

	log.Infof("start raft store node, storeID: %d", n.store.GetId())
	return n.system.start(n.store, n.cfg, engines, trans)
}

// checkStore returns the persisted store id, or 0 if the store is fresh.
func (n *Node) checkStore(engines *engine_util.Engines) (uint64, error) {
	ident := new(rspb.StoreIdent)
	err := engine_util.GetMeta(engines.Kv, meta.StoreIdentKey, ident)
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return 0, nil
		}
		return 0, err
	}
	if ident.ClusterId != n.clusterID {
		return 0, fmt.Errorf("cluster ID mismatch, local %d != configured %d", ident.ClusterId, n.clusterID)
	}
	if ident.StoreId != n.cfg.StoreID {
		return 0, fmt.Errorf("store ID mismatch, persisted %d != configured %d", ident.StoreId, n.cfg.StoreID)
	}
	return ident.StoreId, nil
}

// regionBootstrapped reports whether the region state already exists in the kv engine.
func regionBootstrapped(engines *engine_util.Engines, regionID uint64) (bool, error) {
	state := new(rspb.RegionLocalState)
	err := engine_util.GetMeta(engines.Kv, meta.RegionStateKey(regionID), state)
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (n *Node) Stop() {
	log.Infof("stop raft store thread, storeID: %d", n.store.GetId())
	n.system.shutDown()
}

func (n *Node) GetStoreID() uint64 {
	return n.store.GetId()
}
