package raftstore

import (
	"bytes"
	"errors"
	"sort"

	"github.com/dgraph-io/badger/v4"

	"github.com/Aetherance/kv/engine/raftstore/meta"
	engine_util "github.com/Aetherance/kv/engine/util"
	"github.com/Aetherance/kv/proto/pkg/metapb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
	eraftpb "github.com/Aetherance/kv/proto/pkg/raftpb"
)

const (
	InitEpochVer     uint64 = 1
	InitEpochConfVer uint64 = 1
)

func isRangeEmpty(engine *badger.DB, startKey, endKey []byte) (bool, error) {
	var hasData bool
	err := engine.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		it.Seek(startKey)
		if it.Valid() {
			item := it.Item()
			if bytes.Compare(item.Key(), endKey) < 0 {
				hasData = true
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return !hasData, nil
}

// BootstrapStore writes the StoreIdent for a fresh store. It errors if the
// engines already contain data.
func BootstrapStore(engines *engine_util.Engines, clusterID, storeID uint64) error {
	ident := new(rspb.StoreIdent)
	empty, err := isRangeEmpty(engines.Kv, meta.MinKey, meta.MaxKey)
	if err != nil {
		return err
	}
	if !empty {
		return errors.New("kv store is not empty and has already had data")
	}
	empty, err = isRangeEmpty(engines.Raft, meta.MinKey, meta.MaxKey)
	if err != nil {
		return err
	}
	if !empty {
		return errors.New("raft store is not empty and has already had data")
	}
	ident.ClusterId = clusterID
	ident.StoreId = storeID
	return engine_util.PutMeta(engines.Kv, meta.StoreIdentKey, ident)
}

// PrepareBootstrap creates the single fixed region that spans the whole
// keyspace, with one peer per store in the static cluster (peerID == storeID),
// and persists its initial states. Every node calls this with the same peer
// set so all nodes agree on the region membership.
func PrepareBootstrap(engines *engine_util.Engines, regionID uint64, peers map[uint64]string) (*metapb.Region, error) {
	region := &metapb.Region{
		Id:       regionID,
		StartKey: []byte{},
		EndKey:   []byte{},
		RegionEpoch: &metapb.RegionEpoch{
			Version: InitEpochVer,
			ConfVer: InitEpochConfVer,
		},
		Peers: buildPeers(peers),
	}
	if err := prepareBootstrapCluster(engines, region); err != nil {
		return nil, err
	}
	return region, nil
}

// buildPeers builds a deterministic peer list (sorted by store id) so that
// every node produces identical region metadata. peerID == storeID.
func buildPeers(peers map[uint64]string) []*metapb.Peer {
	ids := make([]uint64, 0, len(peers))
	for storeID := range peers {
		ids = append(ids, storeID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make([]*metapb.Peer, 0, len(ids))
	for _, storeID := range ids {
		result = append(result, &metapb.Peer{Id: storeID, StoreId: storeID})
	}
	return result
}

func prepareBootstrapCluster(engines *engine_util.Engines, region *metapb.Region) error {
	state := new(rspb.RegionLocalState)
	state.Region = region
	kvWB := new(engine_util.WriteBatch)
	kvWB.SetMeta(meta.RegionStateKey(region.Id), state)
	writeInitialApplyState(kvWB, region.Id)
	if err := engines.WriteKV(kvWB); err != nil {
		return err
	}
	raftWB := new(engine_util.WriteBatch)
	writeInitialRaftState(raftWB, region.Id)
	return engines.WriteRaft(raftWB)
}

func writeInitialApplyState(kvWB *engine_util.WriteBatch, regionID uint64) {
	applyState := &rspb.RaftApplyState{
		AppliedIndex: meta.RaftInitLogIndex,
		TruncatedState: &rspb.RaftTruncatedState{
			Index: meta.RaftInitLogIndex,
			Term:  meta.RaftInitLogTerm,
		},
	}
	kvWB.SetMeta(meta.ApplyStateKey(regionID), applyState)
}

func writeInitialRaftState(raftWB *engine_util.WriteBatch, regionID uint64) {
	raftState := &rspb.RaftLocalState{
		HardState: &eraftpb.HardState{
			Term:   meta.RaftInitLogTerm,
			Commit: meta.RaftInitLogIndex,
		},
		LastIndex: meta.RaftInitLogIndex,
		LastTerm:  meta.RaftInitLogTerm,
	}
	raftWB.SetMeta(meta.RaftStateKey(regionID), raftState)
}
